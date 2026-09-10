package logstore

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestHandlerStoresAndQueries(t *testing.T) {
	sdb, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "l.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	if _, err := sdb.Exec(`CREATE TABLE logs (id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL, level TEXT NOT NULL, component TEXT NOT NULL DEFAULT '', message TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	store := NewStore(sdb, 3)
	log := slog.New(NewHandler(slog.NewTextHandler(io.Discard, nil), store, slog.LevelInfo))
	log.Debug("hidden")
	log.With("component", "sync").Info("hello", "games", 3)
	log.Warn("careful", "component", "download")
	log.Error("bad")
	log.Info("fourth")
	store.Close()

	ctx := context.Background()
	entries, more, err := store.Query(ctx, "info", "", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if more || len(entries) != 3 { // pruned to maxRows=3, newest kept
		t.Fatalf("entries = %+v more=%v", entries, more)
	}
	if entries[0].Message != "fourth" || entries[1].Level != "error" || entries[2].Component != "download" {
		t.Errorf("unexpected order/content: %+v", entries)
	}
	if time.Since(entries[0].Time) > time.Minute {
		t.Errorf("bad timestamp %v", entries[0].Time)
	}
	warn, _, _ := store.Query(ctx, "warn", "", 0, 10)
	if len(warn) != 2 {
		t.Errorf("warn+ entries = %d, want 2", len(warn))
	}
	search, _, _ := store.Query(ctx, "debug", "care", 0, 10)
	if len(search) != 1 || search[0].Message != "careful" {
		t.Errorf("search = %+v", search)
	}
	page, more, _ := store.Query(ctx, "debug", "", 0, 2)
	if len(page) != 2 || !more {
		t.Errorf("pagination: %d more=%v", len(page), more)
	}
	older, _, _ := store.Query(ctx, "debug", "", page[1].ID, 2)
	if len(older) != 1 {
		t.Errorf("older page = %+v", older)
	}
}
