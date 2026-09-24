package storage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"
)

type item struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}

func readArray(t *testing.T, path string) []item {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var arr []item
	if err := json.Unmarshal(data, &arr); err != nil {
		t.Fatalf("%s is not a JSON array: %v\n%s", path, err, data)
	}
	return arr
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func write(t *testing.T, s *Store, kind string, items ...item) Result {
	t.Helper()
	w, err := s.Begin(kind)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if err := w.Add(it); err != nil {
			t.Fatal(err)
		}
	}
	res, err := w.Commit()
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestArrayFilePerKind(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out") // created automatically
	s := New(dir)
	write(t, s, KindUsers, item{"1", "Ann"}, item{"2", "Bob"})
	write(t, s, KindProjects, item{"9", "Roadmap"})

	if got := listDir(t, dir); len(got) != 2 || got[0] != "projects.json" || got[1] != "users.json" {
		t.Fatalf("dir: %v", got)
	}
	users := readArray(t, filepath.Join(dir, "users.json"))
	if len(users) != 2 || users[0] != (item{"1", "Ann"}) || users[1] != (item{"2", "Bob"}) {
		t.Fatalf("users: %+v", users)
	}
	if p := readArray(t, filepath.Join(dir, "projects.json")); len(p) != 1 || p[0].Name != "Roadmap" {
		t.Fatalf("projects: %+v", p)
	}
}

func TestEmptyArray(t *testing.T) {
	s := New(t.TempDir())
	write(t, s, KindUsers)
	data, _ := os.ReadFile(s.Path(KindUsers))
	if string(data) != "[]\n" {
		t.Fatalf("got %q", data)
	}
}

func TestRepeatedCommitsReplaceOrSkip(t *testing.T) {
	s := New(t.TempDir())
	if r := write(t, s, KindUsers, item{"1", "Ann"}); r != Written {
		t.Fatal("first commit should write")
	}
	if r := write(t, s, KindUsers, item{"1", "Ann"}); r != Unchanged {
		t.Fatal("identical content should be unchanged")
	}
	if r := write(t, s, KindUsers, item{"1", "Ann B."}); r != Written {
		t.Fatal("changed content should be written")
	}
	if got := readArray(t, s.Path(KindUsers)); len(got) != 1 || got[0].Name != "Ann B." {
		t.Fatalf("not replaced: %+v", got)
	}
	if got := listDir(t, s.dir); len(got) != 1 {
		t.Fatalf("temp files left behind: %v", got)
	}
}

func TestAbortKeepsPreviousFile(t *testing.T) {
	s := New(t.TempDir())
	write(t, s, KindUsers, item{"1", "Ann"})

	w, err := s.Begin(KindUsers)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Add(item{"2", "Half-written"}); err != nil {
		t.Fatal(err)
	}
	w.Abort()
	w.Abort() // idempotent

	if got := readArray(t, s.Path(KindUsers)); len(got) != 1 || got[0].Name != "Ann" {
		t.Fatalf("previous file damaged: %+v", got)
	}
	if got := listDir(t, s.dir); len(got) != 1 {
		t.Fatalf("temp file left behind: %v", got)
	}
	if err := w.Add(item{}); err == nil {
		t.Fatal("Add after Abort must fail")
	}
}

func TestRenameFailureKeepsPreviousFileAndCleansTemp(t *testing.T) {
	s := New(t.TempDir())
	write(t, s, KindUsers, item{"1", "Ann"})
	boom := errors.New("rename failed")
	s.rename = func(string, string) error { return boom }

	w, _ := s.Begin(KindUsers)
	_ = w.Add(item{"1", "Changed"})
	if _, err := w.Commit(); !errors.Is(err, boom) {
		t.Fatalf("want rename error, got %v", err)
	}
	if got := readArray(t, s.Path(KindUsers)); got[0].Name != "Ann" {
		t.Fatalf("previous file damaged: %+v", got)
	}
	if got := listDir(t, s.dir); len(got) != 1 {
		t.Fatalf("temp file left behind: %v", got)
	}
}

func TestUnwritableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits not enforced")
	}
	s := New(t.TempDir())
	write(t, s, KindUsers, item{"1", "Ann"})
	if err := os.Chmod(s.dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(s.dir, 0o755) })

	if _, err := s.Begin(KindUsers); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("want permission error, got %v", err)
	}
	if got := readArray(t, s.Path(KindUsers)); got[0].Name != "Ann" {
		t.Fatalf("previous file damaged: %+v", got)
	}
}

func TestOutputDirIsAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path).Begin(KindProjects); err == nil {
		t.Fatal("expected error")
	}
}

func TestEncodeFailure(t *testing.T) {
	s := New(t.TempDir())
	w, err := s.Begin(KindUsers)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Add(map[string]any{"bad": make(chan int)}); err == nil {
		t.Fatal("expected encode error")
	}
	w.Abort()
	if got := listDir(t, s.dir); len(got) != 0 {
		t.Fatalf("nothing should remain: %v", got)
	}
}

func TestUnknownKind(t *testing.T) {
	if _, err := New(t.TempDir()).Begin("../etc"); err == nil {
		t.Fatal("expected error")
	}
}

func TestCleanupTemp(t *testing.T) {
	s := New(t.TempDir())
	write(t, s, KindUsers, item{"1", "Ann"})
	stale := filepath.Join(s.dir, ".users.json.12345.tmp")
	fresh := filepath.Join(s.dir, ".projects.json.67890.tmp") // e.g. another live process mid-write
	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, []byte(`[{"gid":`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	n, err := s.CleanupTemp(time.Minute)
	if err != nil || n != 1 {
		t.Fatalf("removed=%d err=%v", n, err)
	}
	if got := listDir(t, s.dir); len(got) != 2 || got[0] != ".projects.json.67890.tmp" || got[1] != "users.json" {
		t.Fatalf("dir after cleanup: %v", got)
	}
	if n, err := New(filepath.Join(s.dir, "missing")).CleanupTemp(time.Minute); n != 0 || err != nil {
		t.Fatalf("missing dir: %d %v", n, err)
	}
}
