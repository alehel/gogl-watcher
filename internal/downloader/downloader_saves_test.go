package downloader

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alehel/gogl-watcher/internal/db"
)

// Cloud saves go through the same queue as installers: planned by the sync,
// fetched by the manager into the game's saves folder, and fetched again once
// the cloud holds a newer version.
func TestDownloadsCloudSaves(t *testing.T) {
	d, m, paths, syncer := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	settings := db.DefaultSettings()
	settings.Platforms = []string{"windows"}
	settings.ContentChosen = true
	settings.IncludeSaves = true
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

	idle := func() bool {
		counts, _ := d.CountFilesByStatus(ctx)
		return counts[db.StatusPending] == 0 && len(mgr.Active()) == 0
	}
	waitFor(t, 20*time.Second, idle)
	const stardew = 1207664663
	files, _ := d.ListActiveFilesByGame(ctx, stardew)
	var saves []db.File
	for _, f := range files {
		if f.Kind == db.KindSave {
			saves = append(saves, f)
		}
	}
	if len(saves) != 3 {
		t.Fatalf("want 3 saves, got %+v", saves)
	}
	content := map[int64][]byte{}
	for _, f := range saves {
		if f.Status != db.StatusDone {
			t.Errorf("save %s status %s error %q", f.Name, f.Status, f.Error)
			continue
		}
		abs := paths.Abs(f.LocalPath)
		data, err := os.ReadFile(abs)
		if err != nil || int64(len(data)) != f.Size {
			t.Errorf("save %s missing or wrong size: %v", abs, err)
		}
		content[f.ID] = data
		// The object's path is mirrored under the game's saves folder.
		rel, _ := filepath.Rel(paths.Abs("Stardew Valley/saves"), abs)
		if filepath.ToSlash(rel) != f.Name {
			t.Errorf("save stored at %s, want it under the saves folder as %s", abs, f.Name)
		}
		if f.Name == "saves/startup_preferences" && f.Size != 1024 {
			t.Errorf("size of %s = %d", f.Name, f.Size)
		}
		if f.DownloadedAt == nil {
			t.Errorf("save %s has no download time", f.Name)
		}
	}

	// The cloud gets a newer version of one save: the copy on disk is replaced.
	target := saves[0]
	m.PutSave("50000000000000001", target.Name, target.Size+512)
	if err := syncer.SyncGame(ctx, stardew); err != nil {
		t.Fatal(err)
	}
	mgr.Wake()
	waitFor(t, 20*time.Second, idle)
	f, _ := d.GetFile(ctx, target.ID)
	if f.Status != db.StatusDone || f.PreviousPath != "" || f.Size != target.Size+512 {
		t.Fatalf("the rewritten save was not fetched again: %+v", f)
	}
	data, err := os.ReadFile(paths.Abs(f.LocalPath))
	if err != nil || int64(len(data)) != f.Size {
		t.Fatalf("new copy missing or wrong size: %v", err)
	}
	if string(data[:64]) == string(content[target.ID][:64]) {
		t.Error("the new copy reads like the old one")
	}
	if _, err := os.Stat(paths.Abs(f.LocalPath) + ".part"); err == nil {
		t.Error("part file left behind")
	}
}
