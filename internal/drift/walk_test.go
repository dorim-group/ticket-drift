package drift

import (
	"errors"
	"regexp"
	"slices"
	"testing"
)

func testDetector(src Source) *Detector {
	return &Detector{
		Src:           src,
		Base:          "main",
		LongLived:     []string{"main", "dev", "stage"},
		EnvWords:      []string{"main", "dev", "stage", "prod"},
		TicketPattern: regexp.MustCompile(`(?i)IT-\d+`),
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
	d := testDetector(r)
	d.BaseSHA = mainTip
	res, err := d.walk("dev", devTip, m)
	if err != nil {
		t.Fatal(err)
	}
	checkAccounted(t, r, m, devTip, res)
	return res
}

// checkAccounted asserts the walk's invariant: every ahead commit on a
// first-parent chain reachable from tip through the descents is in exactly
// one result list, or attributed to a PR in res.PRs. The descents are derived
// here from the graph: every parent after the first that is in the ahead set,
// except a reported PR's own head; and the head of a single-parent carrier.
func checkAccounted(t *testing.T, r *fakeRepo, ahead map[string]Commit, tip string, res walkResult) {
	t.Helper()
	reported := map[int]PullRequest{}
	for _, p := range res.PRs {
		reported[p.Number] = p
	}
	count := map[string]int{}
	for _, s := range res.BackMerges {
		count[s]++
	}
	for _, s := range res.Carriers {
		count[s]++
	}
	for _, c := range res.Unattributed {
		count[c.SHA]++
	}
	for sha, n := range res.attributed {
		if _, ok := reported[n]; ok {
			count[sha]++
		}
	}

	seen := map[string]bool{}
	starts := []string{tip}
	for len(starts) > 0 {
		sha := starts[len(starts)-1]
		starts = starts[:len(starts)-1]
		for {
			c, ok := ahead[sha]
			if !ok || seen[sha] {
				break
			}
			seen[sha] = true
			if count[sha] != 1 {
				t.Errorf("commit %s accounted for %d times, want 1", sha, count[sha])
			}
			n, attributed := res.attributed[sha]
			pr, isPR := reported[n]
			isPR = attributed && isPR
			for _, p := range c.Parents[min(1, len(c.Parents)):] {
				if _, in := ahead[p]; in && !(isPR && p == pr.HeadSHA) {
					starts = append(starts, p)
				}
			}
			if attributed && len(c.Parents) == 1 && slices.Contains(res.Carriers, sha) {
				for _, p := range r.prs {
					if p.Number == n {
						starts = append(starts, p.HeadSHA)
					}
				}
			}
			if len(c.Parents) == 0 {
				break
			}
			sha = c.Parents[0]
		}
	}
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
	var got []string
	for _, c := range res.Unattributed {
		got = append(got, c.SHA)
	}
	// The merge and the feature's own commit: neither came through a PR.
	if !slices.Equal(got, []string{"d1", "f1"}) {
		t.Errorf("Unattributed = %v, want [d1 f1]", got)
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

// fastForwardRepo: dev holds squash PRs #1 and #2; a feature branch IT-5/foo
// merged dev into itself (x1) and dev was then fast-forwarded to x1, so the
// old dev chain hangs off x1's second parent.
func fastForwardRepo(x1Msg string) *fakeRepo {
	r := newFakeRepo()
	r.commit("m0")
	r.commitMsg("d1", "IT-1 one (#1)", "m0")
	r.commitMsg("d2", "IT-2 two (#2)", "d1")
	r.commit("f1", "m0")
	r.commitMsg("x1", x1Msg, "f1", "d2")
	r.pr(PullRequest{Number: 1, BaseRef: "dev", HeadRef: "IT-1/a", HeadSHA: "h1", Merged: true, MergeCommitSHA: "d1"})
	r.pr(PullRequest{Number: 2, BaseRef: "dev", HeadRef: "IT-2/b", HeadSHA: "h2", Merged: true, MergeCommitSHA: "d2"})
	return r
}

func TestWalkFastForwardedTargetWithPR(t *testing.T) {
	r := fastForwardRepo("Merge branch 'dev' into IT-5/foo")
	r.pr(PullRequest{Number: 5, BaseRef: "dev", HeadRef: "IT-5/foo", HeadSHA: "x1", Merged: true, MergeCommitSHA: "x1"})
	res := runWalk(t, r, "m0", "x1")
	wantNumbers(t, res, 1, 2, 5)
	wantNone(t, res, true, true, false)
}

func TestWalkFastForwardedTargetEditedMessage(t *testing.T) {
	res := runWalk(t, fastForwardRepo("sync with dev"), "m0", "x1")
	wantNumbers(t, res, 1, 2)
	if !slices.ContainsFunc(res.Unattributed, func(c Commit) bool { return c.SHA == "x1" }) {
		t.Errorf("Unattributed = %+v, want x1 among them", res.Unattributed)
	}
	wantNone(t, res, true, true, false)
}

func TestWalkOctopusMerge(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("m1", "m0")
	r.commitMsg("w1", "IT-3 thing (#3)", "m0")
	r.commitMsg("d1", "Merge branches 'main' and 'work' into dev", "m0", "m1", "w1")
	r.pr(PullRequest{Number: 3, BaseRef: "dev", HeadRef: "IT-3/x", HeadSHA: "h3", Merged: true, MergeCommitSHA: "w1"})
	res := runWalk(t, r, "m1", "d1")
	wantNumbers(t, res, 3)
	if slices.Contains(res.BackMerges, "d1") {
		t.Errorf("BackMerges = %v, d1 must not be a back-merge", res.BackMerges)
	}
	if len(res.Unattributed) != 1 || res.Unattributed[0].SHA != "d1" {
		t.Errorf("Unattributed = %+v, want [d1]", res.Unattributed)
	}
}

// squashedCarrierRepo: stage holds PRs #10 and #11; dev gets d1, a
// single-parent squash of PR #9 from stage@s2.
func squashedCarrierRepo(headSHA string) *fakeRepo {
	r := newFakeRepo()
	stageChain(r)
	r.commitMsg("d1", "Stage (#9)", "m0")
	r.pr(PullRequest{Number: 9, BaseRef: "dev", HeadRef: "stage", HeadSHA: headSHA, Merged: true, MergeCommitSHA: "d1"})
	return r
}

func TestWalkExpandsSquashedCarrier(t *testing.T) {
	res := runWalk(t, squashedCarrierRepo("s2"), "m0", "d1")
	wantNumbers(t, res, 10, 11)
	if !slices.Equal(res.Carriers, []string{"d1"}) {
		t.Errorf("Carriers = %v, want [d1]", res.Carriers)
	}
	wantNone(t, res, true, false, true)
}

func TestWalkSquashedCarrierAlreadyOnMain(t *testing.T) {
	r := squashedCarrierRepo("s2")
	r.commit("m1", "m0", "s2")
	res := runWalk(t, r, "m1", "d1")
	if !slices.Equal(res.BackMerges, []string{"d1"}) {
		t.Errorf("BackMerges = %v, want [d1]", res.BackMerges)
	}
	wantNumbers(t, res)
	wantNone(t, res, false, true, true)
}

// When what the squash carried cannot be listed, the PR itself is judged so
// that it is still reported.
func TestWalkSquashedCarrierUnknownHeadKeepsPR(t *testing.T) {
	res := runWalk(t, squashedCarrierRepo("gone"), "m0", "d1")
	wantNumbers(t, res, 9)
	wantNone(t, res, true, true, true)
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

func TestWalkBackMergeViaPRIgnored(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("m1", "m0")
	r.commitMsg("d0", "IT-1 thing (#1)", "m0")
	r.commitMsg("d1", "Merge pull request #2 from o/main", "d0", "m1")
	r.pr(PullRequest{Number: 1, BaseRef: "dev", HeadRef: "IT-1/x", HeadSHA: "f9", Merged: true, MergeCommitSHA: "d0"})
	r.pr(PullRequest{Number: 2, BaseRef: "dev", HeadRef: "main", HeadSHA: "m1", Merged: true, MergeCommitSHA: "d1"})
	res := runWalk(t, r, "m1", "d1")
	if !slices.Equal(res.BackMerges, []string{"d1"}) {
		t.Errorf("BackMerges = %v, want [d1]", res.BackMerges)
	}
	wantNumbers(t, res, 1)
	wantNone(t, res, false, true, true)
}
