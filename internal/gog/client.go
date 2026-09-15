package gog

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Credentials of the GOG Galaxy client, used by every community downloader.
const (
	clientID     = "46899977096215655"
	clientSecret = "9d85c43b1482497dbbce61f6e4aa173a433796eeae2ca8c5f6129f2dc4de46d9"
	redirectURI  = "https://embed.gog.com/on_login_success?origin=client"

	authBase  = "https://auth.gog.com"
	embedBase = "https://embed.gog.com"
	apiBase   = "https://api.gog.com"
	// contentBase serves the Galaxy content system: build lists and depot
	// manifests. It needs no authorization.
	contentBase = "https://content-system.gog.com"

	// maxAttempts is how often a failed API request is repeated before giving up.
	maxAttempts = 4
)

// Token is a stored OAuth token set.
type Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	UserID       string
	Username     string
	Error        string
}

// TokenStore persists tokens between restarts.
type TokenStore interface {
	Load(ctx context.Context) (Token, error)
	Save(ctx context.Context, t Token) error
	Clear(ctx context.Context) error
}

// persist is the context a token set is written to the store with: the values
// of ctx, but not its cancellation. A refresh rotates the refresh token, so a
// write that fails because the request that needed the token was cancelled
// meanwhile would leave the only valid token in memory, and the next restart
// would find a session GOG no longer accepts.
func persist(ctx context.Context) context.Context { return context.WithoutCancel(ctx) }

// Client is the real GOG client.
type Client struct {
	http      *http.Client
	dl        *http.Client
	store     TokenStore
	log       *slog.Logger
	userAgent string
	limiter   *rate.Limiter

	mu      sync.Mutex // guards token and gameTokens
	token   Token
	session uint64
	// gameTokens caches the tokens issued for games' own clients (see cloud.go),
	// keyed by client id.
	gameTokens map[string]gameToken
	persistMu  sync.Mutex // serializes store writes with logout
	// refreshMu serialises token refreshes. It is never held together with mu
	// across a network call, so readers of the token state are not blocked while
	// GOG is slow to answer.
	refreshMu sync.Mutex
}

// NewClient creates a client and loads any stored token.
func NewClient(ctx context.Context, store TokenStore, log *slog.Logger, version string) (*Client, error) {
	// Installer transfers can legitimately take hours, so the download client has no
	// overall timeout; the transport still gives up on a server that never answers,
	// and the downloader aborts transfers that stop delivering data.
	dlTransport := http.DefaultTransport.(*http.Transport).Clone()
	dlTransport.ResponseHeaderTimeout = 60 * time.Second
	c := &Client{
		http:      &http.Client{Timeout: 90 * time.Second},
		dl:        &http.Client{Transport: dlTransport},
		store:     store,
		log:       log.With("component", "gog"),
		userAgent: "gogl-watcher/" + version,
		limiter:   rate.NewLimiter(rate.Limit(4), 4),
	}
	t, err := store.Load(ctx)
	if err != nil {
		return nil, err
	}
	c.token = t
	return c, nil
}

// AuthURL implements API.
func (c *Client) AuthURL() string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("layout", "client2")
	return authBase + "/auth?" + q.Encode()
}

// ExtractCode pulls the authorization code out of a pasted URL or raw code.
func ExtractCode(input string) string {
	s := strings.TrimSpace(input)
	if strings.Contains(s, "code=") {
		if u, err := url.Parse(s); err == nil {
			if code := u.Query().Get("code"); code != "" {
				return code
			}
		}
		if i := strings.Index(s, "code="); i >= 0 {
			rest := s[i+5:]
			if j := strings.IndexAny(rest, "&# "); j >= 0 {
				rest = rest[:j]
			}
			return rest
		}
	}
	return s
}

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshToken     string `json:"refresh_token"`
	UserID           string `json:"user_id"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// ExchangeCode implements API.
func (c *Client) ExchangeCode(ctx context.Context, input string) (*User, error) {
	code := ExtractCode(input)
	if code == "" {
		return nil, errors.New("no authorization code provided")
	}
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("client_secret", clientSecret)
	q.Set("grant_type", "authorization_code")
	q.Set("code", code)
	q.Set("redirect_uri", redirectURI)
	tr, err := c.tokenRequest(ctx, q)
	if err != nil {
		return nil, err
	}
	t := Token{AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken, UserID: tr.UserID,
		ExpiresAt: time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)}
	c.mu.Lock()
	c.token = t
	c.session++
	c.gameTokens = nil
	session := c.session
	c.mu.Unlock()
	// GOG has consumed the one-time code by now, and every refresh from here on
	// rotates the refresh token: a token set that is not written down is a session
	// lost at the next restart. So the writes must not fail with the caller, which
	// may be a browser that closed the tab or a transfer that was just cancelled.
	if err := c.saveSession(persist(ctx), session, t); err != nil {
		return nil, err
	}
	// Fetch the username; failure here is not fatal.
	username, userID := "", t.UserID
	if u, err := c.fetchUser(ctx); err == nil {
		username = u.Username
		if u.ID != "" {
			userID = u.ID
		}
	} else {
		c.log.Warn("could not fetch user data", "error", err)
	}
	// Annotate the live token rather than the copy from before the fetch: the
	// fetch itself may have refreshed (and rotated) the tokens, or a logout may
	// have happened meanwhile.
	c.mu.Lock()
	if c.token.RefreshToken == "" {
		c.mu.Unlock()
		return nil, &AuthError{Msg: "disconnected while authorizing", Permanent: true}
	}
	c.token.Username = username
	c.token.UserID = userID
	live := c.token
	session = c.session
	c.mu.Unlock()
	if err := c.saveSession(persist(ctx), session, live); err != nil {
		return nil, err
	}
	c.log.Info("authorized with GOG", "user", live.Username)
	return &User{ID: live.UserID, Username: live.Username}, nil
}

func (c *Client) saveSession(ctx context.Context, session uint64, t Token) error {
	c.persistMu.Lock()
	defer c.persistMu.Unlock()
	c.mu.Lock()
	valid := c.session == session && c.token.RefreshToken == t.RefreshToken
	c.mu.Unlock()
	if !valid {
		return &AuthError{Msg: "GOG session changed during token save", Permanent: true}
	}
	return c.store.Save(ctx, t)
}

func (c *Client) tokenRequest(ctx context.Context, q url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authBase+"/token?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		// The request URL carries the client secret and the refresh token or the
		// authorization code, and *url.Error prints it. Errors end up in logs, the
		// database and API responses, so strip the URL.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tr tokenResponse
	_ = json.Unmarshal(body, &tr)
	if resp.StatusCode != http.StatusOK || tr.AccessToken == "" {
		msg := tr.ErrorDescription
		if msg == "" {
			msg = tr.Error
		}
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		// 4xx means GOG rejected the grant itself; rate limiting is transient.
		permanent := resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests
		return nil, &AuthError{Msg: "GOG rejected the request: " + msg, Permanent: permanent}
	}
	return &tr, nil
}

// AuthError is returned when GOG refuses to issue tokens.
type AuthError struct {
	Msg       string
	Permanent bool
}

func (e *AuthError) Error() string { return e.Msg }

// Authenticated implements API.
func (c *Client) Authenticated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token.RefreshToken != "" && c.token.Error == ""
}

// AuthError implements API.
func (c *Client) AuthError() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token.Error
}

// CurrentUser implements API.
func (c *Client) CurrentUser() *User {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token.RefreshToken == "" {
		return nil
	}
	return &User{ID: c.token.UserID, Username: c.token.Username}
}

// Logout implements API.
func (c *Client) Logout(ctx context.Context) error {
	c.persistMu.Lock()
	defer c.persistMu.Unlock()
	c.mu.Lock()
	c.token = Token{}
	c.session++
	c.gameTokens = nil
	c.mu.Unlock()
	return c.store.Clear(ctx)
}

// currentToken returns the stored access token if it is still usable, "" if it
// has to be refreshed, or an error if the client cannot make authenticated
// requests at all.
func (c *Client) currentToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token.RefreshToken == "" {
		return "", &AuthError{Msg: "not authorized with GOG", Permanent: true}
	}
	if c.token.Error != "" {
		return "", &AuthError{Msg: c.token.Error, Permanent: true}
	}
	if c.token.AccessToken != "" && time.Until(c.token.ExpiresAt) > 2*time.Minute {
		return c.token.AccessToken, nil
	}
	return "", nil
}

// accessToken returns a valid access token, refreshing when needed. The token
// mutex is not held while GOG is being asked, so Authenticated() and the status
// endpoint never wait on the network.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	tok, err := c.currentToken()
	if err != nil || tok != "" {
		return tok, err
	}
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	// Another request may have refreshed the token while we waited for the lock.
	tok, err = c.currentToken()
	if err != nil || tok != "" {
		return tok, err
	}
	c.mu.Lock()
	refresh := c.token.RefreshToken
	session := c.session
	c.mu.Unlock()
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("client_secret", clientSecret)
	q.Set("grant_type", "refresh_token")
	q.Set("refresh_token", refresh)
	tr, rerr := c.tokenRequest(ctx, q)
	c.mu.Lock()
	if c.token.RefreshToken != refresh {
		// Logged out or re-authorized while the refresh was in flight: the answer
		// belongs to a session that no longer exists.
		if c.token.RefreshToken == "" {
			c.mu.Unlock()
			return "", &AuthError{Msg: "not authorized with GOG", Permanent: true}
		}
		if c.token.AccessToken != "" {
			out := c.token.AccessToken
			c.mu.Unlock()
			return out, nil
		}
		c.mu.Unlock()
		return "", errors.New("GOG session changed during token refresh")
	}
	if rerr != nil {
		var ae *AuthError
		if errors.As(rerr, &ae) && ae.Permanent {
			c.token.Error = "GOG session expired, please authorize again (" + ae.Msg + ")"
			live := c.token
			c.mu.Unlock()
			_ = c.saveSession(persist(ctx), session, live)
			c.log.Error("refresh token rejected, re-authorization required", "error", ae.Msg)
			return "", &AuthError{Msg: live.Error, Permanent: true}
		}
		c.mu.Unlock()
		return "", rerr
	}
	c.token.AccessToken = tr.AccessToken
	if tr.RefreshToken != "" {
		c.token.RefreshToken = tr.RefreshToken
	}
	if tr.UserID != "" {
		c.token.UserID = tr.UserID
	}
	c.token.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	live := c.token
	c.mu.Unlock()
	// The old refresh token is gone the moment GOG answered; see ExchangeCode.
	if err := c.saveSession(persist(ctx), session, live); err != nil {
		return "", err
	}
	c.log.Debug("refreshed GOG access token")
	return live.AccessToken, nil
}

// HTTPError is a non-2xx API response.
type HTTPError struct {
	Status int
	URL    string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("GOG returned HTTP %d for %s", e.Status, e.URL)
}

// getJSON performs an authenticated GET with retries and decodes the JSON body.
func (c *Client) getJSON(ctx context.Context, u string, auth bool, out any) error {
	body, err := c.get(ctx, u, auth)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding %s: %w", u, err)
	}
	return nil
}

func (c *Client) get(ctx context.Context, u string, auth bool) ([]byte, error) {
	var lastErr error
	for attempt := range maxAttempts {
		if attempt > 0 {
			wait := time.Duration(1<<uint(attempt)) * time.Second
			var he *HTTPError
			if errors.As(lastErr, &he) && he.Status == http.StatusTooManyRequests {
				wait = 10 * time.Second * time.Duration(attempt)
			}
			c.log.Debug("retrying GOG request", "url", u, "attempt", attempt, "wait", wait, "error", lastErr)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Accept", "application/json")
		if auth {
			tok, err := c.accessToken(ctx)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			if rerr != nil {
				lastErr = rerr
				continue
			}
			return body, nil
		}
		lastErr = &HTTPError{Status: resp.StatusCode, URL: u}
		if resp.StatusCode == http.StatusUnauthorized && auth {
			// Force a refresh on the next attempt.
			c.mu.Lock()
			c.token.AccessToken = ""
			c.mu.Unlock()
			continue
		}
		if resp.StatusCode == http.StatusNotFound || (resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests) {
			return nil, lastErr
		}
	}
	return nil, lastErr
}

func (c *Client) fetchUser(ctx context.Context) (*User, error) {
	var ud struct {
		Username string     `json:"username"`
		UserID   FlexString `json:"userId"`
		GalaxyID FlexString `json:"galaxyUserId"`
	}
	if err := c.getJSON(ctx, embedBase+"/userData.json", true, &ud); err != nil {
		return nil, err
	}
	id := string(ud.UserID)
	if id == "" {
		id = string(ud.GalaxyID)
	}
	return &User{ID: id, Username: ud.Username}, nil
}

// OwnedIDs implements API.
func (c *Client) OwnedIDs(ctx context.Context) (OwnedSet, error) {
	var resp struct {
		Owned []int64 `json:"owned"`
	}
	if err := c.getJSON(ctx, embedBase+"/user/data/games", true, &resp); err != nil {
		return nil, err
	}
	set := OwnedSet{}
	for _, id := range resp.Owned {
		set[id] = true
	}
	return set, nil
}

type filteredProducts struct {
	Page       int `json:"page"`
	TotalPages int `json:"totalPages"`
	// Tags is the catalogue of the user's own tags; products refer to them by id.
	Tags []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"tags"`
	Products []struct {
		ID      int64  `json:"id"`
		Title   string `json:"title"`
		Slug    string `json:"slug"`
		Image   string `json:"image"`
		WorksOn struct {
			Windows bool `json:"Windows"`
			Mac     bool `json:"Mac"`
			Linux   bool `json:"Linux"`
		} `json:"worksOn"`
		Tags []string `json:"tags"`
	} `json:"products"`
}

// tagNames resolves a product's tag ids against the catalogue; an id the
// catalogue does not know is dropped.
func (fp *filteredProducts) tagNames(ids []string) []string {
	var names []string
	for _, id := range ids {
		for _, t := range fp.Tags {
			if t.ID == id && t.Name != "" {
				names = append(names, t.Name)
				break
			}
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// ListGames implements API.
func (c *Client) ListGames(ctx context.Context, progress func(page, total int)) ([]ListedGame, error) {
	var out []ListedGame
	seen := map[int64]bool{}
	for page, total := 1, 1; page <= total; page++ {
		var fp filteredProducts
		u := fmt.Sprintf("%s/account/getFilteredProducts?mediaType=1&sortBy=title&page=%d", embedBase, page)
		if err := c.getJSON(ctx, u, true, &fp); err != nil {
			return nil, err
		}
		if fp.TotalPages > 0 {
			total = fp.TotalPages
		}
		for _, p := range fp.Products {
			if seen[p.ID] {
				continue
			}
			seen[p.ID] = true
			out = append(out, ListedGame{
				ID: p.ID, Title: p.Title, Slug: p.Slug, Image: NormalizeImage(p.Image),
				WorksWindows: p.WorksOn.Windows, WorksMac: p.WorksOn.Mac, WorksLinux: p.WorksOn.Linux,
				Tags: fp.tagNames(p.Tags),
			})
		}
		if progress != nil {
			progress(page, total)
		}
		if len(fp.Products) == 0 {
			break
		}
	}
	return out, nil
}

// NormalizeImage turns GOG's protocol-relative, extension-less image ids into a URL.
func NormalizeImage(img string) string {
	if img == "" {
		return ""
	}
	if strings.HasPrefix(img, "//") {
		img = "https:" + img
	}
	if ext := strings.ToLower(path.Ext(img)); ext != ".jpg" && ext != ".png" && ext != ".webp" {
		img += "_392.jpg"
	}
	return img
}

// ProductDetails implements API.
func (c *Client) ProductDetails(ctx context.Context, id int64) (*Product, error) {
	var p Product
	u := fmt.Sprintf("%s/products/%d?expand=downloads,expanded_dlcs&locale=en-US", apiBase, id)
	if err := c.getJSON(ctx, u, true, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// BoxArt implements API. The v2 games endpoint is public; a game GOG no longer
// lists there answers 404, which is not a failure.
func (c *Client) BoxArt(ctx context.Context, id int64) (string, error) {
	var resp struct {
		Links struct {
			BoxArtImage struct {
				Href string `json:"href"`
			} `json:"boxArtImage"`
		} `json:"_links"`
	}
	u := fmt.Sprintf("%s/v2/games/%d", apiBase, id)
	if err := c.getJSON(ctx, u, false, &resp); err != nil {
		var he *HTTPError
		if errors.As(err, &he) && he.Status == http.StatusNotFound {
			return "", nil
		}
		return "", err
	}
	return NormalizeImage(resp.Links.BoxArtImage.Href), nil
}

// buildOS maps a platform name used by the application onto the one the content
// system uses in its paths.
func buildOS(os string) string {
	if os == "mac" {
		return "osx"
	}
	return os
}

// LatestBuild implements API.
func (c *Client) LatestBuild(ctx context.Context, productID int64, os string) (*Build, error) {
	var resp struct {
		Items []struct {
			BuildID       FlexString `json:"build_id"`
			VersionName   string     `json:"version_name"`
			DatePublished string     `json:"date_published"`
			Public        bool       `json:"public"`
		} `json:"items"`
	}
	u := fmt.Sprintf("%s/products/%d/os/%s/builds?generation=2", contentBase, productID, buildOS(os))
	if err := c.getJSON(ctx, u, false, &resp); err != nil {
		var he *HTTPError
		if errors.As(err, &he) && he.Status == http.StatusNotFound {
			// Nothing is published for this product and OS, which is not a failure.
			return nil, nil
		}
		return nil, err
	}
	var latest *Build
	for _, it := range resp.Items {
		if !it.Public || it.BuildID == "" {
			continue
		}
		b := Build{ID: string(it.BuildID), VersionName: it.VersionName, Public: true, PublishedAt: parseBuildTime(it.DatePublished)}
		// GOG lists the newest build first, but the order is not promised, so the
		// publication date decides where there is one.
		if latest == nil || b.PublishedAt.After(latest.PublishedAt) {
			cur := b
			latest = &cur
		}
	}
	return latest, nil
}

// parseBuildTime reads the publication date of a build. The content system
// writes its zone offset without a colon, which is not RFC 3339, but both
// spellings turn up. An unreadable date is left at zero, which keeps the order
// GOG listed the builds in.
func parseBuildTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05-0700"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// ResolveDownlink implements API.
func (c *Client) ResolveDownlink(ctx context.Context, downlink string) (*Downlink, error) {
	var d Downlink
	if err := c.getJSON(ctx, downlink, true, &d); err != nil {
		return nil, err
	}
	if d.URL == "" {
		return nil, fmt.Errorf("GOG returned no download URL for %s", downlink)
	}
	return &d, nil
}

// FetchChecksum implements API. The checksum document is small, so it uses the
// ordinary API client and its timeout, not the download client that is allowed
// to take hours.
func (c *Client) FetchChecksum(ctx context.Context, u string) (*Checksum, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{Status: resp.StatusCode, URL: u}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	return ParseChecksum(body)
}

// ParseChecksum decodes the checksum XML document.
func ParseChecksum(body []byte) (*Checksum, error) {
	var cs Checksum
	if err := xml.Unmarshal(body, &cs); err != nil {
		return nil, fmt.Errorf("decoding checksum xml: %w", err)
	}
	return &cs, nil
}

// OpenDownload implements API.
func (c *Client) OpenDownload(ctx context.Context, u string, offset int64) (*Download, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := c.dl.Do(req)
	if err != nil {
		return nil, err
	}
	d := &Download{Body: resp.Body, Length: -1}
	switch resp.StatusCode {
	case http.StatusOK:
		d.Offset = 0
		if resp.ContentLength >= 0 {
			d.Length = resp.ContentLength
		}
	case http.StatusPartialContent:
		d.Offset = offset
		if cr := resp.Header.Get("Content-Range"); cr != "" {
			if i := strings.LastIndex(cr, "/"); i >= 0 {
				if n, err := strconv.ParseInt(cr[i+1:], 10, 64); err == nil {
					d.Length = n
				}
			}
		}
		if d.Length < 0 && resp.ContentLength >= 0 {
			d.Length = offset + resp.ContentLength
		}
	case http.StatusRequestedRangeNotSatisfiable:
		resp.Body.Close()
		return nil, ErrRangeNotSatisfiable
	default:
		resp.Body.Close()
		return nil, &HTTPError{Status: resp.StatusCode, URL: u}
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			d.Filename = params["filename"]
		}
	}
	return d, nil
}

// ErrRangeNotSatisfiable means the local partial file is larger than the remote one.
var ErrRangeNotSatisfiable = errors.New("requested range not satisfiable")

// FilenameFromURL extracts the file name from a CDN URL.
func FilenameFromURL(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	name := path.Base(parsed.Path)
	if name == "." || name == "/" {
		return ""
	}
	if dec, err := url.PathUnescape(name); err == nil {
		name = dec
	}
	return name
}
