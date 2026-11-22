package github

import "github.com/dorim-group/ticket-drift/internal/drift"

// RepoSource binds a Client to one repository. It implements drift.Source and
// adds the two ref operations with the same binding.
type RepoSource struct {
	C           *Client
	Owner, Repo string
}

func (s RepoSource) BranchSHA(branch string) (string, bool, error) {
	return s.C.BranchSHA(s.Owner, s.Repo, branch)
}

func (s RepoSource) UpdateRef(branch, sha string) error {
	return s.C.UpdateRef(s.Owner, s.Repo, branch, sha)
}

func (s RepoSource) CompareStatus(base, head string) (string, error) {
	return s.C.CompareStatus(s.Owner, s.Repo, base, head)
}

func (s RepoSource) AheadCommits(base, head string) ([]drift.Commit, error) {
	return s.C.AheadCommits(s.Owner, s.Repo, base, head)
}

func (s RepoSource) PullsForCommit(sha string) ([]drift.PullRequest, error) {
	return s.C.PullsForCommit(s.Owner, s.Repo, sha)
}

func (s RepoSource) Pull(number int) (drift.PullRequest, error) {
	return s.C.Pull(s.Owner, s.Repo, number)
}

func (s RepoSource) ClosedPullsByBase(base string, page int) ([]drift.PullRequest, error) {
	return s.C.ClosedPullsByBase(s.Owner, s.Repo, base, page)
}

func (s RepoSource) ClosedPullsByHead(base, headRef string) ([]drift.PullRequest, error) {
	return s.C.ClosedPullsByHead(s.Owner, s.Repo, base, headRef)
}
