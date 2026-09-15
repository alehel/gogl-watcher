package library

import (
	"context"
	"fmt"
	"slices"

	"github.com/alehel/gogl-watcher/internal/db"
)

// ReasonUnselected is the removal reason for files of a game that is not selected
// while only selected games are downloaded.
const ReasonUnselected = "unselected"

// RemovalAction says what to do with files that are already downloaded when they
// stop being wanted. Anything else (in particular the empty value the UI sends
// before it has asked) means the question has not been answered yet.
type RemovalAction string

const (
	RemovalKeep   RemovalAction = "keep"   // leave them on disk, stop tracking them
	RemovalDelete RemovalAction = "delete" // delete them from disk
)

// Answered reports whether a is an answer to the confirmation question.
func (a RemovalAction) Answered() bool { return a == RemovalKeep || a == RemovalDelete }

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
// offered is what the tracked installers say about the languages GOG has for
// f's product and platform; see fallsBackTo.
func unwantedReason(f db.File, game db.Game, isDLC bool, s db.Settings, offered offeredLanguages) string {
	if !s.WantsGame(game) {
		return ReasonUnselected
	}
	s = s.ForGame(game)
	if isDLC && !s.IncludeDLC {
		return "dlc"
	}
	if f.Kind == db.KindInstaller && !isDLC && !s.IncludeInstallers {
		return "installers"
	}
	if f.Kind == db.KindPatch && !s.IncludePatches {
		return "patches"
	}
	if f.Kind == db.KindExtra {
		if !s.IncludeExtras {
			return "extras"
		}
		return ""
	}
	if f.Kind == db.KindSave {
		if !s.IncludeSaves {
			return "saves"
		}
		return ""
	}
	if !s.WantsPlatform(f.OS) {
		return "platform:" + f.OS
	}
	if !s.WantsLanguage(f.Language) && !(s.LanguageFallback && offered.fallsBackTo(f, s)) {
		return "language:" + f.Language
	}
	return ""
}

// offeredLanguages is, per product, platform and kind, the languages of the
// tracked installers (and, apart, of the tracked patches): the choice the
// plan's language fallback picks between, as far as it is known without
// asking GOG.
type offeredLanguages map[languageKey][]string

type languageKey struct {
	product int64
	os      string
	kind    string
}

func offeredLanguagesOf(files []db.File) offeredLanguages {
	out := offeredLanguages{}
	for _, f := range files {
		if f.Kind != db.KindInstaller && f.Kind != db.KindPatch {
			continue
		}
		k := languageKey{f.ProductID, f.OS, f.Kind}
		if !slices.Contains(out[k], f.Language) {
			out[k] = append(out[k], f.Language)
		}
	}
	return out
}

// fallsBackTo reports whether the fallback keeps f, an installer or patch in a
// language the settings do not select: it does only while none of the selected
// languages exists for the same product, platform and kind. Where one does,
// the plan picks that one and drops f, whatever the fallback says.
func (o offeredLanguages) fallsBackTo(f db.File, s db.Settings) bool {
	for _, lang := range o[languageKey{f.ProductID, f.OS, f.Kind}] {
		if s.WantsLanguage(lang) {
			return false
		}
	}
	return true
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
	// The games carry their selection and their own DLC and extras opt-ins.
	games := map[int64]db.Game{}
	all, err := s.db.ListGames(ctx)
	if err != nil {
		return nil, err
	}
	for _, g := range all {
		games[g.ID] = g
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
	offered := offeredLanguagesOf(files)
	var out []unwanted
	for _, f := range files {
		if r := unwantedReason(f, games[f.GameID], dlc[f.ProductID], ns, offered); r != "" {
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
	slices.Sort(p.Reasons)
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

// confirmRemoval reports (as an error the caller passes on) that dropping list
// would touch downloaded files while onRemoved says nothing about them.
func confirmRemoval(list []unwanted, onRemoved RemovalAction) error {
	if p := previewOf(list); p.NeedsConfirmation && !onRemoved.Answered() {
		return &ErrConfirmationRequired{Preview: p}
	}
	return nil
}

// dropFiles forgets the files in list. onRemoved must be answered when downloaded
// files are among them; files never downloaded are simply forgotten.
func (s *Syncer) dropFiles(ctx context.Context, list []unwanted, onRemoved RemovalAction, why string) error {
	if err := confirmRemoval(list, onRemoved); err != nil {
		return err
	}
	// The rows go first, all of them, and the transfers after: a transfer that is
	// aborted wakes the download queue, which would otherwise start the next
	// file of the same game again from a row that is about to be dropped (and
	// then run on with no row to cancel it through).
	deleted := 0
	for _, u := range list {
		f := u.file
		onDisk := downloadedPath(f)
		switch {
		case onDisk == "":
			if err := s.db.DeleteFile(ctx, f.ID); err != nil {
				return err
			}
			deleted++
		case onRemoved == RemovalDelete:
			if err := s.paths.Remove(onDisk); err != nil {
				return fmt.Errorf("deleting %s: %w", onDisk, err)
			}
			if err := s.db.DeleteFile(ctx, f.ID); err != nil {
				return err
			}
			deleted++
			s.log.Info("deleted file after "+why, "path", onDisk, "reason", u.reason)
		default:
			if err := s.db.SetFileInactive(ctx, f.ID); err != nil {
				return err
			}
		}
	}
	for _, u := range list {
		f := u.file
		if f.Status == db.StatusDone {
			continue
		}
		// A transfer in progress is abandoned either way; only its partial file goes.
		s.abortTransfer(f.ID, f.LocalPath)
	}
	if len(list) > 0 {
		s.log.Info(why+" dropped tracked files", "files", len(list), "deleted", deleted, "action", onRemoved)
	}
	return nil
}

// ApplySettings stores ns. onRemoved must be answered when downloaded files stop
// being wanted; files never downloaded are simply forgotten. A sync
// that is running with the old settings is stopped first, since it would plan
// (and re-add) files the new settings drop, and the next sync cannot start until
// the new settings are stored; the caller triggers that fresh sync.
func (s *Syncer) ApplySettings(ctx context.Context, ns db.Settings, onRemoved RemovalAction) error {
	// Ask before touching anything: a change that is refused for lack of an
	// answer must not disturb a running sync.
	list, err := s.findUnwanted(ctx, ns)
	if err != nil {
		return err
	}
	if err := confirmRemoval(list, onRemoved); err != nil {
		return err
	}
	old, err := s.db.GetSettings(ctx)
	if err != nil {
		return err
	}
	if !old.SamePlan(ns) {
		if !s.lockPlan(ctx) {
			return ctx.Err()
		}
		defer s.unlockPlan()
		if err := s.CancelAndWait(ctx); err != nil {
			return err
		}
		// A sync of one game would go on planning with the old settings, too. One
		// that starts now waits for the plan lock, and so for the new settings.
		if err := s.stopGameSyncs(ctx); err != nil {
			return err
		}
		// The syncs may have planned more files before they stopped.
		if list, err = s.findUnwanted(ctx, ns); err != nil {
			return err
		}
	}
	if err := s.dropFiles(ctx, list, onRemoved, "settings change"); err != nil {
		return err
	}
	return s.db.SaveSettings(ctx, ns)
}

// SetGameOptions opts one game in to (or out of) base game installers, DLC,
// patches, extras and cloud saves regardless of the library-wide settings. Opting out
// drops the files it no longer wants like a settings change does: onRemoved
// must be answered when downloaded files are affected. Opting in never touches
// files; the caller syncs the game afterwards so they get planned.
func (s *Syncer) SetGameOptions(ctx context.Context, id int64, o db.GameOptions, onRemoved RemovalAction) error {
	defer s.lockGame(id)()
	game, err := s.db.GetGame(ctx, id)
	if err != nil {
		return err
	}
	if game == nil {
		return ErrGameNotFound
	}
	settings, err := s.db.GetSettings(ctx)
	if err != nil {
		return err
	}
	game.Options = o
	files, err := s.db.ListActiveFilesByGame(ctx, id)
	if err != nil {
		return err
	}
	prods, err := s.db.ListProducts(ctx, id)
	if err != nil {
		return err
	}
	isDLC := map[int64]bool{}
	for _, p := range prods {
		isDLC[p.ID] = p.IsDLC
	}
	offered := offeredLanguagesOf(files)
	var list []unwanted
	for _, f := range files {
		if r := unwantedReason(f, *game, isDLC[f.ProductID], settings, offered); r != "" {
			list = append(list, unwanted{f, r})
		}
	}
	if err := s.dropFiles(ctx, list, onRemoved, "changing the game's options"); err != nil {
		return err
	}
	if err := s.db.SetGameOptions(ctx, id, o); err != nil {
		return err
	}
	// The cached offer marked what the old options would download.
	s.offerMu.Lock()
	delete(s.offers, id)
	s.offerMu.Unlock()
	s.log.Info("game options changed", "game", game.Title, "installers", o.Installers, "dlc", o.DLC, "patches", o.Patches, "extras", o.Extras, "saves", o.Saves)
	return nil
}

// SetSelection marks games as selected for download or not. Deselecting a game
// drops its tracked files like a settings change does: onRemoved must be answered
// when downloaded files are affected. Selecting never touches files; the caller
// syncs the games afterwards so their files get planned.
func (s *Syncer) SetSelection(ctx context.Context, ids []int64, selected bool, onRemoved RemovalAction) error {
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
