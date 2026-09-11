// Package scheduler runs the periodic library check.
package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/alehel/gogl-watcher/internal/db"
	"github.com/alehel/gogl-watcher/internal/gog"
	"github.com/alehel/gogl-watcher/internal/library"
)

// Scheduler triggers syncs on an interval and on demand.
type Scheduler struct {
	db      *db.DB
	gog     gog.API
	syncer  *library.Syncer
	log     *slog.Logger
	trigger chan struct{}
	nudge   chan struct{}
	started time.Time
	delay   time.Duration
}

// New creates a scheduler. startupDelay postpones the first run after boot.
func New(d *db.DB, g gog.API, s *library.Syncer, log *slog.Logger, startupDelay time.Duration) *Scheduler {
	return &Scheduler{db: d, gog: g, syncer: s, log: log.With("component", "scheduler"), trigger: make(chan struct{}, 1),
		nudge: make(chan struct{}, 1), started: time.Now(), delay: startupDelay}
}

// TriggerNow requests a sync as soon as possible.
func (s *Scheduler) TriggerNow() {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

// Reschedule makes the scheduler recompute when the next sync is due, for
// example after the check interval was changed in the settings. It does not
// start a sync by itself.
func (s *Scheduler) Reschedule() {
	select {
	case s.nudge <- struct{}{}:
	default:
	}
}

func (s *Scheduler) ready(ctx context.Context) bool {
	done, err := s.db.SetupComplete(ctx)
	return err == nil && done && s.gog.Authenticated()
}

func (s *Scheduler) nextRun(ctx context.Context) time.Time {
	settings, err := s.db.GetSettings(ctx)
	interval := 6 * time.Hour
	if err == nil {
		interval = time.Duration(settings.CheckIntervalHours) * time.Hour
	}
	st := s.syncer.Status()
	var next time.Time
	if st.LastFinishedAt == nil {
		next = s.started.Add(s.delay)
	} else {
		next = st.LastFinishedAt.Add(interval)
		if boot := s.started.Add(s.delay); next.Before(boot) {
			next = boot
		}
	}
	return next
}

// Run blocks until ctx is done.
func (s *Scheduler) Run(ctx context.Context) {
	for {
		var wait time.Duration
		if s.ready(ctx) {
			next := s.nextRun(ctx)
			s.syncer.SetNextRun(&next)
			wait = time.Until(next)
			if wait < 0 {
				wait = 0
			}
		} else {
			s.syncer.SetNextRun(nil)
			wait = 30 * time.Second
		}
		timer := time.NewTimer(wait)
		fired := false
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			fired = s.ready(ctx)
		case <-s.trigger:
			timer.Stop()
			fired = s.ready(ctx)
		case <-s.nudge:
			timer.Stop()
		}
		if !fired {
			continue
		}
		s.syncer.SetNextRun(nil)
		if err := s.syncer.SyncAll(ctx); err != nil && !errors.Is(err, library.ErrSyncRunning) && ctx.Err() == nil {
			s.log.Error("scheduled sync failed", "error", err)
		}
	}
}
