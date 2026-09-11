package library

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/alehel/gogl-watcher/internal/db"
	"github.com/alehel/gogl-watcher/internal/gog"
)

const (
	md5A = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	md5B = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// verifyFixture is one game with one Windows installer whose metadata and
// published checksum the test controls.
type verifyFixture struct {
	mock   *gog.Mock
	db     *db.DB
	syncer *Syncer
	paths  Paths
	file   db.File
}

const verifyDownlink = "mock://1/w1/setup_game.exe?size=1000"

// setInstaller republishes the game with this version and manifest size.
func (f *verifyFixture) setInstaller(version string, size int64) {
	f.mock.Games = []gog.MockGame{{
		Listed: gog.ListedGame{ID: 1, Title: "Game", Slug: "game", WorksWindows: true},
		Product: gog.Product{ID: 1, Title: "Game", Downloads: gog.Downloads{Installers: []gog.Installer{{
			ID: "installer_windows_en", Name: "Game", OS: "windows", Language: "en", Version: version,
			Files: []gog.DownloadFile{{ID: "w1", Size: gog.FlexInt(size), Downlink: verifyDownlink}},
		}}}},
	}}
}

// reload returns the installer row as it stands now.
func (f *verifyFixture) reload(t *testing.T) db.File {
	t.Helper()
	got, err := f.db.GetFile(context.Background(), f.file.ID)
	if err != nil || got == nil {
		t.Fatalf("reloading the file: %v", err)
	}
	return *got
}

// newVerifyFixture syncs the game once and marks its installer downloaded, with
// md5A on disk, which is where every test below starts.
func newVerifyFixture(t *testing.T) *verifyFixture {
	t.Helper()
	ctx := context.Background()
	m, err := gog.NewMock(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ExchangeCode(ctx, "code"); err != nil {
		t.Fatal(err)
	}
	m.Checksums[verifyDownlink] = md5A
	d, syncer, paths := newSyncTest(t, m)
	saveSettings(t, d, "windows")
	f := &verifyFixture{mock: m, db: d, syncer: syncer, paths: paths}
	f.setInstaller("1.0", 1000)
	if err := syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	files, err := d.ListFilesByGame(ctx, 1)
	if err != nil || len(files) != 1 {
		t.Fatalf("expected one planned file, got %d (%v)", len(files), err)
	}
	f.file = files[0]
	// Stand in for a finished download: the real one records GOG's checksum for
	// the bytes it fetched and then marks the row done.
	rel := LocalRelPath("Game", f.file.RelDir, "setup_game.exe")
	abs := paths.Abs(rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("installer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := d.SetFileResolved(ctx, f.file.ID, "setup_game.exe", rel, md5A, 1000); err != nil {
		t.Fatal(err)
	}
	if err := d.SetFileDone(ctx, f.file.ID, rel, 1000); err != nil {
		t.Fatal(err)
	}
	if got := f.reload(t); got.Status != db.StatusDone {
		t.Fatalf("setup: file should be done, is %q", got.Status)
	}
	return f
}

// neverVerified makes every file look like one that has sat on disk since
// before checksum verification existed, which is what the rolling re-check is
// there for. It writes the column directly because nothing in the application
// ever moves a verification backwards.
func neverVerified(t *testing.T, d *db.DB) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(), `UPDATE files SET verified_at = NULL`); err != nil {
		t.Fatal(err)
	}
}

// staleBuildChecks makes the recorded build ids old enough to be looked up
// again, so a test can move a build without waiting out buildCheckInterval.
func staleBuildChecks(t *testing.T, d *db.DB) {
	t.Helper()
	old := time.Now().Add(-2 * buildCheckInterval).UnixMilli()
	if _, err := d.ExecContext(context.Background(), `UPDATE game_builds SET checked_at = ?`, old); err != nil {
		t.Fatal(err)
	}
}

// GOG bumps installer version strings for rebuilds that do not change the
// installer at all. Taking that at face value throws away a copy that may be
// tens of gigabytes, so the checksum decides.
func TestVersionBumpWithIdenticalChecksumKeepsTheLocalCopy(t *testing.T) {
	f := newVerifyFixture(t)
	f.setInstaller("1.1", 1000)
	if err := f.syncer.SyncAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.reload(t)
	if got.Status != db.StatusDone {
		t.Errorf("identical checksum must keep the file done, got %q", got.Status)
	}
	if got.PreviousPath != "" {
		t.Errorf("nothing is being replaced, so no previous path: %q", got.PreviousPath)
	}
	if got.Version != "1.1" {
		t.Errorf("the new version must still be recorded, got %q", got.Version)
	}
	if got.VerifiedAt == nil {
		t.Error("the check should be recorded so it is not repeated every sync")
	}
}

// A real update: the metadata moved and so did the bytes.
func TestVersionBumpWithNewChecksumQueuesTheUpdate(t *testing.T) {
	f := newVerifyFixture(t)
	f.setInstaller("1.1", 1000)
	f.mock.Checksums[verifyDownlink] = md5B
	if err := f.syncer.SyncAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.reload(t)
	if got.Status != db.StatusPending {
		t.Errorf("a changed file must be queued again, got %q", got.Status)
	}
	if got.PreviousPath != f.file.LocalPath && got.PreviousPath == "" {
		t.Error("the old copy must be remembered so it can be replaced")
	}
}

// The case the version string cannot catch: GOG replaces an installer and
// leaves the version and the manifest size alone. The moved Galaxy build id is
// the only hint that anything happened.
func TestMovedGalaxyBuildFindsASilentReplacement(t *testing.T) {
	f := newVerifyFixture(t)
	ctx := context.Background()
	f.mock.SetBuild(1, "windows", "build-1", "1.0")
	if err := f.syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.reload(t); got.Status != db.StatusDone {
		t.Fatalf("the first build id has nothing to compare against: %q", got.Status)
	}
	// The installer is replaced without a word in its metadata.
	f.mock.Checksums[verifyDownlink] = md5B
	f.mock.SetBuild(1, "windows", "build-2", "1.0")
	staleBuildChecks(t, f.db)
	if err := f.syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.reload(t); got.Status != db.StatusPending {
		t.Errorf("a rebuilt game's replaced installer must be queued, got %q", got.Status)
	}
}

// A moved build id on its own means nothing: Galaxy builds and offline
// installers are published separately, so the file is checked and kept.
func TestMovedGalaxyBuildWithoutAFileChangeKeepsTheLocalCopy(t *testing.T) {
	f := newVerifyFixture(t)
	ctx := context.Background()
	f.mock.SetBuild(1, "windows", "build-1", "1.0")
	if err := f.syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	f.mock.SetBuild(1, "windows", "build-2", "1.1")
	staleBuildChecks(t, f.db)
	if err := f.syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.reload(t); got.Status != db.StatusDone {
		t.Errorf("the installer did not change, so it must be kept: %q", got.Status)
	}
}

// Nothing at all points at the file, but its turn comes round eventually: this
// is the only thing that finds a silent replacement of a game Galaxy never
// covers (Linux builds, extras).
func TestRollingRecheckFindsASilentReplacement(t *testing.T) {
	f := newVerifyFixture(t)
	ctx := context.Background()
	neverVerified(t, f.db)
	f.mock.Checksums[verifyDownlink] = md5B
	if err := f.syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	got := f.reload(t)
	if got.Status != db.StatusPending {
		t.Errorf("the rolling re-check must notice the replacement, got %q", got.Status)
	}
	if got.VerifiedAt == nil {
		t.Error("the check should be recorded")
	}
}

// A file checked recently is left alone, so the budget goes to files that have
// waited longest.
func TestRollingRecheckSkipsRecentlyCheckedFiles(t *testing.T) {
	f := newVerifyFixture(t)
	ctx := context.Background()
	if err := f.db.SetFileVerified(ctx, f.file.ID); err != nil {
		t.Fatal(err)
	}
	f.mock.Checksums[verifyDownlink] = md5B
	if err := f.syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.reload(t); got.Status != db.StatusDone {
		t.Errorf("a file checked moments ago must not be checked again, got %q", got.Status)
	}
}

// Without a checksum to compare (GOG publishes none for many extras) the
// metadata has the last word, as it did before any of this existed.
func TestWithoutAChecksumTheMetadataDecides(t *testing.T) {
	f := newVerifyFixture(t)
	delete(f.mock.Checksums, verifyDownlink)
	f.setInstaller("1.1", 1000)
	if err := f.syncer.SyncAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.reload(t); got.Status != db.StatusPending {
		t.Errorf("an unverifiable version change must still queue the file, got %q", got.Status)
	}
}

// The rolling re-check must not grow with the library: it is a fixed number of
// files per sync however many are waiting.
func TestRollingRecheckIsBounded(t *testing.T) {
	ctx := context.Background()
	m, err := gog.NewMock(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ExchangeCode(ctx, "code"); err != nil {
		t.Fatal(err)
	}
	d, syncer, paths := newSyncTest(t, m)
	saveSettings(t, d, "windows")
	// Every file is downloaded, unverified and unsuspicious, and there are more of
	// them than one sync may check.
	const files = verifyBudget + 10
	if err := d.UpsertGame(ctx, db.Game{ID: 1, Title: "Game", Folder: "Game"}); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertProduct(ctx, db.Product{ID: 1, GameID: 1, Title: "Game"}); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i := 0; i < files; i++ {
		rel := filepath.ToSlash(filepath.Join("Game", "extras", "e"+strconv.Itoa(i)+".zip"))
		abs := paths.Abs(rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		f := db.File{GameID: 1, ProductID: 1, Kind: "extra", GogID: "e" + strconv.Itoa(i), Name: "Extra", Size: 1,
			Downlink: "dl" + strconv.Itoa(i), RelDir: "extras"}
		res, err := d.UpsertDesiredFile(ctx, f, paths.Exists)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.SetFileResolved(ctx, res.ID, "e"+strconv.Itoa(i)+".zip", rel, md5A, 1); err != nil {
			t.Fatal(err)
		}
		if err := d.SetFileDone(ctx, res.ID, rel, 1); err != nil {
			t.Fatal(err)
		}
		m.Checksums["dl"+strconv.Itoa(i)] = md5A
		ids = append(ids, res.ID)
	}
	neverVerified(t, d)
	syncer.rollingLeft.Store(verifyBudget)
	verify := syncer.verifier(db.Game{ID: 1, Title: "Game"}, nil, true)
	checked := 0
	for i, id := range ids {
		stored, err := d.GetFile(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		next := db.File{GameID: 1, ProductID: 1, Kind: "extra", GogID: stored.GogID, Downlink: "dl" + strconv.Itoa(i), Size: 1}
		if _, err := verify(ctx, *stored, next, false); err != nil {
			t.Fatal(err)
		}
		if got, _ := d.GetFile(ctx, id); got.VerifiedAt != nil {
			checked++
		}
	}
	if checked != verifyBudget {
		t.Errorf("a sync should check exactly %d files, checked %d", verifyBudget, checked)
	}
}

// Syncing one game follows up only on what that game's own signals say. The
// rolling re-check works through the whole library and belongs to a full sync,
// where its budget is shared out.
func TestSingleGameSyncDoesNotRollingRecheck(t *testing.T) {
	f := newVerifyFixture(t)
	ctx := context.Background()
	neverVerified(t, f.db)
	f.mock.Checksums[verifyDownlink] = md5B
	if err := f.syncer.SyncGame(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if got := f.reload(t); got.VerifiedAt != nil {
		t.Errorf("one game's sync must not spend the library's re-check budget, status %q", got.Status)
	}
}
