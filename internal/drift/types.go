package drift

import (
	"errors"
	"regexp"
	"time"
)

type Commit struct {
	SHA         string
	Parents     []string
	Message     string
	AuthorLogin string // GitHub login; "" when GitHub knows no account
	AuthorName  string // git author name
}

type PullRequest struct {
	Number         int
	Title          string
	URL            string
	HeadRef        string
	HeadSHA        string
	BaseRef        string
	AuthorLogin    string // "" when the account was deleted
	Merged         bool
	MergedAt       time.Time
	MergeCommitSHA string
}

// Source is everything detection needs from GitHub.
type Source interface {
	// CompareStatus reports how head relates to base:
	// "ahead", "behind", "identical" or "diverged".
	CompareStatus(base, head string) (string, error)
	// AheadCommits lists the commits reachable from head and not from base.
	AheadCommits(base, head string) ([]Commit, error)
	PullsForCommit(sha string) ([]PullRequest, error)
	Pull(number int) (PullRequest, error)
	// ClosedPullsByBase pages newest-updated first, 100 per page, from page 1;
	// an empty page means there are no more.
	ClosedPullsByBase(base string, page int) ([]PullRequest, error)
	ClosedPullsByHead(base, headRef string) ([]PullRequest, error)
}

var (
	ErrNotFound       = errors.New("drift: not found")        // a SHA or PR GitHub does not have
	ErrTooManyCommits = errors.New("drift: too many commits") // more than MaxAhead
)

const MaxAhead = 2000

type Detector struct {
	Src       Source
	Base      string   // "main"
	BaseSHA   string   // pinned tip of Base
	LongLived []string // Base plus every target
	EnvWords  []string // LongLived plus "prod"

	// TicketPattern finds ticket IDs in branch names. The same-branch rule
	// applies only to branches that carry one; nil disables that rule.
	TicketPattern *regexp.Regexp

	// closedByHead caches merged PRs into Base, per head ref. It is written
	// without a lock, so one Detector must not run Target concurrently.
	closedByHead map[string][]PullRequest
}
