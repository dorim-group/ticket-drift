package discord

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.HandlerFunc) (*Client, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(srv.URL + "/api/webhooks/1/secrettoken")
	var slept []time.Duration
	c.Sleep = func(d time.Duration) { slept = append(slept, d) }
	return c, &slept
}

func TestPostBody(t *testing.T) {
	var ct string
	var body struct {
		Content string `json:"content"`
		Flags   int    `json:"flags"`
		Allowed struct {
			Parse []string `json:"parse"`
			Users []string `json:"users"`
		} `json:"allowed_mentions"`
	}
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		ct = r.Header.Get("Content-Type")
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.Post("привет", []string{"111", "222"}); err != nil {
		t.Fatal(err)
	}
	if ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if body.Content != "привет" || body.Flags != 4 {
		t.Errorf("body = %+v", body)
	}
	if body.Allowed.Parse == nil || len(body.Allowed.Parse) != 0 {
		t.Errorf("parse = %#v, want empty array", body.Allowed.Parse)
	}
	if !slices.Equal(body.Allowed.Users, []string{"111", "222"}) {
		t.Errorf("users = %v", body.Allowed.Users)
	}
}

func TestPostNoMentionsSendsEmptyArray(t *testing.T) {
	var raw string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		raw = string(b)
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.Post("x", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"users":[]`) {
		t.Errorf("body = %s, want users:[]", raw)
	}
}

func TestPostKeepsExistingQuery(t *testing.T) {
	var q map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL + "/x?thread_id=1")
	if err := c.Post("x", nil); err != nil {
		t.Fatal(err)
	}
	if q["thread_id"][0] != "1" || q["wait"][0] != "true" {
		t.Errorf("query = %v", q)
	}
}

func TestPostWaitsOn429(t *testing.T) {
	var n atomic.Int32
	c, slept := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"retry_after":0.5}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.Post("x", nil); err != nil {
		t.Fatal(err)
	}
	if want := []time.Duration{500 * time.Millisecond}; !slices.Equal(*slept, want) {
		t.Errorf("sleeps = %v, want %v", *slept, want)
	}
}

func TestPostSleepsWhenBucketEmpty(t *testing.T) {
	c, slept := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset-After", "1.5")
		_, _ = w.Write([]byte(`{}`))
	})
	if err := c.Post("x", nil); err != nil {
		t.Fatal(err)
	}
	if want := []time.Duration{1500 * time.Millisecond}; !slices.Equal(*slept, want) {
		t.Errorf("sleeps = %v, want %v", *slept, want)
	}
}

func TestPostRetries5xx(t *testing.T) {
	var n atomic.Int32
	c, slept := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	if err := c.Post("x", nil); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 2 {
		t.Errorf("requests = %d, want 2", n.Load())
	}
	if want := []time.Duration{time.Second}; !slices.Equal(*slept, want) {
		t.Errorf("sleeps = %v, want %v", *slept, want)
	}
}

func TestPostFailsFastOn4xx(t *testing.T) {
	var n atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"bad"}`))
	})
	err := c.Post("x", nil)
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "bad") {
		t.Errorf("err = %v, want status and body", err)
	}
	if n.Load() != 1 {
		t.Errorf("requests = %d, want 1", n.Load())
	}
}

func TestPostErrorHidesWebhookURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	hook := srv.URL + "/api/webhooks/1/secrettoken"
	srv.Close() // connection refused
	c := New(hook)
	c.Sleep = func(time.Duration) {}
	err := c.Post("x", nil)
	if err == nil {
		t.Fatal("want error")
	}
	for _, leak := range []string{"secrettoken", "/api/webhooks", srv.URL} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error leaks %q: %v", leak, err)
		}
	}
}
