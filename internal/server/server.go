// Package server exposes the HTTP API and serves the web UI.
package server

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
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
		writeJSON(w, http.StatusOK, map[string]any{"languages": Languages})
	})
	mux.HandleFunc("GET /api/games", s.handleGames)
	mux.HandleFunc("PUT /api/games/selection", s.handleGamesSelection)
	mux.HandleFunc("GET /api/games/{id}", s.handleGame)
	mux.HandleFunc("GET /api/games/{id}/offer", s.handleGameOffer)
	mux.HandleFunc("PUT /api/games/{id}/options", s.handleGameOptions)
	mux.HandleFunc("POST /api/games/{id}/sync", s.handleGameSync)
	mux.HandleFunc("POST /api/games/{id}/retry", s.handleGameRetry)
	mux.HandleFunc("POST /api/files/{id}/retry", s.handleFileRetry)
	mux.HandleFunc("POST /api/sync", s.handleSync)
	mux.HandleFunc("GET /api/downloads", s.handleDownloads)
	mux.HandleFunc("POST /api/downloads/pause", func(w http.ResponseWriter, r *http.Request) { s.setPaused(w, r, true) })
	mux.HandleFunc("POST /api/downloads/resume", func(w http.ResponseWriter, r *http.Request) { s.setPaused(w, r, false) })
	mux.HandleFunc("GET /api/logs", s.handleLogs)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeError(w, http.StatusNotFound, "not found") })
	mux.Handle("/", s.spaHandler())
	return sameSiteOnly(mux)
}

// sameSiteOnly rejects state-changing requests that a browser sent from another
// site. The UI has no login of its own, so this is what keeps a malicious page
// (on the internet or the LAN) from driving the API through the user's browser.
// Non-browser clients send neither header and are unaffected.
func sameSiteOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
				if site == "cross-site" {
					writeError(w, http.StatusForbidden, "cross-site requests are not allowed")
					return
				}
			} else if origin := r.Header.Get("Origin"); origin != "" && origin != "null" {
				// Older browsers: fall back to comparing the origin with the host the
				// request was addressed to (also as seen by a reverse proxy).
				u, err := url.Parse(origin)
				if err != nil || (!strings.EqualFold(u.Host, r.Host) && !strings.EqualFold(u.Host, r.Header.Get("X-Forwarded-Host"))) {
					writeError(w, http.StatusForbidden, "cross-site requests are not allowed")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
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

// writeConfirmation answers a settings or selection change that would drop
// downloaded files with the preview of what it would remove, so the UI can ask
// whether to keep or delete them. It reports whether err was that request.
func writeConfirmation(w http.ResponseWriter, err error) bool {
	var cr *library.ErrConfirmationRequired
	if !errors.As(err, &cr) {
		return false
	}
	writeJSON(w, http.StatusConflict, map[string]any{"error": "confirmation_required", "needs_confirmation": true,
		"removed": cr.Preview.Removed, "reasons": cr.Preview.Reasons})
	return true
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	return dec.Decode(v)
}

// unansweredStep reports the first setup question these settings leave open,
// with the message to show when something tries to go on without it. It returns
// ("done", "") when every question is answered. Authorization is asked for
// separately: it is not part of the settings.
func unansweredStep(s db.Settings) (step, message string) {
	switch {
	case s.DownloadMode == "":
		return "games", "choose whether to download all games or only selected ones"
	case len(s.Platforms) == 0:
		return "platforms", "choose at least one platform"
	case !s.ContentChosen:
		return "content", "choose what content to download"
	}
	return "done", ""
}

func (s *Server) setupStep(ctx context.Context) (string, bool, db.Settings) {
	settings, _ := s.DB.GetSettings(ctx)
	done, _ := s.DB.SetupComplete(ctx)
	if done {
		return "done", true, settings
	}
	if !s.GOG.Authenticated() {
		return "auth", false, settings
	}
	step, _ := unansweredStep(settings)
	return step, false, settings
}

// ---- status ----

type gameSummary struct {
	ID       int64           `json:"id"`
	Title    string          `json:"title"`
	Slug     string          `json:"slug"`
	Image    string          `json:"image"`
	Folder   string          `json:"folder"`
	WorksOn  map[string]bool `json:"works_on"`
	Tags     []string        `json:"tags"`
	Owned    bool            `json:"owned"`
	Selected bool            `json:"selected"`
	// IncludeDLC and IncludeExtras are the game's own opt-ins; they matter when
	// the library-wide setting is off.
	IncludeDLC    bool       `json:"include_dlc"`
	IncludeExtras bool       `json:"include_extras"`
	Status        string     `json:"status"`
	FilesTotal    int        `json:"files_total"`
	FilesDone     int        `json:"files_done"`
	BytesTotal    int64      `json:"bytes_total"`
	BytesDone     int64      `json:"bytes_done"`
	Progress      float64    `json:"progress"`
	LastSyncedAt  *time.Time `json:"last_synced_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	DetailsError  string     `json:"details_error,omitempty"`
}

func (s *Server) summarize(g db.Game, st db.GameStats, active map[int64][]downloader.Progress, settings db.Settings) gameSummary {
	out := gameSummary{
		ID: g.ID, Title: g.Title, Slug: g.Slug, Image: g.Cover(), Folder: g.Folder,
		WorksOn: worksOn(g), Tags: g.Tags,
		Owned: g.Owned, Selected: g.Selected, IncludeDLC: g.IncludeDLC, IncludeExtras: g.IncludeExtras, FilesTotal: st.FilesTotal, FilesDone: st.FilesDone, BytesTotal: st.BytesTotal, BytesDone: st.BytesDone,
		LastSyncedAt: g.DetailsSyncedAt, UpdatedAt: g.UpdatedAt, DetailsError: g.DetailsError,
	}
	if out.Tags == nil {
		out.Tags = []string{} // a list, not null, for the UI
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

// worksOn is the game's platform support, keyed the way the UI expects it.
func worksOn(g db.Game) map[string]bool {
	out := make(map[string]bool, len(db.Platforms))
	for _, p := range db.Platforms {
		out[p] = g.WorksOn(p)
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
		writeError(w, http.StatusInternalServerError, err.Error())
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
	lib["include_dlc"] = settings.IncludeDLC
	lib["include_extras"] = settings.IncludeExtras
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
	free, total := library.DiskUsage(s.LibraryDir)
	writeJSON(w, http.StatusOK, map[string]any{
		"version":        s.Version,
		"setup_complete": complete,
		"setup_step":     step,
		"authenticated":  s.GOG.Authenticated(),
		"auth_error":     s.authError(),
		"user":           s.GOG.CurrentUser(),
		"sync":           s.Syncer.Status(),
		"downloads": map[string]any{
			"paused": s.Downloads.Paused(), "active": len(active), "queued": queued, "speed_bps": speed,
		},
		"library": lib,
		"disk":    map[string]any{"library_dir": s.LibraryDir, "free_bytes": free, "total_bytes": total},
	})
}

// ---- auth ----

func (s *Server) handleAuthGet(w http.ResponseWriter, r *http.Request) {
	a, _ := s.DB.GetAuth(r.Context())
	var exp *time.Time
	if !a.ExpiresAt.IsZero() {
		exp = &a.ExpiresAt
	}
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": s.GOG.Authenticated(), "user": s.GOG.CurrentUser(), "expires_at": exp, "error": s.authError()})
}

func (s *Server) handleAuthURL(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"url": s.GOG.AuthURL()})
}

func (s *Server) handleAuthCode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := readJSON(w, r, &body); err != nil || strings.TrimSpace(body.Code) == "" {
		writeError(w, http.StatusBadRequest, "provide the code or the redirect URL")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	user, err := s.GOG.ExchangeCode(ctx, body.Code)
	if err != nil {
		s.Log.Warn("authorization failed", "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if done, _ := s.DB.SetupComplete(ctx); done {
		s.Downloads.Wake()
		s.Scheduler.TriggerNow()
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.Syncer.Cancel()
	if err := s.GOG.Logout(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Log.Info("GOG account disconnected")
	// No sync can run without a session: drop the scheduled next run from the status.
	s.Scheduler.Reschedule()
	w.WriteHeader(http.StatusNoContent)
}

// ---- setup ----

func (s *Server) handleSetupComplete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	settings, err := s.DB.GetSettings(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !s.GOG.Authenticated() {
		writeError(w, http.StatusConflict, "authorize with GOG first")
		return
	}
	if _, msg := unansweredStep(settings); msg != "" {
		writeError(w, http.StatusConflict, msg)
		return
	}
	if err := s.DB.SetSetupComplete(ctx, true); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Log.Info("setup completed", "mode", settings.DownloadMode, "platforms", strings.Join(settings.Platforms, ","), "dlc", settings.IncludeDLC, "extras", settings.IncludeExtras)
	s.Downloads.Configure(settings, true)
	s.Scheduler.TriggerNow()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- settings ----

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	settings, err := s.DB.GetSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handleSettingsPreview(w http.ResponseWriter, r *http.Request) {
	var ns db.Settings
	if err := readJSON(w, r, &ns); err != nil {
		writeError(w, http.StatusBadRequest, "invalid settings: "+err.Error())
		return
	}
	if err := ns.Normalize(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := s.Syncer.PreviewSettings(r.Context(), ns)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleSettingsPut(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		Settings  db.Settings           `json:"settings"`
		OnRemoved library.RemovalAction `json:"on_removed"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid settings: "+err.Error())
		return
	}
	ns := body.Settings
	if err := ns.Normalize(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	old, _ := s.DB.GetSettings(ctx)
	done, _ := s.DB.SetupComplete(ctx)
	// Once the wizard is through, its questions may not be un-answered again.
	if _, msg := unansweredStep(ns); done && msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if err := s.Syncer.ApplySettings(ctx, ns, body.OnRemoved); err != nil {
		if !writeConfirmation(w, err) {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
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
	writeJSON(w, http.StatusOK, ns)
}

// ---- library ----

func (s *Server) handleGames(w http.ResponseWriter, r *http.Request) {
	sums, err := s.allSummaries(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	status := r.URL.Query().Get("status")
	tag := r.URL.Query().Get("tag")
	filtered := sums[:0]
	for _, g := range sums {
		if q != "" && !strings.Contains(strings.ToLower(g.Title), q) {
			continue
		}
		if status != "" && status != "all" && g.Status != status {
			continue
		}
		if tag != "" && !slices.Contains(g.Tags, tag) {
			continue
		}
		filtered = append(filtered, g)
	}
	sortSummaries(filtered, r.URL.Query().Get("sort"), boolParam(r.URL.Query().Get("downloaded_first")))
	tags, err := s.DB.ListTags(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"games": filtered, "tags": tags})
}

// sortSummaries orders the listing in place. The games arrive in title order, so "title"
// (and any unknown value) leaves them alone; the other sorts are stable on top of it. With
// downloadedFirst the games that have files on disk are moved to the front, each group
// keeping the order the chosen sort gave it.
func sortSummaries(games []gameSummary, by string, downloadedFirst bool) {
	switch by {
	case "status":
		order := map[string]int{"error": 0, "downloading": 1, "partial": 2, "pending": 3, "unsynced": 4, "complete": 5, "unavailable": 6, "unselected": 7}
		slices.SortStableFunc(games, func(a, b gameSummary) int { return cmp.Compare(order[a.Status], order[b.Status]) })
	case "updated":
		slices.SortStableFunc(games, func(a, b gameSummary) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	}
	if downloadedFirst {
		slices.SortStableFunc(games, func(a, b gameSummary) int { return btoi(hasDownload(b)) - btoi(hasDownload(a)) })
	}
}

// hasDownload reports whether any wanted file of the game is already on disk.
func hasDownload(g gameSummary) bool { return g.FilesDone > 0 }

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// boolParam reads a query flag written as 1, true or yes.
func boolParam(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes":
		return true
	}
	return false
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

// authError is the last GOG token error, as a JSON null when there is none.
func (s *Server) authError() *string { return nullable(s.GOG.AuthError()) }

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// pathID reads the {id} path value.
func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil
}

func (s *Server) handleGame(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	g, err := s.DB.GetGame(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if g == nil {
		writeError(w, http.StatusNotFound, "game not found")
		return
	}
	stats, _ := s.DB.GameStatsAll(ctx)
	settings, _ := s.DB.GetSettings(ctx)
	active := s.activeByGame()
	summary := s.summarize(*g, stats[id], active, settings)
	products, err := s.DB.ListProducts(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	files, err := s.DB.ListFilesByGame(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
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
	writeJSON(w, http.StatusOK, map[string]any{"game": summary, "products": out})
}

// handleGamesSelection marks games as selected for download or not. Selected games
// are synced in the background so their files get planned and queued.
func (s *Server) handleGamesSelection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		IDs       []int64               `json:"ids"`
		Selected  bool                  `json:"selected"`
		OnRemoved library.RemovalAction `json:"on_removed"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if len(body.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "provide at least one game id")
		return
	}
	for _, id := range body.IDs {
		g, err := s.DB.GetGame(ctx, id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if g == nil {
			writeError(w, http.StatusNotFound, fmt.Sprintf("game %d not found", id))
			return
		}
	}
	if err := s.Syncer.SetSelection(ctx, body.IDs, body.Selected, body.OnRemoved); err != nil {
		if !writeConfirmation(w, err) {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
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
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "selected": body.Selected, "ids": body.IDs})
}

// handleGameOptions opts a game in to or out of DLC and extras on its own. Opting
// in syncs the game right away so the files get planned; opting out may need
// the keep-or-delete answer for downloaded files (409, like a settings change).
func (s *Server) handleGameOptions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	var body struct {
		IncludeDLC    bool                  `json:"include_dlc"`
		IncludeExtras bool                  `json:"include_extras"`
		OnRemoved     library.RemovalAction `json:"on_removed"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	before, err := s.DB.GetGame(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if before == nil {
		writeError(w, http.StatusNotFound, "game not found")
		return
	}
	if err := s.Syncer.SetGameOptions(ctx, id, body.IncludeDLC, body.IncludeExtras, body.OnRemoved); err != nil {
		if !writeConfirmation(w, err) {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	settings, _ := s.DB.GetSettings(ctx)
	done, _ := s.DB.SetupComplete(ctx)
	optedIn := (body.IncludeDLC && !before.IncludeDLC) || (body.IncludeExtras && !before.IncludeExtras)
	if optedIn && done && settings.WantsGame(*before) && s.GOG.Authenticated() {
		go func() {
			sctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := s.Syncer.SyncGame(sctx, id); err != nil {
				s.Log.Warn("could not sync game after changing its options", "game_id", id, "error", err)
			}
			s.Downloads.Wake()
		}()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "include_dlc": body.IncludeDLC, "include_extras": body.IncludeExtras})
}

// handleGameOffer answers what GOG offers for a game without planning any of
// it, so the page of a game that is not selected can show what selecting it
// would download.
func (s *Server) handleGameOffer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	offer, err := s.Syncer.Offer(ctx, id)
	switch {
	case errors.Is(err, library.ErrGameNotFound):
		writeError(w, http.StatusNotFound, "game not found")
		return
	case err != nil:
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, offer)
}

func (s *Server) handleGameSync(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := s.Syncer.SyncGame(ctx, id); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.Downloads.Wake()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleGameRetry(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := s.DB.ResetGameErrors(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Downloads.Wake()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleFileRetry(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := s.DB.ResetFile(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Downloads.Wake()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- sync & downloads ----

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	if !s.GOG.Authenticated() {
		writeError(w, http.StatusConflict, "not authorized with GOG")
		return
	}
	if done, _ := s.DB.SetupComplete(r.Context()); !done {
		writeError(w, http.StatusConflict, "finish setup first")
		return
	}
	if s.Syncer.Status().Running {
		writeError(w, http.StatusConflict, "a sync is already running")
		return
	}
	s.Scheduler.TriggerNow()
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (s *Server) handleDownloads(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	active := s.Downloads.Active()
	slices.SortFunc(active, func(a, b downloader.Progress) int { return a.StartedAt.Compare(b.StartedAt) })
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
		writeError(w, http.StatusInternalServerError, err.Error())
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
	writeJSON(w, http.StatusOK, map[string]any{"paused": s.Downloads.Paused(), "active": act, "queued_total": total, "queue": queue})
}

func (s *Server) setPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	if err := s.Downloads.SetPaused(r.Context(), paused); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"paused": paused})
}

// ---- logs ----

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before_id"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	entries, more, err := s.Logs.Query(r.Context(), q.Get("level"), q.Get("q"), before, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": entries, "has_more": more})
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
