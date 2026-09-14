package gog

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// A token refresh in flight must not block callers that only read the token state.
func TestAuthenticatedDoesNotBlockDuringRefresh(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/auth.gog.com/token", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("grant_type") == "refresh_token" {
			close(entered)
			<-release
		}
		fmt.Fprint(w, `{"access_token":"a2","expires_in":3600,"refresh_token":"r2","user_id":"7"}`)
	})
	mux.HandleFunc("/embed.gog.com/user/data/games", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"owned":[1]}`)
	})
	c, srv := newTestClient(t, mux)
	defer srv.Close()
	var once sync.Once
	defer once.Do(func() { close(release) }) // runs before srv.Close so the handler can exit
	c.mu.Lock()
	c.token = Token{RefreshToken: "r1", ExpiresAt: time.Now().Add(-time.Minute)}
	c.mu.Unlock()

	go func() { _, _ = c.OwnedIDs(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh never started")
	}
	done := make(chan bool, 1)
	go func() { done <- c.Authenticated() }()
	select {
	case ok := <-done:
		if !ok {
			t.Error("client should still count as authenticated while refreshing")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Authenticated() blocked while a token refresh was in flight")
	}
	once.Do(func() { close(release) })
}

// A rate-limited (429) or otherwise transient failure of the token endpoint must not
// invalidate the stored session; only a real rejection of the refresh token should.
func TestTransientRefreshFailureKeepsSession(t *testing.T) {
	status := 429
	mux := http.NewServeMux()
	mux.HandleFunc("/auth.gog.com/token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, `{"error":"too_many_requests"}`)
	})
	c, srv := newTestClient(t, mux)
	defer srv.Close()
	c.mu.Lock()
	c.token = Token{RefreshToken: "r1", ExpiresAt: time.Now().Add(-time.Minute)}
	c.mu.Unlock()
	if _, err := c.OwnedIDs(context.Background()); err == nil {
		t.Fatal("expected the request to fail")
	}
	if !c.Authenticated() || c.AuthError() != "" {
		t.Fatalf("a 429 from the token endpoint must not require re-authorization: authenticated=%v err=%q", c.Authenticated(), c.AuthError())
	}
	status = 400
	if _, err := c.OwnedIDs(context.Background()); err == nil {
		t.Fatal("expected the request to fail")
	}
	if c.Authenticated() || c.AuthError() == "" {
		t.Fatalf("a rejected refresh token must require re-authorization: authenticated=%v err=%q", c.Authenticated(), c.AuthError())
	}
}

// A transport failure while talking to the token endpoint must not put the
// request URL (client secret, refresh token, authorization code) into the error.
func TestTokenRequestErrorsDoNotLeakCredentials(t *testing.T) {
	c, srv := newTestClient(t, http.NewServeMux())
	srv.Close() // every request now fails at the transport level
	c.mu.Lock()
	c.token = Token{RefreshToken: "secret-refresh-token", ExpiresAt: time.Now().Add(-time.Minute)}
	c.mu.Unlock()
	_, err := c.OwnedIDs(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, needle := range []string{"secret-refresh-token", clientSecret, "client_secret"} {
		if strings.Contains(err.Error(), needle) {
			t.Fatalf("error leaks %q: %v", needle, err)
		}
	}
	if _, err := c.ExchangeCode(context.Background(), "one-time-code"); err == nil || strings.Contains(err.Error(), "one-time-code") {
		t.Fatalf("exchange error leaks the code: %v", err)
	}
}

// The user-data request after an exchange may refresh (rotate) the tokens; the
// rotated pair must be what ends up stored, with the user name added.
func TestExchangeCodeKeepsTokensRotatedDuringUserFetch(t *testing.T) {
	var userCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("/auth.gog.com/token", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("grant_type") {
		case "authorization_code":
			fmt.Fprint(w, `{"access_token":"a1","expires_in":3600,"refresh_token":"r1","user_id":"7"}`)
		case "refresh_token":
			fmt.Fprint(w, `{"access_token":"a2","expires_in":3600,"refresh_token":"r2","user_id":"7"}`)
		}
	})
	mux.HandleFunc("/embed.gog.com/userData.json", func(w http.ResponseWriter, r *http.Request) {
		userCalls++
		if r.Header.Get("Authorization") == "Bearer a1" {
			w.WriteHeader(401) // a1 is rejected: the client refreshes to a2/r2
			return
		}
		fmt.Fprint(w, `{"username":"tester","userId":"7"}`)
	})
	c, srv := newTestClient(t, mux)
	defer srv.Close()
	store := c.store.(*memStore)
	u, err := c.ExchangeCode(context.Background(), "good")
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "tester" {
		t.Errorf("user = %+v", u)
	}
	c.mu.Lock()
	tok := c.token
	c.mu.Unlock()
	if tok.RefreshToken != "r2" || tok.AccessToken != "a2" || tok.Username != "tester" {
		t.Errorf("live token lost the rotation: %+v", tok)
	}
	if store.t.RefreshToken != "r2" || store.t.Username != "tester" {
		t.Errorf("stored token lost the rotation: %+v", store.t)
	}
}

// ctxStore fails a write whose context is done, as the database does.
type ctxStore struct{ memStore }

func (s *ctxStore) Save(ctx context.Context, t Token) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.memStore.Save(ctx, t)
}

// cancelAfterTransport cancels the caller once a request to path has been
// answered in full, which is the moment GOG has already rotated the tokens.
type cancelAfterTransport struct {
	rewriteTransport
	path   string
	cancel context.CancelFunc
}

func (t cancelAfterTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.rewriteTransport.RoundTrip(r)
	if err != nil || r.URL.Path != t.path {
		return resp, err
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	t.cancel()
	return resp, nil
}

// GOG rotates the refresh token on every refresh, so the pair it answers with
// must reach the store even when the request that needed it is cancelled right
// then (a paused transfer, a closed tab): otherwise the process restarts into a
// session GOG no longer accepts.
func TestRotatedTokensAreStoredWhenTheCallerIsCancelled(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth.gog.com/token", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"access_token":"a2","expires_in":3600,"refresh_token":"r2","user_id":"7"}`)
	})
	mux.HandleFunc("/embed.gog.com/user/data/games", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"owned":[1]}`)
	})
	c, srv := newTestClient(t, mux)
	defer srv.Close()
	store := &ctxStore{memStore{Token{RefreshToken: "r1"}}}
	c.store = store
	c.mu.Lock()
	c.token = Token{RefreshToken: "r1", ExpiresAt: time.Now().Add(-time.Minute)}
	c.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.http.Transport = cancelAfterTransport{rewriteTransport: c.http.Transport.(rewriteTransport), path: "/token", cancel: cancel}

	_, _ = c.OwnedIDs(ctx) // may well fail: the caller is gone
	if store.t.RefreshToken != "r2" || store.t.AccessToken != "a2" {
		t.Fatalf("rotated tokens were not stored: %+v", store.t)
	}
	c.mu.Lock()
	live := c.token
	c.mu.Unlock()
	if live.RefreshToken != "r2" {
		t.Errorf("live token = %+v", live)
	}
}
