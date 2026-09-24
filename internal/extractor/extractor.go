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

// Stats describes one run. Users/Projects count resources fetched (also
// when a listing later failed); Written/Unchanged count array files.
type Stats struct {
	Users     int
	Projects  int
	Written   int // files replaced with new content
	Unchanged int // files whose content was identical and left untouched
	Retries   int64
	Duration  time.Duration
}

// Run extracts users, then projects, each into its own JSON array file. A
// failure in one resource type does not prevent the other from being
// attempted, unless ctx is canceled. Each page is streamed to a temp file
// before the next is fetched, so memory is bounded by one page.
func (e *Extractor) Run(ctx context.Context) (Stats, error) {
	runID := e.runs.Add(1)
	start := e.now()
	retriesBefore := e.client.Retries()
	var st Stats

	e.log.Info("extraction started", "run", runID, "workspace", e.workspace)

	var usersErr, projectsErr error
	st.Users, usersErr = extractKind(ctx, e, storage.KindUsers, e.client.ListUsers, &st)
	if ctx.Err() == nil {
		st.Projects, projectsErr = extractKind(ctx, e, storage.KindProjects, e.client.ListProjects, &st)
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

// extractKind streams every page of one resource type into its array file
// and publishes the file only if the whole listing succeeded; otherwise the
// previous file is kept. It returns the number of resources fetched.
func extractKind[T any](ctx context.Context, e *Extractor, kind string,
	list func(context.Context, string, func([]T) error) error, st *Stats,
) (int, error) {
	w, err := e.store.Begin(kind)
	if err != nil {
		return 0, err
	}
	defer w.Abort() // no-op after a successful Commit

	err = list(ctx, e.workspace, func(page []T) error {
		for _, item := range page {
			if err := w.Add(item); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return w.Count(), err
	}

	res, err := w.Commit()
	if err != nil {
		return w.Count(), err
	}
	if res == storage.Unchanged {
		st.Unchanged++
	} else {
		st.Written++
	}
	e.log.Info("resource file saved", "file", e.store.Path(kind), "count", w.Count(), "changed", res == storage.Written)
	return w.Count(), nil
}
