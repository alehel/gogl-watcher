package library

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/alehel/gogl-watcher/internal/db"
	"github.com/alehel/gogl-watcher/internal/gog"
)

// Cloud saves are backed up like installers: the sync lists what a game's
// cloud storage holds and plans one file per object, the downloader fetches
// them, and a save that was rewritten in the cloud (its stored hash moved) is
// fetched again, the old copy staying in place until the new one is complete.
// Only the newest version of a save is kept, as with installers.
//
// Which container to list is decided by the game's Galaxy client, read from
// its build manifest. Games without a Galaxy build (older titles, and every
// Linux-only one) have no cloud storage at all.
const (
	// SavesDir is the folder under a game's folder that holds its cloud saves.
	SavesDir = "saves"
	// savesOS are the platforms whose builds name a game's Galaxy client, in
	// the order they are tried. Galaxy has no Linux builds.
	savesOS = "windows,mac"
	// clientRecheckInterval is how long a recorded Galaxy client is trusted.
	// Clients do not change, but a game may gain Galaxy support, so a game
	// recorded as having none is asked about again after this long.
	clientRecheckInterval = 7 * 24 * time.Hour
	// saveDownlinkScheme marks a file row's downlink as a cloud save; see
	// SaveDownlink.
	saveDownlinkScheme = "gog-cloud://"
)

// SaveDownlink is what a save file's row stores as its downlink: the client
// whose container holds it and the object's name within, which is everything
// the downloader needs to ask GOG for it.
func SaveDownlink(clientID, name string) string {
	return saveDownlinkScheme + clientID + "/" + name
}

// ParseSaveDownlink reads a downlink written by SaveDownlink.
func ParseSaveDownlink(downlink string) (clientID, name string, ok bool) {
	rest, found := strings.CutPrefix(downlink, saveDownlinkScheme)
	if !found {
		return "", "", false
	}
	clientID, name, found = strings.Cut(rest, "/")
	if !found || clientID == "" || name == "" {
		return "", "", false
	}
	return clientID, name, true
}

// SaveRelDir is the directory, relative to the game folder, a cloud save is
// stored in: the saves folder, then the object's own path with each segment
// made safe for the file system. A segment that would climb out of the folder
// is kept as a name instead.
func SaveRelDir(name string) string {
	parts := strings.Split(path.Clean("/"+path.Dir(name)), "/")
	dir := SavesDir
	for _, p := range parts {
		if p == "" || p == "." {
			continue
		}
		dir += "/" + SanitizeFilename(p)
	}
	return dir
}

// SaveFilename is the local name of a cloud save: the last segment of its
// object name, made safe for the file system.
func SaveFilename(name string) string {
	return SanitizeFilename(path.Base(name))
}

// PlanSaves turns a container listing into the files that should exist under
// the game's saves folder. The object's stored hash serves as the version:
// a rewritten save is a new version of the same file.
func PlanSaves(gameID, productID int64, clientID string, saves []gog.CloudSave) []db.File {
	files := make([]db.File, 0, len(saves))
	for _, s := range saves {
		if s.Name == "" || s.Hash == gog.DeletedSaveHash {
			continue
		}
		files = append(files, db.File{
			GameID: gameID, ProductID: productID, Kind: db.KindSave, OS: "", Language: "",
			GogID: clientID + "/" + s.Name, Name: s.Name, Version: s.Hash, Size: s.Bytes,
			Downlink: SaveDownlink(clientID, s.Name), RelDir: SaveRelDir(s.Name),
		})
	}
	return files
}

// gameClients returns the Galaxy clients of a game, looked up through its
// builds and remembered; there is usually one, shared by every OS. A game
// without any has no cloud saves.
func (s *Syncer) gameClients(ctx context.Context, game db.Game) ([]gog.GameClient, error) {
	var out []gog.GameClient
	seen := map[string]bool{}
	for _, os := range strings.Split(savesOS, ",") {
		if !game.WorksOn(os) {
			continue
		}
		rec, err := s.db.GetGameClient(ctx, game.ID, os)
		if err != nil {
			return nil, err
		}
		if rec == nil || (rec.ClientID == "" && time.Since(rec.CheckedAt) > clientRecheckInterval) {
			c, err := s.gog.GameClient(ctx, game.ID, os)
			if err != nil {
				return nil, fmt.Errorf("reading the %s build of %s: %w", os, game.Title, err)
			}
			rec = &db.GameClient{GameID: game.ID, OS: os}
			if c != nil {
				rec.ClientID, rec.ClientSecret = c.ID, c.Secret
			}
			if err := s.db.SetGameClient(ctx, game.ID, os, rec.ClientID, rec.ClientSecret); err != nil {
				return nil, err
			}
		}
		if rec.ClientID != "" && !seen[rec.ClientID] {
			seen[rec.ClientID] = true
			out = append(out, gog.GameClient{ID: rec.ClientID, Secret: rec.ClientSecret})
		}
	}
	return out, nil
}

// errSavesUnavailable is why a sync leaves a game's save rows as they are: the
// listing could not be read, so nothing is known about what changed.
var errSavesUnavailable = errors.New("cloud saves could not be listed")

// planSaves lists the game's cloud storage and plans its files under the base
// product. An error means the listing is unknown, not that there are no saves:
// the caller keeps the rows it has.
func (s *Syncer) planSaves(ctx context.Context, game db.Game, productID int64) ([]db.File, error) {
	clients, err := s.gameClients(ctx, game)
	if err != nil {
		return nil, err
	}
	var files []db.File
	for _, c := range clients {
		saves, err := s.gog.ListCloudSaves(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("%w for %s: %v", errSavesUnavailable, game.Title, err)
		}
		files = append(files, PlanSaves(game.ID, productID, c.ID, saves)...)
	}
	return files, nil
}
