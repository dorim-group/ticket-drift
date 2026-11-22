// Package clickup looks up a task by its custom id (IT-10249) so a notification
// can carry the ticket title and link.
package clickup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://api.clickup.com"
	maxAttempts    = 4
)

type Task struct{ ID, CustomID, Name, URL, Status string }

var ErrNotFound = errors.New("clickup: task not found")

type Client struct {
	BaseURL string
	HTTP    *http.Client
	Sleep   func(time.Duration) // swappable so tests never really sleep

	token, teamID string
}

func New(token, teamID string) *Client {
	return &Client{
		BaseURL: defaultBaseURL,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
		Sleep:   time.Sleep,
		token:   token,
		teamID:  teamID,
	}
}

// Check proves the token works and can see the team, so that TaskByCustomID
// can read any other 4xx as "no such task".
func (c *Client) Check() error {
	status, raw, err := c.fetch(strings.TrimSuffix(c.BaseURL, "/") + "/api/v2/team")
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("clickup: check token: status %d: %s", status, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Teams []struct {
			ID string `json:"id"`
		} `json:"teams"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("clickup: check token: decode response: %w", err)
	}
	for _, t := range out.Teams {
		if t.ID == c.teamID {
			return nil
		}
	}
	return fmt.Errorf("clickup: the token cannot see team %s (CLICKUP_TEAM_ID)", c.teamID)
}

// TaskByCustomID retries 429, 5xx and transport errors. Any other 4xx is
// ErrNotFound: ClickUp answers an unknown or inaccessible ID with more than
// one code, including 401. That reading is only sound after Check succeeded,
// which rules out a bad token or team.
func (c *Client) TaskByCustomID(id string) (Task, error) {
	q := url.Values{"custom_task_ids": {"true"}, "team_id": {c.teamID}}
	u := strings.TrimSuffix(c.BaseURL, "/") + "/api/v2/task/" + url.PathEscape(id) + "?" + q.Encode()

	status, raw, err := c.fetch(u)
	switch {
	case err != nil:
		return Task{}, err
	case status == http.StatusOK:
		var out struct {
			ID       string `json:"id"`
			CustomID string `json:"custom_id"`
			Name     string `json:"name"`
			URL      string `json:"url"`
			Status   struct {
				Status string `json:"status"`
			} `json:"status"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return Task{}, fmt.Errorf("clickup: %s: decode response: %w", id, err)
		}
		if out.ID == "" {
			return Task{}, fmt.Errorf("clickup: %s: response has no task id", id)
		}
		return Task{ID: out.ID, CustomID: out.CustomID, Name: out.Name, URL: out.URL, Status: out.Status.Status}, nil
	case status >= 400 && status <= 499:
		return Task{}, fmt.Errorf("clickup: %s: status %d: %w", id, status, ErrNotFound)
	default:
		return Task{}, fmt.Errorf("clickup: %s: status %d: %s", id, status, strings.TrimSpace(string(raw)))
	}
}

// fetch GETs u, retrying 429, 5xx and transport errors with 1s, 2s, 4s
// backoff, and returns the final status and body.
func (c *Client) fetch(u string) (int, []byte, error) {
	backoff := time.Second
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			c.Sleep(backoff)
			backoff *= 2
		}
		status, raw, err := c.get(u)
		if err == nil && status != http.StatusTooManyRequests && status < 500 {
			return status, raw, nil
		}
		if err == nil {
			err = fmt.Errorf("clickup: status %d", status)
		}
		lastErr = err
	}
	return 0, nil, fmt.Errorf("clickup: giving up after %d attempts: %w", maxAttempts, lastErr)
}

func (c *Client) get(u string) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", c.token) // bare token, no Bearer

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("clickup: request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, fmt.Errorf("clickup: read response: %w", err)
	}
	return resp.StatusCode, raw, nil
}
