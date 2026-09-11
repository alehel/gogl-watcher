package scheduler

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/alehel/gogl-watcher/internal/db"
	"github.com/alehel/gogl-watcher/internal/gog"
	"github.com/alehel/gogl-watcher/internal/library"
)

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// Changing the check interval must move the next run right away, not after the
// old interval has elapsed.
func TestRescheduleAppliesNewInterval(t *testing.T) {
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	m, _ := gog.NewMock(ctx, nil)
	_, _ = m.ExchangeCode(ctx, "code")
	s := db.DefaultSettings()
	s.Platforms = []string{"windows"}
	s.ContentChosen = true
	s.CheckIntervalHours = 6
	_ = d.SaveSettings(ctx, s)
	_ = d.SetSetupComplete(ctx, true)
	// The last sync just finished, so the next one is a full interval away.
	_ = d.SetTime(ctx, "sync.last_finished_at", time.Now())
	syncer := library.NewSyncer(d, m, library.Paths{Root: filepath.Join(dir, "lib")}, slog.Default())
	sched := New(d, m, syncer, slog.Default(), 0)
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go sched.Run(rctx)

	nextIn := func() time.Duration {
		st := syncer.Status()
		if st.NextRunAt == nil {
			return 0
		}
		return time.Until(*st.NextRunAt)
	}
	waitFor(t, 5*time.Second, func() bool { return nextIn() > 5*time.Hour })

	s.CheckIntervalHours = 1
	_ = d.SaveSettings(ctx, s)
	sched.Reschedule()
	waitFor(t, 5*time.Second, func() bool { d := nextIn(); return d > 0 && d < 2*time.Hour })
	if syncer.Status().Running {
		t.Error("rescheduling must not start a sync")
	}
}
