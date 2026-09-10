package library

import (
	"context"
	"fmt"
	"sort"

	"github.com/alehel/gogl-watcher/internal/db"
)

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
func unwantedReason(f db.File, isDLC bool, s db.Settings) string {
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

func (s *Syncer) findUnwanted(ctx context.Context, ns db.Settings) ([]unwanted, error) {
	files, err := s.db.ListActiveFiles(ctx)
	if err != nil {
		return nil, err
	}
	dlc := map[int64]bool{}
	games := map[int64]bool{}
	for _, f := range files {
		if !games[f.GameID] {
			games[f.GameID] = true
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
		if r := unwantedReason(f, dlc[f.ProductID], ns); r != "" {
			out = append(out, unwanted{f, r})
		}
	}
	return out, nil
}

// PreviewSettings reports what applying ns would drop.
func (s *Syncer) PreviewSettings(ctx context.Context, ns db.Settings) (*RemovalPreview, error) {
	list, err := s.findUnwanted(ctx, ns)
	if err != nil {
		return nil, err
	}
	p := &RemovalPreview{Reasons: []string{}}
	reasons := map[string]bool{}
	for _, u := range list {
		p.Removed.Files++
		p.Removed.Bytes += u.file.Size
		if u.file.Status == db.StatusDone {
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
	return p, nil
}

// ErrConfirmationRequired is returned by ApplySettings when downloaded files would be dropped.
type ErrConfirmationRequired struct {
	Preview *RemovalPreview
}

func (e *ErrConfirmationRequired) Error() string { return "confirmation_required" }

// ApplySettings stores ns. onRemoved must be "keep" or "delete" when downloaded
// files stop being wanted; files never downloaded are simply forgotten.
func (s *Syncer) ApplySettings(ctx context.Context, ns db.Settings, onRemoved string) error {
	list, err := s.findUnwanted(ctx, ns)
	if err != nil {
		return err
	}
	preview, _ := s.PreviewSettings(ctx, ns)
	if preview.NeedsConfirmation && onRemoved != "keep" && onRemoved != "delete" {
		return &ErrConfirmationRequired{Preview: preview}
	}
	for _, u := range list {
		f := u.file
		switch {
		case f.Status != db.StatusDone:
			if err := s.db.DeleteFile(ctx, f.ID); err != nil {
				return err
			}
			_ = s.paths.Remove(f.LocalPath) // clears any .part
		case onRemoved == "delete":
			if err := s.paths.Remove(f.LocalPath); err != nil {
				return fmt.Errorf("deleting %s: %w", f.LocalPath, err)
			}
			if err := s.db.DeleteFile(ctx, f.ID); err != nil {
				return err
			}
			s.log.Info("deleted file after settings change", "path", f.LocalPath, "reason", u.reason)
		default:
			if err := s.db.SetFileInactive(ctx, f.ID); err != nil {
				return err
			}
		}
	}
	if len(list) > 0 {
		s.log.Info("settings change dropped tracked files", "files", len(list), "action", onRemoved)
	}
	return s.db.SaveSettings(ctx, ns)
}
