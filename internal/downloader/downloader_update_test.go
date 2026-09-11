package downloader

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alehel/gogl-watcher/internal/db"
	"github.com/alehel/gogl-watcher/internal/gog"
	"github.com/alehel/gogl-watcher/internal/library"
)

// resize makes every mock file `size` bytes so a paced transfer takes several reads.
func resize(m *gog.Mock, size int64) {
	for gi := range m.Games {
		p := &m.Games[gi].Product
		for ii := range p.Downloads.Installers {
			for fi := range p.Downloads.Installers[ii].Files {
				f := &p.Downloads.Installers[ii].Files[fi]
				f.Size = gog.FlexInt(size + int64(fi))
				f.Downlink = gog.MockDownlink(p.ID, string(f.ID), "file_"+string(f.ID)+".bin", int64(f.Size))
			}
		}
	}
}

// A new version published while the old one is still downloading must never end
// up marked done under the new version with the old bytes on disk.
func TestVersionChangeDuringDownloadIsNotMarkedDone(t *testing.T) {
	d, m, paths, syncer := setup(t)
	resize(m, 400000)
	m.Speed = 200000 // 64 kB reads every ~0.3 s, ~2 s per file
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
	next, _ := d.NextPendingFiles(ctx, 1, nil)
	target := next[0]

	mgr := New(d, m, paths, slog.Default())
	mgr.Configure(settings, true)
	go mgr.Run(ctx)
	waitFor(t, 10*time.Second, func() bool {
		p, ok := mgr.ProgressFor(target.ID)
		return ok && p.Downloaded > 0
	})

	// GOG publishes a new build of the file while it is being transferred.
	newSize := target.Size + 7
	updated := target
	updated.Version = "9.9"
	updated.Size = newSize
	updated.Downlink = gog.MockDownlink(target.ProductID, target.GogID, "file_"+target.GogID+"_v2.bin", newSize)
	if _, err := d.UpsertDesiredFile(ctx, updated, paths.Exists); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 60*time.Second, func() bool {
		counts, _ := d.CountFilesByStatus(ctx)
		return counts[db.StatusPending] == 0 && len(mgr.Active()) == 0
	})
	got, _ := d.GetFile(ctx, target.ID)
	if got.Status != db.StatusDone {
		t.Fatalf("file should end up done: %+v", got)
	}
	st, err := os.Stat(paths.Abs(got.LocalPath))
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != newSize || got.Size != newSize {
		t.Fatalf("version 9.9 marked done but the file on disk is the old build: disk=%d db=%d want=%d", st.Size(), got.Size, newSize)
	}
	if _, err := os.Stat(paths.Abs(target.RelDir) + ".part"); err == nil {
		t.Errorf("part file left behind")
	}
}

// stallingAPI serves downloads whose body never delivers data.
type stallingAPI struct {
	*gog.Mock
}

type stallBody struct{ ctx context.Context }

func (b stallBody) Read(p []byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (b stallBody) Close() error { return nil }

func (s stallingAPI) OpenDownload(ctx context.Context, u string, offset int64) (*gog.Download, error) {
	return &gog.Download{Body: stallBody{ctx}, Offset: offset, Length: 50000, Filename: "stalled.bin"}, nil
}

var _ io.ReadCloser = stallBody{}

// A connection that stops delivering data must be given up on and retried
// instead of holding a download slot forever.
func TestStalledTransferIsAbandonedAndRetried(t *testing.T) {
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
	next, _ := d.NextPendingFiles(ctx, 1, nil)
	target := next[0]

	mgr := New(d, stallingAPI{m}, paths, slog.Default())
	mgr.StallTimeout = 300 * time.Millisecond
	mgr.Configure(settings, true)
	go mgr.Run(ctx)
	waitFor(t, 10*time.Second, func() bool {
		f, _ := d.GetFile(ctx, target.ID)
		return f != nil && f.Attempts >= 1
	})
	f, _ := d.GetFile(ctx, target.ID)
	if f.Status != db.StatusPending || !strings.Contains(f.Error, "no data received") {
		t.Fatalf("stalled transfer should be scheduled for retry with a stall error: %+v", f)
	}
	if _, ok := mgr.ProgressFor(target.ID); ok {
		t.Error("stalled transfer still occupies a download slot")
	}
}

// A file that is already complete on disk (say, after the process died between the
// rename and the database update) still replaces the previous version.
func TestAlreadyPresentFileRemovesPreviousVersion(t *testing.T) {
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
	next, _ := d.NextPendingFiles(ctx, 1, nil)
	target := next[0]
	game, _ := d.GetGame(ctx, target.GameID)
	// Pretend an older build was downloaded before, and the new one is already on disk.
	oldRel := library.LocalRelPath(game.Folder, target.RelDir, "old_build.bin")
	_ = os.MkdirAll(filepath.Dir(paths.Abs(oldRel)), 0o755)
	_ = os.WriteFile(paths.Abs(oldRel), []byte("old"), 0o644)
	if _, err := d.ExecContext(ctx, `UPDATE files SET previous_path = ? WHERE id = ?`, oldRel, target.ID); err != nil {
		t.Fatal(err)
	}
	newRel := library.LocalRelPath(game.Folder, target.RelDir, "file_"+target.GogID+".bin")
	data := make([]byte, target.Size)
	for i := range data {
		data[i] = byte(int64(i) * 31 % 251)
	}
	_ = os.WriteFile(paths.Abs(newRel), data, 0o644)

	mgr := New(d, m, paths, slog.Default())
	mgr.Configure(settings, true)
	go mgr.Run(ctx)
	waitFor(t, 20*time.Second, func() bool {
		f, _ := d.GetFile(ctx, target.ID)
		return f != nil && f.Status == db.StatusDone
	})
	if _, err := os.Stat(paths.Abs(oldRel)); err == nil {
		t.Errorf("previous version %s still on disk after the new build was found present", oldRel)
	}
	if _, err := os.Stat(paths.Abs(newRel)); err != nil {
		t.Errorf("new build missing: %v", err)
	}
}
