package library

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/alehel/gogl-watcher/internal/db"
	"github.com/alehel/gogl-watcher/internal/gog"
)

// The offer of an unselected game shows everything GOG has and marks what the
// settings would pick, without planning any of it.
func TestOfferMarksWantedWithoutPlanning(t *testing.T) {
	m, _ := gog.NewMock(context.Background(), nil)
	_, _ = m.ExchangeCode(context.Background(), "code")
	d, syncer, _ := newSyncTest(t, m)
	s := saveSettings(t, d, "windows")
	s.DownloadMode = db.DownloadSelected
	s.IncludeExtras = false
	_ = d.SaveSettings(context.Background(), s)
	if err := syncer.SyncAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	games, _ := d.ListGames(context.Background())
	var game db.Game
	for _, g := range games {
		if g.WorksWindows && g.WorksLinux {
			game = g
			break
		}
	}
	if game.ID == 0 {
		t.Fatal("mock has no game on both windows and linux")
	}

	offer, err := syncer.Offer(context.Background(), game.ID)
	if err != nil {
		t.Fatal(err)
	}
	var winWanted, linuxWanted, extras, extrasWanted int
	for _, it := range offer.Products[0].Items {
		switch {
		case it.Kind == "extra":
			extras++
			if it.Wanted {
				extrasWanted++
			}
		case it.OS == "windows" && it.Wanted:
			winWanted++
		case it.OS == "linux" && it.Wanted:
			linuxWanted++
		}
	}
	if winWanted == 0 {
		t.Error("windows installers should be wanted with windows selected")
	}
	if linuxWanted != 0 {
		t.Error("linux installers must not be wanted with only windows selected")
	}
	if extras > 0 && extrasWanted != 0 {
		t.Error("extras must not be wanted while extras are off")
	}
	if offer.WantedFiles == 0 || offer.WantedBytes == 0 {
		t.Errorf("wanted totals empty: %+v", offer)
	}
	files, _ := d.ListFilesByGame(context.Background(), game.ID)
	if len(files) != 0 {
		t.Errorf("an offer must not plan files, found %d", len(files))
	}

	// Changing the settings invalidates the cached offer.
	s.Platforms = []string{"linux"}
	_ = d.SaveSettings(context.Background(), s)
	offer, err = syncer.Offer(context.Background(), game.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range offer.Products[0].Items {
		if it.Kind == "installer" && it.OS == "windows" && it.Wanted {
			t.Fatal("offer served from cache after the settings changed")
		}
	}
}

// Removing a language must be previewed (and confirmed) like removing a
// platform, also with the language fallback on: the fallback only keeps an
// installer while none of the selected languages exists for it. Where one does,
// the next sync drops the other language, so the user has to be asked first.
func TestPreviewReportsLanguageDroppedDespiteFallback(t *testing.T) {
	m, _ := gog.NewMock(context.Background(), nil)
	_, _ = m.ExchangeCode(context.Background(), "code")
	d, syncer, paths := newSyncTest(t, m)
	ctx := context.Background()
	s := saveSettings(t, d, "windows")
	s.Languages = []string{"en", "de"}
	s.LanguageFallback = true
	_ = d.SaveSettings(ctx, s)
	if err := syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	const witcher, pentiment = 1207658924, 1207669193 // en and de installers; de only
	byLang := func(game int64) map[string]int {
		files, _ := d.ListActiveFilesByGame(ctx, game)
		out := map[string]int{}
		for _, f := range files {
			out[f.Language]++
		}
		return out
	}
	if l := byLang(witcher); l["en"] == 0 || l["de"] == 0 {
		t.Fatalf("both languages should be tracked: %v", l)
	}
	if l := byLang(pentiment); l["de"] == 0 {
		t.Fatalf("the fallback should keep the german-only game: %v", l)
	}
	// One german installer is downloaded, so dropping it is a question.
	files, _ := d.ListActiveFilesByGame(ctx, witcher)
	for _, f := range files {
		if f.Language == "de" {
			rel := "Witcher/windows/de/setup.exe"
			_ = os.MkdirAll(filepath.Dir(paths.Abs(rel)), 0o755)
			_ = os.WriteFile(paths.Abs(rel), []byte("x"), 0o644)
			_ = d.SetFileDone(ctx, f.ID, rel, 1)
			break
		}
	}

	ns := s
	ns.Languages = []string{"en"}
	p, err := syncer.PreviewSettings(ctx, ns)
	if err != nil {
		t.Fatal(err)
	}
	if want := byLang(witcher)["de"]; p.Removed.Files != want || p.Removed.DownloadedFiles != 1 {
		t.Errorf("preview should list the %d german installers (1 downloaded), got %+v", want, p.Removed)
	}
	if !p.NeedsConfirmation || !slices.Contains(p.Reasons, "language:de") {
		t.Errorf("preview = %+v, want confirmation for language:de", p)
	}
	if err := syncer.ApplySettings(ctx, ns, ""); err == nil {
		t.Fatal("applying without an answer must be refused")
	}
	if err := syncer.ApplySettings(ctx, ns, RemovalKeep); err != nil {
		t.Fatal(err)
	}
	if l := byLang(witcher); l["de"] != 0 || l["en"] == 0 {
		t.Errorf("after the change: %v", l)
	}
	if l := byLang(pentiment); l["de"] == 0 {
		t.Errorf("the fallback must keep the german-only game: %v", l)
	}
	// The sync agrees with the preview: nothing else to drop, nothing to re-add.
	if err := syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	if l := byLang(witcher); l["de"] != 0 {
		t.Errorf("sync re-added german: %v", l)
	}
	if l := byLang(pentiment); l["de"] == 0 {
		t.Errorf("sync dropped the german-only game: %v", l)
	}
}

// The offer lists patches after the installers and marks them wanted only
// when the settings ask for them.
func TestOfferListsPatches(t *testing.T) {
	m, _ := gog.NewMock(context.Background(), nil)
	_, _ = m.ExchangeCode(context.Background(), "code")
	d, syncer, _ := newSyncTest(t, m)
	ctx := context.Background()
	s := saveSettings(t, d, "windows")
	s.DownloadMode = db.DownloadSelected
	_ = d.SaveSettings(ctx, s)
	if err := syncer.SyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	const witcher = 1207658924
	items := func() []OfferItem {
		offer, err := syncer.Offer(ctx, witcher)
		if err != nil {
			t.Fatal(err)
		}
		return offer.Products[0].Items
	}
	all := items()
	var patches []OfferItem
	last := -1
	for _, it := range all {
		if kindOrder[it.Kind] < last {
			t.Fatalf("items out of order (installers, patches, extras): %+v", all)
		}
		last = kindOrder[it.Kind]
		if it.Kind == db.KindPatch {
			patches = append(patches, it)
		}
	}
	if len(patches) != 3 {
		t.Fatalf("want the sample's 3 patches listed, got %+v", patches)
	}
	for _, it := range patches {
		if it.Wanted {
			t.Errorf("patch wanted while patches are off: %+v", it)
		}
		if it.Version == "" || it.Name == "" || it.Files == 0 || it.Size == 0 || it.OS == "" || it.Language == "" {
			t.Errorf("incomplete patch item: %+v", it)
		}
	}
	s.IncludePatches = true
	_ = d.SaveSettings(ctx, s)
	wanted := 0
	for _, it := range items() {
		if it.Kind == db.KindPatch && it.Wanted {
			wanted++
			if it.OS != "windows" || it.Language != "en" {
				t.Errorf("wrong patch wanted: %+v", it)
			}
		}
	}
	if wanted != 1 {
		t.Errorf("want exactly the windows/en patch wanted, got %d", wanted)
	}
}
