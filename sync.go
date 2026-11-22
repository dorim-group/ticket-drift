package main

import (
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/dorim-group/ticket-drift/internal/clickup"
	"github.com/dorim-group/ticket-drift/internal/drift"
)

type repoAPI interface {
	drift.Source
	BranchSHA(branch string) (sha string, found bool, err error)
	UpdateRef(branch, sha string) error
}

type ticketLookup interface {
	Check() error
	TaskByCustomID(id string) (clickup.Task, error)
}

type notifier interface {
	Post(content string, mentionUserIDs []string) error
}

const (
	resetDone         = "done"
	resetNotNeeded    = "not_needed"
	resetSkipped      = "skipped"
	resetSuperseded   = "superseded"
	resetAbortedMoved = "aborted_moved"
	resetFailed       = "failed"
)

// Russian reasons for the correction message, keyed by reset outcome.
var correctionReasons = map[string]string{
	resetAbortedMoved: "в неё попали новые изменения во время проверки",
	resetFailed:       "GitHub отклонил обновление ветки",
}

// runSync performs every phase. The returned report is filled as far as the
// run got, even when err != nil.
func runSync(cfg config, gh repoAPI, cu ticketLookup, dc notifier, out io.Writer) (report, error) {
	rep := report{
		Version: version, Owner: cfg.Owner, Repo: cfg.Repo, Base: cfg.Base,
		DryRun: cfg.DryRun, NoReset: cfg.NoReset,
		Targets: []targetReport{}, Groups: []groupReport{},
	}

	// 1. Snapshot: everything after works on these pinned SHAs.
	baseSHA, found, err := gh.BranchSHA(cfg.Base)
	if err != nil {
		return rep, fmt.Errorf("sync: read %s: %w", cfg.Base, err)
	}
	if !found {
		return rep, fmt.Errorf("sync: base branch %q does not exist", cfg.Base)
	}
	rep.BaseSHA = baseSHA
	for _, name := range cfg.Targets {
		tr := newTargetReport(name)
		sha, found, err := gh.BranchSHA(name)
		if err != nil {
			return rep, fmt.Errorf("sync: read %s: %w", name, err)
		}
		tr.SHA, tr.Exists = sha, found
		tr.Reset = resetReport{resetSkipped, "branch does not exist"}
		if found {
			tr.Reset.Reason = "the run stopped before the reset"
		}
		rep.Targets = append(rep.Targets, tr)
	}

	// 2. Detect what a reset of each target would wipe.
	longLived := append([]string{cfg.Base}, cfg.Targets...)
	det := &drift.Detector{
		Src: gh, Base: cfg.Base, BaseSHA: baseSHA,
		LongLived: longLived, EnvWords: append(slices.Clone(longLived), "prod"),
		TicketPattern: cfg.Pattern,
	}
	var results []drift.TargetResult
	for i := range rep.Targets {
		tr := &rep.Targets[i]
		if !tr.Exists {
			continue
		}
		res, err := det.Target(tr.Name, tr.SHA)
		if err != nil {
			return rep, err
		}
		tr.fill(res)
		results = append(results, res)
	}

	// 3. Look every ticket up. ClickUp being unreachable stops the run before
	// anything is posted or reset; a ticket it does not know is still reported.
	groups := drift.Groups(results, cfg.Pattern)
	inputs := make([]renderInput, len(groups))
	for i, g := range groups {
		rep.Groups = append(rep.Groups, newGroupReport(g))
		inputs[i] = renderInput{
			Owner: cfg.Owner, Repo: cfg.Repo, Base: cfg.Base, Group: g,
			Users: cfg.Users, WillReset: !cfg.NoReset,
		}
	}
	// A lookup reads any 4xx as "no such ticket", so first prove the token and
	// team are good; otherwise a bad token would announce every ticket missing.
	if slices.ContainsFunc(groups, func(g drift.Group) bool { return g.Kind == drift.KindTicket }) {
		if err := cu.Check(); err != nil {
			return rep, fmt.Errorf("sync: %w", err)
		}
	}
	for i, g := range groups {
		if g.Kind != drift.KindTicket {
			continue
		}
		task, err := cu.TaskByCustomID(g.Key)
		switch {
		case errors.Is(err, clickup.ErrNotFound):
			inputs[i].TaskMissing = true
		case err != nil:
			return rep, fmt.Errorf("sync: look up %s: %w", g.Key, err)
		default:
			inputs[i].Task = &task
			rep.Groups[i].ClickUpFound, rep.Groups[i].ClickUpURL = true, task.URL
		}
	}

	// 4. Render and print.
	mentions := make([][]string, len(groups))
	for i := range groups {
		rep.Groups[i].Message, mentions[i] = renderGroup(inputs[i])
	}
	printRun(out, rep)

	// 5.
	if cfg.DryRun {
		for i := range rep.Targets {
			tr := &rep.Targets[i]
			if !tr.Exists {
				continue
			}
			tr.Reset = resetReport{resetSkipped, "dry run"}
			if tr.SHA != baseSHA {
				fmt.Fprintf(out, "would reset %s %s -> %s\n", tr.Name, shortSHA(tr.SHA), shortSHA(baseSHA))
			}
		}
		return rep, nil
	}

	// 6. Notify before resetting: a reset must never happen unannounced.
	for i, g := range rep.Groups {
		if err := dc.Post(g.Message, mentions[i]); err != nil {
			skipExisting(&rep, "a message was not delivered")
			return rep, fmt.Errorf("sync: post %s: %w", g.Key, err)
		}
		rep.Groups[i].Posted = true
	}

	// 7.
	if cfg.NoReset {
		skipExisting(&rep, "-no-reset")
		return rep, nil
	}

	// 8. Targets are independent: a failure on one does not stop the next.
	for i := range rep.Targets {
		tr := &rep.Targets[i]
		if !tr.Exists {
			continue
		}
		outcome, reason := resetTarget(gh, cfg.Base, baseSHA, tr.Name, tr.SHA)
		tr.Reset = resetReport{outcome, reason}
		if reason != "" {
			fmt.Fprintf(out, "reset %s: %s: %s\n", tr.Name, outcome, reason)
		} else {
			fmt.Fprintf(out, "reset %s: %s\n", tr.Name, outcome)
		}
	}

	// 9. Messages already said these targets were reset; take that back.
	var errs []error
	var done []string
	posted := 0
	for _, g := range rep.Groups {
		if g.Posted {
			posted++
		}
	}
	for _, tr := range rep.Targets {
		switch tr.Reset.Outcome {
		case resetDone:
			done = append(done, tr.Name)
			continue
		case resetAbortedMoved, resetFailed:
		default:
			continue
		}
		errs = append(errs, fmt.Errorf("sync: %s not reset: %s: %s", tr.Name, tr.Reset.Outcome, tr.Reset.Reason))
		if !announced(rep.Groups, tr.Name) {
			continue
		}
		msg := renderCorrection(cfg.Repo, tr.Name, cfg.Base, correctionReasons[tr.Reset.Outcome])
		if err := dc.Post(msg, nil); err != nil {
			errs = append(errs, fmt.Errorf("sync: post correction for %s: %w", tr.Name, err))
		}
	}

	// 10.
	if posted > 0 && len(done) > 0 {
		msg := renderSummary(cfg.Repo, done, cfg.Base, baseSHA, posted)
		if err := dc.Post(msg, nil); err != nil {
			errs = append(errs, fmt.Errorf("sync: post summary: %w", err))
		}
	}

	// 11.
	return rep, errors.Join(errs...)
}

// resetTarget applies the spec's phase 7 to one target.
func resetTarget(gh repoAPI, base, pinnedBase, target, snapshotSHA string) (outcome, reason string) {
	if snapshotSHA == pinnedBase {
		return resetNotNeeded, ""
	}
	tip, found, err := gh.BranchSHA(target)
	if err != nil {
		return resetFailed, "re-read the branch: " + err.Error()
	}
	if !found {
		return resetFailed, "the branch no longer exists"
	}
	update := func() (string, string) {
		if err := gh.UpdateRef(target, pinnedBase); err != nil {
			return resetFailed, err.Error()
		}
		return resetDone, ""
	}
	if tip == snapshotSHA {
		return update()
	}

	// The branch moved since the snapshot, normally because an overlapping
	// run for another merge to the base got there first.
	status, err := gh.CompareStatus(pinnedBase, tip)
	if err != nil {
		return resetFailed, "compare with the pinned " + base + ": " + err.Error()
	}
	if status == "identical" || status == "behind" {
		return update() // an earlier run reset it to an older base
	}
	live, found, err := gh.BranchSHA(base)
	if err != nil {
		return resetFailed, "re-read " + base + ": " + err.Error()
	}
	if !found {
		return resetFailed, base + " no longer exists"
	}
	status, err = gh.CompareStatus(live, tip)
	if err != nil {
		return resetFailed, "compare with the live " + base + ": " + err.Error()
	}
	if status == "identical" || status == "behind" {
		return resetSuperseded, fmt.Sprintf("already reset by a run for %s at %s", base, shortSHA(live))
	}
	return resetAbortedMoved, fmt.Sprintf("moved to %s, which has work not on %s", shortSHA(tip), base)
}

func skipExisting(rep *report, reason string) {
	for i := range rep.Targets {
		if rep.Targets[i].Exists {
			rep.Targets[i].Reset = resetReport{resetSkipped, reason}
		}
	}
}

// announced reports whether a posted message said target was reset.
func announced(groups []groupReport, target string) bool {
	for _, g := range groups {
		if g.Posted && slices.Contains(g.Targets, target) {
			return true
		}
	}
	return false
}

func printRun(out io.Writer, rep report) {
	fmt.Fprintf(out, "%s/%s: %s at %s\n", rep.Owner, rep.Repo, rep.Base, shortSHA(rep.BaseSHA))
	for _, tr := range rep.Targets {
		if !tr.Exists {
			fmt.Fprintf(out, "%s: branch does not exist\n", tr.Name)
			continue
		}
		fmt.Fprintf(out, "%s at %s: %d ahead, %d shipped, %d lost, %d unattributed\n",
			tr.Name, shortSHA(tr.SHA), tr.AheadBy, len(tr.Shipped), len(tr.Lost), len(tr.Unattributed))
		// A name-based match is the least certain; CI logs must show which rule cleared a PR.
		for _, p := range tr.Shipped {
			fmt.Fprintf(out, "  shipped #%d %s matched_by=%s\n", p.Number, p.HeadRef, p.MatchedBy)
		}
	}
	if len(rep.Groups) == 0 {
		fmt.Fprintln(out, "nothing lost, no messages")
		return
	}
	for i, g := range rep.Groups {
		fmt.Fprintf(out, "\n--- message %d of %d ---\n%s\n", i+1, len(rep.Groups), g.Message)
	}
	fmt.Fprintln(out)
}
