package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ademant/dansal/internal/logging"
	sqlite3 "github.com/mattn/go-sqlite3"
)

// Debug-only tracking of connection-holding objects (#1732). A result set or
// transaction keeps one pool connection until it is closed. When the pool is
// exhausted, the goroutine dump shows the waiters but not the holders, because
// the holder's goroutine has already returned. This driver wrapper records the
// call site of every open Rows and Tx, so the debug log can name the code path
// that never closed one.
//
// The wrapper is registered under trackedDriverName and is used only when
// log_level is debug (see openDB). At info level database/sql talks to the
// plain sqlite3 driver and nothing here runs.

// trackedDriverName is the database/sql driver name used when debugging.
const trackedDriverName = "sqlite3-tracked"

// trackedAfter is how long a result set or transaction can stay open before
// it is reported as held (#1732).
const trackedAfter = 10 * time.Second

// trackedRegistry holds every open Rows and Tx while tracking is on.
var trackedRegistry = struct {
	mu   sync.Mutex
	open map[*trackedEntry]struct{}
}{open: map[*trackedEntry]struct{}{}}

// trackedEntry is one open Rows or Tx, with the call site that opened it.
type trackedEntry struct {
	kind   string // "rows" or "tx"
	site   string // file:line (function) inside cmd/dansal
	opened time.Time
	conn   *trackedConn
}

// close removes the entry from the registry and from its connection's set.
// It is safe to call more than once.
func (e *trackedEntry) close() {
	trackedRegistry.mu.Lock()
	delete(trackedRegistry.open, e)
	trackedRegistry.mu.Unlock()
	if e.conn != nil {
		e.conn.mu.Lock()
		delete(e.conn.entries, e)
		e.conn.mu.Unlock()
	}
}

// trackOpen records a new Rows or Tx opened on c.
func trackOpen(kind string, c *trackedConn) *trackedEntry {
	e := &trackedEntry{kind: kind, site: callSite(), opened: time.Now(), conn: c}
	trackedRegistry.mu.Lock()
	trackedRegistry.open[e] = struct{}{}
	trackedRegistry.mu.Unlock()
	c.mu.Lock()
	c.entries[e] = struct{}{}
	c.mu.Unlock()
	return e
}

// callSite returns the first frame inside cmd/dansal that is not this file,
// as "file.go:line (function)".
func callSite() string {
	pcs := make([]uintptr, 32)
	n := runtime.Callers(3, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		if strings.Contains(f.File, "/cmd/dansal/") && !strings.HasSuffix(f.File, "dbtrack.go") {
			fn := f.Function
			if i := strings.LastIndex(fn, "."); i >= 0 {
				fn = fn[i+1:]
			}
			return fmt.Sprintf("%s:%d (%s)", filepath.Base(f.File), f.Line, fn)
		}
		if !more {
			return "unknown"
		}
	}
}

// trackedGroup is the open entries sharing one kind and call site.
type trackedGroup struct {
	count  int
	oldest time.Duration
}

// trackedOpenGroups returns the open entries grouped by kind and call site,
// and the entries open longer than trackedAfter.
func trackedOpenGroups(now time.Time) (groups map[string]*trackedGroup, held []string) {
	groups = map[string]*trackedGroup{}
	trackedRegistry.mu.Lock()
	defer trackedRegistry.mu.Unlock()
	for e := range trackedRegistry.open {
		age := now.Sub(e.opened)
		key := e.kind + " " + e.site
		g := groups[key]
		if g == nil {
			g = &trackedGroup{}
			groups[key] = g
		}
		g.count++
		if age > g.oldest {
			g.oldest = age
		}
		if age > trackedAfter {
			held = append(held, fmt.Sprintf("db held: kind=%s site=%s age=%ds", e.kind, e.site, int(age.Seconds())))
		}
	}
	return groups, held
}

// logTrackedDB writes the open result sets and transactions by call site, and
// one line for each held longer than trackedAfter (#1732). It does nothing
// unless the tracking driver is in use.
func logTrackedDB() {
	if !trackingDB {
		return
	}
	groups, held := trackedOpenGroups(time.Now())
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		g := groups[k]
		logging.Debugf("db open: %s count=%d oldest=%ds", k, g.count, int(g.oldest.Seconds()))
	}
	sort.Strings(held)
	for _, line := range held {
		logging.Debugf("%s", line)
	}
}

// trackingDB is set when openDB picks the tracking driver.
var trackingDB bool

// trackDriver wraps the sqlite3 driver so every connection is tracked.
type trackDriver struct {
	inner *sqlite3.SQLiteDriver
}

func (d *trackDriver) Open(dsn string) (driver.Conn, error) {
	c, err := d.inner.Open(dsn)
	if err != nil {
		return nil, err
	}
	return &trackedConn{Conn: c, entries: map[*trackedEntry]struct{}{}}, nil
}

// trackedConn wraps one sqlite3 connection. It forwards the optional query
// interfaces to the inner connection and reports ErrSkip where the inner one
// lacks them, so database/sql falls back to Prepare exactly as before.
type trackedConn struct {
	driver.Conn
	mu      sync.Mutex
	entries map[*trackedEntry]struct{}
}

// unwrapDriverConn returns the sqlite3 connection behind a tracked one, so
// callers that type-assert the driver connection (backup.go) still work.
func unwrapDriverConn(raw any) any {
	if c, ok := raw.(*trackedConn); ok {
		return c.Conn
	}
	return raw
}

// Close closes any result sets and transactions the connection still holds,
// then the connection. A closed connection can't hold a pool slot.
func (c *trackedConn) Close() error {
	c.mu.Lock()
	open := make([]*trackedEntry, 0, len(c.entries))
	for e := range c.entries {
		open = append(open, e)
	}
	c.mu.Unlock()
	for _, e := range open {
		e.close()
	}
	return c.Conn.Close()
}

func (c *trackedConn) Prepare(query string) (driver.Stmt, error) {
	s, err := c.Conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &trackedStmt{Stmt: s, conn: c}, nil
}

func (c *trackedConn) Begin() (driver.Tx, error) {
	tx, err := c.Conn.Begin() //nolint:staticcheck // driver.Conn fallback
	if err != nil {
		return nil, err
	}
	return c.wrapTx(tx), nil
}

func (c *trackedConn) Ping(ctx context.Context) error {
	if p, ok := c.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *trackedConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	cb, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return nil, driver.ErrSkip
	}
	tx, err := cb.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return c.wrapTx(tx), nil
}

func (c *trackedConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	cp, ok := c.Conn.(driver.ConnPrepareContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	s, err := cp.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return &trackedStmt{Stmt: s, conn: c}, nil
}

func (c *trackedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	rows, err := q.QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return c.wrapRows(rows), nil
}

func (c *trackedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	ex, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return ex.ExecContext(ctx, query, args)
}

func (c *trackedConn) wrapTx(tx driver.Tx) driver.Tx {
	return &trackedTx{Tx: tx, entry: trackOpen("tx", c)}
}

func (c *trackedConn) wrapRows(rows driver.Rows) driver.Rows {
	return &trackedRows{Rows: rows, entry: trackOpen("rows", c)}
}

// trackedTx closes its entry on Commit or Rollback.
type trackedTx struct {
	driver.Tx
	entry *trackedEntry
}

func (t *trackedTx) Commit() error {
	defer t.entry.close()
	return t.Tx.Commit()
}

func (t *trackedTx) Rollback() error {
	defer t.entry.close()
	return t.Tx.Rollback()
}

// trackedRows closes its entry when the result set is closed.
type trackedRows struct {
	driver.Rows
	entry *trackedEntry
}

func (r *trackedRows) Close() error {
	defer r.entry.close()
	return r.Rows.Close()
}

// trackedStmt wraps a prepared statement so its result sets are tracked.
type trackedStmt struct {
	driver.Stmt
	conn *trackedConn
}

func (s *trackedStmt) Query(args []driver.Value) (driver.Rows, error) {
	rows, err := s.Stmt.Query(args) //nolint:staticcheck // driver.Stmt fallback
	if err != nil {
		return nil, err
	}
	return s.conn.wrapRows(rows), nil
}

func (s *trackedStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	sq, ok := s.Stmt.(driver.StmtQueryContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	rows, err := sq.QueryContext(ctx, args)
	if err != nil {
		return nil, err
	}
	return s.conn.wrapRows(rows), nil
}

func (s *trackedStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	se, ok := s.Stmt.(driver.StmtExecContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return se.ExecContext(ctx, args)
}

func init() {
	sql.Register(trackedDriverName, &trackDriver{inner: &sqlite3.SQLiteDriver{}})
}
