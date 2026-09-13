package library

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/alehel/gogl-watcher/internal/db"
	"github.com/alehel/gogl-watcher/internal/gog"
)

// offerTTL is how long a fetched offer is served without asking GOG again. A
// user browsing back and forth between games should not spend a request per
// visit, and an offer changes about as often as an installer does.
const offerTTL = 10 * time.Minute

// Offer is what GOG has on offer for a game: every installer and extra of the
// game and its owned DLC, with the ones the current settings would download
// marked. It is a read-only look at GOG for a game that is not selected, so
// the user can see what selecting it would fetch. Nothing of it is planned or
// queued.
type Offer struct {
	FetchedAt time.Time      `json:"fetched_at"`
	Products  []OfferProduct `json:"products"`
	// Wanted totals the files the current settings would download.
	WantedFiles int   `json:"wanted_files"`
	WantedBytes int64 `json:"wanted_bytes"`
}

// OfferProduct is the game itself or one of its owned DLCs.
type OfferProduct struct {
	ID    int64       `json:"id"`
	Title string      `json:"title"`
	IsDLC bool        `json:"is_dlc"`
	Items []OfferItem `json:"items"`
}

// OfferItem is one installer (possibly in several parts) or one extra.
type OfferItem struct {
	Kind     string `json:"kind"` // "installer" or "extra"
	OS       string `json:"os"`
	Language string `json:"language"`
	Name     string `json:"name"`
	Version  string `json:"version"`
	Type     string `json:"type,omitempty"` // extras only: soundtrack, manual, ...
	Size     int64  `json:"size"`
	Files    int    `json:"files"`
	// Wanted reports whether the current settings would download this item.
	Wanted bool `json:"wanted"`
}

type cachedOffer struct {
	offer    *Offer
	settings db.Settings
}

// Offer returns what GOG offers for a game, from a short-lived cache when the
// settings have not changed since it was fetched.
func (s *Syncer) Offer(ctx context.Context, id int64) (*Offer, error) {
	if !s.gog.Authenticated() {
		return nil, errors.New("not authorized with GOG")
	}
	game, err := s.db.GetGame(ctx, id)
	if err != nil {
		return nil, err
	}
	if game == nil {
		return nil, ErrGameNotFound
	}
	settings, err := s.db.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	s.offerMu.Lock()
	c, ok := s.offers[id]
	s.offerMu.Unlock()
	if ok && time.Since(c.offer.FetchedAt) < offerTTL && c.settings.SamePlan(settings) {
		return c.offer, nil
	}
	owned, err := s.gog.OwnedIDs(ctx)
	if err != nil {
		return nil, err
	}
	p, err := s.gog.ProductDetails(ctx, id)
	if err != nil {
		return nil, err
	}
	offer := buildOffer(*game, p, owned, settings)
	s.offerMu.Lock()
	if s.offers == nil {
		s.offers = map[int64]cachedOffer{}
	}
	s.offers[id] = cachedOffer{offer: offer, settings: settings}
	s.offerMu.Unlock()
	return offer, nil
}

// ErrGameNotFound is returned for an id that is not in the library.
var ErrGameNotFound = errors.New("game not in library")

func buildOffer(game db.Game, p *gog.Product, owned gog.OwnedSet, settings db.Settings) *Offer {
	// The plan knows which files the settings pick; an item is wanted when the
	// plan contains one of its files.
	wanted := map[string]bool{}
	for _, pp := range Plan(game, p, owned, settings) {
		for _, f := range pp.Files {
			wanted[fmt.Sprintf("%d/%s", f.ProductID, f.GogID)] = true
		}
	}
	out := &Offer{FetchedAt: time.Now()}
	add := func(prod gog.Product, isDLC bool, title string) {
		op := OfferProduct{ID: prod.ID, Title: title, IsDLC: isDLC, Items: []OfferItem{}}
		for _, inst := range prod.Downloads.Installers {
			item := OfferItem{Kind: "installer", OS: db.NormalizePlatform(inst.OS), Language: db.NormalizeLanguage(inst.Language),
				Name: inst.Name, Version: inst.Version, Files: len(inst.Files)}
			for i, f := range inst.Files {
				item.Size += int64(f.Size)
				item.Wanted = item.Wanted || wanted[fmt.Sprintf("%d/%s", prod.ID, fileID(string(f.ID), inst.ID, i))]
			}
			op.Items = append(op.Items, item)
		}
		for _, b := range prod.Downloads.BonusContent {
			item := OfferItem{Kind: "extra", Name: b.Name, Type: b.Type, Files: len(b.Files)}
			for i, f := range b.Files {
				item.Size += int64(f.Size)
				item.Wanted = item.Wanted || wanted[fmt.Sprintf("%d/%s", prod.ID, fileID(string(f.ID), string(b.ID), i))]
			}
			op.Items = append(op.Items, item)
		}
		// Installers first, then extras; both in GOG's order within an OS and language.
		slices.SortStableFunc(op.Items, func(a, b OfferItem) int {
			return cmp.Or(cmp.Compare(b.Kind, a.Kind), cmp.Compare(a.OS, b.OS), cmp.Compare(a.Language, b.Language))
		})
		for _, it := range op.Items {
			if it.Wanted {
				out.WantedFiles += it.Files
				out.WantedBytes += it.Size
			}
		}
		out.Products = append(out.Products, op)
	}
	add(*p, false, cmp.Or(p.Title, game.Title))
	dlcs := slices.Clone(p.ExpandedDLCs)
	slices.SortFunc(dlcs, func(a, b gog.Product) int { return cmp.Compare(a.Title, b.Title) })
	for _, d := range dlcs {
		if d.ID == 0 || (owned != nil && !owned[d.ID]) {
			continue
		}
		add(d, true, d.Title)
	}
	return out
}
