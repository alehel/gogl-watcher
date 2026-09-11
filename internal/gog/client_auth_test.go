package gog

import (
	"context"
	"fmt"
	"net/http"
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
