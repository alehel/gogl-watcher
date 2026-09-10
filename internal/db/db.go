// Package db wraps the SQLite database used for all persistent state.
package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// DB is the application database.
type DB struct {
	*sql.DB
}

// Open opens (creating if needed) the database at path and applies the schema.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)", path)
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sdb.SetMaxOpenConns(1) // modernc sqlite is happiest with a single writer connection
	if err := sdb.Ping(); err != nil {
		sdb.Close()
		return nil, err
	}
	d := &DB{sdb}
	if err := d.migrate(); err != nil {
		sdb.Close()
		return nil, err
	}
	return d, nil
}

const schema = `
CREATE TABLE IF NOT EXISTS kv (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS auth (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  access_token TEXT NOT NULL DEFAULT '',
  refresh_token TEXT NOT NULL DEFAULT '',
  expires_at INTEGER NOT NULL DEFAULT 0,
  user_id TEXT NOT NULL DEFAULT '',
  username TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS games (
  id INTEGER PRIMARY KEY,
  title TEXT NOT NULL,
  slug TEXT NOT NULL DEFAULT '',
  image TEXT NOT NULL DEFAULT '',
  folder TEXT NOT NULL,
  works_windows INTEGER NOT NULL DEFAULT 0,
  works_mac INTEGER NOT NULL DEFAULT 0,
  works_linux INTEGER NOT NULL DEFAULT 0,
  owned INTEGER NOT NULL DEFAULT 1,
  details_synced_at INTEGER,
  details_error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS products (
  id INTEGER PRIMARY KEY,
  game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
  title TEXT NOT NULL,
  is_dlc INTEGER NOT NULL DEFAULT 0,
  folder TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS products_game ON products(game_id);
CREATE TABLE IF NOT EXISTS files (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
  product_id INTEGER NOT NULL REFERENCES products(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  os TEXT NOT NULL DEFAULT '',
  language TEXT NOT NULL DEFAULT '',
  gog_id TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  version TEXT NOT NULL DEFAULT '',
  size INTEGER NOT NULL DEFAULT 0,
  downlink TEXT NOT NULL,
  rel_dir TEXT NOT NULL DEFAULT '',
  filename TEXT NOT NULL DEFAULT '',
  local_path TEXT NOT NULL DEFAULT '',
  previous_path TEXT NOT NULL DEFAULT '',
  md5 TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'pending',
  active INTEGER NOT NULL DEFAULT 1,
  error TEXT NOT NULL DEFAULT '',
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at INTEGER NOT NULL DEFAULT 0,
  downloaded_at INTEGER,
  updated_at INTEGER NOT NULL,
  UNIQUE(product_id, kind, gog_id)
);
CREATE INDEX IF NOT EXISTS files_game ON files(game_id);
CREATE INDEX IF NOT EXISTS files_status ON files(status, active);
CREATE TABLE IF NOT EXISTS logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts INTEGER NOT NULL,
  level TEXT NOT NULL,
  component TEXT NOT NULL DEFAULT '',
  message TEXT NOT NULL
);
`

func (d *DB) migrate() error {
	_, err := d.Exec(schema)
	return err
}

// ---- key/value helpers ----

// GetKV returns the raw value for key, or "" if absent.
func (d *DB) GetKV(ctx context.Context, key string) (string, error) {
	var v string
	err := d.QueryRowContext(ctx, `SELECT value FROM kv WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetKV stores value under key.
func (d *DB) SetKV(ctx context.Context, key, value string) error {
	_, err := d.ExecContext(ctx, `INSERT INTO kv(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// ---- settings ----

// Settings are the user-editable options shown in the UI.
type Settings struct {
	Platforms              []string `json:"platforms"`
	Languages              []string `json:"languages"`
	LanguageFallback       bool     `json:"language_fallback"`
	ContentChosen          bool     `json:"content_chosen"`
	IncludeDLC             bool     `json:"include_dlc"`
	IncludeExtras          bool     `json:"include_extras"`
	MaxConcurrentDownloads int      `json:"max_concurrent_downloads"`
	SpeedLimitKBps         int      `json:"speed_limit_kbps"`
	CheckIntervalHours     int      `json:"check_interval_hours"`
	DownloadsPaused        bool     `json:"downloads_paused"`
}

// DefaultSettings returns the settings used before the user configured anything.
func DefaultSettings() Settings {
	return Settings{
		Platforms:              []string{},
		Languages:              []string{"en"},
		LanguageFallback:       true,
		IncludeDLC:             true,
		IncludeExtras:          false,
		MaxConcurrentDownloads: 2,
		SpeedLimitKBps:         0,
		CheckIntervalHours:     6,
	}
}

// Normalize validates and cleans a settings value.
func (s *Settings) Normalize() error {
	seen := map[string]bool{}
	var plats []string
	for _, p := range s.Platforms {
		p = strings.ToLower(strings.TrimSpace(p))
		switch p {
		case "windows", "mac", "linux":
		case "osx", "macos":
			p = "mac"
		default:
			return fmt.Errorf("unknown platform %q", p)
		}
		if !seen[p] {
			seen[p] = true
			plats = append(plats, p)
		}
	}
	if plats == nil {
		plats = []string{}
	}
	s.Platforms = plats
	seen = map[string]bool{}
	var langs []string
	for _, l := range s.Languages {
		l = strings.ToLower(strings.TrimSpace(l))
		if l == "" {
			continue
		}
		if !seen[l] {
			seen[l] = true
			langs = append(langs, l)
		}
	}
	if len(langs) == 0 {
		langs = []string{"en"}
	}
	s.Languages = langs
	if s.MaxConcurrentDownloads < 1 || s.MaxConcurrentDownloads > 8 {
		return fmt.Errorf("max_concurrent_downloads must be between 1 and 8")
	}
	if s.SpeedLimitKBps < 0 {
		return fmt.Errorf("speed_limit_kbps must be >= 0")
	}
	if s.CheckIntervalHours < 1 || s.CheckIntervalHours > 168 {
		return fmt.Errorf("check_interval_hours must be between 1 and 168")
	}
	return nil
}

// WantsPlatform reports whether os is selected.
func (s Settings) WantsPlatform(os string) bool {
	for _, p := range s.Platforms {
		if p == os {
			return true
		}
	}
	return false
}

// WantsLanguage reports whether lang is selected.
func (s Settings) WantsLanguage(lang string) bool {
	for _, l := range s.Languages {
		if l == lang {
			return true
		}
	}
	return false
}

// GetSettings loads the stored settings, falling back to defaults.
func (d *DB) GetSettings(ctx context.Context) (Settings, error) {
	s := DefaultSettings()
	raw, err := d.GetKV(ctx, "settings")
	if err != nil || raw == "" {
		return s, err
	}
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return DefaultSettings(), err
	}
	if s.Platforms == nil {
		s.Platforms = []string{}
	}
	return s, nil
}

// SaveSettings persists s.
func (d *DB) SaveSettings(ctx context.Context, s Settings) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return d.SetKV(ctx, "settings", string(raw))
}

// SetupComplete reports whether the first-run wizard finished.
func (d *DB) SetupComplete(ctx context.Context) (bool, error) {
	v, err := d.GetKV(ctx, "setup_complete")
	return v == "1", err
}

// SetSetupComplete stores the wizard state.
func (d *DB) SetSetupComplete(ctx context.Context, done bool) error {
	v := "0"
	if done {
		v = "1"
	}
	return d.SetKV(ctx, "setup_complete", v)
}

// GetTime reads a stored timestamp (unix millis) under key.
func (d *DB) GetTime(ctx context.Context, key string) (*time.Time, error) {
	v, err := d.GetKV(ctx, key)
	if err != nil || v == "" {
		return nil, err
	}
	var ms int64
	if _, err := fmt.Sscanf(v, "%d", &ms); err != nil {
		return nil, nil
	}
	t := time.UnixMilli(ms)
	return &t, nil
}

// SetTime stores t under key.
func (d *DB) SetTime(ctx context.Context, key string, t time.Time) error {
	return d.SetKV(ctx, key, fmt.Sprintf("%d", t.UnixMilli()))
}

// ---- auth ----

// Auth is the stored GOG token set.
type Auth struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	UserID       string
	Username     string
	Error        string
}

// GetAuth returns the stored tokens (zero value when none).
func (d *DB) GetAuth(ctx context.Context) (Auth, error) {
	var a Auth
	var exp int64
	err := d.QueryRowContext(ctx, `SELECT access_token, refresh_token, expires_at, user_id, username, error FROM auth WHERE id = 1`).
		Scan(&a.AccessToken, &a.RefreshToken, &exp, &a.UserID, &a.Username, &a.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return Auth{}, nil
	}
	if err != nil {
		return Auth{}, err
	}
	if exp > 0 {
		a.ExpiresAt = time.UnixMilli(exp)
	}
	return a, nil
}

// SaveAuth replaces the stored tokens.
func (d *DB) SaveAuth(ctx context.Context, a Auth) error {
	var exp int64
	if !a.ExpiresAt.IsZero() {
		exp = a.ExpiresAt.UnixMilli()
	}
	_, err := d.ExecContext(ctx, `INSERT INTO auth(id, access_token, refresh_token, expires_at, user_id, username, error)
		VALUES(1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET access_token = excluded.access_token, refresh_token = excluded.refresh_token,
		expires_at = excluded.expires_at, user_id = excluded.user_id, username = excluded.username, error = excluded.error`,
		a.AccessToken, a.RefreshToken, exp, a.UserID, a.Username, a.Error)
	return err
}

// ClearAuth removes the stored tokens.
func (d *DB) ClearAuth(ctx context.Context) error {
	_, err := d.ExecContext(ctx, `DELETE FROM auth WHERE id = 1`)
	return err
}

// ---- games ----

// Game is a base game in the user's library.
type Game struct {
	ID              int64
	Title           string
	Slug            string
	Image           string
	Folder          string
	WorksWindows    bool
	WorksMac        bool
	WorksLinux      bool
	Owned           bool
	DetailsSyncedAt *time.Time
	DetailsError    string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// GameStats aggregates the active files of a game.
type GameStats struct {
	FilesTotal   int
	FilesDone    int
	FilesError   int
	FilesPending int
	BytesTotal   int64
	BytesDone    int64
}

// UpsertGame inserts or updates listing information. Folder is only set on insert.
func (d *DB) UpsertGame(ctx context.Context, g Game) error {
	now := time.Now().UnixMilli()
	_, err := d.ExecContext(ctx, `INSERT INTO games(id, title, slug, image, folder, works_windows, works_mac, works_linux, owned, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET title = excluded.title, slug = excluded.slug, image = excluded.image,
		works_windows = excluded.works_windows, works_mac = excluded.works_mac, works_linux = excluded.works_linux,
		owned = 1, updated_at = excluded.updated_at`,
		g.ID, g.Title, g.Slug, g.Image, g.Folder, b2i(g.WorksWindows), b2i(g.WorksMac), b2i(g.WorksLinux), now, now)
	return err
}

// FolderTaken reports whether another game already uses folder.
func (d *DB) FolderTaken(ctx context.Context, folder string, excludeID int64) (bool, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM games WHERE folder = ? AND id <> ?`, folder, excludeID).Scan(&n)
	return n > 0, err
}

// GetGame loads one game.
func (d *DB) GetGame(ctx context.Context, id int64) (*Game, error) {
	rows, err := d.QueryContext(ctx, gameSelect+` WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil
	}
	g, err := scanGame(rows)
	if err != nil {
		return nil, err
	}
	return &g, nil
}

const gameSelect = `SELECT id, title, slug, image, folder, works_windows, works_mac, works_linux, owned, details_synced_at, details_error, created_at, updated_at FROM games`

func scanGame(rows *sql.Rows) (Game, error) {
	var g Game
	var ww, wm, wl, owned int
	var synced sql.NullInt64
	var created, updated int64
	err := rows.Scan(&g.ID, &g.Title, &g.Slug, &g.Image, &g.Folder, &ww, &wm, &wl, &owned, &synced, &g.DetailsError, &created, &updated)
	if err != nil {
		return g, err
	}
	g.WorksWindows, g.WorksMac, g.WorksLinux, g.Owned = ww == 1, wm == 1, wl == 1, owned == 1
	if synced.Valid {
		t := time.UnixMilli(synced.Int64)
		g.DetailsSyncedAt = &t
	}
	g.CreatedAt, g.UpdatedAt = time.UnixMilli(created), time.UnixMilli(updated)
	return g, nil
}

// ListGames returns all games ordered by title.
func (d *DB) ListGames(ctx context.Context) ([]Game, error) {
	rows, err := d.QueryContext(ctx, gameSelect+` ORDER BY title COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Game
	for rows.Next() {
		g, err := scanGame(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ListGameIDs returns every game id.
func (d *DB) ListGameIDs(ctx context.Context, ownedOnly bool) ([]int64, error) {
	q := `SELECT id FROM games`
	if ownedOnly {
		q += ` WHERE owned = 1`
	}
	rows, err := d.QueryContext(ctx, q+` ORDER BY title COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// MarkGamesNotOwned flags every game whose id is not in keep as no longer owned.
func (d *DB) MarkGamesNotOwned(ctx context.Context, keep []int64) error {
	if len(keep) == 0 {
		return nil
	}
	ph := strings.Repeat("?,", len(keep))
	ph = ph[:len(ph)-1]
	args := make([]any, len(keep))
	for i, id := range keep {
		args[i] = id
	}
	_, err := d.ExecContext(ctx, `UPDATE games SET owned = 0 WHERE id NOT IN (`+ph+`)`, args...)
	return err
}

// SetGameDetailsSynced records a successful (err == "") or failed detail fetch.
func (d *DB) SetGameDetailsSynced(ctx context.Context, id int64, errMsg string) error {
	now := time.Now().UnixMilli()
	if errMsg != "" {
		_, err := d.ExecContext(ctx, `UPDATE games SET details_error = ?, updated_at = ? WHERE id = ?`, errMsg, now, id)
		return err
	}
	_, err := d.ExecContext(ctx, `UPDATE games SET details_synced_at = ?, details_error = '', updated_at = ? WHERE id = ?`, now, now, id)
	return err
}

// GameStatsAll returns per-game aggregates over active files.
func (d *DB) GameStatsAll(ctx context.Context) (map[int64]GameStats, error) {
	rows, err := d.QueryContext(ctx, `SELECT game_id,
		COUNT(*), SUM(status = 'done'), SUM(status = 'error'), SUM(status = 'pending'),
		COALESCE(SUM(size), 0), COALESCE(SUM(CASE WHEN status = 'done' THEN size ELSE 0 END), 0)
		FROM files WHERE active = 1 GROUP BY game_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]GameStats{}
	for rows.Next() {
		var id int64
		var s GameStats
		if err := rows.Scan(&id, &s.FilesTotal, &s.FilesDone, &s.FilesError, &s.FilesPending, &s.BytesTotal, &s.BytesDone); err != nil {
			return nil, err
		}
		out[id] = s
	}
	return out, rows.Err()
}

// ---- products ----

// Product is a base game or DLC that owns files.
type Product struct {
	ID     int64
	GameID int64
	Title  string
	IsDLC  bool
	Folder string
}

// UpsertProduct inserts or updates a product.
func (d *DB) UpsertProduct(ctx context.Context, p Product) error {
	_, err := d.ExecContext(ctx, `INSERT INTO products(id, game_id, title, is_dlc, folder) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET game_id = excluded.game_id, title = excluded.title, is_dlc = excluded.is_dlc, folder = excluded.folder`,
		p.ID, p.GameID, p.Title, b2i(p.IsDLC), p.Folder)
	return err
}

// ListProducts returns the products of a game, base game first.
func (d *DB) ListProducts(ctx context.Context, gameID int64) ([]Product, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, game_id, title, is_dlc, folder FROM products WHERE game_id = ? ORDER BY is_dlc, title COLLATE NOCASE`, gameID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Product
	for rows.Next() {
		var p Product
		var dlc int
		if err := rows.Scan(&p.ID, &p.GameID, &p.Title, &dlc, &p.Folder); err != nil {
			return nil, err
		}
		p.IsDLC = dlc == 1
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- files ----

// File statuses stored in the database.
const (
	StatusPending  = "pending"
	StatusDone     = "done"
	StatusError    = "error"
	StatusInactive = "inactive"
)

// File is one downloadable item (installer part or extra).
type File struct {
	ID            int64
	GameID        int64
	ProductID     int64
	Kind          string // "installer" | "extra"
	OS            string
	Language      string
	GogID         string
	Name          string
	Version       string
	Size          int64
	Downlink      string
	RelDir        string
	Filename      string
	LocalPath     string
	PreviousPath  string
	MD5           string
	Status        string
	Active        bool
	Error         string
	Attempts      int
	NextAttemptAt time.Time
	DownloadedAt  *time.Time
	UpdatedAt     time.Time
}

const fileSelect = `SELECT id, game_id, product_id, kind, os, language, gog_id, name, version, size, downlink, rel_dir, filename,
	local_path, previous_path, md5, status, active, error, attempts, next_attempt_at, downloaded_at, updated_at FROM files`

func scanFile(rows *sql.Rows) (File, error) {
	var f File
	var active int
	var next int64
	var dl sql.NullInt64
	var upd int64
	err := rows.Scan(&f.ID, &f.GameID, &f.ProductID, &f.Kind, &f.OS, &f.Language, &f.GogID, &f.Name, &f.Version, &f.Size,
		&f.Downlink, &f.RelDir, &f.Filename, &f.LocalPath, &f.PreviousPath, &f.MD5, &f.Status, &active, &f.Error,
		&f.Attempts, &next, &dl, &upd)
	if err != nil {
		return f, err
	}
	f.Active = active == 1
	if next > 0 {
		f.NextAttemptAt = time.UnixMilli(next)
	}
	if dl.Valid {
		t := time.UnixMilli(dl.Int64)
		f.DownloadedAt = &t
	}
	f.UpdatedAt = time.UnixMilli(upd)
	return f, nil
}

func (d *DB) queryFiles(ctx context.Context, where string, args ...any) ([]File, error) {
	rows, err := d.QueryContext(ctx, fileSelect+" "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []File
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetFile loads a file by id.
func (d *DB) GetFile(ctx context.Context, id int64) (*File, error) {
	fs, err := d.queryFiles(ctx, `WHERE id = ?`, id)
	if err != nil || len(fs) == 0 {
		return nil, err
	}
	return &fs[0], nil
}

// ListFilesByGame returns all files (active or not) of a game.
func (d *DB) ListFilesByGame(ctx context.Context, gameID int64) ([]File, error) {
	return d.queryFiles(ctx, `WHERE game_id = ? ORDER BY product_id, CASE kind WHEN 'installer' THEN 0 ELSE 1 END, os, language, gog_id`, gameID)
}

// ListFilesByStatus returns files with the given status and active flag.
func (d *DB) ListFilesByStatus(ctx context.Context, status string, active bool) ([]File, error) {
	return d.queryFiles(ctx, `WHERE status = ? AND active = ? ORDER BY id`, status, b2i(active))
}

// ListActiveFiles returns every active file.
func (d *DB) ListActiveFiles(ctx context.Context) ([]File, error) {
	return d.queryFiles(ctx, `WHERE active = 1 ORDER BY game_id, id`)
}

// NextPendingFiles returns up to limit pending files ready to be downloaded, skipping exclude ids.
func (d *DB) NextPendingFiles(ctx context.Context, limit int, exclude []int64) ([]File, error) {
	now := time.Now().UnixMilli()
	where := `WHERE status = 'pending' AND active = 1 AND next_attempt_at <= ?`
	args := []any{now}
	if len(exclude) > 0 {
		ph := strings.Repeat("?,", len(exclude))
		where += ` AND id NOT IN (` + ph[:len(ph)-1] + `)`
		for _, id := range exclude {
			args = append(args, id)
		}
	}
	// Prefer finishing games that are already partially downloaded, then small files first.
	where += ` ORDER BY game_id, size LIMIT ?`
	args = append(args, limit)
	return d.queryFiles(ctx, where, args...)
}

// CountFiles returns counts of pending/error/done active files.
func (d *DB) CountFilesByStatus(ctx context.Context) (map[string]int, error) {
	rows, err := d.QueryContext(ctx, `SELECT status, COUNT(*) FROM files WHERE active = 1 GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}

// UpsertFileResult reports what happened during reconcile.
type UpsertFileResult struct {
	ID       int64
	Inserted bool
	Updated  bool // an existing done file got a new version/size
}

// UpsertDesiredFile inserts a wanted file, or refreshes an existing row. If the row
// was done and the version/size changed, it becomes pending again and the old path is
// remembered so it can be removed after the new download succeeds.
func (d *DB) UpsertDesiredFile(ctx context.Context, f File, localExists func(path string) bool) (UpsertFileResult, error) {
	now := time.Now().UnixMilli()
	existing, err := d.queryFiles(ctx, `WHERE product_id = ? AND kind = ? AND gog_id = ?`, f.ProductID, f.Kind, f.GogID)
	if err != nil {
		return UpsertFileResult{}, err
	}
	if len(existing) == 0 {
		res, err := d.ExecContext(ctx, `INSERT INTO files(game_id, product_id, kind, os, language, gog_id, name, version, size, downlink, rel_dir, status, active, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', 1, ?)`,
			f.GameID, f.ProductID, f.Kind, f.OS, f.Language, f.GogID, f.Name, f.Version, f.Size, f.Downlink, f.RelDir, now)
		if err != nil {
			return UpsertFileResult{}, err
		}
		id, _ := res.LastInsertId()
		return UpsertFileResult{ID: id, Inserted: true}, nil
	}
	e := existing[0]
	status := e.Status
	prev := e.PreviousPath
	updated := false
	if !e.Active || status == StatusInactive {
		// Re-activated by a settings change: keep the file if it is still on disk.
		if e.LocalPath != "" && localExists(e.LocalPath) && (e.Version == f.Version && e.Size == f.Size) {
			status = StatusDone
		} else {
			status = StatusPending
		}
	}
	changed := e.Version != f.Version || (f.Size > 0 && e.Size != f.Size)
	if status == StatusDone && changed {
		status = StatusPending
		prev = e.LocalPath
		updated = true
	}
	if status == StatusDone && e.LocalPath != "" && !localExists(e.LocalPath) {
		status = StatusPending
	}
	_, err = d.ExecContext(ctx, `UPDATE files SET game_id = ?, os = ?, language = ?, name = ?, version = ?, size = ?, downlink = ?, rel_dir = ?,
		status = ?, active = 1, previous_path = ?, error = CASE WHEN ? = 'pending' AND status <> 'pending' THEN '' ELSE error END,
		attempts = CASE WHEN ? = 'pending' AND status <> 'pending' THEN 0 ELSE attempts END, next_attempt_at = 0, updated_at = ? WHERE id = ?`,
		f.GameID, f.OS, f.Language, f.Name, f.Version, f.Size, f.Downlink, f.RelDir, status, prev, status, status, now, e.ID)
	if err != nil {
		return UpsertFileResult{}, err
	}
	return UpsertFileResult{ID: e.ID, Updated: updated}, nil
}

// DeactivateOtherFiles marks active files of the game not in keepIDs as inactive
// (if downloaded) or deletes them (if never downloaded). Returns the deactivated files.
func (d *DB) DeactivateOtherFiles(ctx context.Context, gameID int64, keepIDs []int64) ([]File, error) {
	keep := map[int64]bool{}
	for _, id := range keepIDs {
		keep[id] = true
	}
	files, err := d.queryFiles(ctx, `WHERE game_id = ? AND active = 1`, gameID)
	if err != nil {
		return nil, err
	}
	var out []File
	now := time.Now().UnixMilli()
	for _, f := range files {
		if keep[f.ID] {
			continue
		}
		if f.Status == StatusDone && f.LocalPath != "" {
			if _, err := d.ExecContext(ctx, `UPDATE files SET active = 0, status = 'inactive', updated_at = ? WHERE id = ?`, now, f.ID); err != nil {
				return nil, err
			}
		} else {
			if _, err := d.ExecContext(ctx, `DELETE FROM files WHERE id = ?`, f.ID); err != nil {
				return nil, err
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// SetFileInactive marks a file as kept-but-untracked.
func (d *DB) SetFileInactive(ctx context.Context, id int64) error {
	_, err := d.ExecContext(ctx, `UPDATE files SET active = 0, status = 'inactive', updated_at = ? WHERE id = ?`, time.Now().UnixMilli(), id)
	return err
}

// DeleteFile removes a file row.
func (d *DB) DeleteFile(ctx context.Context, id int64) error {
	_, err := d.ExecContext(ctx, `DELETE FROM files WHERE id = ?`, id)
	return err
}

// SetFileResolved stores the resolved filename and destination path.
func (d *DB) SetFileResolved(ctx context.Context, id int64, filename, localPath, md5 string, size int64) error {
	_, err := d.ExecContext(ctx, `UPDATE files SET filename = ?, local_path = ?, md5 = ?, size = CASE WHEN ? > 0 THEN ? ELSE size END, updated_at = ? WHERE id = ?`,
		filename, localPath, md5, size, size, time.Now().UnixMilli(), id)
	return err
}

// SetFileDone marks a download complete.
func (d *DB) SetFileDone(ctx context.Context, id int64, localPath string, size int64) error {
	now := time.Now().UnixMilli()
	_, err := d.ExecContext(ctx, `UPDATE files SET status = 'done', local_path = ?, size = ?, previous_path = '', error = '', attempts = 0,
		next_attempt_at = 0, downloaded_at = ?, updated_at = ? WHERE id = ?`, localPath, size, now, now, id)
	return err
}

// SetFileError records a failed attempt. If retryAfter is zero the file goes to the error state.
func (d *DB) SetFileError(ctx context.Context, id int64, msg string, retryAfter time.Duration) error {
	now := time.Now()
	if retryAfter > 0 {
		_, err := d.ExecContext(ctx, `UPDATE files SET status = 'pending', error = ?, attempts = attempts + 1, next_attempt_at = ?, updated_at = ? WHERE id = ?`,
			msg, now.Add(retryAfter).UnixMilli(), now.UnixMilli(), id)
		return err
	}
	_, err := d.ExecContext(ctx, `UPDATE files SET status = 'error', error = ?, attempts = attempts + 1, next_attempt_at = 0, updated_at = ? WHERE id = ?`,
		msg, now.UnixMilli(), id)
	return err
}

// ResetFile puts a file back to pending.
func (d *DB) ResetFile(ctx context.Context, id int64) error {
	_, err := d.ExecContext(ctx, `UPDATE files SET status = 'pending', error = '', attempts = 0, next_attempt_at = 0, updated_at = ? WHERE id = ? AND active = 1`,
		time.Now().UnixMilli(), id)
	return err
}

// ResetGameErrors puts every failed file of a game back to pending.
func (d *DB) ResetGameErrors(ctx context.Context, gameID int64) error {
	_, err := d.ExecContext(ctx, `UPDATE files SET status = 'pending', error = '', attempts = 0, next_attempt_at = 0, updated_at = ? WHERE game_id = ? AND active = 1 AND status = 'error'`,
		time.Now().UnixMilli(), gameID)
	return err
}

// MarkMissingDone moves done files whose local file vanished back to pending.
func (d *DB) MarkMissingDone(ctx context.Context, exists func(path string) bool) (int, error) {
	files, err := d.queryFiles(ctx, `WHERE status = 'done' AND active = 1`)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, f := range files {
		if f.LocalPath == "" || !exists(f.LocalPath) {
			if err := d.ResetFile(ctx, f.ID); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// LibraryTotals aggregates bytes over active files.
func (d *DB) LibraryTotals(ctx context.Context) (bytesTotal, bytesDone int64, err error) {
	err = d.QueryRowContext(ctx, `SELECT COALESCE(SUM(size),0), COALESCE(SUM(CASE WHEN status='done' THEN size ELSE 0 END),0) FROM files WHERE active = 1`).
		Scan(&bytesTotal, &bytesDone)
	return
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
