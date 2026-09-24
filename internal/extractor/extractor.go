// Package extractor runs one full extraction: every visible user and
// project of the workspace is fetched page by page and upserted to storage.
package extractor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"asana-extractor/internal/asana"
	"asana-extractor/internal/storage"
)

type Extractor struct {
	client    *asana.Client
	store     *storage.Store
	workspace string
	log       *slog.Logger
	runs      atomic.Int64
	now       func() time.Time
}

func New(client *asana.Client, store *storage.Store, workspaceGID string, log *slog.Logger) *Extractor {
	return &Extractor{client: client, store: store, workspace: workspaceGID, log: log, now: time.Now}
}

// Stats describes one run. Counts cover resources processed before any failure.
type Stats struct {
	Users     int
	Projects  int
	Written   int // resource files created or replaced
	Unchanged int // resource files whose content was identical and left untouched
	Retries   int64
	Duration  time.Duration
}

// Run extracts users, then projects, writing one JSON file per resource. A
// failure in one resource type does not prevent the other from being
// attempted, unless ctx is canceled. Each page is written before the next
// is fetched, so memory is bounded by one page.
func (e *Extractor) Run(ctx context.Context) (Stats, error) {
	runID := e.runs.Add(1)
	start := e.now()
	retriesBefore := e.client.Retries()
	var st Stats

	e.log.Info("extraction started", "run", runID, "workspace", e.workspace)

	usersErr := e.client.ListUsers(ctx, e.workspace, func(page []asana.User) error {
		for _, u := range page {
			if err := e.put(storage.KindUsers, u.GID, u, &st); err != nil {
				return err
			}
			st.Users++
		}
		return nil
	})

	var projectsErr error
	if ctx.Err() == nil {
		projectsErr = e.client.ListProjects(ctx, e.workspace, func(page []asana.Project) error {
			for _, p := range page {
				if err := e.put(storage.KindProjects, p.GID, p, &st); err != nil {
					return err
				}
				st.Projects++
			}
			return nil
		})
	}

	st.Retries = e.client.Retries() - retriesBefore
	st.Duration = e.now().Sub(start)
	err := errors.Join(usersErr, projectsErr)
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}

	attrs := []any{
		"run", runID,
		"duration", st.Duration.Round(time.Millisecond),
		"users", st.Users,
		"projects", st.Projects,
		"written", st.Written,
		"unchanged", st.Unchanged,
		"retries", st.Retries,
	}
	if err != nil && ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		e.log.Warn("extraction interrupted", append(attrs, "status", "canceled", "err", err)...)
		return st, fmt.Errorf("extraction run %d interrupted: %w", runID, err)
	}
	if err != nil {
		e.log.Error("extraction failed", append(attrs, "status", "failed", "err", err)...)
		return st, fmt.Errorf("extraction run %d failed: %w", runID, err)
	}
	e.log.Info("extraction finished", append(attrs, "status", "success")...)
	return st, nil
}

func (e *Extractor) put(kind, gid string, v any, st *Stats) error {
	res, err := e.store.Put(kind, gid, v)
	if err != nil {
		return err
	}
	if res == storage.Unchanged {
		st.Unchanged++
		return nil
	}
	st.Written++
	e.log.Debug("resource file saved", "file", e.store.Path(kind, gid))
	return nil
}
