package library

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

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
	var out []PlannedProduct
	base := PlannedProduct{Product: db.Product{ID: p.ID, GameID: game.ID, Title: firstNonEmpty(p.Title, game.Title), IsDLC: false}}
	base.Files = planFiles(game.ID, p.ID, p.Downloads, "", s)
	out = append(out, base)
	if !s.IncludeDLC {
		return out
	}
	dlcs := append([]gog.Product{}, p.ExpandedDLCs...)
	sort.Slice(dlcs, func(i, j int) bool { return dlcs[i].Title < dlcs[j].Title })
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
		pp.Files = planFiles(game.ID, d.ID, d.Downloads, folder, s)
		out = append(out, pp)
	}
	return out
}

func planFiles(gameID, productID int64, dl gog.Downloads, dlcFolder string, s db.Settings) []db.File {
	var files []db.File
	byOS := map[string][]gog.Installer{}
	for _, inst := range dl.Installers {
		os := normalizeOS(inst.OS)
		byOS[os] = append(byOS[os], inst)
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
				id := string(f.ID)
				if id == "" {
					id = fmt.Sprintf("%s_%d", inst.ID, i)
				}
				files = append(files, db.File{
					GameID: gameID, ProductID: productID, Kind: "installer", OS: os, Language: lang,
					GogID: id, Name: inst.Name, Version: inst.Version, Size: int64(f.Size), Downlink: f.Downlink,
					RelDir: dir,
				})
			}
		}
	}
	if s.IncludeExtras {
		for _, b := range dl.BonusContent {
			for i, f := range b.Files {
				id := string(f.ID)
				if id == "" {
					id = fmt.Sprintf("%s_%d", string(b.ID), i)
				}
				files = append(files, db.File{
					GameID: gameID, ProductID: productID, Kind: "extra", OS: "", Language: "",
					GogID: id, Name: b.Name, Version: "", Size: int64(f.Size), Downlink: f.Downlink,
					RelDir: RelDir("extra", "", dlcFolder),
				})
			}
		}
	}
	return files
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

func normalizeOS(os string) string {
	switch strings.ToLower(os) {
	case "windows", "win":
		return "windows"
	case "mac", "osx", "macos":
		return "mac"
	case "linux":
		return "linux"
	}
	return strings.ToLower(os)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// LocalRelPath returns the library-relative path for a file with a resolved name.
func LocalRelPath(gameFolder, relDir, filename string) string {
	return filepath.ToSlash(filepath.Join(gameFolder, relDir, filename))
}
