package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// sim drives a Scheduler on a virtual clock. Each job "takes" the next
// duration from durations; after len(durations) jobs the context is canceled.
type sim struct {
	now    time.Time
	starts []time.Duration // offsets from t0
	sleeps []time.Duration
}

func run(t *testing.T, interval time.Duration, durations ...time.Duration) *sim {
	t.Helper()
	s := &sim{now: t0}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	sch := &Scheduler{
		Interval: interval,
		Log:      slog.New(slog.DiscardHandler),
		Now:      func() time.Time { return s.now },
		Sleep: func(ctx context.Context, d time.Duration) error {
			if d < 0 {
				t.Fatalf("negative sleep %s", d)
			}
			s.sleeps = append(s.sleeps, d)
			s.now = s.now.Add(d)
			return ctx.Err()
		},
	}
	sch.Run(ctx, func(ctx context.Context) {
		s.starts = append(s.starts, s.now.Sub(t0))
		s.now = s.now.Add(durations[len(s.starts)-1])
		if len(s.starts) == len(durations) {
			cancel()
		}
	})
	return s
}

func assertStarts(t *testing.T, s *sim, want ...time.Duration) {
	t.Helper()
	if fmt.Sprint(s.starts) != fmt.Sprint(want) {
		t.Fatalf("starts = %v, want %v", s.starts, want)
	}
}

func TestRunsImmediatelyThenEveryFiveMinutes(t *testing.T) {
	s := run(t, 5*time.Minute, 10*time.Second, 10*time.Second, 10*time.Second)
	assertStarts(t, s, 0, 5*time.Minute, 10*time.Minute)
}

func TestThirtySecondInterval(t *testing.T) {
	s := run(t, 30*time.Second, 2*time.Second, 29*time.Second, 1*time.Second, 0)
	assertStarts(t, s, 0, 30*time.Second, 60*time.Second, 90*time.Second)
}

func TestOverrunSkipsMissedSlotsWithoutBacklog(t *testing.T) {
	// First run takes 70s: slots at 30s and 60s pass during it and are skipped.
	s := run(t, 30*time.Second, 70*time.Second, 5*time.Second, 5*time.Second)
	assertStarts(t, s, 0, 90*time.Second, 120*time.Second)
}

func TestRunEndingExactlyOnSlotStartsNextImmediately(t *testing.T) {
	s := run(t, 30*time.Second, 30*time.Second, 1*time.Second)
	assertStarts(t, s, 0, 30*time.Second)
}

func TestLongOverrunSchedulesNextFutureSlot(t *testing.T) {
	// 10m31s run on a 5m grid: slots 5m and 10m are skipped, next is 15m.
	s := run(t, 5*time.Minute, 10*time.Minute+31*time.Second, time.Second)
	assertStarts(t, s, 0, 15*time.Minute)
}

func TestCancelDuringWaitStopsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	sch := New(time.Hour, slog.New(slog.DiscardHandler)) // real sleep
	var calls atomic.Int32
	ran := make(chan struct{})
	done := make(chan struct{})
	go func() {
		sch.Run(ctx, func(context.Context) {
			if calls.Add(1) == 1 {
				close(ran)
			}
		})
		close(done)
	}()
	<-ran
	time.Sleep(20 * time.Millisecond) // let the scheduler enter its hour-long wait
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not stop after cancel")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d, want only the immediate run", calls.Load())
	}
}

func TestCancelDuringJobDoesNotScheduleAnother(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	sch := New(time.Millisecond, slog.New(slog.DiscardHandler))
	calls := 0
	sch.Run(ctx, func(context.Context) {
		calls++
		cancel()
	})
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
}
