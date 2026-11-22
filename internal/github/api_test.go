package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/dorim-group/ticket-drift/internal/drift"
)

var _ drift.Source = RepoSource{}

func TestBranchSHA(t *testing.T) {
	var path string
	status := http.StatusOK
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"object":{"sha":"abc123"}}`))
		}
	})

	sha, found, err := c.BranchSHA("o", "r", "dev")
	if err != nil || !found || sha != "abc123" {
		t.Fatalf("got %q %v %v, want abc123 true nil", sha, found, err)
	}
	if path != "/repos/o/r/git/ref/heads/dev" {
		t.Errorf("path = %q", path)
	}

	status = http.StatusNotFound
	sha, found, err = c.BranchSHA("o", "r", "dev")
	if err != nil || found || sha != "" {
		t.Fatalf("404: got %q %v %v, want \"\" false nil", sha, found, err)
	}
}

func TestUpdateRefForces(t *testing.T) {
	var method, path, body string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		_, _ = w.Write([]byte(`{}`))
	})
	if err := c.UpdateRef("o", "r", "feat/x", "abc"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPatch || path != "/repos/o/r/git/refs/heads/feat/x" {
		t.Errorf("got %s %s", method, path)
	}
	if body != `{"sha":"abc","force":true}` {
		t.Errorf("body = %s", body)
	}
}

// Characters beyond "/" must reach GitHub percent-encoded, or a space, "%" or
// "#" would cut or corrupt the ref; the assertions read the raw request URI.
func TestRefPathsEscapeBranchNames(t *testing.T) {
	var uris []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		uris = append(uris, r.Method+" "+r.RequestURI)
		_, _ = w.Write([]byte(`{"object":{"sha":"abc"},"status":"ahead"}`))
	})
	const branch = "feat/a b%c#d"
	if _, _, err := c.BranchSHA("o", "r", branch); err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateRef("o", "r", branch, "abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CompareStatus("o", "r", "main", branch); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /repos/o/r/git/ref/heads/feat/a%20b%25c%23d",
		"PATCH /repos/o/r/git/refs/heads/feat/a%20b%25c%23d",
		"GET /repos/o/r/compare/main...feat/a%20b%25c%23d?per_page=1",
	}
	if !slices.Equal(uris, want) {
		t.Errorf("request URIs = %q, want %q", uris, want)
	}
}

func TestCompareStatusNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	_, err := c.CompareStatus("o", "r", "main", "dead")
	if !errors.Is(err, drift.ErrNotFound) {
		t.Fatalf("err = %v, want drift.ErrNotFound", err)
	}
}

func TestCompareStatus(t *testing.T) {
	var path, query string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		path, query = r.URL.Path, r.URL.RawQuery
		_, _ = w.Write([]byte(`{"status":"ahead"}`))
	})
	st, err := c.CompareStatus("o", "r", "main", "feat/x")
	if err != nil || st != "ahead" {
		t.Fatalf("got %q %v", st, err)
	}
	if path != "/repos/o/r/compare/main...feat/x" || query != "per_page=1" {
		t.Errorf("request = %s?%s", path, query)
	}
}

func TestAheadCommitsPages(t *testing.T) {
	var requests atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if r.URL.Query().Get("per_page") != "100" {
			t.Errorf("per_page = %q", r.URL.Query().Get("per_page"))
		}
		n := 100
		if page == 2 {
			n = 50
		}
		commits := make([]map[string]any, 0, n)
		for i := 0; i < n; i++ {
			c := map[string]any{
				"sha":     fmt.Sprintf("s%d-%d", page, i),
				"parents": []map[string]any{{"sha": "p1"}, {"sha": "p2"}},
				"commit": map[string]any{
					"message": "msg",
					"author":  map[string]any{"name": "Ann"},
				},
				"author": map[string]any{"login": "ann"},
			}
			if page == 2 && i == 0 {
				c["author"] = nil
			}
			commits = append(commits, c)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_commits": 150, "commits": commits})
	})

	got, err := c.AheadCommits("o", "r", "main", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 150 || requests.Load() != 2 {
		t.Fatalf("got %d commits in %d requests, want 150 in 2", len(got), requests.Load())
	}
	first := got[0]
	if first.SHA != "s1-0" || len(first.Parents) != 2 || first.Parents[1] != "p2" ||
		first.Message != "msg" || first.AuthorLogin != "ann" || first.AuthorName != "Ann" {
		t.Errorf("first = %+v", first)
	}
	if got[100].SHA != "s2-0" || got[100].AuthorLogin != "" || got[100].AuthorName != "Ann" {
		t.Errorf("null author commit = %+v", got[100])
	}
}

func TestAheadCommitsTooMany(t *testing.T) {
	var requests atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"total_commits":2001,"commits":[]}`))
	})
	_, err := c.AheadCommits("o", "r", "main", "dev")
	if !errors.Is(err, drift.ErrTooManyCommits) {
		t.Fatalf("err = %v, want ErrTooManyCommits", err)
	}
	if requests.Load() != 1 {
		t.Errorf("requests = %d, want 1", requests.Load())
	}
}

func TestAheadCommitsNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	})
	if _, err := c.AheadCommits("o", "r", "main", "dead"); !errors.Is(err, drift.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestPullMapping(t *testing.T) {
	body := `{"number":5,"title":"T","html_url":"https://x/5",
		"head":{"ref":"IT-1/x","sha":"hs"},"base":{"ref":"main"},
		"user":%s,"merged_at":%s,"merge_commit_sha":"mc"}`
	var resp string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/pulls/5" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(resp))
	})

	resp = fmt.Sprintf(body, `{"login":"ann"}`, `"2026-11-20T10:00:00Z"`)
	pr, err := c.Pull("o", "r", 5)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 5 || pr.Title != "T" || pr.URL != "https://x/5" || pr.HeadRef != "IT-1/x" ||
		pr.HeadSHA != "hs" || pr.BaseRef != "main" || pr.AuthorLogin != "ann" ||
		!pr.Merged || pr.MergeCommitSHA != "mc" || pr.MergedAt.Year() != 2026 {
		t.Errorf("pr = %+v", pr)
	}

	resp = fmt.Sprintf(body, `null`, `null`)
	pr, err = c.Pull("o", "r", 5)
	if err != nil {
		t.Fatal(err)
	}
	if pr.AuthorLogin != "" || pr.Merged || !pr.MergedAt.IsZero() {
		t.Errorf("null user/merged_at: pr = %+v", pr)
	}
}

func TestPullNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	})
	if _, err := c.Pull("o", "r", 9); !errors.Is(err, drift.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestClosedPullsByHeadQuery(t *testing.T) {
	var q map[string][]string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query()
		_, _ = w.Write([]byte(`[{"number":1,"head":{"ref":"IT-1/x"},"base":{"ref":"main"}}]`))
	})
	prs, err := c.ClosedPullsByHead("o", "r", "main", "IT-1/x")
	if err != nil || len(prs) != 1 || prs[0].Number != 1 {
		t.Fatalf("got %+v %v", prs, err)
	}
	for k, want := range map[string]string{"head": "o:IT-1/x", "base": "main", "state": "closed", "per_page": "100"} {
		if got := q[k]; len(got) != 1 || got[0] != want {
			t.Errorf("query %s = %v, want %q", k, got, want)
		}
	}
}

func TestClosedPullsByBaseQuery(t *testing.T) {
	var q map[string][]string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query()
		_, _ = w.Write([]byte(`[]`))
	})
	if _, err := c.ClosedPullsByBase("o", "r", "main", 3); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"state": "closed", "base": "main", "sort": "updated",
		"direction": "desc", "per_page": "100", "page": "3"} {
		if got := q[k]; len(got) != 1 || got[0] != want {
			t.Errorf("query %s = %v, want %q", k, got, want)
		}
	}
}

func TestPullsForCommitTolerates422(t *testing.T) {
	for _, status := range []int{http.StatusUnprocessableEntity, http.StatusNotFound} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "no commit", status)
		})
		prs, err := c.PullsForCommit("o", "r", "abc")
		if err != nil || len(prs) != 0 {
			t.Errorf("status %d: got %v %v, want empty nil", status, prs, err)
		}
	}
}

func TestPullsForCommitMaps(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/commits/abc/pulls" || r.URL.Query().Get("per_page") != "100" {
			t.Errorf("request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`[{"number":2,"head":{"ref":"h"},"base":{"ref":"main"}}]`))
	})
	prs, err := c.PullsForCommit("o", "r", "abc")
	if err != nil || len(prs) != 1 || prs[0].Number != 2 || prs[0].HeadRef != "h" {
		t.Fatalf("got %+v %v", prs, err)
	}
}

func TestAPIErrorCarriesStatus(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := c.Pull("o", "r", 1)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 500 {
		t.Fatalf("err = %v, want *APIError with Status 500", err)
	}
	if apiErr.Method != "GET" || apiErr.Path != "/repos/o/r/pulls/1" {
		t.Errorf("apiErr = %+v", apiErr)
	}
}

func TestRepoSourceBinds(t *testing.T) {
	var paths []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		_, _ = w.Write([]byte(`{"object":{"sha":"abc"},"status":"identical"}`))
	})
	s := RepoSource{C: c, Owner: "o", Repo: "r"}
	if sha, ok, err := s.BranchSHA("dev"); err != nil || !ok || sha != "abc" {
		t.Fatalf("BranchSHA = %q %v %v", sha, ok, err)
	}
	if err := s.UpdateRef("dev", "abc"); err != nil {
		t.Fatal(err)
	}
	if st, err := s.CompareStatus("main", "dev"); err != nil || st != "identical" {
		t.Fatalf("CompareStatus = %q %v", st, err)
	}
	want := []string{"GET /repos/o/r/git/ref/heads/dev", "PATCH /repos/o/r/git/refs/heads/dev", "GET /repos/o/r/compare/main...dev"}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("paths[%d] = %q, want %q", i, paths[i], want[i])
		}
	}
}
