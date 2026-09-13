package gog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Mock is an in-memory GOG implementation with a sample library. It is used for
// demos (MOCK_GOG=1) and tests. Downloads stream deterministic bytes.
type Mock struct {
	store TokenStore
	mu    sync.Mutex
	token Token
	// Speed limits each mock download in bytes/second (0 = unlimited).
	Speed int64
	// Games is the sample library.
	Games []MockGame
	// FailDownlinks contains downlink ids whose resolution fails.
	FailDownlinks map[string]bool
	// Builds are the Galaxy builds to report, keyed by "<product id>/<os>".
	// A missing entry means GOG publishes no build, as for most Linux games.
	Builds map[string]*Build
	// Checksums are the MD5 sums to publish, keyed by downlink. A downlink with
	// no entry has no checksum, which is what GOG does for most extras.
	Checksums map[string]string
}

// MockGame is a sample game definition.
type MockGame struct {
	Listed  ListedGame
	Product Product
}

// NewMock creates a mock with the default sample library.
func NewMock(ctx context.Context, store TokenStore) (*Mock, error) {
	m := &Mock{store: store, Speed: 6 << 20, FailDownlinks: map[string]bool{}, Builds: map[string]*Build{}, Checksums: map[string]string{}}
	if store != nil {
		t, err := store.Load(ctx)
		if err != nil {
			return nil, err
		}
		m.token = t
	}
	m.Games = SampleLibrary()
	m.FailDownlinks[MockDownlink(1207659212, "en1installer1", "setup_broken_sword_directors_cut_2.0-1.bin", 20*mb)] = true // "Broken Sword" part 2 fails on purpose
	return m, nil
}

func (m *Mock) AuthURL() string {
	return "https://auth.gog.com/auth?client_id=46899977096215655&redirect_uri=https%3A%2F%2Fembed.gog.com%2Fon_login_success%3Forigin%3Dclient&response_type=code&layout=client2"
}

func (m *Mock) ExchangeCode(ctx context.Context, code string) (*User, error) {
	code = ExtractCode(code)
	if code == "" {
		return nil, errors.New("no authorization code provided")
	}
	if strings.EqualFold(code, "bad") {
		return nil, &AuthError{Msg: "GOG rejected the request: invalid_grant", Permanent: true}
	}
	m.mu.Lock()
	m.token = Token{AccessToken: "mock-access", RefreshToken: "mock-refresh", ExpiresAt: time.Now().Add(time.Hour), UserID: "42", Username: "demo_user"}
	t := m.token
	m.mu.Unlock()
	if m.store != nil {
		if err := m.store.Save(ctx, t); err != nil {
			return nil, err
		}
	}
	return &User{ID: t.UserID, Username: t.Username}, nil
}

func (m *Mock) Authenticated() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.token.RefreshToken != "" && m.token.Error == ""
}

func (m *Mock) AuthError() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.token.Error
}

func (m *Mock) CurrentUser() *User {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token.RefreshToken == "" {
		return nil
	}
	return &User{ID: m.token.UserID, Username: m.token.Username}
}

func (m *Mock) Logout(ctx context.Context) error {
	m.mu.Lock()
	m.token = Token{}
	m.mu.Unlock()
	if m.store != nil {
		return m.store.Clear(ctx)
	}
	return nil
}

func (m *Mock) requireAuth() error {
	if !m.Authenticated() {
		return &AuthError{Msg: "not authorized with GOG", Permanent: true}
	}
	return nil
}

func (m *Mock) OwnedIDs(ctx context.Context) (OwnedSet, error) {
	if err := m.requireAuth(); err != nil {
		return nil, err
	}
	set := OwnedSet{}
	for _, g := range m.Games {
		set[g.Listed.ID] = true
		for _, d := range g.Product.ExpandedDLCs {
			set[d.ID] = true
		}
	}
	return set, nil
}

func (m *Mock) ListGames(ctx context.Context, progress func(page, total int)) ([]ListedGame, error) {
	if err := m.requireAuth(); err != nil {
		return nil, err
	}
	var out []ListedGame
	for _, g := range m.Games {
		out = append(out, g.Listed)
	}
	if progress != nil {
		progress(1, 1)
	}
	return out, nil
}

func (m *Mock) ProductDetails(ctx context.Context, id int64) (*Product, error) {
	if err := m.requireAuth(); err != nil {
		return nil, err
	}
	for _, g := range m.Games {
		if g.Listed.ID == id {
			p := g.Product
			return &p, nil
		}
	}
	return nil, &HTTPError{Status: 404, URL: fmt.Sprintf("mock://%d", id)}
}

// BoxArt implements API. The mock's listing image is already a 2:3 cover.
func (m *Mock) BoxArt(ctx context.Context, id int64) (string, error) {
	return "", nil
}

// SetBuild registers the Galaxy build reported for a product and OS.
func (m *Mock) SetBuild(productID int64, os, buildID, version string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Builds[fmt.Sprintf("%d/%s", productID, os)] = &Build{ID: buildID, VersionName: version, Public: true, PublishedAt: time.Now()}
}

func (m *Mock) LatestBuild(ctx context.Context, productID int64, os string) (*Build, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.Builds[fmt.Sprintf("%d/%s", productID, os)]
	if b == nil {
		return nil, nil
	}
	cp := *b
	return &cp, nil
}

func (m *Mock) ResolveDownlink(ctx context.Context, downlink string) (*Downlink, error) {
	if err := m.requireAuth(); err != nil {
		return nil, err
	}
	if m.FailDownlinks[downlink] {
		return nil, &HTTPError{Status: 403, URL: downlink}
	}
	d := &Downlink{URL: downlink}
	m.mu.Lock()
	if _, ok := m.Checksums[downlink]; ok {
		d.ChecksumURL = mockChecksumPrefix + downlink
	}
	m.mu.Unlock()
	return d, nil
}

// mockChecksumPrefix marks a checksum URL that FetchChecksum resolves against
// the registered sums.
const mockChecksumPrefix = "mock-checksum://"

func (m *Mock) FetchChecksum(ctx context.Context, u string) (*Checksum, error) {
	if strings.HasPrefix(u, mockChecksumPrefix) {
		m.mu.Lock()
		sum, ok := m.Checksums[strings.TrimPrefix(u, mockChecksumPrefix)]
		m.mu.Unlock()
		if ok {
			return &Checksum{MD5: sum}, nil
		}
	}
	return nil, &HTTPError{Status: 404, URL: u}
}

// OpenDownload serves the mock URL "mock://<product>/<fileid>/<filename>?size=N".
func (m *Mock) OpenDownload(ctx context.Context, u string, offset int64) (*Download, error) {
	parsed, err := url.Parse(u)
	if err != nil {
		return nil, err
	}
	size, _ := strconv.ParseInt(parsed.Query().Get("size"), 10, 64)
	name := FilenameFromURL(u)
	if offset > size {
		return nil, ErrRangeNotSatisfiable
	}
	body := &mockBody{ctx: ctx, remaining: size - offset, pos: offset, speed: m.Speed}
	return &Download{Body: body, Offset: offset, Length: size, Filename: name}, nil
}

type mockBody struct {
	ctx       context.Context
	remaining int64
	pos       int64
	speed     int64
	last      time.Time
}

func (b *mockBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, io.EOF
	}
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	n := len(p)
	if int64(n) > b.remaining {
		n = int(b.remaining)
	}
	if b.speed > 0 {
		// Pace the stream: n bytes should take n/speed seconds.
		if !b.last.IsZero() {
			wait := time.Duration(float64(n) / float64(b.speed) * float64(time.Second))
			select {
			case <-time.After(wait):
			case <-b.ctx.Done():
				return 0, b.ctx.Err()
			}
		}
		b.last = time.Now()
	}
	for i := 0; i < n; i++ {
		p[i] = byte((b.pos + int64(i)) * 31 % 251)
	}
	b.pos += int64(n)
	b.remaining -= int64(n)
	return n, nil
}

func (b *mockBody) Close() error { return nil }

// MockDownlink builds the downlink used by the sample library.
func MockDownlink(productID int64, fileID, name string, size int64) string {
	return fmt.Sprintf("mock://%d/%s/%s?size=%d", productID, fileID, url.PathEscape(name), size)
}

const (
	mb = 1 << 20
)

type sampleInstaller struct {
	os, lang, version string
	parts             []int64 // sizes
}

func sampleProduct(id int64, title, slug string, installers []sampleInstaller, extras map[string]int64) Product {
	p := Product{ID: id, Title: title, Slug: slug}
	osCode := map[string]string{"windows": "1", "mac": "2", "linux": "3"}
	for _, in := range installers {
		inst := Installer{ID: fmt.Sprintf("installer_%s_%s", in.os, in.lang), Name: title, OS: in.os, Language: in.lang, Version: in.version}
		inst.LanguageFull = map[string]string{"en": "English", "de": "Deutsch", "fr": "français"}[in.lang]
		for i, sz := range in.parts {
			fid := fmt.Sprintf("%s%sinstaller%d", in.lang, osCode[in.os], i)
			ext := ".exe"
			if i > 0 && in.os == "windows" {
				ext = fmt.Sprintf("-%d.bin", i)
			}
			if in.os == "linux" {
				ext = ".sh"
			}
			if in.os == "mac" {
				ext = ".pkg"
			}
			name := fmt.Sprintf("setup_%s_%s%s", slug, strings.ReplaceAll(in.version, " ", "_"), ext)
			inst.Files = append(inst.Files, DownloadFile{ID: FlexString(fid), Size: FlexInt(sz), Downlink: MockDownlink(id, fid, name, sz)})
			inst.TotalSize += FlexInt(sz)
		}
		p.Downloads.Installers = append(p.Downloads.Installers, inst)
	}
	// Deterministic ids: map iteration order changes between runs and the ids are
	// what the database keys files by.
	names := make([]string, 0, len(extras))
	for name := range extras {
		names = append(names, name)
	}
	sort.Strings(names)
	for i, name := range names {
		sz := extras[name]
		fid := fmt.Sprintf("%d", 5001+i)
		fname := strings.ToLower(strings.ReplaceAll(name, " ", "_")) + ".zip"
		p.Downloads.BonusContent = append(p.Downloads.BonusContent, Bonus{ID: FlexString(fid), Name: name, Type: "soundtrack", Count: 1, TotalSize: FlexInt(sz),
			Files: []DownloadFile{{ID: FlexString(fid), Size: FlexInt(sz), Downlink: MockDownlink(id, fid, fname, sz)}}})
	}
	return p
}

// MockCover renders a simple 2:3 SVG cover for a title as a data URI so the UI can
// show artwork without network access.
func MockCover(title string) string {
	var h uint32 = 2166136261
	for i := 0; i < len(title); i++ {
		h = (h ^ uint32(title[i])) * 16777619
	}
	hue := int(h % 360)
	words := strings.Fields(title)
	var lines []string
	cur := ""
	for _, w := range words {
		if cur != "" && len(cur)+1+len(w) > 16 {
			lines = append(lines, cur)
			cur = w
			continue
		}
		if cur == "" {
			cur = w
		} else {
			cur += " " + w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) > 4 {
		lines = lines[:4]
	}
	var text strings.Builder
	y := 300 - (len(lines)-1)*17
	for _, l := range lines {
		esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "'", "&apos;", `"`, "&quot;").Replace(l)
		fmt.Fprintf(&text, `<text x="24" y="%d" font-family="Helvetica,Arial,sans-serif" font-size="26" font-weight="700" fill="#fff">%s</text>`, y, esc)
		y += 34
	}
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="300" height="450" viewBox="0 0 300 450">`+
		`<rect width="300" height="450" fill="hsl(%d,35%%,22%%)"/>`+
		`<circle cx="230" cy="110" r="140" fill="hsl(%d,45%%,32%%)"/>`+
		`<rect y="250" width="300" height="200" fill="rgba(0,0,0,0.35)"/>%s</svg>`, hue, (hue+40)%360, text.String())
	return "data:image/svg+xml;charset=utf-8," + url.PathEscape(svg)
}

// SampleLibrary returns the demo library.
func SampleLibrary() []MockGame {
	// A few games carry the user's own gog.com tags, so the tag filter has
	// something to show.
	tags := map[int64][]string{
		1207658924: {"Completed", "Favorite"},
		1207664663: {"Favorite"},
		1207659212: {"Backlog"},
	}
	mk := func(id int64, title, slug string, win, mac, lin bool, p Product, dlcs ...Product) MockGame {
		p.ExpandedDLCs = dlcs
		return MockGame{
			Listed:  ListedGame{ID: id, Title: title, Slug: slug, Image: MockCover(title), WorksWindows: win, WorksMac: mac, WorksLinux: lin, Tags: tags[id]},
			Product: p,
		}
	}
	return []MockGame{
		mk(1207658924, "The Witcher: Enhanced Edition", "the_witcher", true, true, false,
			sampleProduct(1207658924, "The Witcher: Enhanced Edition", "the_witcher",
				[]sampleInstaller{{"windows", "en", "1.5 (A)", []int64{8 * mb, 40 * mb, 40 * mb}}, {"windows", "de", "1.5 (A)", []int64{8 * mb, 40 * mb}}, {"mac", "en", "1.5", []int64{45 * mb}}},
				map[string]int64{"Soundtrack": 12 * mb, "Manual": 2 * mb})),
		mk(1207664663, "Stardew Valley", "stardew_valley", true, true, true,
			sampleProduct(1207664663, "Stardew Valley", "stardew_valley",
				[]sampleInstaller{{"windows", "en", "1.6.15", []int64{30 * mb}}, {"linux", "en", "1.6.15", []int64{32 * mb}}, {"mac", "en", "1.6.15", []int64{31 * mb}}},
				map[string]int64{"Soundtrack": 25 * mb})),
		mk(1207659212, "Broken Sword: Director's Cut", "broken_sword_directors_cut", true, true, false,
			sampleProduct(1207659212, "Broken Sword: Director's Cut", "broken_sword_directors_cut",
				[]sampleInstaller{{"windows", "en", "2.0", []int64{6 * mb, 20 * mb}}, {"mac", "en", "2.0", []int64{24 * mb}}},
				nil)),
		mk(1207666633, "Cyberpunk 2077", "cyberpunk_2077", true, false, false,
			sampleProduct(1207666633, "Cyberpunk 2077", "cyberpunk_2077",
				[]sampleInstaller{{"windows", "en", "2.21", []int64{9 * mb, 60 * mb, 60 * mb, 60 * mb}}},
				map[string]int64{"Soundtrack": 40 * mb, "Artbook": 15 * mb}),
			sampleProduct(1207666634, "Cyberpunk 2077: Phantom Liberty", "cyberpunk_2077_phantom_liberty",
				[]sampleInstaller{{"windows", "en", "2.21", []int64{5 * mb, 50 * mb}}}, map[string]int64{"Phantom Liberty Soundtrack": 18 * mb})),
		mk(1207661673, "Baldur's Gate II: Enhanced Edition", "baldurs_gate_2_enhanced_edition", true, true, true,
			sampleProduct(1207661673, "Baldur's Gate II: Enhanced Edition", "baldurs_gate_2_enhanced_edition",
				[]sampleInstaller{{"windows", "en", "2.6.6.0", []int64{7 * mb, 55 * mb}}, {"linux", "en", "2.6.6.0", []int64{58 * mb}}, {"mac", "en", "2.6.6.0", []int64{57 * mb}}},
				map[string]int64{"Soundtrack": 20 * mb})),
		mk(1207658695, "Heroes of Might and Magic III: Complete", "heroes_of_might_and_magic_3_complete_edition", true, false, false,
			sampleProduct(1207658695, "Heroes of Might and Magic III: Complete", "heroes_of_might_and_magic_3_complete_edition",
				[]sampleInstaller{{"windows", "en", "4.0", []int64{25 * mb}}, {"windows", "fr", "4.0", []int64{25 * mb}}},
				map[string]int64{"Manual": 3 * mb})),
		mk(1207660063, "Disco Elysium: The Final Cut", "disco_elysium", true, true, false,
			sampleProduct(1207660063, "Disco Elysium: The Final Cut", "disco_elysium",
				[]sampleInstaller{{"windows", "en", "d5cc8ad9", []int64{6 * mb, 48 * mb}}, {"mac", "en", "d5cc8ad9", []int64{50 * mb}}},
				map[string]int64{"Soundtrack": 30 * mb})),
		mk(1207662883, "Hollow Knight", "hollow_knight", true, true, true,
			sampleProduct(1207662883, "Hollow Knight", "hollow_knight",
				[]sampleInstaller{{"windows", "en", "1.5.78.11833", []int64{28 * mb}}, {"linux", "en", "1.5.78.11833", []int64{29 * mb}}, {"mac", "en", "1.5.78.11833", []int64{28 * mb}}},
				map[string]int64{"Soundtrack": 22 * mb})),
		mk(1207667043, "Divinity: Original Sin 2 - Definitive Edition", "divinity_original_sin_2", true, true, false,
			sampleProduct(1207667043, "Divinity: Original Sin 2 - Definitive Edition", "divinity_original_sin_2",
				[]sampleInstaller{{"windows", "en", "3.6.117.3735", []int64{8 * mb, 52 * mb, 52 * mb}}, {"mac", "en", "3.6.117.3735", []int64{60 * mb}}},
				nil)),
		mk(1207661953, "FTL: Faster Than Light", "faster_than_light", true, true, true,
			sampleProduct(1207661953, "FTL: Faster Than Light", "faster_than_light",
				[]sampleInstaller{{"windows", "en", "1.6.14", []int64{14 * mb}}, {"linux", "en", "1.6.14", []int64{15 * mb}}, {"mac", "en", "1.6.14", []int64{14 * mb}}},
				map[string]int64{"Soundtrack": 9 * mb})),
		mk(1207665073, "Return of the Obra Dinn", "return_of_the_obra_dinn", true, true, false,
			sampleProduct(1207665073, "Return of the Obra Dinn", "return_of_the_obra_dinn",
				[]sampleInstaller{{"windows", "en", "1.2.111", []int64{22 * mb}}, {"mac", "en", "1.2.111", []int64{23 * mb}}}, nil)),
		mk(1207663943, "Outer Wilds", "outer_wilds", true, false, false,
			sampleProduct(1207663943, "Outer Wilds", "outer_wilds",
				[]sampleInstaller{{"windows", "en", "1.1.15", []int64{5 * mb, 45 * mb}}}, map[string]int64{"Soundtrack": 26 * mb}),
			sampleProduct(1207663944, "Outer Wilds: Echoes of the Eye", "outer_wilds_echoes_of_the_eye",
				[]sampleInstaller{{"windows", "en", "1.1.15", []int64{3 * mb, 30 * mb}}}, nil)),
		mk(1207669193, "Pentiment", "pentiment", true, false, false,
			sampleProduct(1207669193, "Pentiment", "pentiment",
				[]sampleInstaller{{"windows", "de", "1.0.3", []int64{21 * mb}}}, nil)),
		mk(1207658930, "Quake", "quake", true, false, false,
			sampleProduct(1207658930, "Quake", "quake",
				[]sampleInstaller{{"linux", "en", "1.0", []int64{9 * mb}}}, nil)),
	}
}
