package github

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewTokenClient("t")
	c.BaseURL = srv.URL
	c.Sleep = func(time.Duration) {}
	return c
}

// sleeps makes c record its retry waits instead of sleeping.
func sleeps(c *Client) *[]time.Duration {
	var slept []time.Duration
	c.Sleep = func(d time.Duration) { slept = append(slept, d) }
	return &slept
}

func TestRetries502ThenSucceeds(t *testing.T) {
	var n atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"object":{"sha":"abc"}}`))
	})
	slept := sleeps(c)
	if sha, _, err := c.BranchSHA("o", "r", "dev"); err != nil || sha != "abc" {
		t.Fatalf("BranchSHA = %q, %v", sha, err)
	}
	if want := []time.Duration{time.Second}; !slices.Equal(*slept, want) {
		t.Errorf("sleeps = %v, want %v", *slept, want)
	}
}

func TestGivesUpAfterFourAttempts(t *testing.T) {
	var n atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	slept := sleeps(c)
	_, _, err := c.BranchSHA("o", "r", "dev")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("err = %v, want *APIError 503", err)
	}
	if n.Load() != 4 {
		t.Errorf("requests = %d, want 4", n.Load())
	}
	if want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}; !slices.Equal(*slept, want) {
		t.Errorf("sleeps = %v, want %v", *slept, want)
	}
}

func TestRetries403WithRetryAfter(t *testing.T) {
	var n atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"object":{"sha":"abc"}}`))
	})
	slept := sleeps(c)
	if _, _, err := c.BranchSHA("o", "r", "dev"); err != nil {
		t.Fatal(err)
	}
	if want := []time.Duration{2 * time.Second}; !slices.Equal(*slept, want) {
		t.Errorf("sleeps = %v, want %v", *slept, want)
	}
}

func TestNoRetryOn403WithoutRetryAfter(t *testing.T) {
	var n atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusForbidden)
	})
	slept := sleeps(c)
	if _, _, err := c.BranchSHA("o", "r", "dev"); err == nil {
		t.Fatal("want error")
	}
	if n.Load() != 1 || len(*slept) != 0 {
		t.Errorf("requests = %d, sleeps = %v; want 1 and none", n.Load(), *slept)
	}
}

func TestRetryAfterIsCapped(t *testing.T) {
	var n atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"object":{"sha":"abc"}}`))
	})
	slept := sleeps(c)
	if _, _, err := c.BranchSHA("o", "r", "dev"); err != nil {
		t.Fatal(err)
	}
	if want := []time.Duration{60 * time.Second}; !slices.Equal(*slept, want) {
		t.Errorf("sleeps = %v, want %v", *slept, want)
	}
}

func TestRetriesTransportError(t *testing.T) {
	var n atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close() // the client sees a broken connection
			return
		}
		_, _ = w.Write([]byte(`{"object":{"sha":"abc"}}`))
	})
	slept := sleeps(c)
	if _, _, err := c.BranchSHA("o", "r", "dev"); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 2 || len(*slept) != 1 {
		t.Errorf("requests = %d, sleeps = %v; want 2 and one", n.Load(), *slept)
	}
}

// Forcing a ref to a fixed SHA is idempotent, so the PATCH is retried, with
// its body.
func TestRetriedPatchResendsBody(t *testing.T) {
	var bodies []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, r.Method+" "+string(b))
		if len(bodies) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	if err := c.UpdateRef("o", "r", "dev", "abc"); err != nil {
		t.Fatal(err)
	}
	want := `PATCH {"sha":"abc","force":true}`
	if len(bodies) != 2 || bodies[0] != want || bodies[1] != want {
		t.Errorf("requests = %q, want two of %q", bodies, want)
	}
}

func TestAPIErrorTruncatesBody(t *testing.T) {
	e := &APIError{Status: 500, Method: "GET", Path: "/p", Body: strings.Repeat("x", 2000)}
	if n := strings.Count(e.Error(), "x"); n != 500 {
		t.Errorf("error carries %d bytes of body, want 500", n)
	}
}

func TestTokenClientSendsBearer(t *testing.T) {
	var auth, ua string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		auth, ua = r.Header.Get("Authorization"), r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{"object":{"sha":"abc"}}`))
	})
	if _, _, err := c.BranchSHA("o", "r", "dev"); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer t" {
		t.Errorf("Authorization = %q, want %q", auth, "Bearer t")
	}
	if ua != "ticket-drift" {
		t.Errorf("User-Agent = %q, want ticket-drift", ua)
	}
}

func TestAppClientMintsInstallationToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	var (
		mu    sync.Mutex
		calls []string
		auths []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.URL.Path == "/app/installations/7/access_tokens" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "ghs_inst",
				"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
			})
			return
		}
		_, _ = w.Write([]byte(`{"object":{"sha":"abc"}}`))
	}))
	defer srv.Close()

	c, err := NewAppClient("1", "7", pemKey)
	if err != nil {
		t.Fatal(err)
	}
	c.BaseURL = srv.URL
	for range 2 {
		if _, _, err := c.BranchSHA("o", "r", "dev"); err != nil {
			t.Fatal(err)
		}
	}

	if len(calls) != 3 || calls[0] != "POST /app/installations/7/access_tokens" {
		t.Fatalf("calls = %v, want token mint once then two API calls", calls)
	}
	if auths[1] != "Bearer ghs_inst" || auths[2] != "Bearer ghs_inst" {
		t.Errorf("API calls used %q, want the installation token", auths[1:])
	}
}
