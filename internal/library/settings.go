package library

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/alehel/gogl-watcher/internal/db"
)

// ReasonUnselected is the removal reason for files of a game that is not selected
// while only selected games are downloaded.
const ReasonUnselected = "unselected"

// RemovalPreview describes tracked files a settings change would drop.
type RemovalPreview struct {
	NeedsConfirmation bool `json:"needs_confirmation"`
	Removed           struct {
		Files           int   `json:"files"`
		Bytes           int64 `json:"bytes"`
		DownloadedFiles int   `json:"downloaded_files"`
		DownloadedBytes int64 `json:"downloaded_bytes"`
	} `json:"removed"`
	Reasons []string `json:"reasons"`
}

// unwantedReason returns why f would no longer be wanted under s, or "".
func unwantedReason(f db.File, game db.Game, isDLC bool, s db.Settings) string {
	if !s.WantsGame(game) {
		return ReasonUnselected
	}
	if isDLC && !s.IncludeDLC {
		return "dlc"
	}
	if f.Kind == "extra" {
		if !s.IncludeExtras {
			return "extras"
		}
		return ""
	}
	if !s.WantsPlatform(f.OS) {
		return "platform:" + f.OS
	}
	if !s.WantsLanguage(f.Language) && !s.LanguageFallback {
		return "language:" + f.Language
	}
	return ""
}

type unwanted struct {
	file   db.File
	reason string
}

// downloadedPath returns the copy of f that exists on disk: the finished download,
// or the previous version that is kept while a newer one is pending. "" if none.
func downloadedPath(f db.File) string {
	if f.Status == db.StatusDone {
		return f.LocalPath
	}
	return f.PreviousPath
}

func (s *Syncer) findUnwanted(ctx context.Context, ns db.Settings) ([]unwanted, error) {
	files, err := s.db.ListActiveFiles(ctx)
	if err != nil {
		return nil, err
	}
	games := map[int64]db.Game{}
	if ns.SelectedOnly() {
		all, err := s.db.ListGames(ctx)
		if err != nil {
			return nil, err
		}
		for _, g := range all {
			games[g.ID] = g
		}
	}
	dlc := map[int64]bool{}
	seen := map[int64]bool{}
	for _, f := range files {
		if !seen[f.GameID] {
			seen[f.GameID] = true
			prods, err := s.db.ListProducts(ctx, f.GameID)
			if err != nil {
				return nil, err
			}
			for _, p := range prods {
				dlc[p.ID] = p.IsDLC
			}
		}
	}
	var out []unwanted
	for _, f := range files {
		if r := unwantedReason(f, games[f.GameID], dlc[f.ProductID], ns); r != "" {
			out = append(out, unwanted{f, r})
		}
	}
	return out, nil
}

func previewOf(list []unwanted) *RemovalPreview {
	p := &RemovalPreview{Reasons: []string{}}
	reasons := map[string]bool{}
	for _, u := range list {
		p.Removed.Files++
		p.Removed.Bytes += u.file.Size
		if downloadedPath(u.file) != "" {
			p.Removed.DownloadedFiles++
			p.Removed.DownloadedBytes += u.file.Size
		}
		reasons[u.reason] = true
	}
	for r := range reasons {
		p.Reasons = append(p.Reasons, r)
	}
	sort.Strings(p.Reasons)
	p.NeedsConfirmation = p.Removed.DownloadedFiles > 0
	return p
}

// PreviewSettings reports what applying ns would drop.
func (s *Syncer) PreviewSettings(ctx context.Context, ns db.Settings) (*RemovalPreview, error) {
	list, err := s.findUnwanted(ctx, ns)
	if err != nil {
		return nil, err
	}
	return previewOf(list), nil
}

// ErrConfirmationRequired is returned by ApplySettings when downloaded files would be dropped.
type ErrConfirmationRequired struct {
	Preview *RemovalPreview
}

func (e *ErrConfirmationRequired) Error() string { return "confirmation_required" }

// dropFiles forgets the files in list. onRemoved must be "keep" or "delete" when
// downloaded files are among them; files never downloaded are simply forgotten.
func (s *Syncer) dropFiles(ctx context.Context, list []unwanted, onRemoved, why string) error {
	preview := previewOf(list)
	if preview.NeedsConfirmation && onRemoved != "keep" && onRemoved != "delete" {
		return &ErrConfirmationRequired{Preview: preview}
	}
	for _, u := range list {
		f := u.file
		if s.OnDrop != nil {
			s.OnDrop(f.ID)
		}
		onDisk := downloadedPath(f)
		// A transfer in progress is abandoned either way; only its partial file goes.
		if f.Status != db.StatusDone {
			s.paths.RemovePart(f.LocalPath)
		}
		switch {
		case onDisk == "":
			if err := s.db.DeleteFile(ctx, f.ID); err != nil {
				return err
			}
		case onRemoved == "delete":
			if err := s.paths.Remove(onDisk); err != nil {
				return fmt.Errorf("deleting %s: %w", onDisk, err)
			}
			if err := s.db.DeleteFile(ctx, f.ID); err != nil {
				return err
			}
			s.log.Info("deleted file after "+why, "path", onDisk, "reason", u.reason)
		default:
			if err := s.db.SetFileInactive(ctx, f.ID); err != nil {
				return err
			}
		}
	}
	if len(list) > 0 {
		s.log.Info(why+" dropped tracked files", "files", len(list), "action", onRemoved)
	}
	return nil
}

// ApplySettings stores ns. onRemoved must be "keep" or "delete" when downloaded
// files stop being wanted; files never downloaded are simply forgotten. A sync
// that is running with the old settings is stopped first, since it would plan
// (and re-add) files the new settings drop; the caller starts a fresh one.
func (s *Syncer) ApplySettings(ctx context.Context, ns db.Settings, onRemoved string) error {
	old, err := s.db.GetSettings(ctx)
	if err != nil {
		return err
	}
	if !old.SamePlan(ns) {
		if err := s.CancelAndWait(ctx); err != nil {
			return err
		}
	}
	list, err := s.findUnwanted(ctx, ns)
	if err != nil {
		return err
	}
	if err := s.dropFiles(ctx, list, onRemoved, "settings change"); err != nil {
		return err
	}
	return s.db.SaveSettings(ctx, ns)
}

// SetSelection marks games as selected for download or not. Deselecting a game
// drops its tracked files like a settings change does: onRemoved must be "keep" or
// "delete" when downloaded files are affected. Selecting never touches files; the
// caller syncs the games afterwards so their files get planned.
func (s *Syncer) SetSelection(ctx context.Context, ids []int64, selected bool, onRemoved string) error {
	// Hold the games' locks (in a fixed order) so a sync of one of them cannot plan
	// files between the drop below and the flag change.
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	for _, id := range sorted {
		defer s.lockGame(id)()
	}
	if !selected {
		settings, err := s.db.GetSettings(ctx)
		if err != nil {
			return err
		}
		var list []unwanted
		if settings.SelectedOnly() {
			for _, id := range ids {
				files, err := s.db.ListActiveFilesByGame(ctx, id)
				if err != nil {
					return err
				}
				for _, f := range files {
					list = append(list, unwanted{f, ReasonUnselected})
				}
			}
		}
		if err := s.dropFiles(ctx, list, onRemoved, "deselecting games"); err != nil {
			return err
		}
	}
	if err := s.db.SetGamesSelected(ctx, ids, selected); err != nil {
		return err
	}
	s.log.Info("game selection changed", "games", len(ids), "selected", selected)
	return nil
}
