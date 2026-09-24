package extractor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"asana-extractor/internal/asana"
	"asana-extractor/internal/asanatest"
	"asana-extractor/internal/storage"
)

const (
	token = "tok-SECRET"
	ws    = "42"
)

type harness struct {
	fake *asanatest.Server
	out  string
	logs *bytes.Buffer
	ex   *Extractor
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	fake := asanatest.New(t, token, ws)
	out := filepath.Join(t.TempDir(), "output")
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := asana.New(asana.Options{BaseURL: fake.URL, Token: token, RateLimitPerMinute: 600000, MaxRetries: 3, HTTPTimeout: 5 * time.Second, Logger: log})
	return &harness{fake: fake, out: out, logs: logs, ex: New(client, storage.New(out), ws, log)}
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestRunWritesEveryResourceAcrossPages(t *testing.T) {
	h := newHarness(t)
	h.fake.SetUsers(asanatest.Users(250)...)
	h.fake.SetProjects(asanatest.Projects(3)...)

	st, err := h.ex.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.Users != 250 || st.Projects != 3 || st.Written != 253 || st.Unchanged != 0 {
		t.Fatalf("stats: %+v", st)
	}
	if h.fake.Requests("/users") != 3 || h.fake.Requests("/projects") != 1 {
		t.Fatalf("requests users=%d projects=%d", h.fake.Requests("/users"), h.fake.Requests("/projects"))
	}
	if countFiles(t, filepath.Join(h.out, "users")) != 250 || countFiles(t, filepath.Join(h.out, "projects")) != 3 {
		t.Fatal("wrong file counts")
	}
	if !strings.Contains(h.logs.String(), "status=success") {
		t.Fatalf("missing success log: %s", h.logs)
	}
}

func TestRepeatedRunsUpsertAndKeepStaleFiles(t *testing.T) {
	h := newHarness(t)
	users := asanatest.Users(3)
	h.fake.SetUsers(users...)
	if _, err := h.ex.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	users[0]["name"] = "Renamed"
	h.fake.SetUsers(users[:2]...) // user 1002 disappears
	st, err := h.ex.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.Written != 1 || st.Unchanged != 1 {
		t.Fatalf("stats: %+v", st)
	}
	data, err := os.ReadFile(filepath.Join(h.out, "users", "1000.json"))
	if err != nil || !strings.Contains(string(data), `"Renamed"`) {
		t.Fatalf("user not updated: %s %v", data, err)
	}
	if countFiles(t, filepath.Join(h.out, "users")) != 3 {
		t.Fatal("absent resource file must be preserved")
	}
}

func TestProjectsAttemptedWhenUsersFail(t *testing.T) {
	h := newHarness(t)
	h.fake.SetUsers(asanatest.Users(2)...)
	h.fake.SetProjects(asanatest.Projects(2)...)
	h.fake.Fail("/users", asanatest.Failure{Status: 403})

	st, err := h.ex.Run(t.Context())
	var apiErr *asana.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 403 || !strings.Contains(err.Error(), "list users") {
		t.Fatalf("got %v", err)
	}
	if st.Projects != 2 || countFiles(t, filepath.Join(h.out, "projects")) != 2 {
		t.Fatalf("projects should still be extracted: %+v", st)
	}
	if !strings.Contains(h.logs.String(), "status=failed") {
		t.Fatalf("failed run not marked: %s", h.logs)
	}
}

func TestFailureMidListingKeepsWrittenPages(t *testing.T) {
	h := newHarness(t)
	h.fake.SetUsers(asanatest.Users(160)...)
	h.fake.FailAfter("/users", 1, asanatest.Failure{Status: 403}) // page 2 fails

	st, err := h.ex.Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "list users page 2") {
		t.Fatalf("got %v", err)
	}
	if st.Users != 100 || countFiles(t, filepath.Join(h.out, "users")) != 100 {
		t.Fatalf("page 1 files should be kept (per-file consistency): %+v", st)
	}
}

func TestRetriesAreCounted(t *testing.T) {
	h := newHarness(t)
	h.fake.SetUsers(asanatest.Users(1)...)
	h.fake.Fail("/users", asanatest.Failure{Status: 429, RetryAfter: "0"})
	h.fake.Fail("/projects", asanatest.Failure{Status: 429, RetryAfter: "0"}, asanatest.Failure{Status: 429, RetryAfter: "0"})

	st, err := h.ex.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.Retries != 3 {
		t.Fatalf("retries=%d", st.Retries)
	}
	if !strings.Contains(h.logs.String(), "retries=3") {
		t.Fatalf("retry count not logged: %s", h.logs)
	}
}

func TestStorageFailureFailsRun(t *testing.T) {
	h := newHarness(t)
	h.fake.SetUsers(asanatest.Users(1)...)
	if err := os.MkdirAll(h.out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.out, "users"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := h.ex.Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "list users page 1") {
		t.Fatalf("got %v", err)
	}
}

func TestLogsContainNoSecretsOrPayloads(t *testing.T) {
	h := newHarness(t)
	h.fake.SetUsers(asanatest.Users(2)...)
	h.fake.Fail("/users", asanatest.Failure{Status: 401})
	_, _ = h.ex.Run(t.Context())
	_, _ = h.ex.Run(t.Context())
	logs := h.logs.String()
	if strings.Contains(logs, token) || strings.Contains(logs, "user0@example.com") {
		t.Fatalf("logs leak token or payload:\n%s", logs)
	}
}

func TestCanceledRunIsMarkedCanceled(t *testing.T) {
	h := newHarness(t)
	h.fake.Fail("/users", asanatest.Failure{Status: 429, RetryAfter: "3600"})
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)

	_, err := h.ex.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(h.logs.String(), "status=canceled") || strings.Contains(h.logs.String(), "status=failed") {
		t.Fatalf("logs:\n%s", h.logs)
	}
	if h.fake.Requests("/projects") != 0 {
		t.Fatal("projects must not be attempted after cancellation")
	}
}
