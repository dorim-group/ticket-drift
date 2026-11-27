package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	usersURLSource  = "TICKET_DRIFT_USERS_URL"
	maxUsersBytes   = 1 << 20 // a real list is a few KB
	usersURLTimeout = 15 * time.Second
	maxRedirects    = 10
)

// newUsersClient refuses a redirect away from https. A nil transport is the
// default one.
func newUsersClient(transport http.RoundTripper) *http.Client {
	return &http.Client{
		Transport: transport,
		Timeout:   usersURLTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" {
				return errors.New("redirected to a non-https URL")
			}
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			return nil
		},
	}
}

// loadUsersURL downloads the people list from cfg.UsersURL into cfg. With no
// URL it does nothing. Any failure stops the run before anything is posted.
func loadUsersURL(cfg *config, client *http.Client) error {
	if cfg.UsersURL == "" {
		return nil
	}
	raw, err := fetchUsers(client, cfg.UsersURL)
	if err != nil {
		return err
	}
	cfg.Users, cfg.ClickUpUsers, err = parseUsers(raw, usersURLSource)
	return err
}

// fetchUsers names the URL in its errors only as redactURL shows it.
func fetchUsers(client *http.Client, rawURL string) ([]byte, error) {
	shown := redactURL(rawURL)
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("users: %s is not a valid URL", usersURLSource)
	}
	req.Header.Set("User-Agent", "ticket-drift/"+version)
	resp, err := client.Do(req)
	if err != nil {
		// *url.Error repeats the full URL; keep only its cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("users: download %s: %w", shown, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("users: download %s: HTTP %d", shown, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxUsersBytes+1))
	if err != nil {
		return nil, fmt.Errorf("users: download %s: %w", shown, err)
	}
	if len(body) > maxUsersBytes {
		return nil, fmt.Errorf("users: %s is larger than 1 MiB", shown)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, fmt.Errorf("users: %s is empty", shown)
	}
	return body, nil
}

// redactURL drops the user info, query and fragment, where a signed link or
// a credential would sit.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return usersURLSource
	}
	return u.Scheme + "://" + u.Host + u.EscapedPath()
}
