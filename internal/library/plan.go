package library

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/alehel/gogl-watcher/internal/db"
	"github.com/alehel/gogl-watcher/internal/gog"
)

// PlannedProduct is a base game or DLC that will own files.
type PlannedProduct struct {
	Product db.Product
	Files   []db.File
}

// Plan computes which files of a product (and its owned DLC) should exist locally
// under the given settings. owned may be nil, in which case all DLC count as owned.
func Plan(game db.Game, p *gog.Product, owned gog.OwnedSet, s db.Settings) []PlannedProduct {
	s = s.ForGame(game)
	var out []PlannedProduct
	base := PlannedProduct{Product: db.Product{ID: p.ID, GameID: game.ID, Title: cmp.Or(p.Title, game.Title), IsDLC: false}}
	base.Files = planFiles(game.ID, p.ID, p.Downloads, "", s, s.IncludeInstallers)
	out = append(out, base)
	if !s.IncludeDLC {
		return out
	}
	dlcs := slices.Clone(p.ExpandedDLCs)
	slices.SortFunc(dlcs, func(a, b gog.Product) int { return cmp.Compare(a.Title, b.Title) })
	usedFolders := map[string]bool{}
	for _, d := range dlcs {
		if d.ID == 0 || (owned != nil && !owned[d.ID]) {
			continue
		}
		folder := SanitizeFolder(d.Title)
		if usedFolders[folder] {
			folder = fmt.Sprintf("%s (%d)", folder, d.ID)
		}
		usedFolders[folder] = true
		pp := PlannedProduct{Product: db.Product{ID: d.ID, GameID: game.ID, Title: d.Title, IsDLC: true, Folder: folder}}
		// The DLC setting covers the DLC's installers; the base game's own
		// installers are a choice of their own.
		pp.Files = planFiles(game.ID, d.ID, d.Downloads, folder, s, true)
		out = append(out, pp)
	}
	return out
}

// planFiles plans the files of one product: its installers when installers
// says so, and its patches and extras when the settings do.
func planFiles(gameID, productID int64, dl gog.Downloads, dlcFolder string, s db.Settings, installers bool) []db.File {
	var files []db.File
	// The installers are picked even where they are not downloaded: the
	// patches follow their languages.
	listed := byPlatform(dl.Installers)
	chosen := map[string][]gog.Installer{}
	for _, os := range s.Platforms {
		chosen[os] = chooseLanguages(listed[os], s)
	}
	if installers {
		files = append(files, installerFiles(gameID, productID, db.KindInstaller, chosen, dlcFolder, s)...)
	}
	if s.IncludePatches {
		files = append(files, installerFiles(gameID, productID, db.KindPatch, choosePatches(dl.Patches, listed, chosen, s), dlcFolder, s)...)
	}
	if s.IncludeExtras {
		for _, b := range dl.BonusContent {
			for i, f := range b.Files {
				files = append(files, db.File{
					GameID: gameID, ProductID: productID, Kind: "extra", OS: "", Language: "",
					GogID: fileID(string(f.ID), string(b.ID), i), Name: b.Name, Version: "", Size: int64(f.Size), Downlink: f.Downlink,
					RelDir: RelDir("extra", "", dlcFolder),
				})
			}
		}
	}
	return files
}

// byPlatform groups installers (or patches) by platform.
func byPlatform(list []gog.Installer) map[string][]gog.Installer {
	out := map[string][]gog.Installer{}
	for _, inst := range list {
		os := db.NormalizePlatform(inst.OS)
		out[os] = append(out[os], inst)
	}
	return out
}

// choosePatches picks, per chosen platform, the patches to download. A patch
// is only of use for an installer of the same language, so a platform's
// patches come in the languages its installer is picked in (chosen), whether
// or not the installers themselves are downloaded: the settings' languages
// and their fallback decide once, for the installer, and the patch follows.
// Where GOG lists no installer at all for the platform (listed), there is
// nothing to follow, and the patches are picked like installers.
func choosePatches(patches []gog.Installer, listed, chosen map[string][]gog.Installer, s db.Settings) map[string][]gog.Installer {
	byOS := byPlatform(patches)
	out := map[string][]gog.Installer{}
	for _, os := range s.Platforms {
		if len(listed[os]) == 0 {
			out[os] = chooseLanguages(byOS[os], s)
			continue
		}
		langs := map[string]bool{}
		for _, inst := range chosen[os] {
			langs[db.NormalizeLanguage(inst.Language)] = true
		}
		for _, p := range byOS[os] {
			if langs[db.NormalizeLanguage(p.Language)] {
				out[os] = append(out[os], p)
			}
		}
	}
	return out
}

// installerFiles is the files of the chosen installers (or patches) of one
// product, platform by platform in the order the settings list them.
func installerFiles(gameID, productID int64, kind string, chosen map[string][]gog.Installer, dlcFolder string, s db.Settings) []db.File {
	var files []db.File
	for _, os := range s.Platforms {
		for _, inst := range chosen[os] {
			lang := db.NormalizeLanguage(inst.Language)
			dir := installerDir(kind, os, lang, dlcFolder)
			for i, f := range inst.Files {
				files = append(files, db.File{
					GameID: gameID, ProductID: productID, Kind: kind, OS: os, Language: lang,
					GogID: fileID(string(f.ID), inst.ID, i), Name: inst.Name, Version: inst.Version, Size: int64(f.Size), Downlink: f.Downlink,
					RelDir: dir,
				})
			}
		}
	}
	return files
}

// installerDir is the folder, relative to the game folder, of an installer or
// a patch in one language. Installers of different languages often share their
// file names, so every installer goes into its language's folder, whether or
// not another language is chosen today: one chosen later, or kept from before,
// can then never be written over. The parts of one installer stay together,
// which they must. Patches go into a folder of their own under the language
// folder, next to the installer they update: the installer's folder stays what
// it was, and the patches for it are found in one place.
func installerDir(kind, os, lang, dlcFolder string) string {
	dir := filepath.Join(RelDir("installer", os, dlcFolder), languageFolder(lang))
	if kind == db.KindPatch {
		dir = filepath.Join(dir, PatchesDir)
	}
	return filepath.ToSlash(dir)
}

// languageFolder is the folder an installer's language gets under the
// platform folder: GOG's code as normalized here ("en", "de", "esmx"), or a
// fixed name for the rare installer GOG lists without one.
func languageFolder(lang string) string {
	if lang == "" {
		return "unknown"
	}
	return SanitizeFolder(lang)
}

// fileID is the stable id of the i-th file of an installer or extra: GOG's own
// when it has one, else derived from the parent's id.
func fileID(id, parent string, i int) string {
	if id == "" {
		return fmt.Sprintf("%s_%d", parent, i)
	}
	return id
}

// chooseLanguages picks the installers (or patches) to download for one OS.
func chooseLanguages(installers []gog.Installer, s db.Settings) []gog.Installer {
	var chosen []gog.Installer
	for _, inst := range installers {
		if s.WantsLanguage(db.NormalizeLanguage(inst.Language)) {
			chosen = append(chosen, inst)
		}
	}
	if len(chosen) > 0 || !s.LanguageFallback || len(installers) == 0 {
		return chosen
	}
	for _, inst := range installers {
		if db.NormalizeLanguage(inst.Language) == "en" {
			return []gog.Installer{inst}
		}
	}
	return []gog.Installer{installers[0]}
}

// LocalRelPath returns the library-relative path for a file with a resolved name.
func LocalRelPath(gameFolder, relDir, filename string) string {
	return filepath.ToSlash(filepath.Join(gameFolder, relDir, filename))
}
