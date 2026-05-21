package main

import (
	"compress/gzip"
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"log/syslog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

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
	go db.Exec("UPDATE tokens SET last_seen_at=? WHERE token=?", now.Format(time.RFC3339), token)
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
		ip := getIP(r)
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
	ticker := time.NewTicker(rl.window)
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
		ip := getIP(r)
		if !rateLimiter.Allow(ip) {
			rateLimitRejectionsTotal.Inc()
			writeError(w, "Rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func getIP(r *http.Request) string {
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		return strings.TrimSpace(strings.Split(ip, ",")[0])
	}
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return strings.TrimSpace(ip)
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	return ip
}

// corsOrigin returns the allowed CORS origin for the request.
// With no AllowedOrigins configured, "*" is returned (open API mode).
func corsOrigin(r *http.Request) string {
	if len(config.Server.AllowedOrigins) == 0 {
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
// a separate OPTIONS handler.
func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := corsOrigin(r); o != "" {
			w.Header().Set("Access-Control-Allow-Origin", o)
			if o != "*" {
				w.Header().Add("Vary", "Origin")
			}
		}
		if r.Method == http.MethodOptions {
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
			res, err := db.Exec("DELETE FROM tokens WHERE expires_at < ?", time.Now().UTC().Format(time.RFC3339))
			if err != nil {
				log.Printf("token cleanup: %v", err)
			} else if n, _ := res.RowsAffected(); n > 0 {
				log.Printf("token cleanup: removed %d expired token(s)", n)
			}
			db.Exec("DELETE FROM verification_tokens WHERE expires_at < ?", time.Now().UTC().Format(time.RFC3339))
			db.Exec("DELETE FROM magic_login_tokens WHERE expires_at < ?", time.Now().UTC().Format(time.RFC3339))
			db.Exec("DELETE FROM contact_posts WHERE expires_at < ?", time.Now().UTC().Format(time.RFC3339))
			db.Exec("DELETE FROM bookings WHERE status='pending' AND expires_at < ?", time.Now().UTC().Format(time.RFC3339))
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

func migrateDB() {
	// Errors are silently ignored (column already exists).
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
	db.Exec("CREATE INDEX IF NOT EXISTS idx_magic_login_tokens_token ON magic_login_tokens(token)")
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
	db.Exec("CREATE INDEX IF NOT EXISTS idx_event_locations_event_id ON event_locations(event_id)")
	db.Exec("ALTER TABLE locations ADD COLUMN country TEXT")
	db.Exec(`CREATE TABLE IF NOT EXISTS event_musicians (
		event_id INTEGER NOT NULL,
		musician_id INTEGER NOT NULL,
		PRIMARY KEY (event_id, musician_id),
		FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE,
		FOREIGN KEY (musician_id) REFERENCES musicians(id) ON DELETE CASCADE
	)`)
	db.Exec("CREATE INDEX IF NOT EXISTS idx_event_musicians_event_id ON event_musicians(event_id)")
	// Remove tables and columns that are no longer part of the schema.
	db.Exec("DROP TABLE IF EXISTS posts")
	db.Exec("DROP TABLE IF EXISTS threads")
	db.Exec("DROP TABLE IF EXISTS bookings")
	db.Exec("ALTER TABLE events DROP COLUMN capacity") // no-op if already absent
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
	db.Exec("CREATE INDEX IF NOT EXISTS idx_verification_tokens_token ON verification_tokens(token)")
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
	db.Exec("CREATE INDEX IF NOT EXISTS idx_contact_posts_verify_token ON contact_posts(verify_token)")
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
	db.Exec("CREATE INDEX IF NOT EXISTS idx_bookings_verify_token ON bookings(verify_token)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_bookings_qr_token ON bookings(qr_token)")
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
	db.Exec("CREATE INDEX IF NOT EXISTS idx_event_dances_event_id ON event_dances(event_id)")
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
	db.Exec("CREATE INDEX IF NOT EXISTS idx_fetch_source_dances_source_id ON fetch_source_dances(fetch_source_id)")
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
		expires_at DATETIME NOT NULL,
		FOREIGN KEY (post_id) REFERENCES contact_posts(id) ON DELETE CASCADE
	)`)
	db.Exec("CREATE INDEX IF NOT EXISTS idx_contact_requests_verify_token ON contact_requests(verify_token)")
	migrateContactPostsCheckConstraint()
	db.Exec("ALTER TABLE timetable_entries ADD COLUMN musician_id INTEGER REFERENCES musicians(id) ON DELETE SET NULL")
}

func createTables() error {
	schema := `
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT UNIQUE NOT NULL,
		email TEXT UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		role TEXT DEFAULT 'user' CHECK(role IN ('admin', 'user', 'publisher', 'viewer')),
		telegram TEXT,
		matrix TEXT,
		email_verified INTEGER DEFAULT 0,
		telegram_verified INTEGER DEFAULT 0,
		matrix_verified INTEGER DEFAULT 0,
		disabled INTEGER DEFAULT 0,
		failed_login_count INTEGER DEFAULT 0,
		failed_login_since DATETIME,
		last_magic_sent_at DATETIME,
		description TEXT,
		mastodon TEXT,
		website TEXT,
		telegram_chat_id TEXT,
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
		tags TEXT,
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
		FOREIGN KEY (location_id) REFERENCES locations(id)
	);
	CREATE TABLE IF NOT EXISTS tokens (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		token TEXT UNIQUE NOT NULL,
		expires_at DATETIME NOT NULL,
		user_agent TEXT,
		ip TEXT,
		fingerprint TEXT,
		last_seen_at DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);
	CREATE TABLE IF NOT EXISTS locations (
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
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
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
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS dances (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT UNIQUE NOT NULL
	);
	CREATE TABLE IF NOT EXISTS fetch_sources (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		url TEXT UNIQUE NOT NULL,
		type TEXT NOT NULL DEFAULT 'ical',
		tags TEXT,
		organization_id INTEGER REFERENCES organizations(id) ON DELETE SET NULL,
		last_fetched_at DATETIME,
		last_result TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
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
	CREATE INDEX IF NOT EXISTS idx_fetch_source_dances_source_id ON fetch_source_dances(fetch_source_id);
	CREATE TABLE IF NOT EXISTS api_keys (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		name TEXT NOT NULL,
		api_key TEXT UNIQUE NOT NULL,
		expires_at DATETIME,
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
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
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
		role TEXT NOT NULL DEFAULT 'user',
		org_id INTEGER,
		expires_at DATETIME NOT NULL,
		used_at DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE CASCADE,
		FOREIGN KEY (org_id) REFERENCES organizations(id) ON DELETE SET NULL
	);
	CREATE INDEX IF NOT EXISTS idx_invite_links_token ON invite_links(token);
	CREATE TABLE IF NOT EXISTS verification_tokens (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		token TEXT UNIQUE NOT NULL,
		user_id INTEGER NOT NULL,
		channel TEXT NOT NULL CHECK(channel IN ('email','telegram','matrix')),
		expires_at DATETIME NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_verification_tokens_token ON verification_tokens(token);
	CREATE TABLE IF NOT EXISTS magic_login_tokens (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		token TEXT UNIQUE NOT NULL,
		user_id INTEGER NOT NULL,
		expires_at DATETIME NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_magic_login_tokens_token ON magic_login_tokens(token);
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
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE,
		FOREIGN KEY (location_id) REFERENCES locations(id),
		FOREIGN KEY (musician_id) REFERENCES musicians(id) ON DELETE SET NULL
	);
	CREATE INDEX IF NOT EXISTS idx_timetable_event_id ON timetable_entries(event_id);
	CREATE TABLE IF NOT EXISTS event_locations (
		event_id INTEGER NOT NULL,
		location_id INTEGER NOT NULL,
		PRIMARY KEY (event_id, location_id),
		FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE,
		FOREIGN KEY (location_id) REFERENCES locations(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_event_locations_event_id ON event_locations(event_id);
	CREATE TABLE IF NOT EXISTS event_musicians (
		event_id INTEGER NOT NULL,
		musician_id INTEGER NOT NULL,
		PRIMARY KEY (event_id, musician_id),
		FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE,
		FOREIGN KEY (musician_id) REFERENCES musicians(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_event_musicians_event_id ON event_musicians(event_id);
	CREATE INDEX IF NOT EXISTS idx_event_musicians_musician_id ON event_musicians(musician_id);
	CREATE TABLE IF NOT EXISTS event_dances (
		event_id INTEGER NOT NULL,
		dance_id INTEGER NOT NULL,
		PRIMARY KEY (event_id, dance_id),
		FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE,
		FOREIGN KEY (dance_id) REFERENCES dances(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_event_dances_event_id ON event_dances(event_id);
	CREATE TABLE IF NOT EXISTS contact_posts (
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
	);
	CREATE INDEX IF NOT EXISTS idx_contact_posts_event_id ON contact_posts(event_id);
	CREATE INDEX IF NOT EXISTS idx_contact_posts_verify_token ON contact_posts(verify_token);
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
	CREATE INDEX IF NOT EXISTS idx_contact_requests_verify_token ON contact_requests(verify_token);
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
		expires_at DATETIME NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_bookings_event_id ON bookings(event_id);
	CREATE INDEX IF NOT EXISTS idx_bookings_verify_token ON bookings(verify_token);
	CREATE INDEX IF NOT EXISTS idx_bookings_qr_token ON bookings(qr_token);
	CREATE INDEX IF NOT EXISTS idx_events_url             ON events(url) WHERE url IS NOT NULL;
	CREATE INDEX IF NOT EXISTS idx_events_published_start ON events(is_published, start_time);
	CREATE INDEX IF NOT EXISTS idx_events_title_location  ON events(title, location_id);
	CREATE INDEX IF NOT EXISTS idx_events_location_id     ON events(location_id);
	CREATE INDEX IF NOT EXISTS idx_events_organization_id ON events(organization_id) WHERE organization_id IS NOT NULL;
	CREATE INDEX IF NOT EXISTS idx_events_end_time ON events(end_time);
	CREATE INDEX IF NOT EXISTS idx_locations_location     ON locations(location);
	CREATE INDEX IF NOT EXISTS idx_locations_town         ON locations(town);
	CREATE INDEX IF NOT EXISTS idx_tokens_expires_at      ON tokens(expires_at);
	CREATE INDEX IF NOT EXISTS idx_org_members_user_id    ON organization_members(user_id);
	CREATE INDEX IF NOT EXISTS idx_location_organizations_org_id ON location_organizations(organization_id);
	`
	_, err := db.Exec(schema)
	return err
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

	if w, err := syslog.New(syslog.LOG_INFO|syslog.LOG_DAEMON, "dansal"); err == nil {
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

	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000&_foreign_keys=ON",
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
	initImageCache(config.Server.ImagesDir)
	initMusicianImageCache(config.Server.ImagesDir + "/musicians")
	initOrgImageCache(config.Server.ImagesDir + "/orgs")
	initMetrics()
	startTokenCleanup()
	log.Println("Database initialized successfully")

	rateLimiter = NewRateLimiter(config.Server.RateLimit, time.Minute)
	loginRateLimiter = NewRateLimiter(config.Server.LoginRateLimit, time.Minute)
	connLimiter = NewConnLimiter(config.Server.MaxConnsPerIP)

	smux := http.NewServeMux()

	// auth wraps a handler with TokenMiddleware (requires valid session token).
	auth := func(h http.HandlerFunc) http.Handler { return TokenMiddleware(http.HandlerFunc(h)) }
	// optAuth enriches the response when a token is present but does not require one.
	optAuth := OptionalTokenMiddleware

	// Info endpoint (public)
	smux.HandleFunc("GET /api/v1/info", getInfo)

	// Authentication endpoints (no token required)
	smux.HandleFunc("GET /api/v1/login", login)
	smux.HandleFunc("POST /api/v1/login", login)
	smux.HandleFunc("DELETE /api/v1/login", logout)
	smux.HandleFunc("POST /api/v1/login/magic", requestMagicLogin)
	smux.HandleFunc("GET /api/v1/login/magic/{token}", useMagicLogin)

	// Verification endpoints (public)
	smux.HandleFunc("GET /api/v1/verify/{token}", consumeVerification)
	smux.HandleFunc("POST /api/v1/invites/{token}", useInvite)

	// Telegram bot webhook (public, called by Telegram servers)
	smux.HandleFunc("POST /telegram/webhook", telegramWebhookHandler)

	// Contact board — public reads and post actions
	smux.HandleFunc("GET /api/v1/events/{id}/contact-posts", listContactPosts)
	smux.HandleFunc("POST /api/v1/events/{id}/contact-posts", createContactPost)
	smux.HandleFunc("GET /api/v1/contact-posts/verify/{token}", verifyContactPost)
	smux.HandleFunc("GET /api/v1/contact-posts/delete/{token}", deleteContactPostByToken)
	smux.HandleFunc("POST /api/v1/contact-posts/{id}/contact", contactPoster)
	smux.HandleFunc("GET /api/v1/contact-requests/verify/{token}", verifyContactRequest)

	// Bookings — public create + verify
	smux.HandleFunc("POST /api/v1/events/{id}/bookings", createBooking)
	smux.HandleFunc("GET /api/v1/bookings/verify/{token}", verifyBooking)

	// Public reads — OptionalTokenMiddleware enriches the response when a valid
	// token is present (e.g. editable flag, unpublished events).
	smux.Handle("GET /api/v1/events", optAuth(http.HandlerFunc(getEvents)))
	smux.Handle("GET /api/v1/events/{id}", optAuth(http.HandlerFunc(getEvent)))
	smux.Handle("GET /api/v1/locations", optAuth(http.HandlerFunc(getLocations)))
	smux.Handle("GET /api/v1/locations/{id}", optAuth(http.HandlerFunc(getLocation)))
	smux.Handle("GET /api/v1/organizations", optAuth(http.HandlerFunc(getOrganizations)))
	smux.Handle("GET /api/v1/organizations/stats", optAuth(http.HandlerFunc(getOrganizationStats)))
	smux.Handle("GET /api/v1/organizations/{id}", optAuth(http.HandlerFunc(getOrganization)))
	smux.Handle("GET /api/v1/musicians", optAuth(http.HandlerFunc(getMusicians)))
	smux.Handle("GET /api/v1/musicians/{id}", optAuth(http.HandlerFunc(getMusician)))
	smux.Handle("GET /api/v1/tags", optAuth(http.HandlerFunc(getTags)))
	smux.Handle("GET /api/v1/dances", optAuth(http.HandlerFunc(getDances)))
	smux.Handle("GET /api/v1/images/{event_id}", optAuth(http.HandlerFunc(getEventImage)))
	smux.HandleFunc("GET /api/v1/musician-images/{id}", getMusicianImage)
	smux.HandleFunc("GET /api/v1/org-images/{id}", getOrgImage)

	// Protected event writes
	smux.Handle("POST /api/v1/events", auth(createEvent))
	smux.Handle("PUT /api/v1/events/{id}", auth(updateEvent))
	smux.Handle("DELETE /api/v1/events/{id}", auth(deleteEvent))
	smux.Handle("POST /api/v1/events/{id}/timetable", auth(addTimetableEntries))
	smux.Handle("PUT /api/v1/events/{id}/timetable", auth(replaceTimetable))
	smux.Handle("GET /api/v1/events/{id}/bookings", auth(listBookings))

	// Protected location writes
	smux.Handle("POST /api/v1/locations", auth(createLocation))
	smux.Handle("POST /api/v1/locations/bulk-assign-org", auth(bulkAssignLocationOrg))
	smux.Handle("POST /api/v1/locations/unassign-org", auth(unassignLocationOrg))
	smux.Handle("PATCH /api/v1/locations/{id}", auth(patchLocation))
	smux.Handle("DELETE /api/v1/locations/{id}", auth(deleteLocation))

	// Dance endpoints (protected writes)
	smux.Handle("POST /api/v1/dances", auth(createDance))
	smux.Handle("DELETE /api/v1/dances/{id}", auth(deleteDance))

	// Protected musician writes
	smux.Handle("POST /api/v1/musicians", auth(createMusician))
	smux.Handle("PUT /api/v1/musicians/{id}", auth(updateMusician))
	smux.Handle("DELETE /api/v1/musicians/{id}", auth(deleteMusician))

	// Protected image writes
	smux.Handle("POST /api/v1/images/{event_id}", auth(uploadEventImage))
	smux.Handle("DELETE /api/v1/images/{event_id}", auth(deleteEventImage))
	smux.Handle("POST /api/v1/musician-images/{id}", auth(uploadMusicianImage))
	smux.Handle("DELETE /api/v1/musician-images/{id}", auth(deleteMusicianImage))
	smux.Handle("POST /api/v1/org-images/{id}", auth(uploadOrgImage))
	smux.Handle("DELETE /api/v1/org-images/{id}", auth(deleteOrgImage))

	// User endpoints (protected)
	smux.Handle("GET /api/v1/users", auth(getUsers))
	smux.Handle("POST /api/v1/users", auth(createUser))
	smux.Handle("GET /api/v1/users/{id}", auth(getUser))
	smux.Handle("PUT /api/v1/users/{id}", auth(updateUser))
	smux.Handle("DELETE /api/v1/users/{id}", auth(deleteUser))
	smux.Handle("POST /api/v1/users/{id}/verify", auth(sendVerification))
	smux.Handle("POST /api/v1/users/{id}/telegram/message", auth(sendTelegramMessageToUser))

	// Contact board — protected delete
	smux.Handle("DELETE /api/v1/contact-posts/{id}", auth(deleteContactPost))

	// Bookings — protected management
	smux.Handle("GET /api/v1/bookings/checkin/{qr_token}", auth(checkinBooking))
	smux.Handle("PATCH /api/v1/bookings/{id}/status", auth(updateBookingStatus))
	smux.Handle("DELETE /api/v1/bookings/{id}", auth(deleteBooking))

	// Organization writes (protected)
	smux.Handle("POST /api/v1/organizations", auth(createOrganization))
	smux.Handle("GET /api/v1/organizations/check-actor-name", auth(checkActorName))
	smux.Handle("PUT /api/v1/organizations/{id}", auth(updateOrganization))
	smux.Handle("DELETE /api/v1/organizations/{id}", auth(deleteOrganization))
	smux.Handle("GET /api/v1/organizations/{id}/members", auth(getOrganizationMembers))
	smux.Handle("POST /api/v1/organizations/{id}/members", auth(addOrganizationMember))
	smux.Handle("DELETE /api/v1/organizations/{id}/members/{user_id}", auth(removeOrganizationMember))

	// Fetch URL endpoints (protected)
	smux.Handle("GET /api/v1/fetchurl", auth(getFetchSources))
	smux.Handle("POST /api/v1/fetchurl", auth(fetchURL))
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

	// Invite management (protected)
	smux.Handle("GET /api/v1/invites", auth(listInvites))
	smux.Handle("POST /api/v1/invites", auth(createInvite))
	smux.Handle("DELETE /api/v1/invites/{token}", auth(revokeInvite))

	// Session endpoints (protected)
	smux.Handle("GET /api/v1/sessions", auth(getSessions))
	smux.Handle("DELETE /api/v1/sessions/{id}", auth(deleteSession))

	// Admin site config (protected, admin-only)
	smux.Handle("GET /api/v1/admin/config", auth(getAdminConfig))
	smux.Handle("PATCH /api/v1/admin/config", auth(patchAdminConfig))

	// Middleware chain: MetricsMiddleware is outermost to capture all status codes.
	handler := MetricsMiddleware(smux)(middlewareChain(icsRouter(smux),
		CORSMiddleware,
		SecurityHeadersMiddleware,
		GzipMiddleware,
		MaxBodyMiddleware,
		ConnLimitMiddleware,
		RateLimitMiddleware,
	))

	adminLn := startAdminSocket(config.Server.AdminSocket)
	startMetricsServer()

	port := getPort()
	log.Printf("Server starting on %s\n", port)
	srv := &http.Server{
		Addr:         port,
		Handler:      handler,
		ReadTimeout:  time.Duration(config.Server.ReadTimeoutSecs) * time.Second,
		WriteTimeout: time.Duration(config.Server.WriteTimeoutSecs) * time.Second,
		IdleTimeout:  time.Duration(config.Server.IdleTimeoutSecs) * time.Second,
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
