// Package asanatest provides an in-memory fake of the Asana list endpoints
// used by the extractor, for offline tests.
package asanatest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Failure is a canned error response.
type Failure struct {
	Status     int
	RetryAfter string
}

type Server struct {
	URL string

	token     string
	workspace string

	mu       sync.Mutex
	users    []map[string]any
	projects []map[string]any
	pageSize int
	failures map[string][]Failure // path -> queued failures
	failFrom map[string]int       // path -> request number from which delayed failures apply
	requests map[string]int
}

// New starts a fake that accepts only the given token and workspace GID.
// Default page size is 100, like the real API's maximum.
func New(t testing.TB, token, workspace string) *Server {
	s := &Server{token: token, workspace: workspace, pageSize: 100, failures: map[string][]Failure{}, failFrom: map[string]int{}, requests: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

func (s *Server) SetUsers(u ...map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users = u
}

func (s *Server) SetProjects(p ...map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.projects = p
}

// SetPageSize caps items per page below the requested limit to force pagination.
func (s *Server) SetPageSize(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pageSize = n
}

// Fail queues failures for the next requests to path ("/users" or "/projects").
func (s *Server) Fail(path string, f ...Failure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[path] = append(s.failures[path], f...)
}

// FailAfter lets the next n requests to path succeed, then serves the
// queued failures (e.g. to fail page 2 of a listing).
func (s *Server) FailAfter(path string, n int, f ...Failure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failFrom[path] = s.requests[path] + n + 1
	s.failures[path] = append(s.failures[path], f...)
}

// Requests returns how many requests hit path.
func (s *Server) Requests(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[path]
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests[r.URL.Path]++
	w.Header().Set("Content-Type", "application/json")

	if q := s.failures[r.URL.Path]; len(q) > 0 && s.requests[r.URL.Path] >= s.failFrom[r.URL.Path] {
		f := q[0]
		s.failures[r.URL.Path] = q[1:]
		if f.RetryAfter != "" {
			w.Header().Set("Retry-After", f.RetryAfter)
		}
		writeError(w, f.Status, "injected failure")
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.token {
		writeError(w, http.StatusUnauthorized, "Not Authorized")
		return
	}
	q := r.URL.Query()
	if q.Get("workspace") != s.workspace {
		writeError(w, http.StatusBadRequest, "workspace: Not a recognized ID")
		return
	}

	var items []map[string]any
	switch r.URL.Path {
	case "/users":
		items = s.users
	case "/projects":
		items = s.projects
	default:
		writeError(w, http.StatusNotFound, "Not found")
		return
	}

	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit < 1 || limit > 100 {
		writeError(w, http.StatusBadRequest, "limit: must be between 1 and 100")
		return
	}
	limit = min(limit, s.pageSize)
	start := 0
	if off := q.Get("offset"); off != "" {
		start, err = strconv.Atoi(strings.TrimPrefix(off, "tok-"))
		if err != nil || start > len(items) {
			writeError(w, http.StatusBadRequest, "offset: Your pagination token is invalid")
			return
		}
	}
	end := min(start+limit, len(items))

	resp := map[string]any{"data": selectFields(items[start:end], q.Get("opt_fields")), "next_page": nil}
	if end < len(items) {
		resp["next_page"] = map[string]any{"offset": fmt.Sprintf("tok-%d", end)}
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// selectFields mimics opt_fields: only gid plus requested top-level fields are returned.
func selectFields(items []map[string]any, optFields string) []map[string]any {
	want := map[string]bool{"gid": true}
	for f := range strings.SplitSeq(optFields, ",") {
		top, _, _ := strings.Cut(f, ".")
		want[top] = true
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		m := map[string]any{}
		for k, v := range it {
			if want[k] {
				m[k] = v
			}
		}
		out = append(out, m)
	}
	return out
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"errors":[{"message":%q}]}`, msg)
}

// Users returns n fake users with sequential GIDs starting at 1000.
func Users(n int) []map[string]any {
	out := make([]map[string]any, n)
	for i := range n {
		out[i] = map[string]any{"gid": strconv.Itoa(1000 + i), "name": fmt.Sprintf("User %d", i), "email": fmt.Sprintf("user%d@example.com", i)}
	}
	return out
}

// Projects returns n fake projects with sequential GIDs starting at 5000.
func Projects(n int) []map[string]any {
	out := make([]map[string]any, n)
	for i := range n {
		out[i] = map[string]any{"gid": strconv.Itoa(5000 + i), "name": fmt.Sprintf("Project %d", i), "archived": i%2 == 1}
	}
	return out
}
