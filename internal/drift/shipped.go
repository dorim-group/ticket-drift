package drift

import (
	"errors"
	"fmt"
)

type MatchedBy string

const (
	ByAncestor MatchedBy = "ancestor" // head commit is reachable from main
	ByHead     MatchedBy = "head"     // a PR merged into main from the same branch contains the head commit
	ByBranch   MatchedBy = "branch"   // the same ticket branch merged into main at or after this PR was merged
	BySibling  MatchedBy = "sibling"  // an env-suffixed sibling shipped and this PR has no commits of its own
)

type ShippedPR struct {
	PR        PullRequest
	MatchedBy MatchedBy
}

type TargetResult struct {
	Target       string
	SHA          string
	AheadBy      int
	Shipped      []ShippedPR
	Lost         []PullRequest
	BackMerges   []string
	Carriers     []string
	Unattributed []Commit
}

// Target reports what a reset of target (currently at sha) to d.BaseSHA would wipe.
func (d *Detector) Target(target, sha string) (TargetResult, error) {
	res := TargetResult{Target: target, SHA: sha}

	commits, err := d.Src.AheadCommits(d.BaseSHA, sha)
	if err != nil {
		return res, fmt.Errorf("drift: %s: %w", target, err)
	}
	if len(commits) > MaxAhead {
		return res, fmt.Errorf("drift: %s: %w", target, ErrTooManyCommits)
	}
	res.AheadBy = len(commits)
	if len(commits) == 0 {
		return res, nil
	}
	ahead := make(map[string]Commit, len(commits))
	for _, c := range commits {
		ahead[c.SHA] = c
	}

	w, err := d.walk(target, sha, ahead)
	if err != nil {
		return res, fmt.Errorf("drift: %s: %w", target, err)
	}
	res.BackMerges, res.Carriers, res.Unattributed = w.BackMerges, w.Carriers, w.Unattributed

	for _, pr := range w.PRs {
		how, ok, err := d.shippedBy(pr, ahead)
		if err != nil {
			return res, fmt.Errorf("drift: %s: #%d: %w", target, pr.Number, err)
		}
		if ok {
			res.Shipped = append(res.Shipped, ShippedPR{PR: pr, MatchedBy: how})
		} else {
			res.Lost = append(res.Lost, pr)
		}
	}
	return res, nil
}

// unknown reports an error that means "cannot tell", which counts as lost.
func unknown(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrTooManyCommits)
}

func (d *Detector) shippedBy(pr PullRequest, ahead map[string]Commit) (MatchedBy, bool, error) {
	status, err := d.Src.CompareStatus(d.BaseSHA, pr.HeadSHA)
	switch {
	case err == nil:
		if status == "behind" || status == "identical" {
			return ByAncestor, true, nil
		}
	case !unknown(err):
		return "", false, err
	}
	// Only reached when what a long-lived branch carried could not be listed;
	// its name says nothing about which of its versions reached the base.
	if d.longLived(pr.HeadRef) {
		return "", false, nil
	}

	merged, err := d.mergedIntoBase(pr.HeadRef)
	if err != nil {
		return "", false, err
	}
	for _, m := range merged {
		if m.HeadSHA == pr.HeadSHA {
			return ByHead, true, nil
		}
		status, err := d.Src.CompareStatus(pr.HeadSHA, m.HeadSHA)
		switch {
		case err == nil:
			if status == "ahead" || status == "identical" {
				return ByHead, true, nil
			}
		case !unknown(err):
			return "", false, err
		}
	}
	// A generic name such as "hotfix" is reused for unrelated work, so only a
	// branch named after a ticket is taken to be the same work rewritten.
	if d.TicketPattern != nil && len(TicketIDs(d.TicketPattern, pr.HeadRef)) > 0 {
		for _, m := range merged {
			if !m.MergedAt.Before(pr.MergedAt) {
				return ByBranch, true, nil
			}
		}
	}

	mergeCommit, ok := ahead[pr.MergeCommitSHA]
	if !ok || len(mergeCommit.Parents) == 0 {
		return "", false, nil
	}
	for _, cand := range SiblingCandidates(pr.HeadRef, d.EnvWords) {
		siblings, err := d.mergedIntoBase(cand)
		if err != nil {
			return "", false, err
		}
		for _, s := range siblings {
			own, err := d.hasOwnCommits(s, pr, mergeCommit)
			switch {
			case err == nil:
				if !own {
					return BySibling, true, nil
				}
			case !unknown(err):
				return "", false, err
			}
		}
	}
	return "", false, nil
}

// hasOwnCommits reports whether pr's head carries a non-merge commit that is
// neither in the shipped sibling nor just the target's own history merged in.
func (d *Detector) hasOwnCommits(s, pr PullRequest, mergeCommit Commit) (bool, error) {
	fromHead, err := d.Src.AheadCommits(s.HeadSHA, pr.HeadSHA)
	if err != nil {
		return false, err
	}
	fromBase, err := d.Src.AheadCommits(s.HeadSHA, mergeCommit.Parents[0])
	if err != nil {
		return false, err
	}
	inBase := make(map[string]bool, len(fromBase))
	for _, c := range fromBase {
		inBase[c.SHA] = true
	}
	for _, c := range fromHead {
		if len(c.Parents) == 1 && !inBase[c.SHA] {
			return true, nil
		}
	}
	return false, nil
}

// mergedIntoBase lists merged PRs into d.Base from headRef, cached per ref.
// A list that cannot be read in full counts as empty: the rules that need it
// do not hold, so the PR is reported.
func (d *Detector) mergedIntoBase(headRef string) ([]PullRequest, error) {
	if ps, ok := d.closedByHead[headRef]; ok {
		return ps, nil
	}
	all, err := d.Src.ClosedPullsByHead(d.Base, headRef)
	if err != nil && !unknown(err) {
		return nil, err
	}
	// Re-checked here: if the head filter were ever ignored, every PR into the
	// base would come back and mark nearly everything shipped.
	var merged []PullRequest
	for _, p := range all {
		if p.Merged && p.HeadRef == headRef && p.BaseRef == d.Base {
			merged = append(merged, p)
		}
	}
	if d.closedByHead == nil {
		d.closedByHead = map[string][]PullRequest{}
	}
	d.closedByHead[headRef] = merged
	return merged, nil
}
