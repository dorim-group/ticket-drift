// Package github is a minimal GitHub REST client: App or token auth, and the
// handful of endpoints drift detection and the branch reset need.
package github

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DefaultAPIURL = "https://api.github.com"
	acceptHeader  = "application/vnd.github+json"
	apiVersion    = "2022-11-28"

	maxAttempts  = 4
	maxRetryWait = 60 * time.Second
	maxErrorBody = 500 // bytes of a response body kept in an error message
)

type Client struct {
	BaseURL   string
	UserAgent string
	HTTP      *http.Client
	Sleep     func(time.Duration) // swappable so tests never really sleep

	appID     string
	installID string
	key       *rsa.PrivateKey

	now func() time.Time // swappable so tests can pin JWT timestamps

	token    string // static for NewTokenClient, minted for App auth
	tokenExp time.Time
	static   bool
}

// APIError is a non-2xx response from GitHub.
type APIError struct {
	Status       int
	Method, Path string
	Body         string
}

func (e *APIError) Error() string {
	body := e.Body
	if len(body) > maxErrorBody {
		cut := maxErrorBody
		for cut > 0 && !utf8.RuneStart(body[cut]) {
			cut--
		}
		body = body[:cut]
	}
	return fmt.Sprintf("github: %s %s: status %d: %s", e.Method, e.Path, e.Status, body)
}

func NewAppClient(appID, installationID string, pemKey []byte) (*Client, error) {
	if strings.TrimSpace(appID) == "" {
		return nil, errors.New("github: app id is empty")
	}
	if strings.TrimSpace(installationID) == "" {
		return nil, errors.New("github: installation id is empty")
	}
	key, err := parsePrivateKey(pemKey)
	if err != nil {
		return nil, err
	}
	return &Client{
		BaseURL:   DefaultAPIURL,
		UserAgent: "ticket-drift",
		HTTP:      &http.Client{Timeout: 30 * time.Second},
		Sleep:     time.Sleep,
		appID:     appID,
		installID: installationID,
		key:       key,
		now:       time.Now,
	}, nil
}

func NewTokenClient(token string) *Client {
	return &Client{
		BaseURL:   DefaultAPIURL,
		UserAgent: "ticket-drift",
		HTTP:      &http.Client{Timeout: 30 * time.Second},
		Sleep:     time.Sleep,
		now:       time.Now,
		token:     token,
		static:    true,
	}
}

// GitHub hands out PKCS#1; openssl produces PKCS#8 if you convert it.
func parsePrivateKey(pemKey []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(bytes.TrimSpace(pemKey))
	if block == nil {
		return nil, errors.New("github: private key is not PEM (did the base64 decode step run?)")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	any, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("github: parse private key: %w", err)
	}
	k, ok := any.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("github: private key is %T, want RSA", any)
	}
	return k, nil
}

// GitHub rejects a JWT whose iat is in the future, hence the back-dating.
func appJWT(appID string, key *rsa.PrivateKey, now time.Time) (string, error) {
	b64 := func(v any) (string, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(raw), nil
	}

	header, err := b64(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := b64(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": appID,
	})
	if err != nil {
		return "", err
	}

	signing := header + "." + claims
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("github: sign jwt: %w", err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (c *Client) installationToken() (string, error) {
	if c.static {
		return c.token, nil
	}
	if c.token != "" && c.now().Before(c.tokenExp.Add(-2*time.Minute)) {
		return c.token, nil
	}

	jwt, err := appJWT(c.appID, c.key, c.now())
	if err != nil {
		return "", err
	}

	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	path := "/app/installations/" + url.PathEscape(c.installID) + "/access_tokens"
	if err := c.request("POST", path, "Bearer "+jwt, nil, &out); err != nil {
		return "", fmt.Errorf("github: mint installation token: %w", err)
	}
	if out.Token == "" {
		return "", errors.New("github: installation token response had no token")
	}

	c.token, c.tokenExp = out.Token, out.ExpiresAt
	return c.token, nil
}

func (c *Client) do(method, path string, body, out any) error {
	tok, err := c.installationToken()
	if err != nil {
		return err
	}
	return c.request(method, path, "Bearer "+tok, body, out)
}

// request retries transport errors, 5xx, 429 and a 403 that carries
// Retry-After (GitHub's secondary rate limit), waiting 1s, 2s, 4s or the
// Retry-After it gives. Every call is safe to repeat: reads, forcing a ref to
// a fixed SHA, and minting an installation token.
func (c *Client) request(method, path, authorization string, body, out any) error {
	var payload []byte
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = raw
	}

	sleep := c.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	backoff := time.Second
	for attempt := 1; ; attempt++ {
		resp, raw, err := c.send(method, path, authorization, payload)
		var wait time.Duration
		switch {
		case err != nil:
			wait = backoff
		case resp.StatusCode >= 200 && resp.StatusCode <= 299:
			if out == nil || len(bytes.TrimSpace(raw)) == 0 {
				return nil
			}
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("github: %s %s: decode response: %w", method, path, err)
			}
			return nil
		default:
			err = &APIError{Status: resp.StatusCode, Method: method, Path: path, Body: strings.TrimSpace(string(raw))}
			retry := resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests ||
				(resp.StatusCode == http.StatusForbidden && resp.Header.Get("Retry-After") != "")
			if !retry {
				return err
			}
			wait = backoff
			if after, ok := retryAfter(resp.Header); ok {
				wait = after
			}
		}
		if attempt == maxAttempts {
			return err
		}
		sleep(wait)
		backoff *= 2
	}
}

func (c *Client) send(method, path, authorization string, payload []byte) (*http.Response, []byte, error) {
	var r io.Reader
	if payload != nil {
		r = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, strings.TrimSuffix(c.BaseURL, "/")+path, r)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Accept", acceptHeader)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	ua := c.UserAgent
	if ua == "" {
		ua = "ticket-drift"
	}
	req.Header.Set("User-Agent", ua)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, nil, err
	}
	return resp, raw, nil
}

// retryAfter reads a Retry-After given in seconds, capped at maxRetryWait.
func retryAfter(h http.Header) (time.Duration, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After")))
	if err != nil || n < 0 {
		return 0, false
	}
	return min(time.Duration(n)*time.Second, maxRetryWait), true
}
