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

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("%s is not a JSON object: %v", path, err)
	}
	return m
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

func TestPutCreatesOneObjectPerFile(t *testing.T) {
	dir := t.TempDir()
	s := New(filepath.Join(dir, "out")) // parent created automatically
	for _, it := range []item{{"1", "Ann"}, {"2", "Bob"}} {
		if _, err := s.Put(KindUsers, it.GID, it); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Put(KindProjects, "9", item{"9", "Roadmap"}); err != nil {
		t.Fatal(err)
	}

	if got := listDir(t, filepath.Join(dir, "out", "users")); len(got) != 2 || got[0] != "1.json" || got[1] != "2.json" {
		t.Fatalf("users dir: %v", got)
	}
	m := readJSON(t, s.Path(KindUsers, "2"))
	if m["gid"] != "2" || m["name"] != "Bob" {
		t.Fatalf("content: %v", m)
	}
	if readJSON(t, s.Path(KindProjects, "9"))["name"] != "Roadmap" {
		t.Fatal("project not written")
	}
}

func TestPutUpsertsAndSkipsIdentical(t *testing.T) {
	s := New(t.TempDir())
	if r, _ := s.Put(KindUsers, "1", item{"1", "Ann"}); r != Written {
		t.Fatal("first write should write")
	}
	if r, _ := s.Put(KindUsers, "1", item{"1", "Ann"}); r != Unchanged {
		t.Fatal("identical content should be unchanged")
	}
	if r, _ := s.Put(KindUsers, "1", item{"1", "Ann B."}); r != Written {
		t.Fatal("changed content should be written")
	}
	if got := readJSON(t, s.Path(KindUsers, "1"))["name"]; got != "Ann B." {
		t.Fatalf("not updated: %v", got)
	}
	if got := listDir(t, filepath.Join(s.dir, "users")); len(got) != 1 {
		t.Fatalf("stable filename expected, got %v", got)
	}
}

func TestPutRejectsUnsafeGIDs(t *testing.T) {
	s := New(t.TempDir())
	for _, gid := range []string{"", "..", "../x", "a/b", `a\b`, "1.json", "has space"} {
		if _, err := s.Put(KindUsers, gid, item{}); !errors.Is(err, ErrInvalidGID) {
			t.Errorf("gid %q: want ErrInvalidGID, got %v", gid, err)
		}
	}
	if _, err := os.Stat(filepath.Join(s.dir, "users")); !os.IsNotExist(err) {
		t.Fatal("nothing should be created for invalid gids")
	}
}

func TestPutRejectsUnknownKind(t *testing.T) {
	if _, err := New(t.TempDir()).Put("../etc", "1", item{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestRenameFailureKeepsExistingFileAndCleansTemp(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Put(KindUsers, "1", item{"1", "Ann"}); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("rename failed")
	s.rename = func(string, string) error { return boom }

	_, err := s.Put(KindUsers, "1", item{"1", "Changed"})
	if !errors.Is(err, boom) {
		t.Fatalf("want rename error, got %v", err)
	}
	if got := readJSON(t, s.Path(KindUsers, "1"))["name"]; got != "Ann" {
		t.Fatalf("existing file damaged: %v", got)
	}
	if got := listDir(t, filepath.Join(s.dir, "users")); len(got) != 1 {
		t.Fatalf("temp file left behind: %v", got)
	}
}

func TestUnwritableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits not enforced")
	}
	s := New(t.TempDir())
	if _, err := s.Put(KindUsers, "1", item{"1", "Ann"}); err != nil {
		t.Fatal(err)
	}
	usersDir := filepath.Join(s.dir, "users")
	if err := os.Chmod(usersDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(usersDir, 0o755) })

	_, err := s.Put(KindUsers, "1", item{"1", "Changed"})
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("want permission error, got %v", err)
	}
	if got := readJSON(t, s.Path(KindUsers, "1"))["name"]; got != "Ann" {
		t.Fatalf("existing file damaged: %v", got)
	}
}

func TestDirectoryCreationFailure(t *testing.T) {
	root := t.TempDir()
	// A regular file where the kind directory should be.
	if err := os.WriteFile(filepath.Join(root, "projects"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New(root).Put(KindProjects, "1", item{"1", "P"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestEncodeFailureWritesNothing(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Put(KindUsers, "1", map[string]any{"bad": make(chan int)}); err == nil {
		t.Fatal("expected encode error")
	}
	if _, err := os.Stat(filepath.Join(s.dir, "users")); !os.IsNotExist(err) {
		t.Fatal("nothing should be created")
	}
}

func TestCleanupTemp(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Put(KindUsers, "1", item{"1", "Ann"}); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(s.dir, "users", ".2.12345.tmp")
	fresh := filepath.Join(s.dir, "users", ".3.67890.tmp") // e.g. another live process mid-write
	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, []byte(`{"gid":`), 0o600); err != nil {
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
	if got := listDir(t, filepath.Join(s.dir, "users")); len(got) != 2 || got[0] != ".3.67890.tmp" || got[1] != "1.json" {
		t.Fatalf("dir after cleanup: %v", got)
	}
	if n, err := New(filepath.Join(s.dir, "missing")).CleanupTemp(time.Minute); n != 0 || err != nil {
		t.Fatalf("missing dir: %d %v", n, err)
	}
}
