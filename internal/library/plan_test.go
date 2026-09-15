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
		if f.RelDir != "windows/en" {
			t.Errorf("rel dir = %q, want windows/en", f.RelDir)
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
		// they can on GOG): every language gets its own folder, even where it is
		// the only one.
		if f.Kind == "installer" && f.OS == "windows" && f.RelDir != "windows/"+f.Language {
			t.Errorf("windows/%s installer rel dir = %q, want per-language folder", f.Language, f.RelDir)
		}
		if f.Kind == "installer" && f.OS == "mac" && f.RelDir != "mac/en" {
			t.Errorf("single-language mac installer rel dir = %q, want mac/en", f.RelDir)
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

	// DLC owned: planned in dlc/<folder>/windows/<language>.
	plan := Plan(game, &cp, gog.OwnedSet{cp.ID: true, dlcID: true}, settings([]string{"windows"}, []string{"en"}, true, true, false))
	if len(plan) != 2 || !plan[1].Product.IsDLC {
		t.Fatalf("expected base + dlc, got %d products", len(plan))
	}
	if plan[1].Files[0].RelDir != "dlc/Cyberpunk 2077 Phantom Liberty/windows/en" {
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

// Patches are a choice of their own: picked by platform and language like
// installers, stored next to the installer they update, and fetched whether
// or not the installers themselves are, since they update an installed game.
func TestPlanPatches(t *testing.T) {
	game, p, owned := witcher()
	s := settings([]string{"windows"}, []string{"en"}, true, true, false)
	if got := countKind(Plan(game, p, owned, s)[0].Files, db.KindPatch, "", ""); got != 0 {
		t.Fatalf("patches planned although switched off: %d", got)
	}
	s.IncludePatches = true
	files := Plan(game, p, owned, s)[0].Files
	if got := countKind(files, db.KindPatch, "windows", "en"); got != 2 {
		t.Errorf("windows/en patch parts = %d, want 2", got)
	}
	if got := countKind(files, db.KindPatch, "windows", "de"); got != 0 {
		t.Errorf("german patch planned although only english is chosen: %d files", got)
	}
	if got := countKind(files, db.KindPatch, "mac", ""); got != 0 {
		t.Errorf("mac patch planned although only windows is chosen: %d files", got)
	}
	if got := countKind(files, db.KindInstaller, "windows", "en"); got != 3 {
		t.Errorf("installers = %d, want the 3 parts next to the patches", got)
	}
	for _, f := range files {
		if f.Kind != db.KindPatch {
			continue
		}
		if f.RelDir != "windows/en/patches" {
			t.Errorf("patch rel dir = %q, want windows/en/patches", f.RelDir)
		}
		if f.Version != "1.5 (A)" || f.Name == "" || f.Downlink == "" || f.GogID == "" || f.Size == 0 {
			t.Errorf("incomplete patch: %+v", f)
		}
	}
	// Without the base game's installers the patches are still fetched.
	s.IncludeInstallers = false
	files = Plan(game, p, owned, s)[0].Files
	if countKind(files, db.KindInstaller, "", "") != 0 || countKind(files, db.KindPatch, "windows", "en") != 2 {
		t.Errorf("patches without installers: %+v", files)
	}
	// The language fallback applies to patches as it does to installers.
	s = settings([]string{"windows"}, []string{"fr"}, true, true, false)
	s.IncludePatches = true
	if got := countKind(Plan(game, p, owned, s)[0].Files, db.KindPatch, "windows", "en"); got != 2 {
		t.Errorf("fallback should pick the english patch, got %d files", got)
	}
	s.LanguageFallback = false
	if got := countKind(Plan(game, p, owned, s)[0].Files, db.KindPatch, "", ""); got != 0 {
		t.Errorf("without fallback no patch should be planned, got %d files", got)
	}
	// The game opts in on its own.
	s = settings([]string{"windows"}, []string{"en"}, true, true, false)
	game.Options.Patches = true
	if got := countKind(Plan(game, p, owned, s)[0].Files, db.KindPatch, "windows", "en"); got != 2 {
		t.Errorf("an opted-in game should get its patches, got %d files", got)
	}
}

// A DLC's patches come with the DLC, under its folder, when patches are on;
// several patches of one product never share a file id.
func TestPlanDLCPatches(t *testing.T) {
	var cp gog.Product
	for _, g := range gog.SampleLibrary() {
		if g.Listed.ID == 1207666633 {
			cp = g.Product
		}
	}
	game := db.Game{ID: cp.ID, Title: cp.Title, Folder: "Cyberpunk 2077"}
	owned := gog.OwnedSet{cp.ID: true, cp.ExpandedDLCs[0].ID: true}
	s := settings([]string{"windows"}, []string{"en"}, true, true, false)
	s.IncludePatches = true
	plan := Plan(game, &cp, owned, s)
	if len(plan) != 2 {
		t.Fatalf("expected base + dlc, got %d products", len(plan))
	}
	// Two patches in a row for the base game, two parts each.
	if got := countKind(plan[0].Files, db.KindPatch, "windows", "en"); got != 4 {
		t.Errorf("base game patch parts = %d, want 4", got)
	}
	ids := map[string]bool{}
	for _, f := range plan[0].Files {
		key := f.Kind + "/" + f.GogID
		if ids[key] {
			t.Errorf("two files share the id %s", key)
		}
		ids[key] = true
	}
	if got := countKind(plan[1].Files, db.KindPatch, "windows", "en"); got != 1 {
		t.Errorf("dlc patch parts = %d, want 1", got)
	}
	for _, f := range plan[1].Files {
		if f.Kind == db.KindPatch && f.RelDir != "dlc/Cyberpunk 2077 Phantom Liberty/windows/en/patches" {
			t.Errorf("dlc patch rel dir = %q", f.RelDir)
		}
	}
	// With DLC off its patches go with it; the base game keeps its own.
	s.IncludeDLC = false
	plan = Plan(game, &cp, owned, s)
	if len(plan) != 1 || countKind(plan[0].Files, db.KindPatch, "windows", "en") != 4 {
		t.Errorf("dlc disabled: %d products, %d base game patch parts", len(plan), countKind(plan[0].Files, db.KindPatch, "", ""))
	}
}
