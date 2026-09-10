package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	if st["setup_step"] != "platforms" {
		t.Errorf("step after auth = %v", st["setup_step"])
	}
	_, settings := call(t, srv, "GET", "/api/settings", nil)
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
