// Package downloader transfers wanted files from GOG's CDN with concurrency and
// bandwidth limits, resumable partial files and checksum verification.
package downloader

import (
	"cmp"
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
	// defaultStallTimeout is how long a transfer may deliver no data before it is
	// abandoned and retried; a connection that silently dies would otherwise hold
	// its download slot forever.
	defaultStallTimeout = 2 * time.Minute
)

// errStalled marks a transfer that stopped delivering data.
var errStalled = errors.New("transfer stalled")

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
	done       chan struct{} // closed once the transfer goroutine has left
}

// progress snapshots the transfer. The caller must hold Manager.mu, which is
// what guards filename.
func (t *transfer) progress() Progress {
	return Progress{
		FileID: t.file.ID, GameID: t.file.GameID, GameTitle: t.gameTitle, Filename: t.filename,
		Size: t.size.Load(), Downloaded: t.downloaded.Load(), SpeedBps: float64(t.speed.Load()), StartedAt: t.startedAt,
	}
}

// Manager runs the download queue.
type Manager struct {
	db    *db.DB
	gog   gog.API
	paths library.Paths
	log   *slog.Logger

	limiter *rate.Limiter
	wake    chan struct{}
	// StallTimeout is how long a transfer may deliver no data before it is
	// abandoned and retried. Set before Run.
	StallTimeout time.Duration

	transfers sync.WaitGroup // running transfer goroutines

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
		active: map[int64]*transfer{}, maxActive: 2, StallTimeout: defaultStallTimeout,
	}
}

// Configure applies concurrency, speed and pause settings at runtime.
func (m *Manager) Configure(s db.Settings, enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maxActive = max(s.MaxConcurrentDownloads, 1) // a stored 0 would stall the queue
	m.paused = s.DownloadsPaused
	m.enabled = enabled
	limit := int64(s.SpeedLimitKBps) * 1024
	if limit != m.rateLimit {
		m.rateLimit = limit
		if limit <= 0 {
			m.limiter.SetLimit(rate.Inf)
			m.limiter.SetBurst(minBurst)
		} else {
			burst := max(int(limit), minBurst)
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
		out = append(out, t.progress())
	}
	return out
}

// ProgressFor returns the transfer state of one file, if active.
func (m *Manager) ProgressFor(fileID int64) (Progress, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.active[fileID]
	if t == nil {
		return Progress{}, false
	}
	return t.progress(), true
}

// Cancel aborts the transfer of a file if it is running and returns once it
// has stopped, so the caller can remove the partial file without the transfer
// writing to it, and so nothing of the file is in flight when its row goes.
func (m *Manager) Cancel(fileID int64) {
	m.mu.Lock()
	t := m.active[fileID]
	m.mu.Unlock()
	if t != nil {
		t.cancel()
		<-t.done
	}
}

// Run drives the queue until ctx is cancelled, then waits for the running
// transfers to wind down so their final state reaches the database before the
// caller closes it.
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
			m.transfers.Wait()
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
	t := &transfer{file: f, gameTitle: game.Title, filename: f.Filename, startedAt: time.Now(), cancel: cancel, done: make(chan struct{})}
	t.size.Store(f.Size)
	m.mu.Lock()
	// The queue was read without the lock held, so re-check the pause and
	// setup state: a pause that landed meanwhile must not be lost.
	if _, exists := m.active[f.ID]; exists || len(m.active) >= m.maxActive || m.paused || !m.enabled {
		m.mu.Unlock()
		cancel()
		return
	}
	m.active[f.ID] = t
	m.transfers.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.transfers.Done()
		defer func() {
			cancel()
			m.mu.Lock()
			delete(m.active, f.ID)
			m.mu.Unlock()
			close(t.done)
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
	if errors.As(err, &ae) {
		// The GOG session failed, not the file: keep it queued for when the user
		// has re-authorized, without using up an attempt.
		m.log.Warn("download needs a valid GOG session, will retry", "game", title, "file", cmp.Or(t.filename, t.file.Name), "error", err)
		_ = m.db.DeferFile(ctx, t.file.ID, err.Error(), time.Minute)
		return
	}
	permanent := false
	var he *gog.HTTPError
	if errors.As(err, &he) && (he.Status == 403 || he.Status == 404) {
		permanent = true
	}
	if attempts+1 >= maxAttempts || permanent {
		m.log.Error("download failed", "game", title, "file", cmp.Or(t.filename, t.file.Name), "error", err)
		_ = m.db.SetFileError(ctx, t.file.ID, err.Error(), 0)
		return
	}
	wait := time.Duration(30*(1<<uint(attempts))) * time.Second
	m.log.Warn("download failed, will retry", "game", title, "file", cmp.Or(t.filename, t.file.Name), "retry_in", wait, "error", err)
	_ = m.db.SetFileError(ctx, t.file.ID, err.Error(), wait)
}

// target is where a transfer is going and what it has to end up being.
type target struct {
	name string // the file name GOG gives it
	rel  string // destination, relative to the library root
	abs  string
	part string // the partial download that becomes abs once it is complete
	size int64  // expected total size (0 when nobody said)
	md5  string // GOG's checksum ("" when it publishes none, normal for extras)
}

// remote asks GOG where the file is and what it should end up being. The
// checksum document is authoritative where GOG publishes one; the name and size
// are what is known before the transfer is open.
func (m *Manager) remote(ctx context.Context, f db.File) (*gog.Downlink, target, error) {
	link, err := m.gog.ResolveDownlink(ctx, f.Downlink)
	if err != nil {
		return nil, target{}, fmt.Errorf("resolving download link: %w", err)
	}
	t := target{name: gog.FilenameFromURL(link.URL), size: f.Size}
	if link.ChecksumURL == "" {
		return link, t, nil
	}
	cs, err := m.gog.FetchChecksum(ctx, link.ChecksumURL)
	if err != nil {
		m.log.Debug("no checksum available", "file", f.Name, "error", err)
		return link, t, nil
	}
	t.md5 = strings.ToLower(cs.MD5)
	if cs.TotalSize > 0 {
		t.size = cs.TotalSize
	}
	if cs.Name != "" {
		t.name = cs.Name
	}
	return link, t, nil
}

// resolve fills in what only the open transfer can say — the name may still have
// to come from its headers — and with a name, the paths.
func (t target) resolve(f db.File, game db.Game, dl *gog.Download, paths library.Paths) target {
	t.name = library.SanitizeFilename(cmp.Or(t.name, dl.Filename, fmt.Sprintf("%s_%s", library.SanitizeFolder(f.Name), f.GogID)))
	if dl.Length > 0 {
		t.size = dl.Length
	}
	t.rel = library.LocalRelPath(game.Folder, f.RelDir, t.name)
	t.abs = paths.Abs(t.rel)
	t.part = t.abs + ".part"
	return t
}

// finished reports whether the complete file is already on disk and is really
// this one, which is what a database reset or an interrupted rename leaves behind.
func (t target) finished() bool {
	st, err := os.Stat(t.abs)
	if err != nil || t.size <= 0 || st.Size() != t.size {
		return false
	}
	return t.md5 == "" || fileMD5(t.abs) == t.md5
}

func (m *Manager) download(ctx context.Context, t *transfer, game db.Game) error {
	f := t.file
	link, tgt, err := m.remote(ctx, f)
	if err != nil {
		return err
	}
	// The transfer itself runs under a context that the stall watchdog can cancel
	// without this looking like a user cancellation.
	dlCtx, abort := context.WithCancelCause(ctx)
	defer abort(nil)
	openWithOffset := func(off int64) (*gog.Download, error) {
		return m.gog.OpenDownload(dlCtx, link.URL, off)
	}
	// Open the transfer first so a Content-Disposition name can be used when the URL has none.
	dl, err := openWithOffset(0)
	if err != nil {
		return fmt.Errorf("opening download: %w", err)
	}
	tgt = tgt.resolve(f, game, dl, m.paths)
	if f.LocalPath != "" && f.LocalPath != tgt.rel {
		// The file used to be planned elsewhere (language folder, renamed DLC);
		// a partial left there would never be picked up again.
		m.paths.RemovePart(f.LocalPath)
	}
	m.mu.Lock()
	t.filename = tgt.name // read by Active() under the same lock
	m.mu.Unlock()
	t.size.Store(tgt.size)
	if err := m.db.SetFileResolved(ctx, f.ID, tgt.name, tgt.rel, tgt.md5, tgt.size); err != nil {
		dl.Body.Close()
		return err
	}
	if err := os.MkdirAll(filepath.Dir(tgt.abs), 0o755); err != nil {
		dl.Body.Close()
		return err
	}
	// Already complete on disk (e.g. after a database reset)? Verify and finish.
	if st, err := os.Stat(tgt.abs); err == nil && tgt.size > 0 && st.Size() == tgt.size {
		dl.Body.Close()
		// Size alone cannot identify a replacement whose old version occupies
		// this same path. Without a checksum, fetch it even if the size matches.
		if (tgt.md5 != "" || f.PreviousPath != tgt.rel) && tgt.finished() {
			m.log.Info("file already present, skipping download", "game", game.Title, "file", tgt.name)
			_, err := m.complete(ctx, f, tgt.rel, tgt.size, game.Title)
			return err
		}
		m.log.Info("replacing file on disk", "game", game.Title, "file", tgt.name)
		// Keep the existing installer until the verified .part replaces it.
		// The transfer was closed while hashing; open it again for the download.
		if dl, err = openWithOffset(0); err != nil {
			return fmt.Errorf("opening download: %w", err)
		}
	}
	// Resume a partial file when possible.
	var offset int64
	if st, err := os.Stat(tgt.part); err == nil && st.Size() > 0 && st.Size() < tgt.size {
		dl.Body.Close()
		offset = st.Size()
		dl, err = openWithOffset(offset)
		if errors.Is(err, gog.ErrRangeNotSatisfiable) || (err == nil && dl.Offset != offset) {
			if dl != nil {
				dl.Body.Close()
			}
			offset = 0
			_ = os.Remove(tgt.part)
			dl, err = openWithOffset(0)
		}
		if err != nil {
			return fmt.Errorf("resuming download: %w", err)
		}
	} else if err == nil {
		_ = os.Remove(tgt.part)
	}
	defer dl.Body.Close()
	if err := m.checkSpace(tgt.abs, tgt.size-offset); err != nil {
		return err
	}
	written, err := m.writePart(ctx, dlCtx, abort, tgt, dl, offset, t, game.Title)
	if err != nil {
		return err
	}
	total := offset + written
	if tgt.size > 0 && total != tgt.size {
		_ = os.Remove(tgt.part)
		return fmt.Errorf("size mismatch: got %d bytes, expected %d", total, tgt.size)
	}
	if err := os.Rename(tgt.part, tgt.abs); err != nil {
		return err
	}
	ok, err := m.complete(ctx, f, tgt.rel, total, game.Title)
	if err != nil || !ok {
		if !ok {
			// What was downloaded is the old build (the row is pending for the new
			// one), or nothing wants it anymore.
			_ = os.Remove(tgt.abs)
		}
		return err
	}
	m.log.Info("download complete", "game", game.Title, "file", tgt.name, "size", total)
	return nil
}

// writePart streams the open transfer into the partial file, appending to the
// offset bytes that are already there, and holds the result against GOG's
// checksum. A partial file that fails the check is removed: resuming it would
// only produce the same bad file again.
func (m *Manager) writePart(ctx, dlCtx context.Context, abort context.CancelCauseFunc, tgt target, dl *gog.Download, offset int64, t *transfer, title string) (int64, error) {
	flags := os.O_CREATE | os.O_WRONLY
	if offset > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	out, err := os.OpenFile(tgt.part, flags, 0o644)
	if err != nil {
		return 0, err
	}
	var hasher hash.Hash
	if tgt.md5 != "" {
		hasher = md5.New()
		if offset > 0 {
			// Hash the part we already have.
			if err := hashFile(tgt.part, hasher, offset); err != nil {
				out.Close()
				return 0, err
			}
		}
	}
	t.downloaded.Store(offset)
	if offset > 0 {
		m.log.Info("resuming download", "game", title, "file", tgt.name, "offset", offset)
	} else {
		m.log.Info("downloading", "game", title, "file", tgt.name, "size", tgt.size)
	}
	stopSpeed := m.trackSpeed(ctx, t)
	written, err := m.copy(ctx, dlCtx, abort, out, dl.Body, hasher, t)
	stopSpeed()
	if err == nil {
		// The file is renamed into place and recorded as done right after this;
		// make sure the bytes are on disk first so a crash cannot leave a
		// truncated installer that the database calls complete.
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil && cerr != nil {
		err = cerr
	}
	if err != nil {
		return written, err
	}
	if hasher != nil {
		if got := hex.EncodeToString(hasher.Sum(nil)); got != tgt.md5 {
			_ = os.Remove(tgt.part)
			return written, fmt.Errorf("checksum mismatch: got %s, expected %s", got, tgt.md5)
		}
	}
	return written, nil
}

// complete records the file as done unless a sync replaced its version or download
// link while it was transferring, in which case the bytes on disk belong to the
// old build and must not be presented as the new one. On success the superseded
// previous version, if any, is removed.
func (m *Manager) complete(ctx context.Context, f db.File, rel string, size int64, title string) (bool, error) {
	ok, err := m.db.CompleteFile(ctx, f.ID, rel, size, f.Downlink, f.Version)
	if err != nil {
		return false, err
	}
	if !ok {
		m.log.Info("file changed on GOG during download, fetching the new version", "game", title, "file", f.Name)
		return false, nil
	}
	if f.PreviousPath != "" && f.PreviousPath != rel {
		if err := m.paths.Remove(f.PreviousPath); err != nil {
			m.log.Warn("could not remove superseded file", "path", f.PreviousPath, "error", err)
		} else {
			m.log.Info("removed superseded file", "path", f.PreviousPath)
		}
	}
	return true, nil
}

// copy streams src to dst, enforcing the speed limit and aborting the transfer
// (via abort, which cancels dlCtx) when no data arrives for StallTimeout.
func (m *Manager) copy(ctx, dlCtx context.Context, abort context.CancelCauseFunc, dst io.Writer, src io.Reader, h hash.Hash, t *transfer) (int64, error) {
	buf := make([]byte, readChunk)
	var written int64
	var watchdog *time.Timer
	if m.StallTimeout > 0 {
		watchdog = time.AfterFunc(m.StallTimeout, func() { abort(errStalled) })
		defer watchdog.Stop()
	}
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			// Waiting for bandwidth is not a stall.
			if watchdog != nil {
				watchdog.Stop()
			}
			if err := m.limiter.WaitN(ctx, n); err != nil {
				return written, err
			}
			if watchdog != nil {
				watchdog.Reset(m.StallTimeout)
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
			if errors.Is(context.Cause(dlCtx), errStalled) {
				return written, fmt.Errorf("no data received for %s", m.StallTimeout)
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
	free, total := library.DiskUsage(filepath.Dir(path))
	if total == 0 {
		return nil // unknown filesystem, do not block
	}
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
