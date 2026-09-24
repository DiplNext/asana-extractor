// Package storage writes one JSON file per resource with atomic replacement.
//
// Layout: <dir>/<kind>/<gid>.json. Each write goes to a temp file in the
// destination directory and is renamed over the target, so readers see
// either the previous complete file or the new complete file, never a
// truncated one. Files for resources absent from later runs are kept.
package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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

var gidPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ErrInvalidGID is returned for identifiers that are unsafe as filenames.
var ErrInvalidGID = errors.New("invalid gid")

type Store struct {
	dir    string
	rename func(oldpath, newpath string) error
}

func New(dir string) *Store {
	return &Store{dir: dir, rename: os.Rename}
}

// Path returns the file a resource is stored in.
func (s *Store) Path(kind, gid string) string {
	return filepath.Join(s.dir, kind, gid+".json")
}

// Result of a Put.
type Result int

const (
	Written   Result = iota // file created or replaced
	Unchanged               // identical content already on disk; nothing written
)

// Put stores v as <dir>/<kind>/<gid>.json.
func (s *Store) Put(kind, gid string, v any) (Result, error) {
	if kind != KindUsers && kind != KindProjects {
		return 0, fmt.Errorf("store %s/%s: unknown kind", kind, gid)
	}
	if !gidPattern.MatchString(gid) {
		return 0, fmt.Errorf("store %s %q: %w", kind, gid, ErrInvalidGID)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("encode %s/%s: %w", kind, gid, err)
	}
	data = append(data, '\n')

	dir := filepath.Join(s.dir, kind)
	target := s.Path(kind, gid)

	if existing, err := os.ReadFile(target); err == nil && bytes.Equal(existing, data) {
		return Unchanged, nil
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return 0, fmt.Errorf("create directory %s: %w", dir, err)
	}
	if err := s.writeAtomic(dir, target, gid, data); err != nil {
		return 0, fmt.Errorf("write %s: %w", target, err)
	}
	return Written, nil
}

func (s *Store) writeAtomic(dir, target, gid string, data []byte) (err error) {
	tmp, err := os.CreateTemp(dir, "."+gid+".*"+tempSuffix)
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Chmod(filePerm); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return s.rename(tmpName, target)
}

// CleanupTemp removes temp files left behind by a crashed process (e.g.
// SIGKILL between create and rename) whose modification time is older than
// olderThan. The age check keeps it from deleting a temp file another live
// process is writing right now. Missing directories are not an error.
func (s *Store) CleanupTemp(olderThan time.Duration) (removed int, err error) {
	cutoff := time.Now().Add(-olderThan)
	var errs []error
	for _, kind := range []string{KindUsers, KindProjects} {
		entries, err := os.ReadDir(filepath.Join(s.dir, kind))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if !e.Type().IsRegular() || !strings.HasPrefix(name, ".") || !strings.HasSuffix(name, tempSuffix) {
				continue
			}
			if info, err := e.Info(); err != nil || info.ModTime().After(cutoff) {
				continue
			}
			if err := os.Remove(filepath.Join(s.dir, kind, name)); err != nil {
				errs = append(errs, err)
				continue
			}
			removed++
		}
	}
	return removed, errors.Join(errs...)
}
