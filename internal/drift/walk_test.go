package drift

import (
	"errors"
	"slices"
	"testing"
)

func testDetector(src Source) *Detector {
	return &Detector{
		Src:       src,
		Base:      "main",
		LongLived: []string{"main", "dev", "stage"},
		EnvWords:  []string{"main", "dev", "stage", "prod"},
	}
}

func runWalk(t *testing.T, r *fakeRepo, mainTip, devTip string) walkResult {
	t.Helper()
	ahead, err := r.AheadCommits(mainTip, devTip)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]Commit{}
	for _, c := range ahead {
		m[c.SHA] = c
	}
	res, err := testDetector(r).walk("dev", devTip, m)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func prNumbers(res walkResult) []int {
	var n []int
	for _, p := range res.PRs {
		n = append(n, p.Number)
	}
	return n
}

func wantNumbers(t *testing.T, res walkResult, want ...int) {
	t.Helper()
	got := prNumbers(res)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("PR numbers = %v, want %v", got, want)
	}
}

func wantNone(t *testing.T, res walkResult, backMerges, carriers, unattributed bool) {
	t.Helper()
	if backMerges && len(res.BackMerges) != 0 {
		t.Errorf("BackMerges = %v, want none", res.BackMerges)
	}
	if carriers && len(res.Carriers) != 0 {
		t.Errorf("Carriers = %v, want none", res.Carriers)
	}
	if unattributed && len(res.Unattributed) != 0 {
		t.Errorf("Unattributed = %v, want none", res.Unattributed)
	}
}

func mergePRRepo() *fakeRepo {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("f1", "m0")
	r.commitMsg("d1", "Merge pull request #1 from o/IT-1/x", "m0", "f1")
	r.pr(PullRequest{Number: 1, BaseRef: "dev", HeadRef: "IT-1/x", HeadSHA: "f1", Merged: true, MergeCommitSHA: "d1"})
	return r
}

func squashRepo(msg string) *fakeRepo {
	r := newFakeRepo()
	r.commit("m0")
	r.commitMsg("d1", msg, "m0")
	r.pr(PullRequest{Number: 2, BaseRef: "dev", HeadRef: "f9", HeadSHA: "f9", Merged: true, MergeCommitSHA: "d1"})
	return r
}

func TestWalkMergeCommitPR(t *testing.T) {
	res := runWalk(t, mergePRRepo(), "m0", "d1")
	wantNumbers(t, res, 1)
	wantNone(t, res, true, true, true)
}

func TestWalkSquashPR(t *testing.T) {
	res := runWalk(t, squashRepo("IT-2 thing (#2)"), "m0", "d1")
	wantNumbers(t, res, 2)
	wantNone(t, res, true, true, true)
}

func TestWalkFallsBackToMessageWhenLookupEmpty(t *testing.T) {
	r := mergePRRepo()
	r.noCommitLookup = true
	res := runWalk(t, r, "m0", "d1")
	wantNumbers(t, res, 1)
	if r.calls["Pull"] != 1 {
		t.Errorf("Pull calls = %d, want 1", r.calls["Pull"])
	}
}

func TestWalkFallsBackToListWhenMessageHasNoNumber(t *testing.T) {
	r := squashRepo("IT-2 thing")
	r.noCommitLookup = true
	res := runWalk(t, r, "m0", "d1")
	wantNumbers(t, res, 2)
	if r.calls["ClosedPullsByBase"] < 1 {
		t.Errorf("ClosedPullsByBase calls = %d, want >= 1", r.calls["ClosedPullsByBase"])
	}
}

func TestWalkRebaseMergeCountsPROnce(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("d1", "m0")
	r.commit("d2", "d1")
	r.pr(PullRequest{Number: 3, BaseRef: "dev", HeadRef: "IT-3/x", HeadSHA: "d2", Merged: true, MergeCommitSHA: "d2"})
	res := runWalk(t, r, "m0", "d2")
	if got := prNumbers(res); !slices.Equal(got, []int{3}) {
		t.Errorf("PR numbers = %v, want [3]", got)
	}
	wantNone(t, res, true, true, true)
}

func TestWalkBackMergeIgnored(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("m1", "m0")
	r.commit("m2", "m1")
	r.commitMsg("d0", "IT-1 thing (#1)", "m0")
	r.commitMsg("d1", "Merge branch 'main' into dev", "d0", "m1")
	r.pr(PullRequest{Number: 1, BaseRef: "dev", HeadRef: "IT-1/x", HeadSHA: "f9", Merged: true, MergeCommitSHA: "d0"})
	res := runWalk(t, r, "m2", "d1")
	if !slices.Equal(res.BackMerges, []string{"d1"}) {
		t.Errorf("BackMerges = %v, want [d1]", res.BackMerges)
	}
	wantNumbers(t, res, 1)
	wantNone(t, res, false, false, true)
}

func TestWalkDirectPushUnattributed(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("d1", "m0")
	r.author("d1", "ann", "Ann")
	res := runWalk(t, r, "m0", "d1")
	if len(res.Unattributed) != 1 || res.Unattributed[0].SHA != "d1" || res.Unattributed[0].AuthorLogin != "ann" {
		t.Errorf("Unattributed = %+v, want [d1 by ann]", res.Unattributed)
	}
	wantNone(t, res, true, true, false)
}

func TestWalkLocalFeatureMergeUnattributed(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("f1", "m0")
	r.commitMsg("d1", "Merge branch 'IT-9/x' into dev", "m0", "f1")
	res := runWalk(t, r, "m0", "d1")
	if len(res.Unattributed) != 1 || res.Unattributed[0].SHA != "d1" {
		t.Errorf("Unattributed = %+v, want [d1]", res.Unattributed)
	}
	wantNone(t, res, true, true, false)
}

// stageChain adds a stage branch with two merged PRs (#10, #11), tip s2.
func stageChain(r *fakeRepo) {
	r.commit("m0")
	r.commit("f1", "m0")
	r.commit("s1", "m0", "f1")
	r.commit("f2", "m0")
	r.commit("s2", "s1", "f2")
	r.pr(PullRequest{Number: 10, BaseRef: "stage", HeadRef: "IT-1/a", HeadSHA: "f1", Merged: true, MergeCommitSHA: "s1"})
	r.pr(PullRequest{Number: 11, BaseRef: "stage", HeadRef: "IT-2/b", HeadSHA: "f2", Merged: true, MergeCommitSHA: "s2"})
}

func TestWalkDescendsIntoCarrierPR(t *testing.T) {
	r := newFakeRepo()
	stageChain(r)
	r.commitMsg("d1", "Merge pull request #20 from o/stage", "m0", "s2")
	r.pr(PullRequest{Number: 20, BaseRef: "dev", HeadRef: "stage", HeadSHA: "s2", Merged: true, MergeCommitSHA: "d1"})
	res := runWalk(t, r, "m0", "d1")
	wantNumbers(t, res, 10, 11)
	if !slices.Equal(res.Carriers, []string{"d1"}) {
		t.Errorf("Carriers = %v, want [d1]", res.Carriers)
	}
	wantNone(t, res, true, false, true)
}

func TestWalkDescendsIntoDirectLongLivedMerge(t *testing.T) {
	r := newFakeRepo()
	stageChain(r)
	r.commitMsg("d1", "Merge branch 'stage' into dev", "m0", "s2")
	res := runWalk(t, r, "m0", "d1")
	wantNumbers(t, res, 10, 11)
	if !slices.Equal(res.Carriers, []string{"d1"}) {
		t.Errorf("Carriers = %v, want [d1]", res.Carriers)
	}
	wantNone(t, res, true, false, true)
}

func TestWalkNeverVisitsTwice(t *testing.T) {
	r := newFakeRepo()
	stageChain(r)
	r.commitMsg("d1", "Merge branch 'stage' into dev", "m0", "s2")
	r.commitMsg("d2", "Merge branch 'stage' into dev", "d1", "s2")
	res := runWalk(t, r, "m0", "d2")
	wantNumbers(t, res, 10, 11)
	if !slices.Equal(res.Carriers, []string{"d2", "d1"}) {
		t.Errorf("Carriers = %v, want [d2 d1]", res.Carriers)
	}
}

type failingSource struct{ Source }

func (failingSource) PullsForCommit(string) ([]PullRequest, error) {
	return nil, errors.New("boom")
}

func TestWalkSourceErrorAborts(t *testing.T) {
	r := mergePRRepo()
	ahead, _ := r.AheadCommits("m0", "d1")
	m := map[string]Commit{}
	for _, c := range ahead {
		m[c.SHA] = c
	}
	_, err := testDetector(failingSource{r}).walk("dev", "d1", m)
	if err == nil || err.Error() != "boom" {
		t.Errorf("err = %v, want boom", err)
	}
}
