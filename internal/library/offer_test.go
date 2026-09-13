package library

import (
	"context"
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
