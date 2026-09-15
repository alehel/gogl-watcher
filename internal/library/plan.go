package library

import (
	"cmp"
	"fmt"
	"path"
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

// keepLanguagesApart moves a planned installer into its language subfolder
// when the flat folder is not its own. Installers of different languages often
// share their file names, which is why several languages chosen together get
// a subfolder each (see planFiles); a language chosen after another was
// downloaded is planned flat, and its files would be written over the copies
// kept from before. So the rows the game already has (existing, active or
// not) decide: a file of another language in the flat folder, kept or waiting
// to be replaced, claims it, and a part of this same installer that already
// lives in the subfolder keeps the rest of it together.
func keepLanguagesApart(game db.Game, planned []PlannedProduct, existing []db.File) {
	for pi := range planned {
		pp := &planned[pi]
		for fi := range pp.Files {
			f := &pp.Files[fi]
			if f.Kind != db.KindInstaller || f.Language == "" {
				continue
			}
			flat := RelDir(db.KindInstaller, f.OS, pp.Product.Folder)
			if f.RelDir != flat {
				continue // already in a subfolder of its own
			}
			sub := filepath.ToSlash(filepath.Join(flat, SanitizeFolder(f.Language)))
			flatDir := LocalRelPath(game.Folder, flat, "")
			subDir := LocalRelPath(game.Folder, sub, "")
			for _, e := range existing {
				if e.ProductID != f.ProductID || e.Kind != db.KindInstaller || e.OS != f.OS {
					continue
				}
				taken := e.Language != f.Language && (path.Dir(e.LocalPath) == flatDir || path.Dir(e.PreviousPath) == flatDir)
				started := e.Language == f.Language && path.Dir(e.LocalPath) == subDir
				if taken || started {
					f.RelDir = sub
					break
				}
			}
		}
	}
}

// planFiles plans the files of one product: its installers when installers
// says so, and its extras when the settings do.
func planFiles(gameID, productID int64, dl gog.Downloads, dlcFolder string, s db.Settings, installers bool) []db.File {
	var files []db.File
	byOS := map[string][]gog.Installer{}
	for _, inst := range dl.Installers {
		os := db.NormalizePlatform(inst.OS)
		byOS[os] = append(byOS[os], inst)
	}
	if !installers {
		byOS = nil
	}
	for _, os := range s.Platforms {
		chosen := chooseLanguages(byOS[os], s)
		for _, inst := range chosen {
			lang := db.NormalizeLanguage(inst.Language)
			dir := RelDir("installer", os, dlcFolder)
			if len(chosen) > 1 {
				// Installers of different languages often share file names; give
				// each language its own folder so they cannot overwrite each other.
				// The parts of one installer stay together, which they must.
				dir = filepath.ToSlash(filepath.Join(dir, SanitizeFolder(lang)))
			}
			for i, f := range inst.Files {
				files = append(files, db.File{
					GameID: gameID, ProductID: productID, Kind: "installer", OS: os, Language: lang,
					GogID: fileID(string(f.ID), inst.ID, i), Name: inst.Name, Version: inst.Version, Size: int64(f.Size), Downlink: f.Downlink,
					RelDir: dir,
				})
			}
		}
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

// fileID is the stable id of the i-th file of an installer or extra: GOG's own
// when it has one, else derived from the parent's id.
func fileID(id, parent string, i int) string {
	if id == "" {
		return fmt.Sprintf("%s_%d", parent, i)
	}
	return id
}

// chooseLanguages picks the installers to download for one OS.
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
