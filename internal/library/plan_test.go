package library

import (
	"testing"

	"github.com/alehel/gogl-watcher/internal/db"
	"github.com/alehel/gogl-watcher/internal/gog"
)

func settings(platforms []string, langs []string, fallback, dlc, extras bool) db.Settings {
	s := db.DefaultSettings()
	s.Platforms = platforms
	s.Languages = langs
	s.LanguageFallback = fallback
	s.IncludeDLC = dlc
	s.IncludeExtras = extras
	return s
}

func witcher() (db.Game, *gog.Product, gog.OwnedSet) {
	lib := gog.SampleLibrary()
	for _, g := range lib {
		if g.Listed.ID == 1207658924 {
			p := g.Product
			owned := gog.OwnedSet{p.ID: true}
			return db.Game{ID: g.Listed.ID, Title: g.Listed.Title, Folder: "The Witcher"}, &p, owned
		}
	}
	panic("sample game missing")
}

func countKind(files []db.File, kind, os, lang string) int {
	n := 0
	for _, f := range files {
		if f.Kind == kind && (os == "" || f.OS == os) && (lang == "" || f.Language == lang) {
			n++
		}
	}
	return n
}

func TestPlanSelectsPlatformsAndLanguages(t *testing.T) {
	game, p, owned := witcher()
	plan := Plan(game, p, owned, settings([]string{"windows"}, []string{"en"}, true, true, false))
	if len(plan) != 1 {
		t.Fatalf("expected only the base product, got %d", len(plan))
	}
	files := plan[0].Files
	if got := countKind(files, "installer", "windows", "en"); got != 3 {
		t.Errorf("windows/en installer parts = %d, want 3", got)
	}
	if got := countKind(files, "installer", "windows", "de"); got != 0 {
		t.Errorf("german installer should not be planned, got %d files", got)
	}
	if got := countKind(files, "installer", "mac", ""); got != 0 {
		t.Errorf("mac installer should not be planned, got %d files", got)
	}
	if got := countKind(files, "extra", "", ""); got != 0 {
		t.Errorf("extras should be off, got %d", got)
	}
	for _, f := range files {
		if f.RelDir != "windows" {
			t.Errorf("rel dir = %q, want windows", f.RelDir)
		}
		if f.Downlink == "" || f.GogID == "" || f.Size == 0 {
			t.Errorf("incomplete file: %+v", f)
		}
	}
}

func TestPlanMultipleLanguagesAndExtras(t *testing.T) {
	game, p, owned := witcher()
	plan := Plan(game, p, owned, settings([]string{"windows", "mac"}, []string{"en", "de"}, true, true, true))
	files := plan[0].Files
	if got := countKind(files, "installer", "windows", ""); got != 5 {
		t.Errorf("windows installers (en+de) = %d, want 5", got)
	}
	if got := countKind(files, "installer", "mac", "en"); got != 1 {
		t.Errorf("mac installers = %d, want 1", got)
	}
	if got := countKind(files, "extra", "", ""); got != 2 {
		t.Errorf("extras = %d, want 2", got)
	}
	for _, f := range files {
		if f.Kind == "extra" && f.RelDir != "extras" {
			t.Errorf("extra rel dir = %q", f.RelDir)
		}
	}
}

func TestPlanLanguageFallback(t *testing.T) {
	game, p, owned := witcher()
	// Only French requested; the Witcher sample has en+de for windows.
	withFallback := Plan(game, p, owned, settings([]string{"windows"}, []string{"fr"}, true, false, false))
	if got := countKind(withFallback[0].Files, "installer", "windows", "en"); got != 3 {
		t.Errorf("fallback should pick english, got %d en files", got)
	}
	noFallback := Plan(game, p, owned, settings([]string{"windows"}, []string{"fr"}, false, false, false))
	if got := len(noFallback[0].Files); got != 0 {
		t.Errorf("without fallback nothing should be planned, got %d", got)
	}
}

func TestPlanDLCOwnership(t *testing.T) {
	lib := gog.SampleLibrary()
	var cp gog.Product
	for _, g := range lib {
		if g.Listed.ID == 1207666633 {
			cp = g.Product
		}
	}
	game := db.Game{ID: cp.ID, Title: cp.Title, Folder: "Cyberpunk 2077"}
	dlcID := cp.ExpandedDLCs[0].ID

	// DLC owned: planned in dlc/<folder>/windows.
	plan := Plan(game, &cp, gog.OwnedSet{cp.ID: true, dlcID: true}, settings([]string{"windows"}, []string{"en"}, true, true, false))
	if len(plan) != 2 || !plan[1].Product.IsDLC {
		t.Fatalf("expected base + dlc, got %d products", len(plan))
	}
	if plan[1].Files[0].RelDir != "dlc/Cyberpunk 2077 Phantom Liberty/windows" {
		t.Errorf("dlc rel dir = %q", plan[1].Files[0].RelDir)
	}
	// DLC not owned: skipped.
	plan = Plan(game, &cp, gog.OwnedSet{cp.ID: true}, settings([]string{"windows"}, []string{"en"}, true, true, false))
	if len(plan) != 1 {
		t.Errorf("unowned dlc should be skipped, got %d products", len(plan))
	}
	// DLC disabled: skipped even if owned.
	plan = Plan(game, &cp, gog.OwnedSet{cp.ID: true, dlcID: true}, settings([]string{"windows"}, []string{"en"}, true, false, false))
	if len(plan) != 1 {
		t.Errorf("dlc disabled should be skipped, got %d products", len(plan))
	}
}

func TestSanitizeFolder(t *testing.T) {
	cases := map[string]string{
		"The Witcher: Enhanced Edition": "The Witcher Enhanced Edition",
		`Broken/Sword\ "DC"`:            "Broken Sword DC",
		"   trailing dots...":           "trailing dots",
		"":                              "untitled",
		"a\x00b":                        "a b",
	}
	for in, want := range cases {
		if got := SanitizeFolder(in); got != want {
			t.Errorf("SanitizeFolder(%q) = %q, want %q", in, got, want)
		}
	}
}
