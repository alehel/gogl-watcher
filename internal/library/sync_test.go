package library

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/alehel/gogl-watcher/internal/db"
	"github.com/alehel/gogl-watcher/internal/gog"
)

// gatedAPI blocks ProductDetails until gate is closed (or the context ends).
type gatedAPI struct {
	*gog.Mock
	gate chan struct{}
}

func (g gatedAPI) ProductDetails(ctx context.Context, id int64) (*gog.Product, error) {
	select {
	case <-g.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return g.Mock.ProductDetails(ctx, id)
}

func newSyncTest(t *testing.T, api gog.API) (*db.DB, *Syncer, Paths) {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	paths := Paths{Root: filepath.Join(dir, "lib")}
	return d, NewSyncer(d, api, paths, slog.Default()), paths
}

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

func saveSettings(t *testing.T, d *db.DB, platforms ...string) db.Settings {
	t.Helper()
	s := db.DefaultSettings()
	s.Platforms = platforms
	s.ContentChosen = true
	s.DownloadMode = db.DownloadAll
	if err := d.SaveSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	_ = d.SetSetupComplete(context.Background(), true)
	return s
}

// A sync that is interrupted (shutdown, logout) must not count as a finished run:
// otherwise the scheduler waits a whole interval before trying again, and the UI
// shows "context canceled" as the last error.
func TestCancelledSyncIsNotAFinishedRun(t *testing.T) {
	m, _ := gog.NewMock(context.Background(), nil)
	_, _ = m.ExchangeCode(context.Background(), "code")
	api := gatedAPI{Mock: m, gate: make(chan struct{})}
	d, syncer, _ := newSyncTest(t, api)
	saveSettings(t, d, "windows")

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- syncer.SyncAll(ctx) }()
	waitFor(t, 5*time.Second, func() bool { return syncer.Status().Phase == "details" })
	cancel()
	if err := <-errc; err == nil {
		t.Fatal("cancelled sync should return an error to its caller")
	}
	st := syncer.Status()
	if st.Running {
		t.Fatal("sync still marked running")
	}
	if st.LastFinishedAt != nil {
		t.Errorf("an interrupted sync must not record a finish time, got %v", st.LastFinishedAt)
	}
	if st.LastError != nil {
		t.Errorf("an interrupted sync must not record an error, got %q", *st.LastError)
	}
	if ts, _ := d.GetTime(context.Background(), "sync.last_finished_at"); ts != nil {
		t.Errorf("finish time persisted for an interrupted sync: %v", ts)
	}
	if e, _ := d.GetKV(context.Background(), "sync.last_error"); e != "" {
		t.Errorf("error persisted for an interrupted sync: %q", e)
	}
}

// Applying settings while a sync is running must not let that sync (which planned
// with the old settings) re-add the files the settings change just dropped.
func TestApplySettingsDuringSyncDoesNotResurrectDroppedFiles(t *testing.T) {
	m, _ := gog.NewMock(context.Background(), nil)
	_, _ = m.ExchangeCode(context.Background(), "code")
	api := gatedAPI{Mock: m, gate: make(chan struct{})}
	d, syncer, _ := newSyncTest(t, api)
	ctx := context.Background()
	old := saveSettings(t, d, "windows", "linux")

	// First sync with both platforms (gate open).
	close(api.gate)
	if err := syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	files, _ := d.ListActiveFiles(ctx)
	hasLinux := func(fs []db.File) bool {
		for _, f := range fs {
			if f.OS == "linux" {
				return true
			}
		}
		return false
	}
	if !hasLinux(files) {
		t.Fatal("expected linux files after the first sync")
	}

	// Second sync stalls in the details phase; meanwhile the user removes linux.
	api.gate = make(chan struct{})
	syncer.gog = api
	errc := make(chan error, 1)
	go func() { errc <- syncer.SyncAll(ctx) }()
	waitFor(t, 5*time.Second, func() bool { return syncer.Status().Phase == "details" })
	ns := old
	ns.Platforms = []string{"windows"}
	if err := syncer.ApplySettings(ctx, ns, RemovalKeep); err != nil {
		t.Fatal(err)
	}
	close(api.gate)
	<-errc
	// The sync that was running when the settings changed must not have planned
	// with the old settings: the linux files stay dropped (and are not queued for
	// download) until the next sync, which uses the new settings.
	files, _ = d.ListActiveFiles(ctx)
	if hasLinux(files) {
		t.Errorf("the interrupted sync re-added linux files although the platform was removed")
	}
	if err := syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	files, _ = d.ListActiveFiles(ctx)
	if hasLinux(files) {
		t.Errorf("linux files are tracked again although the platform was removed")
	}
}

// The scheduler restarts a cancelled sync right away; that sync must wait until
// the settings change that cancelled it has stored the new settings.
func TestSyncStartedDuringSettingsChangeWaitsForNewSettings(t *testing.T) {
	m, _ := gog.NewMock(context.Background(), nil)
	_, _ = m.ExchangeCode(context.Background(), "code")
	d, syncer, _ := newSyncTest(t, m)
	ctx := context.Background()
	old := saveSettings(t, d, "windows", "linux")

	// Simulate ApplySettings holding the plan lock while it stores the settings.
	if !syncer.lockPlan(ctx) {
		t.Fatal("lock")
	}
	errc := make(chan error, 1)
	go func() { errc <- syncer.SyncAll(ctx) }()
	waitFor(t, 5*time.Second, func() bool { return syncer.Status().Running })
	time.Sleep(200 * time.Millisecond)
	if games, _ := d.ListGames(ctx); len(games) != 0 {
		t.Fatalf("sync planned before the settings change finished: %d games", len(games))
	}
	ns := old
	ns.Platforms = []string{"windows"}
	_ = d.SaveSettings(ctx, ns)
	syncer.unlockPlan()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	files, _ := d.ListActiveFiles(ctx)
	for _, f := range files {
		if f.OS == "linux" {
			t.Fatalf("the restarted sync used the old settings: %+v", f)
		}
	}
}

// A settings change that needs an answer must be refused before it disturbs a
// running sync.
func TestRefusedSettingsChangeLeavesRunningSyncAlone(t *testing.T) {
	m, _ := gog.NewMock(context.Background(), nil)
	_, _ = m.ExchangeCode(context.Background(), "code")
	api := gatedAPI{Mock: m, gate: make(chan struct{})}
	d, syncer, paths := newSyncTest(t, api)
	ctx := context.Background()
	old := saveSettings(t, d, "windows", "linux")
	close(api.gate)
	if err := syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	// One linux file downloaded, so removing linux needs keep/delete.
	files, _ := d.ListActiveFiles(ctx)
	for _, f := range files {
		if f.OS == "linux" {
			rel := "Game/linux/x.sh"
			_ = os.MkdirAll(filepath.Dir(paths.Abs(rel)), 0o755)
			_ = os.WriteFile(paths.Abs(rel), []byte("x"), 0o644)
			_ = d.SetFileDone(ctx, f.ID, rel, 1)
			break
		}
	}
	api.gate = make(chan struct{})
	syncer.gog = api
	errc := make(chan error, 1)
	go func() { errc <- syncer.SyncAll(ctx) }()
	waitFor(t, 5*time.Second, func() bool { return syncer.Status().Phase == "details" })
	ns := old
	ns.Platforms = []string{"windows"}
	err := syncer.ApplySettings(ctx, ns, "")
	var cr *ErrConfirmationRequired
	if !errors.As(err, &cr) {
		t.Fatalf("expected confirmation_required, got %v", err)
	}
	if !syncer.Status().Running {
		t.Fatal("a refused settings change must not cancel the running sync")
	}
	close(api.gate)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

// A file whose newer version is pending still has the previous version on disk.
// Dropping it (deselecting the game, removing the platform) must treat that copy
// like any downloaded file: ask, keep it on "keep", remove it on "delete", and
// never delete it silently.
func TestDroppingPendingUpdateKeepsPreviousVersion(t *testing.T) {
	for _, answer := range []RemovalAction{RemovalKeep, RemovalDelete} {
		t.Run(string(answer), func(t *testing.T) {
			m, _ := gog.NewMock(context.Background(), nil)
			_, _ = m.ExchangeCode(context.Background(), "code")
			d, syncer, paths := newSyncTest(t, m)
			ctx := context.Background()
			s := saveSettings(t, d, "windows")
			s.DownloadMode = db.DownloadSelected
			_ = d.SaveSettings(ctx, s)
			if err := d.UpsertGame(ctx, db.Game{ID: 1, Title: "Game", Folder: "Game"}); err != nil {
				t.Fatal(err)
			}
			_ = d.SetGamesSelected(ctx, []int64{1}, true)
			_ = d.UpsertProduct(ctx, db.Product{ID: 1, GameID: 1, Title: "Game"})
			f := db.File{GameID: 1, ProductID: 1, Kind: "extra", GogID: "5001", Name: "Soundtrack", Size: 10, Downlink: "x", RelDir: "extras"}
			res, _ := d.UpsertDesiredFile(ctx, f, paths.Exists)
			const rel = "Game/extras/soundtrack.zip"
			abs := paths.Abs(rel)
			_ = os.MkdirAll(filepath.Dir(abs), 0o755)
			_ = os.WriteFile(abs, []byte("0123456789"), 0o644)
			if err := d.SetFileDone(ctx, res.ID, rel, 10); err != nil {
				t.Fatal(err)
			}
			// GOG replaced the file (same name, new size): pending again, old copy kept.
			f.Size = 12
			if _, err := d.UpsertDesiredFile(ctx, f, paths.Exists); err != nil {
				t.Fatal(err)
			}
			got, _ := d.GetFile(ctx, res.ID)
			if got.Status != db.StatusPending || got.PreviousPath != rel {
				t.Fatalf("setup: %+v", got)
			}

			err := syncer.SetSelection(ctx, []int64{1}, false, "")
			var cr *ErrConfirmationRequired
			if !errors.As(err, &cr) {
				t.Fatalf("deselecting must ask what to do with the previous version, got %v", err)
			}
			if cr.Preview.Removed.DownloadedFiles != 1 {
				t.Errorf("preview should count the previous version as downloaded: %+v", cr.Preview.Removed)
			}
			if _, err := os.Stat(abs); err != nil {
				t.Fatalf("previous version deleted although nothing was confirmed: %v", err)
			}
			if err := syncer.SetSelection(ctx, []int64{1}, false, answer); err != nil {
				t.Fatal(err)
			}
			_, statErr := os.Stat(abs)
			got, _ = d.GetFile(ctx, res.ID)
			switch answer {
			case RemovalKeep:
				if statErr != nil {
					t.Errorf("keep must leave the previous version on disk: %v", statErr)
				}
				if got == nil || got.Active || got.Status != db.StatusInactive {
					t.Errorf("kept file should be inactive: %+v", got)
				}
			case "delete":
				if statErr == nil {
					t.Errorf("delete must remove the previous version from disk")
				}
				if got != nil {
					t.Errorf("deleted file should be forgotten: %+v", got)
				}
			}
		})
	}
}

// When GOG changes the version (and download link) of a file that is currently
// being downloaded, the syncer must abort that transfer so the stale bytes are
// never completed under the new version.
func TestSyncAbortsTransferOfUpdatedFile(t *testing.T) {
	m, _ := gog.NewMock(context.Background(), nil)
	_, _ = m.ExchangeCode(context.Background(), "code")
	d, syncer, _ := newSyncTest(t, m)
	ctx := context.Background()
	saveSettings(t, d, "windows")
	var dropped []int64
	syncer.OnDrop = func(id int64) { dropped = append(dropped, id) }
	if err := syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	dropped = nil
	game := m.Games[0]
	files, _ := d.ListActiveFilesByGame(ctx, game.Listed.ID)
	if len(files) == 0 {
		t.Fatal("no files planned")
	}
	// GOG publishes a new build of the first installer.
	inst := &m.Games[0].Product.Downloads.Installers[0]
	inst.Version = inst.Version + "-new"
	for i := range inst.Files {
		inst.Files[i].Downlink += "&v=2"
	}
	if err := syncer.SyncGame(ctx, game.Listed.ID); err != nil {
		t.Fatal(err)
	}
	if len(dropped) != len(inst.Files) {
		t.Fatalf("expected the %d updated (pending) files to have their transfers aborted, OnDrop called for %v", len(inst.Files), dropped)
	}
}

// Files GOG no longer offers must have any running transfer aborted, otherwise the
// download completes into a file nothing tracks.
func TestSyncAbortsTransferOfRemovedFile(t *testing.T) {
	m, _ := gog.NewMock(context.Background(), nil)
	_, _ = m.ExchangeCode(context.Background(), "code")
	d, syncer, _ := newSyncTest(t, m)
	ctx := context.Background()
	saveSettings(t, d, "windows")
	var dropped []int64
	syncer.OnDrop = func(id int64) { dropped = append(dropped, id) }
	if err := syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	dropped = nil
	game := m.Games[0]
	before, _ := d.ListActiveFilesByGame(ctx, game.Listed.ID)
	inst := &m.Games[0].Product.Downloads.Installers[0]
	removed := inst.Files[len(inst.Files)-1]
	inst.Files = inst.Files[:len(inst.Files)-1]
	if err := syncer.SyncGame(ctx, game.Listed.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := d.ListActiveFilesByGame(ctx, game.Listed.ID)
	if len(after) != len(before)-1 {
		t.Fatalf("expected one file dropped, %d -> %d", len(before), len(after))
	}
	var want int64
	for _, f := range before {
		if f.GogID == string(removed.ID) {
			want = f.ID
		}
	}
	if len(dropped) != 1 || dropped[0] != want {
		t.Errorf("OnDrop should be called for the removed file %d, got %v", want, dropped)
	}
}

func TestSanitizeFolderKeepsValidUTF8(t *testing.T) {
	long := "a" + strings.Repeat("€", 80)
	got := SanitizeFolder(long)
	if !utf8.ValidString(got) {
		t.Errorf("truncated folder name is not valid UTF-8: %q", got)
	}
	if utf8.RuneCountInString(got) > 120 || len(got) > 120 {
		t.Errorf("folder name too long: %d bytes", len(got))
	}
}

// A downloaded file that vanished is queued again, but an empty or missing
// library folder means the volume is not there, not that everything was deleted.
func TestCheckMissingRefusesUnavailableLibrary(t *testing.T) {
	m, _ := gog.NewMock(context.Background(), nil)
	d, syncer, paths := newSyncTest(t, m)
	ctx := context.Background()
	_ = d.UpsertGame(ctx, db.Game{ID: 1, Title: "Game", Folder: "Game"})
	_ = d.UpsertProduct(ctx, db.Product{ID: 1, GameID: 1, Title: "Game"})
	f := db.File{GameID: 1, ProductID: 1, Kind: "installer", OS: "windows", Language: "en", GogID: "a", Name: "Game", Version: "1", Size: 3, Downlink: "x", RelDir: "windows"}
	res, _ := d.UpsertDesiredFile(ctx, f, paths.Exists)
	abs := paths.Abs("Game/windows/setup.exe")
	_ = os.MkdirAll(filepath.Dir(abs), 0o755)
	_ = os.WriteFile(abs, []byte("abc"), 0o644)
	_ = d.SetFileDone(ctx, res.ID, "Game/windows/setup.exe", 3)

	// Library folder gone entirely (volume not mounted).
	_ = os.RemoveAll(paths.Root)
	if _, err := syncer.CheckMissing(ctx); !errors.Is(err, ErrLibraryUnavailable) {
		t.Fatalf("missing root: err = %v", err)
	}
	// Mounted but empty: same thing.
	_ = os.MkdirAll(paths.Root, 0o755)
	if _, err := syncer.CheckMissing(ctx); !errors.Is(err, ErrLibraryUnavailable) {
		t.Fatalf("empty root: err = %v", err)
	}
	if got, _ := d.GetFile(ctx, res.ID); got.Status != db.StatusDone {
		t.Fatalf("file must stay done while the library is unavailable: %+v", got)
	}
	// Folder present with other content, this file missing: queued again.
	_ = os.MkdirAll(filepath.Join(paths.Root, "Other Game"), 0o755)
	n, err := syncer.CheckMissing(ctx)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if got, _ := d.GetFile(ctx, res.ID); got.Status != db.StatusPending {
		t.Errorf("vanished file should be pending: %+v", got)
	}
}
