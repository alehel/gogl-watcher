// Command gogl-watcher keeps an offline copy of a GOG library and serves a web UI.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/alehel/gogl-watcher/internal/config"
	"github.com/alehel/gogl-watcher/internal/db"
	"github.com/alehel/gogl-watcher/internal/downloader"
	"github.com/alehel/gogl-watcher/internal/gog"
	"github.com/alehel/gogl-watcher/internal/library"
	"github.com/alehel/gogl-watcher/internal/logstore"
	"github.com/alehel/gogl-watcher/internal/scheduler"
	"github.com/alehel/gogl-watcher/internal/server"
	"github.com/alehel/gogl-watcher/web"
)

// dbTokenStore adapts the database to gog.TokenStore.
type dbTokenStore struct{ db *db.DB }

func (s dbTokenStore) Load(ctx context.Context) (gog.Token, error) {
	a, err := s.db.GetAuth(ctx)
	return gog.Token{AccessToken: a.AccessToken, RefreshToken: a.RefreshToken, ExpiresAt: a.ExpiresAt, UserID: a.UserID, Username: a.Username, Error: a.Error}, err
}

func (s dbTokenStore) Save(ctx context.Context, t gog.Token) error {
	return s.db.SaveAuth(ctx, db.Auth{AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, ExpiresAt: t.ExpiresAt, UserID: t.UserID, Username: t.Username, Error: t.Error})
}

func (s dbTokenStore) Clear(ctx context.Context) error { return s.db.ClearAuth(ctx) }

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.LibraryDir, 0o755); err != nil {
		return fmt.Errorf("creating library dir: %w", err)
	}
	database, err := db.Open(cfg.DBPath())
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer database.Close()

	logs := logstore.NewStore(database.DB, 20000)
	defer logs.Close()
	stdout := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})
	log := slog.New(logstore.NewHandler(stdout, logs, cfg.LogLevel))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var api gog.API
	mock := os.Getenv("MOCK_GOG") == "1"
	if mock {
		m, err := gog.NewMock(ctx, dbTokenStore{database})
		if err != nil {
			return err
		}
		api = m
		log.Warn("MOCK_GOG=1: using a fake GOG library, no real downloads will happen", "component", "main")
	} else {
		c, err := gog.NewClient(ctx, dbTokenStore{database}, log, config.Version)
		if err != nil {
			return err
		}
		api = c
	}

	paths := library.Paths{Root: cfg.LibraryDir}
	syncer := library.NewSyncer(database, api, paths, log)
	dl := downloader.New(database, api, paths, log)
	syncer.OnChange = dl.Wake
	syncer.OnDrop = dl.Cancel
	sched := scheduler.New(database, api, syncer, log, time.Duration(cfg.StartupSyncDelaySeconds)*time.Second)

	settings, err := database.GetSettings(ctx)
	if err != nil {
		return err
	}
	setupDone, _ := database.SetupComplete(ctx)
	dl.Configure(settings, setupDone)
	if n, err := syncer.CheckMissing(ctx); err != nil {
		log.Warn("startup disk check skipped", "component", "main", "error", err)
	} else if n > 0 {
		log.Warn("downloaded files missing from disk, queued again", "component", "main", "count", n)
	}

	var workers sync.WaitGroup
	workers.Add(2)
	go func() { defer workers.Done(); dl.Run(ctx) }()
	go func() { defer workers.Done(); sched.Run(ctx) }()

	srv := &server.Server{
		DB: database, GOG: api, Syncer: syncer, Downloads: dl, Scheduler: sched, Logs: logs, Paths: paths,
		UI: web.Dist(), Version: config.Version, Log: log.With("component", "http"), LibraryDir: cfg.LibraryDir,
	}
	httpServer := &http.Server{Addr: cfg.Listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	log.Info("gogl-watcher started", "component", "main", "version", config.Version, "listen", cfg.Listen,
		"library", cfg.LibraryDir, "data", cfg.DataDir, "setup_complete", setupDone, "mock", mock)
	err = httpServer.ListenAndServe()
	// Stop the workers (also when the listener failed) and let running transfers
	// record their state before the deferred closes take the database away.
	stop()
	workers.Wait()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Info("shutting down", "component", "main")
	return nil
}
