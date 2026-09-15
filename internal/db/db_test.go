package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestSettingsRoundTripAndNormalize(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	s, err := d.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Platforms) != 0 || s.CheckIntervalHours != 6 {
		t.Errorf("unexpected defaults: %+v", s)
	}
	s.Platforms = []string{"Windows", "osx", "windows"}
	s.Languages = []string{"EN", "", "de"}
	if err := s.Normalize(); err != nil {
		t.Fatal(err)
	}
	if len(s.Platforms) != 2 || s.Platforms[1] != "mac" || len(s.Languages) != 2 {
		t.Errorf("normalize: %+v", s)
	}
	s.MaxConcurrentDownloads = 9
	if err := s.Normalize(); err == nil {
		t.Error("expected validation error")
	}
	s.MaxConcurrentDownloads = 3
	if err := d.SaveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, _ := d.GetSettings(ctx)
	if got.MaxConcurrentDownloads != 3 || got.Platforms[0] != "windows" {
		t.Errorf("round trip: %+v", got)
	}
}

// Settings stored before a setting existed leave it out; such an installation
// keeps behaving as it did, which for the base game installers means fetching them.
func TestSettingsAddedLaterKeepTheOldBehaviour(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	if err := d.SetKV(ctx, "settings", `{"download_mode":"all","platforms":["windows"],"languages":["en"],"include_dlc":true,"max_concurrent_downloads":2,"check_interval_hours":6}`); err != nil {
		t.Fatal(err)
	}
	s, err := d.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !s.IncludeInstallers || s.IncludeSaves || s.IncludePatches {
		t.Errorf("old settings should keep downloading installers and nothing new: %+v", s)
	}
	s.IncludeInstallers = false
	if err := d.SaveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.GetSettings(ctx); got.IncludeInstallers {
		t.Error("switching installers off did not stick")
	}
}

func TestFileLifecycle(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	if err := d.UpsertGame(ctx, Game{ID: 1, Title: "Game", Folder: "Game", WorksWindows: true}); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertProduct(ctx, Product{ID: 1, GameID: 1, Title: "Game"}); err != nil {
		t.Fatal(err)
	}
	onDisk := map[string]bool{}
	exists := func(p string) bool { return onDisk[p] }
	f := File{GameID: 1, ProductID: 1, Kind: "installer", OS: "windows", Language: "en", GogID: "en1installer0", Name: "Game", Version: "1.0", Size: 100, Downlink: "x", RelDir: "windows"}
	res, err := d.UpsertDesiredFile(ctx, f, exists)
	if err != nil || !res.Inserted {
		t.Fatalf("insert: %+v %v", res, err)
	}
	pending, _ := d.NextPendingFiles(ctx, 10, nil)
	if len(pending) != 1 || pending[0].ID != res.ID {
		t.Fatalf("pending = %+v", pending)
	}
	if err := d.SetFileDone(ctx, res.ID, "Game/windows/setup.exe", 100); err != nil {
		t.Fatal(err)
	}
	onDisk["Game/windows/setup.exe"] = true
	// Same version again: stays done.
	res2, _ := d.UpsertDesiredFile(ctx, f, exists)
	got, _ := d.GetFile(ctx, res2.ID)
	if res2.Updated || got.Status != StatusDone {
		t.Errorf("unchanged file should stay done: %+v", got)
	}
	// New version: becomes pending and remembers the old path.
	f.Version = "1.1"
	f.Size = 120
	res3, _ := d.UpsertDesiredFile(ctx, f, exists)
	got, _ = d.GetFile(ctx, res3.ID)
	if !res3.Updated || got.Status != StatusPending || got.PreviousPath != "Game/windows/setup.exe" || got.Size != 120 {
		t.Errorf("updated file: %+v", got)
	}
	// Errors with retry schedule are not picked up immediately.
	if err := d.SetFileError(ctx, res3.ID, "boom", 60e9); err != nil {
		t.Fatal(err)
	}
	pending, _ = d.NextPendingFiles(ctx, 10, nil)
	if len(pending) != 0 {
		t.Errorf("file scheduled for later should not be pending now")
	}
	if err := d.SetFileError(ctx, res3.ID, "boom", 0); err != nil {
		t.Fatal(err)
	}
	got, _ = d.GetFile(ctx, res3.ID)
	if got.Status != StatusError || got.Attempts != 2 {
		t.Errorf("error state: %+v", got)
	}
	if err := d.ResetGameErrors(ctx, 1); err != nil {
		t.Fatal(err)
	}
	got, _ = d.GetFile(ctx, res3.ID)
	if got.Status != StatusPending || got.Attempts != 0 {
		t.Errorf("after reset: %+v", got)
	}
	// Deactivate files that are no longer wanted: pending rows vanish, done rows go inactive.
	if err := d.SetFileDone(ctx, res3.ID, "Game/windows/setup_1.1.exe", 120); err != nil {
		t.Fatal(err)
	}
	onDisk["Game/windows/setup_1.1.exe"] = true
	other := f
	other.GogID = "en1installer1"
	resO, _ := d.UpsertDesiredFile(ctx, other, exists)
	dropped, err := d.DeactivateOtherFiles(ctx, 1, nil)
	if err != nil || len(dropped) != 2 {
		t.Fatalf("dropped = %+v, err = %v", dropped, err)
	}
	if g, _ := d.GetFile(ctx, resO.ID); g != nil {
		t.Errorf("never-downloaded file should be deleted")
	}
	got, _ = d.GetFile(ctx, res3.ID)
	if got.Active || got.Status != StatusInactive {
		t.Errorf("downloaded file should be inactive: %+v", got)
	}
	// Wanted again with the same version and still on disk: straight back to done.
	res4, _ := d.UpsertDesiredFile(ctx, f, exists)
	got, _ = d.GetFile(ctx, res4.ID)
	if !got.Active || got.Status != StatusDone {
		t.Errorf("reactivated file: %+v", got)
	}
	stats, _ := d.GameStatsAll(ctx)
	if stats[1].FilesTotal != 1 || stats[1].FilesDone != 1 || stats[1].BytesDone != 120 {
		t.Errorf("stats = %+v", stats[1])
	}
	// File vanished from disk: MarkMissingDone re-queues it.
	delete(onDisk, "Game/windows/setup_1.1.exe")
	n, _ := d.MarkMissingDone(ctx, exists)
	got, _ = d.GetFile(ctx, res4.ID)
	if n != 1 || got.Status != StatusPending {
		t.Errorf("missing file should be pending again: n=%d %+v", n, got)
	}
}

func TestAuthRoundTrip(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	a, _ := d.GetAuth(ctx)
	if a.RefreshToken != "" {
		t.Fatal("expected empty auth")
	}
	if err := d.SaveAuth(ctx, Auth{RefreshToken: "r", Username: "u"}); err != nil {
		t.Fatal(err)
	}
	a, _ = d.GetAuth(ctx)
	if a.RefreshToken != "r" || a.Username != "u" {
		t.Errorf("auth = %+v", a)
	}
	_ = d.ClearAuth(ctx)
	a, _ = d.GetAuth(ctx)
	if a.RefreshToken != "" {
		t.Error("auth not cleared")
	}
}

func TestGameSelection(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	for _, id := range []int64{1, 2, 3} {
		if err := d.UpsertGame(ctx, Game{ID: id, Title: "G", Folder: "G"}); err != nil {
			t.Fatal(err)
		}
	}
	g, _ := d.GetGame(ctx, 1)
	if g.Selected {
		t.Error("games start unselected")
	}
	if err := d.SetGamesSelected(ctx, []int64{1, 3}, true); err != nil {
		t.Fatal(err)
	}
	games, _ := d.ListGames(ctx)
	want := map[int64]bool{1: true, 2: false, 3: true}
	for _, g := range games {
		if g.Selected != want[g.ID] {
			t.Errorf("game %d selected = %v", g.ID, g.Selected)
		}
	}
	// Re-listing from GOG must not reset the flag.
	if err := d.UpsertGame(ctx, Game{ID: 1, Title: "G2", Folder: "G"}); err != nil {
		t.Fatal(err)
	}
	g, _ = d.GetGame(ctx, 1)
	if !g.Selected || g.Title != "G2" {
		t.Errorf("upsert should keep selection: %+v", g)
	}
	if err := d.SetGamesSelected(ctx, []int64{1}, false); err != nil {
		t.Fatal(err)
	}
	g, _ = d.GetGame(ctx, 1)
	if g.Selected {
		t.Error("deselect failed")
	}
	all := Settings{}
	sel := Settings{DownloadMode: DownloadSelected}
	if !all.WantsGame(*g) || sel.WantsGame(*g) {
		t.Error("WantsGame should depend on the mode")
	}
	norm := DefaultSettings()
	norm.DownloadMode = "Selected "
	if err := norm.Normalize(); err != nil || norm.DownloadMode != DownloadSelected {
		t.Errorf("normalize mode: %q %v", norm.DownloadMode, err)
	}
	bad := DefaultSettings()
	bad.DownloadMode = "some"
	if err := bad.Normalize(); err == nil {
		t.Error("invalid mode should be rejected")
	}
}

// GOG's manifest reports sizes rounded to whole MiB while the downloader records
// the exact byte count. Comparing the two must not make a finished file look
// updated on every sync.
func TestManifestSizeIsNotComparedWithMeasuredSize(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	_ = d.UpsertGame(ctx, Game{ID: 1, Title: "Game", Folder: "Game"})
	_ = d.UpsertProduct(ctx, Product{ID: 1, GameID: 1, Title: "Game"})
	onDisk := map[string]bool{"Game/windows/setup.exe": true}
	exists := func(p string) bool { return onDisk[p] }
	f := File{GameID: 1, ProductID: 1, Kind: "installer", OS: "windows", Language: "en", GogID: "en1installer0", Name: "Game", Version: "1.0", Size: 108003328, Downlink: "x", RelDir: "windows"}
	res, err := d.UpsertDesiredFile(ctx, f, exists)
	if err != nil {
		t.Fatal(err)
	}
	// The transfer learns the real size, then finishes.
	if err := d.SetFileResolved(ctx, res.ID, "setup.exe", "Game/windows/setup.exe", "", 107911234); err != nil {
		t.Fatal(err)
	}
	if err := d.SetFileDone(ctx, res.ID, "Game/windows/setup.exe", 107911234); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		res2, err := d.UpsertDesiredFile(ctx, f, exists)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := d.GetFile(ctx, res2.ID)
		if res2.Updated || got.Status != StatusDone || got.Size != 107911234 || got.ManifestSize != 108003328 {
			t.Fatalf("sync %d with an unchanged manifest: updated=%v %+v", i, res2.Updated, got)
		}
	}
	// A manifest without a size must not wipe the measured size either.
	noSize := f
	noSize.Size = 0
	res3, _ := d.UpsertDesiredFile(ctx, noSize, exists)
	if got, _ := d.GetFile(ctx, res3.ID); res3.Updated || got.Size != 107911234 {
		t.Errorf("manifest without size: updated=%v %+v", res3.Updated, got)
	}
	// Rows from before manifest_size existed have 0 there: no change until known.
	if _, err := d.ExecContext(ctx, `UPDATE files SET manifest_size = 0 WHERE id = ?`, res.ID); err != nil {
		t.Fatal(err)
	}
	res4, _ := d.UpsertDesiredFile(ctx, f, exists)
	if got, _ := d.GetFile(ctx, res4.ID); res4.Updated || got.Status != StatusDone || got.ManifestSize != 108003328 {
		t.Errorf("legacy row: updated=%v %+v", res4.Updated, got)
	}
	// A different manifest size is a real change.
	bigger := f
	bigger.Size = 109051904
	res5, _ := d.UpsertDesiredFile(ctx, bigger, exists)
	if got, _ := d.GetFile(ctx, res5.ID); !res5.Updated || got.Status != StatusPending || got.PreviousPath != "Game/windows/setup.exe" || got.Size != 109051904 {
		t.Errorf("changed manifest size: updated=%v %+v", res5.Updated, got)
	}
	// Reactivating a kept file compares manifest sizes too.
	if err := d.SetFileDone(ctx, res.ID, "Game/windows/setup.exe", 108900000); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DeactivateOtherFiles(ctx, 1, nil); err != nil {
		t.Fatal(err)
	}
	res6, _ := d.UpsertDesiredFile(ctx, bigger, exists)
	if got, _ := d.GetFile(ctx, res6.ID); got.Status != StatusDone || got.Size != 108900000 {
		t.Errorf("reactivated unchanged file should stay done with its measured size: %+v", got)
	}
}

// A new build must also be fetched for a file whose previous build failed for
// good, and a kept (inactive) older build must be replaced, not orphaned.
func TestNewVersionSupersedesErrorsAndKeptCopies(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	_ = d.UpsertGame(ctx, Game{ID: 1, Title: "Game", Folder: "Game"})
	_ = d.UpsertProduct(ctx, Product{ID: 1, GameID: 1, Title: "Game"})
	onDisk := map[string]bool{}
	exists := func(p string) bool { return onDisk[p] }
	f := File{GameID: 1, ProductID: 1, Kind: "installer", OS: "windows", Language: "en", GogID: "en1installer0", Name: "Game", Version: "1.0", Size: 100, Downlink: "x", RelDir: "windows"}
	res, _ := d.UpsertDesiredFile(ctx, f, exists)
	if err := d.SetFileError(ctx, res.ID, "HTTP 403", 0); err != nil {
		t.Fatal(err)
	}
	// Same version again: stays failed (the user has to retry).
	same, _ := d.UpsertDesiredFile(ctx, f, exists)
	if got, _ := d.GetFile(ctx, same.ID); same.Updated || got.Status != StatusError {
		t.Errorf("unchanged failed file: updated=%v %+v", same.Updated, got)
	}
	// New version: queued again with a clean slate.
	f.Version = "1.1"
	upd, _ := d.UpsertDesiredFile(ctx, f, exists)
	if got, _ := d.GetFile(ctx, upd.ID); !upd.Updated || got.Status != StatusPending || got.Error != "" || got.Attempts != 0 {
		t.Errorf("new version of a failed file: updated=%v %+v", upd.Updated, got)
	}

	// Downloaded, then kept (inactive) after a settings change, then wanted again
	// at a newer version: the old copy is remembered so it gets replaced.
	if err := d.SetFileDone(ctx, res.ID, "Game/windows/setup_1.1.exe", 110); err != nil {
		t.Fatal(err)
	}
	onDisk["Game/windows/setup_1.1.exe"] = true
	if _, err := d.DeactivateOtherFiles(ctx, 1, nil); err != nil {
		t.Fatal(err)
	}
	f.Version = "1.2"
	f.Size = 120
	re, _ := d.UpsertDesiredFile(ctx, f, exists)
	got, _ := d.GetFile(ctx, re.ID)
	if !re.Updated || got.Status != StatusPending || got.PreviousPath != "Game/windows/setup_1.1.exe" || !got.Active {
		t.Errorf("reactivated at a new version: updated=%v %+v", re.Updated, got)
	}
}

func TestNormalizeLanguage(t *testing.T) {
	cases := map[string]string{"en": "en", " EN ": "en", "es_mx": "esmx", "es-MX": "esmx", "esmx": "esmx", "gk": "el", "sb": "sr", "el": "el"}
	for in, want := range cases {
		if got := NormalizeLanguage(in); got != want {
			t.Errorf("NormalizeLanguage(%q) = %q, want %q", in, got, want)
		}
	}
	s := DefaultSettings()
	s.Languages = []string{"ES_MX", "gk"}
	if err := s.Normalize(); err != nil || len(s.Languages) != 2 || s.Languages[0] != "esmx" || s.Languages[1] != "el" {
		t.Errorf("normalized languages = %v (%v)", s.Languages, err)
	}
}

// A new build of a file whose last attempt failed must report Changed so the
// stale partial of the old build is discarded before it is resumed.
func TestNewBuildOfFailedFileIsReportedAsChanged(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	_ = d.UpsertGame(ctx, Game{ID: 1, Title: "Game", Folder: "Game"})
	_ = d.UpsertProduct(ctx, Product{ID: 1, GameID: 1, Title: "Game"})
	exists := func(string) bool { return false }
	f := File{GameID: 1, ProductID: 1, Kind: "extra", GogID: "5001", Name: "Soundtrack", Size: 100, Downlink: "x", RelDir: "extras"}
	res, _ := d.UpsertDesiredFile(ctx, f, exists)
	_ = d.SetFileResolved(ctx, res.ID, "soundtrack.zip", "Game/extras/soundtrack.zip", "", 100)
	_ = d.SetFileError(ctx, res.ID, "stalled", 0)
	f.Size = 120
	got, err := d.UpsertDesiredFile(ctx, f, exists)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Changed || got.LocalPath != "Game/extras/soundtrack.zip" {
		t.Errorf("expected Changed with the old path, got %+v", got)
	}
	// Deferring keeps the file queued without using up an attempt.
	if err := d.DeferFile(ctx, res.ID, "no session", time.Minute); err != nil {
		t.Fatal(err)
	}
	row, _ := d.GetFile(ctx, res.ID)
	if row.Status != StatusPending || row.Attempts != 0 || row.Error != "no session" || row.NextAttemptAt.Before(time.Now().Add(30*time.Second)) {
		t.Errorf("deferred file: %+v", row)
	}
}

// A game the account no longer owns cannot be downloaded from GOG; its pending
// files wait instead of failing, and are picked up again once it is owned again.
func TestNextPendingFilesSkipsGamesNotOwned(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	for _, id := range []int64{1, 2} {
		if err := d.UpsertGame(ctx, Game{ID: id, Title: "Game", Folder: "Game"}); err != nil {
			t.Fatal(err)
		}
		if err := d.UpsertProduct(ctx, Product{ID: id, GameID: id, Title: "Game"}); err != nil {
			t.Fatal(err)
		}
		f := File{GameID: id, ProductID: id, Kind: "installer", OS: "windows", Language: "en", GogID: "a", Name: "Game", Version: "1", Size: 1, Downlink: "x", RelDir: "windows"}
		if _, err := d.UpsertDesiredFile(ctx, f, func(string) bool { return false }); err != nil {
			t.Fatal(err)
		}
	}
	// Game 2 has left the account.
	if err := d.MarkGamesNotOwned(ctx, []int64{1}); err != nil {
		t.Fatal(err)
	}
	next, err := d.NextPendingFiles(ctx, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].GameID != 1 {
		t.Fatalf("queue = %+v, want only game 1's file", next)
	}
	// It is listed again: its file is downloadable once more.
	if err := d.UpsertGame(ctx, Game{ID: 2, Title: "Game", Folder: "Game"}); err != nil {
		t.Fatal(err)
	}
	if next, _ = d.NextPendingFiles(ctx, 10, nil); len(next) != 2 {
		t.Fatalf("queue = %+v, want both files", next)
	}
}

// A transfer that fails after its row was dropped (deselected, platform
// removed) or kept as inactive must not write the row back to pending: nothing
// tracks it anymore, and an inactive row that says "pending" is shown as such.
func TestSetFileErrorLeavesDroppedRowsAlone(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	if err := d.UpsertGame(ctx, Game{ID: 1, Title: "Game", Folder: "Game"}); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertProduct(ctx, Product{ID: 1, GameID: 1, Title: "Game"}); err != nil {
		t.Fatal(err)
	}
	f := File{GameID: 1, ProductID: 1, Kind: "installer", OS: "windows", Language: "en", GogID: "a", Name: "Game", Version: "1", Size: 1, Downlink: "x", RelDir: "windows"}
	res, err := d.UpsertDesiredFile(ctx, f, func(string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetFileInactive(ctx, res.ID); err != nil {
		t.Fatal(err)
	}
	for _, retry := range []time.Duration{time.Minute, 0} {
		if err := d.SetFileError(ctx, res.ID, "boom", retry); err != nil {
			t.Fatal(err)
		}
		got, _ := d.GetFile(ctx, res.ID)
		if got.Active || got.Status != StatusInactive || got.Error != "" {
			t.Errorf("retry %v: dropped row written back: %+v", retry, got)
		}
	}
}

// "Keep" answered while an update is transferring keeps what was on disk when
// the question was asked, the previous version: the transfer that finishes
// afterwards must not turn the kept row into a done copy of the new build.
func TestCompleteFileRefusesDroppedRow(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	if err := d.UpsertGame(ctx, Game{ID: 1, Title: "Game", Folder: "Game"}); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertProduct(ctx, Product{ID: 1, GameID: 1, Title: "Game"}); err != nil {
		t.Fatal(err)
	}
	f := File{GameID: 1, ProductID: 1, Kind: "installer", OS: "windows", Language: "en", GogID: "a", Name: "Game", Version: "1", Size: 1, Downlink: "x", RelDir: "windows"}
	res, err := d.UpsertDesiredFile(ctx, f, func(string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetFileInactive(ctx, res.ID); err != nil {
		t.Fatal(err)
	}
	ok, err := d.CompleteFile(ctx, res.ID, "Game/windows/setup.exe", 1, "x", "1")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a dropped row must not be completed")
	}
	got, _ := d.GetFile(ctx, res.ID)
	if got.Active || got.Status != StatusInactive || got.LocalPath != "" {
		t.Errorf("row changed: %+v", got)
	}
}

// The downloads page lists the queue in the order the downloader takes it,
// with the files it passes over for now (retry delay, game not owned) last.
func TestListQueuedFilesFollowsTheDownloadOrder(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	for id := int64(1); id <= 3; id++ {
		if err := d.UpsertGame(ctx, Game{ID: id, Title: "Game", Folder: "Game"}); err != nil {
			t.Fatal(err)
		}
		if err := d.UpsertProduct(ctx, Product{ID: id, GameID: id, Title: "Game"}); err != nil {
			t.Fatal(err)
		}
	}
	add := func(game int64, gogID string, size int64) int64 {
		t.Helper()
		f := File{GameID: game, ProductID: game, Kind: "installer", OS: "windows", Language: "en", GogID: gogID, Name: gogID, Version: "1", Size: size, Downlink: "x" + gogID, RelDir: "windows"}
		res, err := d.UpsertDesiredFile(ctx, f, func(string) bool { return false })
		if err != nil {
			t.Fatal(err)
		}
		return res.ID
	}
	late := add(2, "late", 10)
	big := add(1, "big", 500)
	small := add(1, "small", 100)
	waiting := add(1, "waiting", 50)
	unowned := add(3, "unowned", 1)
	if err := d.SetFileError(ctx, waiting, "boom", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkGamesNotOwned(ctx, []int64{1, 2}); err != nil {
		t.Fatal(err)
	}
	got, err := d.ListQueuedFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{small, big, late, waiting, unowned}
	ids := make([]int64, len(got))
	for i, f := range got {
		ids[i] = f.ID
	}
	for i := range want {
		if i >= len(ids) || ids[i] != want[i] {
			t.Fatalf("queue order = %v, want %v", ids, want)
		}
	}
}

// A game's opt-ins, patches included, survive the round trip; the settings
// know that patches exist per platform and change the plan.
func TestGameOptionsRoundTrip(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	if err := d.UpsertGame(ctx, Game{ID: 1, Title: "G", Folder: "G"}); err != nil {
		t.Fatal(err)
	}
	want := GameOptions{Patches: true, Saves: true}
	if err := d.SetGameOptions(ctx, 1, want); err != nil {
		t.Fatal(err)
	}
	g, err := d.GetGame(ctx, 1)
	if err != nil || g == nil {
		t.Fatal(err)
	}
	if g.Options != want {
		t.Errorf("options = %+v, want %+v", g.Options, want)
	}
	s := DefaultSettings()
	s.IncludeInstallers, s.IncludeDLC = false, false
	if s.NeedsPlatforms() {
		t.Error("nothing per platform is wanted, so no platform is needed")
	}
	if !s.ForGame(*g).IncludePatches {
		t.Error("the game's opt-in should switch patches on for it")
	}
	s.IncludePatches = true
	if !s.NeedsPlatforms() {
		t.Error("patches exist per platform, so one has to be chosen")
	}
	o := s
	o.IncludePatches = false
	if s.SamePlan(o) {
		t.Error("the patches setting changes the plan")
	}
}
