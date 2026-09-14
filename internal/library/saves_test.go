package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/alehel/gogl-watcher/internal/db"
	"github.com/alehel/gogl-watcher/internal/gog"
)

func TestSavePaths(t *testing.T) {
	// The object's own path is mirrored under the saves folder, whatever
	// Galaxy called its top-level directory; unsafe characters and segments
	// that would climb out of the folder are neutralised.
	cases := map[string][2]string{
		"slot1.sav":                    {"saves", "slot1.sav"},
		"saves/Farmer_1/Farmer_1":      {"saves/saves/Farmer_1", "Farmer_1"},
		"__default/AutoSave-0.dat":     {"saves/__default", "AutoSave-0.dat"},
		"../../etc/passwd":             {"saves/etc", "passwd"},
		"a/../b/c:d.sav":               {"saves/b", "c d.sav"},
		"profile <1>/Quick: Save?.sav": {"saves/profile  1", "Quick  Save .sav"},
	}
	for name, want := range cases {
		if dir, file := SaveRelDir(name), SaveFilename(name); dir != want[0] || file != want[1] {
			t.Errorf("%q: got %q / %q, want %q / %q", name, dir, file, want[0], want[1])
		}
	}
	link := SaveDownlink("123", "saves/a/b.sav")
	if id, name, ok := ParseSaveDownlink(link); !ok || id != "123" || name != "saves/a/b.sav" {
		t.Errorf("ParseSaveDownlink(%q) = %q, %q, %v", link, id, name, ok)
	}
	if _, _, ok := ParseSaveDownlink("https://api.gog.com/products/1/downlink/installer/x"); ok {
		t.Error("an installer downlink is not a save")
	}
}

// flakyCloud fails the listing of cloud saves on demand.
type flakyCloud struct {
	*gog.Mock
	fail bool
}

func (f *flakyCloud) ListCloudSaves(ctx context.Context, c gog.GameClient) ([]gog.CloudSave, error) {
	if f.fail {
		return nil, errors.New("cloud storage down")
	}
	return f.Mock.ListCloudSaves(ctx, c)
}

func saveFiles(t *testing.T, d *db.DB, gameID int64) []db.File {
	t.Helper()
	files, err := d.ListActiveFilesByGame(context.Background(), gameID)
	if err != nil {
		t.Fatal(err)
	}
	var out []db.File
	for _, f := range files {
		if f.Kind == db.KindSave {
			out = append(out, f)
		}
	}
	return out
}

func TestSyncPlansCloudSaves(t *testing.T) {
	m, _ := gog.NewMock(context.Background(), nil)
	_, _ = m.ExchangeCode(context.Background(), "code")
	api := &flakyCloud{Mock: m}
	d, syncer, paths := newSyncTest(t, api)
	ctx := context.Background()
	s := saveSettings(t, d, "windows")
	const stardew, witcher = 1207664663, 1207658924
	const client = "50000000000000001"

	// Saves are off by default: nothing of them is planned.
	if err := syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(saveFiles(t, d, stardew)); n != 0 {
		t.Fatalf("%d saves planned although saves are off", n)
	}

	s.IncludeSaves = true
	if err := syncer.ApplySettings(ctx, s, ""); err != nil {
		t.Fatal(err)
	}
	if err := syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	saves := saveFiles(t, d, stardew)
	if len(saves) != 3 {
		t.Fatalf("want 3 saves planned for Stardew Valley, got %+v", saves)
	}
	byName := map[string]db.File{}
	for _, f := range saves {
		byName[f.Name] = f
		if f.Status != db.StatusPending || f.ProductID != stardew || f.OS != "" {
			t.Errorf("unexpected save row: %+v", f)
		}
	}
	farmer := byName["saves/Farmer_123456789/Farmer_123456789"]
	if farmer.RelDir != "saves/saves/Farmer_123456789" || farmer.Version == "" || farmer.Size != 180*1024 {
		t.Errorf("unexpected save row: %+v", farmer)
	}
	if _, name, ok := ParseSaveDownlink(farmer.Downlink); !ok || name != farmer.Name {
		t.Errorf("save downlink %q does not name the object", farmer.Downlink)
	}
	// A game without a Galaxy client has no saves, and its client lookup is remembered.
	if n := len(saveFiles(t, d, witcher)); n != 0 {
		t.Errorf("%d saves planned for a game without cloud storage", n)
	}
	if rec, err := d.GetGameClient(ctx, witcher, "windows"); err != nil || rec == nil || rec.ClientID != "" {
		t.Errorf("the missing client should be recorded, got %+v, %v", rec, err)
	}
	if rec, err := d.GetGameClient(ctx, stardew, "windows"); err != nil || rec == nil || rec.ClientID != client {
		t.Errorf("the client should be recorded, got %+v, %v", rec, err)
	}

	// A downloaded save that is rewritten in the cloud is fetched again; the
	// old copy is kept until the new one is in place.
	const farmerPath = "Stardew Valley/saves/saves/Farmer_123456789/Farmer_123456789"
	if err := os.MkdirAll(filepath.Dir(paths.Abs(farmerPath)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Abs(farmerPath), []byte("save"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := d.SetFileDone(ctx, farmer.ID, farmerPath, 4); err != nil {
		t.Fatal(err)
	}
	if err := syncer.SyncGame(ctx, stardew); err != nil {
		t.Fatal(err)
	}
	if f, _ := d.GetFile(ctx, farmer.ID); f.Status != db.StatusDone {
		t.Fatalf("an unchanged save was planned again: %+v", f)
	}
	m.PutSave(client, farmer.Name, 181*1024)
	if err := syncer.SyncGame(ctx, stardew); err != nil {
		t.Fatal(err)
	}
	if f, _ := d.GetFile(ctx, farmer.ID); f.Status != db.StatusPending || f.PreviousPath == "" || f.Version == farmer.Version {
		t.Fatalf("a rewritten save should be pending again with the old copy kept: %+v", f)
	}

	// A listing that cannot be read leaves the known saves alone.
	api.fail = true
	if err := syncer.SyncGame(ctx, stardew); err != nil {
		t.Fatal(err)
	}
	if n := len(saveFiles(t, d, stardew)); n != 3 {
		t.Fatalf("a failed listing dropped saves: %d left", n)
	}
	api.fail = false

	// A save deleted in the cloud is no longer tracked; a downloaded copy stays.
	m.DeleteSave(client, "saves/startup_preferences")
	if err := syncer.SyncGame(ctx, stardew); err != nil {
		t.Fatal(err)
	}
	if n := len(saveFiles(t, d, stardew)); n != 2 {
		t.Fatalf("want 2 saves after one was deleted, got %d", n)
	}

	// Turning saves off asks about the downloaded copy, then drops the rows.
	s.IncludeSaves = false
	p, err := syncer.PreviewSettings(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	// Other sample games have saves too; the one downloaded copy is Stardew's.
	if !p.NeedsConfirmation || p.Removed.DownloadedFiles != 1 || p.Removed.Files < 2 || len(p.Reasons) != 1 || p.Reasons[0] != "saves" {
		t.Fatalf("unexpected preview: %+v", p)
	}
	if err := syncer.ApplySettings(ctx, s, RemovalKeep); err != nil {
		t.Fatal(err)
	}
	if n := len(saveFiles(t, d, stardew)); n != 0 {
		t.Fatalf("%d saves still tracked with saves off", n)
	}
}

func TestGameOptsIntoCloudSavesOnItsOwn(t *testing.T) {
	m, _ := gog.NewMock(context.Background(), nil)
	_, _ = m.ExchangeCode(context.Background(), "code")
	d, syncer, _ := newSyncTest(t, m)
	ctx := context.Background()
	saveSettings(t, d, "windows")
	const stardew = 1207664663

	if err := syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(saveFiles(t, d, stardew)); n != 0 {
		t.Fatalf("%d saves planned although saves are off", n)
	}
	if err := syncer.SetGameOptions(ctx, stardew, false, false, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := syncer.SyncGame(ctx, stardew); err != nil {
		t.Fatal(err)
	}
	if n := len(saveFiles(t, d, stardew)); n == 0 {
		t.Fatal("no saves planned after the game opted in")
	}
	if err := syncer.SetGameOptions(ctx, stardew, false, false, false, ""); err != nil {
		t.Fatal(err)
	}
	if n := len(saveFiles(t, d, stardew)); n != 0 {
		t.Fatalf("%d saves still tracked after the game opted out", n)
	}
}
