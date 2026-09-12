package library

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/alehel/gogl-watcher/internal/db"
	"github.com/alehel/gogl-watcher/internal/gog"
)

const (
	// scanInterval paces the catalog scan. One game every quarter minute covers
	// about 5000 games a day, which is more than most libraries hold, while
	// staying far below the request rate of an ordinary sync.
	scanInterval = 15 * time.Second
	// scanIdleInterval is the slower beat used once every game is current, so an
	// idle installation is not querying anything in a tight loop.
	scanIdleInterval = 5 * time.Minute
	// scanStaleAfter is when a game's catalog is fetched again. Installers do
	// change size, but an estimate is advisory and games the sync looks at are
	// refreshed for free, so the scan itself can be this patient.
	scanStaleAfter = 30 * 24 * time.Hour
	// scanBackoffMax caps the wait after repeated failures.
	scanBackoffMax = 10 * time.Minute
	// ownedTTL is how long the scanner reuses the owned-product set it needs to
	// tell owned DLC from the rest, rather than fetching it per game.
	ownedTTL = time.Hour
)

// ScanStatus describes the catalog scan for the UI.
type ScanStatus struct {
	GamesScanned int        `json:"games_scanned"`
	GamesTotal   int        `json:"games_total"`
	Scanning     bool       `json:"scanning"`
	LastError    *string    `json:"last_error"`
	NextScanAt   *time.Time `json:"next_scan_at"`
}

// Scanner fills the size catalog for the games a sync never looks at.
//
// In the "all" download mode every game is synced anyway and the scanner finds
// nothing to do. In "selected" mode the games nobody ticked are never fetched,
// and they are exactly the ones a "what would the whole library cost?" answer
// needs, so they are collected here instead: one game at a time, slowly, in the
// background.
type Scanner struct {
	db         *db.DB
	gog        gog.API
	syncer     *Syncer
	log        *slog.Logger
	interval   time.Duration
	staleAfter time.Duration

	mu        sync.Mutex
	scanning  bool
	lastError *string
	nextAt    *time.Time

	owned      gog.OwnedSet
	ownedAt    time.Time
	ownedValid bool
}

// NewScanner creates a catalog scanner.
func NewScanner(d *db.DB, g gog.API, s *Syncer, log *slog.Logger) *Scanner {
	return &Scanner{db: d, gog: g, syncer: s, log: log.With("component", "catalog"),
		interval: scanInterval, staleAfter: scanStaleAfter}
}

// Status returns a snapshot of the scan for the UI.
func (sc *Scanner) Status(ctx context.Context) ScanStatus {
	sc.mu.Lock()
	st := ScanStatus{Scanning: sc.scanning, LastError: sc.lastError, NextScanAt: sc.nextAt}
	sc.mu.Unlock()
	st.GamesScanned, st.GamesTotal, _ = sc.db.CatalogCoverage(ctx)
	return st
}

// Run scans one game per interval until ctx is done.
func (sc *Scanner) Run(ctx context.Context) {
	wait := sc.interval
	for {
		next := time.Now().Add(wait)
		sc.setNext(&next)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			sc.setNext(nil)
			return
		case <-timer.C:
		}
		scanned, err := sc.scanOne(ctx)
		switch {
		case ctx.Err() != nil:
			sc.setNext(nil)
			return
		case err != nil:
			wait *= 2
			if wait > scanBackoffMax {
				wait = scanBackoffMax
			}
		case scanned:
			wait = sc.interval
		default:
			wait = scanIdleInterval
		}
	}
}

// scanOne fetches the catalog of the game that has been waiting longest. It
// reports whether a game was scanned; no game to scan is not an error.
func (sc *Scanner) scanOne(ctx context.Context) (bool, error) {
	if !sc.ready(ctx) {
		return false, nil
	}
	game, err := sc.db.NextCatalogScan(ctx, time.Now().Add(-sc.staleAfter))
	if err != nil || game == nil {
		return false, err
	}
	sc.setScanning(true)
	defer sc.setScanning(false)
	owned, err := sc.ownedIDs(ctx)
	if err != nil {
		sc.fail(ctx, "owned products", err)
		return false, err
	}
	if err := sc.syncer.ScanCatalog(ctx, *game, owned); err != nil {
		sc.fail(ctx, game.Title, err)
		return false, err
	}
	sc.setError(nil)
	sc.log.Debug("catalog scanned", "game", game.Title)
	return true, nil
}

func (sc *Scanner) fail(ctx context.Context, what string, err error) {
	if ctx.Err() != nil {
		return
	}
	msg := err.Error()
	sc.setError(&msg)
	sc.log.Warn("could not scan sizes", "what", what, "error", err)
}

// ready reports whether scanning makes sense now. It stands down while a sync
// runs: the sync is fetching the same details far faster, and its games are
// written to the catalog on the way past.
func (sc *Scanner) ready(ctx context.Context) bool {
	done, err := sc.db.SetupComplete(ctx)
	return err == nil && done && sc.gog.Authenticated() && !sc.syncer.Status().Running
}

// ownedIDs returns the owned-product set, refetching it at most once per TTL so
// that telling owned DLC from the rest does not double the request rate.
func (sc *Scanner) ownedIDs(ctx context.Context) (gog.OwnedSet, error) {
	sc.mu.Lock()
	if sc.ownedValid && time.Since(sc.ownedAt) < ownedTTL {
		owned := sc.owned
		sc.mu.Unlock()
		return owned, nil
	}
	sc.mu.Unlock()
	owned, err := sc.gog.OwnedIDs(ctx)
	if err != nil {
		return nil, err
	}
	sc.mu.Lock()
	sc.owned, sc.ownedAt, sc.ownedValid = owned, time.Now(), true
	sc.mu.Unlock()
	return owned, nil
}

func (sc *Scanner) setNext(t *time.Time) {
	sc.mu.Lock()
	sc.nextAt = t
	sc.mu.Unlock()
}

func (sc *Scanner) setScanning(v bool) {
	sc.mu.Lock()
	sc.scanning = v
	sc.mu.Unlock()
}

func (sc *Scanner) setError(e *string) {
	sc.mu.Lock()
	sc.lastError = e
	sc.mu.Unlock()
}

// ScanCatalog records what GOG offers for one game, whether or not the current
// settings want any of it.
func (s *Syncer) ScanCatalog(ctx context.Context, game db.Game, owned gog.OwnedSet) error {
	defer s.lockGame(game.ID)()
	p, err := s.gog.ProductDetails(ctx, game.ID)
	if err != nil {
		return err
	}
	return s.db.ReplaceCatalog(ctx, game.ID, CatalogOf(game, p, owned))
}
