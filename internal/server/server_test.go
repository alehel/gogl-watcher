package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/alehel/gogl-watcher/internal/db"
	"github.com/alehel/gogl-watcher/internal/downloader"
	"github.com/alehel/gogl-watcher/internal/gog"
	"github.com/alehel/gogl-watcher/internal/library"
	"github.com/alehel/gogl-watcher/internal/logstore"
	"github.com/alehel/gogl-watcher/internal/scheduler"
)

type tokenStore struct{ d *db.DB }

func (s tokenStore) Load(ctx context.Context) (gog.Token, error) {
	a, err := s.d.GetAuth(ctx)
	return gog.Token{RefreshToken: a.RefreshToken, Username: a.Username, UserID: a.UserID}, err
}
func (s tokenStore) Save(ctx context.Context, t gog.Token) error {
	return s.d.SaveAuth(ctx, db.Auth{RefreshToken: t.RefreshToken, Username: t.Username, UserID: t.UserID})
}
func (s tokenStore) Clear(ctx context.Context) error { return s.d.ClearAuth(ctx) }

func newTestServer(t *testing.T) (*httptest.Server, *db.DB) {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	m, _ := gog.NewMock(context.Background(), tokenStore{d})
	m.Speed = 0
	logs := logstore.NewStore(d.DB, 1000)
	t.Cleanup(logs.Close)
	log := slog.New(logstore.NewHandler(slog.NewTextHandler(bytes.NewBuffer(nil), nil), logs, slog.LevelDebug))
	paths := library.Paths{Root: filepath.Join(dir, "lib")}
	syncer := library.NewSyncer(d, m, paths, log)
	dl := downloader.New(d, m, paths, log)
	syncer.OnChange = dl.Wake
	syncer.OnDrop = dl.Cancel
	sched := scheduler.New(d, m, syncer, log, 0)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go sched.Run(ctx)
	s := &Server{DB: d, GOG: m, Syncer: syncer, Downloads: dl, Scheduler: sched, Logs: logs, Paths: paths, Version: "test", Log: log, LibraryDir: paths.Root}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, d
}

func call(t *testing.T, srv *httptest.Server, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, srv.URL+path, &buf)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestSetupFlowAndSettings(t *testing.T) {
	srv, _ := newTestServer(t)

	code, st := call(t, srv, "GET", "/api/status", nil)
	if code != 200 || st["setup_complete"] != false || st["setup_step"] != "auth" {
		t.Fatalf("initial status: %d %v", code, st)
	}
	if code, _ := call(t, srv, "POST", "/api/setup/complete", nil); code != 409 {
		t.Errorf("setup should be refused before auth, got %d", code)
	}
	if code, out := call(t, srv, "POST", "/api/auth/code", map[string]string{"code": "bad"}); code != 400 || out["error"] == nil {
		t.Errorf("bad code: %d %v", code, out)
	}
	code, out := call(t, srv, "POST", "/api/auth/code", map[string]string{"code": "https://embed.gog.com/on_login_success?origin=client&code=abc"})
	if code != 200 || out["user"].(map[string]any)["username"] != "demo_user" {
		t.Fatalf("auth: %d %v", code, out)
	}
	_, st = call(t, srv, "GET", "/api/status", nil)
	if st["setup_step"] != "games" {
		t.Errorf("step after auth = %v", st["setup_step"])
	}
	_, settings := call(t, srv, "GET", "/api/settings", nil)
	if settings["download_mode"] != "" {
		t.Errorf("download mode should be unset before the wizard asked: %v", settings["download_mode"])
	}
	settings["download_mode"] = "bogus"
	if code, _ := call(t, srv, "PUT", "/api/settings", map[string]any{"settings": settings}); code != 400 {
		t.Errorf("bogus download mode should be rejected, got %d", code)
	}
	settings["download_mode"] = "all"
	if code, out := call(t, srv, "PUT", "/api/settings", map[string]any{"settings": settings}); code != 200 {
		t.Fatalf("save download mode: %d %v", code, out)
	}
	_, st = call(t, srv, "GET", "/api/status", nil)
	if st["setup_step"] != "platforms" {
		t.Errorf("step after download mode = %v", st["setup_step"])
	}
	settings["platforms"] = []string{"windows", "linux"}
	if code, out := call(t, srv, "PUT", "/api/settings", map[string]any{"settings": settings, "on_removed": nil}); code != 200 {
		t.Fatalf("save platforms: %d %v", code, out)
	}
	_, st = call(t, srv, "GET", "/api/status", nil)
	if st["setup_step"] != "content" {
		t.Errorf("step after platforms = %v", st["setup_step"])
	}
	settings["content_chosen"] = true
	settings["include_extras"] = true
	if code, out := call(t, srv, "PUT", "/api/settings", map[string]any{"settings": settings}); code != 200 {
		t.Fatalf("save content: %d %v", code, out)
	}
	if code, out := call(t, srv, "POST", "/api/setup/complete", nil); code != 200 {
		t.Fatalf("complete: %d %v", code, out)
	}
	// The scheduler was triggered; wait for the sync to populate games.
	deadline := time.Now().Add(10 * time.Second)
	var games []any
	for time.Now().Before(deadline) {
		_, out := call(t, srv, "GET", "/api/games", nil)
		games, _ = out["games"].([]any)
		if len(games) >= 10 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(games) < 10 {
		t.Fatalf("expected the sample library, got %d games", len(games))
	}
	waitSyncIdle(t, srv)
	_, out = call(t, srv, "GET", "/api/games", nil)
	games, _ = out["games"].([]any)
	_, st = call(t, srv, "GET", "/api/status", nil)
	if st["setup_complete"] != true || st["setup_step"] != "done" {
		t.Errorf("final status: %v", st)
	}

	// Game detail lists products and files.
	code, detail := call(t, srv, "GET", "/api/games/1207666633", nil)
	prods := detail["products"].([]any)
	if code != 200 || len(prods) != 2 {
		t.Fatalf("detail: %d %v", code, detail)
	}
	base := prods[0].(map[string]any)
	if base["is_dlc"] != false || len(base["files"].([]any)) != 6 { // 4 installer parts + 2 extras
		t.Errorf("base product files: %v", base)
	}

	// Removing a platform with nothing downloaded needs no confirmation.
	settings["platforms"] = []string{"windows"}
	code, prev := call(t, srv, "POST", "/api/settings/preview", settings)
	if code != 200 || prev["needs_confirmation"] != false {
		t.Errorf("preview: %d %v", code, prev)
	}
	removed := prev["removed"].(map[string]any)
	if removed["files"].(float64) == 0 {
		t.Errorf("expected linux files to be listed for removal: %v", prev)
	}

	// Languages endpoint and logs endpoint respond.
	if code, out := call(t, srv, "GET", "/api/settings/languages", nil); code != 200 || len(out["languages"].([]any)) == 0 {
		t.Errorf("languages: %d %v", code, out)
	}
	if code, out := call(t, srv, "GET", "/api/logs?level=info", nil); code != 200 || len(out["logs"].([]any)) == 0 {
		t.Errorf("logs: %d %v", code, out)
	}
	// Filter by status works.
	_, out = call(t, srv, "GET", "/api/games?status=pending&q=witcher", nil)
	if n := len(out["games"].([]any)); n != 1 {
		t.Errorf("filtered games = %d, want 1", n)
	}
	// The user's gog.com tags come with the listing and filter it.
	if tags := out["tags"].([]any); len(tags) != 3 {
		t.Errorf("tags = %v, want the three of the sample library", tags)
	}
	_, out = call(t, srv, "GET", "/api/games?tag=Favorite", nil)
	if n := len(out["games"].([]any)); n != 2 {
		t.Errorf("games tagged Favorite = %d, want 2", n)
	}
	// Pause/resume.
	if code, out := call(t, srv, "POST", "/api/downloads/pause", nil); code != 200 || out["paused"] != true {
		t.Errorf("pause: %d %v", code, out)
	}
	_, dls := call(t, srv, "GET", "/api/downloads", nil)
	if dls["paused"] != true {
		t.Errorf("downloads should report paused: %v", dls)
	}
	// Logout clears auth and the UI must go back to the auth step.
	if code, _ := call(t, srv, "POST", "/api/auth/logout", nil); code != 204 {
		t.Errorf("logout: %d", code)
	}
	_, st = call(t, srv, "GET", "/api/status", nil)
	if st["authenticated"] != false {
		t.Errorf("should be logged out: %v", st)
	}
}

// waitGames polls until at least n games are listed.
func waitGames(t *testing.T, srv *httptest.Server, n int) []any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var games []any
	for time.Now().Before(deadline) {
		_, out := call(t, srv, "GET", "/api/games", nil)
		games, _ = out["games"].([]any)
		if len(games) >= n {
			return games
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("expected %d games, got %d", n, len(games))
	return nil
}

func waitSyncIdle(t *testing.T, srv *httptest.Server) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, st := call(t, srv, "GET", "/api/status", nil)
		sync := st["sync"].(map[string]any)
		if sync["running"] == false && sync["last_finished_at"] != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("sync did not finish")
}

func gameByID(t *testing.T, srv *httptest.Server, id string) map[string]any {
	t.Helper()
	code, detail := call(t, srv, "GET", "/api/games/"+id, nil)
	if code != 200 {
		t.Fatalf("game %s: %d %v", id, code, detail)
	}
	return detail
}

func TestSummarizePendingFilesBeforeDetailsSynced(t *testing.T) {
	s := &Server{}
	settings := db.DefaultSettings()
	settings.DownloadMode = db.DownloadSelected
	game := db.Game{ID: 1, Title: "The Witcher", Selected: true}
	stats := db.GameStats{FilesTotal: 3, BytesTotal: 1024}

	got := s.summarize(game, stats, map[int64][]downloader.Progress{}, settings)
	if got.Status != "pending" {
		t.Fatalf("status = %q, want pending", got.Status)
	}
}

func TestSelectedModeDownloadsOnlySelectedGames(t *testing.T) {
	srv, d := newTestServer(t)
	ctx := context.Background()

	if code, _ := call(t, srv, "POST", "/api/auth/code", map[string]string{"code": "abc"}); code != 200 {
		t.Fatal("auth failed")
	}
	_, settings := call(t, srv, "GET", "/api/settings", nil)
	settings["download_mode"] = "selected"
	settings["platforms"] = []string{"windows"}
	settings["content_chosen"] = true
	if code, out := call(t, srv, "PUT", "/api/settings", map[string]any{"settings": settings}); code != 200 {
		t.Fatalf("save settings: %d %v", code, out)
	}
	if code, out := call(t, srv, "POST", "/api/setup/complete", nil); code != 200 {
		t.Fatalf("complete: %d %v", code, out)
	}
	games := waitGames(t, srv, 10)
	waitSyncIdle(t, srv)
	_, out := call(t, srv, "GET", "/api/games", nil)
	games = out["games"].([]any)

	// Nothing is selected by default, so nothing is planned or queued.
	for _, g := range games {
		gm := g.(map[string]any)
		if gm["status"] != "unselected" || gm["selected"] != false || gm["files_total"].(float64) != 0 {
			t.Errorf("game should be unselected with no files: %v", gm)
		}
	}
	_, st := call(t, srv, "GET", "/api/status", nil)
	lib := st["library"].(map[string]any)
	if lib["download_mode"] != "selected" || lib["unselected"].(float64) != float64(len(games)) {
		t.Errorf("status library: %v", lib)
	}
	if _, dls := call(t, srv, "GET", "/api/downloads", nil); dls["queued_total"].(float64) != 0 {
		t.Errorf("nothing should be queued: %v", dls)
	}
	// The status filter knows the new state.
	if _, out := call(t, srv, "GET", "/api/games?status=unselected", nil); len(out["games"].([]any)) != len(games) {
		t.Errorf("filter by unselected: %v", out)
	}

	// Selecting a game fetches its details and queues its files.
	const witcher = "1207658924"
	if code, out := call(t, srv, "PUT", "/api/games/selection", map[string]any{"ids": []int64{1207658924}, "selected": true}); code != 200 {
		t.Fatalf("select: %d %v", code, out)
	}
	deadline := time.Now().Add(10 * time.Second)
	var detail map[string]any
	for time.Now().Before(deadline) {
		detail = gameByID(t, srv, witcher)
		// Wait for the whole plan, not for its first file: the sync inserts the
		// rows one at a time and every count in between is a half-written plan.
		if detail["game"].(map[string]any)["files_total"].(float64) >= 3 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	game := detail["game"].(map[string]any)
	if game["selected"] != true || game["status"] != "pending" || game["files_total"].(float64) != 3 {
		t.Fatalf("selected game should have its windows/en files pending: %v", game)
	}
	if _, dls := call(t, srv, "GET", "/api/downloads", nil); dls["queued_total"].(float64) != 3 {
		t.Errorf("queue should hold the selected game's files: %v", dls)
	}
	// Other games stay untouched.
	if g := gameByID(t, srv, "1207664663")["game"].(map[string]any); g["status"] != "unselected" {
		t.Errorf("unselected game changed: %v", g)
	}

	// Deselecting with nothing downloaded needs no confirmation and forgets the files.
	if code, out := call(t, srv, "PUT", "/api/games/selection", map[string]any{"ids": []int64{1207658924}, "selected": false}); code != 200 {
		t.Fatalf("deselect: %d %v", code, out)
	}
	game = gameByID(t, srv, witcher)["game"].(map[string]any)
	if game["status"] != "unselected" || game["files_total"].(float64) != 0 {
		t.Errorf("deselected game should have no files: %v", game)
	}

	// Select again, pretend one file finished, then deselecting asks what to do with it.
	if code, _ := call(t, srv, "PUT", "/api/games/selection", map[string]any{"ids": []int64{1207658924}, "selected": true}); code != 200 {
		t.Fatal("re-select failed")
	}
	var files []db.File
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		files, _ = d.ListActiveFilesByGame(ctx, 1207658924)
		if len(files) == 3 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(files) != 3 {
		t.Fatalf("files after re-select = %d", len(files))
	}
	if err := d.SetFileDone(ctx, files[0].ID, "The Witcher/windows/setup.exe", files[0].Size); err != nil {
		t.Fatal(err)
	}
	code, out := call(t, srv, "PUT", "/api/games/selection", map[string]any{"ids": []int64{1207658924}, "selected": false})
	if code != 409 || out["error"] != "confirmation_required" {
		t.Fatalf("deselect with a downloaded file should ask: %d %v", code, out)
	}
	if reasons := out["reasons"].([]any); len(reasons) != 1 || reasons[0] != "unselected" {
		t.Errorf("reasons = %v", reasons)
	}
	if removed := out["removed"].(map[string]any); removed["downloaded_files"].(float64) != 1 || removed["files"].(float64) != 3 {
		t.Errorf("removed = %v", removed)
	}
	if g := gameByID(t, srv, witcher)["game"].(map[string]any); g["selected"] != true {
		t.Errorf("refused deselect must not change the selection: %v", g)
	}
	if code, out := call(t, srv, "PUT", "/api/games/selection", map[string]any{"ids": []int64{1207658924}, "selected": false, "on_removed": "keep"}); code != 200 {
		t.Fatalf("deselect keep: %d %v", code, out)
	}
	kept, _ := d.GetFile(ctx, files[0].ID)
	if kept == nil || kept.Active || kept.Status != db.StatusInactive {
		t.Errorf("kept file should be inactive: %+v", kept)
	}
	if rest, _ := d.ListActiveFilesByGame(ctx, 1207658924); len(rest) != 0 {
		t.Errorf("pending files should be forgotten: %d left", len(rest))
	}
	if code, _ := call(t, srv, "PUT", "/api/games/selection", map[string]any{"ids": []int64{999}, "selected": true}); code != 404 {
		t.Errorf("unknown game should be 404, got %d", code)
	}

	// Switching to "all" needs no confirmation; the next sync plans everything.
	_, settings = call(t, srv, "GET", "/api/settings", nil)
	settings["download_mode"] = "all"
	if code, out := call(t, srv, "PUT", "/api/settings", map[string]any{"settings": settings}); code != 200 {
		t.Fatalf("switch to all: %d %v", code, out)
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, out := call(t, srv, "GET", "/api/games?status=unselected", nil)
		if len(out["games"].([]any)) == 0 {
			if g := gameByID(t, srv, "1207664663")["game"].(map[string]any); g["files_total"].(float64) > 0 {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if g := gameByID(t, srv, "1207664663")["game"].(map[string]any); g["status"] != "pending" {
		t.Errorf("in all mode every game gets planned: %v", g)
	}
	// And back to "selected": the previously pending files are dropped, downloaded
	// ones would need confirmation.
	waitSyncIdle(t, srv)
	_, prev := call(t, srv, "POST", "/api/settings/preview", func() map[string]any { settings["download_mode"] = "selected"; return settings }())
	if prev["needs_confirmation"] != false || prev["removed"].(map[string]any)["files"].(float64) == 0 {
		t.Errorf("preview of switching back: %v", prev)
	}
	if reasons := prev["reasons"].([]any); len(reasons) != 1 || reasons[0] != "unselected" {
		t.Errorf("preview reasons = %v", reasons)
	}
}

func TestExistingInstallKeepsDownloadingEverything(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s := db.DefaultSettings()
	s.Platforms = []string{"windows"}
	s.ContentChosen = true
	_ = d.SaveSettings(ctx, s)
	_ = d.SetSetupComplete(ctx, true)
	d.Close()

	d, err = db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	got, _ := d.GetSettings(ctx)
	if got.DownloadMode != db.DownloadAll {
		t.Errorf("finished setups without a mode must default to all, got %q", got.DownloadMode)
	}
}

// Saving a new check interval must be reflected in the scheduled next run at once.
func TestIntervalChangeReschedulesNextRun(t *testing.T) {
	srv, _ := newTestServer(t)
	if code, _ := call(t, srv, "POST", "/api/auth/code", map[string]string{"code": "abc"}); code != 200 {
		t.Fatal("auth failed")
	}
	_, settings := call(t, srv, "GET", "/api/settings", nil)
	settings["download_mode"] = "selected"
	settings["platforms"] = []string{"windows"}
	settings["content_chosen"] = true
	if code, out := call(t, srv, "PUT", "/api/settings", map[string]any{"settings": settings}); code != 200 {
		t.Fatalf("save settings: %d %v", code, out)
	}
	if code, out := call(t, srv, "POST", "/api/setup/complete", nil); code != 200 {
		t.Fatalf("complete: %d %v", code, out)
	}
	waitSyncIdle(t, srv)
	nextRunIn := func() time.Duration {
		_, st := call(t, srv, "GET", "/api/status", nil)
		next, _ := st["sync"].(map[string]any)["next_run_at"].(string)
		if next == "" {
			return 0
		}
		ts, err := time.Parse(time.RFC3339Nano, next)
		if err != nil {
			t.Fatal(err)
		}
		return time.Until(ts)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && nextRunIn() < 5*time.Hour {
		time.Sleep(50 * time.Millisecond)
	}
	if d := nextRunIn(); d < 5*time.Hour {
		t.Fatalf("expected the next run about 6 h away, got %v", d)
	}
	settings["check_interval_hours"] = 1
	if code, out := call(t, srv, "PUT", "/api/settings", map[string]any{"settings": settings}); code != 200 {
		t.Fatalf("save interval: %d %v", code, out)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && nextRunIn() > 2*time.Hour {
		time.Sleep(50 * time.Millisecond)
	}
	if d := nextRunIn(); d <= 0 || d > 2*time.Hour {
		t.Fatalf("next run should follow the new 1 h interval, got %v", d)
	}
}

// State-changing requests from another site are refused; same-site and
// non-browser requests go through.
func TestCrossSiteWritesAreRefused(t *testing.T) {
	srv, _ := newTestServer(t)
	do := func(headers map[string]string) int {
		t.Helper()
		req, _ := http.NewRequest("POST", srv.URL+"/api/downloads/pause", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	host := srv.Listener.Addr().String()
	if code := do(map[string]string{"Origin": "https://evil.example", "Content-Type": "text/plain"}); code != 403 {
		t.Errorf("cross-origin POST: %d, want 403", code)
	}
	if code := do(map[string]string{"Sec-Fetch-Site": "cross-site"}); code != 403 {
		t.Errorf("Sec-Fetch-Site cross-site: %d, want 403", code)
	}
	if code := do(map[string]string{"Origin": "http://" + host, "Sec-Fetch-Site": "same-origin"}); code != 200 {
		t.Errorf("same-origin POST: %d, want 200", code)
	}
	if code := do(map[string]string{"Origin": "http://" + host}); code != 200 {
		t.Errorf("same-host POST without Sec-Fetch-Site: %d, want 200", code)
	}
	if code := do(nil); code != 200 {
		t.Errorf("non-browser POST: %d, want 200", code)
	}
	// Behind a reverse proxy that rewrites Host (nginx without proxy_set_header
	// Host) the browser's own verdict is what counts; a same-site request from
	// another host still has to match.
	if code := do(map[string]string{"Origin": "https://gogl.example.com", "Sec-Fetch-Site": "same-origin"}); code != 200 {
		t.Errorf("same-origin POST through a Host-rewriting proxy: %d, want 200", code)
	}
	if code := do(map[string]string{"Origin": "https://gogl.example.com", "Sec-Fetch-Site": "same-site"}); code != 403 {
		t.Errorf("same-site POST from another host: %d, want 403", code)
	}
	// An opaque origin (a sandboxed frame, a data: page) is not the UI.
	if code := do(map[string]string{"Origin": "null"}); code != 403 {
		t.Errorf("POST with a null origin: %d, want 403", code)
	}
	// GET is never blocked.
	req, _ := http.NewRequest("GET", srv.URL+"/api/status", nil)
	req.Header.Set("Origin", "https://evil.example")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("GET with foreign origin: %d", resp.StatusCode)
	}
}

// Disconnecting must not leave a scheduled next run in the status.
func TestLogoutClearsNextRun(t *testing.T) {
	srv, _ := newTestServer(t)
	if code, _ := call(t, srv, "POST", "/api/auth/code", map[string]string{"code": "abc"}); code != 200 {
		t.Fatal("auth failed")
	}
	_, settings := call(t, srv, "GET", "/api/settings", nil)
	settings["download_mode"] = "selected"
	settings["platforms"] = []string{"windows"}
	settings["content_chosen"] = true
	call(t, srv, "PUT", "/api/settings", map[string]any{"settings": settings})
	if code, _ := call(t, srv, "POST", "/api/setup/complete", nil); code != 200 {
		t.Fatal("complete failed")
	}
	waitSyncIdle(t, srv)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, st := call(t, srv, "GET", "/api/status", nil)
		if st["sync"].(map[string]any)["next_run_at"] != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code, _ := call(t, srv, "POST", "/api/auth/logout", nil); code != 204 {
		t.Fatal("logout failed")
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, st := call(t, srv, "GET", "/api/status", nil)
		if st["sync"].(map[string]any)["next_run_at"] == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("next_run_at still set after logout")
}

func TestSortSummariesDownloadedFirst(t *testing.T) {
	now := time.Now()
	games := []gameSummary{
		{Title: "Alpha", Status: "pending", FilesDone: 0, UpdatedAt: now.Add(-3 * time.Hour)},
		{Title: "Bravo", Status: "complete", FilesDone: 4, UpdatedAt: now.Add(-2 * time.Hour)},
		{Title: "Charlie", Status: "error", FilesDone: 0, UpdatedAt: now.Add(-1 * time.Hour)},
		{Title: "Delta", Status: "partial", FilesDone: 1, UpdatedAt: now},
	}
	titles := func(in []gameSummary) []string {
		out := make([]string, len(in))
		for i, g := range in {
			out[i] = g.Title
		}
		return out
	}
	check := func(name string, by string, downloadedFirst bool, want ...string) {
		t.Helper()
		list := append([]gameSummary(nil), games...)
		sortSummaries(list, by, downloadedFirst)
		if got := titles(list); !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	// Without the toggle nothing changes about the existing sorts.
	check("title", "title", false, "Alpha", "Bravo", "Charlie", "Delta")
	check("status", "status", false, "Charlie", "Delta", "Alpha", "Bravo")
	check("updated", "updated", false, "Delta", "Charlie", "Bravo", "Alpha")
	// With it, downloaded games lead and the chosen sort orders each group.
	check("title, downloaded first", "title", true, "Bravo", "Delta", "Alpha", "Charlie")
	check("status, downloaded first", "status", true, "Delta", "Bravo", "Charlie", "Alpha")
	check("updated, downloaded first", "updated", true, "Delta", "Bravo", "Charlie", "Alpha")
}

// Without base game installers or DLC, no platform is needed: a saves-only
// or extras-only backup does not have to pick one.
func TestPlatformsOnlyNeededForInstallers(t *testing.T) {
	srv, _ := newTestServer(t)
	if code, _ := call(t, srv, "POST", "/api/auth/code", map[string]string{"code": "abc"}); code != 200 {
		t.Fatal("auth failed")
	}
	_, settings := call(t, srv, "GET", "/api/settings", nil)
	if settings["include_installers"] != true {
		t.Fatalf("installers should be on by default: %v", settings)
	}
	settings["download_mode"] = "all"
	settings["platforms"] = []string{}
	settings["content_chosen"] = true
	settings["include_installers"] = false
	settings["include_dlc"] = false
	settings["include_saves"] = true
	if code, out := call(t, srv, "PUT", "/api/settings", map[string]any{"settings": settings}); code != 200 {
		t.Fatalf("save settings: %d %v", code, out)
	}
	if _, st := call(t, srv, "GET", "/api/status", nil); st["setup_step"] != "done" {
		t.Errorf("setup should have nothing left to ask, got %v", st["setup_step"])
	}
	if code, out := call(t, srv, "POST", "/api/setup/complete", nil); code != 200 {
		t.Fatalf("complete: %d %v", code, out)
	}
	waitGames(t, srv, 10)
	waitSyncIdle(t, srv)
	// Only saves get planned: Stardew Valley has them, The Witcher has nothing.
	for _, p := range gameByID(t, srv, "1207664663")["products"].([]any) {
		for _, f := range p.(map[string]any)["files"].([]any) {
			if f.(map[string]any)["kind"] != "save" {
				t.Errorf("planned something other than a save: %v", f)
			}
		}
	}
	if g := gameByID(t, srv, "1207658924")["game"].(map[string]any); g["files_total"].(float64) != 0 || g["status"] != "unavailable" {
		t.Errorf("a game without saves should have nothing planned: %v", g)
	}
	// With installers back on, a platform is required again.
	settings["include_installers"] = true
	if code, out := call(t, srv, "PUT", "/api/settings", map[string]any{"settings": settings}); code != 400 {
		t.Fatalf("installers without a platform should be refused: %d %v", code, out)
	}
}

// A game opts in to cloud saves on its own when the library-wide setting is
// off; the status and the game's summary say so, and its saves get planned.
func TestGameOptsIntoCloudSaves(t *testing.T) {
	srv, _ := newTestServer(t)
	if code, _ := call(t, srv, "POST", "/api/auth/code", map[string]string{"code": "abc"}); code != 200 {
		t.Fatal("auth failed")
	}
	_, settings := call(t, srv, "GET", "/api/settings", nil)
	if settings["include_saves"] != false {
		t.Fatalf("saves should be off by default: %v", settings)
	}
	settings["download_mode"] = "all"
	settings["platforms"] = []string{"windows"}
	settings["content_chosen"] = true
	if code, out := call(t, srv, "PUT", "/api/settings", map[string]any{"settings": settings}); code != 200 {
		t.Fatalf("save settings: %d %v", code, out)
	}
	if code, out := call(t, srv, "POST", "/api/setup/complete", nil); code != 200 {
		t.Fatalf("complete: %d %v", code, out)
	}
	waitGames(t, srv, 10)
	waitSyncIdle(t, srv)
	_, st := call(t, srv, "GET", "/api/status", nil)
	if st["library"].(map[string]any)["include_saves"] != false {
		t.Errorf("status should carry the saves setting: %v", st["library"])
	}
	const stardew = "1207664663"
	countSaves := func() int {
		n := 0
		for _, p := range gameByID(t, srv, stardew)["products"].([]any) {
			for _, f := range p.(map[string]any)["files"].([]any) {
				if f.(map[string]any)["kind"] == "save" {
					n++
				}
			}
		}
		return n
	}
	if n := countSaves(); n != 0 {
		t.Fatalf("%d saves tracked with saves off", n)
	}
	code, out := call(t, srv, "PUT", "/api/games/"+stardew+"/options", map[string]any{"include_saves": true})
	if code != 200 || out["include_saves"] != true {
		t.Fatalf("options: %d %v", code, out)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && countSaves() < 3 {
		time.Sleep(100 * time.Millisecond)
	}
	if n := countSaves(); n != 3 {
		t.Fatalf("want 3 saves planned after opting in, got %d", n)
	}
	if g := gameByID(t, srv, stardew)["game"].(map[string]any); g["include_saves"] != true {
		t.Errorf("summary should carry the opt-in: %v", g)
	}
}
