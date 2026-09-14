package gog

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestParseCloudListing(t *testing.T) {
	body := `[
	  {"name":"saves/slot1.sav","hash":"9e107d9d372bb6826bd81d3542a419d6","bytes":1234,"last_modified":"2024-05-01T10:11:12.345678","content_type":"application/octet-stream"},
	  {"name":"saves/gone.sav","hash":"aadd86936a80ee8a369579c3926f1b3c","bytes":20,"last_modified":"2024-05-01T10:11:12"},
	  {"subdir":"saves/"},
	  {"name":"saves/","hash":"d41d8cd98f00b204e9800998ecf8427e","bytes":0,"last_modified":"2024-05-01T10:11:12"}
	]`
	saves, err := ParseCloudListing([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(saves) != 1 {
		t.Fatalf("want the one real file, got %+v", saves)
	}
	s := saves[0]
	if s.Name != "saves/slot1.sav" || s.Hash != "9e107d9d372bb6826bd81d3542a419d6" || s.Bytes != 1234 {
		t.Errorf("unexpected save: %+v", s)
	}
	if want := time.Date(2024, 5, 1, 10, 11, 12, 345678000, time.UTC); !s.LastModified.Equal(want) {
		t.Errorf("last modified = %v, want %v", s.LastModified, want)
	}
}

// The cloud storage is reached through the game's own OAuth client: its
// credentials come from the build manifest, the token for it is issued against
// the account's refresh token without touching the account's session, and the
// files come back gzip-compressed with a checksum of the stored form.
func TestCloudSaves(t *testing.T) {
	const clientID, clientSecret = "51153410217180642", "s3cret"
	var (
		tokens   int
		save     = []byte("the save game as the game wrote it")
		gzSave   bytes.Buffer
		listing  = `[{"name":"saves/slot1.sav","hash":"%s","bytes":%d,"last_modified":"2024-05-01T10:11:12"}]`
		manifest bytes.Buffer
	)
	zw := gzip.NewWriter(&gzSave)
	zw.Write(save)
	zw.Close()
	sum := md5.Sum(gzSave.Bytes())
	gzHash := hex.EncodeToString(sum[:])
	mw := zlib.NewWriter(&manifest)
	fmt.Fprintf(mw, `{"baseProductId":"1","clientId":"%s","clientSecret":"%s","depots":[]}`, clientID, clientSecret)
	mw.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/auth.gog.com/token", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("grant_type") == "authorization_code":
			fmt.Fprint(w, `{"access_token":"a1","expires_in":3600,"refresh_token":"r1","user_id":"7"}`)
		case q.Get("client_id") == clientID:
			tokens++
			if q.Get("client_secret") != clientSecret || q.Get("refresh_token") != "r1" || q.Get("without_new_session") != "1" {
				t.Errorf("unexpected game token request: %s", r.URL.RawQuery)
				w.WriteHeader(400)
				return
			}
			fmt.Fprint(w, `{"access_token":"game-token","expires_in":3600,"refresh_token":"game-refresh","user_id":"7"}`)
		case q.Get("client_id") == "other":
			fmt.Fprint(w, `{"access_token":"other-token","expires_in":3600,"refresh_token":"other-refresh","user_id":"7"}`)
		case q.Get("client_id") == "refused":
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"invalid_client"}`)
		default:
			t.Errorf("unexpected token request: %s", r.URL.RawQuery)
			w.WriteHeader(400)
		}
	})
	mux.HandleFunc("/embed.gog.com/userData.json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"username":"tester","userId":"7"}`)
	})
	mux.HandleFunc("/content-system.gog.com/products/1/os/windows/builds", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"build_id":"1","public":true,"link":"https://cdn.gog.com/manifests/1"}]}`)
	})
	mux.HandleFunc("/content-system.gog.com/products/2/os/windows/builds", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	})
	mux.HandleFunc("/cdn.gog.com/manifests/1", func(w http.ResponseWriter, r *http.Request) {
		w.Write(manifest.Bytes())
	})
	container := "/cloudstorage.gog.com/v1/7/" + clientID
	mux.HandleFunc(container, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer game-token" {
			w.WriteHeader(401)
			return
		}
		if r.Header.Get("User-Agent") != cloudUserAgent {
			t.Errorf("cloud request without the Galaxy user agent: %q", r.Header.Get("User-Agent"))
		}
		fmt.Fprintf(w, listing, gzHash, gzSave.Len())
	})
	mux.HandleFunc("/cloudstorage.gog.com/v1/7/other", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	})
	mux.HandleFunc(container+"/saves/slot1.sav", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer game-token" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Etag", gzHash)
		w.Header().Set("X-Object-Meta-LocalLastModified", "2024-05-01T10:11:12+00:00")
		w.Write(gzSave.Bytes())
	})
	mux.HandleFunc(container+"/saves/bad.sav", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Etag", "00000000000000000000000000000000")
		w.Write(gzSave.Bytes())
	})

	c, srv := newTestClient(t, mux)
	defer srv.Close()
	ctx := context.Background()
	if _, err := c.ExchangeCode(ctx, "good"); err != nil {
		t.Fatal(err)
	}

	gc, err := c.GameClient(ctx, 1, "windows")
	if err != nil {
		t.Fatal(err)
	}
	if gc == nil || gc.ID != clientID || gc.Secret != clientSecret {
		t.Fatalf("game client = %+v", gc)
	}
	if gc, err := c.GameClient(ctx, 2, "windows"); err != nil || gc != nil {
		t.Errorf("a game without builds should have no client, got %+v, %v", gc, err)
	}

	saves, err := c.ListCloudSaves(ctx, *gc)
	if err != nil {
		t.Fatal(err)
	}
	if len(saves) != 1 || saves[0].Name != "saves/slot1.sav" || saves[0].Hash != gzHash {
		t.Fatalf("listing = %+v", saves)
	}
	if empty, err := c.ListCloudSaves(ctx, GameClient{ID: "other", Secret: "x"}); err != nil || len(empty) != 0 {
		t.Errorf("a container GOG never created should list as empty, got %v, %v", empty, err)
	}

	dl, err := c.OpenCloudSave(ctx, *gc, "saves/slot1.sav")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(dl.Body)
	dl.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, save) {
		t.Errorf("downloaded %q, want the decompressed save", got)
	}
	if want := time.Date(2024, 5, 1, 10, 11, 12, 0, time.UTC); !dl.ModTime.Equal(want) {
		t.Errorf("mod time = %v, want %v", dl.ModTime, want)
	}
	// The token was fetched once and reused for the listing and the download.
	if tokens != 1 {
		t.Errorf("game token requested %d times, want 1", tokens)
	}

	dl, err = c.OpenCloudSave(ctx, *gc, "saves/bad.sav")
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(dl.Body)
	dl.Body.Close()
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("a stored form that does not hash as promised should fail the read, got %v", err)
	}

	// A client GOG issues no token for is a failure of that game, not of the
	// account's session, which stays usable.
	var ae *AuthError
	if _, err := c.ListCloudSaves(ctx, GameClient{ID: "refused", Secret: "x"}); err == nil || errors.As(err, &ae) {
		t.Errorf("a refused game client should fail plainly, got %v", err)
	}
	if !c.Authenticated() {
		t.Error("a refused game client must not end the account's session")
	}

	// Logging out forgets the game tokens with the session.
	if err := c.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListCloudSaves(ctx, *gc); err == nil {
		t.Error("listing without a session should fail")
	}
}
