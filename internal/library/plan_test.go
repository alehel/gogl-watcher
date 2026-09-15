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

// The base game's installers are a choice of their own: without them the plan
// still holds the DLC's installers and the extras the settings ask for.
func TestPlanWithoutBaseGameInstallers(t *testing.T) {
	game, p, owned := witcher()
	p.ExpandedDLCs = []gog.Product{gog.SampleLibrary()[3].Product.ExpandedDLCs[0]}
	owned[p.ExpandedDLCs[0].ID] = true
	s := settings([]string{"windows"}, []string{"en"}, true, true, true)
	s.IncludeInstallers = false
	plan := Plan(game, p, owned, s)
	if len(plan) != 2 {
		t.Fatalf("expected the base product and one DLC, got %d", len(plan))
	}
	if got := countKind(plan[0].Files, "installer", "", ""); got != 0 {
		t.Errorf("base game installers planned although switched off: %d", got)
	}
	if got := countKind(plan[0].Files, "extra", "", ""); got == 0 {
		t.Error("extras should still be planned")
	}
	if got := countKind(plan[1].Files, "installer", "windows", "en"); got == 0 {
		t.Error("the DLC's installers should still be planned")
	}
	// The game itself can opt back in.
	game.Options.Installers = true
	if got := countKind(Plan(game, p, owned, s)[0].Files, "installer", "windows", "en"); got != 3 {
		t.Errorf("an opted-in game should get its installers, got %d", got)
	}
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
		// English and German installers share file names in the sample library (as
		// they can on GOG): with several languages each gets its own folder.
		if f.Kind == "installer" && f.OS == "windows" && f.RelDir != "windows/"+f.Language {
			t.Errorf("windows/%s installer rel dir = %q, want per-language folder", f.Language, f.RelDir)
		}
		if f.Kind == "installer" && f.OS == "mac" && f.RelDir != "mac" {
			t.Errorf("single-language mac installer rel dir = %q, want mac", f.RelDir)
		}
	}
	paths := map[string]string{}
	for _, f := range files {
		key := f.RelDir + "/" + f.GogID
		if other, dup := paths[key]; dup {
			t.Errorf("two files plan the same location %s: %s and %s", key, other, f.Language)
		}
		paths[key] = f.Language
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

// A language chosen after another was downloaded must not be planned into the
// flat folder the other language's files still occupy: localized installers
// often share their file names, and the copies kept from before would be
// written over. Languages chosen together are kept apart by the plan itself.
func TestKeepLanguagesApart(t *testing.T) {
	game := db.Game{ID: 1, Folder: "Game"}
	installer := func(product int64, os, lang, local, prev string) db.File {
		return db.File{ProductID: product, Kind: db.KindInstaller, OS: os, Language: lang, LocalPath: local, PreviousPath: prev}
	}
	planned := func(relDir string) []PlannedProduct {
		return []PlannedProduct{{Product: db.Product{ID: 1}, Files: []db.File{
			{ProductID: 1, Kind: db.KindInstaller, OS: "windows", Language: "de", GogID: "de0", RelDir: relDir},
			{ProductID: 1, Kind: db.KindInstaller, OS: "windows", Language: "de", GogID: "de1", RelDir: relDir},
		}}}
	}
	cases := []struct {
		name     string
		existing []db.File
		want     string
	}{
		{"nothing there yet", nil, "windows"},
		{"another language kept in the flat folder", []db.File{installer(1, "windows", "en", "Game/windows/setup.exe", "")}, "windows/de"},
		{"another language waiting there to be replaced", []db.File{installer(1, "windows", "en", "Game/windows/en/setup.exe", "Game/windows/setup.exe")}, "windows/de"},
		{"another language in its own subfolder", []db.File{installer(1, "windows", "en", "Game/windows/en/setup.exe", "")}, "windows"},
		{"the same language in the flat folder", []db.File{installer(1, "windows", "de", "Game/windows/setup-2.bin", "")}, "windows"},
		{"a part of this installer already in the subfolder", []db.File{installer(1, "windows", "de", "Game/windows/de/setup-1.bin", "")}, "windows/de"},
		{"another platform", []db.File{installer(1, "linux", "en", "Game/linux/setup.sh", "")}, "windows"},
		{"another product", []db.File{installer(2, "windows", "en", "Game/windows/setup.exe", "")}, "windows"},
		{"a row not downloaded", []db.File{installer(1, "windows", "en", "", "")}, "windows"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := planned("windows")
			keepLanguagesApart(game, p, c.existing)
			for _, f := range p[0].Files {
				if f.RelDir != c.want {
					t.Errorf("%s planned into %q, want %q", f.GogID, f.RelDir, c.want)
				}
			}
		})
	}
	// Languages chosen together are in subfolders already and stay there.
	p := planned("windows/de")
	keepLanguagesApart(game, p, []db.File{installer(1, "windows", "en", "Game/windows/setup.exe", "")})
	if got := p[0].Files[0].RelDir; got != "windows/de" {
		t.Errorf("a subfolder of its own was changed to %q", got)
	}
	// A DLC's installers live in the DLC's folder, with the same rule.
	dlc := []PlannedProduct{{Product: db.Product{ID: 2, Folder: "Expansion", IsDLC: true}, Files: []db.File{
		{ProductID: 2, Kind: db.KindInstaller, OS: "windows", Language: "de", GogID: "x", RelDir: "dlc/Expansion/windows"},
	}}}
	keepLanguagesApart(game, dlc, []db.File{installer(2, "windows", "en", "Game/dlc/Expansion/windows/setup.exe", "")})
	if got := dlc[0].Files[0].RelDir; got != "dlc/Expansion/windows/de" {
		t.Errorf("DLC installer planned into %q, want its language subfolder", got)
	}
	// Extras carry no language and are left alone.
	extras := []PlannedProduct{{Product: db.Product{ID: 1}, Files: []db.File{
		{ProductID: 1, Kind: db.KindExtra, GogID: "e", RelDir: "extras"},
	}}}
	keepLanguagesApart(game, extras, []db.File{installer(1, "windows", "en", "Game/windows/setup.exe", "")})
	if got := extras[0].Files[0].RelDir; got != "extras" {
		t.Errorf("extra moved to %q", got)
	}
}
