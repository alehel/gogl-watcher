package library

import (
	"fmt"

	"github.com/alehel/gogl-watcher/internal/db"
	"github.com/alehel/gogl-watcher/internal/gog"
)

// The catalog answers "how much disk would this combination need?" without
// asking GOG again.
//
// A sync only ever stores the files the current settings want, so the database
// alone can say what dropping a platform would free but not what adding one
// would cost. GOG hands out everything it offers for a product in the single
// details request the sync makes anyway, so that answer is free to keep: the
// catalog is that request's full installer and bonus list, reduced to sizes.
//
// Estimates run the real planner over it rather than reimplementing the
// platform, language, DLC and extras rules, so an estimate and the sync it
// predicts cannot drift apart.

// CatalogOf flattens what GOG offers for a game into catalog items. owned may
// be nil, in which case all DLC count as owned; DLC and extras are always
// included, since the settings that filter them are applied when estimating.
func CatalogOf(game db.Game, p *gog.Product, owned gog.OwnedSet) []db.CatalogItem {
	out := catalogOfProduct(game.ID, p.ID, false, p.Downloads)
	for _, d := range p.ExpandedDLCs {
		if d.ID == 0 || (owned != nil && !owned[d.ID]) {
			continue
		}
		out = append(out, catalogOfProduct(game.ID, d.ID, true, d.Downloads)...)
	}
	return out
}

func catalogOfProduct(gameID, productID int64, isDLC bool, dl gog.Downloads) []db.CatalogItem {
	var out []db.CatalogItem
	for i, inst := range dl.Installers {
		it := db.CatalogItem{GameID: gameID, ProductID: productID, IsDLC: isDLC, Kind: "installer",
			ItemID: itemID(inst.ID, i), OS: db.NormalizePlatform(inst.OS), Language: db.NormalizeLanguage(inst.Language), Seq: i}
		for _, f := range inst.Files {
			it.Files++
			it.Bytes += int64(f.Size)
		}
		out = append(out, it)
	}
	for i, b := range dl.BonusContent {
		it := db.CatalogItem{GameID: gameID, ProductID: productID, IsDLC: isDLC, Kind: "extra",
			ItemID: itemID(string(b.ID), i), Seq: i}
		for _, f := range b.Files {
			it.Files++
			it.Bytes += int64(f.Size)
		}
		out = append(out, it)
	}
	return out
}

// itemID falls back to the position when GOG gives an item no id, so that two
// items of one product never collide on the catalog's primary key.
func itemID(id string, i int) string {
	if id != "" {
		return id
	}
	return fmt.Sprintf("_%d", i)
}

// Estimate reports what settings s would download of the catalog: every game it
// covers, whether or not the game is selected for download. Sizes are GOG's
// manifest figures, which are close to but not exactly the bytes that arrive.
func Estimate(items []db.CatalogItem, s db.Settings) (files int, bytes int64) {
	for _, pi := range groupByProduct(items) {
		if pi.isDLC && !s.IncludeDLC {
			continue
		}
		for _, f := range planFiles(pi.gameID, pi.productID, pi.downloads, "", s) {
			files++
			bytes += f.Size
		}
	}
	return files, bytes
}

// productItems is one product's catalog rebuilt into the shape the planner takes.
type productItems struct {
	gameID    int64
	productID int64
	isDLC     bool
	downloads gog.Downloads
}

// groupByProduct rebuilds the installer and bonus lists the catalog was made
// from. Each item becomes one entry whose first file carries all of its bytes:
// the planner keeps or drops whole items, so that reproduces both the file
// count and the byte total exactly, which is all an estimate reads.
func groupByProduct(items []db.CatalogItem) []*productItems {
	var out []*productItems
	type key struct{ game, product int64 }
	byProduct := map[key]*productItems{}
	for _, it := range items {
		k := key{it.GameID, it.ProductID}
		p := byProduct[k]
		if p == nil {
			p = &productItems{gameID: it.GameID, productID: it.ProductID, isDLC: it.IsDLC}
			byProduct[k] = p
			out = append(out, p)
		}
		switch it.Kind {
		case "extra":
			p.downloads.BonusContent = append(p.downloads.BonusContent, gog.Bonus{
				ID: gog.FlexString(it.ItemID), Count: gog.FlexInt(it.Files), TotalSize: gog.FlexInt(it.Bytes),
				Files: splitFiles(it.Files, it.Bytes),
			})
		default:
			p.downloads.Installers = append(p.downloads.Installers, gog.Installer{
				ID: it.ItemID, OS: it.OS, Language: it.Language, TotalSize: gog.FlexInt(it.Bytes),
				Files: splitFiles(it.Files, it.Bytes),
			})
		}
	}
	return out
}

// splitFiles returns n files that together weigh bytes.
func splitFiles(n int, bytes int64) []gog.DownloadFile {
	files := make([]gog.DownloadFile, 0, n)
	for i := 0; i < n; i++ {
		size := int64(0)
		if i == 0 {
			size = bytes
		}
		files = append(files, gog.DownloadFile{ID: gog.FlexString(fmt.Sprintf("_%d", i)), Size: gog.FlexInt(size)})
	}
	return files
}
