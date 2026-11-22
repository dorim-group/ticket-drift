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

// TaskByCustomID retries 429, 5xx and transport errors with 1s, 2s, 4s backoff.
// Other statuses fail at once: a 401 must not look like a missing ticket.
func (c *Client) TaskByCustomID(id string) (Task, error) {
	q := url.Values{"custom_task_ids": {"true"}, "team_id": {c.teamID}}
	u := strings.TrimSuffix(c.BaseURL, "/") + "/api/v2/task/" + url.PathEscape(id) + "?" + q.Encode()

	backoff := time.Second
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			c.Sleep(backoff)
			backoff *= 2
		}
		task, retry, err := c.get(u)
		if err == nil || !retry {
			return task, err
		}
		lastErr = err
	}
	return Task{}, fmt.Errorf("clickup: giving up after %d attempts: %w", maxAttempts, lastErr)
}

func (c *Client) get(u string) (task Task, retry bool, err error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return Task{}, false, err
	}
	req.Header.Set("Authorization", c.token) // bare token, no Bearer

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Task{}, true, fmt.Errorf("clickup: request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Task{}, true, fmt.Errorf("clickup: read response: %w", err)
	}

	switch {
	case resp.StatusCode == http.StatusOK:
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
			return Task{}, false, fmt.Errorf("clickup: decode response: %w", err)
		}
		return Task{ID: out.ID, CustomID: out.CustomID, Name: out.Name, URL: out.URL, Status: out.Status.Status}, false, nil
	case resp.StatusCode == http.StatusNotFound:
		return Task{}, false, ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return Task{}, true, fmt.Errorf("clickup: status %d", resp.StatusCode)
	default:
		return Task{}, false, fmt.Errorf("clickup: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
}
