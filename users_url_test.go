package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const peopleJSON = `[{"name":"Ann","discord_id":"111111111111111111","clickup_email":"Ann@x.io","github_login":"ann-gh"}]`

func usersServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func load(srv *httptest.Server, rawURL string) (config, error) {
	cfg := config{UsersURL: rawURL, Users: map[string]string{}, ClickUpUsers: map[string]string{}}
	err := loadUsersURL(&cfg, newUsersClient(srv.Client().Transport))
	return cfg, err
}

func TestUsersURLLoadsList(t *testing.T) {
	srv := usersServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/people.json" || r.URL.Query().Get("sig") != "s" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(peopleJSON))
	})
	cfg, err := load(srv, srv.URL+"/people.json?sig=s")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Users["ann-gh"] != "111111111111111111" || cfg.ClickUpUsers["ann@x.io"] != "111111111111111111" {
		t.Errorf("users = %v, ClickUp users = %v", cfg.Users, cfg.ClickUpUsers)
	}
}

func TestUsersURLNoURLDoesNothing(t *testing.T) {
	cfg := config{Users: map[string]string{"bob": "1"}}
	if err := loadUsersURL(&cfg, nil); err != nil || cfg.Users["bob"] != "1" {
		t.Errorf("users = %v, err = %v", cfg.Users, err)
	}
}

func TestUsersURLFailures(t *testing.T) {
	big := "[" + strings.Repeat(" ", maxUsersBytes) + "]"
	for name, tc := range map[string]struct {
		h    http.HandlerFunc
		want string
	}{
		"not found": {func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }, "HTTP 404"},
		"forbidden": {func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }, "HTTP 403"},
		"empty":     {func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(" \n")) }, "is empty"},
		"too large": {func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(big)) }, "larger than 1 MiB"},
		"bad JSON":  {func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("[{")) }, "TICKET_DRIFT_USERS_URL must be a JSON list"},
		"bad entry": {func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`[{"discord_id":"12","github_login":"x"}]`))
		}, "want 15 to 21 digits"},
		"redirect to http": {func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://bucket.example/p.json", http.StatusFound)
		}, "redirected to a non-https URL"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := usersServer(t, tc.h)
			_, err := load(srv, srv.URL+"/p.json?X-Amz-Signature=topsecret")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "topsecret") {
				t.Errorf("error shows the query: %v", err)
			}
		})
	}
}

// A failed connection must not print the link's secret parts either.
func TestUsersURLConnectionErrorHidesSecrets(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	transport := srv.Client().Transport
	u := strings.Replace(srv.URL, "https://", "https://user:pwsecret@", 1) + "/p.json?X-Amz-Signature=topsecret#frag"
	srv.Close()
	cfg := config{UsersURL: u}
	err := loadUsersURL(&cfg, newUsersClient(transport))
	if err == nil {
		t.Fatal("err = nil")
	}
	for _, s := range []string{"topsecret", "pwsecret", "user:", "frag"} {
		if strings.Contains(err.Error(), s) {
			t.Errorf("error shows %q: %v", s, err)
		}
	}
	if !strings.Contains(err.Error(), "/p.json") {
		t.Errorf("error does not say which file: %v", err)
	}
}

func TestRedactURL(t *testing.T) {
	if got := redactURL("https://u:p@host.example:8443/a/b.json?sig=x#f"); got != "https://host.example:8443/a/b.json" {
		t.Errorf("got %q", got)
	}
}
