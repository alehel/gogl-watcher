package library

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alehel/gogl-watcher/internal/db"
	"github.com/alehel/gogl-watcher/internal/gog"
)

// How the sync decides that a file changed, cheapest signal first.
//
// GOG offers no build identity for the offline installers this application
// archives: the manifest carries a free-text version and an approximate size,
// both of which move when nothing changed and stay put when something did. So
// three signals are combined:
//
//  1. The installer's version and manifest size (free, part of the details
//     fetch that happens anyway). Catches ordinary updates.
//  2. The newest Galaxy build id of the game, one cheap request per platform.
//     The chunked Galaxy build is not the offline installer and the two are
//     published on their own schedules, so a moved build id is only a hint that
//     this game deserves a closer look.
//  3. GOG's published MD5 for the file, which is authoritative but costs a
//     request or two per file. It is spent on the files the first two signals
//     point at, and on a slow rolling re-check of everything else, so that a
//     silent replacement is noticed within verifyInterval even for the games
//     Galaxy never covers.
//
// Signal 3 decides when it can answer, because it is the only one that can
// prevent both a needless multi-gigabyte re-download and a missed update.
const (
	// verifyInterval is how long a checksum-verified copy is taken on trust.
	verifyInterval = 30 * 24 * time.Hour
	// verifyBudget caps the rolling re-checks of one full sync, so the cost of a
	// sync stays roughly constant however large the library is. At the default
	// six-hourly interval this covers about 3000 files a month.
	verifyBudget = 25
	// buildCheckInterval is the shortest time between two build list requests for
	// the same game and platform. A short check interval would otherwise spend
	// most of its requests on a signal that moves days before the installers do.
	buildCheckInterval = 6 * time.Hour
)

// ErrSyncRunning is returned when a sync is requested while one is active.
var ErrSyncRunning = errors.New("a library sync is already running")

// ErrLibraryUnavailable is returned by CheckMissing when the library folder is
// missing or empty although downloads are recorded, which is what an unmounted
// volume looks like.
var ErrLibraryUnavailable = errors.New("library folder is missing or empty although files were downloaded into it; leaving records alone (check the mount)")

// Status describes the sync state for the UI.
type Status struct {
	Running        bool       `json:"running"`
	Phase          string     `json:"phase"`
	GamesTotal     int        `json:"games_total"`
	GamesDone      int        `json:"games_done"`
	LastStartedAt  *time.Time `json:"last_started_at"`
	LastFinishedAt *time.Time `json:"last_finished_at"`
	LastError      *string    `json:"last_error"`
	NextRunAt      *time.Time `json:"next_run_at"`
}

// Syncer reconciles the database with the GOG account.
type Syncer struct {
	db    *db.DB
	gog   gog.API
	log   *slog.Logger
	paths Paths
	// OnChange is called whenever files may have become downloadable.
	OnChange func()
	// OnDrop is called before a tracked file is forgotten, so a running transfer
	// of it can be aborted.
	OnDrop func(fileID int64)

	mu      sync.Mutex
	status  Status
	cancel  context.CancelFunc
	done    chan struct{} // closed when the running sync has stopped
	details int           // concurrent detail fetches
	// gameLocks serialises work on one game (int64 -> *sync.Mutex): a manual or
	// selection-triggered sync of a game, the scheduled sync and a selection
	// change would otherwise race on its file rows.
	gameLocks sync.Map
	// planSem (capacity 1) serialises a settings change with the start of a sync,
	// so a sync cannot read the settings while ApplySettings is still storing them.
	planSem chan struct{}
	// rollingLeft is how many files the running full sync may still re-check
	// against GOG's checksums without a reason to suspect them.
	rollingLeft atomic.Int64
}

// lockPlan takes planSem, giving up when ctx ends.
func (s *Syncer) lockPlan(ctx context.Context) bool {
	select {
	case s.planSem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Syncer) unlockPlan() { <-s.planSem }

// lockGame takes the per-game lock and returns the function that releases it.
func (s *Syncer) lockGame(id int64) func() {
	l, _ := s.gameLocks.LoadOrStore(id, &sync.Mutex{})
	mu := l.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// NewSyncer creates a Syncer.
func NewSyncer(d *db.DB, g gog.API, paths Paths, log *slog.Logger) *Syncer {
	s := &Syncer{db: d, gog: g, log: log.With("component", "sync"), paths: paths, details: 3, planSem: make(chan struct{}, 1)}
	ctx := context.Background()
	s.status.LastFinishedAt, _ = d.GetTime(ctx, "sync.last_finished_at")
	if e, _ := d.GetKV(ctx, "sync.last_error"); e != "" {
		s.status.LastError = &e
	}
	return s
}

// Status returns a snapshot of the sync state.
func (s *Syncer) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// SetNextRun records when the scheduler will run next (for display only).
func (s *Syncer) SetNextRun(t *time.Time) {
	s.mu.Lock()
	s.status.NextRunAt = t
	s.mu.Unlock()
}

// Paths exposes the library path helper.
func (s *Syncer) Paths() Paths { return s.paths }

func (s *Syncer) start() (context.Context, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.Running {
		return nil, ErrSyncRunning
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	now := time.Now()
	s.status.Running = true
	s.status.Phase = "listing"
	s.status.GamesTotal, s.status.GamesDone = 0, 0
	s.status.LastStartedAt = &now
	s.status.LastError = nil
	return ctx, nil
}

func (s *Syncer) finish(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Running = false
	s.status.Phase = ""
	s.cancel = nil
	if s.done != nil {
		close(s.done)
		s.done = nil
	}
	if errors.Is(err, context.Canceled) {
		// Interrupted (shutdown, logout, settings change): not a finished run, so
		// the scheduler retries without waiting a whole interval, and not an error
		// worth showing.
		return
	}
	now := time.Now()
	s.status.LastFinishedAt = &now
	_ = s.db.SetTime(context.Background(), "sync.last_finished_at", now)
	if err != nil {
		msg := err.Error()
		s.status.LastError = &msg
		_ = s.db.SetKV(context.Background(), "sync.last_error", msg)
	} else {
		_ = s.db.SetKV(context.Background(), "sync.last_error", "")
	}
}

// Cancel aborts a running sync.
func (s *Syncer) Cancel() {
	s.mu.Lock()
	c := s.cancel
	s.mu.Unlock()
	if c != nil {
		c()
	}
}

// CancelAndWait aborts a running sync and blocks until it has stopped, or until
// ctx ends. It returns immediately when no sync is running.
func (s *Syncer) CancelAndWait(ctx context.Context) error {
	s.mu.Lock()
	c, done := s.cancel, s.done
	s.mu.Unlock()
	if c == nil {
		return nil
	}
	c()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Syncer) setPhase(phase string, total, done int) {
	s.mu.Lock()
	s.status.Phase = phase
	if total >= 0 {
		s.status.GamesTotal = total
	}
	if done >= 0 {
		s.status.GamesDone = done
	}
	s.mu.Unlock()
}

// SyncAll refreshes the game list and every game's downloads. It blocks until done.
func (s *Syncer) SyncAll(parent context.Context) error {
	ctx, err := s.start()
	if err != nil {
		return err
	}
	stop := context.AfterFunc(parent, func() { s.Cancel() })
	defer stop()
	err = s.syncAll(ctx)
	s.finish(err)
	if s.OnChange != nil {
		s.OnChange()
	}
	return err
}

func (s *Syncer) syncAll(ctx context.Context) error {
	if !s.gog.Authenticated() {
		return errors.New("not authorized with GOG")
	}
	// Wait for a settings change in progress: it stops the previous sync and
	// stores the new settings before letting the next one plan with them.
	if !s.lockPlan(ctx) {
		return ctx.Err()
	}
	settings, err := s.db.GetSettings(ctx)
	s.unlockPlan()
	if err != nil {
		return err
	}
	s.rollingLeft.Store(verifyBudget)
	s.log.Info("library sync started")
	owned, err := s.gog.OwnedIDs(ctx)
	if err != nil {
		return fmt.Errorf("fetching owned products: %w", err)
	}
	games, err := s.gog.ListGames(ctx, func(page, total int) {
		s.log.Debug("listed games page", "page", page, "total", total)
	})
	if err != nil {
		return fmt.Errorf("listing games: %w", err)
	}
	ids := make([]int64, 0, len(games))
	for _, g := range games {
		folder, err := s.folderFor(ctx, g)
		if err != nil {
			return err
		}
		if err := s.db.UpsertGame(ctx, db.Game{ID: g.ID, Title: g.Title, Slug: g.Slug, Image: g.Image, Folder: folder,
			WorksWindows: g.WorksWindows, WorksMac: g.WorksMac, WorksLinux: g.WorksLinux}); err != nil {
			return err
		}
		ids = append(ids, g.ID)
	}
	if err := s.db.MarkGamesNotOwned(ctx, ids); err != nil {
		return err
	}
	s.log.Info("game list fetched", "games", len(games))
	s.setPhase("details", len(games), 0)

	var (
		wg      sync.WaitGroup
		sem     = make(chan struct{}, s.details)
		mu      sync.Mutex
		done    int
		failed  int
		firstEr error
	)
	for _, g := range games {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(id int64, title string) {
			defer wg.Done()
			defer func() { <-sem }()
			err := s.syncGame(ctx, id, owned, settings, true)
			mu.Lock()
			done++
			if err != nil {
				failed++
				if firstEr == nil {
					firstEr = err
				}
				if ctx.Err() == nil {
					s.log.Warn("could not sync game", "game", title, "error", err)
				}
			}
			d := done
			mu.Unlock()
			s.setPhase("details", -1, d)
		}(g.ID, g.Title)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.setPhase("reconciling", -1, -1)
	if n, err := s.CheckMissing(ctx); err != nil {
		s.log.Error("disk check skipped", "error", err)
	} else if n > 0 {
		s.log.Warn("downloaded files missing from disk, queued again", "count", n)
	}
	s.log.Info("library sync finished", "games", len(games), "failed", failed)
	if failed > 0 {
		return fmt.Errorf("%d of %d games could not be synced (first error: %v)", failed, len(games), firstEr)
	}
	return nil
}

func (s *Syncer) folderFor(ctx context.Context, g gog.ListedGame) (string, error) {
	if existing, err := s.db.GetGame(ctx, g.ID); err != nil {
		return "", err
	} else if existing != nil && existing.Folder != "" {
		return existing.Folder, nil
	}
	folder := SanitizeFolder(g.Title)
	taken, err := s.db.FolderTaken(ctx, folder, g.ID)
	if err != nil {
		return "", err
	}
	if taken {
		folder = fmt.Sprintf("%s (%d)", folder, g.ID)
	}
	return folder, nil
}

// CheckMissing queues downloaded files that vanished from disk again. It refuses
// (ErrLibraryUnavailable) when the library folder itself is gone or empty while
// downloads are recorded: an unmounted volume must not turn into a re-download of
// the whole library into the mount point.
func (s *Syncer) CheckMissing(ctx context.Context) (int, error) {
	st, err := os.Stat(s.paths.Root)
	if err != nil || !st.IsDir() {
		return 0, ErrLibraryUnavailable
	}
	entries, err := os.ReadDir(s.paths.Root)
	if err != nil {
		return 0, ErrLibraryUnavailable
	}
	if len(entries) == 0 {
		counts, err := s.db.CountFilesByStatus(ctx)
		if err != nil {
			return 0, err
		}
		if counts[db.StatusDone] > 0 {
			return 0, ErrLibraryUnavailable
		}
	}
	return s.db.MarkMissingDone(ctx, s.paths.Exists)
}

// checkBuilds asks the Galaxy content system for the newest build of the game on
// each selected platform and returns the platforms whose build moved since the
// last sync. A build id that was never recorded is stored without suspecting
// anything: there is nothing to compare it against yet.
//
// Failures are not errors. Most games have no build for some platform, older
// ones have none at all, and the endpoint is not part of the download path, so
// nothing here may stop a sync.
func (s *Syncer) checkBuilds(ctx context.Context, game db.Game, settings db.Settings) map[string]bool {
	suspect := map[string]bool{}
	for _, os := range settings.Platforms {
		if !game.WorksOn(os) {
			continue
		}
		prev, err := s.db.GetGameBuild(ctx, game.ID, os)
		if err != nil {
			s.log.Warn("could not read the recorded build", "game", game.Title, "os", os, "error", err)
			return suspect
		}
		if prev != nil && time.Since(prev.CheckedAt) < buildCheckInterval {
			continue
		}
		build, err := s.gog.LatestBuild(ctx, game.ID, os)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Debug("could not read the build list", "game", game.Title, "os", os, "error", err)
			}
			continue
		}
		if build == nil {
			continue
		}
		if err := s.db.SetGameBuild(ctx, game.ID, os, build.ID, build.VersionName); err != nil {
			s.log.Warn("could not record the build", "game", game.Title, "os", os, "error", err)
			return suspect
		}
		if prev != nil && prev.BuildID != build.ID {
			suspect[os] = true
			s.log.Info("game was rebuilt on GOG, checking its installers",
				"game", game.Title, "os", os, "build", build.ID, "version", build.VersionName)
		}
	}
	return suspect
}

// remoteMD5 returns the MD5 GOG publishes for a file, or "" when it publishes
// none (which is normal for extras).
func (s *Syncer) remoteMD5(ctx context.Context, downlink string) (string, error) {
	link, err := s.gog.ResolveDownlink(ctx, downlink)
	if err != nil {
		return "", err
	}
	if link.ChecksumURL == "" {
		return "", nil
	}
	cs, err := s.gog.FetchChecksum(ctx, link.ChecksumURL)
	if err != nil {
		return "", err
	}
	return strings.ToLower(strings.TrimSpace(cs.MD5)), nil
}

// claimRolling reports whether f is due for a rolling re-check and this sync
// still has room for one, taking it from the budget if so.
func (s *Syncer) claimRolling(f db.File) bool {
	if f.VerifiedAt != nil && time.Since(*f.VerifiedAt) < verifyInterval {
		return false
	}
	if s.rollingLeft.Add(-1) < 0 {
		s.rollingLeft.Add(1)
		return false
	}
	return true
}

// verifier answers, for the files it is worth a request for, whether a
// downloaded copy really differs from what GOG now offers. suspect holds the
// platforms whose Galaxy build moved.
func (s *Syncer) verifier(game db.Game, suspect map[string]bool, rolling bool) db.Verifier {
	return func(ctx context.Context, stored db.File, next db.File, hint bool) (bool, error) {
		switch {
		case hint:
			// The metadata says this changed. Confirm it before a copy that may be
			// tens of gigabytes is thrown away over a bumped version string.
		case suspect[stored.OS], stored.OS == "" && len(suspect) > 0:
			// The game was rebuilt, so its installers may have been too without the
			// metadata moving. Extras carry no platform and are checked with the rest.
		case rolling && s.claimRolling(stored):
			// Nothing points at this file; it is just the one whose turn it is.
		default:
			return hint, nil
		}
		md5, err := s.remoteMD5(ctx, next.Downlink)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Debug("could not read a checksum, using GOG's metadata instead",
					"game", game.Title, "file", stored.Name, "error", err)
			}
			return hint, err
		}
		// Even a file GOG publishes no checksum for counts as checked, so the
		// rolling re-check is not spent on it again on the next sync.
		if err := s.db.SetFileVerified(ctx, stored.ID); err != nil {
			s.log.Debug("could not record a checksum check", "file", stored.Name, "error", err)
		}
		if md5 == "" {
			return hint, nil
		}
		if md5 == strings.ToLower(stored.MD5) {
			if hint {
				s.log.Info("GOG's metadata changed but the file is identical, keeping the local copy",
					"game", game.Title, "file", stored.Name, "version", next.Version)
			}
			return false, nil
		}
		if !hint {
			s.log.Info("file replaced on GOG without a version change",
				"game", game.Title, "file", stored.Name, "version", next.Version)
		}
		return true, nil
	}
}

// abortTransfer stops a running download of a file and removes its partial data.
func (s *Syncer) abortTransfer(fileID int64, localPath string) {
	if s.OnDrop != nil {
		s.OnDrop(fileID)
	}
	s.paths.RemovePart(localPath)
}

// SyncGame refreshes one game's downloads from GOG.
func (s *Syncer) SyncGame(ctx context.Context, id int64) error {
	if !s.gog.Authenticated() {
		return errors.New("not authorized with GOG")
	}
	settings, err := s.db.GetSettings(ctx)
	if err != nil {
		return err
	}
	owned, err := s.gog.OwnedIDs(ctx)
	if err != nil {
		return err
	}
	// The rolling re-check works through the whole library and belongs to a full
	// sync; one game's sync only follows up on what it is told about that game.
	err = s.syncGame(ctx, id, owned, settings, false)
	if s.OnChange != nil {
		s.OnChange()
	}
	return err
}

// syncGame plans one game's files. rolling allows the slow re-check of files
// nothing in particular suspects, which belongs to a full sync.
func (s *Syncer) syncGame(ctx context.Context, id int64, owned gog.OwnedSet, settings db.Settings, rolling bool) error {
	defer s.lockGame(id)()
	game, err := s.db.GetGame(ctx, id)
	if err != nil {
		return err
	}
	if game == nil {
		return fmt.Errorf("game %d not in library", id)
	}
	if !settings.WantsGame(*game) {
		// Not selected for download: leave GOG alone and plan nothing. Its files were
		// dropped when it was deselected (or when the download mode changed); anything
		// still tracked slipped in from a detail fetch that was already running.
		dropped, err := s.db.DeactivateOtherFiles(ctx, id, nil)
		if err != nil {
			return err
		}
		for _, f := range dropped {
			s.abortTransfer(f.ID, f.LocalPath)
		}
		if len(dropped) > 0 {
			s.log.Info("dropped files of a game that is not selected", "game", game.Title, "files", len(dropped))
		}
		s.log.Debug("game not selected, skipped", "game", game.Title)
		return nil
	}
	p, err := s.gog.ProductDetails(ctx, id)
	if err != nil {
		_ = s.db.SetGameDetailsSynced(ctx, id, err.Error())
		return err
	}
	suspect := s.checkBuilds(ctx, *game, settings)
	planned := Plan(*game, p, owned, settings)
	verify := s.verifier(*game, suspect, rolling)
	var keep []int64
	added, updated := 0, 0
	for _, pp := range planned {
		if err := s.db.UpsertProduct(ctx, pp.Product); err != nil {
			return err
		}
		for _, f := range pp.Files {
			res, err := s.db.UpsertDesiredFileVerified(ctx, f, s.paths.Exists, verify)
			if err != nil {
				return err
			}
			keep = append(keep, res.ID)
			if res.Inserted {
				added++
			}
			if res.Updated {
				updated++
				s.log.Info("new version available", "game", game.Title, "file", f.Name, "version", f.Version)
			}
			if res.Changed {
				// A transfer of the old version may be running; it must not finish
				// under the new one, and its partial file is of no use.
				s.abortTransfer(res.ID, res.LocalPath)
			}
		}
	}
	dropped, err := s.db.DeactivateOtherFiles(ctx, id, keep)
	if err != nil {
		return err
	}
	for _, f := range dropped {
		// Nothing tracks the file anymore, so a running transfer would only leave
		// an orphan behind.
		s.abortTransfer(f.ID, f.LocalPath)
	}
	if added > 0 || updated > 0 || len(dropped) > 0 {
		s.log.Info("game synced", "game", game.Title, "new_files", added, "updated_files", updated, "dropped_files", len(dropped))
	} else {
		s.log.Debug("game unchanged", "game", game.Title)
	}
	return s.db.SetGameDetailsSynced(ctx, id, "")
}
