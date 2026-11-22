package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dorim-group/ticket-drift/internal/clickup"
	"github.com/dorim-group/ticket-drift/internal/drift"
)

const (
	mainSHA  = "aaaaaaa1111111111111111111111111111111111"
	main2SHA = "bbbbbbb2222222222222222222222222222222222"
)

// fakeGH answers drift.Source from scripted maps, so runSync's own Detector
// runs for real. Unknown compare keys answer "identical", others empty.
type fakeGH struct {
	branches     map[string]string
	status       map[string]string              // "base...head" -> status
	ahead        map[string][]drift.Commit      // "base...head" -> commits
	prsByCommit  map[string][]drift.PullRequest // commit SHA -> PRs
	closedByHead map[string][]drift.PullRequest // head ref -> PRs into base
	updates      []string                       // "branch=sha"
	failUpdate   map[string]error               // branch -> error
	events       *[]string                      // shared with fakeDC

	// afterSnapshot runs once, after the snapshotReads-th BranchSHA call.
	afterSnapshot func(*fakeGH)
	snapshotReads int
	reads         int
}

func newFakeGH(events *[]string) *fakeGH {
	return &fakeGH{
		branches:      map[string]string{"main": mainSHA},
		status:        map[string]string{},
		ahead:         map[string][]drift.Commit{},
		prsByCommit:   map[string][]drift.PullRequest{},
		closedByHead:  map[string][]drift.PullRequest{},
		failUpdate:    map[string]error{},
		events:        events,
		snapshotReads: 3, // main, dev, stage
	}
}

func (g *fakeGH) BranchSHA(branch string) (string, bool, error) {
	sha, ok := g.branches[branch]
	g.reads++
	if g.reads == g.snapshotReads && g.afterSnapshot != nil {
		hook := g.afterSnapshot
		g.afterSnapshot = nil
		hook(g)
	}
	return sha, ok, nil
}

func (g *fakeGH) UpdateRef(branch, sha string) error {
	if err := g.failUpdate[branch]; err != nil {
		return err
	}
	g.updates = append(g.updates, branch+"="+sha)
	g.branches[branch] = sha
	*g.events = append(*g.events, "update "+branch)
	return nil
}

func (g *fakeGH) CompareStatus(base, head string) (string, error) {
	if s, ok := g.status[base+"..."+head]; ok {
		return s, nil
	}
	return "identical", nil
}

func (g *fakeGH) AheadCommits(base, head string) ([]drift.Commit, error) {
	return g.ahead[base+"..."+head], nil
}

func (g *fakeGH) PullsForCommit(sha string) ([]drift.PullRequest, error) {
	return g.prsByCommit[sha], nil
}

func (g *fakeGH) Pull(int) (drift.PullRequest, error) {
	return drift.PullRequest{}, drift.ErrNotFound
}

func (g *fakeGH) ClosedPullsByBase(string, int) ([]drift.PullRequest, error) { return nil, nil }

func (g *fakeGH) ClosedPullsByHead(_, headRef string) ([]drift.PullRequest, error) {
	return g.closedByHead[headRef], nil
}

type lostPR struct {
	number      int
	headRef     string
	authorLogin string
}

// lose puts target on top of main as a chain of PR merge commits whose heads
// main does not contain, so every PR is lost.
func (g *fakeGH) lose(target string, prs ...lostPR) {
	base := g.branches["main"]
	prev := base
	var commits []drift.Commit
	for _, p := range prs {
		head := fmt.Sprintf("%s-head-%d", target, p.number)
		merge := fmt.Sprintf("%s-merge-%d", target, p.number)
		commits = append(commits,
			drift.Commit{SHA: merge, Parents: []string{prev, head}, Message: "merge " + p.headRef},
			drift.Commit{SHA: head, Parents: []string{base}, Message: "work"})
		g.prsByCommit[merge] = []drift.PullRequest{{
			Number: p.number, URL: fmt.Sprintf("https://github.com/o/r/pull/%d", p.number),
			HeadRef: p.headRef, HeadSHA: head, BaseRef: target, AuthorLogin: p.authorLogin,
			Merged: true, MergeCommitSHA: merge,
		}}
		g.status[base+"..."+head] = "ahead"
		prev = merge
	}
	g.branches[target] = prev
	g.ahead[base+"..."+prev] = commits
}

type fakeCU struct {
	tasks    map[string]clickup.Task
	errs     map[string]error
	calls    int
	checkErr error
	checks   int
}

func (c *fakeCU) Check() error {
	c.checks++
	return c.checkErr
}

func (c *fakeCU) TaskByCustomID(id string) (clickup.Task, error) {
	c.calls++
	if err := c.errs[id]; err != nil {
		return clickup.Task{}, err
	}
	if t, ok := c.tasks[id]; ok {
		return t, nil
	}
	return clickup.Task{}, clickup.ErrNotFound
}

type post struct {
	content string
	ids     []string
}

type fakeDC struct {
	posts    []post
	failOn   int // 1-based attempt that fails; 0 never
	attempts int
	events   *[]string
}

func (d *fakeDC) Post(content string, ids []string) error {
	d.attempts++
	if d.attempts == d.failOn {
		return errors.New("discord: status 500")
	}
	d.posts = append(d.posts, post{content, ids})
	*d.events = append(*d.events, "post "+strings.SplitN(content, "\n", 2)[0])
	return nil
}

type syncEnv struct {
	cfg    config
	gh     *fakeGH
	cu     *fakeCU
	dc     *fakeDC
	events *[]string
	out    bytes.Buffer
}

func newSyncEnv(t *testing.T) *syncEnv {
	t.Helper()
	re, err := drift.CompilePattern(`IT-\d+`)
	if err != nil {
		t.Fatal(err)
	}
	events := &[]string{}
	return &syncEnv{
		cfg: config{
			Owner: "o", Repo: "r", Base: "main", Targets: []string{"dev", "stage"},
			Pattern: re, Users: map[string]string{"ann": "111"},
		},
		gh: newFakeGH(events),
		cu: &fakeCU{tasks: map[string]clickup.Task{
			"IT-1": {CustomID: "IT-1", Name: "Task one", URL: "https://app.clickup.com/t/1", Status: "in dev"},
			"IT-2": {CustomID: "IT-2", Name: "Task two", URL: "https://app.clickup.com/t/2", Status: "in dev"},
		}},
		dc:     &fakeDC{events: events},
		events: events,
	}
}

func (e *syncEnv) run() (report, error) {
	return runSync(e.cfg, e.gh, e.cu, e.dc, &e.out)
}

func targetOf(t *testing.T, r report, name string) targetReport {
	t.Helper()
	for _, tr := range r.Targets {
		if tr.Name == name {
			return tr
		}
	}
	t.Fatalf("no target %q in report", name)
	return targetReport{}
}

func postsContaining(d *fakeDC, s string) int {
	n := 0
	for _, p := range d.posts {
		if strings.Contains(p.content, s) {
			n++
		}
	}
	return n
}

// postsBeforeUpdates checks that every group post precedes the first UpdateRef.
func postsBeforeUpdates(t *testing.T, events []string, groupPosts int) {
	t.Helper()
	first := slices.IndexFunc(events, func(e string) bool { return strings.HasPrefix(e, "update ") })
	if first < 0 {
		return
	}
	posts := 0
	for _, e := range events[:first] {
		if strings.HasPrefix(e, "post ") {
			posts++
		}
	}
	if posts != groupPosts {
		t.Errorf("%d group posts before the first update, want %d; events %q", posts, groupPosts, events)
	}
}

func TestSyncHappyPath(t *testing.T) {
	e := newSyncEnv(t)
	e.gh.lose("dev", lostPR{1, "IT-1/a", "ann"})
	e.gh.branches["stage"] = mainSHA

	r, err := e.run()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(e.dc.posts) != 2 {
		t.Fatalf("posts = %d, want 2: %q", len(e.dc.posts), e.dc.posts)
	}
	first := e.dc.posts[0]
	if !strings.HasPrefix(first.content, "**IT-1** · Task one\n") {
		t.Errorf("first post = %q", first.content)
	}
	if !reflect.DeepEqual(first.ids, []string{"111"}) {
		t.Errorf("first post mentions = %v, want [111]", first.ids)
	}
	wantSummary := "`r`: **dev** выровнен до main (`aaaaaaa`). Тикетов для повторного деплоя: 1."
	if got := e.dc.posts[1].content; got != wantSummary {
		t.Errorf("summary = %q, want %q", got, wantSummary)
	}
	if want := []string{"dev=" + mainSHA}; !reflect.DeepEqual(e.gh.updates, want) {
		t.Errorf("updates = %v, want %v", e.gh.updates, want)
	}
	if got := targetOf(t, r, "dev").Reset.Outcome; got != "done" {
		t.Errorf("dev reset = %q, want done", got)
	}
	if got := targetOf(t, r, "stage").Reset.Outcome; got != "not_needed" {
		t.Errorf("stage reset = %q, want not_needed", got)
	}
	postsBeforeUpdates(t, *e.events, 1)
	if e.cu.checks != 1 {
		t.Errorf("ClickUp checks = %d, want 1", e.cu.checks)
	}
	if !slices.Equal(*e.events, []string{"post **IT-1** · Task one", "update dev", "post " + wantSummary}) {
		t.Errorf("events = %q", *e.events)
	}

	dev := targetOf(t, r, "dev")
	if !dev.Exists || dev.SHA != "dev-merge-1" || dev.AheadBy != 2 || len(dev.Lost) != 1 || dev.Lost[0].Number != 1 {
		t.Errorf("dev report = %+v", dev)
	}
	if r.BaseSHA != mainSHA || len(r.Groups) != 1 || !r.Groups[0].Posted || !r.Groups[0].ClickUpFound ||
		r.Groups[0].Kind != "ticket" || r.Groups[0].Key != "IT-1" ||
		!reflect.DeepEqual(r.Groups[0].Targets, []string{"dev"}) || !reflect.DeepEqual(r.Groups[0].People, []string{"ann"}) {
		t.Errorf("report = %+v", r)
	}
}

func TestSyncNothingLostResetsSilently(t *testing.T) {
	e := newSyncEnv(t)
	e.gh.branches["dev"] = "0000000dev"
	e.gh.status[mainSHA+"...0000000dev"] = "behind"
	e.gh.branches["stage"] = mainSHA

	r, err := e.run()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(e.dc.posts) != 0 || e.cu.calls != 0 || e.cu.checks != 0 {
		t.Errorf("posts = %d, ClickUp calls = %d, checks = %d, want none", len(e.dc.posts), e.cu.calls, e.cu.checks)
	}
	if want := []string{"dev=" + mainSHA}; !reflect.DeepEqual(e.gh.updates, want) {
		t.Errorf("updates = %v, want %v", e.gh.updates, want)
	}
	if got := targetOf(t, r, "dev").Reset.Outcome; got != "done" {
		t.Errorf("dev reset = %q, want done", got)
	}
}

func TestSyncTargetMissingOrEqual(t *testing.T) {
	e := newSyncEnv(t)
	e.gh.branches["dev"] = mainSHA

	r, err := e.run()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(e.dc.posts) != 0 || e.cu.calls != 0 || len(e.gh.updates) != 0 {
		t.Errorf("posts = %d, ClickUp calls = %d, updates = %v; want none", len(e.dc.posts), e.cu.calls, e.gh.updates)
	}
	stage := targetOf(t, r, "stage")
	if stage.Exists || stage.Reset.Outcome != "skipped" || stage.Reset.Reason != "branch does not exist" {
		t.Errorf("stage = %+v", stage)
	}
	if got := targetOf(t, r, "dev").Reset.Outcome; got != "not_needed" {
		t.Errorf("dev reset = %q, want not_needed", got)
	}
}

func TestSyncDryRunChangesNothing(t *testing.T) {
	e := newSyncEnv(t)
	e.cfg.DryRun = true
	e.gh.lose("dev", lostPR{1, "IT-1/a", "ann"})
	e.gh.branches["stage"] = mainSHA

	r, err := e.run()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(e.dc.posts) != 0 || e.dc.attempts != 0 || len(e.gh.updates) != 0 {
		t.Errorf("post attempts = %d, updates = %v; want none", e.dc.attempts, e.gh.updates)
	}
	if e.cu.calls != 1 {
		t.Errorf("ClickUp calls = %d, want 1", e.cu.calls)
	}
	out := e.out.String()
	for _, want := range []string{"**IT-1** · Task one", "`r`: **dev** выровнен до main. Тикет нужно задеплоить заново.",
		"would reset dev dev-mer -> aaaaaaa"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "would reset stage") {
		t.Errorf("output resets stage, which equals main:\n%s", out)
	}
	for _, tr := range r.Targets {
		if tr.Reset.Outcome != "skipped" {
			t.Errorf("%s reset = %q, want skipped", tr.Name, tr.Reset.Outcome)
		}
	}
	if r.Groups[0].Posted || !r.DryRun {
		t.Errorf("report = %+v", r)
	}
}

func TestSyncNoReset(t *testing.T) {
	e := newSyncEnv(t)
	e.cfg.NoReset = true
	e.gh.lose("dev", lostPR{1, "IT-1/a", "ann"})
	e.gh.branches["stage"] = mainSHA

	r, err := e.run()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(e.dc.posts) != 1 || postsContaining(e.dc, "будет выровнен") != 1 {
		t.Errorf("posts = %q, want one containing «будет выровнен»", e.dc.posts)
	}
	if postsContaining(e.dc, "Тикетов для повторного деплоя") != 0 {
		t.Errorf("summary posted in -no-reset: %q", e.dc.posts)
	}
	if len(e.gh.updates) != 0 {
		t.Errorf("updates = %v, want none", e.gh.updates)
	}
	if got := targetOf(t, r, "dev").Reset.Outcome; got != "skipped" {
		t.Errorf("dev reset = %q, want skipped", got)
	}
}

func TestSyncClickUpDownStopsEverything(t *testing.T) {
	e := newSyncEnv(t)
	e.cu.errs = map[string]error{"IT-1": errors.New("503")}
	e.gh.lose("dev", lostPR{1, "IT-1/a", "ann"})
	e.gh.branches["stage"] = mainSHA

	if _, err := e.run(); err == nil {
		t.Fatal("err = nil, want the ClickUp error")
	}
	if e.dc.attempts != 0 || len(e.gh.updates) != 0 {
		t.Errorf("post attempts = %d, updates = %v; want none", e.dc.attempts, e.gh.updates)
	}
}

func TestSyncClickUpCheckFailureStopsEverything(t *testing.T) {
	e := newSyncEnv(t)
	e.cu.checkErr = errors.New("clickup: check token: status 401")
	e.gh.lose("dev", lostPR{1, "IT-1/a", "ann"})
	e.gh.branches["stage"] = mainSHA

	_, err := e.run()
	if err == nil || !strings.Contains(err.Error(), "status 401") {
		t.Fatalf("err = %v, want the ClickUp check error", err)
	}
	if e.dc.attempts != 0 || len(e.gh.updates) != 0 || e.cu.calls != 0 {
		t.Errorf("post attempts = %d, updates = %v, lookups = %d; want none", e.dc.attempts, e.gh.updates, e.cu.calls)
	}
}

// ClickUp is checked only when there is a ticket to look up.
func TestSyncBranchWithoutTicketSkipsClickUp(t *testing.T) {
	e := newSyncEnv(t)
	e.gh.lose("dev", lostPR{1, "hotfix", "ann"})
	e.gh.branches["stage"] = mainSHA

	if _, err := e.run(); err != nil {
		t.Fatalf("err = %v", err)
	}
	if e.cu.checks != 0 || e.cu.calls != 0 {
		t.Errorf("checks = %d, lookups = %d; want 0 and 0", e.cu.checks, e.cu.calls)
	}
	if postsContaining(e.dc, "hotfix") != 1 {
		t.Errorf("posts = %q, want one for hotfix", e.dc.posts)
	}
}

func TestSyncClickUpNotFoundStillPosts(t *testing.T) {
	e := newSyncEnv(t)
	delete(e.cu.tasks, "IT-1")
	e.gh.lose("dev", lostPR{1, "IT-1/a", "ann"})
	e.gh.branches["stage"] = mainSHA

	r, err := e.run()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if postsContaining(e.dc, "IT-1 не найден в ClickUp") != 1 {
		t.Errorf("posts = %q, want one with the not-found line", e.dc.posts)
	}
	if want := []string{"dev=" + mainSHA}; !reflect.DeepEqual(e.gh.updates, want) {
		t.Errorf("updates = %v, want %v", e.gh.updates, want)
	}
	if r.Groups[0].ClickUpFound || r.Groups[0].ClickUpURL != "" {
		t.Errorf("group = %+v", r.Groups[0])
	}
	postsBeforeUpdates(t, *e.events, 1)
}

func TestSyncDiscordFailureBlocksReset(t *testing.T) {
	e := newSyncEnv(t)
	e.dc.failOn = 2
	e.gh.lose("dev", lostPR{1, "IT-1/a", "ann"}, lostPR{2, "IT-2/b", "bob"})
	e.gh.branches["stage"] = mainSHA

	r, err := e.run()
	if err == nil {
		t.Fatal("err = nil, want the Discord error")
	}
	if len(e.gh.updates) != 0 {
		t.Errorf("updates = %v, want none", e.gh.updates)
	}
	var posted []bool
	for _, g := range r.Groups {
		posted = append(posted, g.Posted)
	}
	if !reflect.DeepEqual(posted, []bool{true, false}) {
		t.Errorf("posted = %v, want [true false]", posted)
	}
	if got := targetOf(t, r, "dev").Reset.Outcome; got != "skipped" {
		t.Errorf("dev reset = %q, want skipped", got)
	}
}

func TestSyncTargetMovedWithNewWork(t *testing.T) {
	e := newSyncEnv(t)
	e.gh.lose("dev", lostPR{1, "IT-1/a", "ann"})
	e.gh.branches["stage"] = mainSHA
	e.gh.afterSnapshot = func(g *fakeGH) {
		g.branches["dev"] = "dev-newer"
		g.status[mainSHA+"...dev-newer"] = "diverged"
	}

	r, err := e.run()
	if err == nil {
		t.Fatal("err = nil, want an error for the aborted reset")
	}
	if len(e.gh.updates) != 0 {
		t.Errorf("updates = %v, want none", e.gh.updates)
	}
	if got := targetOf(t, r, "dev").Reset.Outcome; got != "aborted_moved" {
		t.Errorf("dev reset = %q, want aborted_moved", got)
	}
	want := renderCorrection("r", "dev", "main", "в неё попали новые изменения во время проверки")
	if len(e.dc.posts) != 2 || e.dc.posts[1].content != want {
		t.Errorf("posts = %q, want the group then %q", e.dc.posts, want)
	}
	if postsContaining(e.dc, "ветка **dev** НЕ выровнена до main") != 1 {
		t.Errorf("no correction for dev in %q", e.dc.posts)
	}
}

func TestSyncTargetAlreadyResetByEarlierRun(t *testing.T) {
	e := newSyncEnv(t)
	e.gh.lose("dev", lostPR{1, "IT-1/a", "ann"})
	e.gh.branches["stage"] = mainSHA
	e.gh.afterSnapshot = func(g *fakeGH) {
		g.branches["dev"] = "0000000old"
		g.status[mainSHA+"...0000000old"] = "behind"
	}

	r, err := e.run()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if want := []string{"dev=" + mainSHA}; !reflect.DeepEqual(e.gh.updates, want) {
		t.Errorf("updates = %v, want %v", e.gh.updates, want)
	}
	if got := targetOf(t, r, "dev").Reset.Outcome; got != "done" {
		t.Errorf("dev reset = %q, want done", got)
	}
	if postsContaining(e.dc, "НЕ выровнена") != 0 {
		t.Errorf("unexpected correction in %q", e.dc.posts)
	}
}

func TestSyncSupersededByNewerRun(t *testing.T) {
	e := newSyncEnv(t)
	e.gh.lose("dev", lostPR{1, "IT-1/a", "ann"})
	e.gh.branches["stage"] = mainSHA
	e.gh.afterSnapshot = func(g *fakeGH) {
		g.branches["main"] = main2SHA
		g.branches["dev"] = main2SHA
		g.status[mainSHA+"..."+main2SHA] = "ahead"
		g.status[main2SHA+"..."+main2SHA] = "identical"
	}

	r, err := e.run()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(e.gh.updates) != 0 {
		t.Errorf("updates = %v, want none", e.gh.updates)
	}
	if got := targetOf(t, r, "dev").Reset.Outcome; got != "superseded" {
		t.Errorf("dev reset = %q, want superseded", got)
	}
	if postsContaining(e.dc, "НЕ выровнена") != 0 {
		t.Errorf("unexpected correction in %q", e.dc.posts)
	}
}

func TestSyncUpdateRefusedPostsCorrection(t *testing.T) {
	e := newSyncEnv(t)
	e.gh.lose("dev", lostPR{1, "IT-1/a", "ann"})
	e.gh.branches["stage"] = "0000000stg"
	e.gh.status[mainSHA+"...0000000stg"] = "behind"
	e.gh.failUpdate["dev"] = errors.New("github: PATCH refs/heads/dev: status 422")

	r, err := e.run()
	if err == nil {
		t.Fatal("err = nil, want an error for the refused reset")
	}
	dev := targetOf(t, r, "dev")
	if dev.Reset.Outcome != "failed" || !strings.Contains(dev.Reset.Reason, "status 422") {
		t.Errorf("dev reset = %+v, want failed with the GitHub error", dev.Reset)
	}
	want := renderCorrection("r", "dev", "main", "GitHub отклонил обновление ветки")
	if postsContaining(e.dc, want) != 1 {
		t.Errorf("posts = %q, want the correction %q", e.dc.posts, want)
	}
	if want := []string{"stage=" + mainSHA}; !reflect.DeepEqual(e.gh.updates, want) {
		t.Errorf("updates = %v, want %v", e.gh.updates, want)
	}
	if got := targetOf(t, r, "stage").Reset.Outcome; got != "done" {
		t.Errorf("stage reset = %q, want done", got)
	}
	postsBeforeUpdates(t, *e.events, 1)
}

// The same-branch rule must be on (runSync passes the ticket pattern), and
// every shipped PR is printed with the rule that cleared it.
func TestSyncPrintsMatchedBy(t *testing.T) {
	e := newSyncEnv(t)
	e.gh.lose("dev", lostPR{1, "IT-1/a", "ann"})
	e.gh.branches["stage"] = mainSHA
	e.gh.closedByHead["IT-1/a"] = []drift.PullRequest{{Number: 7, HeadRef: "IT-1/a", HeadSHA: "rebased", BaseRef: "main", Merged: true}}
	e.gh.status["dev-head-1...rebased"] = "diverged"

	r, err := e.run()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if dev := targetOf(t, r, "dev"); len(dev.Shipped) != 1 || dev.Shipped[0].MatchedBy != "branch" {
		t.Fatalf("dev shipped = %+v, want #1 by branch", dev.Shipped)
	}
	if want := "  shipped #1 IT-1/a matched_by=branch\n"; !strings.Contains(e.out.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, e.out.String())
	}
}

func TestReportJSONShape(t *testing.T) {
	e := newSyncEnv(t)
	e.gh.branches["dev"] = "0000000dev"
	e.gh.status[mainSHA+"...0000000dev"] = "behind"

	r, err := e.run()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{`"ticket_drift_version":`, `"lost":[]`, `"groups":[]`, `"shipped":[]`,
		`"back_merges":[]`, `"carriers":[]`, `"unattributed":[]`, `"base_sha":"` + mainSHA + `"`,
		`"reset":{"outcome":"done"`, `"exists":false`, `"ahead_by":0`, `"dry_run":false`, `"no_reset":false`} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON lacks %s:\n%s", want, s)
		}
	}
	if strings.Contains(s, "null") {
		t.Errorf("JSON contains null:\n%s", s)
	}
}
