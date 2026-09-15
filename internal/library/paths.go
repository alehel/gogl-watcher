// Package library keeps the local copy of the GOG library in sync with the account.
package library

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	badChars   = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]+`)
	multiSpace = regexp.MustCompile(`\s+`)
)

// SanitizeFolder turns a title into a safe directory name.
func SanitizeFolder(title string) string {
	s := badChars.ReplaceAllString(title, " ")
	s = multiSpace.ReplaceAllString(s, " ")
	s = strings.Trim(s, " .")
	if len(s) > 120 {
		// Cut on a rune boundary so the name stays valid UTF-8.
		cut := 120
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = strings.Trim(s[:cut], " .")
	}
	if s == "" {
		return "untitled"
	}
	return s
}

// SanitizeFilename removes unsafe path characters without shortening the name.
// Installer part numbers and extensions must survive, and multipart installers
// may require the exact names of their companion files.
func SanitizeFilename(name string) string {
	s := strings.Trim(badChars.ReplaceAllString(name, " "), " .")
	if s == "" {
		return "untitled"
	}
	return s
}

// Paths resolves relative library paths to absolute ones.
type Paths struct {
	Root string
}

// Abs returns the absolute path for a library-relative path.
func (p Paths) Abs(rel string) string {
	if rel == "" {
		return ""
	}
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(p.Root, rel)
}

// Exists reports whether a library-relative (or absolute) path exists as a regular file.
func (p Paths) Exists(rel string) bool {
	if rel == "" {
		return false
	}
	st, err := os.Stat(p.Abs(rel))
	return err == nil && st.Mode().IsRegular()
}

// Remove deletes a library-relative file (and its .part) and prunes empty parent directories.
func (p Paths) Remove(rel string) error {
	if rel == "" {
		return nil
	}
	abs := p.Abs(rel)
	_ = os.Remove(abs + ".part")
	if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
		return err
	}
	p.pruneEmpty(filepath.Dir(abs))
	return nil
}

// RemovePart deletes the partial download of a library-relative file, if there is
// one, leaving the file itself alone.
func (p Paths) RemovePart(rel string) {
	if rel == "" {
		return
	}
	abs := p.Abs(rel)
	if err := os.Remove(abs + ".part"); err == nil {
		p.pruneEmpty(filepath.Dir(abs))
	}
}

func (p Paths) pruneEmpty(dir string) {
	root := filepath.Clean(p.Root)
	for dir != root && strings.HasPrefix(dir, root) {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// RelDir returns the directory (relative to the game folder) of a platform's
// installers, each language of which gets a folder of its own under it, or of
// the extras; cloud saves have SaveRelDir.
func RelDir(kind, os, dlcFolder string) string {
	sub := os
	if kind == "extra" {
		sub = "extras"
	}
	if dlcFolder != "" {
		return filepath.ToSlash(filepath.Join("dlc", dlcFolder, sub))
	}
	return sub
}
