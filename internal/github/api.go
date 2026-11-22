package github

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dorim-group/ticket-drift/internal/drift"
)

func repoPath(owner, repo string) string {
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
}

// escRef escapes each segment of a branch name but keeps the slashes, which
// GitHub expects literally in ref and compare paths.
func escRef(ref string) string {
	parts := strings.Split(ref, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func isStatus(err error, statuses ...int) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	for _, s := range statuses {
		if apiErr.Status == s {
			return true
		}
	}
	return false
}

// notFound turns a 404 into drift.ErrNotFound, keeping the path in the message.
func notFound(err error) error {
	if isStatus(err, http.StatusNotFound) {
		var apiErr *APIError
		errors.As(err, &apiErr)
		return fmt.Errorf("github: %s %s: %w", apiErr.Method, apiErr.Path, drift.ErrNotFound)
	}
	return err
}

func (c *Client) BranchSHA(owner, repo, branch string) (string, bool, error) {
	var out struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	err := c.do("GET", repoPath(owner, repo)+"/git/ref/heads/"+escRef(branch), nil, &out)
	if isStatus(err, http.StatusNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return out.Object.SHA, true, nil
}

// Always force: the reset target is meant to be overwritten, even when it has
// diverged from the new tip.
func (c *Client) UpdateRef(owner, repo, branch, sha string) error {
	body := struct {
		SHA   string `json:"sha"`
		Force bool   `json:"force"`
	}{sha, true}
	return c.do("PATCH", repoPath(owner, repo)+"/git/refs/heads/"+escRef(branch), body, nil)
}

type compareResponse struct {
	Status       string `json:"status"`
	TotalCommits int    `json:"total_commits"`
	Commits      []struct {
		SHA     string `json:"sha"`
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
		Commit struct {
			Message string `json:"message"`
			Author  struct {
				Name string `json:"name"`
			} `json:"author"`
		} `json:"commit"`
		Author *struct {
			Login string `json:"login"`
		} `json:"author"`
	} `json:"commits"`
}

func comparePath(owner, repo, base, head string) string {
	return repoPath(owner, repo) + "/compare/" + escRef(base) + "..." + escRef(head)
}

func (c *Client) CompareStatus(owner, repo, base, head string) (string, error) {
	var out compareResponse
	if err := c.do("GET", comparePath(owner, repo, base, head)+"?per_page=1", nil, &out); err != nil {
		return "", notFound(err)
	}
	return out.Status, nil
}

func (c *Client) AheadCommits(owner, repo, base, head string) ([]drift.Commit, error) {
	var all []drift.Commit
	for page := 1; ; page++ {
		var out compareResponse
		path := fmt.Sprintf("%s?per_page=100&page=%d", comparePath(owner, repo, base, head), page)
		if err := c.do("GET", path, nil, &out); err != nil {
			return nil, notFound(err)
		}
		if out.TotalCommits > drift.MaxAhead {
			return nil, fmt.Errorf("github: %s...%s is %d commits ahead: %w",
				base, head, out.TotalCommits, drift.ErrTooManyCommits)
		}
		for _, cm := range out.Commits {
			d := drift.Commit{
				SHA:        cm.SHA,
				Message:    cm.Commit.Message,
				AuthorName: cm.Commit.Author.Name,
			}
			for _, p := range cm.Parents {
				d.Parents = append(d.Parents, p.SHA)
			}
			if cm.Author != nil {
				d.AuthorLogin = cm.Author.Login
			}
			all = append(all, d)
		}
		// An empty page ends the loop too, so a total that never fills cannot spin.
		if len(all) >= out.TotalCommits || len(out.Commits) == 0 {
			return all, nil
		}
	}
}

type pullJSON struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	HTMLURL string `json:"html_url"`
	Head    struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	User *struct {
		Login string `json:"login"`
	} `json:"user"`
	MergedAt       *time.Time `json:"merged_at"`
	MergeCommitSHA string     `json:"merge_commit_sha"`
}

func (p pullJSON) toPull() drift.PullRequest {
	pr := drift.PullRequest{
		Number:         p.Number,
		Title:          p.Title,
		URL:            p.HTMLURL,
		HeadRef:        p.Head.Ref,
		HeadSHA:        p.Head.SHA,
		BaseRef:        p.Base.Ref,
		MergeCommitSHA: p.MergeCommitSHA,
	}
	if p.User != nil {
		pr.AuthorLogin = p.User.Login
	}
	if p.MergedAt != nil {
		pr.Merged = true
		pr.MergedAt = *p.MergedAt
	}
	return pr
}

func toPulls(in []pullJSON) []drift.PullRequest {
	out := make([]drift.PullRequest, 0, len(in))
	for _, p := range in {
		out = append(out, p.toPull())
	}
	return out
}

// 404 and 422 both mean GitHub does not know the commit, which for a lookup
// is the same answer as "no pull requests".
func (c *Client) PullsForCommit(owner, repo, sha string) ([]drift.PullRequest, error) {
	var out []pullJSON
	err := c.do("GET", repoPath(owner, repo)+"/commits/"+url.PathEscape(sha)+"/pulls?per_page=100", nil, &out)
	if isStatus(err, http.StatusNotFound, http.StatusUnprocessableEntity) {
		return []drift.PullRequest{}, nil
	}
	if err != nil {
		return nil, err
	}
	return toPulls(out), nil
}

func (c *Client) Pull(owner, repo string, number int) (drift.PullRequest, error) {
	var out pullJSON
	if err := c.do("GET", fmt.Sprintf("%s/pulls/%d", repoPath(owner, repo), number), nil, &out); err != nil {
		return drift.PullRequest{}, notFound(err)
	}
	return out.toPull(), nil
}

func (c *Client) ClosedPullsByBase(owner, repo, base string, page int) ([]drift.PullRequest, error) {
	q := url.Values{
		"state":     {"closed"},
		"base":      {base},
		"sort":      {"updated"},
		"direction": {"desc"},
		"per_page":  {"100"},
		"page":      {fmt.Sprint(page)},
	}
	var out []pullJSON
	if err := c.do("GET", repoPath(owner, repo)+"/pulls?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return toPulls(out), nil
}

func (c *Client) ClosedPullsByHead(owner, repo, base, headRef string) ([]drift.PullRequest, error) {
	q := url.Values{
		"state":    {"closed"},
		"base":     {base},
		"head":     {owner + ":" + headRef},
		"per_page": {"100"},
	}
	var out []pullJSON
	if err := c.do("GET", repoPath(owner, repo)+"/pulls?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return toPulls(out), nil
}
