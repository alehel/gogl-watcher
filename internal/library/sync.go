package library

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/alehel/gogl-watcher/internal/db"
	"github.com/alehel/gogl-watcher/internal/gog"
)

// ErrSyncRunning is returned when a sync is requested while one is active.
var ErrSyncRunning = errors.New("a library sync is already running")

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

	mu      sync.Mutex
	status  Status
	cancel  context.CancelFunc
	details int // concurrent detail fetches
}

// NewSyncer creates a Syncer.
func NewSyncer(d *db.DB, g gog.API, paths Paths, log *slog.Logger) *Syncer {
	s := &Syncer{db: d, gog: g, log: log.With("component", "sync"), paths: paths, details: 3}
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
	now := time.Now()
	s.status.Running = false
	s.status.Phase = ""
	s.status.LastFinishedAt = &now
	_ = s.db.SetTime(context.Background(), "sync.last_finished_at", now)
	if err != nil {
		msg := err.Error()
		s.status.LastError = &msg
		_ = s.db.SetKV(context.Background(), "sync.last_error", msg)
	} else {
		_ = s.db.SetKV(context.Background(), "sync.last_error", "")
	}
	s.cancel = nil
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
	settings, err := s.db.GetSettings(ctx)
	if err != nil {
		return err
	}
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
			err := s.syncGame(ctx, id, owned, settings)
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
	if n, err := s.db.MarkMissingDone(ctx, s.paths.Exists); err == nil && n > 0 {
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
	err = s.syncGame(ctx, id, owned, settings)
	if s.OnChange != nil {
		s.OnChange()
	}
	return err
}

func (s *Syncer) syncGame(ctx context.Context, id int64, owned gog.OwnedSet, settings db.Settings) error {
	game, err := s.db.GetGame(ctx, id)
	if err != nil {
		return err
	}
	if game == nil {
		return fmt.Errorf("game %d not in library", id)
	}
	p, err := s.gog.ProductDetails(ctx, id)
	if err != nil {
		_ = s.db.SetGameDetailsSynced(ctx, id, err.Error())
		return err
	}
	planned := Plan(*game, p, owned, settings)
	var keep []int64
	added, updated := 0, 0
	for _, pp := range planned {
		if err := s.db.UpsertProduct(ctx, pp.Product); err != nil {
			return err
		}
		for _, f := range pp.Files {
			res, err := s.db.UpsertDesiredFile(ctx, f, s.paths.Exists)
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
		}
	}
	dropped, err := s.db.DeactivateOtherFiles(ctx, id, keep)
	if err != nil {
		return err
	}
	if added > 0 || updated > 0 || len(dropped) > 0 {
		s.log.Info("game synced", "game", game.Title, "new_files", added, "updated_files", updated, "dropped_files", len(dropped))
	} else {
		s.log.Debug("game unchanged", "game", game.Title)
	}
	return s.db.SetGameDetailsSynced(ctx, id, "")
}
