package github

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewTokenClient("t")
	c.BaseURL = srv.URL
	return c
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
