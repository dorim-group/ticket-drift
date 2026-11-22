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
	"strings"
	"time"
)

const (
	DefaultAPIURL = "https://api.github.com"
	acceptHeader  = "application/vnd.github+json"
	apiVersion    = "2022-11-28"
)

type Client struct {
	BaseURL   string
	UserAgent string
	HTTP      *http.Client

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
	return fmt.Sprintf("github: %s %s: status %d: %s", e.Method, e.Path, e.Status, e.Body)
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

func (c *Client) request(method, path, authorization string, body, out any) error {
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(raw)
	}

	req, err := http.NewRequest(method, strings.TrimSuffix(c.BaseURL, "/")+path, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Accept", acceptHeader)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	ua := c.UserAgent
	if ua == "" {
		ua = "ticket-drift"
	}
	req.Header.Set("User-Agent", ua)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{Status: resp.StatusCode, Method: method, Path: path, Body: strings.TrimSpace(string(raw))}
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("github: %s %s: decode response: %w", method, path, err)
	}
	return nil
}
