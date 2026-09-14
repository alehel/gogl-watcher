package gog

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Cloud saves live in GOG's cloud storage, an object store with one container
// per user and game. The game is identified not by its product id but by the
// OAuth client of its Galaxy build, whose credentials are published in the
// build's manifest. Reading the container takes a token issued for that client,
// which GOG grants against the account's ordinary refresh token. The GOG Galaxy
// client and community tools (Heroic's gogdl) do exactly this.
const (
	cloudBase = "https://cloudstorage.gog.com"
	// cloudUserAgent is what the Galaxy sync service identifies itself as; the
	// cloud storage is only ever talked to by it, so this is what it expects.
	cloudUserAgent = "GOGGalaxyCommunicationService/2.0.13.27 (Windows_32bit) dont_sync_marker/true installation_source/gog"
	// DeletedSaveHash is the hash of the placeholder Galaxy leaves in place of a
	// save it deleted: an empty gzip stream. Such an entry is not a file.
	DeletedSaveHash = "aadd86936a80ee8a369579c3926f1b3c"
)

// GameClient is the OAuth client of a game's Galaxy build. Its credentials are
// not secrets of the user: they are published in the build manifest, which
// anyone can read. They are what names the game's cloud storage container.
type GameClient struct {
	ID     string
	Secret string
}

// CloudSave is one object in a game's cloud storage container.
type CloudSave struct {
	// Name is the object's path within the container, such as "saves/slot1.sav".
	Name string
	// Hash is the MD5 of the object as stored, which is the compressed form for
	// the files Galaxy uploads. It identifies the version of the file.
	Hash string
	// Bytes is the size as stored, before decompression.
	Bytes int64
	// LastModified is when the object was last written to the cloud.
	LastModified time.Time
}

// GameClient implements API. It reads the newest Galaxy build of the product
// for one OS ("windows" or "mac"; Galaxy has no Linux builds) and returns the
// client it names, or nil when there is no build or the build names none: such
// a game has no cloud storage.
func (c *Client) GameClient(ctx context.Context, productID int64, os string) (*GameClient, error) {
	var resp struct {
		Items []struct {
			Link   string `json:"link"`
			Public bool   `json:"public"`
		} `json:"items"`
	}
	u := fmt.Sprintf("%s/products/%d/os/%s/builds?generation=2", contentBase, productID, buildOS(os))
	if err := c.getJSON(ctx, u, false, &resp); err != nil {
		var he *HTTPError
		if errors.As(err, &he) && he.Status == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	link := ""
	for _, it := range resp.Items {
		if it.Public && it.Link != "" {
			link = it.Link
			break
		}
	}
	if link == "" {
		return nil, nil
	}
	body, err := c.get(ctx, link, false)
	if err != nil {
		return nil, err
	}
	var meta struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
	}
	if err := decodeManifest(body, &meta); err != nil {
		return nil, fmt.Errorf("decoding build manifest: %w", err)
	}
	if meta.ClientID == "" || meta.ClientSecret == "" {
		return nil, nil
	}
	return &GameClient{ID: meta.ClientID, Secret: meta.ClientSecret}, nil
}

// decodeManifest reads a Galaxy build manifest, which the content system serves
// zlib-compressed, although plain JSON turns up too.
func decodeManifest(body []byte, out any) error {
	if zr, err := zlib.NewReader(bytes.NewReader(body)); err == nil {
		plain, err := io.ReadAll(io.LimitReader(zr, 64<<20))
		zr.Close()
		if err == nil {
			return json.Unmarshal(plain, out)
		}
	}
	return json.Unmarshal(body, out)
}

// gameToken is a token issued for a game's client, with the user id GOG
// answered it with (the cloud storage container is named by both).
type gameToken struct {
	access    string
	userID    string
	expiresAt time.Time
}

// gameAccessToken returns a token for the game's client, from the cache while
// it lasts. GOG issues it against the account's refresh token; asking without a
// new session keeps that refresh token as it is, so the account's own session
// is not touched.
func (c *Client) gameAccessToken(ctx context.Context, client GameClient) (gameToken, error) {
	c.mu.Lock()
	if t, ok := c.gameTokens[client.ID]; ok && time.Until(t.expiresAt) > 2*time.Minute {
		c.mu.Unlock()
		return t, nil
	}
	refresh := c.token.RefreshToken
	userID := c.token.UserID
	c.mu.Unlock()
	if _, err := c.currentToken(); err != nil {
		return gameToken{}, err
	}
	if err := c.limiter.Wait(ctx); err != nil {
		return gameToken{}, err
	}
	q := url.Values{}
	q.Set("client_id", client.ID)
	q.Set("client_secret", client.Secret)
	q.Set("grant_type", "refresh_token")
	q.Set("refresh_token", refresh)
	q.Set("without_new_session", "1")
	tr, err := c.tokenRequest(ctx, q)
	if err != nil {
		if ctx.Err() != nil {
			return gameToken{}, ctx.Err()
		}
		var ae *AuthError
		if errors.As(err, &ae) {
			// GOG refused the grant for this client, which says nothing about the
			// account's session: that is judged by its own refresh. So this is not
			// an AuthError, or a save GOG will not issue a token for would wait
			// for a re-authorization forever instead of failing.
			return gameToken{}, fmt.Errorf("GOG refused a token for game client %s: %s", client.ID, ae.Msg)
		}
		return gameToken{}, fmt.Errorf("token for game client %s: %w", client.ID, err)
	}
	if tr.UserID != "" {
		userID = tr.UserID
	}
	t := gameToken{access: tr.AccessToken, userID: userID, expiresAt: time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)}
	c.mu.Lock()
	if c.gameTokens == nil {
		c.gameTokens = map[string]gameToken{}
	}
	c.gameTokens[client.ID] = t
	c.mu.Unlock()
	return t, nil
}

// cloudRequest builds an authenticated request against the game's container.
// name is the object path within it, "" for the container itself.
func (c *Client) cloudRequest(ctx context.Context, client GameClient, name string) (*http.Request, error) {
	t, err := c.gameAccessToken(ctx, client)
	if err != nil {
		return nil, err
	}
	u := fmt.Sprintf("%s/v1/%s/%s", cloudBase, url.PathEscape(t.userID), url.PathEscape(client.ID))
	if name != "" {
		u += "/" + escapeObjectName(name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", cloudUserAgent)
	req.Header.Set("X-Object-Meta-User-Agent", cloudUserAgent)
	req.Header.Set("Authorization", "Bearer "+t.access)
	return req, nil
}

// escapeObjectName escapes an object path for a URL, segment by segment, so the
// slashes that structure it stay.
func escapeObjectName(name string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// ListCloudSaves implements API. A container GOG has never created answers 404,
// which means the game has no saves in the cloud, not that something failed.
func (c *Client) ListCloudSaves(ctx context.Context, client GameClient) ([]CloudSave, error) {
	var lastErr error
	for attempt := range maxAttempts {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(1<<uint(attempt)) * time.Second):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, err
		}
		req, err := c.cloudRequest(ctx, client, "")
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
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
		switch {
		case resp.StatusCode == http.StatusNotFound:
			return []CloudSave{}, nil
		case resp.StatusCode == http.StatusUnauthorized:
			// The cached token is no good; the next attempt fetches a new one.
			c.mu.Lock()
			delete(c.gameTokens, client.ID)
			c.mu.Unlock()
			lastErr = &HTTPError{Status: resp.StatusCode, URL: req.URL.String()}
			continue
		case resp.StatusCode != http.StatusOK:
			lastErr = &HTTPError{Status: resp.StatusCode, URL: req.URL.String()}
			if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
				return nil, lastErr
			}
			continue
		case rerr != nil:
			lastErr = rerr
			continue
		}
		return ParseCloudListing(body)
	}
	return nil, lastErr
}

// ParseCloudListing decodes a container listing. Objects Galaxy has deleted
// (see DeletedSaveHash) and directory markers are left out: neither is a file.
func ParseCloudListing(body []byte) ([]CloudSave, error) {
	var raw []struct {
		Name         string  `json:"name"`
		Hash         string  `json:"hash"`
		Bytes        int64   `json:"bytes"`
		LastModified string  `json:"last_modified"`
		ContentType  string  `json:"content_type"`
		Subdir       *string `json:"subdir"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decoding cloud listing: %w", err)
	}
	out := make([]CloudSave, 0, len(raw))
	for _, r := range raw {
		hash := strings.ToLower(strings.Trim(r.Hash, `"`))
		if r.Subdir != nil || r.Name == "" || strings.HasSuffix(r.Name, "/") || hash == DeletedSaveHash {
			continue
		}
		out = append(out, CloudSave{Name: r.Name, Hash: hash, Bytes: r.Bytes, LastModified: parseCloudTime(r.LastModified)})
	}
	return out, nil
}

// parseCloudTime reads the object store's timestamps, which are ISO 8601
// without a zone (they are UTC) and sometimes RFC 3339.
func parseCloudTime(s string) time.Time {
	for _, layout := range []string{"2006-01-02T15:04:05.999999", "2006-01-02T15:04:05", time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// OpenCloudSave implements API. The body is the file as the game wrote it:
// Galaxy uploads saves gzip-compressed and the store hands them back that way,
// so the stream is decompressed here, after its stored form has been held
// against the checksum the store sends. A mismatch surfaces as a read error at
// the end of the stream.
func (c *Client) OpenCloudSave(ctx context.Context, client GameClient, name string) (*Download, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	req, err := c.cloudRequest(ctx, client, name)
	if err != nil {
		return nil, err
	}
	// Ask for the stored form explicitly, so the transport does not decompress
	// it behind our back before it can be checked.
	req.Header.Set("Accept-Encoding", "gzip, identity")
	resp, err := c.dl.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		c.mu.Lock()
		delete(c.gameTokens, client.ID)
		c.mu.Unlock()
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, &HTTPError{Status: resp.StatusCode, URL: req.URL.String()}
	}
	var body io.ReadCloser = resp.Body
	if etag := strings.ToLower(strings.Trim(resp.Header.Get("Etag"), `"`)); len(etag) == 32 {
		body = &checkedReader{ReadCloser: body, want: etag, h: md5.New()}
	}
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		zr, err := gzip.NewReader(body)
		if err != nil {
			body.Close()
			return nil, fmt.Errorf("decompressing %s: %w", name, err)
		}
		body = &gzipReader{Reader: zr, raw: body}
	}
	d := &Download{Body: body, Length: -1}
	if t, err := time.Parse(time.RFC3339, resp.Header.Get("X-Object-Meta-LocalLastModified")); err == nil {
		d.ModTime = t
	} else if t, err := http.ParseTime(resp.Header.Get("Last-Modified")); err == nil {
		d.ModTime = t
	}
	return d, nil
}

// ErrChecksumMismatch is the error a cloud save's stream ends with when its
// stored form did not hash to what the store said it would.
var ErrChecksumMismatch = errors.New("checksum mismatch")

// checkedReader hashes what passes through it and turns the end of the stream
// into an error when the hash is not the expected one.
type checkedReader struct {
	io.ReadCloser
	want string
	h    hash.Hash
}

func (r *checkedReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		r.h.Write(p[:n])
	}
	if err == io.EOF {
		if got := hex.EncodeToString(r.h.Sum(nil)); got != r.want {
			return n, fmt.Errorf("%w: got %s, expected %s", ErrChecksumMismatch, got, r.want)
		}
	}
	return n, err
}

// gzipReader decompresses a stream and closes the stream underneath with it.
type gzipReader struct {
	*gzip.Reader
	raw io.Closer
}

func (r *gzipReader) Close() error {
	r.Reader.Close()
	return r.raw.Close()
}
