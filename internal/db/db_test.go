package db

import (
	"context"
	"path/filepath"
	"testing"
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
