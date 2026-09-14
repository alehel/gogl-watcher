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
	"slices"
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
  selected INTEGER NOT NULL DEFAULT 0,
  details_synced_at INTEGER,
  details_error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS game_tags (
  game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
  tag TEXT NOT NULL,
  PRIMARY KEY (game_id, tag)
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
  manifest_size INTEGER NOT NULL DEFAULT 0,
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
  verified_at INTEGER,
  updated_at INTEGER NOT NULL,
  UNIQUE(product_id, kind, gog_id)
);
CREATE INDEX IF NOT EXISTS files_game ON files(game_id);
CREATE INDEX IF NOT EXISTS files_status ON files(status, active);
CREATE TABLE IF NOT EXISTS game_builds (
  game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
  os TEXT NOT NULL,
  build_id TEXT NOT NULL,
  version_name TEXT NOT NULL DEFAULT '',
  checked_at INTEGER NOT NULL,
  PRIMARY KEY (game_id, os)
);
CREATE TABLE IF NOT EXISTS logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts INTEGER NOT NULL,
  level TEXT NOT NULL,
  component TEXT NOT NULL DEFAULT '',
  message TEXT NOT NULL
);
`

func (d *DB) migrate() error {
	if _, err := d.Exec(schema); err != nil {
		return err
	}
	if err := d.addColumn("games", "selected", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := d.addColumn("files", "manifest_size", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := d.addColumn("files", "verified_at", "INTEGER"); err != nil {
		return err
	}
	if err := d.addColumn("games", "box_art", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := d.addColumn("games", "box_art_checked_at", "INTEGER"); err != nil {
		return err
	}
	if err := d.addColumn("games", "include_dlc", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := d.addColumn("games", "include_extras", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	return d.migrateDownloadMode()
}

// addColumn adds a column to an existing table if it is missing.
func (d *DB) addColumn(table, column, def string) error {
	rows, err := d.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = d.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, def))
	return err
}

// migrateDownloadMode keeps installations set up before the download mode existed
// downloading everything, which is what they were doing.
func (d *DB) migrateDownloadMode() error {
	ctx := context.Background()
	done, err := d.SetupComplete(ctx)
	if err != nil || !done {
		return err
	}
	s, err := d.GetSettings(ctx)
	if err != nil || s.DownloadMode != "" {
		return err
	}
	s.DownloadMode = DownloadAll
	return d.SaveSettings(ctx, s)
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

// Download modes: which games of the library are downloaded.
const (
	DownloadAll         = "all"          // every owned game
	DownloadSelected    = "selected"     // only games the user selected
	DownloadSelectedNew = "selected_new" // selected games, and games bought from now on are selected as they appear
)

// Platforms are the installer platforms the application knows, in the order the
// UI lists them.
var Platforms = []string{"windows", "mac", "linux"}

// NormalizePlatform maps the spellings that turn up — GOG's installer metadata,
// the settings API, settings stored by an older version — onto one of Platforms,
// or "" when the value is none of them.
func NormalizePlatform(os string) string {
	switch strings.ToLower(strings.TrimSpace(os)) {
	case "windows", "win":
		return "windows"
	case "mac", "osx", "macos":
		return "mac"
	case "linux":
		return "linux"
	}
	return ""
}

// Settings are the user-editable options shown in the UI.
type Settings struct {
	// DownloadMode is "all" or "selected"; empty until the setup wizard asked.
	DownloadMode           string   `json:"download_mode"`
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
	for _, raw := range s.Platforms {
		p := NormalizePlatform(raw)
		if p == "" {
			return fmt.Errorf("unknown platform %q", raw)
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
		l = NormalizeLanguage(l)
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
	s.DownloadMode = strings.ToLower(strings.TrimSpace(s.DownloadMode))
	switch s.DownloadMode {
	case "", DownloadAll, DownloadSelected, DownloadSelectedNew:
	default:
		return fmt.Errorf("download_mode must be %q, %q or %q", DownloadAll, DownloadSelected, DownloadSelectedNew)
	}
	return nil
}

// NormalizeLanguage brings a language code into the form settings use, so that
// codes from the picker and codes from GOG's installer manifests compare equal:
// lower case, no separators ("es_mx" and "es-MX" become "esmx"), and GOG's
// legacy codes for Greek ("gk") and Serbian ("sb") folded onto "el" and "sr".
func NormalizeLanguage(code string) string {
	c := strings.ToLower(strings.TrimSpace(code))
	c = strings.NewReplacer("-", "", "_", "").Replace(c)
	switch c {
	case "gk":
		return "el"
	case "sb":
		return "sr"
	}
	return c
}

// SamePlan reports whether s and o would plan the same files, i.e. differ only
// in options that do not affect which files are wanted (concurrency, speed,
// interval, pause).
func (s Settings) SamePlan(o Settings) bool {
	return s.DownloadMode == o.DownloadMode &&
		strings.Join(s.Platforms, ",") == strings.Join(o.Platforms, ",") &&
		strings.Join(s.Languages, ",") == strings.Join(o.Languages, ",") &&
		s.LanguageFallback == o.LanguageFallback && s.IncludeDLC == o.IncludeDLC && s.IncludeExtras == o.IncludeExtras
}

// SelectedOnly reports whether only selected games are downloaded (whether the
// user selects them all, or new games select themselves).
func (s Settings) SelectedOnly() bool {
	return s.DownloadMode == DownloadSelected || s.DownloadMode == DownloadSelectedNew
}

// SelectsNewGames reports whether a game that newly appears in the library is
// selected for download as it does.
func (s Settings) SelectsNewGames() bool {
	return s.DownloadMode == DownloadSelectedNew
}

// WantsGame reports whether a game's files should be downloaded under these settings.
func (s Settings) WantsGame(g Game) bool {
	return !s.SelectedOnly() || g.Selected
}

// ForGame returns the settings as they apply to one game: the library-wide
// settings, with DLC and extras switched on where the game opted in.
func (s Settings) ForGame(g Game) Settings {
	s.IncludeDLC = s.IncludeDLC || g.IncludeDLC
	s.IncludeExtras = s.IncludeExtras || g.IncludeExtras
	return s
}

// WantsPlatform reports whether os is selected.
func (s Settings) WantsPlatform(os string) bool { return slices.Contains(s.Platforms, os) }

// WantsLanguage reports whether lang is selected.
func (s Settings) WantsLanguage(lang string) bool { return slices.Contains(s.Languages, lang) }

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
	Image           string // landscape store tile from the game list
	BoxArt          string // portrait cover, fetched separately; may be empty
	BoxArtCheckedAt *time.Time
	Folder          string
	WorksWindows    bool
	WorksMac        bool
	WorksLinux      bool
	Owned           bool
	Tags            []string // the user's own gog.com tags on the game, sorted
	Selected        bool     // chosen for download (only matters in the "selected" download mode)
	// IncludeDLC and IncludeExtras opt this game into DLC and extras when the
	// settings leave them out for the library as a whole.
	IncludeDLC      bool
	IncludeExtras   bool
	DetailsSyncedAt *time.Time
	DetailsError    string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Cover is the artwork to show for the game: the portrait cover when GOG has
// one, else the landscape tile.
func (g Game) Cover() string {
	if g.BoxArt != "" {
		return g.BoxArt
	}
	return g.Image
}

// WorksOn reports whether GOG lists the game as running on a platform.
func (g Game) WorksOn(os string) bool {
	switch NormalizePlatform(os) {
	case "windows":
		return g.WorksWindows
	case "mac":
		return g.WorksMac
	case "linux":
		return g.WorksLinux
	}
	return false
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

// UpsertGame inserts or updates listing information, tags included. Folder is
// only set on insert. The game's updated_at moves only when the listing really
// changed: every sync lists every game, and "recently updated" must not mean
// "recently synced".
func (d *DB) UpsertGame(ctx context.Context, g Game) error {
	now := time.Now().UnixMilli()
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO games(id, title, slug, image, folder, works_windows, works_mac, works_linux, owned, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET title = excluded.title, slug = excluded.slug, image = excluded.image,
		works_windows = excluded.works_windows, works_mac = excluded.works_mac, works_linux = excluded.works_linux,
		owned = 1, updated_at = CASE WHEN title = excluded.title AND slug = excluded.slug AND image = excluded.image
			AND works_windows = excluded.works_windows AND works_mac = excluded.works_mac AND works_linux = excluded.works_linux
			AND owned = 1 THEN updated_at ELSE excluded.updated_at END`,
		g.ID, g.Title, g.Slug, g.Image, g.Folder, b2i(g.WorksWindows), b2i(g.WorksMac), b2i(g.WorksLinux), now, now)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM game_tags WHERE game_id = ?`, g.ID); err != nil {
		return err
	}
	for _, t := range g.Tags {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO game_tags(game_id, tag) VALUES(?, ?)`, g.ID, t); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// loadTags attaches the tags of the given games.
func (d *DB) loadTags(ctx context.Context, games []Game) error {
	if len(games) == 0 {
		return nil
	}
	byID := map[int64]*Game{}
	args := make([]any, 0, len(games))
	for i := range games {
		byID[games[i].ID] = &games[i]
		args = append(args, games[i].ID)
	}
	rows, err := d.QueryContext(ctx, `SELECT game_id, tag FROM game_tags WHERE game_id IN (`+placeholders(len(games))+`) ORDER BY tag COLLATE NOCASE`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var tag string
		if err := rows.Scan(&id, &tag); err != nil {
			return err
		}
		if g := byID[id]; g != nil {
			g.Tags = append(g.Tags, tag)
		}
	}
	return rows.Err()
}

// Tag is one of the user's gog.com tags with the number of owned games carrying it.
type Tag struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// ListTags returns every tag in use on an owned game, by name.
func (d *DB) ListTags(ctx context.Context) ([]Tag, error) {
	rows, err := d.QueryContext(ctx, `SELECT t.tag, COUNT(*) FROM game_tags t JOIN games g ON g.id = t.game_id
		WHERE g.owned = 1 GROUP BY t.tag ORDER BY t.tag COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Tag{}
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.Name, &t.Count); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GameIDs returns the id of every game ever listed, owned or not.
func (d *DB) GameIDs(ctx context.Context) (map[int64]bool, error) {
	rows, err := d.QueryContext(ctx, `SELECT id FROM games`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, rows.Err()
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
		// A failed read must not look like a missing game.
		return nil, rows.Err()
	}
	g, err := scanGame(rows)
	if err != nil {
		return nil, err
	}
	rows.Close()
	gs := []Game{g}
	if err := d.loadTags(ctx, gs); err != nil {
		return nil, err
	}
	return &gs[0], nil
}

const gameSelect = `SELECT id, title, slug, image, box_art, box_art_checked_at, folder, works_windows, works_mac, works_linux, owned, selected, include_dlc, include_extras, details_synced_at, details_error, created_at, updated_at FROM games`

func scanGame(rows *sql.Rows) (Game, error) {
	var g Game
	var ww, wm, wl, owned, selected, dlc, extras int
	var synced, artChecked sql.NullInt64
	var created, updated int64
	err := rows.Scan(&g.ID, &g.Title, &g.Slug, &g.Image, &g.BoxArt, &artChecked, &g.Folder, &ww, &wm, &wl, &owned, &selected, &dlc, &extras, &synced, &g.DetailsError, &created, &updated)
	if err != nil {
		return g, err
	}
	g.WorksWindows, g.WorksMac, g.WorksLinux, g.Owned, g.Selected = ww == 1, wm == 1, wl == 1, owned == 1, selected == 1
	g.IncludeDLC, g.IncludeExtras = dlc == 1, extras == 1
	if synced.Valid {
		t := time.UnixMilli(synced.Int64)
		g.DetailsSyncedAt = &t
	}
	if artChecked.Valid {
		t := time.UnixMilli(artChecked.Int64)
		g.BoxArtCheckedAt = &t
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	return out, d.loadTags(ctx, out)
}

// SetGamesSelected marks the given games as selected (or not) for download.
func (d *DB) SetGamesSelected(ctx context.Context, ids []int64, selected bool) error {
	if len(ids) == 0 {
		return nil
	}
	args := []any{b2i(selected), time.Now().UnixMilli()}
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := d.ExecContext(ctx, `UPDATE games SET selected = ?, updated_at = ? WHERE id IN (`+placeholders(len(ids))+`)`, args...)
	return err
}

// SetGameOptions stores a game's own DLC and extras opt-ins.
func (d *DB) SetGameOptions(ctx context.Context, id int64, dlc, extras bool) error {
	_, err := d.ExecContext(ctx, `UPDATE games SET include_dlc = ?, include_extras = ?, updated_at = ? WHERE id = ?`,
		b2i(dlc), b2i(extras), time.Now().UnixMilli(), id)
	return err
}

// MarkGamesNotOwned flags every game whose id is not in keep as no longer owned.
func (d *DB) MarkGamesNotOwned(ctx context.Context, keep []int64) error {
	if len(keep) == 0 {
		return nil
	}
	args := make([]any, len(keep))
	for i, id := range keep {
		args[i] = id
	}
	_, err := d.ExecContext(ctx, `UPDATE games SET owned = 0 WHERE id NOT IN (`+placeholders(len(keep))+`)`, args...)
	return err
}

// SetGameDetailsSynced records a successful (err == "") or failed detail fetch.
// It is not an update of the game: details_synced_at is its own timestamp.
func (d *DB) SetGameDetailsSynced(ctx context.Context, id int64, errMsg string) error {
	now := time.Now().UnixMilli()
	if errMsg != "" {
		_, err := d.ExecContext(ctx, `UPDATE games SET details_error = ? WHERE id = ?`, errMsg, id)
		return err
	}
	_, err := d.ExecContext(ctx, `UPDATE games SET details_synced_at = ?, details_error = '' WHERE id = ?`, now, id)
	return err
}

// TouchGame records that something about the game changed just now, which is
// what the library's "recently updated" order goes by: a sync that planned new
// or updated files for it, or dropped some.
func (d *DB) TouchGame(ctx context.Context, id int64) error {
	_, err := d.ExecContext(ctx, `UPDATE games SET updated_at = ? WHERE id = ?`, time.Now().UnixMilli(), id)
	return err
}

// SetGameBoxArt records the outcome of a box art lookup, empty when GOG has none.
func (d *DB) SetGameBoxArt(ctx context.Context, id int64, url string) error {
	now := time.Now().UnixMilli()
	_, err := d.ExecContext(ctx, `UPDATE games SET box_art = ?, box_art_checked_at = ? WHERE id = ?`, url, now, id)
	return err
}

// GameBuild is the newest Galaxy build seen for a game and OS.
type GameBuild struct {
	BuildID     string
	VersionName string
	CheckedAt   time.Time
}

// GetGameBuild returns the build last recorded for a game and OS, or nil when
// none was ever recorded.
func (d *DB) GetGameBuild(ctx context.Context, gameID int64, os string) (*GameBuild, error) {
	var b GameBuild
	var checked int64
	err := d.QueryRowContext(ctx, `SELECT build_id, version_name, checked_at FROM game_builds WHERE game_id = ? AND os = ?`,
		gameID, os).Scan(&b.BuildID, &b.VersionName, &checked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b.CheckedAt = time.UnixMilli(checked)
	return &b, nil
}

// SetGameBuild records the build now published for a game and OS.
func (d *DB) SetGameBuild(ctx context.Context, gameID int64, os, buildID, versionName string) error {
	_, err := d.ExecContext(ctx, `INSERT INTO game_builds(game_id, os, build_id, version_name, checked_at) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(game_id, os) DO UPDATE SET build_id = excluded.build_id, version_name = excluded.version_name, checked_at = excluded.checked_at`,
		gameID, os, buildID, versionName, time.Now().UnixMilli())
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
	Size          int64 // best known size: GOG's manifest until the transfer measured it
	ManifestSize  int64 // size as reported by GOG's manifest at the last sync (0 = unknown)
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
	// VerifiedAt is when the local copy was last compared against GOG's checksum
	// (nil = never).
	VerifiedAt *time.Time
	UpdatedAt  time.Time
}

const fileSelect = `SELECT id, game_id, product_id, kind, os, language, gog_id, name, version, size, manifest_size, downlink, rel_dir, filename,
	local_path, previous_path, md5, status, active, error, attempts, next_attempt_at, downloaded_at, verified_at, updated_at FROM files`

func scanFile(rows *sql.Rows) (File, error) {
	var f File
	var active int
	var next int64
	var dl, ver sql.NullInt64
	var upd int64
	err := rows.Scan(&f.ID, &f.GameID, &f.ProductID, &f.Kind, &f.OS, &f.Language, &f.GogID, &f.Name, &f.Version, &f.Size, &f.ManifestSize,
		&f.Downlink, &f.RelDir, &f.Filename, &f.LocalPath, &f.PreviousPath, &f.MD5, &f.Status, &active, &f.Error,
		&f.Attempts, &next, &dl, &ver, &upd)
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
	if ver.Valid {
		t := time.UnixMilli(ver.Int64)
		f.VerifiedAt = &t
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

// ListActiveFilesByGame returns the active files of one game.
func (d *DB) ListActiveFilesByGame(ctx context.Context, gameID int64) ([]File, error) {
	return d.queryFiles(ctx, `WHERE game_id = ? AND active = 1 ORDER BY id`, gameID)
}

// NextPendingFiles returns up to limit pending files ready to be downloaded, skipping exclude ids.
// Files of a game the account no longer owns wait: GOG would refuse them, and
// they are wanted again as they are should the game come back.
func (d *DB) NextPendingFiles(ctx context.Context, limit int, exclude []int64) ([]File, error) {
	now := time.Now().UnixMilli()
	where := `WHERE status = 'pending' AND active = 1 AND next_attempt_at <= ?
		AND game_id IN (SELECT id FROM games WHERE owned = 1)`
	args := []any{now}
	if len(exclude) > 0 {
		where += ` AND id NOT IN (` + placeholders(len(exclude)) + `)`
		for _, id := range exclude {
			args = append(args, id)
		}
	}
	// Keep a game's files together (so one game finishes before the next starts),
	// and take its small files first.
	where += ` ORDER BY game_id, size LIMIT ?`
	args = append(args, limit)
	return d.queryFiles(ctx, where, args...)
}

// CountFilesByStatus returns counts of pending/error/done active files.
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
	// Changed is set when a file that was already pending now points at another
	// version or download link, so a transfer started for the old one is stale.
	Changed bool
	// LocalPath is the path the row had before this update (empty if unresolved).
	LocalPath string
}

// Verifier answers whether a file that was already downloaded really differs
// from the one GOG now offers. stored is the row as it stands, next the file as
// GOG describes it now, and hint what comparing their metadata concluded.
// Returning an error means the answer is unknown and the hint is used.
//
// It exists because GOG's installer metadata is a weak signal in both
// directions: version strings are bumped for rebuilds that change nothing, and
// installers are sometimes replaced without the version or the manifest size
// moving at all. Answering from a checksum costs a request or two, so a
// Verifier decides for itself which files are worth the trouble.
type Verifier func(ctx context.Context, stored File, next File, hint bool) (bool, error)

// UpsertDesiredFile inserts a wanted file, or refreshes an existing row. If the row
// was done and the version/size changed, it becomes pending again and the old path is
// remembered so it can be removed after the new download succeeds. It trusts GOG's
// metadata; UpsertDesiredFileVerified can confirm a change before acting on it.
func (d *DB) UpsertDesiredFile(ctx context.Context, f File, localExists func(path string) bool) (UpsertFileResult, error) {
	return d.UpsertDesiredFileVerified(ctx, f, localExists, nil)
}

// UpsertDesiredFileVerified is UpsertDesiredFile with verify consulted, where it
// can answer, about whether a downloaded file really changed. A nil verify
// behaves exactly like UpsertDesiredFile.
func (d *DB) UpsertDesiredFileVerified(ctx context.Context, f File, localExists func(path string) bool, verify Verifier) (UpsertFileResult, error) {
	now := time.Now().UnixMilli()
	existing, err := d.queryFiles(ctx, `WHERE product_id = ? AND kind = ? AND gog_id = ?`, f.ProductID, f.Kind, f.GogID)
	if err != nil {
		return UpsertFileResult{}, err
	}
	if len(existing) == 0 {
		res, err := d.ExecContext(ctx, `INSERT INTO files(game_id, product_id, kind, os, language, gog_id, name, version, size, manifest_size, downlink, rel_dir, status, active, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', 1, ?)`,
			f.GameID, f.ProductID, f.Kind, f.OS, f.Language, f.GogID, f.Name, f.Version, f.Size, f.Size, f.Downlink, f.RelDir, now)
		if err != nil {
			return UpsertFileResult{}, err
		}
		id, _ := res.LastInsertId()
		return UpsertFileResult{ID: id, Inserted: true}, nil
	}
	e := existing[0]
	onDisk := e.LocalPath != "" && localExists(e.LocalPath)
	changed := metadataChange(e, f)
	// A copy on disk can be held against GOG's checksum, which is the only
	// authoritative answer; the metadata comparison is a guess that can be wrong
	// in either direction. Nothing else is worth a request: a file that is not
	// downloaded is fetched whatever the answer would be.
	if verify != nil && e.MD5 != "" && ((e.Active && e.Status == StatusDone && e.LocalPath != "") || keptCopy(e, onDisk)) {
		if got, err := verify(ctx, e, f, changed); err == nil {
			changed = got
		}
	}
	u := planUpsert(e, f, changed, onDisk)
	_, err = d.ExecContext(ctx, `UPDATE files SET game_id = ?, os = ?, language = ?, name = ?, version = ?, size = ?, manifest_size = ?, downlink = ?, rel_dir = ?,
		status = ?, active = 1, previous_path = ?, error = CASE WHEN ? = 'pending' AND status <> 'pending' THEN '' ELSE error END,
		attempts = CASE WHEN ? = 'pending' AND status <> 'pending' THEN 0 ELSE attempts END, next_attempt_at = 0, updated_at = ? WHERE id = ?`,
		f.GameID, f.OS, f.Language, f.Name, f.Version, u.size, f.Size, f.Downlink, f.RelDir, u.status, u.prev, u.status, u.status, now, e.ID)
	if err != nil {
		return UpsertFileResult{}, err
	}
	// A failed row keeps its partial file too, so a new build must discard it as well.
	changedWhilePending := e.Active && (e.Status == StatusPending || e.Status == StatusError) && (changed || e.Downlink != f.Downlink)
	return UpsertFileResult{ID: e.ID, Updated: u.updated, Changed: changedWhilePending, LocalPath: e.LocalPath}, nil
}

// metadataChange is what GOG's manifest alone says about whether the file was
// replaced. GOG's manifest sizes are not byte-exact, and the downloader replaces
// size with the real byte count once it knows it, so a size change is only ever
// detected between two manifest sizes. A stored manifest size of 0 is unknown
// (rows from before the column existed, manifests without a size) and never
// counts as a change.
func metadataChange(stored, next File) bool {
	sizeChanged := next.Size > 0 && stored.ManifestSize > 0 && stored.ManifestSize != next.Size
	return stored.Version != next.Version || sizeChanged
}

// reactivated reports whether a settings change is bringing a dropped row back.
func reactivated(stored File) bool { return !stored.Active || stored.Status == StatusInactive }

// keptCopy reports whether such a row still has its own download on disk. A
// remembered previous_path means the copy there is an older build that was
// waiting to be replaced, so it is not this file.
func keptCopy(stored File, onDisk bool) bool {
	return reactivated(stored) && stored.PreviousPath == "" && onDisk
}

// fileUpdate is what an incoming file does to the row that is already stored.
type fileUpdate struct {
	status string
	// prev is the downloaded copy that the next successful download replaces,
	// kept until then so an update never leaves the game without an installer.
	prev string
	// size is the best size to record: see metadataChange on why the stored one
	// can be better than the one GOG just sent.
	size int64
	// updated marks a file that was downloaded and now has to be fetched again.
	updated bool
}

// planUpsert decides what the file GOG offers now (next) means for the row that
// is stored (stored). changed says whether it really is another build — the
// checksum's answer where there is one, GOG's metadata otherwise — and onDisk
// whether the row's local_path is still there. It touches nothing: every input
// is already in hand, so the rules can be read, and tested, on their own.
func planUpsert(stored, next File, changed, onDisk bool) fileUpdate {
	u := fileUpdate{status: stored.Status, prev: stored.PreviousPath, size: next.Size}
	kept := keptCopy(stored, onDisk)
	if reactivated(stored) {
		switch {
		case kept && !changed:
			u.status = StatusDone
		case kept:
			// The kept copy is an older build: replace it once the new one is in place.
			u.status, u.prev, u.updated = StatusPending, stored.LocalPath, true
		default:
			u.status = StatusPending
		}
	}
	if (u.status == StatusDone || u.status == StatusError) && changed {
		// A new build also supersedes a failed attempt at the old one.
		if u.status == StatusDone {
			u.prev = stored.LocalPath
		}
		u.status, u.updated = StatusPending, true
	}
	if u.status == StatusDone && stored.LocalPath != "" && !onDisk {
		u.status = StatusPending
	}
	if !changed && stored.LocalPath != "" && stored.Size > 0 {
		// The transfer already measured the real size; the manifest is an estimate.
		u.size = stored.Size
	}
	return u
}

// DeactivateOtherFiles marks active files of the game not in keepIDs as inactive
// (if a downloaded copy exists: the file itself, or a previous version kept while
// an update was pending) or deletes them (if never downloaded). Returns the
// deactivated files.
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
	for _, f := range files {
		if keep[f.ID] {
			continue
		}
		if (f.Status == StatusDone && f.LocalPath != "") || f.PreviousPath != "" {
			if err := d.SetFileInactive(ctx, f.ID); err != nil {
				return nil, err
			}
		} else if err := d.DeleteFile(ctx, f.ID); err != nil {
			return nil, err
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

// fileDoneSet is the SET clause that marks a file as downloaded. A finished
// transfer has just been held against GOG's checksum, so it counts as verified:
// the rolling re-check belongs to files that have sat on disk.
const fileDoneSet = `SET status = 'done', local_path = ?, size = ?, previous_path = '', error = '', attempts = 0,
	next_attempt_at = 0, downloaded_at = ?, verified_at = ?, updated_at = ?`

// SetFileDone marks a download complete unconditionally. The downloader uses
// CompleteFile instead; this is for callers that own the row already (tests,
// and repairs that do not go through a transfer).
func (d *DB) SetFileDone(ctx context.Context, id int64, localPath string, size int64) error {
	now := time.Now().UnixMilli()
	_, err := d.ExecContext(ctx, `UPDATE files `+fileDoneSet+` WHERE id = ?`, localPath, size, now, now, now, id)
	return err
}

// CompleteFile marks a download complete like SetFileDone, but only if the row
// still wants the download that was made: it is active and describes the same
// download link and version. It reports false when the file was updated on GOG
// in the meantime (the row is then left pending for the new version), or when
// the file stopped being wanted while it transferred (the row was dropped, or
// kept as it was when the user answered "keep": the previous version).
func (d *DB) CompleteFile(ctx context.Context, id int64, localPath string, size int64, downlink, version string) (bool, error) {
	now := time.Now().UnixMilli()
	res, err := d.ExecContext(ctx, `UPDATE files `+fileDoneSet+` WHERE id = ? AND active = 1 AND downlink = ? AND version = ?`,
		localPath, size, now, now, now, id, downlink, version)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetFileVerified records that the local copy was compared against GOG's
// checksum just now, so the rolling re-check moves on to other files.
func (d *DB) SetFileVerified(ctx context.Context, id int64) error {
	_, err := d.ExecContext(ctx, `UPDATE files SET verified_at = ? WHERE id = ?`, time.Now().UnixMilli(), id)
	return err
}

// SetFileError records a failed attempt. If retryAfter is zero the file goes to
// the error state. A row that was dropped or kept as inactive while the
// transfer ran is left as it is: nothing tracks it anymore.
func (d *DB) SetFileError(ctx context.Context, id int64, msg string, retryAfter time.Duration) error {
	now := time.Now()
	if retryAfter > 0 {
		_, err := d.ExecContext(ctx, `UPDATE files SET status = 'pending', error = ?, attempts = attempts + 1, next_attempt_at = ?, updated_at = ? WHERE id = ? AND active = 1`,
			msg, now.Add(retryAfter).UnixMilli(), now.UnixMilli(), id)
		return err
	}
	_, err := d.ExecContext(ctx, `UPDATE files SET status = 'error', error = ?, attempts = attempts + 1, next_attempt_at = 0, updated_at = ? WHERE id = ? AND active = 1`,
		msg, now.UnixMilli(), id)
	return err
}

// DeferFile keeps a pending file queued but not before retryAfter, without
// counting a failed attempt: for failures of the GOG session rather than the file.
func (d *DB) DeferFile(ctx context.Context, id int64, msg string, retryAfter time.Duration) error {
	now := time.Now()
	_, err := d.ExecContext(ctx, `UPDATE files SET status = 'pending', error = ?, next_attempt_at = ?, updated_at = ? WHERE id = ? AND active = 1`,
		msg, now.Add(retryAfter).UnixMilli(), now.UnixMilli(), id)
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

// placeholders returns "?,?,…" for an IN clause of n values.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
