// Package logstore provides a slog.Handler that mirrors log records into a
// bounded SQLite table so they can be shown in the web UI.
package logstore

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Entry is one stored log line.
type Entry struct {
	ID        int64     `json:"id"`
	Time      time.Time `json:"ts"`
	Level     string    `json:"level"`
	Component string    `json:"component"`
	Message   string    `json:"message"`
}

// Store persists log entries.
type Store struct {
	db        *sql.DB
	ch        chan Entry
	maxRows   int
	wg        sync.WaitGroup
	closeCh   chan struct{}
	closeOnce sync.Once
}

// NewStore starts a background writer. maxRows bounds the table size.
func NewStore(db *sql.DB, maxRows int) *Store {
	s := &Store{db: db, ch: make(chan Entry, 1024), maxRows: maxRows, closeCh: make(chan struct{})}
	s.wg.Add(1)
	go s.loop()
	return s
}

func (s *Store) loop() {
	defer s.wg.Done()
	prune := time.NewTicker(5 * time.Minute)
	defer prune.Stop()
	n := 0
	for {
		select {
		case e := <-s.ch:
			s.insert(e)
			n++
			if n >= 500 {
				n = 0
				s.pruneNow()
			}
		case <-prune.C:
			s.pruneNow()
		case <-s.closeCh:
			for {
				select {
				case e := <-s.ch:
					s.insert(e)
				default:
					s.pruneNow()
					return
				}
			}
		}
	}
}

func (s *Store) insert(e Entry) {
	_, _ = s.db.Exec(`INSERT INTO logs(ts, level, component, message) VALUES(?,?,?,?)`,
		e.Time.UnixMilli(), e.Level, e.Component, e.Message)
}

func (s *Store) pruneNow() {
	_, _ = s.db.Exec(`DELETE FROM logs WHERE id <= (SELECT id FROM logs ORDER BY id DESC LIMIT 1 OFFSET ?)`, s.maxRows)
}

// Close flushes pending entries.
func (s *Store) Close() {
	s.closeOnce.Do(func() { close(s.closeCh) })
	s.wg.Wait()
}

// Query returns entries newest-first. minLevel is "debug"|"info"|"warn"|"error".
func (s *Store) Query(ctx context.Context, minLevel, q string, beforeID int64, limit int) ([]Entry, bool, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	levels := levelsAtLeast(minLevel)
	args := []any{}
	var where []string
	if len(levels) > 0 && len(levels) < 4 {
		ph := make([]string, len(levels))
		for i, l := range levels {
			ph[i] = "?"
			args = append(args, l)
		}
		where = append(where, "level IN ("+strings.Join(ph, ",")+")")
	}
	if q != "" {
		// The search text is literal: % and _ must not act as wildcards.
		pat := "%" + likeEscaper.Replace(q) + "%"
		where = append(where, `(message LIKE ? ESCAPE '\' OR component LIKE ? ESCAPE '\')`)
		args = append(args, pat, pat)
	}
	if beforeID > 0 {
		where = append(where, "id < ?")
		args = append(args, beforeID)
	}
	sqlStr := `SELECT id, ts, level, component, message FROM logs`
	if len(where) > 0 {
		sqlStr += " WHERE " + strings.Join(where, " AND ")
	}
	sqlStr += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var ts int64
		if err := rows.Scan(&e.ID, &ts, &e.Level, &e.Component, &e.Message); err != nil {
			return nil, false, err
		}
		e.Time = time.UnixMilli(ts)
		out = append(out, e)
	}
	more := false
	if len(out) > limit {
		out = out[:limit]
		more = true
	}
	if out == nil {
		out = []Entry{}
	}
	return out, more, rows.Err()
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func levelsAtLeast(min string) []string {
	all := []string{"debug", "info", "warn", "error"}
	for i, l := range all {
		if l == strings.ToLower(min) {
			return all[i:]
		}
	}
	return all
}

// Handler is a slog.Handler that forwards to an inner handler and to the store.
type Handler struct {
	inner  slog.Handler
	store  *Store
	level  slog.Level
	attrs  []slog.Attr // already qualified with the groups they were added under
	groups []string
}

// NewHandler wraps inner so every record at or above level is also stored.
func NewHandler(inner slog.Handler, store *Store, level slog.Level) *Handler {
	return &Handler{inner: inner, store: store, level: level}
}

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.level || h.inner.Enabled(ctx, l)
}

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= h.level {
		e := Entry{Time: r.Time, Level: levelName(r.Level)}
		var b strings.Builder
		b.WriteString(r.Message)
		addAttr := func(a slog.Attr) {
			if a.Key == "component" {
				e.Component = a.Value.String()
				return
			}
			if a.Key == "" {
				return
			}
			b.WriteString(" ")
			b.WriteString(a.Key)
			b.WriteString("=")
			b.WriteString(a.Value.String())
		}
		for _, a := range h.attrs {
			addAttr(a)
		}
		prefix := h.prefix()
		r.Attrs(func(a slog.Attr) bool { addAttr(h.qualify(prefix, a)); return true })
		e.Message = b.String()
		select {
		case h.store.ch <- e:
		default: // drop rather than block
		}
	}
	if h.inner.Enabled(ctx, r.Level) {
		return h.inner.Handle(ctx, r)
	}
	return nil
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	n := *h
	n.inner = h.inner.WithAttrs(attrs)
	prefix := h.prefix()
	n.attrs = append([]slog.Attr{}, h.attrs...)
	for _, a := range attrs {
		n.attrs = append(n.attrs, h.qualify(prefix, a))
	}
	return &n
}

func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	n := *h
	n.inner = h.inner.WithGroup(name)
	n.groups = append(append([]string{}, h.groups...), name)
	return &n
}

// prefix is the qualifier for keys added under the open groups ("a.b.").
func (h *Handler) prefix() string {
	if len(h.groups) == 0 {
		return ""
	}
	return strings.Join(h.groups, ".") + "."
}

// qualify prefixes a key with the open groups, as slog handlers must; a grouped
// key can then no longer be mistaken for the top-level "component".
func (h *Handler) qualify(prefix string, a slog.Attr) slog.Attr {
	if prefix == "" || a.Key == "" {
		return a
	}
	return slog.Attr{Key: prefix + a.Key, Value: a.Value}
}

func levelName(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "error"
	case l >= slog.LevelWarn:
		return "warn"
	case l >= slog.LevelInfo:
		return "info"
	default:
		return "debug"
	}
}
