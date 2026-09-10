// Package config reads the process configuration from environment variables.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Version is set at build time via -ldflags "-X ...config.Version=...".
var Version = "dev"

// Config holds all runtime configuration.
type Config struct {
	// DataDir holds the SQLite database and other state.
	DataDir string
	// LibraryDir is where game folders are created.
	LibraryDir string
	// Listen is the HTTP listen address, e.g. ":8080".
	Listen string
	// LogLevel is the minimum level written to stdout and the log store.
	LogLevel slog.Level
	// StartupSyncDelaySeconds is how long to wait after boot before the first scheduled sync.
	StartupSyncDelaySeconds int
}

// FromEnv builds a Config from environment variables, applying defaults.
func FromEnv() (Config, error) {
	c := Config{
		DataDir:                 envOr("DATA_DIR", "/data"),
		LibraryDir:              envOr("LIBRARY_DIR", "/library"),
		Listen:                  ":" + envOr("PORT", "8080"),
		LogLevel:                slog.LevelInfo,
		StartupSyncDelaySeconds: 15,
	}
	if v := os.Getenv("LISTEN"); v != "" {
		c.Listen = v
	}
	switch strings.ToLower(envOr("LOG_LEVEL", "info")) {
	case "debug":
		c.LogLevel = slog.LevelDebug
	case "info":
		c.LogLevel = slog.LevelInfo
	case "warn", "warning":
		c.LogLevel = slog.LevelWarn
	case "error":
		c.LogLevel = slog.LevelError
	default:
		return c, fmt.Errorf("invalid LOG_LEVEL %q", os.Getenv("LOG_LEVEL"))
	}
	if v := os.Getenv("STARTUP_SYNC_DELAY_SECONDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return c, fmt.Errorf("invalid STARTUP_SYNC_DELAY_SECONDS %q", v)
		}
		c.StartupSyncDelaySeconds = n
	}
	for _, d := range []*string{&c.DataDir, &c.LibraryDir} {
		abs, err := filepath.Abs(*d)
		if err != nil {
			return c, err
		}
		*d = abs
	}
	return c, nil
}

// DBPath returns the SQLite file path.
func (c Config) DBPath() string { return filepath.Join(c.DataDir, "gogl-watcher.db") }

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
