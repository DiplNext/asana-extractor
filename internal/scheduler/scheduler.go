// Package scheduler runs a job immediately and then on a fixed-rate grid.
//
// Semantics, with start time T0 and interval I:
//   - The job runs at T0, then at T0+I, T0+2I, ... (a fixed grid, so short
//     runs do not drift the schedule).
//   - At most one job runs at a time: the job is called synchronously.
//   - Grid slots that pass while a job is still running are skipped, not
//     queued; after an overrun the next start is the first grid slot at or
//     after the moment the job finished. There is never a catch-up burst.
//   - Cancelling ctx stops the wait immediately; a running job receives the
//     same ctx and is expected to return promptly.
package scheduler

import (
	"context"
	"log/slog"
	"time"
)

type Scheduler struct {
	Interval time.Duration
	Log      *slog.Logger

	// Now and Sleep are injectable for deterministic tests.
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error
}

func New(interval time.Duration, log *slog.Logger) *Scheduler {
	return &Scheduler{Interval: interval, Log: log, Now: time.Now, Sleep: sleepCtx}
}

// Run blocks until ctx is canceled. Job errors are the job's concern; the
// scheduler keeps going regardless.
func (s *Scheduler) Run(ctx context.Context, job func(context.Context)) {
	next := s.Now()
	for ctx.Err() == nil {
		job(ctx)
		if ctx.Err() != nil {
			return
		}

		next = next.Add(s.Interval)
		now := s.Now()
		if next.Before(now) {
			behind := now.Sub(next)
			skipped := int64((behind + s.Interval - 1) / s.Interval) // ceil
			next = next.Add(time.Duration(skipped) * s.Interval)
			s.Log.Warn("extraction overran interval; skipping missed starts",
				"skipped", skipped, "interval", s.Interval, "next_start", next)
		}

		wait := next.Sub(now)
		s.Log.Debug("next extraction scheduled", "in", wait, "at", next)
		if err := s.Sleep(ctx, wait); err != nil {
			return
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
