package main

import (
	"compress/gzip"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"log/syslog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ademant/dansal/internal/instance"
	_ "github.com/mattn/go-sqlite3"
)

var db *sql.DB
var rateLimiter *RateLimiter
var loginRateLimiter *RateLimiter
var connLimiter *ConnLimiter
var configFilePath string

var lastSeenMu sync.Mutex
var lastSeenCache = make(map[string]time.Time)

const lastSeenUpdateInterval = 60 * time.Second

// updateLastSeen records the current time as last_seen_at for the token.
// Writes are debounced to at most once per lastSeenUpdateInterval to keep
// write pressure low on busy deployments.
func updateLastSeen(token string) {
	now := time.Now().UTC()
	lastSeenMu.Lock()
	if last, ok := lastSeenCache[token]; ok && now.Sub(last) < lastSeenUpdateInterval {
		lastSeenMu.Unlock()
		return
	}
	lastSeenCache[token] = now
	lastSeenMu.Unlock()
	go func() {
		if _, err := db.Exec("UPDATE tokens SET last_seen_at=? WHERE token=?", now.Unix(), sha256Hex(token)); err != nil {
			log.Printf("warn: update last_seen_at: %v", err)
		}
	}()
}

type ConnLimiter struct {
	mu     sync.Mutex
	active map[string]int
	limit  int
}

func NewConnLimiter(limit int) *ConnLimiter {
	return &ConnLimiter{active: make(map[string]int), limit: limit}
}

func (cl *ConnLimiter) acquire(ip string) bool {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.active[ip] >= cl.limit {
		return false
	}
	cl.active[ip]++
	return true
}

func (cl *ConnLimiter) release(ip string) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	cl.active[ip]--
	if cl.active[ip] <= 0 {
		delete(cl.active, ip)
	}
}

func ConnLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isInternalCaller(r) {
			next.ServeHTTP(w, r)
			return
		}
		ip := getClientIP(r)
		if !connLimiter.acquire(ip) {
			writeError(w, "Too many concurrent connections", http.StatusTooManyRequests)
			return
		}
		defer connLimiter.release(ip)
		next.ServeHTTP(w, r)
	})
}

type RateLimiter struct {
	mu       sync.Mutex
	requests map[string][]time.Time
	limit    int
	window   time.Duration
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		requests: make(map[string][]time.Time),
		limit:    limit,
		window:   window,
	}
	go rl.sweepLoop()
	return rl
}

func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	valid := rl.prune(rl.requests[ip], now)
	if len(valid) >= rl.limit {
		return false
	}
	rl.requests[ip] = append(valid, now)
	return true
}

// prune removes timestamps outside the window and deletes the map key when empty.
func (rl *RateLimiter) prune(times []time.Time, now time.Time) []time.Time {
	var valid []time.Time
	for _, t := range times {
		if now.Sub(t) < rl.window {
			valid = append(valid, t)
		}
	}
	return valid
}

// sweepLoop periodically removes stale IP entries from the map.
func (rl *RateLimiter) sweepLoop() {
	ticker := time.NewTicker(rl.window / 2)
	defer ticker.Stop()
	for range ticker.C {
		rl.mu.Lock()
		now := time.Now()
		for ip, times := range rl.requests {
			if valid := rl.prune(times, now); len(valid) == 0 {
				delete(rl.requests, ip)
			} else {
				rl.requests[ip] = valid
			}
		}
		rl.mu.Unlock()
	}
}

type gzipResponseWriter struct {
	http.ResponseWriter
	gz *gzip.Writer
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) { return g.gz.Write(b) }

// Unwrap lets http.ResponseController (e.g. SetWriteDeadline) see through
// this wrapper to the underlying ResponseWriter, per its documented contract.
func (g *gzipResponseWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }
func (g *gzipResponseWriter) WriteHeader(code int) {
	g.Header().Del("Content-Length")
	g.ResponseWriter.WriteHeader(code)
}

// GzipMiddleware compresses responses when the client supports gzip.
// Image paths are excluded — AVIF is already compressed binary data.
func GzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/images/") ||
			!strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")
		gz, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
		defer gz.Close()
		next.ServeHTTP(&gzipResponseWriter{ResponseWriter: w, gz: gz}, r)
	})
}

func MaxBodyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, config.Server.MaxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

func RateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isInternalCaller(r) {
			next.ServeHTTP(w, r)
			return
		}
		ip := getClientIP(r)
		if !rateLimiter.Allow(ip) {
			rateLimitRejectionsTotal.Inc()
			writeError(w, "Rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isInternalCaller reports whether r is dansal-web's backend client calling
// over loopback with the configured shared secret. Such requests are exempt
// from RateLimitMiddleware/ConnLimitMiddleware, since they otherwise share a
// single loopback bucket with all of dansal-web's visitor-driven traffic.
//
// Both conditions are required: the loopback check stops internet clients
// (whose RemoteAddr is also loopback when proxied through nginx) from
// supplying the header themselves — nginx strips X-Dansal-Internal on the
// public /api/ location, so only same-host processes can set it.
func isInternalCaller(r *http.Request) bool {
	secret := config.Server.InternalSharedSecret
	if secret == "" {
		return false
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if host != "127.0.0.1" && host != "::1" {
		return false
	}
	got := r.Header.Get("X-Dansal-Internal")
	return subtle.ConstantTimeCompare([]byte(got), []byte(secret)) == 1
}

// corsOrigin returns the allowed CORS origin for the request.
// When allowed_origins is unset, falls back to the configured base_url origin
// so that unconfigured deployments don't silently open CORS to all domains.
// Falls through to "*" only when base_url is also not set.
func corsOrigin(r *http.Request) string {
	if len(config.Server.AllowedOrigins) == 0 {
		if u, err := url.Parse(config.Server.BaseURL); err == nil && u.Host != "" {
			return u.Scheme + "://" + u.Host
		}
		return "*"
	}
	origin := r.Header.Get("Origin")
	for _, o := range config.Server.AllowedOrigins {
		if o == "*" || o == origin {
			return origin
		}
	}
	return ""
}

// CORSMiddleware adds Access-Control-Allow-Origin to every response and
// handles OPTIONS preflight requests inline so each route need not register
// a separate OPTIONS handler. A bare OPTIONS request (no
// Access-Control-Request-Method header, i.e. not a browser preflight) falls
// through to smux instead, so routes registered for OPTIONS — e.g. the
// write-endpoint schema responders — are reachable.
func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := corsOrigin(r); o != "" {
			w.Header().Set("Access-Control-Allow-Origin", o)
			if o != "*" {
				w.Header().Add("Vary", "Origin")
			}
		}
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-User-Role, X-User-ID")
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// SecurityHeadersMiddleware adds defensive HTTP headers to every response.
func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		next.ServeHTTP(w, r)
	})
}

// middlewareChain applies middlewares in order so the first listed is outermost.
func middlewareChain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

func startTokenCleanup() {
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now().UTC().Unix()
			res, err := db.Exec("DELETE FROM tokens WHERE expires_at < ?", now)
			if err != nil {
				log.Printf("token cleanup: %v", err)
			} else if n, _ := res.RowsAffected(); n > 0 {
				log.Printf("token cleanup: removed %d expired token(s)", n)
			}
			db.Exec("DELETE FROM verification_tokens WHERE expires_at < ?", now)
			db.Exec("DELETE FROM magic_login_tokens WHERE expires_at < ?", now)
			db.Exec("DELETE FROM contact_posts WHERE expires_at < ?", now)
			cleanExpiredBoardSessions(now)
			if config != nil && config.Server.SessionIdleTimeoutMins > 0 {
				idleCutoff := now - int64(config.Server.SessionIdleTimeoutMins*60)
				db.Exec("DELETE FROM tokens WHERE last_seen_at IS NOT NULL AND last_seen_at < ?", idleCutoff)
			}
			db.Exec("DELETE FROM bookings WHERE status='pending' AND expires_at < ?", now)
			// Clean up users pre-created by webauthnInviteBegin that were never
			// completed: their invite session expired, they have no credentials,
			// and they have no org membership.
			db.Exec(`DELETE FROM users WHERE id IN (
				SELECT CAST(json_extract(data,'$.user_id') AS INTEGER)
				FROM webauthn_sessions
				WHERE json_extract(data,'$.invite_id') IS NOT NULL
				  AND expires_at < ?
			) AND id NOT IN (SELECT user_id FROM webauthn_credentials)
			  AND id NOT IN (SELECT user_id FROM organization_members)`, now)
			db.Exec("DELETE FROM webauthn_sessions WHERE expires_at < ?", now)
			// Sweep lastSeenCache: remove entries older than the maximum token lifetime.
			expirationHours := 24
			if config != nil && config.Server.TokenExpirationHours > 0 {
				expirationHours = config.Server.TokenExpirationHours
			}
			cutoff := time.Now().Add(-time.Duration(expirationHours+1) * time.Hour)
			lastSeenMu.Lock()
			for k, t := range lastSeenCache {
				if t.Before(cutoff) {
					delete(lastSeenCache, k)
				}
			}
			lastSeenMu.Unlock()
		}
	}()
}

// migrateUsersRoles trims the users.role CHECK constraint back to the four
// active roles (admin, user, publisher, viewer), removing accountant and visitor
// which were prepared for a booking system that has since been removed.
// SQLite requires full table recreation to change a constraint.
func migrateUsersRoles() {
	var schema string
	if err := db.QueryRow(
		"SELECT sql FROM sqlite_master WHERE type='table' AND name='users'",
	).Scan(&schema); err != nil || !strings.Contains(schema, "accountant") {
		return // already up to date
	}

	conn, err := db.Conn(context.Background())
	if err != nil {
		log.Printf("migrateUsersRoles: get conn: %v", err)
		return
	}
	defer conn.Close()

	if _, err = conn.ExecContext(context.Background(), "PRAGMA foreign_keys=OFF"); err != nil {
		log.Printf("migrateUsersRoles: pragma off: %v", err)
		return
	}

	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		log.Printf("migrateUsersRoles: begin: %v", err)
		conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
		return
	}

	stmts := []string{
		`CREATE TABLE users_v2 (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT UNIQUE NOT NULL,
			email TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			role TEXT DEFAULT 'user' CHECK(role IN ('admin','user','publisher','viewer')),
			telegram TEXT,
			matrix TEXT,
			email_verified INTEGER DEFAULT 0,
			telegram_verified INTEGER DEFAULT 0,
			matrix_verified INTEGER DEFAULT 0,
			disabled INTEGER DEFAULT 0,
			failed_login_count INTEGER DEFAULT 0,
			failed_login_since DATETIME,
			last_magic_sent_at DATETIME,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`INSERT INTO users_v2
			SELECT id, username, email, password_hash,
			       CASE WHEN role IN ('admin','user','publisher','viewer') THEN role ELSE 'user' END,
			       telegram, matrix,
			       email_verified, telegram_verified, matrix_verified, disabled,
			       failed_login_count, failed_login_since, last_magic_sent_at, created_at
			FROM users`,
		`DROP TABLE users`,
		`ALTER TABLE users_v2 RENAME TO users`,
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(context.Background(), stmt); err != nil {
			tx.Rollback()
			conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
			log.Printf("migrateUsersRoles: %v", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("migrateUsersRoles: commit: %v", err)
	}
	conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
}

func migrateLocationsLatLng() {
	var schema string
	if err := db.QueryRow(
		"SELECT sql FROM sqlite_master WHERE type='table' AND name='locations'",
	).Scan(&schema); err != nil || strings.Contains(schema, "latitude REAL") {
		return
	}

	conn, err := db.Conn(context.Background())
	if err != nil {
		log.Printf("migrateLocationsLatLng: %v", err)
		return
	}
	defer conn.Close()

	if _, err = conn.ExecContext(context.Background(), "PRAGMA foreign_keys=OFF"); err != nil {
		log.Printf("migrateLocationsLatLng: pragma off: %v", err)
		return
	}

	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
		log.Printf("migrateLocationsLatLng: begin: %v", err)
		return
	}

	stmts := []string{
		`CREATE TABLE locations_v2 (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			location TEXT NOT NULL,
			short_name TEXT,
			address TEXT,
			zipcode TEXT,
			town TEXT,
			country TEXT,
			latitude REAL,
			longitude REAL,
			internetsite TEXT,
			organization_id INTEGER,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`INSERT INTO locations_v2 SELECT id, location, short_name, address, zipcode, town, country,
			CASE WHEN latitude IS NULL OR latitude = '' THEN NULL ELSE CAST(latitude AS REAL) END,
			CASE WHEN longitude IS NULL OR longitude = '' THEN NULL ELSE CAST(longitude AS REAL) END,
			internetsite, organization_id, created_at FROM locations`,
		`DROP TABLE locations`,
		`ALTER TABLE locations_v2 RENAME TO locations`,
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(context.Background(), stmt); err != nil {
			tx.Rollback()
			conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
			log.Printf("migrateLocationsLatLng: %v", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("migrateLocationsLatLng: commit: %v", err)
	}
	conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
}

// migrateContactPostsCheckConstraint rebuilds contact_posts when the CHECK
// constraint is missing 'ticket_offer' and 'ticket_request'.
func migrateContactPostsCheckConstraint() {
	var schema string
	if err := db.QueryRow(
		"SELECT sql FROM sqlite_master WHERE type='table' AND name='contact_posts'",
	).Scan(&schema); err != nil || strings.Contains(schema, "ticket_offer") {
		return
	}

	conn, err := db.Conn(context.Background())
	if err != nil {
		log.Printf("migrateContactPostsCheckConstraint: get conn: %v", err)
		return
	}
	defer conn.Close()

	if _, err = conn.ExecContext(context.Background(), "PRAGMA foreign_keys=OFF"); err != nil {
		log.Printf("migrateContactPostsCheckConstraint: pragma off: %v", err)
		return
	}

	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
		log.Printf("migrateContactPostsCheckConstraint: begin: %v", err)
		return
	}

	stmts := []string{
		`CREATE TABLE contact_posts_v2 (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			event_id INTEGER NOT NULL,
			type TEXT NOT NULL CHECK(type IN ('ride_offer','ride_request','sleep_offer','sleep_request','ticket_offer','ticket_request')),
			city TEXT NOT NULL,
			persons INTEGER NOT NULL DEFAULT 1,
			message TEXT DEFAULT '',
			nickname TEXT NOT NULL,
			email TEXT NOT NULL DEFAULT '',
			telegram_username TEXT,
			poster_telegram_chat_id TEXT,
			email_verified INTEGER DEFAULT 0,
			verify_token TEXT UNIQUE,
			delete_token TEXT UNIQUE,
			expires_at DATETIME NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE
		)`,
		`INSERT INTO contact_posts_v2 SELECT id, event_id, type, city, persons, message, nickname,
			email, COALESCE(telegram_username,NULL), COALESCE(poster_telegram_chat_id,NULL),
			email_verified, verify_token, delete_token, expires_at, created_at FROM contact_posts`,
		`DROP TABLE contact_posts`,
		`ALTER TABLE contact_posts_v2 RENAME TO contact_posts`,
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(context.Background(), stmt); err != nil {
			tx.Rollback()
			conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
			log.Printf("migrateContactPostsCheckConstraint: %v", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("migrateContactPostsCheckConstraint: commit: %v", err)
	}
	conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
	log.Printf("migrateContactPostsCheckConstraint: rebuilt contact_posts with ticket types")
}

// migrateContactPostsLostFound extends the CHECK constraint to include lost_item / found_item.
func migrateContactPostsLostFound() {
	var schema string
	if err := db.QueryRow(
		"SELECT sql FROM sqlite_master WHERE type='table' AND name='contact_posts'",
	).Scan(&schema); err != nil || strings.Contains(schema, "lost_item") {
		return
	}

	conn, err := db.Conn(context.Background())
	if err != nil {
		log.Printf("migrateContactPostsLostFound: get conn: %v", err)
		return
	}
	defer conn.Close()
	if _, err = conn.ExecContext(context.Background(), "PRAGMA foreign_keys=OFF"); err != nil {
		log.Printf("migrateContactPostsLostFound: pragma off: %v", err)
		return
	}
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
		log.Printf("migrateContactPostsLostFound: begin: %v", err)
		return
	}
	stmts := []string{
		`CREATE TABLE contact_posts_v3 (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			event_id INTEGER NOT NULL,
			type TEXT NOT NULL CHECK(type IN ('ride_offer','ride_request','sleep_offer','sleep_request','ticket_offer','ticket_request','lost_item','found_item')),
			city TEXT NOT NULL,
			persons INTEGER NOT NULL DEFAULT 1,
			message TEXT DEFAULT '',
			nickname TEXT NOT NULL,
			email TEXT NOT NULL DEFAULT '',
			telegram_username TEXT,
			poster_telegram_chat_id TEXT,
			email_verified INTEGER DEFAULT 0,
			manage_token TEXT UNIQUE,
			user_id INTEGER REFERENCES users(id),
			expires_at INTEGER NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE
		)`,
		`INSERT INTO contact_posts_v3 SELECT id,event_id,type,city,persons,message,nickname,email,
			COALESCE(telegram_username,NULL),COALESCE(poster_telegram_chat_id,NULL),
			email_verified,manage_token,user_id,expires_at,created_at FROM contact_posts`,
		`DROP TABLE contact_posts`,
		`ALTER TABLE contact_posts_v3 RENAME TO contact_posts`,
		`CREATE INDEX IF NOT EXISTS idx_contact_posts_event_id ON contact_posts(event_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_contact_posts_manage_token ON contact_posts(manage_token)`,
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(context.Background(), stmt); err != nil {
			tx.Rollback()
			conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
			log.Printf("migrateContactPostsLostFound: %v", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("migrateContactPostsLostFound: commit: %v", err)
	}
	conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
	log.Printf("migrateContactPostsLostFound: added lost_item/found_item types")
}

// migrateUsersDropUsername rebuilds the users table to replace username with display_name.
// Existing usernames are copied into display_name. Returns immediately if username column
// is already gone.
func migrateUsersDropUsername() {
	var schema string
	if err := db.QueryRow(
		"SELECT sql FROM sqlite_master WHERE type='table' AND name='users'",
	).Scan(&schema); err != nil || !strings.Contains(schema, "username") {
		return
	}

	conn, err := db.Conn(context.Background())
	if err != nil {
		log.Printf("migrateUsersDropUsername: get conn: %v", err)
		return
	}
	defer conn.Close()
	if _, err = conn.ExecContext(context.Background(), "PRAGMA foreign_keys=OFF"); err != nil {
		log.Printf("migrateUsersDropUsername: pragma off: %v", err)
		return
	}
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
		log.Printf("migrateUsersDropUsername: begin: %v", err)
		return
	}
	stmts := []string{
		`CREATE TABLE users_v2 (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			email TEXT UNIQUE NOT NULL,
			display_name TEXT,
			password_hash TEXT NOT NULL DEFAULT '',
			role TEXT DEFAULT 'user' CHECK(role IN ('admin', 'user', 'publisher', 'viewer')),
			telegram TEXT,
			matrix TEXT,
			email_verified INTEGER DEFAULT 0,
			telegram_verified INTEGER DEFAULT 0,
			matrix_verified INTEGER DEFAULT 0,
			disabled INTEGER DEFAULT 0,
			failed_login_count INTEGER DEFAULT 0,
			failed_login_since INTEGER,
			last_magic_sent_at INTEGER,
			description TEXT,
			mastodon TEXT,
			website TEXT,
			telegram_chat_id TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`INSERT INTO users_v2 (id, email, display_name, password_hash, role, telegram, matrix,
		  email_verified, telegram_verified, matrix_verified, disabled, failed_login_count,
		  failed_login_since, last_magic_sent_at, description, mastodon, website, telegram_chat_id, created_at)
		 SELECT id, email, username, password_hash, role, telegram, matrix,
		  COALESCE(email_verified,0), COALESCE(telegram_verified,0), COALESCE(matrix_verified,0),
		  COALESCE(disabled,0), COALESCE(failed_login_count,0),
		  failed_login_since, last_magic_sent_at, description, mastodon, website, telegram_chat_id, created_at
		 FROM users`,
		`DROP TABLE users`,
		`ALTER TABLE users_v2 RENAME TO users`,
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(context.Background(), stmt); err != nil {
			tx.Rollback()
			conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
			log.Printf("migrateUsersDropUsername: %v", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("migrateUsersDropUsername: commit: %v", err)
	}
	conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
	log.Printf("migrateUsersDropUsername: replaced username with display_name (backfilled from existing usernames)")
}

// migrateInviteLinksRole rebuilds invite_links to add a CHECK constraint on
// the role column, matching the constraint already present on users.role.
func migrateInviteLinksRole() {
	var schema string
	if err := db.QueryRow(
		"SELECT sql FROM sqlite_master WHERE type='table' AND name='invite_links'",
	).Scan(&schema); err != nil || strings.Contains(schema, "role IN ('admin'") {
		return
	}

	conn, err := db.Conn(context.Background())
	if err != nil {
		log.Printf("migrateInviteLinksRole: get conn: %v", err)
		return
	}
	defer conn.Close()

	if _, err = conn.ExecContext(context.Background(), "PRAGMA foreign_keys=OFF"); err != nil {
		log.Printf("migrateInviteLinksRole: pragma off: %v", err)
		return
	}

	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
		log.Printf("migrateInviteLinksRole: begin: %v", err)
		return
	}

	stmts := []string{
		`CREATE TABLE invite_links_v2 (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			token TEXT UNIQUE NOT NULL,
			created_by INTEGER NOT NULL,
			role TEXT NOT NULL DEFAULT 'user' CHECK(role IN ('admin', 'user', 'publisher', 'viewer')),
			org_id INTEGER,
			expires_at INTEGER NOT NULL,
			used_at INTEGER,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE CASCADE,
			FOREIGN KEY (org_id) REFERENCES organizations(id) ON DELETE SET NULL
		)`,
		`INSERT INTO invite_links_v2
			SELECT id, token, created_by, role, org_id, expires_at, used_at, created_at
			FROM invite_links`,
		`DROP TABLE invite_links`,
		`ALTER TABLE invite_links_v2 RENAME TO invite_links`,
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(context.Background(), stmt); err != nil {
			tx.Rollback()
			conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
			log.Printf("migrateInviteLinksRole: %v", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("migrateInviteLinksRole: commit: %v", err)
	}
	conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
	log.Printf("migrateInviteLinksRole: added CHECK constraint on role")
}

// migrateEventsFK adds FOREIGN KEY constraints on organization_id and
// fetch_source_id to the events table (SQLite requires a full table rebuild).
func migrateEventsFK() {
	var schema string
	if err := db.QueryRow(
		"SELECT sql FROM sqlite_master WHERE type='table' AND name='events'",
	).Scan(&schema); err != nil || strings.Contains(schema, "FOREIGN KEY (organization_id)") {
		return
	}

	conn, err := db.Conn(context.Background())
	if err != nil {
		log.Printf("migrateEventsFK: get conn: %v", err)
		return
	}
	defer conn.Close()

	if _, err = conn.ExecContext(context.Background(), "PRAGMA foreign_keys=OFF"); err != nil {
		log.Printf("migrateEventsFK: pragma off: %v", err)
		return
	}

	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
		log.Printf("migrateEventsFK: begin: %v", err)
		return
	}

	stmts := []string{
		`CREATE TABLE events_v2 (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			uid TEXT UNIQUE,
			title TEXT NOT NULL,
			description TEXT,
			start_time INTEGER NOT NULL,
			end_time INTEGER NOT NULL,
			location_id INTEGER,
			organization_id INTEGER,
			has_ball INTEGER DEFAULT 0,
			has_workshop INTEGER DEFAULT 0,
			has_festival INTEGER DEFAULT 0,
			is_cancelled INTEGER DEFAULT 0,
			is_published INTEGER DEFAULT 0,
			short_code TEXT UNIQUE,
			url TEXT,
			source TEXT,
			source_last_modified INTEGER,
			pricing TEXT,
			workshop_difficulty TEXT DEFAULT '',
			booking_url TEXT DEFAULT '',
			availability TEXT DEFAULT '',
			tickets_total INTEGER DEFAULT 0,
			booking_enabled INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			changed_at INTEGER,
			changed_by TEXT DEFAULT '',
			fetch_source_id INTEGER,
			suggester_email TEXT DEFAULT '',
			suggestion_token TEXT,
			FOREIGN KEY (location_id)     REFERENCES locations(id),
			FOREIGN KEY (organization_id) REFERENCES organizations(id) ON DELETE SET NULL,
			FOREIGN KEY (fetch_source_id) REFERENCES fetch_sources(id) ON DELETE SET NULL
		)`,
		`INSERT INTO events_v2
			SELECT id, uid, title, description, start_time, end_time, location_id, organization_id,
				has_ball, has_workshop, has_festival, is_cancelled, is_published, short_code,
				url, source, source_last_modified, pricing, workshop_difficulty, booking_url,
				availability, tickets_total, booking_enabled, created_at, changed_at, changed_by,
				fetch_source_id, suggester_email, suggestion_token FROM events`,
		`DROP TABLE events`,
		`ALTER TABLE events_v2 RENAME TO events`,
		`CREATE INDEX IF NOT EXISTS idx_events_url             ON events(url) WHERE url IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_events_published_start ON events(is_published, start_time)`,
		`CREATE INDEX IF NOT EXISTS idx_events_title_location  ON events(title, location_id)`,
		`CREATE INDEX IF NOT EXISTS idx_events_location_id     ON events(location_id)`,
		`CREATE INDEX IF NOT EXISTS idx_events_organization_id ON events(organization_id) WHERE organization_id IS NOT NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_events_uid ON events(uid) WHERE uid IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_events_end_time        ON events(end_time)`,
		`CREATE INDEX IF NOT EXISTS idx_events_created_at      ON events(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_events_suggestion_token ON events(suggestion_token)`,
		`CREATE INDEX IF NOT EXISTS idx_events_time_range      ON events(start_time, end_time)`,
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(context.Background(), stmt); err != nil {
			tx.Rollback()
			conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
			log.Printf("migrateEventsFK: %v", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("migrateEventsFK: commit: %v", err)
	}
	conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
	log.Printf("migrateEventsFK: added FK constraints on organization_id and fetch_source_id")
}

func migrateDB() {
	applied := func(v int) bool {
		var c int
		db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version=?", v).Scan(&c)
		return c > 0
	}
	mark := func(v int) {
		db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(?)", v)
	}

	// v1: all legacy migrations. Each ALTER TABLE silently fails when the column
	// already exists, so re-running the block on an existing instance is harmless.
	// createTables() marks v1 applied on fresh installs, so this block is skipped.
	if !applied(1) {
		db.Exec("ALTER TABLE events ADD COLUMN organization_id INTEGER")
		db.Exec("ALTER TABLE events ADD COLUMN source TEXT")
		db.Exec("ALTER TABLE users ADD COLUMN telegram TEXT")
		db.Exec("ALTER TABLE users ADD COLUMN matrix TEXT")
		db.Exec("ALTER TABLE users ADD COLUMN email_verified INTEGER DEFAULT 0")
		db.Exec("ALTER TABLE users ADD COLUMN telegram_verified INTEGER DEFAULT 0")
		db.Exec("ALTER TABLE users ADD COLUMN matrix_verified INTEGER DEFAULT 0")
		db.Exec("ALTER TABLE locations ADD COLUMN organization_id INTEGER")
		db.Exec("ALTER TABLE locations ADD COLUMN short_name TEXT")
		db.Exec("ALTER TABLE musicians ADD COLUMN short_name TEXT")
		db.Exec("ALTER TABLE events ADD COLUMN uid TEXT")
		db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_events_uid ON events(uid) WHERE uid IS NOT NULL")
		db.Exec("ALTER TABLE api_keys ADD COLUMN expires_at DATETIME")
		db.Exec("ALTER TABLE users ADD COLUMN disabled INTEGER DEFAULT 0")
		db.Exec("ALTER TABLE users ADD COLUMN failed_login_count INTEGER DEFAULT 0")
		db.Exec("ALTER TABLE users ADD COLUMN failed_login_since DATETIME")
		db.Exec("ALTER TABLE tokens ADD COLUMN user_agent TEXT")
		db.Exec("ALTER TABLE tokens ADD COLUMN ip TEXT")
		db.Exec("ALTER TABLE tokens ADD COLUMN fingerprint TEXT")
		db.Exec("ALTER TABLE tokens ADD COLUMN last_seen_at DATETIME")
		db.Exec("ALTER TABLE events ADD COLUMN url TEXT")
		db.Exec("CREATE INDEX IF NOT EXISTS idx_events_url ON events(url) WHERE url IS NOT NULL")
		db.Exec("ALTER TABLE events ADD COLUMN source_last_modified INTEGER")
		db.Exec("ALTER TABLE events ADD COLUMN is_cancelled INTEGER DEFAULT 0")
		db.Exec("ALTER TABLE users ADD COLUMN last_magic_sent_at DATETIME")
		db.Exec("ALTER TABLE users ADD COLUMN description TEXT")
		db.Exec("ALTER TABLE users ADD COLUMN mastodon TEXT")
		db.Exec("ALTER TABLE users ADD COLUMN website TEXT")
		db.Exec(`CREATE TABLE IF NOT EXISTS magic_login_tokens (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		token TEXT UNIQUE NOT NULL,
		user_id INTEGER NOT NULL,
		expires_at DATETIME NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	)`)
		db.Exec("ALTER TABLE events ADD COLUMN pricing TEXT")
		db.Exec("ALTER TABLE events ADD COLUMN has_festival INTEGER DEFAULT 0")
		db.Exec("ALTER TABLE musicians ADD COLUMN description TEXT")
		db.Exec("ALTER TABLE musicians ADD COLUMN mbid TEXT")
		db.Exec("ALTER TABLE musicians ADD COLUMN mastodon TEXT")
		db.Exec("ALTER TABLE musicians ADD COLUMN instagram TEXT")
		db.Exec("ALTER TABLE musicians ADD COLUMN facebook TEXT")
		db.Exec("ALTER TABLE musicians ADD COLUMN soundcloud TEXT")
		db.Exec("ALTER TABLE timetable_entries ADD COLUMN description TEXT")
		db.Exec(`CREATE TABLE IF NOT EXISTS event_locations (
		event_id INTEGER NOT NULL,
		location_id INTEGER NOT NULL,
		PRIMARY KEY (event_id, location_id),
		FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE,
		FOREIGN KEY (location_id) REFERENCES locations(id) ON DELETE CASCADE
	)`)
		db.Exec("ALTER TABLE locations ADD COLUMN country TEXT")
		db.Exec(`CREATE TABLE IF NOT EXISTS event_musicians (
		event_id INTEGER NOT NULL,
		musician_id INTEGER NOT NULL,
		PRIMARY KEY (event_id, musician_id),
		FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE,
		FOREIGN KEY (musician_id) REFERENCES musicians(id) ON DELETE CASCADE
	)`)
		// Remove tables and columns that are no longer part of the schema.
		db.Exec("DROP TABLE IF EXISTS posts")
		db.Exec("DROP TABLE IF EXISTS threads")
		db.Exec("DROP TABLE IF EXISTS bookings")
		db.Exec("ALTER TABLE events DROP COLUMN capacity")                                                                      // no-op if already absent
		db.Exec("ALTER TABLE fetch_sources ADD COLUMN organization_id INTEGER REFERENCES organizations(id) ON DELETE SET NULL") // no-op if already present
		db.Exec("ALTER TABLE fetch_sources ADD COLUMN dance_ids TEXT DEFAULT '[]'")                                             // no-op if already present
		db.Exec("ALTER TABLE organizations ADD COLUMN actor_name TEXT")
		db.Exec("ALTER TABLE organizations ADD COLUMN website TEXT")
		db.Exec("ALTER TABLE organizations ADD COLUMN instagram TEXT")
		db.Exec("ALTER TABLE organizations ADD COLUMN mastodon TEXT")
		db.Exec("ALTER TABLE organizations ADD COLUMN facebook TEXT")
		db.Exec("ALTER TABLE organizations ADD COLUMN contact_email TEXT")
		db.Exec("ALTER TABLE events ADD COLUMN workshop_difficulty TEXT DEFAULT ''")
		db.Exec("ALTER TABLE events ADD COLUMN booking_url TEXT DEFAULT ''")
		migrateUsersRoles()
		db.Exec(`CREATE TABLE IF NOT EXISTS verification_tokens (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		token TEXT UNIQUE NOT NULL,
		user_id INTEGER NOT NULL,
		channel TEXT NOT NULL,
		expires_at DATETIME NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	)`)
		db.Exec("ALTER TABLE users ADD COLUMN telegram_chat_id TEXT")
		db.Exec(`CREATE TABLE IF NOT EXISTS contact_posts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_id INTEGER NOT NULL,
		type TEXT NOT NULL CHECK(type IN ('ride_offer','ride_request','sleep_offer','sleep_request','ticket_offer','ticket_request')),
		city TEXT NOT NULL,
		persons INTEGER NOT NULL DEFAULT 1,
		message TEXT DEFAULT '',
		nickname TEXT NOT NULL,
		email TEXT NOT NULL DEFAULT '',
		telegram_username TEXT,
		email_verified INTEGER DEFAULT 0,
		verify_token TEXT UNIQUE,
		delete_token TEXT UNIQUE,
		expires_at DATETIME NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE
	)`)
		db.Exec("CREATE INDEX IF NOT EXISTS idx_contact_posts_event_id ON contact_posts(event_id)")
		db.Exec("ALTER TABLE events ADD COLUMN availability TEXT DEFAULT ''")
		db.Exec("ALTER TABLE events ADD COLUMN tickets_total INTEGER DEFAULT 0")
		db.Exec("ALTER TABLE events ADD COLUMN booking_enabled INTEGER DEFAULT 0")
		db.Exec(`CREATE TABLE IF NOT EXISTS bookings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_id INTEGER NOT NULL,
		name TEXT NOT NULL,
		email TEXT NOT NULL,
		persons INTEGER NOT NULL DEFAULT 1,
		message TEXT DEFAULT '',
		status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','confirmed','approved','checked_in','cancelled')),
		verify_token TEXT UNIQUE,
		qr_token TEXT UNIQUE,
		expires_at DATETIME NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE
	)`)
		db.Exec("CREATE INDEX IF NOT EXISTS idx_bookings_event_id ON bookings(event_id)")
		db.Exec("ALTER TABLE bookings ADD COLUMN lang TEXT NOT NULL DEFAULT ''")
		db.Exec("ALTER TABLE musicians ADD COLUMN wikidata_id TEXT")
		db.Exec("ALTER TABLE musicians ADD COLUMN country TEXT")
		db.Exec("ALTER TABLE musicians ADD COLUMN begin_year INTEGER")
		db.Exec("ALTER TABLE musicians ADD COLUMN biography TEXT")
		db.Exec("ALTER TABLE musicians ADD COLUMN members_json TEXT")
		db.Exec("ALTER TABLE musicians ADD COLUMN albums_json TEXT")
		db.Exec("ALTER TABLE musicians ADD COLUMN discogs_id TEXT")
		db.Exec("ALTER TABLE musicians ADD COLUMN spotify TEXT")
		db.Exec("ALTER TABLE musicians ADD COLUMN deezer TEXT")
		db.Exec("ALTER TABLE musicians ADD COLUMN genre TEXT")
		db.Exec("CREATE INDEX IF NOT EXISTS idx_event_musicians_musician_id ON event_musicians(musician_id)")
		db.Exec("CREATE INDEX IF NOT EXISTS idx_events_organization_id ON events(organization_id) WHERE organization_id IS NOT NULL")
		db.Exec("CREATE INDEX IF NOT EXISTS idx_events_end_time ON events(end_time)")
		db.Exec(`CREATE TABLE IF NOT EXISTS dances (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT UNIQUE NOT NULL
	)`)
		db.Exec(`CREATE TABLE IF NOT EXISTS event_dances (
		event_id INTEGER NOT NULL,
		dance_id INTEGER NOT NULL,
		PRIMARY KEY (event_id, dance_id),
		FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE,
		FOREIGN KEY (dance_id) REFERENCES dances(id) ON DELETE CASCADE
	)`)
		db.Exec(`CREATE TABLE IF NOT EXISTS event_tags (
		event_id INTEGER NOT NULL,
		tag TEXT NOT NULL,
		PRIMARY KEY (event_id, tag),
		FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE
	)`)
		db.Exec("CREATE INDEX IF NOT EXISTS idx_event_tags_tag ON event_tags(tag, event_id)")
		db.Exec(`INSERT OR IGNORE INTO event_tags (event_id, tag)
		SELECT e.id, j.value FROM events e, json_each(e.tags) j WHERE j.value != ''`)
		for _, name := range []string{"balfolk", "bretonic", "swedish", "occitan", "balkan", "israelic", "tango", "forro", "salsa", "social dance"} {
			db.Exec("INSERT OR IGNORE INTO dances (name) VALUES (?)", name)
		}
		db.Exec(`CREATE TABLE IF NOT EXISTS fetch_source_dances (
		fetch_source_id INTEGER NOT NULL,
		dance_id INTEGER NOT NULL,
		PRIMARY KEY (fetch_source_id, dance_id),
		FOREIGN KEY (fetch_source_id) REFERENCES fetch_sources(id) ON DELETE CASCADE,
		FOREIGN KEY (dance_id) REFERENCES dances(id) ON DELETE CASCADE
	)`)
		db.Exec(`INSERT OR IGNORE INTO fetch_source_dances (fetch_source_id, dance_id)
		SELECT fs.id, CAST(j.value AS INTEGER)
		FROM fetch_sources fs, json_each(COALESCE(fs.dance_ids,'[]')) j
		JOIN dances d ON d.id = CAST(j.value AS INTEGER)
		WHERE fs.dance_ids IS NOT NULL AND fs.dance_ids != '[]'`)
		migrateLocationsLatLng()
		db.Exec("ALTER TABLE events ADD COLUMN changed_at INTEGER")
		db.Exec("ALTER TABLE events ADD COLUMN changed_by TEXT DEFAULT ''")
		db.Exec("ALTER TABLE events ADD COLUMN fetch_source_id INTEGER REFERENCES fetch_sources(id) ON DELETE SET NULL")
		db.Exec(`CREATE TABLE IF NOT EXISTS location_organizations (
		location_id INTEGER NOT NULL,
		organization_id INTEGER NOT NULL,
		PRIMARY KEY (location_id, organization_id),
		FOREIGN KEY (location_id) REFERENCES locations(id) ON DELETE CASCADE,
		FOREIGN KEY (organization_id) REFERENCES organizations(id) ON DELETE CASCADE
	)`)
		db.Exec(`INSERT OR IGNORE INTO location_organizations (location_id, organization_id)
		SELECT id, organization_id FROM locations WHERE organization_id IS NOT NULL`)
		// Drop stale columns now superseded by join tables. Errors are silently
		// ignored (column already gone, or SQLite < 3.35 — neither is harmful).
		db.Exec("ALTER TABLE locations DROP COLUMN organization_id")
		db.Exec("ALTER TABLE fetch_sources DROP COLUMN dance_ids")
		// Indexes missing from earlier migrations
		db.Exec("CREATE INDEX IF NOT EXISTS idx_org_members_user_id ON organization_members(user_id)")
		db.Exec("CREATE INDEX IF NOT EXISTS idx_location_organizations_org_id ON location_organizations(organization_id)")
		db.Exec("CREATE INDEX IF NOT EXISTS idx_locations_town ON locations(town)")
		db.Exec("ALTER TABLE fetch_sources ADD COLUMN last_result TEXT") // no-op if already present
		db.Exec("ALTER TABLE contact_posts ADD COLUMN telegram_username TEXT")
		db.Exec("ALTER TABLE contact_posts ADD COLUMN poster_telegram_chat_id TEXT")
		db.Exec(`CREATE TABLE IF NOT EXISTS contact_requests (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		post_id INTEGER NOT NULL,
		sender_email TEXT NOT NULL DEFAULT '',
		sender_telegram TEXT NOT NULL DEFAULT '',
		message TEXT NOT NULL,
		verify_token TEXT UNIQUE,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		expires_at INTEGER NOT NULL,
		FOREIGN KEY (post_id) REFERENCES contact_posts(id) ON DELETE CASCADE
	)`)
		migrateContactPostsCheckConstraint()
		db.Exec("ALTER TABLE timetable_entries ADD COLUMN musician_id INTEGER REFERENCES musicians(id) ON DELETE SET NULL")
		db.Exec("CREATE INDEX IF NOT EXISTS idx_events_created_at ON events(created_at)")
		db.Exec("ALTER TABLE events ADD COLUMN suggester_email TEXT DEFAULT ''")
		db.Exec("ALTER TABLE events ADD COLUMN suggestion_token TEXT")
		db.Exec("CREATE INDEX IF NOT EXISTS idx_events_suggestion_token ON events(suggestion_token)")
		db.Exec(`CREATE TABLE IF NOT EXISTS pending_registrations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		verification_token TEXT UNIQUE NOT NULL,
		approval_token     TEXT UNIQUE NOT NULL,
		username           TEXT NOT NULL,
		email              TEXT NOT NULL,
		password_hash      TEXT NOT NULL,
		reg_type           TEXT NOT NULL CHECK(reg_type IN ('join_org','new_org')),
		org_id             INTEGER,
		org_name           TEXT DEFAULT '',
		org_description    TEXT DEFAULT '',
		org_website        TEXT DEFAULT '',
		org_contact_email  TEXT DEFAULT '',
		org_actor_name     TEXT DEFAULT '',
		verification_channel TEXT NOT NULL CHECK(verification_channel IN ('email','telegram','none')),
		telegram           TEXT DEFAULT '',
		telegram_chat_id   TEXT DEFAULT '',
		verified           INTEGER DEFAULT 0,
		created_at         DATETIME DEFAULT CURRENT_TIMESTAMP,
		expires_at         INTEGER NOT NULL,
		FOREIGN KEY (org_id) REFERENCES organizations(id) ON DELETE CASCADE
	)`)
		// Drop indexes made redundant by UNIQUE constraints or composite-PK leading-column coverage.
		db.Exec("DROP INDEX IF EXISTS idx_verification_tokens_token")
		db.Exec("DROP INDEX IF EXISTS idx_magic_login_tokens_token")
		db.Exec("DROP INDEX IF EXISTS idx_invite_links_token")
		db.Exec("DROP INDEX IF EXISTS idx_contact_posts_verify_token")
		db.Exec("DROP INDEX IF EXISTS idx_contact_requests_verify_token")
		db.Exec("DROP INDEX IF EXISTS idx_bookings_verify_token")
		db.Exec("DROP INDEX IF EXISTS idx_bookings_qr_token")
		db.Exec("DROP INDEX IF EXISTS idx_event_locations_event_id")
		db.Exec("DROP INDEX IF EXISTS idx_event_musicians_event_id")
		db.Exec("DROP INDEX IF EXISTS idx_event_dances_event_id")
		db.Exec("DROP INDEX IF EXISTS idx_fetch_source_dances_source_id")
		db.Exec("DROP INDEX IF EXISTS idx_pending_reg_verification_token")
		db.Exec("DROP INDEX IF EXISTS idx_pending_reg_approval_token")
		// #192: composite index for time-range queries.
		db.Exec("CREATE INDEX IF NOT EXISTS idx_events_time_range ON events(start_time, end_time)")
		// #193: convert expires_at and other timestamp fields from RFC3339 text to unix epoch integer.
		// strftime('%s', ...) understands ISO 8601 / RFC3339 format; typeof()='text' guards are
		// idempotent — rows already stored as integers are left unchanged.
		db.Exec("UPDATE tokens SET expires_at = CAST(strftime('%s', expires_at) AS INTEGER) WHERE typeof(expires_at) = 'text'")
		db.Exec("UPDATE tokens SET last_seen_at = CAST(strftime('%s', last_seen_at) AS INTEGER) WHERE last_seen_at IS NOT NULL AND typeof(last_seen_at) = 'text'")
		db.Exec("UPDATE verification_tokens SET expires_at = CAST(strftime('%s', expires_at) AS INTEGER) WHERE typeof(expires_at) = 'text'")
		db.Exec("UPDATE magic_login_tokens SET expires_at = CAST(strftime('%s', expires_at) AS INTEGER) WHERE typeof(expires_at) = 'text'")
		db.Exec("UPDATE invite_links SET expires_at = CAST(strftime('%s', expires_at) AS INTEGER) WHERE typeof(expires_at) = 'text'")
		db.Exec("UPDATE invite_links SET used_at = CAST(strftime('%s', used_at) AS INTEGER) WHERE used_at IS NOT NULL AND typeof(used_at) = 'text'")
		db.Exec("UPDATE api_keys SET expires_at = CAST(strftime('%s', expires_at) AS INTEGER) WHERE expires_at IS NOT NULL AND typeof(expires_at) = 'text'")
		db.Exec("UPDATE contact_posts SET expires_at = CAST(strftime('%s', expires_at) AS INTEGER) WHERE typeof(expires_at) = 'text'")
		db.Exec("UPDATE contact_requests SET expires_at = CAST(strftime('%s', expires_at) AS INTEGER) WHERE typeof(expires_at) = 'text'")
		db.Exec("UPDATE bookings SET expires_at = CAST(strftime('%s', expires_at) AS INTEGER) WHERE typeof(expires_at) = 'text'")
		db.Exec("UPDATE pending_registrations SET expires_at = CAST(strftime('%s', expires_at) AS INTEGER) WHERE typeof(expires_at) = 'text'")
		db.Exec("UPDATE fetch_sources SET last_fetched_at = CAST(strftime('%s', last_fetched_at) AS INTEGER) WHERE last_fetched_at IS NOT NULL AND typeof(last_fetched_at) = 'text'")
		db.Exec("UPDATE users SET failed_login_since = CAST(strftime('%s', failed_login_since) AS INTEGER) WHERE failed_login_since IS NOT NULL AND typeof(failed_login_since) = 'text'")
		db.Exec("UPDATE users SET last_magic_sent_at = CAST(strftime('%s', last_magic_sent_at) AS INTEGER) WHERE last_magic_sent_at IS NOT NULL AND typeof(last_magic_sent_at) = 'text'")
		// #195: drop legacy events.tags column; event_tags join table is the source of truth.
		db.Exec("ALTER TABLE events DROP COLUMN tags")
		// #208: canonical tags vocabulary table.
		db.Exec(`CREATE TABLE IF NOT EXISTS tags (
		slug     TEXT PRIMARY KEY,
		name     TEXT NOT NULL,
		category TEXT NOT NULL CHECK(category IN ('format','level','type'))
	)`)
		db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('bal-folk',     'Bal Folk',      'format')")
		db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('fest-noz',     'Fest Noz',      'format')")
		db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('session',      'Session',       'format')")
		db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('concert',      'Concert',       'format')")
		db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('festival',     'Festival',      'format')")
		db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('open-air',     'Open Air',      'format')")
		db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('ball',         'Ball',          'format')")
		db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('workshop',          'Workshop',          'type')")
		db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('dance-workshop',    'Dance Workshop',    'type')")
		db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('musician-workshop', 'Musician Workshop', 'type')")
		db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('music-course',      'Music Course',      'type')")
		db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('beginners',    'Beginners',     'level')")
		db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('intermediate', 'Intermediate',  'level')")
		db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('advanced',     'Advanced',      'level')")
		// #209: remap free-text event_tags rows to canonical slugs; delete the rest.
		// OR IGNORE skips rows where the rename would duplicate an existing (event_id, slug) pair.
		db.Exec("UPDATE OR IGNORE event_tags SET tag = 'bal-folk'     WHERE lower(tag) IN ('balfolk','bal folk','bal-folk','bal folk festival')")
		db.Exec("UPDATE OR IGNORE event_tags SET tag = 'fest-noz'     WHERE lower(tag) IN ('fest noz','fest-noz','festnoz','dañserlà')")
		db.Exec("UPDATE OR IGNORE event_tags SET tag = 'session'      WHERE lower(tag) = 'session'")
		db.Exec("UPDATE OR IGNORE event_tags SET tag = 'concert'      WHERE lower(tag) IN ('konzert','concert')")
		db.Exec("UPDATE OR IGNORE event_tags SET tag = 'festival'     WHERE lower(tag) = 'festival'")
		db.Exec("UPDATE OR IGNORE event_tags SET tag = 'open-air'     WHERE lower(tag) IN ('open air','open-air','open air bal folk')")
		db.Exec("UPDATE OR IGNORE event_tags SET tag = 'ball'         WHERE lower(tag) = 'ball'")
		db.Exec("UPDATE OR IGNORE event_tags SET tag = 'workshop'     WHERE lower(tag) IN ('workshop','tanzworkshops','workshops','lernabend/-nachmittag')")
		db.Exec("UPDATE OR IGNORE event_tags SET tag = 'music-course' WHERE lower(tag) IN ('musikkurs','musikerkurs','instrumenten-workshops','music course')")
		db.Exec("DELETE FROM event_tags WHERE tag NOT IN (SELECT slug FROM tags)")
		// #196: add FK constraints on events.organization_id and fetch_source_id.
		migrateEventsFK()
		// #197: add CHECK constraint on invite_links.role.
		migrateInviteLinksRole()
		// #213: add country_code and region to locations.
		db.Exec("ALTER TABLE locations ADD COLUMN country_code TEXT")
		db.Exec("ALTER TABLE locations ADD COLUMN region TEXT")
		// #215: add osm_id and osm_type to locations.
		db.Exec("ALTER TABLE locations ADD COLUMN osm_id INTEGER")
		db.Exec("ALTER TABLE locations ADD COLUMN osm_type TEXT")
		db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_locations_osm ON locations(osm_type, osm_id) WHERE osm_id IS NOT NULL")
		// #221: move music-course and workshop from type→format; remove ball tag.
		db.Exec("UPDATE tags SET category = 'format' WHERE slug IN ('music-course', 'workshop')")
		db.Exec("DELETE FROM event_tags WHERE tag = 'ball'")
		db.Exec("DELETE FROM tags WHERE slug = 'ball'")
		// #214: normalize free-text country to ISO 3166-1 alpha-2.
		for _, row := range []struct{ code, country string }{
			{"AD", "Andorra"},
			{"AT", "Austria"}, {"AT", "Österreich"}, {"AT", "Oesterreich"},
			{"BE", "Belgium"}, {"BE", "Belgique"}, {"BE", "België"},
			{"CH", "Switzerland"}, {"CH", "Schweiz"}, {"CH", "Suisse"}, {"CH", "Svizzera"},
			{"CZ", "Czech Republic"}, {"CZ", "Czechia"}, {"CZ", "Tschechien"},
			{"DE", "Germany"}, {"DE", "Deutschland"}, {"DE", "germany"}, {"DE", "de"},
			{"DK", "Denmark"}, {"DK", "Dänemark"}, {"DK", "Danmark"},
			{"ES", "Spain"}, {"ES", "España"}, {"ES", "Spanien"},
			{"FI", "Finland"}, {"FI", "Finnland"},
			{"FR", "France"}, {"FR", "france"}, {"FR", "fr"},
			{"GB", "United Kingdom"}, {"GB", "UK"}, {"GB", "Great Britain"},
			{"HR", "Croatia"}, {"HR", "Kroatien"},
			{"HU", "Hungary"}, {"HU", "Ungarn"}, {"HU", "Magyarország"},
			{"IE", "Ireland"}, {"IE", "Irland"},
			{"IT", "Italy"}, {"IT", "Italien"}, {"IT", "Italia"},
			{"LU", "Luxembourg"}, {"LU", "Luxemburg"},
			{"NL", "Netherlands"}, {"NL", "Niederlande"}, {"NL", "Nederland"},
			{"NO", "Norway"}, {"NO", "Norwegen"}, {"NO", "Norge"},
			{"PL", "Poland"}, {"PL", "Polen"},
			{"PT", "Portugal"},
			{"RO", "Romania"}, {"RO", "Rumänien"},
			{"SE", "Sweden"}, {"SE", "Schweden"}, {"SE", "Sverige"},
			{"SI", "Slovenia"}, {"SI", "Slowenien"},
			{"SK", "Slovakia"}, {"SK", "Slowakei"},
			{"US", "United States"}, {"US", "USA"},
		} {
			db.Exec("UPDATE locations SET country_code = ? WHERE country_code IS NULL AND country = ?", row.code, row.country)
		}
		// #229: food and drink availability fields.
		db.Exec("ALTER TABLE events ADD COLUMN food TEXT DEFAULT ''")
		db.Exec("ALTER TABLE events ADD COLUMN drink TEXT DEFAULT ''")
		// #248: backfill bal-folk tag for events with has_ball=1 that lost it when ball tag was deleted.
		db.Exec("INSERT OR IGNORE INTO event_tags (event_id, tag) SELECT id, 'bal-folk' FROM events WHERE has_ball = 1")
		// #342: template integration for fetch sources.
		db.Exec("ALTER TABLE fetch_sources ADD COLUMN template_id INTEGER REFERENCES event_templates(id) ON DELETE SET NULL")
		db.Exec("ALTER TABLE fetch_sources ADD COLUMN template_mode TEXT NOT NULL DEFAULT ''")
		// #366: per-org and per-location markdown notes.
		db.Exec("ALTER TABLE organizations ADD COLUMN notes_md TEXT")
		db.Exec("ALTER TABLE locations ADD COLUMN notes_md TEXT")
		// #367-370: location accessibility flags (replaced by attributes JSON in next migration).
		db.Exec("ALTER TABLE locations ADD COLUMN wheelchair_accessible INTEGER NOT NULL DEFAULT 0")
		db.Exec("ALTER TABLE locations ADD COLUMN hearing_loop INTEGER NOT NULL DEFAULT 0")
		db.Exec("ALTER TABLE locations ADD COLUMN visual_support INTEGER NOT NULL DEFAULT 0")
		// attributes JSON replaces individual boolean columns on locations; events get overrides.
		db.Exec("ALTER TABLE locations ADD COLUMN attributes TEXT")
		db.Exec("ALTER TABLE events ADD COLUMN attributes TEXT")
		db.Exec("ALTER TABLE locations DROP COLUMN wheelchair_accessible")
		db.Exec("ALTER TABLE locations DROP COLUMN hearing_loop")
		db.Exec("ALTER TABLE locations DROP COLUMN visual_support")
		db.Exec("ALTER TABLE organizations ADD COLUMN contact_name TEXT")
		db.Exec("ALTER TABLE events ADD COLUMN contact_name TEXT")
		db.Exec("ALTER TABLE events ADD COLUMN contact_email TEXT")
		// #377: parking space tag for locations.
		db.Exec("ALTER TABLE locations ADD COLUMN parking TEXT")
		// #378: floor/ground condition for locations and events.
		db.Exec("ALTER TABLE locations ADD COLUMN floor_condition TEXT")
		db.Exec("ALTER TABLE events ADD COLUMN floor_condition TEXT")
		// Add no_street_shoes boolean field to locations
		db.Exec("ALTER TABLE locations ADD COLUMN no_street_shoes INTEGER DEFAULT 0")
		// #380: WebAuthn passkey credentials and challenge sessions.
		db.Exec(`CREATE TABLE IF NOT EXISTS webauthn_credentials (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		credential_id BLOB NOT NULL UNIQUE,
		public_key BLOB NOT NULL,
		sign_count INTEGER NOT NULL DEFAULT 0,
		aaguid BLOB,
		name TEXT NOT NULL DEFAULT 'Passkey',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
		db.Exec(`CREATE TABLE IF NOT EXISTS webauthn_sessions (
		id TEXT PRIMARY KEY,
		data TEXT NOT NULL,
		expires_at INTEGER NOT NULL
	)`)
		// #385: user_id on contact_posts for logged-in poster association.
		db.Exec("ALTER TABLE contact_posts ADD COLUMN user_id INTEGER REFERENCES users(id)")
		// #386: replace verify_token + delete_token with a single manage_token.
		db.Exec("ALTER TABLE contact_posts ADD COLUMN manage_token TEXT")
		db.Exec("UPDATE contact_posts SET manage_token = COALESCE(delete_token, lower(hex(randomblob(16)))) WHERE manage_token IS NULL")
		db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_contact_posts_manage_token ON contact_posts(manage_token)")
		db.Exec("ALTER TABLE contact_posts DROP COLUMN verify_token")
		db.Exec("ALTER TABLE contact_posts DROP COLUMN delete_token")
		// #391: add lost_item / found_item post types; rebuild CHECK constraint if needed.
		migrateContactPostsLostFound()
		// #392: preset username/email on invite_links for registration-via-invite flow.
		db.Exec("ALTER TABLE invite_links ADD COLUMN preset_username TEXT")
		db.Exec("ALTER TABLE invite_links ADD COLUMN preset_email TEXT")
		// pending_registrations.description — motivation text shown to admin during approval.
		db.Exec("ALTER TABLE pending_registrations ADD COLUMN description TEXT DEFAULT ''")
		// Drop password_hash — no longer stored at registration time; credential setup
		// happens via the invite link after admin approval.
		db.Exec("ALTER TABLE pending_registrations DROP COLUMN password_hash")
		// #393: replace username with email identity + display_name.
		migrateUsersDropUsername()
		db.Exec("ALTER TABLE invite_links DROP COLUMN preset_username")
		db.Exec("ALTER TABLE pending_registrations DROP COLUMN username")
		// #394: changed_by_id FK on events.
		db.Exec("ALTER TABLE events ADD COLUMN changed_by_id INTEGER REFERENCES users(id)")
		db.Exec("UPDATE events SET changed_by_id = (SELECT id FROM users WHERE username = events.changed_by) WHERE changed_by IS NOT NULL AND changed_by != '' AND changed_by != 'fetch'")
		// Store authenticator flags (BackupEligible/BackupState) so FinishLogin can verify flag consistency.
		db.Exec("ALTER TABLE webauthn_credentials ADD COLUMN flags INTEGER NOT NULL DEFAULT 0")
		// #395: email Message-ID for bounce correlation; delivery_failed flag on verification tokens.
		db.Exec("ALTER TABLE verification_tokens ADD COLUMN message_id TEXT NOT NULL DEFAULT ''")
		db.Exec("ALTER TABLE verification_tokens ADD COLUMN delivery_failed INTEGER NOT NULL DEFAULT 0")
		db.Exec("ALTER TABLE pending_registrations ADD COLUMN message_id TEXT NOT NULL DEFAULT ''")
		// #398: created_by_id tracks which user created an event or musician.
		db.Exec("ALTER TABLE events ADD COLUMN created_by_id INTEGER REFERENCES users(id)")
		db.Exec("ALTER TABLE musicians ADD COLUMN created_by_id INTEGER REFERENCES users(id)")
		// #342 fix: remove the FK on fetch_sources.template_id that pointed to
		// event_templates — that table lives in web.db, not calendar.db, so the FK
		// breaks every INSERT into fetch_sources when foreign_keys=ON.
		migrateFetchSourcesDropTemplatesFK()
		// #419: store template JSON directly in fetch_sources so the importer can
		// apply it without querying event_templates (which is in web.db, not calendar.db).
		db.Exec("ALTER TABLE fetch_sources ADD COLUMN template_data TEXT")
		mark(1)
	} // end v1

	// Safety net: ensure musicians.created_by_id exists even if v1 was pre-marked
	// before this ALTER TABLE was added to the v1 block.
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('musicians') WHERE name='created_by_id'").Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE musicians ADD COLUMN created_by_id INTEGER REFERENCES users(id)")
		}
	}
	// Safety net: ensure instructors.created_by_id exists even if v8 was
	// pre-marked before this ALTER TABLE was added to the v8 block.
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('instructors') WHERE name='created_by_id'").Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE instructors ADD COLUMN created_by_id INTEGER REFERENCES users(id)")
		}
	}

	db.Exec("ALTER TABLE invite_links ADD COLUMN invite_type TEXT NOT NULL DEFAULT 'link'")
	db.Exec("ALTER TABLE pending_registrations ADD COLUMN org_actor_name TEXT DEFAULT ''")
	db.Exec("ALTER TABLE pending_registrations ADD COLUMN approved INTEGER DEFAULT 0")
	db.Exec("ALTER TABLE pending_registrations ADD COLUMN approved_invite_url TEXT DEFAULT ''")

	// v2: #425 email optional, discoverable passkey login.
	if !applied(2) {
		migrateUsersEmailOptional()
		// Clean up placeholder users from interrupted passkey registrations (begin without finish).
		db.Exec(`DELETE FROM users WHERE password_hash='' AND role != 'admin'
		         AND id NOT IN (SELECT user_id FROM webauthn_credentials)
		         AND id NOT IN (SELECT user_id FROM tokens)
		         AND created_at < datetime('now','-1 day')`)
		mark(2)
	}
	if !applied(3) {
		db.Exec("ALTER TABLE pending_registrations ADD COLUMN user_id INTEGER REFERENCES users(id)")
		mark(3)
	}
	// v4: #463 geohash on locations, #464 location_geohash on events,
	//     #469 wikidata/MB place ID on locations, #471 wikidata on orgs,
	//     #477 updated_at on musicians/locations/organizations.
	if !applied(4) {
		db.Exec("ALTER TABLE locations ADD COLUMN geohash TEXT")
		db.Exec("ALTER TABLE locations ADD COLUMN wikidata_id TEXT")
		db.Exec("ALTER TABLE locations ADD COLUMN mb_place_id TEXT")
		db.Exec("ALTER TABLE events ADD COLUMN location_geohash TEXT")
		db.Exec("CREATE INDEX IF NOT EXISTS idx_events_location_geohash ON events(location_geohash)")
		db.Exec("ALTER TABLE organizations ADD COLUMN wikidata_id TEXT")
		db.Exec("ALTER TABLE musicians ADD COLUMN updated_at INTEGER")
		db.Exec("ALTER TABLE locations ADD COLUMN updated_at INTEGER")
		db.Exec("ALTER TABLE organizations ADD COLUMN updated_at INTEGER")
		db.Exec("UPDATE musicians SET updated_at = strftime('%s', created_at) WHERE updated_at IS NULL")
		db.Exec("UPDATE locations SET updated_at = strftime('%s', created_at) WHERE updated_at IS NULL")
		db.Exec("UPDATE organizations SET updated_at = strftime('%s', created_at) WHERE updated_at IS NULL")
		mark(4)
	}
	// v5: event_series table and series_id FK on events.
	if !applied(5) {
		db.Exec(`CREATE TABLE IF NOT EXISTS event_series (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			slug TEXT UNIQUE NOT NULL,
			title TEXT NOT NULL,
			description TEXT DEFAULT '',
			organization_id INTEGER REFERENCES organizations(id) ON DELETE SET NULL,
			default_location_id INTEGER REFERENCES locations(id) ON DELETE SET NULL,
			default_start_time TEXT DEFAULT '',
			default_end_time TEXT DEFAULT '',
			invite_token TEXT UNIQUE,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at INTEGER DEFAULT 0
		)`)
		db.Exec("ALTER TABLE events ADD COLUMN series_id INTEGER REFERENCES event_series(id) ON DELETE SET NULL")
		mark(5)
	}
	// v6: location aliases for feed deduplication.
	if !applied(6) {
		db.Exec("ALTER TABLE locations ADD COLUMN aliases TEXT NOT NULL DEFAULT '[]'")
		mark(6)
	}
	// v7: TOTP support — totp_secret stores the active secret, totp_pending the unconfirmed one.
	if !applied(7) {
		db.Exec("ALTER TABLE users ADD COLUMN totp_secret TEXT")
		db.Exec("ALTER TABLE users ADD COLUMN totp_pending TEXT")
		mark(7)
	}
	// v8: instructors table and event_instructors join table.
	if !applied(8) {
		db.Exec(`CREATE TABLE IF NOT EXISTS instructors (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			bio TEXT,
			website TEXT,
			email TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`)
		db.Exec(`CREATE TABLE IF NOT EXISTS event_instructors (
			event_id INTEGER NOT NULL,
			instructor_id INTEGER NOT NULL,
			PRIMARY KEY (event_id, instructor_id),
			FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE,
			FOREIGN KEY (instructor_id) REFERENCES instructors(id) ON DELETE CASCADE
		)`)
		db.Exec("ALTER TABLE instructors ADD COLUMN created_by_id INTEGER REFERENCES users(id)")
		mark(8)
	}
	// v9: entry_type column on timetable_entries (bal/workshop).
	if !applied(9) {
		db.Exec("ALTER TABLE timetable_entries ADD COLUMN entry_type TEXT NOT NULL DEFAULT 'bal' CHECK(entry_type IN ('bal', 'workshop'))")
		mark(9)
	}
	// v10: totp_used_codes table for TOTP replay prevention.
	if !applied(10) {
		db.Exec(`CREATE TABLE IF NOT EXISTS totp_used_codes (
			user_id    INTEGER NOT NULL,
			code       TEXT NOT NULL,
			expires_at INTEGER NOT NULL,
			PRIMARY KEY (user_id, code)
		)`)
		mark(10)
	}
	// Safety net: ensure totp_used_codes table exists even if v10 was pre-marked.
	{
		db.Exec(`CREATE TABLE IF NOT EXISTS totp_used_codes (
			user_id    INTEGER NOT NULL,
			code       TEXT NOT NULL,
			expires_at INTEGER NOT NULL,
			PRIMARY KEY (user_id, code)
		)`)
	}

	// v11: rooms table (named sub-locations) and events.room_id FK.
	if !applied(11) {
		db.Exec(`CREATE TABLE IF NOT EXISTS rooms (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			location_id INTEGER NOT NULL REFERENCES locations(id) ON DELETE CASCADE,
			name TEXT NOT NULL
		)`)
		db.Exec(`CREATE INDEX IF NOT EXISTS idx_rooms_location_id ON rooms(location_id)`)
		db.Exec(`ALTER TABLE events ADD COLUMN room_id INTEGER REFERENCES rooms(id) ON DELETE SET NULL`)
		mark(11)
	}
	// Safety net: ensure rooms table and events.room_id exist even if v11 was pre-marked.
	{
		db.Exec(`CREATE TABLE IF NOT EXISTS rooms (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			location_id INTEGER NOT NULL REFERENCES locations(id) ON DELETE CASCADE,
			name TEXT NOT NULL
		)`)
		db.Exec(`CREATE INDEX IF NOT EXISTS idx_rooms_location_id ON rooms(location_id)`)
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('events') WHERE name='room_id'").Scan(&n)
		if n == 0 {
			db.Exec(`ALTER TABLE events ADD COLUMN room_id INTEGER REFERENCES rooms(id) ON DELETE SET NULL`)
		}
	}
	// v12: event_series.template_data — rich per-series defaults (pricing, tags,
	// dances, food/drink, floor_condition, attributes, contact, booking,
	// timetable) applied to every occurrence created via addSeriesDate/createSeries.
	if !applied(12) {
		db.Exec(`ALTER TABLE event_series ADD COLUMN template_data TEXT NOT NULL DEFAULT '{}'`)
		mark(12)
	}
	// Safety net: ensure event_series.template_data exists even if v12 was pre-marked.
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('event_series') WHERE name='template_data'").Scan(&n)
		if n == 0 {
			db.Exec(`ALTER TABLE event_series ADD COLUMN template_data TEXT NOT NULL DEFAULT '{}'`)
		}
	}
	// v13: musicians.email — admin-only contact address, mirroring instructors.email.
	if !applied(13) {
		db.Exec(`ALTER TABLE musicians ADD COLUMN email TEXT`)
		mark(13)
	}
	// Safety net: ensure musicians.email exists even if v13 was pre-marked.
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('musicians') WHERE name='email'").Scan(&n)
		if n == 0 {
			db.Exec(`ALTER TABLE musicians ADD COLUMN email TEXT`)
		}
	}
	// v14: event_series.musician_id/instructor_id — let a series be owned
	// directly by a musician or instructor instead of only an organization
	// (e.g. an instructor's VHS dance course with no org involved).
	if !applied(14) {
		db.Exec(`ALTER TABLE event_series ADD COLUMN musician_id INTEGER REFERENCES musicians(id) ON DELETE SET NULL`)
		db.Exec(`ALTER TABLE event_series ADD COLUMN instructor_id INTEGER REFERENCES instructors(id) ON DELETE SET NULL`)
		mark(14)
	}
	// Safety net: ensure event_series.musician_id/instructor_id exist even if v14 was pre-marked.
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('event_series') WHERE name='musician_id'").Scan(&n)
		if n == 0 {
			db.Exec(`ALTER TABLE event_series ADD COLUMN musician_id INTEGER REFERENCES musicians(id) ON DELETE SET NULL`)
		}
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('event_series') WHERE name='instructor_id'").Scan(&n)
		if n == 0 {
			db.Exec(`ALTER TABLE event_series ADD COLUMN instructor_id INTEGER REFERENCES instructors(id) ON DELETE SET NULL`)
		}
	}
	// v15: #687 parent-child locations. A room is now a normal locations row
	// with parent_id set, inheriting address/coordinates from its parent at
	// read time (resolvedLocation()) rather than copying them in. Migrates
	// the old rooms table (name-only sub-locations) into locations, repoints
	// events.room_id onto events.location_id, then drops rooms/room_id.
	if !applied(15) {
		db.Exec(`ALTER TABLE locations ADD COLUMN parent_id INTEGER REFERENCES locations(id) ON DELETE CASCADE`)
		db.Exec(`CREATE INDEX IF NOT EXISTS idx_locations_parent_id ON locations(parent_id)`)
		db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_locations_geohash_toplevel
			ON locations(geohash) WHERE parent_id IS NULL AND geohash IS NOT NULL`)
		var hasRooms int
		db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='rooms'").Scan(&hasRooms)
		if hasRooms > 0 {
			db.Exec(`INSERT INTO locations (location, parent_id) SELECT name, location_id FROM rooms`)
			db.Exec(`UPDATE events SET location_id = (
				SELECT loc.id FROM rooms rm
				JOIN locations loc ON loc.parent_id = rm.location_id AND loc.location = rm.name
				WHERE rm.id = events.room_id
			) WHERE room_id IS NOT NULL`)
			db.Exec(`DROP TABLE rooms`)
		}
		var hasRoomID int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('events') WHERE name='room_id'").Scan(&hasRoomID)
		if hasRoomID > 0 {
			db.Exec(`ALTER TABLE events DROP COLUMN room_id`)
		}
		mark(15)
	}
	// Safety net: ensure locations.parent_id and the geohash partial-unique
	// index exist even if v15 was pre-marked (legacy schema_migrations gap).
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('locations') WHERE name='parent_id'").Scan(&n)
		if n == 0 {
			db.Exec(`ALTER TABLE locations ADD COLUMN parent_id INTEGER REFERENCES locations(id) ON DELETE CASCADE`)
		}
		db.Exec(`CREATE INDEX IF NOT EXISTS idx_locations_parent_id ON locations(parent_id)`)
		db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_locations_geohash_toplevel
			ON locations(geohash) WHERE parent_id IS NULL AND geohash IS NOT NULL`)
	}
	// Safety net: ensure the old rooms table/events.room_id are gone even if
	// v15 was pre-marked without the DROP running (e.g. interrupted upgrade).
	{
		var hasRooms int
		db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='rooms'").Scan(&hasRooms)
		if hasRooms > 0 {
			db.Exec(`INSERT INTO locations (location, parent_id) SELECT name, location_id FROM rooms`)
			db.Exec(`UPDATE events SET location_id = (
				SELECT loc.id FROM rooms rm
				JOIN locations loc ON loc.parent_id = rm.location_id AND loc.location = rm.name
				WHERE rm.id = events.room_id
			) WHERE room_id IS NOT NULL`)
			db.Exec(`DROP TABLE rooms`)
		}
		var hasRoomID int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('events') WHERE name='room_id'").Scan(&hasRoomID)
		if hasRoomID > 0 {
			db.Exec(`ALTER TABLE events DROP COLUMN room_id`)
		}
	}
	// v16: add informal capacity (max people) and size_sqm (floor area) fields
	// to locations, shown as optional hints on any location or room (#875).
	if !applied(16) {
		db.Exec(`ALTER TABLE locations ADD COLUMN capacity INTEGER`)
		db.Exec(`ALTER TABLE locations ADD COLUMN size_sqm INTEGER`)
		mark(16)
	}
	// Safety net: ensure locations.capacity/size_sqm exist even if v16 was pre-marked.
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('locations') WHERE name='capacity'").Scan(&n)
		if n == 0 {
			db.Exec(`ALTER TABLE locations ADD COLUMN capacity INTEGER`)
		}
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('locations') WHERE name='size_sqm'").Scan(&n)
		if n == 0 {
			db.Exec(`ALTER TABLE locations ADD COLUMN size_sqm INTEGER`)
		}
	}
	// v17: add plan_x/plan_y — a room's position (0-1 percentage) on its
	// parent building's site-plan image (#877).
	if !applied(17) {
		db.Exec(`ALTER TABLE locations ADD COLUMN plan_x REAL`)
		db.Exec(`ALTER TABLE locations ADD COLUMN plan_y REAL`)
		mark(17)
	}
	// Safety net: ensure locations.plan_x/plan_y exist even if v17 was pre-marked.
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('locations') WHERE name='plan_x'").Scan(&n)
		if n == 0 {
			db.Exec(`ALTER TABLE locations ADD COLUMN plan_x REAL`)
		}
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('locations') WHERE name='plan_y'").Scan(&n)
		if n == 0 {
			db.Exec(`ALTER TABLE locations ADD COLUMN plan_y REAL`)
		}
	}
	// v18: instructor_id on timetable_entries, so a slot can record who's
	// teaching it (distinct from musician_id, who's playing) (#891).
	if !applied(18) {
		db.Exec("ALTER TABLE timetable_entries ADD COLUMN instructor_id INTEGER REFERENCES instructors(id) ON DELETE SET NULL")
		db.Exec("CREATE INDEX IF NOT EXISTS idx_timetable_entries_instructor_id ON timetable_entries(instructor_id)")
		mark(18)
	}
	// Safety net: ensure timetable_entries.instructor_id (and its index) exist
	// even if v18 was pre-marked by createTables()'s catch-all schema_migrations
	// insert (createTables() itself can't create this index unconditionally: on
	// an existing DB the column doesn't exist yet at that point, which would
	// abort the whole schema script with "no such column").
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('timetable_entries') WHERE name='instructor_id'").Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE timetable_entries ADD COLUMN instructor_id INTEGER REFERENCES instructors(id) ON DELETE SET NULL")
		}
		db.Exec("CREATE INDEX IF NOT EXISTS idx_timetable_entries_instructor_id ON timetable_entries(instructor_id)")
	}
	// v19: entry_date on timetable_entries, so a row can be pinned to a
	// specific day of a multi-day event (festival/workshop weekend) (#894).
	// NULL/empty means "same as the event's own start date" — the default
	// for all existing single-day events.
	if !applied(19) {
		db.Exec("ALTER TABLE timetable_entries ADD COLUMN entry_date TEXT")
		mark(19)
	}
	// Safety net: ensure timetable_entries.entry_date exists even if v19 was
	// pre-marked by createTables()'s catch-all schema_migrations insert.
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('timetable_entries') WHERE name='entry_date'").Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE timetable_entries ADD COLUMN entry_date TEXT")
		}
	}
	// v20: image_ai_generated flag on events for KI-VO compliance labeling (#933).
	if !applied(20) {
		db.Exec("ALTER TABLE events ADD COLUMN image_ai_generated INTEGER DEFAULT 0")
		mark(20)
	}
	// v21: seed event_locations from events.location_id so all existing events
	// have their primary venue in the junction table. New events populate it in
	// insertEvent; re-assignments via setEventLocationRef also maintain it.
	if !applied(21) {
		db.Exec(`INSERT OR IGNORE INTO event_locations (event_id, location_id)
			SELECT id, location_id FROM events WHERE location_id IS NOT NULL`)
		mark(21)
	}
	// v22: external_syndication on organizations — JSON blob for per-platform
	// credentials (Eventbrite, social-dance.today, etc.) (#971).
	if !applied(22) {
		db.Exec("ALTER TABLE organizations ADD COLUMN external_syndication TEXT DEFAULT NULL")
		mark(22)
	}
	// Safety net: ensure column exists even if v22 was pre-marked.
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('organizations') WHERE name='external_syndication'").Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE organizations ADD COLUMN external_syndication TEXT DEFAULT NULL")
		}
	}
	// v23: external_sync on events — JSON blob tracking per-platform sync
	// status, external IDs and URLs (#971).
	if !applied(23) {
		db.Exec("ALTER TABLE events ADD COLUMN external_sync TEXT DEFAULT NULL")
		mark(23)
	}
	// Safety net: ensure column exists even if v23 was pre-marked.
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('events') WHERE name='external_sync'").Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE events ADD COLUMN external_sync TEXT DEFAULT NULL")
		}
	}
	// v24: store events.pricing/attributes as JSONB instead of text JSON (#978).
	// json_extract/json_each/json()/jsonb() all accept both formats interchangeably,
	// so this is a one-off backfill, not a breaking change — rows written before
	// this migration and rows written after (via the now-jsonb(?)-wrapped INSERT/
	// UPDATE call sites) coexist safely under the same read queries. jsonb() is
	// idempotent and NULL-safe, so this is also safe to re-run.
	if !applied(24) {
		db.Exec("UPDATE events SET pricing = jsonb(pricing) WHERE pricing IS NOT NULL")
		db.Exec("UPDATE events SET attributes = jsonb(attributes) WHERE attributes IS NOT NULL")
		mark(24)
	}
	if !applied(25) {
		db.Exec(`CREATE TABLE IF NOT EXISTS contact_post_images (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			contact_post_id INTEGER NOT NULL REFERENCES contact_posts(id) ON DELETE CASCADE,
			created_at      INTEGER NOT NULL DEFAULT (unixepoch())
		)`)
		db.Exec(`CREATE INDEX IF NOT EXISTS idx_contact_post_images_post_id ON contact_post_images(contact_post_id)`)
		mark(25)
	}
	// Safety net: ensure contact_post_images table exists even if v25 was pre-marked.
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='contact_post_images'").Scan(&n)
		if n == 0 {
			db.Exec(`CREATE TABLE IF NOT EXISTS contact_post_images (
				id              INTEGER PRIMARY KEY AUTOINCREMENT,
				contact_post_id INTEGER NOT NULL REFERENCES contact_posts(id) ON DELETE CASCADE,
				created_at      INTEGER NOT NULL DEFAULT (unixepoch())
			)`)
			db.Exec(`CREATE INDEX IF NOT EXISTS idx_contact_post_images_post_id ON contact_post_images(contact_post_id)`)
		}
	}
	if !applied(26) {
		db.Exec(`CREATE TABLE IF NOT EXISTS verified_email_sessions (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			token_hash      TEXT    NOT NULL UNIQUE,
			email           TEXT    NOT NULL,
			nickname        TEXT    NOT NULL DEFAULT '',
			created_at      INTEGER NOT NULL DEFAULT (unixepoch()),
			absolute_expiry INTEGER NOT NULL,
			expires_at      INTEGER NOT NULL,
			last_seen_at    INTEGER NOT NULL DEFAULT (unixepoch())
		)`)
		db.Exec(`CREATE TABLE IF NOT EXISTS verified_email_session_renew_tokens (
			token_hash TEXT    PRIMARY KEY,
			email      TEXT    NOT NULL,
			expires_at INTEGER NOT NULL
		)`)
		db.Exec("ALTER TABLE contact_posts ADD COLUMN board_session_id INTEGER")
		mark(26)
	}
	// Safety net: ensure v26 tables exist even if migration was pre-marked.
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='verified_email_sessions'").Scan(&n)
		if n == 0 {
			db.Exec(`CREATE TABLE IF NOT EXISTS verified_email_sessions (
				id              INTEGER PRIMARY KEY AUTOINCREMENT,
				token_hash      TEXT    NOT NULL UNIQUE,
				email           TEXT    NOT NULL,
				nickname        TEXT    NOT NULL DEFAULT '',
				created_at      INTEGER NOT NULL DEFAULT (unixepoch()),
				absolute_expiry INTEGER NOT NULL,
				expires_at      INTEGER NOT NULL,
				last_seen_at    INTEGER NOT NULL DEFAULT (unixepoch())
			)`)
			db.Exec(`CREATE TABLE IF NOT EXISTS verified_email_session_renew_tokens (
				token_hash TEXT    PRIMARY KEY,
				email      TEXT    NOT NULL,
				expires_at INTEGER NOT NULL
			)`)
		}
	}
	// Safety net: ensure board_session_id column exists even if v26 was pre-marked.
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('contact_posts') WHERE name='board_session_id'").Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE contact_posts ADD COLUMN board_session_id INTEGER")
		}
	}
	// Safety net: backfill any events that slipped through (e.g. imported after
	// v21 ran but before insertEvent was updated). Idempotent via OR IGNORE.
	db.Exec(`INSERT OR IGNORE INTO event_locations (event_id, location_id)
		SELECT id, location_id FROM events WHERE location_id IS NOT NULL`)
	// Safety net: ensure image_ai_generated exists even if v20 was pre-marked.
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('events') WHERE name='image_ai_generated'").Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE events ADD COLUMN image_ai_generated INTEGER DEFAULT 0")
		}
	}
	if !applied(27) {
		db.Exec("ALTER TABLE organizations ADD COLUMN image_ai_generated INTEGER DEFAULT 0")
		db.Exec("ALTER TABLE musicians ADD COLUMN image_ai_generated INTEGER DEFAULT 0")
		mark(27)
	}
	// Safety net: ensure image_ai_generated exists on organizations and musicians
	// even if v27 was pre-marked (createTables pre-marks all versions on fresh
	// installs, so existing DBs without the column would otherwise be skipped).
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('organizations') WHERE name='image_ai_generated'").Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE organizations ADD COLUMN image_ai_generated INTEGER DEFAULT 0")
		}
	}
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('musicians') WHERE name='image_ai_generated'").Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE musicians ADD COLUMN image_ai_generated INTEGER DEFAULT 0")
		}
	}
	// v28: image_ai_generated flag for event series banners.
	if !applied(28) {
		db.Exec("ALTER TABLE event_series ADD COLUMN image_ai_generated INTEGER DEFAULT 0")
		mark(28)
	}
	// Safety net: ensure event_series.image_ai_generated exists even if v28 was pre-marked.
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('event_series') WHERE name='image_ai_generated'").Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE event_series ADD COLUMN image_ai_generated INTEGER DEFAULT 0")
		}
	}
	// Safety net: backfill events.organization_id from fetch_sources.organization_id
	// for events imported before insertEvent() learned to write organization_id on
	// update. Restricted to changed_by IN ('', 'fetch') so an admin who manually
	// edited the event (and may have intentionally cleared organization_id) is
	// never overwritten on subsequent runs.
	{
		db.Exec(`UPDATE events SET organization_id = (
			SELECT fs.organization_id FROM fetch_sources fs WHERE fs.id = events.fetch_source_id
		) WHERE organization_id IS NULL
		  AND fetch_source_id IS NOT NULL
		  AND COALESCE(changed_by,'') IN ('', 'fetch')
		  AND EXISTS (
			SELECT 1 FROM fetch_sources fs WHERE fs.id = events.fetch_source_id AND fs.organization_id IS NOT NULL
		  )`)
	}
	// Safety net: ensure entry_type column exists even if v9 was pre-marked.
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('timetable_entries') WHERE name='entry_type'").Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE timetable_entries ADD COLUMN entry_type TEXT NOT NULL DEFAULT 'bal' CHECK(entry_type IN ('bal', 'workshop'))")
		}
	}
	// Safety net: ensure totp columns exist even if v7 was pre-marked.
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('users') WHERE name='totp_secret'").Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE users ADD COLUMN totp_secret TEXT")
			db.Exec("ALTER TABLE users ADD COLUMN totp_pending TEXT")
		}
	}
	// Safety net: ensure aliases column exists even if v6 was pre-marked before
	// schema_migrations existed (legacy upgrade path where createTables created the
	// table and marked all versions applied without running the ALTER TABLE).
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('locations') WHERE name='aliases'").Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE locations ADD COLUMN aliases TEXT NOT NULL DEFAULT '[]'")
		}
	}
	// Safety net: ensure no_street_shoes column exists even if v1 was pre-marked
	// before this ALTER TABLE was added to the v1 block.
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('locations') WHERE name='no_street_shoes'").Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE locations ADD COLUMN no_street_shoes INTEGER DEFAULT 0")
		}
	}
	// Safety net: repopulate canonical tags vocabulary if the table is empty.
	// Happens when createTables pre-marked the v1 migration on an existing DB that
	// lacked schema_migrations, skipping all the INSERT statements in that block.
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM tags").Scan(&n)
		if n == 0 {
			db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('bal-folk',          'Bal Folk',          'format')")
			db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('fest-noz',          'Fest Noz',          'format')")
			db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('session',           'Session',           'format')")
			db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('concert',           'Concert',           'format')")
			db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('festival',          'Festival',          'format')")
			db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('open-air',          'Open Air',          'format')")
			db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('workshop',          'Workshop',          'format')")
			db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('music-course',      'Music Course',      'format')")
			db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('dance-workshop',    'Dance Workshop',    'type')")
			db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('musician-workshop', 'Musician Workshop', 'type')")
			db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('beginners',         'Beginners',         'level')")
			db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('intermediate',      'Intermediate',      'level')")
			db.Exec("INSERT OR IGNORE INTO tags (slug, name, category) VALUES ('advanced',          'Advanced',          'level')")
		}
	}
	// Index for location_organizations(location_id), used by syncLocationOrgs
	// deletes and location merge/lookup queries.
	db.Exec("CREATE INDEX IF NOT EXISTS idx_location_organizations_location_id ON location_organizations(location_id)")
	// Index for the display_name fallback lookup in login(), hit on every
	// login attempt whose email doesn't match any user.
	db.Exec("CREATE INDEX IF NOT EXISTS idx_users_display_name_nocase ON users(display_name COLLATE NOCASE)")

	// Extend verification_channel CHECK constraint to allow 'none' for contact-free
	// registrations. SQLite can't ALTER a constraint, so recreate the table when needed.
	{
		var schema string
		db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='pending_registrations'").Scan(&schema)
		if !strings.Contains(schema, "'none'") {
			db.Exec(`CREATE TABLE IF NOT EXISTS pending_registrations_new (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				verification_token TEXT UNIQUE NOT NULL,
				approval_token     TEXT UNIQUE NOT NULL,
				email              TEXT NOT NULL,
				reg_type           TEXT NOT NULL CHECK(reg_type IN ('join_org','new_org')),
				org_id             INTEGER,
				org_name           TEXT DEFAULT '',
				org_description    TEXT DEFAULT '',
				org_website        TEXT DEFAULT '',
				org_contact_email  TEXT DEFAULT '',
				verification_channel TEXT NOT NULL CHECK(verification_channel IN ('email','telegram','none')),
				telegram           TEXT DEFAULT '',
				telegram_chat_id   TEXT DEFAULT '',
				verified           INTEGER DEFAULT 0,
				created_at         DATETIME DEFAULT CURRENT_TIMESTAMP,
				expires_at         DATETIME NOT NULL,
				description        TEXT DEFAULT '',
				message_id         TEXT NOT NULL DEFAULT '',
				org_actor_name     TEXT DEFAULT '',
				approved           INTEGER DEFAULT 0,
				approved_invite_url TEXT DEFAULT '',
				user_id            INTEGER REFERENCES users(id),
				FOREIGN KEY (org_id) REFERENCES organizations(id) ON DELETE CASCADE
			)`)
			db.Exec(`INSERT INTO pending_registrations_new
				SELECT id, verification_token, approval_token, email, reg_type, org_id, org_name,
				       org_description, org_website, org_contact_email, verification_channel,
				       telegram, telegram_chat_id, verified, created_at, expires_at, description,
				       message_id, org_actor_name, approved, approved_invite_url, user_id
				FROM pending_registrations`)
			db.Exec("DROP TABLE pending_registrations")
			db.Exec("ALTER TABLE pending_registrations_new RENAME TO pending_registrations")
			// Fix existing contact-free registrations that got 'email' as a placeholder.
			db.Exec(`UPDATE pending_registrations SET verification_channel='none'
				WHERE (email='' OR email IS NULL) AND (telegram='' OR telegram IS NULL)
				AND verification_channel='email'`)
		}
	}

	// Enforce case-insensitive uniqueness on display_name. Suffix any existing
	// duplicates with #ID (keeping the lowest-id entry unchanged), then create
	// the partial unique index. The CREATE INDEX is idempotent via IF NOT EXISTS.
	{
		rows, err := db.Query(`
			SELECT id, display_name FROM users
			WHERE display_name IS NOT NULL AND display_name != ''
			AND LOWER(display_name) IN (
				SELECT LOWER(display_name) FROM users
				WHERE display_name IS NOT NULL AND display_name != ''
				GROUP BY display_name COLLATE NOCASE
				HAVING COUNT(*) > 1
			)
			ORDER BY LOWER(display_name), id`)
		if err == nil {
			type dupRow struct {
				id   int64
				name string
			}
			var dups []dupRow
			for rows.Next() {
				var d dupRow
				rows.Scan(&d.id, &d.name)
				dups = append(dups, d)
			}
			rows.Close()
			seen := map[string]bool{}
			for _, d := range dups {
				key := strings.ToLower(d.name)
				if seen[key] {
					db.Exec("UPDATE users SET display_name=? WHERE id=?",
						fmt.Sprintf("%s#%d", d.name, d.id), d.id)
				} else {
					seen[key] = true
				}
			}
		}
		db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_display_name_unique
			ON users(display_name COLLATE NOCASE)
			WHERE display_name IS NOT NULL AND display_name != ''`)
	}

	// #600: indexes on FK columns missing from original schema.
	db.Exec("CREATE INDEX IF NOT EXISTS idx_tokens_user_id                    ON tokens(user_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_events_series_id                  ON events(series_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_events_created_by_id              ON events(created_by_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_events_changed_by_id              ON events(changed_by_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_contact_posts_user_id             ON contact_posts(user_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_verification_tokens_user_id       ON verification_tokens(user_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_magic_login_tokens_user_id        ON magic_login_tokens(user_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_api_keys_user_id                  ON api_keys(user_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_webauthn_credentials_user_id      ON webauthn_credentials(user_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_invite_links_created_by           ON invite_links(created_by)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_invite_links_org_id               ON invite_links(org_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_timetable_entries_location_id     ON timetable_entries(location_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_timetable_entries_musician_id     ON timetable_entries(musician_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_timetable_entries_instructor_id   ON timetable_entries(instructor_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_event_locations_location_id       ON event_locations(location_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_event_dances_dance_id             ON event_dances(dance_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_event_instructors_event_id        ON event_instructors(event_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_event_instructors_instructor_id   ON event_instructors(instructor_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_musicians_created_by_id           ON musicians(created_by_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_instructors_created_by_id         ON instructors(created_by_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_contact_requests_post_id          ON contact_requests(post_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_pending_registrations_org_id      ON pending_registrations(org_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_pending_registrations_user_id     ON pending_registrations(user_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_fetch_sources_organization_id     ON fetch_sources(organization_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_fetch_source_dances_source_id     ON fetch_source_dances(fetch_source_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_fetch_source_dances_dance_id      ON fetch_source_dances(dance_id)")

	// #603: replace non-selective (is_published, start_time) index with a partial
	// index covering only published events, ordered by the columns the hot public
	// feed query needs: end_time for range filtering, start_time for ORDER BY.
	db.Exec("DROP INDEX IF EXISTS idx_events_published_start")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_events_published_end_start ON events(end_time, start_time) WHERE is_published=1")

	// #742: the public feed/tag/town endpoints (events.go getEventsPublic,
	// getEventsByTag, getEventsByTown) filter is_published=1 AND start_time >= ?
	// ordered by start_time ASC — a different query shape than the general
	// getEvents handler's default end_time-led "exclude past" filter above, which
	// idx_events_published_end_start already serves. EXPLAIN QUERY PLAN confirmed
	// this new index is used as a covering index for the start_time-led queries
	// without displacing the end_time-led one, so both are kept.
	db.Exec("CREATE INDEX IF NOT EXISTS idx_events_published_start_time ON events(start_time, end_time) WHERE is_published=1")

	// #603: index on active (email-verified, non-expired) contact posts for the
	// board listing query and the startup cleanup DELETE.
	db.Exec("CREATE INDEX IF NOT EXISTS idx_contact_posts_active ON contact_posts(expires_at, created_at) WHERE email_verified=1")

	// #639: flag low-confidence dedup matches for admin review instead of
	// silently auto-merging or silently creating duplicates.
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('events') WHERE name='needs_duplicate_review'").Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE events ADD COLUMN needs_duplicate_review INTEGER NOT NULL DEFAULT 0")
			db.Exec("ALTER TABLE events ADD COLUMN duplicate_of_id INTEGER REFERENCES events(id) ON DELETE SET NULL")
		}
		db.Exec("CREATE INDEX IF NOT EXISTS idx_events_needs_duplicate_review ON events(needs_duplicate_review) WHERE needs_duplicate_review=1")
	}

	// #643: unified "last updated by" tracking across locations, organizations,
	// musicians (which already had updated_at) and instructors (which had neither).
	// Events already track changed_at/changed_by via updateEvent, no change needed.
	for _, t := range []string{"locations", "organizations", "musicians"} {
		var n int
		db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM pragma_table_info('%s') WHERE name='updated_by'", t)).Scan(&n)
		if n == 0 {
			db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN updated_by TEXT DEFAULT ''", t))
		}
	}
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('instructors') WHERE name='updated_at'").Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE instructors ADD COLUMN updated_at INTEGER")
			db.Exec("ALTER TABLE instructors ADD COLUMN updated_by TEXT DEFAULT ''")
			db.Exec("UPDATE instructors SET updated_at = strftime('%s', created_at) WHERE updated_at IS NULL")
		}
	}

	// #691: API keys must not be stored in plaintext. Hash any row that isn't
	// already a 64-char hex SHA-256 digest (idempotent — safe to run every start).
	{
		rows, err := db.Query("SELECT id, api_key FROM api_keys WHERE length(api_key) != 64")
		if err == nil {
			type idKey struct {
				id  int
				key string
			}
			var toHash []idKey
			for rows.Next() {
				var k idKey
				if rows.Scan(&k.id, &k.key) == nil {
					toHash = append(toHash, k)
				}
			}
			rows.Close()
			for _, k := range toHash {
				db.Exec("UPDATE api_keys SET api_key=? WHERE id=?", hashAPIKey(k.key), k.id)
			}
		}
	}

	// #693: IP-pinned short-lived tokens for publisher integrations.
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('tokens') WHERE name='ip_pinned'").Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE tokens ADD COLUMN ip_pinned INTEGER NOT NULL DEFAULT 0")
		}
	}

	// #694: arbitrary JSON metadata per user (client_name, client_url for publishers; extensible for all roles).
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('users') WHERE name='user_metadata'").Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE users ADD COLUMN user_metadata TEXT")
		}
	}

	// #700: track consecutive fetch failures per source so admins can be
	// alerted once a feed has been broken for a while, instead of never.
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('fetch_sources') WHERE name='consecutive_failures'").Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE fetch_sources ADD COLUMN consecutive_failures INTEGER NOT NULL DEFAULT 0")
		}
	}

	// #741: audit columns (created_by_id, updated_at, updated_by) for dances, tags,
	// fetch_sources, and event_series, matching the pattern established for events/locations/musicians.
	for _, col := range []struct{ table, column, definition string }{
		{"dances", "created_at", "DATETIME DEFAULT CURRENT_TIMESTAMP"},
		{"dances", "created_by_id", "INTEGER REFERENCES users(id)"},
		{"dances", "updated_at", "INTEGER"},
		{"dances", "updated_by", "TEXT DEFAULT ''"},
		{"tags", "created_at", "DATETIME DEFAULT CURRENT_TIMESTAMP"},
		{"tags", "created_by_id", "INTEGER REFERENCES users(id)"},
		{"tags", "updated_at", "INTEGER"},
		{"tags", "updated_by", "TEXT DEFAULT ''"},
		{"fetch_sources", "created_by_id", "INTEGER REFERENCES users(id)"},
		{"fetch_sources", "updated_at", "INTEGER"},
		{"fetch_sources", "updated_by", "TEXT DEFAULT ''"},
		{"event_series", "created_by_id", "INTEGER REFERENCES users(id)"},
		{"event_series", "updated_by", "TEXT DEFAULT ''"},
	} {
		var n int
		db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM pragma_table_info('%s') WHERE name='%s'", col.table, col.column)).Scan(&n)
		if n == 0 {
			db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", col.table, col.column, col.definition))
		}
	}

	// #738: event_tags.tag needs a FK to tags.slug so orphaned rows are prevented
	// at the DB level, not just by periodic cleanup migrations.
	migrateEventTagsFK()

	// #739: CHECK constraints on enum-like TEXT columns that were only enforced by
	// Go-side validation. Each is guarded by a schema check and only rebuilds once.
	migrateFetchSourcesTypeCheck()
	migrateEventsEnumChecks()
	migrateLocationsEnumChecks()

	// #932: widen fetch_sources.type CHECK to allow 'kufer'.
	migrateFetchSourcesKuferType()
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('fetch_sources') WHERE name='kufer_config'").Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE fetch_sources ADD COLUMN kufer_config TEXT")
		}
	}

	// #895: widen timetable_entries.entry_type CHECK to allow 'break'
	// (coffee break / lunch slots), alongside the existing bal/workshop.
	migrateTimetableEntriesBreakType()

	// #893: extend timetable_entries.entry_type to allow session, dance-workshop,
	// musician-workshop — needed by the dedicated timetable editor.
	migrateTimetableEntriesExtendedTypes()

	// #740: migrate locations.aliases JSON column to location_aliases junction table.
	migrateLocationAliasesToJunction()

	// #763: drop FK from event_tags.tag → tags.slug so feed-provided tags are preserved.
	migrateEventTagsDropTagFK()

	// #924: instructors gain the same mastodon/instagram/facebook social
	// fields musicians and organizations already have.
	for _, col := range []string{"mastodon", "instagram", "facebook"} {
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('instructors') WHERE name=?", col).Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE instructors ADD COLUMN " + col + " TEXT")
		}
	}

	// #925: organizations gain a generic chat_links JSON column (Telegram/
	// Signal/WhatsApp/Threema/Matrix/mailing-list invite links), distinct
	// from the identity-linking mastodon/instagram/facebook fields.
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('organizations') WHERE name='chat_links'").Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE organizations ADD COLUMN chat_links TEXT")
		}
	}

	// #927: events gain previous_start_time, set whenever an already-published,
	// non-cancelled event's start_time shifts by >= rescheduleThreshold, so
	// eventStatus can report EventRescheduled in JSON-LD.
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('events') WHERE name='previous_start_time'").Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE events ADD COLUMN previous_start_time INTEGER")
		}
	}

	// #928/#930: magic-link edit access for event suggestions + suggester name.
	for _, col := range []struct{ name, def string }{
		{"email_verified", "INTEGER DEFAULT 0"},
		{"suggestion_token_expires_at", "INTEGER"},
		{"pending_edit_json", "TEXT"},
		{"pending_edit_submitted_at", "INTEGER"},
		{"suggester_name", "TEXT DEFAULT ''"},
	} {
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('events') WHERE name=?", col.name).Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE events ADD COLUMN " + col.name + " " + col.def)
		}
	}
	// Admin-created events (no suggester) and pre-existing rows whose suggestion
	// was already accepted under the old one-shot-token scheme are unaffected
	// by public-listing checks going forward.
	db.Exec("UPDATE events SET email_verified = 1 WHERE suggester_email = '' OR suggester_email IS NULL")
	db.Exec("UPDATE events SET email_verified = 1 WHERE suggestion_token IS NULL OR suggestion_token = ''")

	// #1041: contact_posts gain osm_id, populated by the board post form's
	// Nominatim city search (display-only for now — used to disambiguate
	// e.g. "Köln" vs "Köln-Ehrenfeld" in the city text itself).
	{
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('contact_posts') WHERE name='osm_id'").Scan(&n)
		if n == 0 {
			db.Exec("ALTER TABLE contact_posts ADD COLUMN osm_id INTEGER")
		}
	}
}

// migrateEventTagsFK adds FOREIGN KEY (tag) REFERENCES tags(slug) ON DELETE CASCADE
// to event_tags. Orphaned rows are deleted first so the new FK is not violated.
func migrateEventTagsFK() {
	var schema string
	db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='event_tags'").Scan(&schema)
	if strings.Contains(schema, "REFERENCES tags") {
		return
	}
	db.Exec("DELETE FROM event_tags WHERE tag NOT IN (SELECT slug FROM tags)")
	conn, err := db.Conn(context.Background())
	if err != nil {
		log.Printf("migrateEventTagsFK: get conn: %v", err)
		return
	}
	defer conn.Close()
	ctx := context.Background()
	conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF")
	stmts := []string{
		`CREATE TABLE event_tags_new (
			event_id INTEGER NOT NULL,
			tag TEXT NOT NULL,
			PRIMARY KEY (event_id, tag),
			FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE,
			FOREIGN KEY (tag) REFERENCES tags(slug) ON DELETE CASCADE
		)`,
		`INSERT INTO event_tags_new SELECT event_id, tag FROM event_tags`,
		`DROP TABLE event_tags`,
		`ALTER TABLE event_tags_new RENAME TO event_tags`,
	}
	for _, s := range stmts {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
			log.Printf("migrateEventTagsFK: %v", err)
			return
		}
	}
	conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_event_tags_tag ON event_tags(tag, event_id)")
	log.Printf("migrateEventTagsFK: added FK from event_tags.tag to tags.slug")
}

// migrateEventTagsDropTagFK rebuilds event_tags without the FK on tag → tags.slug
// so feed-provided category slugs are preserved rather than rejected or filtered.
// Idempotent: no-op when the schema no longer references tags.
func migrateEventTagsDropTagFK() {
	var schema string
	db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='event_tags'").Scan(&schema)
	if !strings.Contains(schema, "REFERENCES tags") {
		return
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		log.Printf("migrateEventTagsDropTagFK: get conn: %v", err)
		return
	}
	defer conn.Close()
	ctx := context.Background()
	conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF")
	stmts := []string{
		`CREATE TABLE event_tags_new (
			event_id INTEGER NOT NULL,
			tag TEXT NOT NULL,
			PRIMARY KEY (event_id, tag),
			FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE
		)`,
		`INSERT INTO event_tags_new SELECT event_id, tag FROM event_tags`,
		`DROP TABLE event_tags`,
		`ALTER TABLE event_tags_new RENAME TO event_tags`,
	}
	for _, s := range stmts {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
			log.Printf("migrateEventTagsDropTagFK: %v", err)
			return
		}
	}
	conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_event_tags_tag ON event_tags(tag, event_id)")
	log.Printf("migrateEventTagsDropTagFK: removed FK from event_tags.tag to tags.slug")
}

// migrateFetchSourcesTypeCheck adds CHECK(type IN (...)) to fetch_sources.type.
// Any row with a now-invalid type is coerced to 'ical' before the rebuild.
func migrateFetchSourcesTypeCheck() {
	var schema string
	db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='fetch_sources'").Scan(&schema)
	if strings.Contains(schema, "CHECK(type IN") || strings.Contains(schema, "CHECK (type IN") {
		return
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		log.Printf("migrateFetchSourcesTypeCheck: get conn: %v", err)
		return
	}
	defer conn.Close()
	ctx := context.Background()
	conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF")
	stmts := []string{
		`CREATE TABLE fetch_sources_chk (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			url TEXT UNIQUE NOT NULL,
			type TEXT NOT NULL DEFAULT 'ical' CHECK(type IN ('ical','json','folkdance-json','gancio-json','rss')),
			tags TEXT,
			organization_id INTEGER REFERENCES organizations(id) ON DELETE SET NULL,
			last_fetched_at INTEGER,
			last_result TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			template_id INTEGER,
			template_mode TEXT NOT NULL DEFAULT '',
			template_data TEXT,
			consecutive_failures INTEGER NOT NULL DEFAULT 0,
			dance_ids TEXT DEFAULT '[]',
			created_by_id INTEGER REFERENCES users(id),
			updated_at INTEGER,
			updated_by TEXT DEFAULT ''
		)`,
		`INSERT INTO fetch_sources_chk
			(id, url, type, tags, organization_id, last_fetched_at, last_result,
			 created_at, template_id, template_mode, template_data, consecutive_failures,
			 dance_ids, created_by_id, updated_at, updated_by)
		SELECT id, url,
			CASE WHEN type IN ('ical','json','folkdance-json','gancio-json','rss') THEN type ELSE 'ical' END,
			tags, organization_id, last_fetched_at, last_result,
			created_at, template_id, template_mode, template_data,
			COALESCE(consecutive_failures, 0),
			COALESCE(dance_ids, '[]'),
			created_by_id, updated_at, COALESCE(updated_by, '')
		FROM fetch_sources`,
		`DROP TABLE fetch_sources`,
		`ALTER TABLE fetch_sources_chk RENAME TO fetch_sources`,
	}
	for _, s := range stmts {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
			log.Printf("migrateFetchSourcesTypeCheck: %v", err)
			return
		}
	}
	conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_fetch_sources_organization_id ON fetch_sources(organization_id)")
	log.Printf("migrateFetchSourcesTypeCheck: added CHECK constraint to fetch_sources.type")
}

// migrateFetchSourcesKuferType widens fetch_sources.type's CHECK constraint to
// allow 'kufer' (#932), following the exact rebuild pattern used when 'rss'
// was added in migrateFetchSourcesTypeCheck.
func migrateFetchSourcesKuferType() {
	var schema string
	db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='fetch_sources'").Scan(&schema)
	if strings.Contains(schema, "'kufer'") {
		return
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		log.Printf("migrateFetchSourcesKuferType: get conn: %v", err)
		return
	}
	defer conn.Close()
	ctx := context.Background()
	conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF")
	stmts := []string{
		`CREATE TABLE fetch_sources_chk (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			url TEXT UNIQUE NOT NULL,
			type TEXT NOT NULL DEFAULT 'ical' CHECK(type IN ('ical','json','folkdance-json','gancio-json','rss','kufer')),
			tags TEXT,
			organization_id INTEGER REFERENCES organizations(id) ON DELETE SET NULL,
			last_fetched_at INTEGER,
			last_result TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			template_id INTEGER,
			template_mode TEXT NOT NULL DEFAULT '',
			template_data TEXT,
			consecutive_failures INTEGER NOT NULL DEFAULT 0,
			dance_ids TEXT DEFAULT '[]',
			created_by_id INTEGER REFERENCES users(id),
			updated_at INTEGER,
			updated_by TEXT DEFAULT '',
			kufer_config TEXT
		)`,
		`INSERT INTO fetch_sources_chk
			(id, url, type, tags, organization_id, last_fetched_at, last_result,
			 created_at, template_id, template_mode, template_data, consecutive_failures,
			 dance_ids, created_by_id, updated_at, updated_by, kufer_config)
		SELECT id, url, type, tags, organization_id, last_fetched_at, last_result,
			created_at, template_id, template_mode, template_data,
			COALESCE(consecutive_failures, 0),
			COALESCE(dance_ids, '[]'),
			created_by_id, updated_at, COALESCE(updated_by, ''), NULL
		FROM fetch_sources`,
		`DROP TABLE fetch_sources`,
		`ALTER TABLE fetch_sources_chk RENAME TO fetch_sources`,
	}
	for _, s := range stmts {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
			log.Printf("migrateFetchSourcesKuferType: %v", err)
			return
		}
	}
	conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_fetch_sources_organization_id ON fetch_sources(organization_id)")
	log.Printf("migrateFetchSourcesKuferType: added 'kufer' to fetch_sources.type CHECK constraint")
}

// migrateTimetableEntriesBreakType widens timetable_entries.entry_type's CHECK
// constraint to allow 'break' alongside 'bal'/'workshop' (#895). SQLite can't
// alter a CHECK constraint in place, so this rebuilds the table exactly like
// migrateFetchSourcesTypeCheck above.
func migrateTimetableEntriesBreakType() {
	var schema string
	db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='timetable_entries'").Scan(&schema)
	if strings.Contains(schema, "'break'") {
		return
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		log.Printf("migrateTimetableEntriesBreakType: get conn: %v", err)
		return
	}
	defer conn.Close()
	ctx := context.Background()
	conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF")
	stmts := []string{
		`CREATE TABLE timetable_entries_chk (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			event_id INTEGER NOT NULL,
			start_time TEXT NOT NULL,
			end_time TEXT NOT NULL,
			title TEXT NOT NULL,
			description TEXT,
			room TEXT,
			location_id INTEGER,
			musician_id INTEGER,
			instructor_id INTEGER,
			entry_type TEXT NOT NULL DEFAULT 'bal' CHECK(entry_type IN ('bal', 'workshop', 'break')),
			entry_date TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE,
			FOREIGN KEY (location_id) REFERENCES locations(id),
			FOREIGN KEY (musician_id) REFERENCES musicians(id) ON DELETE SET NULL,
			FOREIGN KEY (instructor_id) REFERENCES instructors(id) ON DELETE SET NULL
		)`,
		`INSERT INTO timetable_entries_chk
			(id, event_id, start_time, end_time, title, description, room,
			 location_id, musician_id, instructor_id, entry_type, entry_date, created_at)
		SELECT id, event_id, start_time, end_time, title, description, room,
			location_id, musician_id, instructor_id,
			CASE WHEN entry_type IN ('bal','workshop','break') THEN entry_type ELSE 'bal' END,
			entry_date, created_at
		FROM timetable_entries`,
		`DROP TABLE timetable_entries`,
		`ALTER TABLE timetable_entries_chk RENAME TO timetable_entries`,
	}
	for _, s := range stmts {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
			log.Printf("migrateTimetableEntriesBreakType: %v", err)
			return
		}
	}
	conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_timetable_event_id ON timetable_entries(event_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_timetable_entries_location_id ON timetable_entries(location_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_timetable_entries_musician_id ON timetable_entries(musician_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_timetable_entries_instructor_id ON timetable_entries(instructor_id)")
	log.Printf("migrateTimetableEntriesBreakType: added 'break' to timetable_entries.entry_type CHECK constraint")
}

// migrateTimetableEntriesExtendedTypes extends the entry_type CHECK in
// timetable_entries to include 'session', 'dance-workshop', 'musician-workshop'
// for the dedicated timetable editor (#893).
func migrateTimetableEntriesExtendedTypes() {
	var schema string
	db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='timetable_entries'").Scan(&schema)
	if strings.Contains(schema, "'session'") {
		return
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		log.Printf("migrateTimetableEntriesExtendedTypes: get conn: %v", err)
		return
	}
	defer conn.Close()
	ctx := context.Background()
	conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF")
	stmts := []string{
		`CREATE TABLE timetable_entries_chk (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			event_id INTEGER NOT NULL,
			start_time TEXT NOT NULL,
			end_time TEXT NOT NULL,
			title TEXT NOT NULL,
			description TEXT,
			room TEXT,
			location_id INTEGER,
			musician_id INTEGER,
			instructor_id INTEGER,
			entry_type TEXT NOT NULL DEFAULT 'bal' CHECK(entry_type IN ('bal', 'workshop', 'break', 'session', 'dance-workshop', 'musician-workshop')),
			entry_date TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE,
			FOREIGN KEY (location_id) REFERENCES locations(id),
			FOREIGN KEY (musician_id) REFERENCES musicians(id) ON DELETE SET NULL,
			FOREIGN KEY (instructor_id) REFERENCES instructors(id) ON DELETE SET NULL
		)`,
		`INSERT INTO timetable_entries_chk
			(id, event_id, start_time, end_time, title, description, room,
			 location_id, musician_id, instructor_id, entry_type, entry_date, created_at)
		SELECT id, event_id, start_time, end_time, title, description, room,
			location_id, musician_id, instructor_id,
			CASE WHEN entry_type IN ('bal','workshop','break','session','dance-workshop','musician-workshop') THEN entry_type ELSE 'bal' END,
			entry_date, created_at
		FROM timetable_entries`,
		`DROP TABLE timetable_entries`,
		`ALTER TABLE timetable_entries_chk RENAME TO timetable_entries`,
	}
	for _, s := range stmts {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
			log.Printf("migrateTimetableEntriesExtendedTypes: %v", err)
			return
		}
	}
	conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_timetable_event_id ON timetable_entries(event_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_timetable_entries_location_id ON timetable_entries(location_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_timetable_entries_musician_id ON timetable_entries(musician_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_timetable_entries_instructor_id ON timetable_entries(instructor_id)")
	log.Printf("migrateTimetableEntriesExtendedTypes: extended timetable_entries.entry_type CHECK constraint")
}

// migrateEventsEnumChecks adds CHECK constraints to events.workshop_difficulty
// and events.availability.
func migrateEventsEnumChecks() {
	var schema string
	db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='events'").Scan(&schema)
	if strings.Contains(schema, "CHECK(workshop_difficulty") || strings.Contains(schema, "CHECK (workshop_difficulty") {
		return
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		log.Printf("migrateEventsEnumChecks: get conn: %v", err)
		return
	}
	defer conn.Close()
	ctx := context.Background()
	conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF")
	stmts := []string{
		`CREATE TABLE events_chk (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			uid TEXT UNIQUE,
			title TEXT NOT NULL,
			description TEXT,
			start_time INTEGER NOT NULL,
			end_time INTEGER NOT NULL,
			location_id INTEGER,
			organization_id INTEGER,
			has_ball INTEGER DEFAULT 0,
			has_workshop INTEGER DEFAULT 0,
			has_festival INTEGER DEFAULT 0,
			is_cancelled INTEGER DEFAULT 0,
			is_published INTEGER DEFAULT 0,
			short_code TEXT UNIQUE,
			url TEXT,
			source TEXT,
			source_last_modified INTEGER,
			pricing TEXT,
			workshop_difficulty TEXT DEFAULT '' CHECK(workshop_difficulty IN ('','beginner','advanced','profi')),
			booking_url TEXT DEFAULT '',
			availability TEXT DEFAULT '' CHECK(availability IN ('','limited','sold_out')),
			tickets_total INTEGER DEFAULT 0,
			booking_enabled INTEGER DEFAULT 0,
			food TEXT DEFAULT '',
			drink TEXT DEFAULT '',
			floor_condition TEXT DEFAULT '',
			attributes TEXT,
			contact_name TEXT,
			contact_email TEXT,
			suggester_email TEXT DEFAULT '',
			suggestion_token TEXT,
			changed_at INTEGER,
			changed_by TEXT DEFAULT '',
			changed_by_id INTEGER REFERENCES users(id),
			created_by_id INTEGER REFERENCES users(id),
			fetch_source_id INTEGER,
			has_lost_found INTEGER NOT NULL DEFAULT 0,
			expires_at INTEGER,
			location_geohash TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			series_id INTEGER REFERENCES event_series(id) ON DELETE SET NULL,
			needs_duplicate_review INTEGER NOT NULL DEFAULT 0,
			duplicate_of_id INTEGER REFERENCES events(id) ON DELETE SET NULL,
			FOREIGN KEY (location_id)     REFERENCES locations(id),
			FOREIGN KEY (organization_id) REFERENCES organizations(id) ON DELETE SET NULL
		)`,
		`INSERT INTO events_chk
			(id, uid, title, description, start_time, end_time, location_id, organization_id,
			 has_ball, has_workshop, has_festival, is_cancelled, is_published, short_code, url,
			 source, source_last_modified, pricing, workshop_difficulty, booking_url, availability,
			 tickets_total, booking_enabled, food, drink, floor_condition, attributes,
			 contact_name, contact_email, suggester_email, suggestion_token,
			 changed_at, changed_by, changed_by_id, created_by_id, fetch_source_id,
			 location_geohash, created_at, series_id, needs_duplicate_review, duplicate_of_id)
		SELECT id, uid, title, description, start_time, end_time, location_id, organization_id,
			 has_ball, has_workshop, has_festival, is_cancelled, is_published, short_code, url,
			 source, source_last_modified, pricing,
			 CASE WHEN COALESCE(workshop_difficulty,'') IN ('','beginner','advanced','profi')
			      THEN COALESCE(workshop_difficulty,'') ELSE '' END,
			 booking_url,
			 CASE WHEN COALESCE(availability,'') IN ('','limited','sold_out')
			      THEN COALESCE(availability,'') ELSE '' END,
			 tickets_total, booking_enabled, food, drink, floor_condition, attributes,
			 contact_name, contact_email, suggester_email, suggestion_token,
			 changed_at, changed_by, changed_by_id, created_by_id, fetch_source_id,
			 location_geohash, created_at, series_id,
			 COALESCE(needs_duplicate_review, 0), duplicate_of_id
		FROM events`,
		`DROP TABLE events`,
		`ALTER TABLE events_chk RENAME TO events`,
	}
	for _, s := range stmts {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
			log.Printf("migrateEventsEnumChecks: %v", err)
			return
		}
	}
	conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
	// Recreate indexes dropped by the table rebuild.
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_events_uid ON events(uid) WHERE uid IS NOT NULL")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_events_organization_id ON events(organization_id) WHERE organization_id IS NOT NULL")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_events_end_time ON events(end_time)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_events_series_id ON events(series_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_events_created_by_id ON events(created_by_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_events_changed_by_id ON events(changed_by_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_events_needs_duplicate_review ON events(needs_duplicate_review) WHERE needs_duplicate_review=1")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_events_published_end_start ON events(end_time, start_time) WHERE is_published=1")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_events_published_start_time ON events(start_time, end_time) WHERE is_published=1")
	log.Printf("migrateEventsEnumChecks: added CHECK constraints to events.workshop_difficulty and events.availability")
}

// migrateLocationsEnumChecks adds CHECK constraints to locations.parking and
// locations.floor_condition, and ensures no_street_shoes column exists.
func migrateLocationsEnumChecks() {
	var schema string
	db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='locations'").Scan(&schema)
	if strings.Contains(schema, "CHECK(parking") || strings.Contains(schema, "CHECK (parking") {
		return
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		log.Printf("migrateLocationsEnumChecks: get conn: %v", err)
		return
	}
	defer conn.Close()
	ctx := context.Background()
	conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF")
	stmts := []string{
		`CREATE TABLE locations_chk (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			location TEXT NOT NULL,
			short_name TEXT,
			address TEXT,
			zipcode TEXT,
			town TEXT,
			country TEXT,
			country_code TEXT,
			region TEXT,
			latitude REAL,
			longitude REAL,
			internetsite TEXT,
			osm_id INTEGER,
			osm_type TEXT,
			geohash TEXT,
			wikidata_id TEXT,
			mb_place_id TEXT,
			notes_md TEXT,
			attributes TEXT,
			parking TEXT CHECK(parking IS NULL OR parking IN ('','none','free','paid')),
			floor_condition TEXT CHECK(floor_condition IS NULL OR floor_condition IN ('','parquet','stone','tiles','grass','sand','pavement')),
			no_street_shoes INTEGER DEFAULT 0,
			aliases TEXT NOT NULL DEFAULT '[]',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at INTEGER,
			updated_by TEXT DEFAULT '',
			organization_id INTEGER,
			wheelchair_accessible INTEGER NOT NULL DEFAULT 0,
			hearing_loop INTEGER NOT NULL DEFAULT 0,
			visual_support INTEGER NOT NULL DEFAULT 0
		)`,
		`INSERT INTO locations_chk
			(id, location, short_name, address, zipcode, town, country, country_code, region,
			 latitude, longitude, internetsite, osm_id, osm_type, geohash, wikidata_id, mb_place_id,
			 notes_md, attributes, parking, floor_condition, no_street_shoes, aliases,
			 created_at, updated_at, updated_by,
			 organization_id, wheelchair_accessible, hearing_loop, visual_support)
		SELECT id, location, short_name, address, zipcode, town, country, country_code, region,
			 latitude, longitude, internetsite, osm_id, osm_type, geohash, wikidata_id, mb_place_id,
			 notes_md, attributes,
			 CASE WHEN parking IS NULL OR parking IN ('','none','free','paid')
			      THEN parking ELSE '' END,
			 CASE WHEN floor_condition IS NULL OR floor_condition IN ('','parquet','stone','tiles','grass','sand','pavement')
			      THEN floor_condition ELSE '' END,
			 COALESCE(no_street_shoes, 0),
			 COALESCE(aliases, '[]'),
			 created_at, updated_at, COALESCE(updated_by, ''),
			 organization_id, COALESCE(wheelchair_accessible, 0), COALESCE(hearing_loop, 0), COALESCE(visual_support, 0)
		FROM locations`,
		`DROP TABLE locations`,
		`ALTER TABLE locations_chk RENAME TO locations`,
	}
	for _, s := range stmts {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
			log.Printf("migrateLocationsEnumChecks: %v", err)
			return
		}
	}
	conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_fetch_sources_organization_id ON fetch_sources(organization_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_location_organizations_location_id ON location_organizations(location_id)")
	log.Printf("migrateLocationsEnumChecks: added CHECK constraints to locations.parking and locations.floor_condition")
}

// migrateLocationAliasesToJunction migrates locations.aliases (JSON TEXT column) to
// the location_aliases junction table and drops the column from locations.
func migrateLocationAliasesToJunction() {
	var n int
	db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('locations') WHERE name='aliases'").Scan(&n)
	if n == 0 {
		return // already migrated
	}

	// Create junction table and index (idempotent).
	db.Exec(`CREATE TABLE IF NOT EXISTS location_aliases (
		location_id INTEGER NOT NULL REFERENCES locations(id) ON DELETE CASCADE,
		alias TEXT NOT NULL,
		PRIMARY KEY (location_id, alias)
	)`)
	db.Exec("CREATE INDEX IF NOT EXISTS idx_location_aliases_alias ON location_aliases(alias)")

	// Populate from existing JSON data.
	db.Exec(`INSERT OR IGNORE INTO location_aliases (location_id, alias)
		SELECT id, j.value FROM locations, json_each(COALESCE(aliases,'[]')) j
		WHERE j.value != ''`)

	// Rebuild locations table without the aliases column.
	conn, err := db.Conn(context.Background())
	if err != nil {
		log.Printf("migrateLocationAliasesToJunction: get conn: %v", err)
		return
	}
	defer conn.Close()
	ctx := context.Background()
	if _, err = conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		log.Printf("migrateLocationAliasesToJunction: pragma off: %v", err)
		return
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("migrateLocationAliasesToJunction: begin: %v", err)
		conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
		return
	}
	stmts := []string{
		`CREATE TABLE locations_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			location TEXT NOT NULL,
			short_name TEXT,
			address TEXT,
			zipcode TEXT,
			town TEXT,
			country TEXT,
			country_code TEXT,
			region TEXT,
			latitude REAL,
			longitude REAL,
			internetsite TEXT,
			osm_id INTEGER,
			osm_type TEXT,
			geohash TEXT,
			wikidata_id TEXT,
			mb_place_id TEXT,
			notes_md TEXT,
			attributes TEXT,
			parking TEXT CHECK(parking IS NULL OR parking IN ('','none','free','paid')),
			floor_condition TEXT CHECK(floor_condition IS NULL OR floor_condition IN ('','parquet','stone','tiles','grass','sand','pavement')),
			no_street_shoes INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at INTEGER,
			updated_by TEXT DEFAULT '',
			organization_id INTEGER,
			wheelchair_accessible INTEGER NOT NULL DEFAULT 0,
			hearing_loop INTEGER NOT NULL DEFAULT 0,
			visual_support INTEGER NOT NULL DEFAULT 0
		)`,
		`INSERT INTO locations_new
			(id, location, short_name, address, zipcode, town, country, country_code, region,
			 latitude, longitude, internetsite, osm_id, osm_type, geohash, wikidata_id, mb_place_id,
			 notes_md, attributes, parking, floor_condition, no_street_shoes,
			 created_at, updated_at, updated_by,
			 organization_id, wheelchair_accessible, hearing_loop, visual_support)
		SELECT id, location, short_name, address, zipcode, town, country, country_code, region,
			 latitude, longitude, internetsite, osm_id, osm_type, geohash, wikidata_id, mb_place_id,
			 notes_md, attributes,
			 CASE WHEN parking IS NULL OR parking IN ('','none','free','paid')
			      THEN parking ELSE '' END,
			 CASE WHEN floor_condition IS NULL OR floor_condition IN ('','parquet','stone','tiles','grass','sand','pavement')
			      THEN floor_condition ELSE '' END,
			 COALESCE(no_street_shoes, 0),
			 created_at, updated_at, COALESCE(updated_by, ''),
			 organization_id, COALESCE(wheelchair_accessible, 0), COALESCE(hearing_loop, 0), COALESCE(visual_support, 0)
		FROM locations`,
		`DROP TABLE locations`,
		`ALTER TABLE locations_new RENAME TO locations`,
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			tx.Rollback()
			conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
			log.Printf("migrateLocationAliasesToJunction: %v", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
		log.Printf("migrateLocationAliasesToJunction: commit: %v", err)
		return
	}
	conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_fetch_sources_organization_id ON fetch_sources(organization_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_location_organizations_location_id ON location_organizations(location_id)")
	log.Printf("migrateLocationAliasesToJunction: migrated locations.aliases to location_aliases junction table")
}

// migrateUsersEmailOptional makes users.email nullable so passkey-only accounts
// (no email address) are supported.
func migrateUsersEmailOptional() {
	var notNull int
	if err := db.QueryRow(`SELECT "notnull" FROM pragma_table_info('users') WHERE name='email'`).Scan(&notNull); err != nil || notNull == 0 {
		return // already nullable or can't determine
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		log.Printf("migrateUsersEmailOptional: get conn: %v", err)
		return
	}
	defer conn.Close()
	if _, err = conn.ExecContext(context.Background(), "PRAGMA foreign_keys=OFF"); err != nil {
		log.Printf("migrateUsersEmailOptional: pragma off: %v", err)
		return
	}
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
		log.Printf("migrateUsersEmailOptional: begin: %v", err)
		return
	}
	stmts := []string{
		`CREATE TABLE users_v3 (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			email TEXT UNIQUE,
			display_name TEXT,
			password_hash TEXT NOT NULL DEFAULT '',
			role TEXT DEFAULT 'user' CHECK(role IN ('admin', 'user', 'publisher')),
			telegram TEXT,
			matrix TEXT,
			email_verified INTEGER DEFAULT 0,
			telegram_verified INTEGER DEFAULT 0,
			matrix_verified INTEGER DEFAULT 0,
			disabled INTEGER DEFAULT 0,
			failed_login_count INTEGER DEFAULT 0,
			failed_login_since INTEGER,
			last_magic_sent_at INTEGER,
			description TEXT,
			mastodon TEXT,
			website TEXT,
			telegram_chat_id TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`INSERT INTO users_v3 SELECT id, NULLIF(email,''), display_name, password_hash, role, telegram, matrix,
		  email_verified, telegram_verified, matrix_verified, disabled, failed_login_count,
		  failed_login_since, last_magic_sent_at, description, mastodon, website, telegram_chat_id, created_at
		 FROM users`,
		`DROP TABLE users`,
		`ALTER TABLE users_v3 RENAME TO users`,
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(context.Background(), stmt); err != nil {
			tx.Rollback()
			conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
			log.Printf("migrateUsersEmailOptional: %v", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("migrateUsersEmailOptional: commit: %v", err)
	}
	conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
	log.Printf("migrateUsersEmailOptional: users.email is now nullable")
}

func migrateFetchSourcesDropTemplatesFK() {
	rows, err := db.Query("PRAGMA foreign_key_list(fetch_sources)")
	if err != nil {
		return
	}
	hasBadFK := false
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match)
		if table == "event_templates" {
			hasBadFK = true
		}
	}
	rows.Close()
	if !hasBadFK {
		return
	}

	conn, err := db.Conn(context.Background())
	if err != nil {
		log.Printf("migrateFetchSourcesDropTemplatesFK: get conn: %v", err)
		return
	}
	defer conn.Close()
	ctx := context.Background()

	conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF")
	conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS fetch_sources_v2 (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		url TEXT UNIQUE NOT NULL,
		type TEXT NOT NULL DEFAULT 'ical',
		tags TEXT,
		organization_id INTEGER REFERENCES organizations(id) ON DELETE SET NULL,
		last_fetched_at INTEGER,
		last_result TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		template_id INTEGER,
		template_mode TEXT NOT NULL DEFAULT ''
	)`)
	conn.ExecContext(ctx, `INSERT OR IGNORE INTO fetch_sources_v2
		SELECT id, url, type, tags, organization_id, last_fetched_at, last_result,
		       created_at, template_id, template_mode FROM fetch_sources`)
	conn.ExecContext(ctx, `DROP TABLE fetch_sources`)
	conn.ExecContext(ctx, `ALTER TABLE fetch_sources_v2 RENAME TO fetch_sources`)
	conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
	log.Printf("migrateFetchSourcesDropTemplatesFK: removed bad FK on fetch_sources.template_id")
}

func logUnmappedCountries() {
	rows, err := db.Query(`SELECT DISTINCT country FROM locations WHERE country != '' AND country_code IS NULL ORDER BY country`)
	if err != nil {
		return
	}
	defer rows.Close()
	var unknown []string
	for rows.Next() {
		var c string
		if rows.Scan(&c) == nil {
			unknown = append(unknown, c)
		}
	}
	if len(unknown) > 0 {
		log.Printf("WARNING: %d location(s) have unmapped country values (fix via admin UI): %v", len(unknown), unknown)
	}
}

func createTables() error {
	schema := `
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email TEXT UNIQUE,
		display_name TEXT,
		password_hash TEXT NOT NULL DEFAULT '',
		role TEXT DEFAULT 'user' CHECK(role IN ('admin', 'user', 'publisher')),
		telegram TEXT,
		matrix TEXT,
		email_verified INTEGER DEFAULT 0,
		telegram_verified INTEGER DEFAULT 0,
		matrix_verified INTEGER DEFAULT 0,
		disabled INTEGER DEFAULT 0,
		failed_login_count INTEGER DEFAULT 0,
		failed_login_since INTEGER,
		last_magic_sent_at INTEGER,
		description TEXT,
		mastodon TEXT,
		website TEXT,
		telegram_chat_id TEXT,
		totp_secret TEXT,
		totp_pending TEXT,
		user_metadata TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid TEXT UNIQUE,
		title TEXT NOT NULL,
		description TEXT,
		start_time INTEGER NOT NULL,
		end_time INTEGER NOT NULL,
		location_id INTEGER,
		organization_id INTEGER,
		has_ball INTEGER DEFAULT 0,
		has_workshop INTEGER DEFAULT 0,
		has_festival INTEGER DEFAULT 0,
		is_cancelled INTEGER DEFAULT 0,
		is_published INTEGER DEFAULT 0,
		short_code TEXT UNIQUE,
		url TEXT,
		source TEXT,
		source_last_modified INTEGER,
		pricing TEXT,
		workshop_difficulty TEXT DEFAULT '' CHECK(workshop_difficulty IN ('','beginner','advanced','profi')),
		booking_url TEXT DEFAULT '',
		availability TEXT DEFAULT '' CHECK(availability IN ('','limited','sold_out')),
		tickets_total INTEGER DEFAULT 0,
		booking_enabled INTEGER DEFAULT 0,
		food TEXT DEFAULT '',
		drink TEXT DEFAULT '',
		floor_condition TEXT DEFAULT '',
		attributes TEXT,
		contact_name TEXT,
		contact_email TEXT,
		suggester_email TEXT DEFAULT '',
		suggester_name TEXT DEFAULT '',
		suggestion_token TEXT,
		email_verified INTEGER DEFAULT 0,
		suggestion_token_expires_at INTEGER,
		pending_edit_json TEXT,
		pending_edit_submitted_at INTEGER,
		changed_at INTEGER,
		changed_by TEXT DEFAULT '',
		changed_by_id INTEGER REFERENCES users(id),
		created_by_id INTEGER REFERENCES users(id),
		fetch_source_id INTEGER,
		has_lost_found INTEGER NOT NULL DEFAULT 0,
		expires_at INTEGER,
		location_geohash TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		series_id INTEGER REFERENCES event_series(id) ON DELETE SET NULL,
		needs_duplicate_review INTEGER NOT NULL DEFAULT 0,
		duplicate_of_id INTEGER REFERENCES events(id) ON DELETE SET NULL,
		previous_start_time INTEGER,
		image_ai_generated INTEGER DEFAULT 0,
		-- location_id and organization_id are intentionally nullable (#736):
		-- events may be created without a venue (online/TBD) or outside any org (admin-only).
		-- Nullability is enforced at the endpoint level where required (e.g. non-admin batch import
		-- requires organization_id; sub-resource PUT .../location requires location_id).
		-- A room is just a location with parent_id set (#687) — location_id
		-- points at the child directly when a room is selected.
		FOREIGN KEY (location_id)     REFERENCES locations(id),
		FOREIGN KEY (organization_id) REFERENCES organizations(id) ON DELETE SET NULL
	);
	CREATE TABLE IF NOT EXISTS event_series (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		slug TEXT UNIQUE NOT NULL,
		title TEXT NOT NULL,
		description TEXT DEFAULT '',
		organization_id INTEGER REFERENCES organizations(id) ON DELETE SET NULL,
		musician_id INTEGER REFERENCES musicians(id) ON DELETE SET NULL,
		instructor_id INTEGER REFERENCES instructors(id) ON DELETE SET NULL,
		default_location_id INTEGER REFERENCES locations(id) ON DELETE SET NULL,
		default_start_time TEXT DEFAULT '',
		default_end_time TEXT DEFAULT '',
		invite_token TEXT UNIQUE,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at INTEGER DEFAULT 0,
		created_by_id INTEGER REFERENCES users(id),
		updated_by TEXT DEFAULT '',
		template_data TEXT NOT NULL DEFAULT '{}',
		image_ai_generated INTEGER DEFAULT 0
	);
	CREATE TABLE IF NOT EXISTS tokens (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		token TEXT UNIQUE NOT NULL,
		expires_at INTEGER NOT NULL,
		user_agent TEXT,
		ip TEXT,
		fingerprint TEXT,
		last_seen_at INTEGER,
		ip_pinned INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);
	-- The parent_id/geohash-partial-unique and parent_id indexes are created by
	-- migrateDB()'s unconditional safety net, not here: on an existing DB where
	-- this CREATE TABLE is a no-op (table predates parent_id), an index on that
	-- column here would fail before migrateDB() gets a chance to add it.
	CREATE TABLE IF NOT EXISTS locations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		location TEXT NOT NULL,
		short_name TEXT,
		address TEXT,
		zipcode TEXT,
		town TEXT,
		country TEXT,
		country_code TEXT,
		region TEXT,
		latitude REAL,
		longitude REAL,
		internetsite TEXT,
		osm_id INTEGER,
		osm_type TEXT,
		geohash TEXT,
		wikidata_id TEXT,
		mb_place_id TEXT,
		notes_md TEXT,
		attributes TEXT,
		parking TEXT CHECK(parking IS NULL OR parking IN ('','none','free','paid')),
		floor_condition TEXT CHECK(floor_condition IS NULL OR floor_condition IN ('','parquet','stone','tiles','grass','sand','pavement')),
		no_street_shoes INTEGER DEFAULT 0,
		parent_id INTEGER REFERENCES locations(id) ON DELETE CASCADE,
		capacity INTEGER,
		size_sqm INTEGER,
		plan_x REAL,
		plan_y REAL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at INTEGER,
		updated_by TEXT DEFAULT ''
	);
	CREATE TABLE IF NOT EXISTS location_aliases (
		location_id INTEGER NOT NULL REFERENCES locations(id) ON DELETE CASCADE,
		alias TEXT NOT NULL,
		PRIMARY KEY (location_id, alias)
	);
	CREATE INDEX IF NOT EXISTS idx_location_aliases_alias ON location_aliases(alias);
	CREATE TABLE IF NOT EXISTS musicians (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		bandname TEXT NOT NULL,
		short_name TEXT,
		internetsite TEXT,
		description TEXT,
		mbid TEXT,
		wikidata_id TEXT,
		discogs_id TEXT,
		country TEXT,
		begin_year INTEGER,
		biography TEXT,
		members_json TEXT,
		albums_json TEXT,
		mastodon TEXT,
		instagram TEXT,
		facebook TEXT,
		soundcloud TEXT,
		spotify TEXT,
		deezer TEXT,
		genre TEXT,
		email TEXT,
		image_ai_generated INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at INTEGER,
		updated_by TEXT DEFAULT '',
		created_by_id INTEGER REFERENCES users(id)
	);
	CREATE TABLE IF NOT EXISTS dances (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT UNIQUE NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		created_by_id INTEGER REFERENCES users(id),
		updated_at INTEGER,
		updated_by TEXT DEFAULT ''
	);
	CREATE TABLE IF NOT EXISTS fetch_sources (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		url TEXT UNIQUE NOT NULL,
		type TEXT NOT NULL DEFAULT 'ical' CHECK(type IN ('ical','json','folkdance-json','gancio-json','rss','kufer')),
		tags TEXT,
		organization_id INTEGER REFERENCES organizations(id) ON DELETE SET NULL,
		last_fetched_at INTEGER,
		last_result TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		template_id INTEGER,
		template_mode TEXT NOT NULL DEFAULT '',
		template_data TEXT,
		consecutive_failures INTEGER NOT NULL DEFAULT 0,
		created_by_id INTEGER REFERENCES users(id),
		updated_at INTEGER,
		updated_by TEXT DEFAULT '',
		kufer_config TEXT
	);
	CREATE TABLE IF NOT EXISTS location_organizations (
		location_id INTEGER NOT NULL,
		organization_id INTEGER NOT NULL,
		PRIMARY KEY (location_id, organization_id),
		FOREIGN KEY (location_id) REFERENCES locations(id) ON DELETE CASCADE,
		FOREIGN KEY (organization_id) REFERENCES organizations(id) ON DELETE CASCADE
	);
	CREATE TABLE IF NOT EXISTS fetch_source_dances (
		fetch_source_id INTEGER NOT NULL,
		dance_id INTEGER NOT NULL,
		PRIMARY KEY (fetch_source_id, dance_id),
		FOREIGN KEY (fetch_source_id) REFERENCES fetch_sources(id) ON DELETE CASCADE,
		FOREIGN KEY (dance_id) REFERENCES dances(id) ON DELETE CASCADE
	);
	CREATE TABLE IF NOT EXISTS api_keys (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		name TEXT NOT NULL,
		api_key TEXT UNIQUE NOT NULL,
		expires_at INTEGER,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);
	CREATE TABLE IF NOT EXISTS organizations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT UNIQUE NOT NULL,
		description TEXT DEFAULT '',
		actor_name TEXT,
		website TEXT,
		instagram TEXT,
		mastodon TEXT,
		facebook TEXT,
		contact_email TEXT,
		contact_name TEXT,
		notes_md TEXT,
		wikidata_id TEXT,
		chat_links TEXT,
		image_ai_generated INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at INTEGER,
		updated_by TEXT DEFAULT ''
	);
	CREATE TABLE IF NOT EXISTS organization_members (
		organization_id INTEGER NOT NULL,
		user_id INTEGER NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (organization_id, user_id),
		FOREIGN KEY (organization_id) REFERENCES organizations(id) ON DELETE CASCADE,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);
	CREATE TABLE IF NOT EXISTS invite_links (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		token TEXT UNIQUE NOT NULL,
		created_by INTEGER NOT NULL,
		role TEXT NOT NULL DEFAULT 'user' CHECK(role IN ('admin', 'user', 'publisher')),
		org_id INTEGER,
		expires_at INTEGER NOT NULL,
		used_at INTEGER,
		preset_email TEXT,
		invite_type TEXT NOT NULL DEFAULT 'link',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE CASCADE,
		FOREIGN KEY (org_id) REFERENCES organizations(id) ON DELETE SET NULL
	);
	CREATE TABLE IF NOT EXISTS verification_tokens (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		token TEXT UNIQUE NOT NULL,
		user_id INTEGER NOT NULL,
		channel TEXT NOT NULL CHECK(channel IN ('email','telegram','matrix')),
		expires_at INTEGER NOT NULL,
		message_id TEXT NOT NULL DEFAULT '',
		delivery_failed INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);
	CREATE TABLE IF NOT EXISTS magic_login_tokens (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		token TEXT UNIQUE NOT NULL,
		user_id INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);
	CREATE TABLE IF NOT EXISTS timetable_entries (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_id INTEGER NOT NULL,
		start_time TEXT NOT NULL,
		end_time TEXT NOT NULL,
		title TEXT NOT NULL,
		description TEXT,
		room TEXT,
		location_id INTEGER,
		musician_id INTEGER,
		instructor_id INTEGER,
		entry_type TEXT NOT NULL DEFAULT 'bal' CHECK(entry_type IN ('bal', 'workshop', 'break', 'session', 'dance-workshop', 'musician-workshop')),
		entry_date TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE,
		FOREIGN KEY (location_id) REFERENCES locations(id),
		FOREIGN KEY (musician_id) REFERENCES musicians(id) ON DELETE SET NULL,
		FOREIGN KEY (instructor_id) REFERENCES instructors(id) ON DELETE SET NULL
	);
	CREATE INDEX IF NOT EXISTS idx_timetable_event_id ON timetable_entries(event_id);
	CREATE TABLE IF NOT EXISTS event_locations (
		event_id INTEGER NOT NULL,
		location_id INTEGER NOT NULL,
		PRIMARY KEY (event_id, location_id),
		FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE,
		FOREIGN KEY (location_id) REFERENCES locations(id) ON DELETE CASCADE
	);
	CREATE TABLE IF NOT EXISTS event_musicians (
		event_id INTEGER NOT NULL,
		musician_id INTEGER NOT NULL,
		PRIMARY KEY (event_id, musician_id),
		FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE,
		FOREIGN KEY (musician_id) REFERENCES musicians(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_event_musicians_musician_id ON event_musicians(musician_id);
	CREATE TABLE IF NOT EXISTS instructors (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		bio TEXT,
		website TEXT,
		email TEXT,
		mastodon TEXT,
		instagram TEXT,
		facebook TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		created_by_id INTEGER REFERENCES users(id),
		updated_at INTEGER,
		updated_by TEXT DEFAULT ''
	);
	CREATE TABLE IF NOT EXISTS event_instructors (
		event_id INTEGER NOT NULL,
		instructor_id INTEGER NOT NULL,
		PRIMARY KEY (event_id, instructor_id),
		FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE,
		FOREIGN KEY (instructor_id) REFERENCES instructors(id) ON DELETE CASCADE
	);
	CREATE TABLE IF NOT EXISTS event_dances (
		event_id INTEGER NOT NULL,
		dance_id INTEGER NOT NULL,
		PRIMARY KEY (event_id, dance_id),
		FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE,
		FOREIGN KEY (dance_id) REFERENCES dances(id) ON DELETE CASCADE
	);
	CREATE TABLE IF NOT EXISTS event_tags (
		event_id INTEGER NOT NULL,
		tag TEXT NOT NULL,
		PRIMARY KEY (event_id, tag),
		FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE,
		FOREIGN KEY (tag) REFERENCES tags(slug) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_event_tags_tag ON event_tags(tag, event_id);
	CREATE TABLE IF NOT EXISTS contact_posts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_id INTEGER NOT NULL,
		type TEXT NOT NULL CHECK(type IN ('ride_offer','ride_request','sleep_offer','sleep_request','ticket_offer','ticket_request','lost_item','found_item')),
		city TEXT NOT NULL,
		osm_id INTEGER,
		persons INTEGER NOT NULL DEFAULT 1,
		message TEXT DEFAULT '',
		nickname TEXT NOT NULL,
		email TEXT NOT NULL DEFAULT '',
		telegram_username TEXT,
		poster_telegram_chat_id TEXT,
		email_verified INTEGER DEFAULT 0,
		manage_token TEXT UNIQUE,
		user_id INTEGER REFERENCES users(id),
		expires_at INTEGER NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_contact_posts_event_id ON contact_posts(event_id);
	CREATE TABLE IF NOT EXISTS contact_requests (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		post_id INTEGER NOT NULL,
		sender_email TEXT NOT NULL DEFAULT '',
		sender_telegram TEXT NOT NULL DEFAULT '',
		message TEXT NOT NULL,
		verify_token TEXT UNIQUE,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		expires_at DATETIME NOT NULL,
		FOREIGN KEY (post_id) REFERENCES contact_posts(id) ON DELETE CASCADE
	);
	CREATE TABLE IF NOT EXISTS bookings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_id INTEGER NOT NULL,
		name TEXT NOT NULL,
		email TEXT NOT NULL,
		persons INTEGER NOT NULL DEFAULT 1,
		message TEXT DEFAULT '',
		status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','confirmed','approved','checked_in','cancelled')),
		verify_token TEXT UNIQUE,
		qr_token TEXT UNIQUE,
		lang TEXT NOT NULL DEFAULT '',
		expires_at INTEGER NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_bookings_event_id ON bookings(event_id);
	CREATE INDEX IF NOT EXISTS idx_events_url             ON events(url) WHERE url IS NOT NULL;
	CREATE INDEX IF NOT EXISTS idx_events_published_end_start ON events(end_time, start_time) WHERE is_published=1;
	CREATE INDEX IF NOT EXISTS idx_events_published_start_time ON events(start_time, end_time) WHERE is_published=1;
	CREATE INDEX IF NOT EXISTS idx_events_title_location  ON events(title, location_id);
	CREATE INDEX IF NOT EXISTS idx_events_location_id     ON events(location_id);
	CREATE INDEX IF NOT EXISTS idx_events_organization_id ON events(organization_id) WHERE organization_id IS NOT NULL;
	CREATE INDEX IF NOT EXISTS idx_events_end_time ON events(end_time);
	CREATE INDEX IF NOT EXISTS idx_locations_location     ON locations(location);
	CREATE INDEX IF NOT EXISTS idx_locations_town         ON locations(town);
	CREATE INDEX IF NOT EXISTS idx_tokens_expires_at      ON tokens(expires_at);
	CREATE INDEX IF NOT EXISTS idx_org_members_user_id    ON organization_members(user_id);
	CREATE INDEX IF NOT EXISTS idx_location_organizations_org_id ON location_organizations(organization_id);
	CREATE INDEX IF NOT EXISTS idx_location_organizations_location_id ON location_organizations(location_id);
	CREATE INDEX IF NOT EXISTS idx_events_created_at ON events(created_at);
	CREATE TABLE IF NOT EXISTS pending_registrations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		verification_token TEXT UNIQUE NOT NULL,
		approval_token     TEXT UNIQUE NOT NULL,
		email              TEXT NOT NULL,
		description        TEXT DEFAULT '',
		reg_type           TEXT NOT NULL CHECK(reg_type IN ('join_org','new_org')),
		org_id             INTEGER,
		org_name           TEXT DEFAULT '',
		org_description    TEXT DEFAULT '',
		org_website        TEXT DEFAULT '',
		org_contact_email  TEXT DEFAULT '',
		org_actor_name     TEXT DEFAULT '',
		verification_channel TEXT NOT NULL CHECK(verification_channel IN ('email','telegram','none')),
		telegram           TEXT DEFAULT '',
		telegram_chat_id   TEXT DEFAULT '',
		verified           INTEGER DEFAULT 0,
		approved           INTEGER DEFAULT 0,
		approved_invite_url TEXT DEFAULT '',
		message_id         TEXT NOT NULL DEFAULT '',
		user_id             INTEGER REFERENCES users(id),
		created_at         DATETIME DEFAULT CURRENT_TIMESTAMP,
		expires_at         INTEGER NOT NULL,
		-- ON DELETE CASCADE is intentional (#743): a join request for a deleted org has nothing
		-- to point at and is meaningless, so silently removing it is the correct behaviour.
		FOREIGN KEY (org_id) REFERENCES organizations(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_events_time_range ON events(start_time, end_time);
	CREATE INDEX IF NOT EXISTS idx_tokens_user_id                    ON tokens(user_id);
	CREATE INDEX IF NOT EXISTS idx_events_series_id                  ON events(series_id);
	CREATE INDEX IF NOT EXISTS idx_events_created_by_id              ON events(created_by_id);
	CREATE INDEX IF NOT EXISTS idx_events_changed_by_id              ON events(changed_by_id);
	CREATE INDEX IF NOT EXISTS idx_contact_posts_user_id             ON contact_posts(user_id);
	CREATE INDEX IF NOT EXISTS idx_verification_tokens_user_id       ON verification_tokens(user_id);
	CREATE INDEX IF NOT EXISTS idx_magic_login_tokens_user_id        ON magic_login_tokens(user_id);
	CREATE INDEX IF NOT EXISTS idx_api_keys_user_id                  ON api_keys(user_id);
	CREATE INDEX IF NOT EXISTS idx_invite_links_created_by           ON invite_links(created_by);
	CREATE INDEX IF NOT EXISTS idx_invite_links_org_id               ON invite_links(org_id);
	CREATE INDEX IF NOT EXISTS idx_timetable_entries_location_id     ON timetable_entries(location_id);
	CREATE INDEX IF NOT EXISTS idx_timetable_entries_musician_id     ON timetable_entries(musician_id);
	CREATE INDEX IF NOT EXISTS idx_event_locations_location_id       ON event_locations(location_id);
	CREATE INDEX IF NOT EXISTS idx_event_dances_dance_id             ON event_dances(dance_id);
	CREATE INDEX IF NOT EXISTS idx_event_instructors_event_id        ON event_instructors(event_id);
	CREATE INDEX IF NOT EXISTS idx_event_instructors_instructor_id   ON event_instructors(instructor_id);
	CREATE INDEX IF NOT EXISTS idx_musicians_created_by_id           ON musicians(created_by_id);
	CREATE INDEX IF NOT EXISTS idx_instructors_created_by_id         ON instructors(created_by_id);
	CREATE INDEX IF NOT EXISTS idx_contact_requests_post_id          ON contact_requests(post_id);
	CREATE INDEX IF NOT EXISTS idx_pending_registrations_org_id      ON pending_registrations(org_id);
	CREATE INDEX IF NOT EXISTS idx_pending_registrations_user_id     ON pending_registrations(user_id);
	CREATE INDEX IF NOT EXISTS idx_fetch_sources_organization_id     ON fetch_sources(organization_id);
	CREATE INDEX IF NOT EXISTS idx_fetch_source_dances_source_id     ON fetch_source_dances(fetch_source_id);
	CREATE INDEX IF NOT EXISTS idx_fetch_source_dances_dance_id      ON fetch_source_dances(dance_id);
	CREATE INDEX IF NOT EXISTS idx_contact_posts_active              ON contact_posts(expires_at, created_at) WHERE email_verified=1;
	CREATE TABLE IF NOT EXISTS tags (
		slug     TEXT PRIMARY KEY,
		name     TEXT NOT NULL,
		category TEXT NOT NULL CHECK(category IN ('format','level','type')),
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		created_by_id INTEGER REFERENCES users(id),
		updated_at INTEGER,
		updated_by TEXT DEFAULT ''
	);
	CREATE TABLE IF NOT EXISTS webauthn_credentials (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		credential_id BLOB NOT NULL UNIQUE,
		public_key BLOB NOT NULL,
		sign_count INTEGER NOT NULL DEFAULT 0,
		aaguid BLOB,
		flags INTEGER NOT NULL DEFAULT 0,
		name TEXT NOT NULL DEFAULT 'Passkey',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_webauthn_credentials_user_id      ON webauthn_credentials(user_id);
	CREATE TABLE IF NOT EXISTS webauthn_sessions (
		id TEXT PRIMARY KEY,
		data TEXT NOT NULL,
		expires_at INTEGER NOT NULL
	);
	CREATE TABLE IF NOT EXISTS totp_used_codes (
		user_id    INTEGER NOT NULL,
		code       TEXT NOT NULL,
		expires_at INTEGER NOT NULL,
		PRIMARY KEY (user_id, code)
	);
	CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS contact_post_images (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		contact_post_id INTEGER NOT NULL REFERENCES contact_posts(id) ON DELETE CASCADE,
		created_at      INTEGER NOT NULL DEFAULT (unixepoch())
	);
	CREATE INDEX IF NOT EXISTS idx_contact_post_images_post_id ON contact_post_images(contact_post_id);
	CREATE TABLE IF NOT EXISTS verified_email_sessions (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		token_hash      TEXT    NOT NULL UNIQUE,
		email           TEXT    NOT NULL,
		nickname        TEXT    NOT NULL DEFAULT '',
		created_at      INTEGER NOT NULL DEFAULT (unixepoch()),
		absolute_expiry INTEGER NOT NULL,
		expires_at      INTEGER NOT NULL,
		last_seen_at    INTEGER NOT NULL DEFAULT (unixepoch())
	);
	CREATE TABLE IF NOT EXISTS verified_email_session_renew_tokens (
		token_hash TEXT    PRIMARY KEY,
		email      TEXT    NOT NULL,
		expires_at INTEGER NOT NULL
	);
	`
	_, err := db.Exec(schema)
	if err != nil {
		return err
	}
	// Mark all migrations as applied for fresh installs.
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(1)")
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(2)")
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(3)")
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(4)")
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(5)")
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(6)")
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(7)")
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(8)")
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(9)")
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(10)")
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(11)")
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(12)")
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(13)")
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(14)")
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(15)")
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(16)")
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(17)")
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(18)")
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(19)")
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(20)")
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(21)")
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(22)")
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(23)")
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(24)")
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(25)")
	db.Exec("INSERT OR IGNORE INTO schema_migrations(version) VALUES(26)")
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_display_name_unique
		ON users(display_name COLLATE NOCASE)
		WHERE display_name IS NOT NULL AND display_name != ''`)
	return nil
}

func reloadConfig(path string) {
	newCfg, err := loadConfig(path)
	if err != nil {
		log.Printf("Config reload failed: %v", err)
		return
	}
	applyDefaults(newCfg)

	if newCfg.Server.Port != config.Server.Port ||
		newCfg.Server.DBPath != config.Server.DBPath ||
		newCfg.Server.AdminSocket != config.Server.AdminSocket {
		log.Printf("Warning: port, db_path and admin_socket changes require a restart to take effect")
	}

	config = newCfg
	rateLimiter = NewRateLimiter(config.Server.RateLimit, time.Minute)
	loginRateLimiter = NewRateLimiter(config.Server.LoginRateLimit, time.Minute)
	connLimiter = NewConnLimiter(config.Server.MaxConnsPerIP)
	initSuggestRateLimiters()
	initRegisterRateLimiter()
	log.Printf("Config reloaded from %s", path)
}

func main() {
	configPath := flag.String("config", "/etc/dansal/config.yaml", "path to config.yaml")
	printVersion := flag.Bool("version", false, "print version and build date then exit")
	flag.Parse()

	if *printVersion {
		fmt.Printf("dansal %s (built %s)\n", Version, BuildTime)
		os.Exit(0)
	}

	tag := "dansal"
	if inst := instance.FromConfigArg(); inst != "" {
		tag = "dansal@" + inst
	}
	if w, err := syslog.New(syslog.LOG_INFO|syslog.LOG_DAEMON, tag); err == nil {
		log.SetOutput(w)
		log.SetFlags(0)
	}

	var err error

	berlinLoc, err = time.LoadLocation("Europe/Berlin")
	if err != nil {
		log.Fatal(err)
	}

	configFilePath = *configPath
	config, err = loadConfig(*configPath)
	if err != nil {
		log.Printf("Warning: could not load %s, using defaults: %v", *configPath, err)
		config = &Config{}
	}
	applyDefaults(config)

	if len(config.Server.AllowedOrigins) == 0 {
		if u, err := url.Parse(config.Server.BaseURL); err == nil && u.Host != "" {
			log.Printf("info: server.allowed_origins is unset — CORS restricted to base_url origin (%s://%s)", u.Scheme, u.Host)
		} else {
			log.Printf("warning: server.allowed_origins and base_url are both unset — CORS defaults to '*' (all origins); set base_url or allowed_origins in config")
		}
	}

	if err := loadOrGenerateInviteSigningKey(); err != nil {
		log.Fatal(err)
	}

	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000&_txlock=immediate&_foreign_keys=ON&_cache_size=-8000&_temp_store=memory&_mmap_size=134217728",
		config.Server.DBPath)
	db, err = sql.Open("sqlite3", dsn)
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(config.Server.DBMaxConns)
	db.SetMaxIdleConns(max(1, config.Server.DBMaxConns/2))
	db.SetConnMaxLifetime(time.Hour)
	if err = db.Ping(); err != nil {
		log.Fatal(err)
	}
	if err = createTables(); err != nil {
		log.Fatal(err)
	}
	migrateDB()
	logUnmappedCountries()
	initImageCache(config.Server.ImagesDir)
	initMusicianImageCache(config.Server.ImagesDir + "/musicians")
	initOrgImageCache(config.Server.ImagesDir + "/orgs")
	initLocationImageCache(config.Server.ImagesDir + "/locations")
	initSeriesImageCache(config.Server.ImagesDir + "/series")
	initAvatarCaches(config.Server.ImagesDir)
	initMetrics()
	startTokenCleanup()
	startScheduledBackup()
	log.Println("Database initialized successfully")

	rateLimiter = NewRateLimiter(config.Server.RateLimit, time.Minute)
	loginRateLimiter = NewRateLimiter(config.Server.LoginRateLimit, time.Minute)
	connLimiter = NewConnLimiter(config.Server.MaxConnsPerIP)
	initSuggestRateLimiters()
	initRegisterRateLimiter()
	initResendRateLimiter()
	startAutoDeclineJob()
	initWebAuthn()

	smux := http.NewServeMux()

	// auth wraps a handler with TokenMiddleware (requires valid session token).
	auth := func(h http.HandlerFunc) http.Handler { return TokenMiddleware(http.HandlerFunc(h)) }
	// optAuth enriches the response when a token is present but does not require one.
	optAuth := OptionalTokenMiddleware

	// Info endpoint (public)
	smux.HandleFunc("GET /api/v1/info", getInfo)

	// Health endpoint (public)
	smux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprintf(w, `{"status":"ok","version":%q,"time":%q}`+"\n", Version, time.Now().UTC().Format(time.RFC3339))
	})

	// Public key for validating invite-link JWTs (see invite_jwt.go, #769).
	smux.HandleFunc("GET /.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		json.NewEncoder(w).Encode(inviteJWKS())
	})

	// Authentication endpoints (no token required)
	smux.HandleFunc("GET /api/v1/login", login)
	smux.HandleFunc("POST /api/v1/login", login)
	smux.HandleFunc("DELETE /api/v1/login", logout)
	smux.HandleFunc("POST /api/v1/cert-login", certLogin)
	smux.HandleFunc("POST /api/v1/login/magic", requestMagicLogin)
	smux.HandleFunc("GET /api/v1/login/magic/{token}", useMagicLogin)

	// Verification endpoints (public)
	smux.HandleFunc("GET /api/v1/verify/{token}", consumeVerification)
	smux.HandleFunc("GET /api/v1/invites/{token}", getInviteInfo)
	smux.HandleFunc("POST /api/v1/invites/{token}", useInvite)
	smux.HandleFunc("POST /api/v1/invites/{token}/publisher", redeemPublisherInvite)
	smux.HandleFunc("POST /api/v1/invites/{token}/webauthn/begin", webauthnInviteBegin)
	smux.HandleFunc("POST /api/v1/invites/{token}/webauthn/finish", webauthnInviteFinish)
	smux.HandleFunc("POST /api/v1/auth/webauthn/login/begin", webauthnLoginBegin)
	smux.HandleFunc("POST /api/v1/auth/webauthn/login/finish", webauthnLoginFinish)
	smux.HandleFunc("POST /api/v1/auth/webauthn/totp-challenge", webauthnTOTPChallenge)
	smux.Handle("GET /api/v1/user/webauthn/credentials", auth(webauthnUserCredentialsList))
	smux.Handle("POST /api/v1/user/webauthn/register/begin", auth(webauthnUserRegisterBegin))
	smux.Handle("POST /api/v1/user/webauthn/register/finish", auth(webauthnUserRegisterFinish))
	smux.Handle("DELETE /api/v1/user/webauthn/credentials/{id}", auth(webauthnUserCredentialDelete))
	smux.Handle("GET /api/v1/auth/totp/setup", auth(totpSetupHandler))
	smux.Handle("POST /api/v1/auth/totp/confirm", auth(totpConfirmHandler))
	smux.Handle("DELETE /api/v1/auth/totp", auth(totpDisableHandler))

	// Telegram bot webhook (public, called by Telegram servers)
	smux.HandleFunc("POST /telegram/webhook", telegramWebhookHandler)

	// Contact board — public reads and post actions
	smux.HandleFunc("GET /api/v1/contact-posts", listAllContactPosts)
	smux.HandleFunc("GET /api/v1/events/{id}/contact-posts", listContactPosts)
	smux.Handle("POST /api/v1/events/{id}/contact-posts", optAuth(http.HandlerFunc(createContactPost)))
	smux.HandleFunc("OPTIONS /api/v1/events/{id}/contact-posts", optionsSchema[ContactPostCreateRequest])
	smux.HandleFunc("GET /api/v1/contact-posts/manage/{token}", getContactPostByToken)
	smux.HandleFunc("POST /api/v1/contact-posts/resend-manage", resendContactManage)
	smux.HandleFunc("POST /api/v1/board-sessions", createBoardSessionHandler)
	smux.HandleFunc("GET /api/v1/board-sessions/me", getBoardSessionMeHandler)
	smux.HandleFunc("DELETE /api/v1/board-sessions/me", deleteBoardSessionMeHandler)
	smux.HandleFunc("POST /api/v1/board-sessions/renew-request", requestBoardSessionRenewHandler)
	smux.HandleFunc("GET /api/v1/board-sessions/renew/{token}", useBoardSessionRenewHandler)
	smux.HandleFunc("GET /api/v1/contact-post-images/{img_id}", getContactPostImage)
	smux.HandleFunc("POST /api/v1/contact-posts/{id}/images", uploadContactPostImage)
	smux.HandleFunc("DELETE /api/v1/contact-posts/{id}/images/{img_id}", deleteContactPostImage)
	smux.HandleFunc("PUT /api/v1/contact-posts/{id}", putContactPost)
	smux.HandleFunc("PATCH /api/v1/contact-posts/{id}", updateContactPost)
	smux.HandleFunc("OPTIONS /api/v1/contact-posts/{id}", optionsSchema[ContactPostWriteRequest])
	smux.HandleFunc("DELETE /api/v1/contact-posts/token/{token}", deleteContactPostByManageToken)
	smux.Handle("POST /api/v1/contact-posts/{id}/contact", optAuth(http.HandlerFunc(contactPoster)))
	smux.HandleFunc("GET /api/v1/contact-requests/verify/{token}", verifyContactRequest)

	// Bookings — public create + verify
	smux.HandleFunc("POST /api/v1/events/{id}/bookings", createBooking)
	smux.HandleFunc("GET /api/v1/bookings/verify/{token}", verifyBooking)

	// Public reads — OptionalTokenMiddleware enriches the response when a valid
	// token is present (e.g. editable flag, unpublished events).
	smux.HandleFunc("GET /api/v1/vocabulary", getVocabulary)
	smux.Handle("GET /api/v1/events", optAuth(http.HandlerFunc(getEvents)))
	smux.Handle("GET /api/v1/events/{id}", optAuth(http.HandlerFunc(getEvent)))
	smux.Handle("GET /api/v1/locations", optAuth(http.HandlerFunc(getLocations)))
	smux.Handle("GET /api/v1/locations/cities", optAuth(http.HandlerFunc(getCities)))
	smux.Handle("GET /api/v1/locations/event-counts", auth(http.HandlerFunc(locationEventCounts)))
	smux.Handle("GET /api/v1/locations/{id}", optAuth(http.HandlerFunc(getLocation)))
	smux.Handle("GET /api/v1/organizations", optAuth(http.HandlerFunc(getOrganizations)))
	smux.Handle("GET /api/v1/organizations/stats", optAuth(http.HandlerFunc(getOrganizationStats)))
	smux.Handle("GET /api/v1/organizations/members", auth(getOrganizationMembersBulk))
	smux.Handle("GET /api/v1/organizations/{id}", optAuth(http.HandlerFunc(getOrganization)))
	smux.Handle("GET /api/v1/musicians", optAuth(http.HandlerFunc(getMusicians)))
	smux.Handle("GET /api/v1/musicians/{id}", optAuth(http.HandlerFunc(getMusician)))
	smux.Handle("GET /api/v1/instructors", optAuth(http.HandlerFunc(getInstructors)))
	smux.Handle("GET /api/v1/instructors/{id}", optAuth(http.HandlerFunc(getInstructor)))
	smux.Handle("GET /api/v1/events/{id}/instructors", optAuth(http.HandlerFunc(getEventInstructors)))
	smux.Handle("GET /api/v1/tags", optAuth(http.HandlerFunc(getTags)))
	smux.Handle("GET /api/v1/dances", optAuth(http.HandlerFunc(getDances)))
	smux.Handle("GET /api/v1/images/{event_id}", optAuth(http.HandlerFunc(getEventImage)))
	smux.HandleFunc("GET /api/v1/musician-images/{id}", getMusicianImage)
	smux.HandleFunc("GET /api/v1/org-images/{id}", getOrgImage)

	// Anonymous suggestion endpoints
	smux.HandleFunc("POST /api/v1/events/suggest-preview", suggestPreviewHandler)
	smux.HandleFunc("POST /api/v1/events/suggest", suggestHandler)
	smux.HandleFunc("GET /api/v1/events/suggest/verify/{token}", suggestVerifyHandler)
	smux.HandleFunc("GET /api/v1/events/suggest/manage/{token}", getSuggestManageEvent)
	smux.HandleFunc("PATCH /api/v1/events/suggest/manage/{token}", patchSuggestManageEvent)
	smux.HandleFunc("POST /api/v1/events/suggest/manage/{token}/image", postSuggestManageImage)

	// Self-registration endpoints
	smux.HandleFunc("POST /api/v1/register", registerHandler)
	smux.HandleFunc("GET /api/v1/register/status/{id}", registerStatusHandler)
	smux.HandleFunc("POST /api/v1/register/resend/{token}", registerResendHandler)
	smux.HandleFunc("DELETE /api/v1/register/{token}", registerCancelHandler)
	smux.HandleFunc("GET /api/v1/register/verify/email/{token}", verifyEmailRegHandler)
	smux.HandleFunc("POST /api/v1/register/passkey/begin", webauthnRegBegin)
	smux.HandleFunc("POST /api/v1/register/passkey/finish", webauthnRegFinish)
	smux.Handle("GET /api/v1/pending-registrations", auth(listPendingRegsHandler))
	smux.Handle("GET /api/v1/pending-registrations/count", auth(pendingRegCountHandler))
	smux.Handle("GET /api/v1/dashboard/attention", auth(dashboardAttentionHandler))
	smux.Handle("POST /api/v1/pending-registrations/{id}/approve", auth(approveRegHandler))
	smux.Handle("DELETE /api/v1/pending-registrations/{id}", auth(rejectRegHandler))

	// Protected event writes
	smux.Handle("POST /api/v1/events/preview", auth(previewEventsHandler))
	smux.Handle("POST /api/v1/events/bulk-set-location", auth(http.HandlerFunc(bulkSetEventLocation)))
	smux.Handle("POST /api/v1/events/bulk-set-time", auth(http.HandlerFunc(bulkSetEventTime)))
	smux.Handle("POST /api/v1/events/bulk-set-attributes", auth(http.HandlerFunc(bulkSetEventAttributes)))
	smux.Handle("POST /api/v1/events", auth(accountMutationLimit(createEvent)))
	smux.Handle("PUT /api/v1/events/{id}", auth(accountMutationLimit(updateEvent)))
	smux.Handle("PATCH /api/v1/events/{id}", auth(accountMutationLimit(patchEvent)))
	smux.HandleFunc("OPTIONS /api/v1/events", optionsSchema[EventWriteRequest])
	smux.HandleFunc("OPTIONS /api/v1/events/{id}", optionsSchema[EventWriteRequest])
	smux.Handle("POST /api/v1/events/{id}/publish", auth(publishEvent))
	smux.Handle("POST /api/v1/events/{id}/cancel", auth(cancelEvent))
	smux.Handle("POST /api/v1/events/{id}/clone", auth(cloneEvent))
	smux.Handle("POST /api/v1/events/{id}/assign-org", auth(assignEventOrg))
	smux.Handle("POST /api/v1/events/{id}/pending-edit/approve", auth(approvePendingEdit))
	smux.Handle("POST /api/v1/events/{id}/pending-edit/reject", auth(rejectPendingEdit))
	smux.Handle("POST /api/v1/events/{id}/remove-from-series", auth(http.HandlerFunc(removeEventFromSeries)))
	smux.Handle("DELETE /api/v1/events/{id}", auth(deleteEvent))
	smux.Handle("POST /api/v1/events/{id}/timetable", auth(addTimetableEntries))
	smux.Handle("POST /api/v1/events/{id}/enrich", auth(http.HandlerFunc(enrichEvent)))
	smux.Handle("PUT /api/v1/events/{id}/timetable", auth(replaceTimetable))
	smux.Handle("DELETE /api/v1/events/{id}/timetable", auth(deleteTimetable))
	smux.Handle("GET /api/v1/events/{id}/bookings", auth(listBookings))
	// Syndication (#971, #953)
	smux.Handle("GET /api/v1/events/{id}/syndication", auth(http.HandlerFunc(getEventSyncStatus)))
	smux.Handle("POST /api/v1/events/{id}/syndicate/eventbrite", auth(http.HandlerFunc(syndicateToEventbrite)))
	smux.Handle("POST /api/v1/events/{id}/syndicate/social-dance-today", auth(http.HandlerFunc(syndicateToSocialDanceToday)))

	// Event relationship sub-resources (#727)
	smux.Handle("PUT /api/v1/events/{id}/location", auth(http.HandlerFunc(setEventLocationRef)))
	smux.Handle("DELETE /api/v1/events/{id}/location", auth(http.HandlerFunc(unsetEventLocationRef)))
	smux.Handle("PUT /api/v1/events/{id}/locations/{location_id}", auth(addEventExtraLocation))
	smux.Handle("DELETE /api/v1/events/{id}/locations/{location_id}", auth(removeEventExtraLocation))
	smux.Handle("PUT /api/v1/events/{id}/locations/{location_id}/primary", auth(setEventExtraLocationPrimary))
	smux.HandleFunc("OPTIONS /api/v1/events/{id}/location", optionsSchema[EventLocationRefRequest])
	smux.Handle("PUT /api/v1/events/{id}/organization", auth(http.HandlerFunc(setEventOrganizationRef)))
	smux.Handle("DELETE /api/v1/events/{id}/organization", auth(http.HandlerFunc(unsetEventOrganizationRef)))
	smux.HandleFunc("OPTIONS /api/v1/events/{id}/organization", optionsSchema[EventOrganizationRefRequest])
	smux.Handle("PUT /api/v1/events/{id}/musicians/{musician_id}", auth(http.HandlerFunc(addEventMusician)))
	smux.Handle("DELETE /api/v1/events/{id}/musicians/{musician_id}", auth(http.HandlerFunc(removeEventMusician)))
	smux.Handle("PUT /api/v1/events/{id}/instructors/{instructor_id}", auth(http.HandlerFunc(addEventInstructor)))
	smux.Handle("DELETE /api/v1/events/{id}/instructors/{instructor_id}", auth(http.HandlerFunc(removeEventInstructor)))
	smux.Handle("PUT /api/v1/events/{id}/dances/{dance_id}", auth(http.HandlerFunc(addEventDance)))
	smux.Handle("DELETE /api/v1/events/{id}/dances/{dance_id}", auth(http.HandlerFunc(removeEventDance)))

	// Protected location writes
	smux.Handle("POST /api/v1/locations", auth(accountMutationLimit(createLocation)))
	smux.Handle("POST /api/v1/locations/merge", auth(mergeLocations))
	smux.Handle("POST /api/v1/locations/bulk-assign-org", auth(bulkAssignLocationOrg))
	smux.Handle("POST /api/v1/locations/unassign-org", auth(unassignLocationOrg))
	smux.Handle("PUT /api/v1/locations/{id}", auth(accountMutationLimit(putLocation)))
	smux.Handle("PATCH /api/v1/locations/{id}", auth(accountMutationLimit(patchLocation)))
	smux.Handle("POST /api/v1/locations/{id}/assign-org", auth(assignLocationOrg))
	smux.Handle("GET /api/v1/locations/{id}/children", optAuth(http.HandlerFunc(getLocationChildren)))
	smux.Handle("POST /api/v1/locations/{id}/children", auth(createLocationChild))
	smux.Handle("POST /api/v1/locations/{id}/site-plan", auth(uploadLocationSitePlan))
	smux.Handle("DELETE /api/v1/locations/{id}/site-plan", auth(deleteLocationSitePlan))
	smux.HandleFunc("GET /api/v1/location-images/{id}", getLocationImage)
	smux.Handle("DELETE /api/v1/locations/{id}", auth(deleteLocation))
	smux.HandleFunc("OPTIONS /api/v1/locations", optionsSchema[LocationCreateRequest])
	smux.HandleFunc("OPTIONS /api/v1/locations/{id}", optionsSchema[LocationCreateRequest])

	// Dance endpoints (protected writes)
	smux.Handle("POST /api/v1/dances", auth(createDance))
	smux.Handle("DELETE /api/v1/dances/{id}", auth(deleteDance))

	// Protected musician writes
	smux.Handle("POST /api/v1/musicians", auth(accountMutationLimit(createMusician)))
	smux.Handle("PUT /api/v1/musicians/{id}", auth(accountMutationLimit(updateMusician)))
	smux.Handle("PATCH /api/v1/musicians/{id}", auth(accountMutationLimit(patchMusician)))
	smux.Handle("DELETE /api/v1/musicians/{id}", auth(deleteMusician))
	smux.HandleFunc("OPTIONS /api/v1/musicians", optionsSchema[MusicianCreateRequest])
	smux.HandleFunc("OPTIONS /api/v1/musicians/{id}", optionsSchema[MusicianCreateRequest])

	// Instructor endpoints
	smux.Handle("POST /api/v1/instructors", auth(accountMutationLimit(createInstructor)))
	smux.Handle("PUT /api/v1/instructors/{id}", auth(accountMutationLimit(updateInstructor)))
	smux.Handle("PATCH /api/v1/instructors/{id}", auth(accountMutationLimit(patchInstructor)))
	smux.Handle("DELETE /api/v1/instructors/{id}", auth(deleteInstructor))
	smux.HandleFunc("OPTIONS /api/v1/instructors", optionsSchema[InstructorRequest])
	smux.HandleFunc("OPTIONS /api/v1/instructors/{id}", optionsSchema[InstructorRequest])
	smux.Handle("PUT /api/v1/events/{id}/instructors", auth(setEventInstructors))

	// Protected image writes
	smux.Handle("POST /api/v1/images/{event_id}", auth(uploadEventImage))
	smux.Handle("DELETE /api/v1/images/{event_id}", auth(deleteEventImage))
	smux.Handle("POST /api/v1/musician-images/{id}", auth(uploadMusicianImage))
	smux.Handle("DELETE /api/v1/musician-images/{id}", auth(deleteMusicianImage))
	smux.Handle("POST /api/v1/org-images/{id}", auth(uploadOrgImage))
	smux.Handle("DELETE /api/v1/org-images/{id}", auth(deleteOrgImage))

	// Avatar endpoints (JPEG, 400×400 max)
	smux.HandleFunc("GET /api/v1/org-avatars/{id}", avatarGetHandler(orgAvatars))
	smux.Handle("POST /api/v1/org-avatars/{id}", auth(avatarUploadHandler(orgAvatars, "organizations", "organization", isOrgMember)))
	smux.Handle("DELETE /api/v1/org-avatars/{id}", auth(avatarDeleteHandler(orgAvatars, "organization", isOrgMember)))
	smux.HandleFunc("GET /api/v1/musician-avatars/{id}", avatarGetHandler(musicianAvatars))
	smux.Handle("POST /api/v1/musician-avatars/{id}", auth(avatarUploadHandler(musicianAvatars, "musicians", "musician", func(_, _ int) bool { return false })))
	smux.Handle("DELETE /api/v1/musician-avatars/{id}", auth(avatarDeleteHandler(musicianAvatars, "musician", func(_, _ int) bool { return false })))
	smux.HandleFunc("GET /api/v1/instructor-avatars/{id}", avatarGetHandler(instructorAvatars))
	smux.Handle("POST /api/v1/instructor-avatars/{id}", auth(avatarUploadHandler(instructorAvatars, "instructors", "instructor", func(_, _ int) bool { return false })))
	smux.Handle("DELETE /api/v1/instructor-avatars/{id}", auth(avatarDeleteHandler(instructorAvatars, "instructor", func(_, _ int) bool { return false })))

	// User endpoints (protected). create-user, delete-user, and set-password
	// are intentionally CLI-only (dansal_admin) — not exposed via this API.
	smux.Handle("GET /api/v1/users", auth(getUsers))
	smux.Handle("GET /api/v1/me", auth(getMe))
	smux.Handle("GET /api/v1/me/stats", auth(getMeStats))
	smux.Handle("DELETE /api/v1/users/me", auth(deleteOwnAccount))
	smux.Handle("GET /api/v1/users/{id}", auth(getUser))
	smux.Handle("GET /api/v1/users/{id}/organizations", auth(getUserOrganizations))
	smux.Handle("PUT /api/v1/users/{id}", auth(updateUser))
	smux.Handle("GET /api/v1/pending-invites", auth(listPendingInvites))
	smux.Handle("POST /api/v1/pending-invites/{id}/resend", auth(resendInvite))
	smux.Handle("POST /api/v1/user/password", auth(changeOwnPassword))
	smux.Handle("POST /api/v1/users/{id}/verify", auth(sendVerification))
	smux.Handle("POST /api/v1/users/{id}/magic-link", auth(generateAdminMagicLink))
	smux.Handle("POST /api/v1/users/{id}/telegram/message", auth(sendTelegramMessageToUser))

	// Contact board — protected delete
	smux.Handle("DELETE /api/v1/contact-posts/{id}", auth(deleteContactPost))

	// Bookings — protected management
	smux.Handle("GET /api/v1/bookings/checkin/{qr_token}", auth(checkinBooking))
	smux.Handle("PATCH /api/v1/bookings/{id}/status", auth(updateBookingStatus))
	smux.Handle("DELETE /api/v1/bookings/{id}", auth(deleteBooking))

	// Event series endpoints
	smux.Handle("GET /api/v1/series", auth(getSeries))
	smux.Handle("POST /api/v1/series", auth(createSeries))
	smux.HandleFunc("GET /api/v1/series-by-token/{token}", getSeriesByToken)
	smux.HandleFunc("PATCH /api/v1/series-by-token/{token}/events/{eventID}", patchSeriesEventDescription)
	smux.Handle("GET /api/v1/series/{id}", auth(getSeriesByID))
	smux.Handle("PUT /api/v1/series/{id}", auth(updateSeries))
	smux.Handle("DELETE /api/v1/series/{id}", auth(deleteSeries))
	smux.Handle("POST /api/v1/series/{id}/add-date", auth(addSeriesDate))
	smux.Handle("POST /api/v1/series/{id}/descriptions", auth(http.HandlerFunc(updateSeriesDescriptions)))
	smux.Handle("POST /api/v1/series/{id}/assign-events", auth(http.HandlerFunc(assignSeriesEvents)))
	smux.Handle("POST /api/v1/series/{id}/token/regenerate", auth(regenerateSeriesToken))
	smux.Handle("POST /api/v1/series/{id}/token/revoke", auth(revokeSeriesToken))
	smux.Handle("GET /api/v1/series/{id}/events", auth(http.HandlerFunc(getSeriesEvents)))
	smux.Handle("PUT /api/v1/series/{id}/events/{event_id}", auth(http.HandlerFunc(addSeriesEvent)))
	smux.Handle("DELETE /api/v1/series/{id}/events/{event_id}", auth(http.HandlerFunc(removeSeriesEvent)))

	// Series image endpoints
	smux.HandleFunc("GET /api/v1/series-images/{id}", getSeriesImage)
	smux.Handle("POST /api/v1/series-images/{id}", auth(uploadSeriesImage))
	smux.Handle("DELETE /api/v1/series-images/{id}", auth(http.HandlerFunc(deleteSeriesImage)))

	// Organization writes (protected)
	smux.Handle("POST /api/v1/organizations", auth(accountMutationLimit(createOrganization)))
	smux.Handle("GET /api/v1/organizations/check-actor-name", auth(checkActorName))
	smux.Handle("PUT /api/v1/organizations/{id}", auth(accountMutationLimit(updateOrganization)))
	smux.Handle("PATCH /api/v1/organizations/{id}", auth(accountMutationLimit(patchOrganization)))
	smux.Handle("DELETE /api/v1/organizations/{id}", auth(deleteOrganization))
	smux.Handle("GET /api/v1/organizations/{id}/members", auth(getOrganizationMembers))
	smux.Handle("POST /api/v1/organizations/{id}/members", auth(addOrganizationMember))
	smux.Handle("DELETE /api/v1/organizations/{id}/members/{user_id}", auth(removeOrganizationMember))
	smux.Handle("GET /api/v1/organizations/{id}/syndication", auth(http.HandlerFunc(getSyndicationConfig)))
	smux.Handle("PUT /api/v1/organizations/{id}/syndication", auth(http.HandlerFunc(putSyndicationConfig)))

	// Fetch URL endpoints (protected)
	smux.Handle("GET /api/v1/fetchurl", auth(getFetchSources))
	smux.Handle("POST /api/v1/fetchurl", auth(fetchURL))
	smux.HandleFunc("OPTIONS /api/v1/fetchurl", optionsSchema[FetchURLRequest])
	smux.HandleFunc("OPTIONS /api/v1/fetchurl/{id}", optionsSchema[FetchSourcePatchRequest])
	smux.Handle("POST /api/v1/fetchurl/bulk-delete", auth(bulkDeleteFetchSources))
	smux.Handle("POST /api/v1/fetchurl/bulk-fetch", auth(bulkFetchURLsByIDs))
	smux.Handle("POST /api/v1/fetchurl/bulk-assign-org", auth(bulkAssignFetchSourceOrg))
	smux.Handle("GET /api/v1/fetchurl/{id}", auth(getFetchSource))
	smux.Handle("PATCH /api/v1/fetchurl/{id}", auth(patchFetchSource))
	smux.Handle("DELETE /api/v1/fetchurl/{id}", auth(deleteFetchSource))
	smux.Handle("POST /api/v1/fetchurl/{id}/fetch", auth(fetchURLByID))

	// API key endpoints (protected)
	smux.Handle("GET /api/v1/apikeys", auth(listAPIKeys))
	smux.Handle("POST /api/v1/apikeys", auth(createAPIKey))
	smux.Handle("DELETE /api/v1/apikeys/{id}", auth(deleteAPIKey))
	// Not wrapped in auth(): renewAPIKey looks up the api_keys row directly
	// (needs its id/expires_at/created_at, which resolveCaller doesn't expose)
	// rather than going through the generic session/API-key resolver.
	smux.Handle("POST /api/v1/apikeys/renew", http.HandlerFunc(renewAPIKey))
	// Not wrapped in auth(): revokeCurrentAPIKey authenticates by the presented
	// key itself (self-revoke), same reasoning as renewAPIKey above.
	smux.Handle("DELETE /api/v1/apikeys/current", http.HandlerFunc(revokeCurrentAPIKey))
	smux.Handle("POST /api/v1/publishers", auth(createPublisher))
	smux.Handle("POST /api/v1/publishers/token", auth(publisherToken))
	smux.Handle("POST /api/v1/publishers/{id}/regenerate-key", auth(regeneratePublisherKey))
	smux.Handle("DELETE /api/v1/publishers/{id}", auth(deletePublisher))

	// Invite management (protected)
	smux.Handle("GET /api/v1/invites", auth(listInvites))
	smux.Handle("POST /api/v1/invites", auth(createInvite))
	smux.Handle("DELETE /api/v1/invites/{token}", auth(revokeInvite))

	// Session endpoints (protected)
	smux.Handle("GET /api/v1/sessions", auth(getSessions))
	smux.Handle("DELETE /api/v1/sessions/{id}", auth(deleteSession))

	// Middleware chain: MetricsMiddleware is outermost to capture all status codes.
	handler := MetricsMiddleware(smux)(middlewareChain(icsRouter(smux),
		CORSMiddleware,
		SecurityHeadersMiddleware,
		GzipMiddleware,
		ErrorIDMiddleware,       // inside Gzip so it sees uncompressed JSON
		PanicRecoveryMiddleware, // inside ErrorID so a recovered panic still gets an error_id (#991)
		MaxBodyMiddleware,
		ConnLimitMiddleware,
		RateLimitMiddleware,
	))

	adminLn := startAdminSocket(config.Server.AdminSocket)
	startMetricsServer()
	go runHeartbeat()

	listenAddr := getListenAddr()
	log.Printf("dansal %s (built %s) starting on %s\n", Version, BuildTime, listenAddr)
	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: time.Duration(config.Server.ReadHeaderTimeoutSecs) * time.Second,
		ReadTimeout:       time.Duration(config.Server.ReadTimeoutSecs) * time.Second,
		WriteTimeout:      time.Duration(config.Server.WriteTimeoutSecs) * time.Second,
		IdleTimeout:       time.Duration(config.Server.IdleTimeoutSecs) * time.Second,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	for sig := range sigs {
		if sig == syscall.SIGHUP {
			reloadConfig(*configPath)
			continue
		}
		break
	}
	log.Println("Shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if adminLn != nil {
		adminLn.Close()
	}
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("Shutdown error: %v", err)
	}
	log.Println("Server stopped")
}
