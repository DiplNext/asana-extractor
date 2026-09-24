package extractor

import (
	"bytes"
	"context"
	"encoding/json"
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

func readArray(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var arr []map[string]any
	if err := json.Unmarshal(data, &arr); err != nil {
		t.Fatalf("%s: not a JSON array: %v", path, err)
	}
	return arr
}

func TestRunWritesEveryResourceAcrossPages(t *testing.T) {
	h := newHarness(t)
	h.fake.SetUsers(asanatest.Users(250)...)
	h.fake.SetProjects(asanatest.Projects(3)...)

	st, err := h.ex.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.Users != 250 || st.Projects != 3 || st.Written != 2 || st.Unchanged != 0 {
		t.Fatalf("stats: %+v", st)
	}
	if h.fake.Requests("/users") != 3 || h.fake.Requests("/projects") != 1 {
		t.Fatalf("requests users=%d projects=%d", h.fake.Requests("/users"), h.fake.Requests("/projects"))
	}
	users := readArray(t, filepath.Join(h.out, "users.json"))
	if len(users) != 250 || users[0]["gid"] != "1000" || users[249]["gid"] != "1249" {
		t.Fatalf("users array: len=%d", len(users))
	}
	if len(readArray(t, filepath.Join(h.out, "projects.json"))) != 3 {
		t.Fatal("projects array")
	}
	if !strings.Contains(h.logs.String(), "status=success") {
		t.Fatalf("missing success log: %s", h.logs)
	}
}

func TestRepeatedRunsReplaceArrays(t *testing.T) {
	h := newHarness(t)
	users := asanatest.Users(3)
	h.fake.SetUsers(users...)
	if _, err := h.ex.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	st, err := h.ex.Run(t.Context())
	if err != nil || st.Written != 0 || st.Unchanged != 2 {
		t.Fatalf("identical rerun: %+v %v", st, err)
	}

	users[0]["name"] = "Renamed"
	h.fake.SetUsers(users[:2]...) // user 1002 disappears
	st, err = h.ex.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.Written != 1 || st.Unchanged != 1 {
		t.Fatalf("stats: %+v", st)
	}
	got := readArray(t, filepath.Join(h.out, "users.json"))
	if len(got) != 2 || got[0]["name"] != "Renamed" {
		t.Fatalf("users.json must be the latest full listing: %v", got)
	}
}

func TestFailedListingKeepsPreviousFileAndStillExtractsProjects(t *testing.T) {
	h := newHarness(t)
	h.fake.SetUsers(asanatest.Users(150)...)
	h.fake.SetProjects(asanatest.Projects(2)...)
	if _, err := h.ex.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	// Next run: page 1 of users succeeds, page 2 fails permanently.
	h.fake.SetUsers(asanatest.Users(160)...)
	h.fake.SetPageSize(100)
	h.fake.FailAfter("/users", 1, asanatest.Failure{Status: 403})
	h.fake.SetProjects(asanatest.Projects(4)...)

	st, err := h.ex.Run(t.Context())
	var apiErr *asana.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 403 || !strings.Contains(err.Error(), "list users page 2") {
		t.Fatalf("got %v", err)
	}
	if got := readArray(t, filepath.Join(h.out, "users.json")); len(got) != 150 {
		t.Fatalf("users.json must keep the previous complete listing, has %d", len(got))
	}
	if st.Projects != 4 || len(readArray(t, filepath.Join(h.out, "projects.json"))) != 4 {
		t.Fatalf("projects should still be extracted: %+v", st)
	}
	entries, _ := os.ReadDir(h.out)
	if len(entries) != 2 {
		t.Fatalf("temp file left behind: %v", entries)
	}
	if !strings.Contains(h.logs.String(), "status=failed") {
		t.Fatalf("failed run not marked: %s", h.logs)
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
	if err := os.WriteFile(h.out, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := h.ex.Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "create directory") {
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
