// Package discord posts one message to a Discord webhook, honouring its rate
// limits.
package discord

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxAttempts  = 4  // 5xx and transport errors
	max429       = 10 // rate-limit waits, counted apart from attempts
	flagNoEmbeds = 4  // suppress link previews
	defaultWait  = time.Second
	maxWait      = time.Minute // cap on any one rate-limit wait
	maxBodyBytes = 1 << 20
)

type Client struct {
	WebhookURL string
	HTTP       *http.Client
	Sleep      func(time.Duration) // swappable so tests never really sleep
}

func New(webhookURL string) *Client {
	return &Client{
		WebhookURL: webhookURL,
		HTTP:       &http.Client{Timeout: 30 * time.Second},
		Sleep:      time.Sleep,
	}
}

// Post sends one message. Only the given user IDs can be pinged.
// Errors never contain the webhook URL: its path is the secret.
func (c *Client) Post(content string, mentionUserIDs []string) error {
	u, err := url.Parse(c.WebhookURL)
	if err != nil {
		return errors.New("discord: webhook URL is not a valid URL")
	}
	q := u.Query()
	q.Set("wait", "true")
	u.RawQuery = q.Encode()

	users := mentionUserIDs
	if users == nil {
		users = []string{} // encode as [], not null
	}
	body, err := json.Marshal(map[string]any{
		"content":          content,
		"flags":            flagNoEmbeds,
		"allowed_mentions": map[string]any{"parse": []string{}, "users": users},
	})
	if err != nil {
		return err
	}

	backoff := time.Second
	failures, limited := 0, 0
	for {
		resp, err := c.send(u.String(), body)
		var status int
		var raw []byte
		if err == nil {
			status = resp.StatusCode
			raw, _ = io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
			resp.Body.Close()
		}

		switch {
		case err == nil && status >= 200 && status <= 299:
			if resp.Header.Get("X-RateLimit-Remaining") == "0" {
				if d, ok := seconds(resp.Header.Get("X-RateLimit-Reset-After")); ok {
					c.Sleep(d)
				}
			}
			return nil
		case err == nil && status == http.StatusTooManyRequests:
			limited++
			if limited > max429 {
				return fmt.Errorf("discord: still rate limited after %d waits", max429)
			}
			c.Sleep(retryAfter(resp, raw))
			continue
		case err == nil && status < 500:
			return fmt.Errorf("discord: status %d: %s", status, strings.TrimSpace(string(raw)))
		}

		failures++
		if failures >= maxAttempts {
			if err != nil {
				return fmt.Errorf("discord: giving up after %d attempts: %w", maxAttempts, err)
			}
			return fmt.Errorf("discord: giving up after %d attempts: status %d", maxAttempts, status)
		}
		c.Sleep(backoff)
		backoff *= 2
	}
}

// send strips the *url.Error wrapper, which would carry the webhook URL.
func (c *Client) send(target string, body []byte) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("discord: build request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("request failed: %w", err)
	}
	return resp, nil
}

func retryAfter(resp *http.Response, body []byte) time.Duration {
	var out struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if json.Unmarshal(body, &out) == nil && out.RetryAfter > 0 {
		return wait(out.RetryAfter)
	}
	if d, ok := seconds(resp.Header.Get("Retry-After")); ok {
		return d
	}
	return defaultWait
}

func seconds(s string) (time.Duration, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || !(f >= 0) { // also rejects NaN
		return 0, false
	}
	return wait(f), true
}

// wait converts Discord's seconds to a sleep, capped before the conversion
// so a huge value cannot overflow.
func wait(sec float64) time.Duration {
	if sec >= maxWait.Seconds() {
		return maxWait
	}
	return time.Duration(sec * float64(time.Second))
}
