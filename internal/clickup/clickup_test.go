package clickup

import (
	"errors"
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
	c := New("tok", "42")
	c.BaseURL = srv.URL
	var slept []time.Duration
	c.Sleep = func(d time.Duration) { slept = append(slept, d) }
	return c, &slept
}

func TestTaskByCustomID(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/task/IT-10249" {
			t.Errorf("path = %q", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("custom_task_ids") != "true" || q.Get("team_id") != "42" {
			t.Errorf("query = %q", r.URL.RawQuery)
		}
		if got := r.Header.Get("Authorization"); got != "tok" {
			t.Errorf("Authorization = %q, want %q", got, "tok")
		}
		_, _ = w.Write([]byte(`{"id":"abc1","custom_id":"IT-10249","name":"Fix login","url":"https://app.clickup.com/t/abc1","status":{"status":"in review"}}`))
	})
	got, err := c.TaskByCustomID("IT-10249")
	if err != nil {
		t.Fatal(err)
	}
	want := Task{ID: "abc1", CustomID: "IT-10249", Name: "Fix login", URL: "https://app.clickup.com/t/abc1", Status: "in review"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestTaskNotFound(t *testing.T) {
	var n atomic.Int32
	c, slept := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := c.TaskByCustomID("IT-1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if n.Load() != 1 {
		t.Errorf("requests = %d, want 1", n.Load())
	}
	if len(*slept) != 0 {
		t.Errorf("sleeps = %v, want none", *slept)
	}
}

func TestTaskRetries429(t *testing.T) {
	var n atomic.Int32
	c, slept := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"id":"a","custom_id":"IT-1","name":"n","url":"u","status":{"status":"s"}}`))
	})
	if _, err := c.TaskByCustomID("IT-1"); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 2 {
		t.Errorf("requests = %d, want 2", n.Load())
	}
	if want := []time.Duration{time.Second}; !slices.Equal(*slept, want) {
		t.Errorf("sleeps = %v, want %v", *slept, want)
	}
}

func TestTaskRetriesTransportError(t *testing.T) {
	var n atomic.Int32
	c, slept := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close() // the client sees a broken connection
			return
		}
		_, _ = w.Write([]byte(`{"id":"a","custom_id":"IT-1","name":"n","url":"u","status":{"status":"s"}}`))
	})
	if _, err := c.TaskByCustomID("IT-1"); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 2 || len(*slept) != 1 {
		t.Errorf("requests = %d, sleeps = %v; want 2 and one", n.Load(), *slept)
	}
}

func TestTaskGiveUpNamesLastStatus(t *testing.T) {
	var n atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 4 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := c.TaskByCustomID("IT-1")
	if err == nil || !strings.Contains(err.Error(), "status 429") || strings.Contains(err.Error(), "502") {
		t.Errorf("err = %v, want it to name the last status, 429", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, must not read as not found", err)
	}
}

// Once Check has proved the token and team, any 4xx for one task means
// ClickUp will not give us that ID, whichever code it picks.
func TestTask401IsNotFound(t *testing.T) {
	var n atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"err":"Team not authorized","ECODE":"OAUTH_027"}`))
	})
	_, err := c.TaskByCustomID("IT-1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if n.Load() != 1 {
		t.Errorf("requests = %d, want 1", n.Load())
	}
}

func TestTaskEmptyIDIsError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"x"}`))
	})
	_, err := c.TaskByCustomID("IT-1")
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want a non-NotFound error", err)
	}
}

func TestCheckOK(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/team" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "tok" {
			t.Errorf("Authorization = %q, want %q", got, "tok")
		}
		_, _ = w.Write([]byte(`{"teams":[{"id":"7","name":"Other"},{"id":"42","name":"Dorim"}]}`))
	})
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
}

func TestCheckWrongTeam(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"teams":[{"id":"7"}]}`))
	})
	err := c.Check()
	if err == nil || !strings.Contains(err.Error(), "42") {
		t.Fatalf("err = %v, want an error naming team 42", err)
	}
}

func TestCheckUnauthorized(t *testing.T) {
	var n atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"err":"Token invalid","ECODE":"OAUTH_025"}`))
	})
	err := c.Check()
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want a 401 error", err)
	}
	if n.Load() != 1 {
		t.Errorf("requests = %d, want 1", n.Load())
	}
}

func TestCheckRetries5xx(t *testing.T) {
	var n atomic.Int32
	c, slept := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"teams":[{"id":"42"}]}`))
	})
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	if want := []time.Duration{time.Second}; !slices.Equal(*slept, want) {
		t.Errorf("sleeps = %v, want %v", *slept, want)
	}
}

func TestTaskRetriesThenSucceeds(t *testing.T) {
	var n atomic.Int32
	c, slept := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"id":"a","custom_id":"IT-1","name":"n","url":"u","status":{"status":"s"}}`))
	})
	got, err := c.TaskByCustomID("IT-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "a" {
		t.Errorf("got %+v", got)
	}
	if n.Load() != 3 {
		t.Errorf("requests = %d, want 3", n.Load())
	}
	if want := []time.Duration{time.Second, 2 * time.Second}; !slices.Equal(*slept, want) {
		t.Errorf("sleeps = %v, want %v", *slept, want)
	}
}

func TestTaskGivesUp(t *testing.T) {
	var n atomic.Int32
	c, slept := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	_, err := c.TaskByCustomID("IT-1")
	if err == nil {
		t.Fatal("want error")
	}
	if n.Load() != 4 {
		t.Errorf("requests = %d, want 4", n.Load())
	}
	if want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}; !slices.Equal(*slept, want) {
		t.Errorf("sleeps = %v, want %v", *slept, want)
	}
}
