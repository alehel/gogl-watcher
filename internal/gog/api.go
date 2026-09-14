// Package gog talks to GOG.com using the same endpoints as the GOG Galaxy client.
package gog

import (
	"context"
	"io"
	"time"
)

// API is the subset of GOG functionality the application needs. The real Client
// implements it; a mock implementation is used for demos and tests.
type API interface {
	// AuthURL returns the login page the user opens in a browser.
	AuthURL() string
	// ExchangeCode turns the code (or redirect URL) pasted by the user into tokens.
	ExchangeCode(ctx context.Context, code string) (*User, error)
	// Authenticated reports whether a usable refresh token is stored.
	Authenticated() bool
	// AuthError returns the last token error, if any.
	AuthError() string
	// CurrentUser returns the stored account, if authenticated.
	CurrentUser() *User
	// Logout drops the stored tokens.
	Logout(ctx context.Context) error

	// OwnedIDs returns every owned product id (games and DLC).
	OwnedIDs(ctx context.Context) (OwnedSet, error)
	// ListGames returns all base games in the account.
	ListGames(ctx context.Context, progress func(page, total int)) ([]ListedGame, error)
	// ProductDetails returns downloads for a game and its DLC.
	ProductDetails(ctx context.Context, id int64) (*Product, error)
	// BoxArt returns the URL of the portrait cover of a game, or "" when GOG
	// has none. The game list only carries the landscape store tile.
	BoxArt(ctx context.Context, id int64) (string, error)
	// LatestBuild returns the newest public Galaxy build of a product for one OS,
	// or nil when GOG publishes none (common for Linux builds and for games that
	// predate Galaxy). It is a change hint only; see library.Syncer.
	LatestBuild(ctx context.Context, productID int64, os string) (*Build, error)
	// ResolveDownlink turns an API downlink into a CDN URL.
	ResolveDownlink(ctx context.Context, downlink string) (*Downlink, error)
	// FetchChecksum reads the checksum XML, if available.
	FetchChecksum(ctx context.Context, url string) (*Checksum, error)
	// OpenDownload starts a (possibly ranged) download of a CDN URL.
	OpenDownload(ctx context.Context, url string, offset int64) (*Download, error)

	// GameClient returns the OAuth client of the game's Galaxy build for one OS
	// ("windows" or "mac"), which names its cloud storage, or nil when the game
	// has no Galaxy build there and so no cloud saves.
	GameClient(ctx context.Context, productID int64, os string) (*GameClient, error)
	// ListCloudSaves lists the files in the game's cloud storage container.
	ListCloudSaves(ctx context.Context, client GameClient) ([]CloudSave, error)
	// OpenCloudSave starts the download of one cloud save, decompressed and
	// checked against the store's checksum (a mismatch ends the body with
	// ErrChecksumMismatch).
	OpenCloudSave(ctx context.Context, client GameClient, name string) (*Download, error)
}

// Download is an open transfer.
type Download struct {
	Body io.ReadCloser
	// Offset is the byte offset the body starts at (0 if the server ignored the range).
	Offset int64
	// Length is the total file length if known, else -1.
	Length int64
	// Filename from Content-Disposition, if any.
	Filename string
	// ModTime is when the file was last written where it came from, if known
	// (cloud saves carry it); zero otherwise.
	ModTime time.Time
}
