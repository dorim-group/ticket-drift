package drift

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"
)

var (
	t1 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 = t1.Add(time.Hour)
	t3 = t2.Add(time.Hour)
)

func runTarget(t *testing.T, r *fakeRepo, baseSHA, devTip string) TargetResult {
	t.Helper()
	d := testDetector(r)
	d.BaseSHA = baseSHA
	res, err := d.Target("dev", devTip)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func wantShipped(t *testing.T, res TargetResult, want ...any) {
	t.Helper()
	var got []any
	for _, s := range res.Shipped {
		got = append(got, s.PR.Number, s.MatchedBy)
	}
	if !slices.Equal(got, want) {
		t.Errorf("Shipped = %v, want %v", got, want)
	}
}

func wantLost(t *testing.T, res TargetResult, want ...int) {
	t.Helper()
	var got []int
	for _, p := range res.Lost {
		got = append(got, p.Number)
	}
	if !slices.Equal(got, want) {
		t.Errorf("Lost = %v, want %v", got, want)
	}
}

func TestTargetNothingAhead(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("m1", "m0")
	res := runTarget(t, r, "m1", "m0")
	if res.AheadBy != 0 || len(res.Lost) != 0 || len(res.Shipped) != 0 {
		t.Errorf("result = %+v, want empty", res)
	}
	if res.Target != "dev" || res.SHA != "m0" {
		t.Errorf("Target/SHA = %q/%q", res.Target, res.SHA)
	}
	for _, name := range []string{"PullsForCommit", "Pull", "ClosedPullsByBase", "ClosedPullsByHead"} {
		if r.calls[name] != 0 {
			t.Errorf("%s called %d times, want 0", name, r.calls[name])
		}
	}
}

func TestShippedByAncestor(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("f1", "m0")
	r.commit("d1", "m0", "f1")
	r.commit("m1", "m0", "f1")
	r.pr(PullRequest{Number: 1, BaseRef: "dev", HeadRef: "IT-1/x", HeadSHA: "f1", Merged: true, MergeCommitSHA: "d1", MergedAt: t1})
	res := runTarget(t, r, "m1", "d1")
	wantShipped(t, res, 1, ByAncestor)
	wantLost(t, res)
}

func TestLostWhenOnlyOnTarget(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("f1", "m0")
	r.commit("d1", "m0", "f1")
	r.pr(PullRequest{Number: 1, BaseRef: "dev", HeadRef: "IT-1/x", HeadSHA: "f1", Merged: true, MergeCommitSHA: "d1", MergedAt: t1})
	res := runTarget(t, r, "m0", "d1")
	wantShipped(t, res)
	wantLost(t, res, 1)
	if res.AheadBy != 2 {
		t.Errorf("AheadBy = %d, want 2", res.AheadBy)
	}
}

func TestShippedByHeadAfterSquashIntoMain(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("f1", "m0")
	r.commit("d1", "m0", "f1")
	r.commit("m1", "m0")
	r.pr(PullRequest{Number: 1, BaseRef: "dev", HeadRef: "IT-1/x", HeadSHA: "f1", Merged: true, MergeCommitSHA: "d1", MergedAt: t1})
	r.pr(PullRequest{Number: 2, BaseRef: "main", HeadRef: "IT-1/x", HeadSHA: "f1", Merged: true, MergeCommitSHA: "m1", MergedAt: t2})
	res := runTarget(t, r, "m1", "d1")
	wantShipped(t, res, 1, ByHead)
	wantLost(t, res)
}

func TestLostWhenBranchGainedCommitsAfterShipping(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("f1", "m0")
	r.commit("f2", "f1")
	r.commit("m1", "m0")
	r.commit("d1", "m0", "f2")
	r.pr(PullRequest{Number: 2, BaseRef: "main", HeadRef: "IT-1/x", HeadSHA: "f1", Merged: true, MergeCommitSHA: "m1", MergedAt: t1})
	r.pr(PullRequest{Number: 3, BaseRef: "dev", HeadRef: "IT-1/x", HeadSHA: "f2", Merged: true, MergeCommitSHA: "d1", MergedAt: t2})
	res := runTarget(t, r, "m1", "d1")
	wantShipped(t, res)
	wantLost(t, res, 3)
}

func TestRepeatMergesOfOneBranchJudgedSeparately(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("f1", "m0")
	r.commit("f2", "f1")
	r.commit("d1", "m0", "f1")
	r.commit("d2", "d1", "f2")
	r.commit("m1", "m0")
	r.pr(PullRequest{Number: 1, BaseRef: "dev", HeadRef: "IT-1/x", HeadSHA: "f1", Merged: true, MergeCommitSHA: "d1", MergedAt: t1})
	r.pr(PullRequest{Number: 2, BaseRef: "main", HeadRef: "IT-1/x", HeadSHA: "f1", Merged: true, MergeCommitSHA: "m1", MergedAt: t2})
	r.pr(PullRequest{Number: 3, BaseRef: "dev", HeadRef: "IT-1/x", HeadSHA: "f2", Merged: true, MergeCommitSHA: "d2", MergedAt: t3})
	res := runTarget(t, r, "m1", "d2")
	wantShipped(t, res, 1, ByHead)
	wantLost(t, res, 3)
}

func TestShippedByBranchAfterRebase(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("f1", "m0")
	r.commit("g1", "m0")
	r.commit("d1", "m0", "f1")
	r.commit("m1", "m0")
	r.pr(PullRequest{Number: 1, BaseRef: "dev", HeadRef: "IT-1/x", HeadSHA: "f1", Merged: true, MergeCommitSHA: "d1", MergedAt: t1})
	r.pr(PullRequest{Number: 2, BaseRef: "main", HeadRef: "IT-1/x", HeadSHA: "g1", Merged: true, MergeCommitSHA: "m1", MergedAt: t2})
	res := runTarget(t, r, "m1", "d1")
	wantShipped(t, res, 1, ByBranch)
	wantLost(t, res)
}

// siblingRepo: IT-1/x shipped to main by squash; IT-1/x-dev reached dev via a
// conflict-resolution merge. extraCommit adds a QA fix on the dev-suffixed branch.
func siblingRepo(extraCommit bool) *fakeRepo {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("m1", "m0")
	r.commit("f1", "m0")
	r.commit("h9", "m0")
	r.commit("d0", "m0")
	r.commit("x1", "f1", "d0")
	head := "x1"
	if extraCommit {
		r.commit("x2", "x1")
		head = "x2"
	}
	r.commit("d1", "d0", head)
	r.pr(PullRequest{Number: 9, BaseRef: "dev", HeadRef: "IT-9/z", HeadSHA: "h9", Merged: true, MergeCommitSHA: "d0", MergedAt: t1})
	r.pr(PullRequest{Number: 1, BaseRef: "dev", HeadRef: "IT-1/x-dev", HeadSHA: head, Merged: true, MergeCommitSHA: "d1", MergedAt: t2})
	r.pr(PullRequest{Number: 2, BaseRef: "main", HeadRef: "IT-1/x", HeadSHA: "f1", Merged: true, MergeCommitSHA: "m1", MergedAt: t3})
	return r
}

func TestShippedBySiblingWithNoOwnCommits(t *testing.T) {
	res := runTarget(t, siblingRepo(false), "m1", "d1")
	wantShipped(t, res, 1, BySibling)
	wantLost(t, res, 9)
}

func TestLostWhenSiblingBranchHasOwnCommit(t *testing.T) {
	res := runTarget(t, siblingRepo(true), "m1", "d1")
	wantShipped(t, res)
	wantLost(t, res, 1, 9)
}

func TestSiblingRuleNeedsAMergedMainPR(t *testing.T) {
	r := siblingRepo(false)
	for i := range r.prs {
		if r.prs[i].Number == 2 {
			r.prs[i].Merged = false
		}
	}
	res := runTarget(t, r, "m1", "d1")
	wantShipped(t, res)
	wantLost(t, res, 1, 9)
}

func TestGoneHeadCommitMeansLost(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("d1", "m0")
	r.pr(PullRequest{Number: 1, BaseRef: "dev", HeadRef: "IT-1/x", HeadSHA: "gone", Merged: true, MergeCommitSHA: "d1", MergedAt: t1})
	res := runTarget(t, r, "m0", "d1")
	wantLost(t, res, 1)
}

type tooManySource struct{ *fakeRepo }

func (tooManySource) AheadCommits(string, string) ([]Commit, error) { return nil, ErrTooManyCommits }

func TestTargetTooManyCommits(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("d1", "m0")
	d := testDetector(tooManySource{r})
	d.BaseSHA = "m0"
	_, err := d.Target("dev", "d1")
	if !errors.Is(err, ErrTooManyCommits) {
		t.Errorf("err = %v, want ErrTooManyCommits", err)
	}
}

// overLimitSource lists one commit more than MaxAhead for the target itself,
// as a Source that does not enforce the limit would.
type overLimitSource struct{ *fakeRepo }

func (overLimitSource) AheadCommits(string, string) ([]Commit, error) {
	out := make([]Commit, MaxAhead+1)
	for i := range out {
		out[i] = Commit{SHA: fmt.Sprintf("c%d", i)}
	}
	return out, nil
}

func TestTargetOwnListOverLimit(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("d1", "m0")
	d := testDetector(overLimitSource{r})
	d.BaseSHA = "m0"
	_, err := d.Target("dev", "d1")
	if !errors.Is(err, ErrTooManyCommits) {
		t.Errorf("err = %v, want ErrTooManyCommits", err)
	}
	if n := r.calls["PullsForCommit"]; n != 0 {
		t.Errorf("PullsForCommit calls = %d, want 0: nothing is walked", n)
	}
}

// The same branch merged into main at exactly the moment this PR was merged
// into the target counts as shipped: the rule is "at or after".
func TestShippedByBranchAtSameInstant(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("f1", "m0")
	r.commit("g1", "m0")
	r.commit("d1", "m0", "f1")
	r.commit("m1", "m0")
	r.pr(PullRequest{Number: 1, BaseRef: "dev", HeadRef: "IT-1/x", HeadSHA: "f1", Merged: true, MergeCommitSHA: "d1", MergedAt: t1})
	r.pr(PullRequest{Number: 2, BaseRef: "main", HeadRef: "IT-1/x", HeadSHA: "g1", Merged: true, MergeCommitSHA: "m1", MergedAt: t1})
	res := runTarget(t, r, "m1", "d1")
	wantShipped(t, res, 1, ByBranch)
	wantLost(t, res)
}

type closedByHeadTooMany struct{ *fakeRepo }

func (closedByHeadTooMany) ClosedPullsByHead(string, string) ([]PullRequest, error) {
	return nil, fmt.Errorf("github: list: %w", ErrTooManyCommits)
}

// A list of merged PRs that is too long to read means the head and branch
// rules cannot be evaluated: the PR is reported, the run goes on.
func TestClosedPullsByHeadTooManyMeansLost(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("f1", "m0")
	r.commit("d1", "m0", "f1")
	r.pr(PullRequest{Number: 1, BaseRef: "dev", HeadRef: "IT-1/x", HeadSHA: "f1", Merged: true, MergeCommitSHA: "d1", MergedAt: t1})
	d := testDetector(closedByHeadTooMany{r})
	d.BaseSHA = "m0"
	res, err := d.Target("dev", "d1")
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	wantShipped(t, res)
	wantLost(t, res, 1)
}

type brokenSource struct{ *fakeRepo }

func (brokenSource) CompareStatus(string, string) (string, error) {
	return "", errors.New("boom")
}

func TestTargetFailsOnUnexpectedSourceError(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("d1", "m0")
	r.pr(PullRequest{Number: 1, BaseRef: "dev", HeadRef: "IT-1/x", HeadSHA: "d1", Merged: true, MergeCommitSHA: "d1", MergedAt: t1})
	d := testDetector(brokenSource{r})
	d.BaseSHA = "m0"
	if _, err := d.Target("dev", "d1"); err == nil {
		t.Error("expected error")
	}
}

func TestClosedPullsByHeadCachedPerRef(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("f1", "m0")
	r.commit("d1", "m0", "f1")
	r.commit("f2", "m0")
	r.commit("d2", "d1", "f2")
	r.pr(PullRequest{Number: 1, BaseRef: "dev", HeadRef: "IT-1/x", HeadSHA: "f1", Merged: true, MergeCommitSHA: "d1", MergedAt: t1})
	r.pr(PullRequest{Number: 2, BaseRef: "dev", HeadRef: "IT-1/x", HeadSHA: "f2", Merged: true, MergeCommitSHA: "d2", MergedAt: t2})
	runTarget(t, r, "m0", "d2")
	// refs: IT-1/x plus its sibling candidates, each fetched once.
	want := 1 + len(SiblingCandidates("IT-1/x", testDetector(r).EnvWords))
	if got := r.calls["ClosedPullsByHead"]; got != want {
		t.Errorf("ClosedPullsByHead calls = %d, want %d", got, want)
	}
}

func TestCarrierPRsReportedForTheWalkedTarget(t *testing.T) {
	r := newFakeRepo()
	stageChain(r)
	r.commitMsg("d1", "Merge pull request #20 from o/stage", "m0", "s2")
	r.pr(PullRequest{Number: 20, BaseRef: "dev", HeadRef: "stage", HeadSHA: "s2", Merged: true, MergeCommitSHA: "d1"})
	res := runTarget(t, r, "m0", "d1")
	lost := []int{}
	for _, p := range res.Lost {
		lost = append(lost, p.Number)
	}
	slices.Sort(lost)
	if !slices.Equal(lost, []int{10, 11}) {
		t.Errorf("Lost = %v, want [10 11]", lost)
	}
	if res.Target != "dev" || !slices.Equal(res.Carriers, []string{"d1"}) {
		t.Errorf("Target = %q, Carriers = %v", res.Target, res.Carriers)
	}
}

// A PR from a long-lived branch reaches the shipped rules only when what it
// carried could not be listed; then only rule 1 may clear it.
func TestLongLivedHeadJudgedByRule1Only(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commitMsg("d1", "Stage (#9)", "m0")
	r.commit("m1", "m0")
	r.pr(PullRequest{Number: 9, BaseRef: "dev", HeadRef: "stage", HeadSHA: "gone", Merged: true, MergeCommitSHA: "d1", MergedAt: t1})
	r.pr(PullRequest{Number: 8, BaseRef: "main", HeadRef: "stage", HeadSHA: "gone", Merged: true, MergeCommitSHA: "m1", MergedAt: t2})
	res := runTarget(t, r, "m1", "d1")
	wantShipped(t, res)
	wantLost(t, res, 9)
	if n := r.calls["ClosedPullsByHead"]; n != 0 {
		t.Errorf("ClosedPullsByHead calls = %d, want 0", n)
	}
}

func TestSquashedCarrierPRsReportedForTheWalkedTarget(t *testing.T) {
	res := runTarget(t, squashedCarrierRepo("s2"), "m0", "d1")
	wantLost(t, res, 11, 10)
	if !slices.Equal(res.Carriers, []string{"d1"}) {
		t.Errorf("Carriers = %v, want [d1]", res.Carriers)
	}
}

// A generic branch name reused for unrelated work must not mark the old work
// shipped: rule 3 needs a ticket ID in the branch name.
func TestRule3SkippedForBranchWithoutTicket(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("h1", "m0")
	r.commitMsg("d1", "Merge pull request #1 from o/hotfix", "m0", "h1")
	r.commit("h2", "m0")
	r.commitMsg("m1", "Merge pull request #2 from o/hotfix", "m0", "h2")
	r.pr(PullRequest{Number: 1, BaseRef: "dev", HeadRef: "hotfix", HeadSHA: "h1", Merged: true, MergeCommitSHA: "d1", MergedAt: t1})
	r.pr(PullRequest{Number: 2, BaseRef: "main", HeadRef: "hotfix", HeadSHA: "h2", Merged: true, MergeCommitSHA: "m1", MergedAt: t2})
	res := runTarget(t, r, "m1", "d1")
	wantShipped(t, res)
	wantLost(t, res, 1)
}

func TestRule3NeedsTicketPattern(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("f1", "m0")
	r.commit("g1", "m0")
	r.commit("d1", "m0", "f1")
	r.commit("m1", "m0")
	r.pr(PullRequest{Number: 1, BaseRef: "dev", HeadRef: "IT-1/x", HeadSHA: "f1", Merged: true, MergeCommitSHA: "d1", MergedAt: t1})
	r.pr(PullRequest{Number: 2, BaseRef: "main", HeadRef: "IT-1/x", HeadSHA: "g1", Merged: true, MergeCommitSHA: "m1", MergedAt: t2})
	d := testDetector(r)
	d.BaseSHA, d.TicketPattern = "m1", nil
	res, err := d.Target("dev", "d1")
	if err != nil {
		t.Fatal(err)
	}
	wantShipped(t, res)
	wantLost(t, res, 1)
}

// ignoresHeadFilter answers ClosedPullsByHead with every PR it has, as GitHub
// would if it ever dropped the head= filter.
type ignoresHeadFilter struct{ *fakeRepo }

func (s ignoresHeadFilter) ClosedPullsByHead(string, string) ([]PullRequest, error) {
	return s.prs, nil
}

func TestClosedPullsByHeadRecheckedClientSide(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("f1", "m0")
	r.commit("d1", "m0", "f1")
	r.commit("g1", "m0")
	r.commit("m1", "m0", "g1")
	r.pr(PullRequest{Number: 1, BaseRef: "dev", HeadRef: "IT-1/x", HeadSHA: "f1", Merged: true, MergeCommitSHA: "d1", MergedAt: t1})
	r.pr(PullRequest{Number: 2, BaseRef: "main", HeadRef: "IT-2/y", HeadSHA: "g1", Merged: true, MergeCommitSHA: "m1", MergedAt: t2})
	r.pr(PullRequest{Number: 3, BaseRef: "main", HeadRef: "IT-1/x", HeadSHA: "f1", MergedAt: t2})
	d := testDetector(ignoresHeadFilter{r})
	d.BaseSHA = "m1"
	res, err := d.Target("dev", "d1")
	if err != nil {
		t.Fatal(err)
	}
	wantShipped(t, res)
	wantLost(t, res, 1)
}

var errSentinel = errors.New("sentinel")

type closedByHeadFails struct{ *fakeRepo }

func (closedByHeadFails) ClosedPullsByHead(string, string) ([]PullRequest, error) {
	return nil, fmt.Errorf("github: list: %w", errSentinel)
}

func TestTargetFailsWhenClosedPullsByHeadFails(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("f1", "m0")
	r.commit("d1", "m0", "f1")
	r.pr(PullRequest{Number: 1, BaseRef: "dev", HeadRef: "IT-1/x", HeadSHA: "f1", Merged: true, MergeCommitSHA: "d1", MergedAt: t1})
	d := testDetector(closedByHeadFails{r})
	d.BaseSHA = "m0"
	if _, err := d.Target("dev", "d1"); !errors.Is(err, errSentinel) {
		t.Errorf("err = %v, want the sentinel", err)
	}
}

// siblingAheadFails fails only rule 4's own-commits listing, which starts
// from the shipped sibling's head (f1 in siblingRepo).
type siblingAheadFails struct{ *fakeRepo }

func (s siblingAheadFails) AheadCommits(base, head string) ([]Commit, error) {
	if base == "f1" {
		return nil, fmt.Errorf("github: compare: %w", errSentinel)
	}
	return s.fakeRepo.AheadCommits(base, head)
}

func TestTargetFailsWhenSiblingRuleCompareFails(t *testing.T) {
	d := testDetector(siblingAheadFails{siblingRepo(false)})
	d.BaseSHA = "m1"
	if _, err := d.Target("dev", "d1"); !errors.Is(err, errSentinel) {
		t.Errorf("err = %v, want the sentinel", err)
	}
}
