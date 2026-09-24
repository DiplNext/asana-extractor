package main

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"asana-extractor/internal/asanatest"
)

const (
	token = "e2e-SECRET-token"
	ws    = "1200000000000001"
)

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func env(fake *asanatest.Server, out string, extra map[string]string) func(string) string {
	m := map[string]string{
		"ASANA_ACCESS_TOKEN":          token,
		"ASANA_WORKSPACE_GID":         ws,
		"ASANA_BASE_URL":              fake.URL,
		"ASANA_OUTPUT_DIR":            out,
		"ASANA_RATE_LIMIT_PER_MINUTE": "1500",
	}
	maps.Copy(m, extra)
	return func(k string) string { return m[k] }
}

func noFiles(string) ([]byte, error) { return nil, os.ErrNotExist }

func readObject(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("%s: not a single JSON object: %v", path, err)
	}
	return obj
}

func dirNames(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return strings.Join(names, ",")
}

func TestOnceEndToEnd(t *testing.T) {
	fake := asanatest.New(t, token, ws)
	fake.SetPageSize(2) // force pagination
	fake.SetUsers(
		map[string]any{"gid": "101", "name": "Ann", "email": "ann@example.com"},
		map[string]any{"gid": "102", "name": "Bob"}, // email not visible
		map[string]any{"gid": "103", "name": "Cy", "email": "cy@example.com"},
	)
	fake.SetProjects(
		map[string]any{"gid": "201", "name": "Launch", "archived": false, "color": "light-green",
			"owner": map[string]any{"gid": "101", "name": "Ann"}, "workspace": map[string]any{"gid": ws, "name": "Test WS"},
			"internal_field": "must not be requested"},
		map[string]any{"gid": "202", "name": "Old", "archived": true, "owner": nil},
	)
	out := filepath.Join(t.TempDir(), "output")
	logs := &syncBuffer{}

	code := run(t.Context(), []string{"--once"}, env(fake, out, nil), noFiles, logs)
	if code != exitOK {
		t.Fatalf("exit=%d logs:\n%s", code, logs)
	}

	// output/users/<gid>.json and output/projects/<gid>.json, one object per file.
	if got := dirNames(t, out); got != "projects,users" {
		t.Fatalf("output dir: %s", got)
	}
	if got := dirNames(t, filepath.Join(out, "users")); got != "101.json,102.json,103.json" {
		t.Fatalf("users dir: %s", got)
	}
	if got := dirNames(t, filepath.Join(out, "projects")); got != "201.json,202.json" {
		t.Fatalf("projects dir: %s", got)
	}
	for _, gid := range []string{"101", "102", "103"} {
		if obj := readObject(t, filepath.Join(out, "users", gid+".json")); obj["gid"] != gid || obj["data"] != nil {
			t.Fatalf("user %s: %v", gid, obj)
		}
	}
	if u := readObject(t, filepath.Join(out, "users", "102.json")); u["email"] != nil {
		t.Fatalf("missing email should be null: %v", u)
	}
	p := readObject(t, filepath.Join(out, "projects", "201.json"))
	if p["name"] != "Launch" || p["owner"].(map[string]any)["name"] != "Ann" || p["internal_field"] != nil {
		t.Fatalf("project 201: %v", p)
	}
	if p := readObject(t, filepath.Join(out, "projects", "202.json")); p["archived"] != true || p["owner"] != nil {
		t.Fatalf("archived project must be exported: %v", p)
	}
	if fake.Requests("/users") != 2 || fake.Requests("/projects") != 1 {
		t.Fatalf("requests users=%d projects=%d", fake.Requests("/users"), fake.Requests("/projects"))
	}
	if strings.Contains(logs.String(), token) {
		t.Fatal("token leaked into logs")
	}
}

func TestOnceFailureExitsNonZero(t *testing.T) {
	fake := asanatest.New(t, "other-token", ws) // our token is rejected with 401
	logs := &syncBuffer{}
	code := run(t.Context(), []string{"--once"}, env(fake, t.TempDir(), nil), noFiles, logs)
	if code != exitFailure {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(logs.String(), "status=failed") || !strings.Contains(logs.String(), "401") {
		t.Fatalf("failure not logged clearly:\n%s", logs)
	}
	if strings.Contains(logs.String(), token) {
		t.Fatal("token leaked into logs")
	}
}

func TestInvalidConfigFailsImmediately(t *testing.T) {
	fake := asanatest.New(t, token, ws)
	logs := &syncBuffer{}
	code := run(t.Context(), []string{"--interval=banana"}, env(fake, t.TempDir(), nil), noFiles, logs)
	if code != exitConfig || !strings.Contains(logs.String(), "configuration error") {
		t.Fatalf("exit=%d logs=%s", code, logs)
	}
	if fake.Requests("/users") != 0 {
		t.Fatal("no API calls expected on bad config")
	}
}

func TestPeriodicRecoversAfterFailedRunAndStopsOnCancel(t *testing.T) {
	fake := asanatest.New(t, token, ws)
	fake.SetUsers(asanatest.Users(2)...)
	fake.SetProjects(asanatest.Projects(1)...)
	fake.Fail("/users", asanatest.Failure{Status: 403}) // first run fails for users
	out := filepath.Join(t.TempDir(), "output")
	logs := &syncBuffer{}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"--interval=1s"}, env(fake, out, nil), noFiles, logs) }()

	deadline := time.After(10 * time.Second)
	for !strings.Contains(logs.String(), "status=success") {
		select {
		case code := <-done:
			t.Fatalf("exited early with %d:\n%s", code, logs)
		case <-deadline:
			t.Fatalf("no successful run after failure:\n%s", logs)
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	select {
	case code := <-done:
		if code != exitOK {
			t.Fatalf("exit=%d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not shut down after cancel")
	}

	l := logs.String()
	failedAt, successAt := strings.Index(l, "status=failed"), strings.Index(l, "status=success")
	if failedAt < 0 || failedAt > successAt {
		t.Fatalf("expected failed run before success:\n%s", l)
	}
	if _, err := os.Stat(filepath.Join(out, "users", "1000.json")); err != nil {
		t.Fatal("user file missing after recovery")
	}
	if !strings.Contains(l, "shutdown complete") {
		t.Fatalf("no clean shutdown log:\n%s", l)
	}
}

func TestInterruptDuringOnceRun(t *testing.T) {
	fake := asanatest.New(t, token, ws)
	fake.Fail("/users", asanatest.Failure{Status: 429, RetryAfter: "3600"})
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)

	start := time.Now()
	code := run(ctx, []string{"--once"}, env(fake, t.TempDir(), nil), noFiles, logs)
	if code != exitFailure || time.Since(start) > 5*time.Second {
		t.Fatalf("exit=%d after %s", code, time.Since(start))
	}
	if !strings.Contains(logs.String(), "context canceled") {
		t.Fatalf("cancellation not reported:\n%s", logs)
	}
}

func TestEnvFileIsLoaded(t *testing.T) {
	fake := asanatest.New(t, token, ws)
	fake.SetUsers(asanatest.Users(1)...)
	out := filepath.Join(t.TempDir(), "output")
	envFile := "ASANA_ACCESS_TOKEN=" + token + "\nASANA_WORKSPACE_GID=" + ws + "\n"
	getenv := func(k string) string {
		switch k {
		case "ASANA_BASE_URL":
			return fake.URL
		case "ASANA_OUTPUT_DIR":
			return out
		case "ASANA_RATE_LIMIT_PER_MINUTE":
			return "1500"
		}
		return ""
	}
	readFile := func(p string) ([]byte, error) {
		if p == ".env" {
			return []byte(envFile), nil
		}
		return nil, os.ErrNotExist
	}
	logs := &syncBuffer{}
	if code := run(t.Context(), []string{"--once"}, getenv, readFile, logs); code != exitOK {
		t.Fatalf("exit=%d logs:\n%s", code, logs)
	}
	if _, err := os.Stat(filepath.Join(out, "users", "1000.json")); err != nil {
		t.Fatal(err)
	}
}
