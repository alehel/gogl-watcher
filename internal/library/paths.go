// Package library keeps the local copy of the GOG library in sync with the account.
package library

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
		s = strings.TrimSpace(s[:120])
	}
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

// RelDir returns the directory (relative to the game folder) for a file.
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
