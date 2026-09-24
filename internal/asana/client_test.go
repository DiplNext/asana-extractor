package asana

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "test-token-do-not-log"

// fakeClock makes pacing and retry waits instant and observable.
type fakeClock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
}

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sleeps = append(f.sleeps, d)
	f.t = f.t.Add(d)
	return nil
}

func (f *fakeClock) slept() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.sleeps...)
}

func newTestClient(t *testing.T, h http.Handler, maxRetries, perMinute int) (*Client, *fakeClock) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(Options{BaseURL: srv.URL, Token: testToken, RateLimitPerMinute: perMinute, MaxRetries: maxRetries, HTTPTimeout: 5 * time.Second})
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	c.now = clock.now
	c.sleep = clock.sleep
	c.jitter = func(d time.Duration) time.Duration { return d } // deterministic upper bound
	return c, clock
}

// recorder captures request metadata safely across server goroutines.
type recorder struct {
	mu   sync.Mutex
	reqs []*http.Request
}

func (r *recorder) add(req *http.Request) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req.Clone(context.Background()))
	return len(r.reqs)
}

func (r *recorder) all() []*http.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*http.Request(nil), r.reqs...)
}

func TestListUsersPaginatesWithFiltersAndFields(t *testing.T) {
	rec := &recorder{}
	pages := map[string]string{
		"":      `{"data":[{"gid":"1","name":"Ann","email":"ann@example.com"},{"gid":"2","name":"Bob"}],"next_page":{"offset":"off-2","path":"/users?offset=off-2","uri":"x"}}`,
		"off-2": `{"data":[{"gid":"3","name":"Cy","email":null}],"next_page":{"offset":"off-3"}}`,
		"off-3": `{"data":[],"next_page":null}`,
	}
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		body, ok := pages[r.URL.Query().Get("offset")]
		if !ok {
			http.Error(w, `{"errors":[{"message":"bad offset"}]}`, http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, body)
	}), 0, 6000)

	var got []User
	pageSizes := []int{}
	err := c.ListUsers(t.Context(), "42", func(p []User) error {
		pageSizes = append(pageSizes, len(p))
		got = append(got, p...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || fmt.Sprint(pageSizes) != "[2 1 0]" {
		t.Fatalf("users=%d pages=%v", len(got), pageSizes)
	}
	if got[0].Email == nil || *got[0].Email != "ann@example.com" || got[1].Email != nil || got[2].Email != nil {
		t.Fatalf("emails not decoded as optional: %+v", got)
	}

	reqs := rec.all()
	if len(reqs) != 3 {
		t.Fatalf("requests=%d", len(reqs))
	}
	for i, r := range reqs {
		q := r.URL.Query()
		if r.URL.Path != "/users" || r.Method != http.MethodGet {
			t.Errorf("req %d: %s %s", i, r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Errorf("req %d: auth header %q", i, got)
		}
		if q.Get("workspace") != "42" || q.Get("limit") != "100" || q.Get("opt_fields") != userFields {
			t.Errorf("req %d: query lost filters: %s", i, r.URL.RawQuery)
		}
	}
	if reqs[0].URL.Query().Has("offset") || reqs[1].URL.Query().Get("offset") != "off-2" || reqs[2].URL.Query().Get("offset") != "off-3" {
		t.Error("offsets not propagated verbatim")
	}
}

func TestListProjectsOptionalFields(t *testing.T) {
	var query string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		fmt.Fprint(w, `{"data":[
			{"gid":"10","name":"Full","archived":true,"completed":false,"color":"dark-red","created_at":"2026-01-01T00:00:00.000Z",
			 "owner":{"gid":"1","name":"Ann"},"team":{"gid":"7","name":"Eng"},"workspace":{"gid":"42","name":"WS"},"privacy_setting":"public_to_workspace"},
			{"gid":"11","name":"Sparse","owner":null,"color":null}
		],"next_page":null}`)
	}), 0, 6000)

	var got []Project
	if err := c.ListProjects(t.Context(), "42", func(p []Project) error { got = append(got, p...); return nil }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, "workspace=42") || strings.Contains(query, "archived=") {
		t.Errorf("projects must be workspace-scoped and include archived: %s", query)
	}
	if !strings.Contains(query, "opt_fields=") {
		t.Errorf("missing opt_fields: %s", query)
	}
	full, sparse := got[0], got[1]
	if !full.Archived || full.Owner.Name != "Ann" || full.Team.GID != "7" || *full.Color != "dark-red" {
		t.Errorf("full project decoded wrong: %+v", full)
	}
	if sparse.Owner != nil || sparse.Team != nil || sparse.Color != nil || sparse.CreatedAt != nil || sparse.Archived {
		t.Errorf("sparse project should have nil optionals: %+v", sparse)
	}
}

func TestEmptyResult(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[],"next_page":null}`)
	}), 0, 6000)
	calls := 0
	if err := c.ListUsers(t.Context(), "42", func(p []User) error { calls++; return nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("callback calls=%d", calls)
	}
}

func TestPacingSpacesRequests(t *testing.T) {
	var mu sync.Mutex
	var times []time.Time
	var c *Client
	var clock *fakeClock
	c, clock = newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, clock.now())
		n := len(times)
		mu.Unlock()
		if n < 3 {
			fmt.Fprintf(w, `{"data":[],"next_page":{"offset":"o%d"}}`, n)
			return
		}
		fmt.Fprint(w, `{"data":[],"next_page":null}`)
	}), 0, 60) // 1 request per second

	if err := c.ListUsers(t.Context(), "42", func([]User) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(times); i++ {
		if gap := times[i].Sub(times[i-1]); gap < time.Second {
			t.Errorf("request %d started %s after previous, want >= 1s", i, gap)
		}
	}
}

func TestRetryOn429(t *testing.T) {
	for name, tc := range map[string]struct {
		header  string
		wantMin time.Duration
		wantMax time.Duration
	}{
		"valid seconds":    {"7", 7 * time.Second, 7 * time.Second},
		"zero seconds":     {"0", 0, 0},
		"missing":          {"", 500 * time.Millisecond, time.Second},
		"malformed text":   {"soon", 500 * time.Millisecond, time.Second},
		"negative seconds": {"-5", 500 * time.Millisecond, time.Second},
		"http date":        {"Thu, 01 Jan 2026 00:00:20 GMT", 20 * time.Second, 20 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var times []time.Time
			var clock *fakeClock
			var c *Client
			c, clock = newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				times = append(times, clock.now())
				n := len(times)
				mu.Unlock()
				if n == 1 {
					if tc.header != "" {
						w.Header().Set("Retry-After", tc.header)
					}
					w.WriteHeader(http.StatusTooManyRequests)
					fmt.Fprint(w, `{"errors":[{"message":"rate limited"}]}`)
					return
				}
				fmt.Fprint(w, `{"data":[],"next_page":null}`)
			}), 3, 60000)

			if err := c.ListUsers(t.Context(), "42", func([]User) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if len(times) != 2 {
				t.Fatalf("attempts=%d", len(times))
			}
			gap := times[1].Sub(times[0])
			if gap < tc.wantMin || gap > tc.wantMax+time.Millisecond {
				t.Errorf("retry after %s, want within [%s, %s]", gap, tc.wantMin, tc.wantMax)
			}
			if c.Retries() != 1 {
				t.Errorf("retries=%d", c.Retries())
			}
		})
	}
}

func TestLongRetryAfterIsNotCapped(t *testing.T) {
	var mu sync.Mutex
	var times []time.Time
	var clock *fakeClock
	var c *Client
	c, clock = newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, clock.now())
		n := len(times)
		mu.Unlock()
		if n == 1 {
			w.Header().Set("Retry-After", "120") // far beyond backoffCap
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"data":[],"next_page":null}`)
	}), 1, 60000)
	if err := c.ListUsers(t.Context(), "42", func([]User) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if gap := times[1].Sub(times[0]); gap < 120*time.Second {
		t.Fatalf("retried after %s, server required 120s", gap)
	}
}

func TestRetryableFailures(t *testing.T) {
	for _, status := range []int{500, 502, 503, 504} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			attempts := 0
			c, clock := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				if attempts < 3 {
					w.WriteHeader(status)
					return
				}
				fmt.Fprint(w, `{"data":[],"next_page":null}`)
			}), 5, 60000)
			if err := c.ListUsers(t.Context(), "42", func([]User) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if attempts != 3 || c.Retries() != 2 {
				t.Fatalf("attempts=%d retries=%d", attempts, c.Retries())
			}
			// Exponential with jitter pinned to its max: 1s then 2s (plus tiny pacer gaps).
			var backoffs []time.Duration
			for _, d := range clock.slept() {
				if d >= 500*time.Millisecond {
					backoffs = append(backoffs, d)
				}
			}
			if fmt.Sprint(backoffs) != "[1s 2s]" {
				t.Errorf("backoffs=%v", backoffs)
			}
		})
	}
}

func TestRetryExhaustion(t *testing.T) {
	attempts := 0
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"errors":[{"message":"try later"}]}`)
	}), 2, 60000)
	err := c.ListUsers(t.Context(), "42", func([]User) error { return nil })
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 503 {
		t.Fatalf("want APIError 503, got %v", err)
	}
	if attempts != 3 || !strings.Contains(err.Error(), "giving up after 3 attempts") || !strings.Contains(err.Error(), "list users page 1") {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

func TestNonRetryableErrors(t *testing.T) {
	for _, status := range []int{400, 401, 402, 403, 404} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			attempts := 0
			c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				w.WriteHeader(status)
				fmt.Fprint(w, `{"errors":[{"message":"nope"}]}`)
			}), 5, 60000)
			err := c.ListProjects(t.Context(), "42", func([]Project) error { return nil })
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != status || apiErr.Message != "nope" {
				t.Fatalf("got %v", err)
			}
			if attempts != 1 {
				t.Fatalf("attempts=%d", attempts)
			}
			if strings.Contains(err.Error(), testToken) {
				t.Fatal("error leaks token")
			}
			if !strings.Contains(err.Error(), "list projects page 1") {
				t.Fatalf("missing op context: %v", err)
			}
		})
	}
}

func TestMalformedJSON(t *testing.T) {
	for name, body := range map[string]string{
		"truncated":  `{"data":[{"gid":"1"`,
		"wrong type": `{"data":"users"}`,
		"html":       `<html>oops</html>`,
	} {
		t.Run(name, func(t *testing.T) {
			attempts := 0
			c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				fmt.Fprint(w, body)
			}), 3, 60000)
			err := c.ListUsers(t.Context(), "42", func([]User) error { return nil })
			if err == nil || !strings.Contains(err.Error(), "decode response") {
				t.Fatalf("got %v", err)
			}
			if attempts != 1 {
				t.Fatalf("malformed JSON must not be retried, attempts=%d", attempts)
			}
		})
	}
}

func TestTransportFailureIsRetried(t *testing.T) {
	attempts := 0
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close() // connection reset without a response
			}
			return
		}
		fmt.Fprint(w, `{"data":[],"next_page":null}`)
	}), 2, 60000)
	if err := c.ListUsers(t.Context(), "42", func([]User) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts=%d", attempts)
	}
}

func TestInvalidNextPage(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[],"next_page":{"offset":"same"}}`)
	}), 0, 60000)
	err := c.ListUsers(t.Context(), "42", func([]User) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "invalid next_page") {
		t.Fatalf("got %v", err)
	}
}

func TestCallbackErrorStopsPaging(t *testing.T) {
	attempts := 0
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		fmt.Fprintf(w, `{"data":[{"gid":"%d"}],"next_page":{"offset":"o%d"}}`, attempts, attempts)
	}), 0, 60000)
	boom := errors.New("disk full")
	err := c.ListUsers(t.Context(), "42", func([]User) error { return boom })
	if !errors.Is(err, boom) || attempts != 1 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}

func TestCancelDuringRequest(t *testing.T) {
	arrived := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := New(Options{BaseURL: srv.URL, Token: testToken, RateLimitPerMinute: 60000, MaxRetries: 5, HTTPTimeout: time.Minute})

	ctx, cancel := context.WithCancel(t.Context())
	go func() { <-arrived; cancel() }()
	start := time.Now()
	err := c.ListUsers(ctx, "42", func([]User) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if time.Since(start) > 5*time.Second || c.Retries() != 0 {
		t.Fatalf("cancellation not prompt or retried: %s retries=%d", time.Since(start), c.Retries())
	}
}

func TestCancelDuringRetryWait(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"429 cooldown": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusTooManyRequests)
		},
		"5xx backoff": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		},
	} {
		t.Run(name, func(t *testing.T) {
			attempts := make(chan struct{}, 10)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts <- struct{}{}
				handler(w, r)
			}))
			defer srv.Close()
			c := New(Options{BaseURL: srv.URL, Token: testToken, RateLimitPerMinute: 60000, MaxRetries: 5})
			c.jitter = func(d time.Duration) time.Duration { return d }
			c.sleep = func(ctx context.Context, d time.Duration) error { return sleepCtx(ctx, 10*d) } // 5xx wait ≈ 10s

			ctx, cancel := context.WithCancel(t.Context())
			go func() { <-attempts; time.Sleep(50 * time.Millisecond); cancel() }()
			start := time.Now()
			err := c.ListUsers(ctx, "42", func([]User) error { return nil })
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("want context.Canceled, got %v", err)
			}
			if time.Since(start) > 3*time.Second {
				t.Fatalf("wait not interrupted promptly: %s", time.Since(start))
			}
			if len(attempts) != 0 {
				t.Fatalf("made %d extra attempts after cancel", len(attempts))
			}
		})
	}
}

func TestCooldownAppliesAfterRetriesExhausted(t *testing.T) {
	var mu sync.Mutex
	var times []time.Time
	var clock *fakeClock
	var c *Client
	c, clock = newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, clock.now())
		n := len(times)
		mu.Unlock()
		if n == 1 {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"data":[],"next_page":null}`)
	}), 0, 60000) // no retries: the first 429 ends the call

	if err := c.ListUsers(t.Context(), "42", func([]User) error { return nil }); err == nil {
		t.Fatal("expected 429 error")
	}
	if err := c.ListProjects(t.Context(), "42", func([]Project) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if gap := times[1].Sub(times[0]); gap < 30*time.Second {
		t.Fatalf("next request started %s after a 429 with Retry-After: 30", gap)
	}
}

func TestHugeRetryAfterDoesNotOverflow(t *testing.T) {
	d, ok := parseRetryAfter("99999999999999999", time.Now())
	if !ok || d <= 0 {
		t.Fatalf("got %s ok=%v", d, ok)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var hits sync.Map
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Store("target", r.Header.Get("Authorization"))
	}))
	defer target.Close()
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/users", http.StatusFound)
	}), 0, 60000)

	err := c.ListUsers(t.Context(), "42", func([]User) error { return nil })
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusFound {
		t.Fatalf("want non-retried 302 APIError, got %v", err)
	}
	if _, followed := hits.Load("target"); followed {
		t.Fatal("redirect was followed")
	}
}
