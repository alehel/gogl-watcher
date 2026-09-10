package downloader

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alehel/gogl-watcher/internal/db"
	"github.com/alehel/gogl-watcher/internal/gog"
	"github.com/alehel/gogl-watcher/internal/library"
)

func setup(t *testing.T) (*db.DB, *gog.Mock, library.Paths, *library.Syncer) {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	m, _ := gog.NewMock(context.Background(), nil)
	m.Speed = 0
	// Shrink the sample library to two games with tiny files for speed.
	m.Games = m.Games[:2]
	for gi := range m.Games {
		p := &m.Games[gi].Product
		for ii := range p.Downloads.Installers {
			for fi := range p.Downloads.Installers[ii].Files {
				f := &p.Downloads.Installers[ii].Files[fi]
				f.Size = 50000 + gog.FlexInt(fi)
				f.Downlink = gog.MockDownlink(p.ID, string(f.ID), "file_"+string(f.ID)+".bin", int64(f.Size))
			}
		}
		p.Downloads.BonusContent = nil
	}
	if _, err := m.ExchangeCode(context.Background(), "code"); err != nil {
		t.Fatal(err)
	}
	paths := library.Paths{Root: filepath.Join(dir, "library")}
	s := library.NewSyncer(d, m, paths, slog.Default())
	return d, m, paths, s
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestDownloadsWholeLibrary(t *testing.T) {
	d, m, paths, syncer := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	settings := db.DefaultSettings()
	settings.Platforms = []string{"windows"}
	settings.ContentChosen = true
	if err := d.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	_ = d.SetSetupComplete(ctx, true)
	if err := syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	mgr := New(d, m, paths, slog.Default())
	mgr.Configure(settings, true)
	go mgr.Run(ctx)

	waitFor(t, 20*time.Second, func() bool {
		counts, _ := d.CountFilesByStatus(ctx)
		return counts[db.StatusPending] == 0 && len(mgr.Active()) == 0
	})
	files, _ := d.ListActiveFiles(ctx)
	if len(files) == 0 {
		t.Fatal("no files planned")
	}
	for _, f := range files {
		if f.Status != db.StatusDone {
			t.Errorf("file %s status %s error %q", f.GogID, f.Status, f.Error)
			continue
		}
		abs := paths.Abs(f.LocalPath)
		st, err := os.Stat(abs)
		if err != nil || st.Size() != f.Size {
			t.Errorf("file %s missing or wrong size: %v", abs, err)
		}
		if filepath.Base(filepath.Dir(abs)) != "windows" {
			t.Errorf("unexpected location %s", abs)
		}
		if _, err := os.Stat(abs + ".part"); err == nil {
			t.Errorf("part file left behind for %s", abs)
		}
	}
}

func TestResumeAndFailure(t *testing.T) {
	d, m, paths, syncer := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	settings := db.DefaultSettings()
	settings.Platforms = []string{"windows"}
	settings.ContentChosen = true
	settings.MaxConcurrentDownloads = 1
	_ = d.SaveSettings(ctx, settings)
	_ = d.SetSetupComplete(ctx, true)
	if err := syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	files, _ := d.ListActiveFiles(ctx)
	target := files[0]
	// Pre-create a partial file with the right prefix so the download resumes.
	game, _ := d.GetGame(ctx, target.GameID)
	name := "file_" + target.GogID + ".bin"
	rel := library.LocalRelPath(game.Folder, target.RelDir, name)
	abs := paths.Abs(rel)
	_ = os.MkdirAll(filepath.Dir(abs), 0o755)
	prefix := make([]byte, 20000)
	for i := range prefix {
		prefix[i] = byte(int64(i) * 31 % 251)
	}
	_ = os.WriteFile(abs+".part", prefix, 0o644)
	// Make every other file fail permanently.
	for _, f := range files[1:] {
		m.FailDownlinks[f.Downlink] = true
	}
	mgr := New(d, m, paths, slog.Default())
	mgr.Configure(settings, true)
	go mgr.Run(ctx)
	waitFor(t, 20*time.Second, func() bool {
		counts, _ := d.CountFilesByStatus(ctx)
		return counts[db.StatusPending] == 0 && len(mgr.Active()) == 0
	})
	got, _ := d.GetFile(ctx, target.ID)
	if got.Status != db.StatusDone {
		t.Fatalf("resumed file: %+v", got)
	}
	data, _ := os.ReadFile(abs)
	if int64(len(data)) != target.Size {
		t.Fatalf("size %d, want %d", len(data), target.Size)
	}
	for i, b := range data {
		if b != byte(int64(i)*31%251) {
			t.Fatalf("byte %d corrupted after resume", i)
		}
	}
	for _, f := range files[1:] {
		got, _ := d.GetFile(ctx, f.ID)
		if got.Status != db.StatusError || got.Error == "" {
			t.Errorf("expected permanent error for %s, got %+v", f.GogID, got)
		}
	}
}
