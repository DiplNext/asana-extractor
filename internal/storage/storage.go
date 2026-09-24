// Package storage writes one JSON array file per resource type with atomic
// replacement.
//
// Layout: <dir>/<kind>.json, e.g. output/users.json. Elements are streamed
// into a temp file in the destination directory as pages arrive, and the
// file is renamed over the target only after the whole listing succeeded.
// Readers therefore see either the previous complete array or the new
// complete array — never a partial or truncated one — and memory use stays
// bounded by one page regardless of the number of resources.
package storage

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	KindUsers    = "users"
	KindProjects = "projects"

	tempSuffix = ".tmp"
	filePerm   = 0o644
	dirPerm    = 0o755
)

type Store struct {
	dir    string
	rename func(oldpath, newpath string) error
}

func New(dir string) *Store {
	return &Store{dir: dir, rename: os.Rename}
}

// Path returns the file a kind is stored in.
func (s *Store) Path(kind string) string {
	return filepath.Join(s.dir, kind+".json")
}

// Result of a committed array.
type Result int

const (
	Written   Result = iota // file created or replaced
	Unchanged               // identical content already on disk; temp discarded
)

// ArrayWriter streams one JSON array to a temp file. Call Commit to publish
// it, or Abort to discard it; Abort after Commit is a no-op, so
// `defer w.Abort()` is safe.
type ArrayWriter struct {
	store  *Store
	kind   string
	target string
	tmp    *os.File
	buf    *bufio.Writer
	hash   hash.Hash // sha256 of everything written, for the unchanged check
	count  int
	done   bool
}

// Begin creates the output directory and a temp file for kind.
func (s *Store) Begin(kind string) (*ArrayWriter, error) {
	if kind != KindUsers && kind != KindProjects {
		return nil, fmt.Errorf("store %s: unknown kind", kind)
	}
	if err := os.MkdirAll(s.dir, dirPerm); err != nil {
		return nil, fmt.Errorf("create directory %s: %w", s.dir, err)
	}
	tmp, err := os.CreateTemp(s.dir, "."+kind+".json.*"+tempSuffix)
	if err != nil {
		return nil, fmt.Errorf("create temp file for %s: %w", s.Path(kind), err)
	}
	h := sha256.New()
	return &ArrayWriter{
		store:  s,
		kind:   kind,
		target: s.Path(kind),
		tmp:    tmp,
		buf:    bufio.NewWriter(io.MultiWriter(tmp, h)),
		hash:   h,
	}, nil
}

// Add appends one element, indented as an array item.
func (w *ArrayWriter) Add(v any) error {
	if w.done {
		return errors.New("storage: Add after Commit/Abort")
	}
	elem, err := json.MarshalIndent(v, "  ", "  ")
	if err != nil {
		return fmt.Errorf("encode %s element %d: %w", w.kind, w.count, err)
	}
	sep := ",\n  "
	if w.count == 0 {
		sep = "[\n  "
	}
	if _, err := w.buf.WriteString(sep); err != nil {
		return w.writeErr(err)
	}
	if _, err := w.buf.Write(elem); err != nil {
		return w.writeErr(err)
	}
	w.count++
	return nil
}

// Count is the number of elements added so far.
func (w *ArrayWriter) Count() int { return w.count }

// Commit closes the array, fsyncs it and atomically replaces the target.
// If the target already has identical content, the temp file is discarded
// and Unchanged is returned. On any error the temp file is removed and the
// previous target is left untouched.
func (w *ArrayWriter) Commit() (res Result, err error) {
	if w.done {
		return 0, errors.New("storage: Commit after Commit/Abort")
	}
	defer func() {
		if err != nil {
			w.Abort()
		}
	}()

	closing := "\n]\n"
	if w.count == 0 {
		closing = "[]\n"
	}
	if _, err := w.buf.WriteString(closing); err != nil {
		return 0, w.writeErr(err)
	}
	if err := w.buf.Flush(); err != nil {
		return 0, w.writeErr(err)
	}

	if same, err := sameContent(w.target, w.hash.Sum(nil)); err == nil && same {
		w.Abort()
		return Unchanged, nil
	}

	if err := w.tmp.Chmod(filePerm); err != nil {
		return 0, w.writeErr(err)
	}
	if err := w.tmp.Sync(); err != nil {
		return 0, w.writeErr(err)
	}
	if err := w.tmp.Close(); err != nil {
		return 0, w.writeErr(err)
	}
	if err := w.store.rename(w.tmp.Name(), w.target); err != nil {
		return 0, w.writeErr(err)
	}
	w.done = true
	return Written, nil
}

// Abort discards the temp file. Safe to call multiple times and after Commit.
func (w *ArrayWriter) Abort() {
	if w.done {
		return
	}
	w.done = true
	w.tmp.Close()
	os.Remove(w.tmp.Name())
}

func (w *ArrayWriter) writeErr(err error) error {
	return fmt.Errorf("write %s: %w", w.target, err)
}

// sameContent reports whether path exists and hashes to sum.
func sameContent(path string, sum []byte) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return bytes.Equal(h.Sum(nil), sum), nil
}

// CleanupTemp removes temp files left behind by a crashed process (e.g.
// SIGKILL before rename) whose modification time is older than olderThan.
// The age check keeps it from deleting a temp file another live process is
// writing right now. A missing directory is not an error.
func (s *Store) CleanupTemp(olderThan time.Duration) (removed int, err error) {
	cutoff := time.Now().Add(-olderThan)
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var errs []error
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasPrefix(name, ".") || !strings.HasSuffix(name, tempSuffix) {
			continue
		}
		if info, err := e.Info(); err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, name)); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}
