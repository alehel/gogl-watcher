// Package downloader transfers wanted files from GOG's CDN with concurrency and
// bandwidth limits, resumable partial files and checksum verification.
package downloader

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/time/rate"

	"github.com/alehel/gogl-watcher/internal/db"
	"github.com/alehel/gogl-watcher/internal/gog"
	"github.com/alehel/gogl-watcher/internal/library"
)

const (
	readChunk   = 64 << 10
	minBurst    = 256 << 10
	maxAttempts = 4
)

// Progress is the live state of one transfer.
type Progress struct {
	FileID     int64
	GameID     int64
	GameTitle  string
	Filename   string
	Size       int64
	Downloaded int64
	SpeedBps   float64
	StartedAt  time.Time
}

type transfer struct {
	file       db.File
	gameTitle  string
	filename   string
	size       atomic.Int64
	downloaded atomic.Int64
	speed      atomic.Int64 // bytes/s, updated once a second
	startedAt  time.Time
	cancel     context.CancelFunc
}

// Manager runs the download queue.
type Manager struct {
	db    *db.DB
	gog   gog.API
	paths library.Paths
	log   *slog.Logger

	limiter *rate.Limiter
	wake    chan struct{}

	mu        sync.Mutex
	active    map[int64]*transfer
	maxActive int
	paused    bool
	enabled   bool // setup complete
	rateLimit int64
}

// New creates a manager. Call Run to start it.
func New(d *db.DB, g gog.API, paths library.Paths, log *slog.Logger) *Manager {
	return &Manager{
		db: d, gog: g, paths: paths, log: log.With("component", "download"),
		limiter: rate.NewLimiter(rate.Inf, minBurst), wake: make(chan struct{}, 1),
		active: map[int64]*transfer{}, maxActive: 2,
	}
}

// Configure applies concurrency, speed and pause settings at runtime.
func (m *Manager) Configure(s db.Settings, enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maxActive = s.MaxConcurrentDownloads
	m.paused = s.DownloadsPaused
	m.enabled = enabled
	limit := int64(s.SpeedLimitKBps) * 1024
	if limit != m.rateLimit {
		m.rateLimit = limit
		if limit <= 0 {
			m.limiter.SetLimit(rate.Inf)
			m.limiter.SetBurst(minBurst)
		} else {
			burst := int(limit)
			if burst < minBurst {
				burst = minBurst
			}
			m.limiter.SetLimit(rate.Limit(limit))
			m.limiter.SetBurst(burst)
		}
	}
	if m.paused {
		for _, t := range m.active {
			t.cancel()
		}
	}
	m.Wake()
}

// Wake nudges the scheduler loop to look for work.
func (m *Manager) Wake() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Paused reports the pause flag.
func (m *Manager) Paused() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.paused
}

// SetPaused pauses or resumes downloads and persists the flag.
func (m *Manager) SetPaused(ctx context.Context, paused bool) error {
	s, err := m.db.GetSettings(ctx)
	if err != nil {
		return err
	}
	s.DownloadsPaused = paused
	if err := m.db.SaveSettings(ctx, s); err != nil {
		return err
	}
	m.mu.Lock()
	enabled := m.enabled
	m.mu.Unlock()
	m.Configure(s, enabled)
	if paused {
		m.log.Info("downloads paused")
	} else {
		m.log.Info("downloads resumed")
	}
	return nil
}

// Active returns a snapshot of running transfers.
func (m *Manager) Active() []Progress {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Progress, 0, len(m.active))
	for _, t := range m.active {
		out = append(out, Progress{
			FileID: t.file.ID, GameID: t.file.GameID, GameTitle: t.gameTitle, Filename: t.filename,
			Size: t.size.Load(), Downloaded: t.downloaded.Load(), SpeedBps: float64(t.speed.Load()), StartedAt: t.startedAt,
		})
	}
	return out
}

// ProgressFor returns the transfer state of one file, if active.
func (m *Manager) ProgressFor(fileID int64) (Progress, bool) {
	for _, p := range m.Active() {
		if p.FileID == fileID {
			return p, true
		}
	}
	return Progress{}, false
}

// Cancel aborts the transfer of a file if it is running.
func (m *Manager) Cancel(fileID int64) {
	m.mu.Lock()
	t := m.active[fileID]
	m.mu.Unlock()
	if t != nil {
		t.cancel()
	}
}

// Run drives the queue until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		m.fill(ctx)
		select {
		case <-ctx.Done():
			m.mu.Lock()
			for _, t := range m.active {
				t.cancel()
			}
			m.mu.Unlock()
			return
		case <-ticker.C:
		case <-m.wake:
		}
	}
}

func (m *Manager) fill(ctx context.Context) {
	m.mu.Lock()
	if m.paused || !m.enabled || !m.gog.Authenticated() {
		m.mu.Unlock()
		return
	}
	free := m.maxActive - len(m.active)
	exclude := make([]int64, 0, len(m.active))
	for id := range m.active {
		exclude = append(exclude, id)
	}
	m.mu.Unlock()
	if free <= 0 {
		return
	}
	files, err := m.db.NextPendingFiles(ctx, free, exclude)
	if err != nil {
		m.log.Error("could not read download queue", "error", err)
		return
	}
	for _, f := range files {
		m.startTransfer(ctx, f)
	}
}

func (m *Manager) startTransfer(parent context.Context, f db.File) {
	game, err := m.db.GetGame(parent, f.GameID)
	if err != nil || game == nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	t := &transfer{file: f, gameTitle: game.Title, filename: f.Filename, startedAt: time.Now(), cancel: cancel}
	t.size.Store(f.Size)
	m.mu.Lock()
	if _, exists := m.active[f.ID]; exists || len(m.active) >= m.maxActive {
		m.mu.Unlock()
		cancel()
		return
	}
	m.active[f.ID] = t
	m.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			m.mu.Lock()
			delete(m.active, f.ID)
			m.mu.Unlock()
			m.Wake()
		}()
		err := m.download(ctx, t, *game)
		switch {
		case err == nil:
		case errors.Is(err, context.Canceled):
			m.log.Info("download interrupted", "game", game.Title, "file", t.filename)
		default:
			m.fail(parent, t, game.Title, err)
		}
	}()
}

func (m *Manager) fail(ctx context.Context, t *transfer, title string, err error) {
	f, _ := m.db.GetFile(ctx, t.file.ID)
	attempts := t.file.Attempts
	if f != nil {
		attempts = f.Attempts
	}
	var ae *gog.AuthError
	permanent := errors.As(err, &ae) && ae.Permanent
	var he *gog.HTTPError
	if errors.As(err, &he) && (he.Status == 403 || he.Status == 404) {
		permanent = true
	}
	if attempts+1 >= maxAttempts || permanent {
		m.log.Error("download failed", "game", title, "file", firstNonEmpty(t.filename, t.file.Name), "error", err)
		_ = m.db.SetFileError(ctx, t.file.ID, err.Error(), 0)
		return
	}
	wait := time.Duration(30*(1<<uint(attempts))) * time.Second
	m.log.Warn("download failed, will retry", "game", title, "file", firstNonEmpty(t.filename, t.file.Name), "retry_in", wait, "error", err)
	_ = m.db.SetFileError(ctx, t.file.ID, err.Error(), wait)
}

func (m *Manager) download(ctx context.Context, t *transfer, game db.Game) error {
	f := t.file
	link, err := m.gog.ResolveDownlink(ctx, f.Downlink)
	if err != nil {
		return fmt.Errorf("resolving download link: %w", err)
	}
	var expectedMD5 string
	var expectedSize int64 = f.Size
	filename := gog.FilenameFromURL(link.URL)
	if link.ChecksumURL != "" {
		if cs, err := m.gog.FetchChecksum(ctx, link.ChecksumURL); err == nil {
			expectedMD5 = strings.ToLower(cs.MD5)
			if cs.TotalSize > 0 {
				expectedSize = cs.TotalSize
			}
			if cs.Name != "" {
				filename = cs.Name
			}
		} else {
			m.log.Debug("no checksum available", "file", f.Name, "error", err)
		}
	}
	// Open the transfer first so a Content-Disposition name can be used when the URL has none.
	partPath := ""
	var offset int64
	openWithOffset := func(off int64) (*gog.Download, error) {
		return m.gog.OpenDownload(ctx, link.URL, off)
	}
	dl, err := openWithOffset(0)
	if err != nil {
		return fmt.Errorf("opening download: %w", err)
	}
	if filename == "" {
		filename = dl.Filename
	}
	if filename == "" {
		filename = fmt.Sprintf("%s_%s", library.SanitizeFolder(f.Name), f.GogID)
	}
	filename = library.SanitizeFolder(filename)
	if dl.Length > 0 {
		expectedSize = dl.Length
	}
	rel := library.LocalRelPath(game.Folder, f.RelDir, filename)
	abs := m.paths.Abs(rel)
	partPath = abs + ".part"
	t.filename = filename
	t.size.Store(expectedSize)
	if err := m.db.SetFileResolved(ctx, f.ID, filename, rel, expectedMD5, expectedSize); err != nil {
		dl.Body.Close()
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		dl.Body.Close()
		return err
	}
	// Already complete on disk (e.g. after a database reset)? Verify and finish.
	if st, err := os.Stat(abs); err == nil && st.Size() == expectedSize && expectedSize > 0 {
		dl.Body.Close()
		if expectedMD5 == "" || fileMD5(abs) == expectedMD5 {
			m.log.Info("file already present, skipping download", "game", game.Title, "file", filename)
			return m.db.SetFileDone(ctx, f.ID, rel, expectedSize)
		}
		_ = os.Remove(abs)
	}
	// Resume a partial file when possible.
	if st, err := os.Stat(partPath); err == nil && st.Size() > 0 && st.Size() < expectedSize {
		dl.Body.Close()
		offset = st.Size()
		dl, err = openWithOffset(offset)
		if errors.Is(err, gog.ErrRangeNotSatisfiable) || (err == nil && dl.Offset != offset) {
			if dl != nil {
				dl.Body.Close()
			}
			offset = 0
			_ = os.Remove(partPath)
			dl, err = openWithOffset(0)
		}
		if err != nil {
			return fmt.Errorf("resuming download: %w", err)
		}
	} else if err == nil {
		_ = os.Remove(partPath)
	}
	defer dl.Body.Close()
	if err := m.checkSpace(abs, expectedSize-offset); err != nil {
		return err
	}
	flags := os.O_CREATE | os.O_WRONLY
	if offset > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	out, err := os.OpenFile(partPath, flags, 0o644)
	if err != nil {
		return err
	}
	var hasher hash.Hash
	if expectedMD5 != "" {
		hasher = md5.New()
		if offset > 0 {
			// Hash the part we already have.
			if err := hashFile(partPath, hasher, offset); err != nil {
				out.Close()
				return err
			}
		}
	}
	t.downloaded.Store(offset)
	if offset > 0 {
		m.log.Info("resuming download", "game", game.Title, "file", filename, "offset", offset)
	} else {
		m.log.Info("downloading", "game", game.Title, "file", filename, "size", expectedSize)
	}
	stopSpeed := m.trackSpeed(ctx, t)
	written, err := m.copy(ctx, out, dl.Body, hasher, t)
	stopSpeed()
	if cerr := out.Close(); err == nil && cerr != nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	total := offset + written
	if expectedSize > 0 && total != expectedSize {
		_ = os.Remove(partPath)
		return fmt.Errorf("size mismatch: got %d bytes, expected %d", total, expectedSize)
	}
	if hasher != nil {
		if got := hex.EncodeToString(hasher.Sum(nil)); got != expectedMD5 {
			_ = os.Remove(partPath)
			return fmt.Errorf("checksum mismatch: got %s, expected %s", got, expectedMD5)
		}
	}
	if err := os.Rename(partPath, abs); err != nil {
		return err
	}
	if f.PreviousPath != "" && f.PreviousPath != rel {
		if err := m.paths.Remove(f.PreviousPath); err == nil {
			m.log.Info("removed superseded file", "path", f.PreviousPath)
		}
	}
	m.log.Info("download complete", "game", game.Title, "file", filename, "size", total)
	return m.db.SetFileDone(ctx, f.ID, rel, total)
}

func (m *Manager) copy(ctx context.Context, dst io.Writer, src io.Reader, h hash.Hash, t *transfer) (int64, error) {
	buf := make([]byte, readChunk)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			if err := m.limiter.WaitN(ctx, n); err != nil {
				return written, err
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return written, werr
			}
			if h != nil {
				h.Write(buf[:n])
			}
			written += int64(n)
			t.downloaded.Add(int64(n))
		}
		if rerr == io.EOF {
			return written, nil
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return written, ctx.Err()
			}
			return written, rerr
		}
	}
}

func (m *Manager) trackSpeed(ctx context.Context, t *transfer) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		last := t.downloaded.Load()
		lastT := time.Now()
		var ewma float64
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				cur := t.downloaded.Load()
				inst := float64(cur-last) / now.Sub(lastT).Seconds()
				last, lastT = cur, now
				if ewma == 0 {
					ewma = inst
				} else {
					ewma = 0.6*ewma + 0.4*inst
				}
				t.speed.Store(int64(ewma))
			}
		}
	}()
	return func() { close(done) }
}

func (m *Manager) checkSpace(path string, need int64) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(filepath.Dir(path), &st); err != nil {
		return nil // unknown filesystem, do not block
	}
	free := int64(st.Bavail) * int64(st.Bsize)
	if need > 0 && free < need+(256<<20) {
		return fmt.Errorf("insufficient disk space: need %d bytes, %d free", need, free)
	}
	return nil
}

func hashFile(path string, h hash.Hash, limit int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.CopyN(h, f, limit)
	return err
}

func fileMD5(path string) string {
	h := md5.New()
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
