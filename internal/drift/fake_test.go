package drift

import (
	"slices"
	"testing"
)

// fakeRepo is an in-memory Source that computes reachability over a real
// commit graph, so tests describe graphs instead of stubbing answers.
type fakeRepo struct {
	commits map[string]Commit
	prs     []PullRequest

	noCommitLookup bool // PullsForCommit returns nothing, like GitHub's indexing lag
	calls          map[string]int
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{commits: map[string]Commit{}, calls: map[string]int{}}
}

func (r *fakeRepo) commit(sha string, parents ...string) { r.commitMsg(sha, "", parents...) }

func (r *fakeRepo) commitMsg(sha, msg string, parents ...string) {
	r.commits[sha] = Commit{SHA: sha, Parents: parents, Message: msg}
}

func (r *fakeRepo) author(sha, login, name string) {
	c := r.commits[sha]
	c.AuthorLogin, c.AuthorName = login, name
	r.commits[sha] = c
}

func (r *fakeRepo) pr(p PullRequest) { r.prs = append(r.prs, p) }

// reach returns every commit reachable from sha, including sha.
func (r *fakeRepo) reach(sha string) (map[string]bool, error) {
	seen := map[string]bool{}
	stack := []string{sha}
	for len(stack) > 0 {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[s] {
			continue
		}
		c, ok := r.commits[s]
		if !ok {
			return nil, ErrNotFound
		}
		seen[s] = true
		stack = append(stack, c.Parents...)
	}
	return seen, nil
}

func (r *fakeRepo) diff(base, head string) (aheadSet, behindSet map[string]bool, err error) {
	b, err := r.reach(base)
	if err != nil {
		return nil, nil, err
	}
	h, err := r.reach(head)
	if err != nil {
		return nil, nil, err
	}
	aheadSet, behindSet = map[string]bool{}, map[string]bool{}
	for s := range h {
		if !b[s] {
			aheadSet[s] = true
		}
	}
	for s := range b {
		if !h[s] {
			behindSet[s] = true
		}
	}
	return aheadSet, behindSet, nil
}

func (r *fakeRepo) CompareStatus(base, head string) (string, error) {
	r.calls["CompareStatus"]++
	a, b, err := r.diff(base, head)
	if err != nil {
		return "", err
	}
	switch {
	case len(a) == 0 && len(b) == 0:
		return "identical", nil
	case len(b) == 0:
		return "ahead", nil
	case len(a) == 0:
		return "behind", nil
	}
	return "diverged", nil
}

func (r *fakeRepo) AheadCommits(base, head string) ([]Commit, error) {
	r.calls["AheadCommits"]++
	a, _, err := r.diff(base, head)
	if err != nil {
		return nil, err
	}
	var out []Commit
	for s := range a {
		out = append(out, r.commits[s])
	}
	slices.SortFunc(out, func(x, y Commit) int {
		if x.SHA < y.SHA {
			return -1
		}
		if x.SHA > y.SHA {
			return 1
		}
		return 0
	})
	return out, nil
}

func (r *fakeRepo) PullsForCommit(sha string) ([]PullRequest, error) {
	r.calls["PullsForCommit"]++
	if r.noCommitLookup {
		return nil, nil
	}
	var out []PullRequest
	for _, p := range r.prs {
		if p.MergeCommitSHA == sha {
			out = append(out, p)
			continue
		}
		if p.HeadSHA == "" {
			continue
		}
		head, err := r.reach(p.HeadSHA)
		if err != nil {
			continue
		}
		// Only a true merge commit has a base-side first parent to subtract;
		// squash and rebase merges keep the head commits associated.
		if mc, ok := r.commits[p.MergeCommitSHA]; ok && len(mc.Parents) > 1 {
			if first, err := r.reach(mc.Parents[0]); err == nil {
				for s := range first {
					delete(head, s)
				}
			}
		}
		if head[sha] {
			out = append(out, p)
		}
	}
	return out, nil
}

func (r *fakeRepo) Pull(number int) (PullRequest, error) {
	r.calls["Pull"]++
	for _, p := range r.prs {
		if p.Number == number {
			return p, nil
		}
	}
	return PullRequest{}, ErrNotFound
}

func (r *fakeRepo) ClosedPullsByBase(base string, page int) ([]PullRequest, error) {
	r.calls["ClosedPullsByBase"]++
	if page != 1 {
		return nil, nil
	}
	var out []PullRequest
	for _, p := range r.prs {
		if p.BaseRef == base {
			out = append(out, p)
		}
	}
	return out, nil
}

func (r *fakeRepo) ClosedPullsByHead(base, headRef string) ([]PullRequest, error) {
	r.calls["ClosedPullsByHead"]++
	var out []PullRequest
	for _, p := range r.prs {
		if p.BaseRef == base && p.HeadRef == headRef {
			out = append(out, p)
		}
	}
	return out, nil
}

func TestFakeRepoReachability(t *testing.T) {
	r := newFakeRepo()
	r.commit("m0")
	r.commit("m1", "m0")
	r.commit("f1", "m0")
	r.commit("d1", "m0", "f1")

	got, err := r.AheadCommits("m1", "d1")
	if err != nil {
		t.Fatal(err)
	}
	var shas []string
	for _, c := range got {
		shas = append(shas, c.SHA)
	}
	if !slices.Equal(shas, []string{"d1", "f1"}) {
		t.Errorf("AheadCommits = %v, want [d1 f1]", shas)
	}

	for _, tc := range []struct{ base, head, want string }{
		{"m1", "m0", "behind"},
		{"m0", "m1", "ahead"},
		{"m1", "d1", "diverged"},
		{"m1", "m1", "identical"},
	} {
		if s, err := r.CompareStatus(tc.base, tc.head); err != nil || s != tc.want {
			t.Errorf("CompareStatus(%s,%s) = %q, %v; want %q", tc.base, tc.head, s, err, tc.want)
		}
	}

	if _, err := r.AheadCommits("m1", "zzz"); err != ErrNotFound {
		t.Errorf("unknown sha error = %v, want ErrNotFound", err)
	}
}
