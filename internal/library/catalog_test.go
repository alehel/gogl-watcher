package library

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/alehel/gogl-watcher/internal/db"
	"github.com/alehel/gogl-watcher/internal/gog"
)

func authedMock(t *testing.T) *gog.Mock {
	t.Helper()
	m, err := gog.NewMock(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ExchangeCode(context.Background(), "code"); err != nil {
		t.Fatal(err)
	}
	return m
}

func estimateOf(t *testing.T, d *db.DB, s db.Settings) (int, int64) {
	t.Helper()
	items, err := d.ListCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return Estimate(items, s)
}

// The whole point of estimating from the catalog is that the answer matches the
// sync it predicts. With the same settings, the estimate must equal what a sync
// actually wrote down, file for file and byte for byte.
func TestEstimateMatchesWhatTheSyncStores(t *testing.T) {
	ctx := context.Background()
	d, syncer, _ := newSyncTest(t, authedMock(t))
	settings := saveSettings(t, d, "windows", "mac")
	settings.IncludeExtras = true
	if err := d.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if err := syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}

	stored, err := d.ListActiveFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var storedBytes int64
	for _, f := range stored {
		storedBytes += f.Size
	}
	files, bytes := estimateOf(t, d, settings)
	if files != len(stored) || bytes != storedBytes {
		t.Errorf("estimate %d files / %d bytes, sync stored %d files / %d bytes", files, bytes, len(stored), storedBytes)
	}
	if files == 0 {
		t.Fatal("the sample library planned no files at all")
	}
}

// The catalog exists to answer what the settings did not ask for. After a
// Windows-only sync, the cost of adding another platform, extras or DLC must
// still be known without going back to GOG.
func TestEstimateCostsCombinationsTheSyncNeverWanted(t *testing.T) {
	ctx := context.Background()
	d, syncer, _ := newSyncTest(t, authedMock(t))
	windows := saveSettings(t, d, "windows")
	if err := syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}

	_, base := estimateOf(t, d, windows)
	if base == 0 {
		t.Fatal("no bytes estimated for the settings that were synced")
	}

	withMac := windows
	withMac.Platforms = []string{"windows", "mac"}
	if _, got := estimateOf(t, d, withMac); got <= base {
		t.Errorf("adding a platform did not cost anything: %d vs %d", got, base)
	}

	macOnly := windows
	macOnly.Platforms = []string{"mac"}
	if _, got := estimateOf(t, d, macOnly); got == 0 {
		t.Error("a platform the sync never wanted estimates as free")
	}

	withExtras := windows
	withExtras.IncludeExtras = true
	if _, got := estimateOf(t, d, withExtras); got <= base {
		t.Errorf("including extras did not cost anything: %d vs %d", got, base)
	}

	noDLC := windows
	noDLC.IncludeDLC = false
	if _, got := estimateOf(t, d, noDLC); got >= base {
		t.Errorf("dropping DLC did not save anything: %d vs %d", got, base)
	}

	german := windows
	german.Languages = []string{"de"}
	german.LanguageFallback = false
	if _, got := estimateOf(t, d, german); got == 0 || got >= base {
		t.Errorf("a language the sync never wanted estimates as %d, base %d", got, base)
	}
}

// In the "selected" download mode the sync never fetches the games nobody
// ticked, so the scanner is the only thing that can cost a full backup.
func TestScannerCatalogsGamesTheSyncSkips(t *testing.T) {
	ctx := context.Background()
	m := authedMock(t)
	d, syncer, _ := newSyncTest(t, m)
	settings := saveSettings(t, d, "windows")
	settings.DownloadMode = db.DownloadSelected
	if err := d.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if err := syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	if _, bytes := estimateOf(t, d, settings); bytes != 0 {
		t.Fatalf("nothing is selected, so nothing should be catalogued yet, got %d bytes", bytes)
	}

	scanner := NewScanner(d, m, syncer, slog.Default())
	for i := 0; i < 3; i++ {
		scanned, err := scanner.scanOne(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !scanned {
			t.Fatalf("scan %d found no game to scan", i)
		}
	}
	scanned, total, err := d.CatalogCoverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if scanned != 3 {
		t.Errorf("scanned %d games, want 3", scanned)
	}
	if total <= scanned {
		t.Fatalf("the sample library should hold more than %d games", scanned)
	}
	if _, bytes := estimateOf(t, d, settings); bytes == 0 {
		t.Error("scanned games contribute nothing to the estimate")
	}
}

// A scanned game must not be scanned again until its catalog goes stale, or a
// large library would never get past its first few games.
func TestScannerTakesEachGameOnce(t *testing.T) {
	ctx := context.Background()
	m := authedMock(t)
	d, syncer, _ := newSyncTest(t, m)
	saveSettings(t, d, "windows")
	if err := syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	// The sync catalogued every game on its way past, so there is nothing left.
	scanner := NewScanner(d, m, syncer, slog.Default())
	scanned, err := scanner.scanOne(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if scanned {
		t.Error("scanned a game the sync had already catalogued")
	}
	covered, total, err := d.CatalogCoverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if covered != total || total == 0 {
		t.Errorf("sync covered %d of %d games", covered, total)
	}
	// Once the entries are old enough, the next game is offered again.
	scanner.staleAfter = 0
	if scanned, err := scanner.scanOne(ctx); err != nil || !scanned {
		t.Errorf("stale catalog not rescanned: scanned=%v err=%v", scanned, err)
	}
}

// Re-scanning a game replaces its catalog instead of adding to it.
func TestScanCatalogReplacesPreviousRows(t *testing.T) {
	ctx := context.Background()
	m := authedMock(t)
	d, syncer, _ := newSyncTest(t, m)
	settings := saveSettings(t, d, "windows")
	if err := syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := d.ListCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	games, err := d.ListGames(ctx)
	if err != nil || len(games) == 0 {
		t.Fatal("no games", err)
	}
	owned, err := m.OwnedIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := syncer.ScanCatalog(ctx, games[0], owned); err != nil {
		t.Fatal(err)
	}
	after, err := d.ListCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("catalog grew from %d to %d rows on a re-scan", len(before), len(after))
	}
	if _, bytes := estimateOf(t, d, settings); bytes == 0 {
		t.Error("estimate empty after a re-scan")
	}
}

// The scan stands down while a sync is running: the sync fetches the same
// details much faster and catalogues them on the way past.
func TestScannerWaitsForASync(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := authedMock(t)
	api := gatedAPI{Mock: m, gate: make(chan struct{})}
	d, syncer, _ := newSyncTest(t, api)
	saveSettings(t, d, "windows")

	go func() { _ = syncer.SyncAll(ctx) }()
	waitFor(t, 5*time.Second, func() bool { return syncer.Status().Running })
	scanner := NewScanner(d, api, syncer, slog.Default())
	if scanner.ready(ctx) {
		t.Error("scanner ready while a sync is running")
	}
	close(api.gate)
	waitFor(t, 10*time.Second, func() bool { return !syncer.Status().Running })
	if !scanner.ready(ctx) {
		t.Error("scanner still standing down after the sync finished")
	}
}
