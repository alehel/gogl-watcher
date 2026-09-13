package gog

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type memStore struct{ t Token }

func (m *memStore) Load(context.Context) (Token, error)   { return m.t, nil }
func (m *memStore) Save(_ context.Context, t Token) error { m.t = t; return nil }
func (m *memStore) Clear(context.Context) error           { m.t = Token{}; return nil }

// newTestClient points a Client at an httptest server that fakes auth, embed and api hosts.
func newTestClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	srv := httptest.NewServer(handler)
	c, err := NewClient(context.Background(), &memStore{}, slog.Default(), "test")
	if err != nil {
		t.Fatal(err)
	}
	// Route every host to the test server.
	c.http.Transport = rewriteTransport{base: srv.URL}
	c.dl.Transport = rewriteTransport{base: srv.URL}
	return c, srv
}

type rewriteTransport struct{ base string }

func (rt rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	u := rt.base + "/" + r.URL.Host + r.URL.Path
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery
	}
	nr, err := http.NewRequestWithContext(r.Context(), r.Method, u, r.Body)
	if err != nil {
		return nil, err
	}
	nr.Header = r.Header
	return http.DefaultTransport.RoundTrip(nr)
}

func TestExchangeRefreshAndList(t *testing.T) {
	var refreshes int
	mux := http.NewServeMux()
	mux.HandleFunc("/auth.gog.com/token", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch q.Get("grant_type") {
		case "authorization_code":
			if q.Get("code") != "good" {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":"invalid_grant","error_description":"bad code"}`)
				return
			}
			fmt.Fprint(w, `{"access_token":"a1","expires_in":3600,"refresh_token":"r1","user_id":"7"}`)
		case "refresh_token":
			refreshes++
			if q.Get("refresh_token") == "dead" {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":"invalid_grant"}`)
				return
			}
			fmt.Fprint(w, `{"access_token":"a2","expires_in":3600,"refresh_token":"r2","user_id":"7"}`)
		}
	})
	mux.HandleFunc("/embed.gog.com/userData.json", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"username":"tester","userId":"7"}`)
	})
	mux.HandleFunc("/embed.gog.com/user/data/games", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"owned":[1,2,3]}`)
	})
	mux.HandleFunc("/embed.gog.com/account/getFilteredProducts", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		if page == "1" {
			fmt.Fprint(w, `{"page":1,"totalPages":2,"tags":[{"id":"10","name":"Favorite","productCount":"1"},{"id":"11","name":"Backlog","productCount":"1"}],"products":[{"id":1,"title":"A","slug":"a","image":"//img/a","worksOn":{"Windows":true,"Mac":false,"Linux":true},"tags":["11","10","99"]}]}`)
		} else {
			fmt.Fprint(w, `{"page":2,"totalPages":2,"products":[{"id":2,"title":"B","slug":"b","image":"//img/b","worksOn":{"Windows":true,"Mac":true,"Linux":false}}]}`)
		}
	})
	mux.HandleFunc("/api.gog.com/products/1/downlink/installer/x", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer a1" && r.Header.Get("Authorization") != "Bearer a2" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"downlink": "https://cdn.gog.com/secure/setup_a.exe?t=1", "checksum": "https://cdn.gog.com/secure/setup_a.exe.xml"})
	})
	mux.HandleFunc("/cdn.gog.com/secure/setup_a.exe", func(w http.ResponseWriter, r *http.Request) {
		data := "0123456789"
		if rg := r.Header.Get("Range"); strings.HasPrefix(rg, "bytes=") {
			var off int
			fmt.Sscanf(rg, "bytes=%d-", &off)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", off, len(data)-1, len(data)))
			w.WriteHeader(206)
			fmt.Fprint(w, data[off:])
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="setup_a.exe"`)
		fmt.Fprint(w, data)
	})

	c, srv := newTestClient(t, mux)
	defer srv.Close()
	ctx := context.Background()

	if _, err := c.ExchangeCode(ctx, "bad"); err == nil {
		t.Fatal("expected error for bad code")
	}
	u, err := c.ExchangeCode(ctx, "https://embed.gog.com/on_login_success?origin=client&code=good")
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "tester" || u.ID != "7" || !c.Authenticated() {
		t.Errorf("unexpected user %+v", u)
	}
	owned, err := c.OwnedIDs(ctx)
	if err != nil || len(owned) != 3 || !owned[2] {
		t.Errorf("owned = %v, err = %v", owned, err)
	}
	games, err := c.ListGames(ctx, nil)
	if err != nil || len(games) != 2 || games[1].Title != "B" || !games[0].WorksLinux {
		t.Errorf("games = %+v, err = %v", games, err)
	}
	// Tag ids resolve to names, sorted; an id the catalogue lacks is dropped.
	if got := games[0].Tags; len(got) != 2 || got[0] != "Backlog" || got[1] != "Favorite" {
		t.Errorf("tags = %v, want [Backlog Favorite]", got)
	}
	if games[1].Tags != nil {
		t.Errorf("untagged game has tags %v", games[1].Tags)
	}
	if games[0].Image != "https://img/a_392.jpg" {
		t.Errorf("image = %q", games[0].Image)
	}

	// Expire the access token and make sure a refresh happens transparently.
	c.mu.Lock()
	c.token.ExpiresAt = time.Now().Add(-time.Minute)
	c.mu.Unlock()
	link, err := c.ResolveDownlink(ctx, "https://api.gog.com/products/1/downlink/installer/x")
	if err != nil {
		t.Fatal(err)
	}
	if refreshes != 1 || c.token.RefreshToken != "r2" {
		t.Errorf("refreshes = %d, refresh token = %q", refreshes, c.token.RefreshToken)
	}
	dl, err := c.OpenDownload(ctx, link.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	dl.Body.Close()
	if dl.Length != 10 || dl.Filename != "setup_a.exe" {
		t.Errorf("download = %+v", dl)
	}
	dl, err = c.OpenDownload(ctx, link.URL, 4)
	if err != nil {
		t.Fatal(err)
	}
	dl.Body.Close()
	if dl.Offset != 4 || dl.Length != 10 {
		t.Errorf("ranged download = %+v", dl)
	}

	// A permanently rejected refresh token flags the client as needing re-auth.
	c.mu.Lock()
	c.token.RefreshToken = "dead"
	c.token.ExpiresAt = time.Now().Add(-time.Minute)
	c.mu.Unlock()
	if _, err := c.OwnedIDs(ctx); err == nil {
		t.Fatal("expected auth failure")
	}
	if c.Authenticated() || c.AuthError() == "" {
		t.Errorf("client should report an auth error, got authenticated=%v err=%q", c.Authenticated(), c.AuthError())
	}
}

// The content system answers with every build it has, in no promised order and
// including private ones, and says nothing at all for products Galaxy does not
// cover.
func TestLatestBuild(t *testing.T) {
	var authorized bool
	mux := http.NewServeMux()
	mux.HandleFunc("/content-system.gog.com/products/1/os/windows/builds", func(w http.ResponseWriter, r *http.Request) {
		authorized = r.Header.Get("Authorization") != ""
		fmt.Fprint(w, `{"total_count":3,"items":[
			{"build_id":"100","version_name":"1.0","date_published":"2024-01-02T10:00:00+0000","public":true},
			{"build_id":"300","version_name":"3.0","date_published":"2024-03-02T10:00:00+0000","public":false},
			{"build_id":"200","version_name":"2.0","date_published":"2024-02-02T10:00:00+0000","public":true}]}`)
	})
	// "mac" is "osx" in the content system's paths.
	mux.HandleFunc("/content-system.gog.com/products/1/os/osx/builds", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"total_count":1,"items":[{"build_id":"55","version_name":"1.0","date_published":"2024-01-02T10:00:00+0000","public":true}]}`)
	})
	mux.HandleFunc("/content-system.gog.com/products/2/os/linux/builds", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	})
	c, srv := newTestClient(t, mux)
	defer srv.Close()
	ctx := context.Background()

	b, err := c.LatestBuild(ctx, 1, "windows")
	if err != nil {
		t.Fatal(err)
	}
	if b == nil || b.ID != "200" || b.VersionName != "2.0" {
		t.Errorf("newest public build expected, got %+v", b)
	}
	if authorized {
		t.Error("the build list is public and must not carry the account's token")
	}
	if b, err := c.LatestBuild(ctx, 1, "mac"); err != nil || b == nil || b.ID != "55" {
		t.Errorf("mac build: %+v %v", b, err)
	}
	// No builds for this product: not an error, just nothing to compare against.
	if b, err := c.LatestBuild(ctx, 2, "linux"); err != nil || b != nil {
		t.Errorf("a product without builds should give (nil, nil), got %+v %v", b, err)
	}
}
