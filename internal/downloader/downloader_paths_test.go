package downloader

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alehel/gogl-watcher/internal/db"
	"github.com/alehel/gogl-watcher/internal/gog"
	"github.com/alehel/gogl-watcher/internal/library"
)

// Localized installers often share their file names. Switching the language
// with the old installer kept used to plan the new language into the folder
// the kept copy was in, and the download then replaced it, leaving the
// inactive row pointing at the other language's bytes. Every language has a
// folder of its own now, whenever it was chosen.
func TestLanguageSwitchKeepsTheOtherLanguagesCopies(t *testing.T) {
	d, m, paths, syncer := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The Witcher: en (3 parts) and de (2 parts) with the same file names; de is
	// given other sizes so that an overwrite would show.
	p := &m.Games[0].Product
	for ii := range p.Downloads.Installers {
		inst := &p.Downloads.Installers[ii]
		if inst.OS != "windows" {
			continue
		}
		for fi := range inst.Files {
			f := &inst.Files[fi]
			name := "setup.exe"
			if fi > 0 {
				name = "setup-" + string(rune('0'+fi)) + ".bin"
			}
			size := int64(50000 + fi)
			if inst.Language == "de" {
				size = int64(60000 + fi)
			}
			f.Size = gog.FlexInt(size)
			f.Downlink = gog.MockDownlink(p.ID, string(f.ID), name, size)
		}
	}
	m.Games = m.Games[:1]
	settings := db.DefaultSettings()
	settings.Platforms = []string{"windows"}
	settings.Languages = []string{"en"}
	settings.LanguageFallback = false
	settings.ContentChosen = true
	settings.DownloadMode = db.DownloadAll
	if err := d.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	_ = d.SetSetupComplete(ctx, true)
	if err := syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	mgr := New(d, m, paths, slog.Default())
	syncer.OnDrop = mgr.Cancel
	syncer.OnChange = mgr.Wake
	mgr.Configure(settings, true)
	go mgr.Run(ctx)
	idle := func() bool {
		counts, _ := d.CountFilesByStatus(ctx)
		return counts[db.StatusPending] == 0 && len(mgr.Active()) == 0
	}
	waitFor(t, 20*time.Second, idle)
	english, _ := d.ListActiveFiles(ctx)
	if len(english) != 3 {
		t.Fatalf("want 3 English files, got %d", len(english))
	}
	sizes := map[string]int64{}
	for _, f := range english {
		if f.Status != db.StatusDone {
			t.Fatalf("English file not done: %+v", f)
		}
		st, err := os.Stat(paths.Abs(f.LocalPath))
		if err != nil {
			t.Fatal(err)
		}
		sizes[f.LocalPath] = st.Size()
	}

	// Switch to German, keeping the English copies.
	ns := settings
	ns.Languages = []string{"de"}
	if err := syncer.ApplySettings(ctx, ns, library.RemovalKeep); err != nil {
		t.Fatal(err)
	}
	if err := syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	mgr.Configure(ns, true)
	mgr.Wake()
	waitFor(t, 20*time.Second, idle)

	for rel, size := range sizes {
		st, err := os.Stat(paths.Abs(rel))
		if err != nil {
			t.Errorf("kept English file %s is gone: %v", rel, err)
			continue
		}
		if st.Size() != size {
			t.Errorf("kept English file %s was written over: %d bytes, was %d", rel, st.Size(), size)
		}
	}
	all, _ := d.ListFilesByGame(ctx, m.Games[0].Listed.ID)
	owners := map[string]int64{}
	german := 0
	for _, f := range all {
		if f.LocalPath == "" {
			continue
		}
		if prev, dup := owners[f.LocalPath]; dup {
			t.Errorf("files %d and %d share the path %s", prev, f.ID, f.LocalPath)
		}
		owners[f.LocalPath] = f.ID
		if f.Language == "de" {
			german++
			if f.Status != db.StatusDone {
				t.Errorf("German file not downloaded: %+v", f)
			}
			if !strings.HasPrefix(f.LocalPath, "The Witcher Enhanced Edition/windows/de/") {
				t.Errorf("German file planned into %s, want the language's own folder", f.LocalPath)
			}
		}
	}
	if german != 2 {
		t.Errorf("want 2 German files, got %d", german)
	}
}

// Two files planned onto one path (say, two extras with the same file name)
// must not write over each other: the second is refused, and the first stays
// what it was.
func TestDownloadRefusesToWriteOverAnotherFile(t *testing.T) {
	d, m, paths, syncer := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &m.Games[0].Product
	p.Downloads.BonusContent = []gog.Bonus{
		{ID: "9001", Name: "Manual", Type: "manual", Files: []gog.DownloadFile{{ID: "9001", Size: 4000, Downlink: gog.MockDownlink(p.ID, "9001", "manual.zip", 4000)}}},
		{ID: "9002", Name: "Manual (German)", Type: "manual", Files: []gog.DownloadFile{{ID: "9002", Size: 5000, Downlink: gog.MockDownlink(p.ID, "9002", "manual.zip", 5000)}}},
	}
	m.Games = m.Games[:1]
	settings := db.DefaultSettings()
	settings.Platforms = []string{"windows"}
	settings.IncludeInstallers = false
	settings.IncludeDLC = false
	settings.IncludeExtras = true
	settings.ContentChosen = true
	settings.DownloadMode = db.DownloadAll
	settings.MaxConcurrentDownloads = 2 // both at once: the claim must hold in memory too
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
	if len(files) != 2 {
		t.Fatalf("want 2 extras, got %d", len(files))
	}
	var done, failed *db.File
	for i := range files {
		switch files[i].Status {
		case db.StatusDone:
			done = &files[i]
		case db.StatusError:
			failed = &files[i]
		}
	}
	if done == nil || failed == nil {
		t.Fatalf("want one downloaded and one refused, got %+v", files)
	}
	st, err := os.Stat(paths.Abs(done.LocalPath))
	if err != nil || st.Size() != done.Size {
		t.Errorf("the downloaded extra is not what its row says: %v, %d bytes, want %d", err, st.Size(), done.Size)
	}
	if !strings.Contains(failed.Error, "another file") {
		t.Errorf("the refused extra should say why: %q", failed.Error)
	}
	if failed.Attempts != 1 {
		t.Errorf("a taken path is final, not retried: %d attempts", failed.Attempts)
	}
}

// Patches are downloaded like installers, into the patches folder under the
// installer's language folder, next to the installer they update.
func TestPatchesAreDownloadedNextToTheirInstaller(t *testing.T) {
	d, m, paths, syncer := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The Witcher only, with tiny patch files.
	m.Games = m.Games[:1]
	p := &m.Games[0].Product
	for ii := range p.Downloads.Patches {
		for fi := range p.Downloads.Patches[ii].Files {
			f := &p.Downloads.Patches[ii].Files[fi]
			f.Size = 4000 + gog.FlexInt(fi)
			f.Downlink = gog.MockDownlink(p.ID, string(f.ID), "patch_"+string(f.ID)+".bin", int64(f.Size))
		}
	}
	settings := db.DefaultSettings()
	settings.Platforms = []string{"windows"}
	settings.IncludeDLC = false
	settings.IncludePatches = true
	settings.ContentChosen = true
	settings.DownloadMode = db.DownloadAll
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
	installers, patches := 0, 0
	for _, f := range files {
		if f.Status != db.StatusDone {
			t.Errorf("not downloaded: %+v", f)
			continue
		}
		if st, err := os.Stat(paths.Abs(f.LocalPath)); err != nil {
			t.Errorf("%s: %v", f.LocalPath, err)
		} else if st.Size() != f.Size {
			t.Errorf("%s: %d bytes on disk, want %d", f.LocalPath, st.Size(), f.Size)
		}
		switch f.Kind {
		case db.KindInstaller:
			installers++
			if !strings.HasPrefix(f.LocalPath, "The Witcher Enhanced Edition/windows/en/file_") {
				t.Errorf("installer downloaded to %s, want the language folder", f.LocalPath)
			}
		case db.KindPatch:
			patches++
			if !strings.HasPrefix(f.LocalPath, "The Witcher Enhanced Edition/windows/en/patches/patch_") {
				t.Errorf("patch downloaded to %s, want the patches folder next to the installer", f.LocalPath)
			}
		}
	}
	if installers != 3 || patches != 2 {
		t.Errorf("want 3 installer parts and 2 patch parts downloaded, got %d and %d", installers, patches)
	}
}
