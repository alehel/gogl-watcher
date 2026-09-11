// Package server exposes the HTTP API and serves the web UI.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/alehel/gogl-watcher/internal/db"
	"github.com/alehel/gogl-watcher/internal/downloader"
	"github.com/alehel/gogl-watcher/internal/gog"
	"github.com/alehel/gogl-watcher/internal/library"
	"github.com/alehel/gogl-watcher/internal/logstore"
	"github.com/alehel/gogl-watcher/internal/scheduler"
)

// Server wires the application services to HTTP.
type Server struct {
	DB         *db.DB
	GOG        gog.API
	Syncer     *library.Syncer
	Downloads  *downloader.Manager
	Scheduler  *scheduler.Scheduler
	Logs       *logstore.Store
	Paths      library.Paths
	UI         fs.FS
	Version    string
	Log        *slog.Logger
	LibraryDir string
}

// Handler builds the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/auth", s.handleAuthGet)
	mux.HandleFunc("GET /api/auth/url", s.handleAuthURL)
	mux.HandleFunc("POST /api/auth/code", s.handleAuthCode)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("POST /api/setup/complete", s.handleSetupComplete)
	mux.HandleFunc("GET /api/settings", s.handleSettingsGet)
	mux.HandleFunc("PUT /api/settings", s.handleSettingsPut)
	mux.HandleFunc("POST /api/settings/preview", s.handleSettingsPreview)
	mux.HandleFunc("GET /api/settings/languages", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"languages": Languages})
	})
	mux.HandleFunc("GET /api/games", s.handleGames)
	mux.HandleFunc("PUT /api/games/selection", s.handleGamesSelection)
	mux.HandleFunc("GET /api/games/{id}", s.handleGame)
	mux.HandleFunc("POST /api/games/{id}/sync", s.handleGameSync)
	mux.HandleFunc("POST /api/games/{id}/retry", s.handleGameRetry)
	mux.HandleFunc("POST /api/files/{id}/retry", s.handleFileRetry)
	mux.HandleFunc("POST /api/sync", s.handleSync)
	mux.HandleFunc("GET /api/downloads", s.handleDownloads)
	mux.HandleFunc("POST /api/downloads/pause", func(w http.ResponseWriter, r *http.Request) { s.setPaused(w, r, true) })
	mux.HandleFunc("POST /api/downloads/resume", func(w http.ResponseWriter, r *http.Request) { s.setPaused(w, r, false) })
	mux.HandleFunc("GET /api/logs", s.handleLogs)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeError(w, 404, "not found") })
	mux.Handle("/", s.spaHandler())
	return mux
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	return dec.Decode(v)
}

func (s *Server) setupStep(ctx context.Context) (string, bool, db.Settings) {
	settings, _ := s.DB.GetSettings(ctx)
	done, _ := s.DB.SetupComplete(ctx)
	switch {
	case done:
		return "done", true, settings
	case !s.GOG.Authenticated():
		return "auth", false, settings
	case settings.DownloadMode == "":
		return "games", false, settings
	case len(settings.Platforms) == 0:
		return "platforms", false, settings
	case !settings.ContentChosen:
		return "content", false, settings
	default:
		return "done", false, settings
	}
}

// ---- status ----

type gameSummary struct {
	ID           int64           `json:"id"`
	Title        string          `json:"title"`
	Slug         string          `json:"slug"`
	Image        string          `json:"image"`
	Folder       string          `json:"folder"`
	WorksOn      map[string]bool `json:"works_on"`
	Owned        bool            `json:"owned"`
	Selected     bool            `json:"selected"`
	Status       string          `json:"status"`
	FilesTotal   int             `json:"files_total"`
	FilesDone    int             `json:"files_done"`
	BytesTotal   int64           `json:"bytes_total"`
	BytesDone    int64           `json:"bytes_done"`
	Progress     float64         `json:"progress"`
	LastSyncedAt *time.Time      `json:"last_synced_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	DetailsError string          `json:"details_error,omitempty"`
}

func (s *Server) summarize(g db.Game, st db.GameStats, active map[int64][]downloader.Progress, settings db.Settings) gameSummary {
	out := gameSummary{
		ID: g.ID, Title: g.Title, Slug: g.Slug, Image: g.Image, Folder: g.Folder,
		WorksOn: map[string]bool{"windows": g.WorksWindows, "mac": g.WorksMac, "linux": g.WorksLinux},
		Owned:   g.Owned, Selected: g.Selected, FilesTotal: st.FilesTotal, FilesDone: st.FilesDone, BytesTotal: st.BytesTotal, BytesDone: st.BytesDone,
		LastSyncedAt: g.DetailsSyncedAt, UpdatedAt: g.UpdatedAt, DetailsError: g.DetailsError,
	}
	for _, p := range active[g.ID] {
		out.BytesDone += p.Downloaded
	}
	switch {
	case !settings.WantsGame(g):
		out.Status = "unselected"
	case len(active[g.ID]) > 0:
		out.Status = "downloading"
	case st.FilesError > 0:
		out.Status = "error"
	case st.FilesTotal > 0 && st.FilesDone == st.FilesTotal:
		out.Status = "complete"
	case st.FilesDone > 0:
		out.Status = "partial"
	case st.FilesTotal > 0:
		out.Status = "pending"
	case g.DetailsSyncedAt == nil:
		out.Status = "unsynced"
	default:
		out.Status = "unavailable"
	}
	if out.BytesTotal > 0 {
		out.Progress = float64(out.BytesDone) / float64(out.BytesTotal)
		if out.Progress > 1 {
			out.Progress = 1
		}
	} else if out.Status == "complete" {
		out.Progress = 1
	}
	return out
}

func (s *Server) activeByGame() map[int64][]downloader.Progress {
	out := map[int64][]downloader.Progress{}
	for _, p := range s.Downloads.Active() {
		out[p.GameID] = append(out[p.GameID], p)
	}
	return out
}

func (s *Server) allSummaries(ctx context.Context) ([]gameSummary, error) {
	games, err := s.DB.ListGames(ctx)
	if err != nil {
		return nil, err
	}
	stats, err := s.DB.GameStatsAll(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := s.DB.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	active := s.activeByGame()
	out := make([]gameSummary, 0, len(games))
	for _, g := range games {
		if !g.Owned {
			continue
		}
		out = append(out, s.summarize(g, stats[g.ID], active, settings))
	}
	return out, nil
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	step, complete, settings := s.setupStep(ctx)
	sums, err := s.allSummaries(ctx)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	lib := map[string]any{}
	counts := map[string]int{}
	var bytesTotal, bytesDone int64
	for _, g := range sums {
		counts[g.Status]++
		bytesTotal += g.BytesTotal
		bytesDone += g.BytesDone
	}
	lib["games"] = len(sums)
	for _, k := range []string{"complete", "pending", "downloading", "partial", "error", "unavailable", "unsynced", "unselected"} {
		lib[k] = counts[k]
	}
	lib["download_mode"] = settings.DownloadMode
	lib["bytes_total"] = bytesTotal
	lib["bytes_done"] = bytesDone

	active := s.Downloads.Active()
	var speed float64
	for _, p := range active {
		speed += p.SpeedBps
	}
	fileCounts, _ := s.DB.CountFilesByStatus(ctx)
	queued := fileCounts[db.StatusPending] - len(active)
	if queued < 0 {
		queued = 0
	}
	var authErr *string
	if e := s.GOG.AuthError(); e != "" {
		authErr = &e
	}
	free, total := diskUsage(s.LibraryDir)
	writeJSON(w, 200, map[string]any{
		"version":        s.Version,
		"setup_complete": complete,
		"setup_step":     step,
		"authenticated":  s.GOG.Authenticated(),
		"auth_error":     authErr,
		"user":           s.GOG.CurrentUser(),
		"sync":           s.Syncer.Status(),
		"downloads": map[string]any{
			"paused": s.Downloads.Paused(), "active": len(active), "queued": queued, "speed_bps": speed,
		},
		"library": lib,
		"disk":    map[string]any{"library_dir": s.LibraryDir, "free_bytes": free, "total_bytes": total},
	})
}

func diskUsage(dir string) (free, total int64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0
	}
	return int64(st.Bavail) * int64(st.Bsize), int64(st.Blocks) * int64(st.Bsize)
}

// ---- auth ----

func (s *Server) handleAuthGet(w http.ResponseWriter, r *http.Request) {
	a, _ := s.DB.GetAuth(r.Context())
	var exp *time.Time
	if !a.ExpiresAt.IsZero() {
		exp = &a.ExpiresAt
	}
	var authErr *string
	if e := s.GOG.AuthError(); e != "" {
		authErr = &e
	}
	writeJSON(w, 200, map[string]any{"authenticated": s.GOG.Authenticated(), "user": s.GOG.CurrentUser(), "expires_at": exp, "error": authErr})
}

func (s *Server) handleAuthURL(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"url": s.GOG.AuthURL()})
}

func (s *Server) handleAuthCode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := readJSON(w, r, &body); err != nil || strings.TrimSpace(body.Code) == "" {
		writeError(w, 400, "provide the code or the redirect URL")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	user, err := s.GOG.ExchangeCode(ctx, body.Code)
	if err != nil {
		s.Log.Warn("authorization failed", "error", err)
		writeError(w, 400, err.Error())
		return
	}
	if done, _ := s.DB.SetupComplete(ctx); done {
		s.Downloads.Wake()
		s.Scheduler.TriggerNow()
	}
	writeJSON(w, 200, map[string]any{"user": user})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.Syncer.Cancel()
	if err := s.GOG.Logout(r.Context()); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	s.Log.Info("GOG account disconnected")
	w.WriteHeader(204)
}

// ---- setup ----

func (s *Server) handleSetupComplete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	settings, err := s.DB.GetSettings(ctx)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	switch {
	case !s.GOG.Authenticated():
		writeError(w, 409, "authorize with GOG first")
		return
	case settings.DownloadMode == "":
		writeError(w, 409, "choose whether to download all games or only selected ones")
		return
	case len(settings.Platforms) == 0:
		writeError(w, 409, "choose at least one platform")
		return
	case !settings.ContentChosen:
		writeError(w, 409, "choose what content to download")
		return
	}
	if err := s.DB.SetSetupComplete(ctx, true); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	s.Log.Info("setup completed", "mode", settings.DownloadMode, "platforms", strings.Join(settings.Platforms, ","), "dlc", settings.IncludeDLC, "extras", settings.IncludeExtras)
	s.Downloads.Configure(settings, true)
	s.Scheduler.TriggerNow()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- settings ----

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	settings, err := s.DB.GetSettings(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, settings)
}

func (s *Server) handleSettingsPreview(w http.ResponseWriter, r *http.Request) {
	var ns db.Settings
	if err := readJSON(w, r, &ns); err != nil {
		writeError(w, 400, "invalid settings: "+err.Error())
		return
	}
	if err := ns.Normalize(); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	p, err := s.Syncer.PreviewSettings(r.Context(), ns)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, p)
}

func (s *Server) handleSettingsPut(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		Settings  db.Settings `json:"settings"`
		OnRemoved *string     `json:"on_removed"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, 400, "invalid settings: "+err.Error())
		return
	}
	ns := body.Settings
	if err := ns.Normalize(); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	old, _ := s.DB.GetSettings(ctx)
	done, _ := s.DB.SetupComplete(ctx)
	if done && len(ns.Platforms) == 0 {
		writeError(w, 400, "choose at least one platform")
		return
	}
	if done && ns.DownloadMode == "" {
		writeError(w, 400, "choose whether to download all games or only selected ones")
		return
	}
	onRemoved := ""
	if body.OnRemoved != nil {
		onRemoved = *body.OnRemoved
	}
	if err := s.Syncer.ApplySettings(ctx, ns, onRemoved); err != nil {
		var cr *library.ErrConfirmationRequired
		if errors.As(err, &cr) {
			writeJSON(w, 409, map[string]any{"error": "confirmation_required", "needs_confirmation": true,
				"removed": cr.Preview.Removed, "reasons": cr.Preview.Reasons})
			return
		}
		writeError(w, 500, err.Error())
		return
	}
	s.Log.Info("settings saved", "mode", ns.DownloadMode, "platforms", strings.Join(ns.Platforms, ","), "languages", strings.Join(ns.Languages, ","),
		"dlc", ns.IncludeDLC, "extras", ns.IncludeExtras, "concurrent", ns.MaxConcurrentDownloads,
		"speed_limit_kbps", ns.SpeedLimitKBps, "interval_hours", ns.CheckIntervalHours)
	s.Downloads.Configure(ns, done)
	if done && !old.SamePlan(ns) {
		s.Scheduler.TriggerNow()
	}
	// A changed check interval applies from now on, not after the next run.
	s.Scheduler.Reschedule()
	writeJSON(w, 200, ns)
}

// ---- library ----

func (s *Server) handleGames(w http.ResponseWriter, r *http.Request) {
	sums, err := s.allSummaries(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	status := r.URL.Query().Get("status")
	filtered := sums[:0]
	for _, g := range sums {
		if q != "" && !strings.Contains(strings.ToLower(g.Title), q) {
			continue
		}
		if status != "" && status != "all" && g.Status != status {
			continue
		}
		filtered = append(filtered, g)
	}
	switch r.URL.Query().Get("sort") {
	case "status":
		order := map[string]int{"error": 0, "downloading": 1, "partial": 2, "pending": 3, "unsynced": 4, "complete": 5, "unavailable": 6, "unselected": 7}
		sort.SliceStable(filtered, func(i, j int) bool { return order[filtered[i].Status] < order[filtered[j].Status] })
	case "updated":
		sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].UpdatedAt.After(filtered[j].UpdatedAt) })
	}
	writeJSON(w, 200, map[string]any{"games": filtered})
}

type fileInfo struct {
	ID           int64          `json:"id"`
	Kind         string         `json:"kind"`
	OS           string         `json:"os"`
	Language     string         `json:"language"`
	Name         string         `json:"name"`
	Version      string         `json:"version"`
	Size         int64          `json:"size"`
	Filename     *string        `json:"filename"`
	Status       string         `json:"status"`
	Progress     map[string]any `json:"progress"`
	Error        *string        `json:"error"`
	LocalPath    *string        `json:"local_path"`
	MD5          *string        `json:"md5"`
	DownloadedAt *time.Time     `json:"downloaded_at"`
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *Server) gameID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil
}

func (s *Server) handleGame(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := s.gameID(r)
	if !ok {
		writeError(w, 400, "bad id")
		return
	}
	g, err := s.DB.GetGame(ctx, id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if g == nil {
		writeError(w, 404, "game not found")
		return
	}
	stats, _ := s.DB.GameStatsAll(ctx)
	settings, _ := s.DB.GetSettings(ctx)
	active := s.activeByGame()
	summary := s.summarize(*g, stats[id], active, settings)
	products, err := s.DB.ListProducts(ctx, id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	files, err := s.DB.ListFilesByGame(ctx, id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	byProduct := map[int64][]fileInfo{}
	for _, f := range files {
		fi := fileInfo{ID: f.ID, Kind: f.Kind, OS: f.OS, Language: f.Language, Name: f.Name, Version: f.Version, Size: f.Size,
			Filename: nullable(f.Filename), Status: f.Status, Error: nullable(f.Error), MD5: nullable(f.MD5), DownloadedAt: f.DownloadedAt}
		if f.LocalPath != "" {
			p := s.Paths.Abs(f.LocalPath)
			fi.LocalPath = &p
		}
		if p, ok := s.Downloads.ProgressFor(f.ID); ok {
			fi.Status = "downloading"
			fi.Progress = map[string]any{"downloaded_bytes": p.Downloaded, "speed_bps": p.SpeedBps}
			if p.Size > 0 {
				fi.Size = p.Size
			}
			if p.Filename != "" {
				fi.Filename = &p.Filename
			}
		}
		byProduct[f.ProductID] = append(byProduct[f.ProductID], fi)
	}
	type productOut struct {
		ID    int64      `json:"id"`
		Title string     `json:"title"`
		IsDLC bool       `json:"is_dlc"`
		Files []fileInfo `json:"files"`
	}
	out := []productOut{}
	for _, p := range products {
		fs := byProduct[p.ID]
		if fs == nil {
			fs = []fileInfo{}
		}
		out = append(out, productOut{ID: p.ID, Title: p.Title, IsDLC: p.IsDLC, Files: fs})
	}
	writeJSON(w, 200, map[string]any{"game": summary, "products": out})
}

// handleGamesSelection marks games as selected for download or not. Selected games
// are synced in the background so their files get planned and queued.
func (s *Server) handleGamesSelection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		IDs       []int64 `json:"ids"`
		Selected  bool    `json:"selected"`
		OnRemoved *string `json:"on_removed"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, 400, "invalid request: "+err.Error())
		return
	}
	if len(body.IDs) == 0 {
		writeError(w, 400, "provide at least one game id")
		return
	}
	for _, id := range body.IDs {
		g, err := s.DB.GetGame(ctx, id)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		if g == nil {
			writeError(w, 404, fmt.Sprintf("game %d not found", id))
			return
		}
	}
	onRemoved := ""
	if body.OnRemoved != nil {
		onRemoved = *body.OnRemoved
	}
	if err := s.Syncer.SetSelection(ctx, body.IDs, body.Selected, onRemoved); err != nil {
		var cr *library.ErrConfirmationRequired
		if errors.As(err, &cr) {
			writeJSON(w, 409, map[string]any{"error": "confirmation_required", "needs_confirmation": true,
				"removed": cr.Preview.Removed, "reasons": cr.Preview.Reasons})
			return
		}
		writeError(w, 500, err.Error())
		return
	}
	settings, _ := s.DB.GetSettings(ctx)
	done, _ := s.DB.SetupComplete(ctx)
	if body.Selected && done && settings.SelectedOnly() && s.GOG.Authenticated() {
		ids := append([]int64(nil), body.IDs...)
		go func() {
			for _, id := range ids {
				sctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				err := s.Syncer.SyncGame(sctx, id)
				cancel()
				if err != nil {
					s.Log.Warn("could not sync selected game", "game_id", id, "error", err)
				}
			}
			s.Downloads.Wake()
		}()
	}
	writeJSON(w, 200, map[string]any{"ok": true, "selected": body.Selected, "ids": body.IDs})
}

func (s *Server) handleGameSync(w http.ResponseWriter, r *http.Request) {
	id, ok := s.gameID(r)
	if !ok {
		writeError(w, 400, "bad id")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := s.Syncer.SyncGame(ctx, id); err != nil {
		writeError(w, 502, err.Error())
		return
	}
	s.Downloads.Wake()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleGameRetry(w http.ResponseWriter, r *http.Request) {
	id, ok := s.gameID(r)
	if !ok {
		writeError(w, 400, "bad id")
		return
	}
	if err := s.DB.ResetGameErrors(r.Context(), id); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	s.Downloads.Wake()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleFileRetry(w http.ResponseWriter, r *http.Request) {
	id, ok := s.gameID(r)
	if !ok {
		writeError(w, 400, "bad id")
		return
	}
	if err := s.DB.ResetFile(r.Context(), id); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	s.Downloads.Wake()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- sync & downloads ----

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	if !s.GOG.Authenticated() {
		writeError(w, 409, "not authorized with GOG")
		return
	}
	if done, _ := s.DB.SetupComplete(r.Context()); !done {
		writeError(w, 409, "finish setup first")
		return
	}
	if s.Syncer.Status().Running {
		writeError(w, 409, "a sync is already running")
		return
	}
	s.Scheduler.TriggerNow()
	writeJSON(w, 202, map[string]bool{"ok": true})
}

func (s *Server) handleDownloads(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	active := s.Downloads.Active()
	sort.Slice(active, func(i, j int) bool { return active[i].StartedAt.Before(active[j].StartedAt) })
	type activeOut struct {
		FileID     int64   `json:"file_id"`
		GameID     int64   `json:"game_id"`
		GameTitle  string  `json:"game_title"`
		Filename   string  `json:"filename"`
		Size       int64   `json:"size"`
		Downloaded int64   `json:"downloaded_bytes"`
		SpeedBps   float64 `json:"speed_bps"`
		ETASeconds *int64  `json:"eta_seconds"`
	}
	act := []activeOut{}
	activeIDs := map[int64]bool{}
	for _, p := range active {
		activeIDs[p.FileID] = true
		a := activeOut{FileID: p.FileID, GameID: p.GameID, GameTitle: p.GameTitle, Filename: p.Filename, Size: p.Size, Downloaded: p.Downloaded, SpeedBps: p.SpeedBps}
		if p.SpeedBps > 0 && p.Size > p.Downloaded {
			eta := int64(float64(p.Size-p.Downloaded) / p.SpeedBps)
			a.ETASeconds = &eta
		}
		act = append(act, a)
	}
	pending, err := s.DB.ListFilesByStatus(ctx, db.StatusPending, true)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	games, _ := s.DB.ListGames(ctx)
	titles := map[int64]string{}
	for _, g := range games {
		titles[g.ID] = g.Title
	}
	type queueOut struct {
		FileID    int64  `json:"file_id"`
		GameID    int64  `json:"game_id"`
		GameTitle string `json:"game_title"`
		Name      string `json:"name"`
		OS        string `json:"os"`
		Size      int64  `json:"size"`
	}
	queue := []queueOut{}
	total := 0
	for _, f := range pending {
		if activeIDs[f.ID] {
			continue
		}
		total++
		if len(queue) < 50 {
			queue = append(queue, queueOut{FileID: f.ID, GameID: f.GameID, GameTitle: titles[f.GameID], Name: f.Name, OS: f.OS, Size: f.Size})
		}
	}
	writeJSON(w, 200, map[string]any{"paused": s.Downloads.Paused(), "active": act, "queued_total": total, "queue": queue})
}

func (s *Server) setPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	if err := s.Downloads.SetPaused(r.Context(), paused); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"paused": paused})
}

// ---- logs ----

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before_id"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	entries, more, err := s.Logs.Query(r.Context(), q.Get("level"), q.Get("q"), before, limit)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"logs": entries, "has_more": more})
}

// ---- SPA ----

func (s *Server) spaHandler() http.Handler {
	if s.UI == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, "<h1>gogl-watcher</h1><p>The web UI was not built into this binary. Run <code>npm run build</code> in <code>web/</code> and rebuild.</p>")
		})
	}
	files := http.FS(s.UI)
	fileServer := http.FileServer(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if f, err := s.UI.Open(p); err == nil {
			st, serr := f.Stat()
			f.Close()
			if serr == nil && !st.IsDir() {
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "no-cache")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}
