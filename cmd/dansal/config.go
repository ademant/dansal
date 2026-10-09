package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"gopkg.in/yaml.v2"
)

// RequestSigningConfig configures opt-in HMAC request signing for
// authenticated publisher writes (#1366). Enabled is a process-wide gate;
// even when true, a given API key only actually gets enforced if its own
// api_keys.require_signature flag is also set.
type RequestSigningConfig struct {
	Enabled     bool `yaml:"enabled"`          // default false
	MaxSkewSecs int  `yaml:"max_skew_seconds"` // default 300 when unset/zero
}

// WebhooksConfig configures the publisher webhook producer (#1370). With
// Enabled false, subscription endpoints still work (rows can be created and
// pre-configured); the producer just never fires.
type WebhooksConfig struct {
	Enabled              bool  `yaml:"enabled"`                // default false
	MaxURLLength         int   `yaml:"max_url_length"`         // default 2048
	TimeoutSecs          int   `yaml:"timeout_seconds"`        // per-delivery connect+read cap, default 10
	RetryBackoffSecs     []int `yaml:"retry_backoff_seconds"`  // default [30, 300, 1800]
	DisableAfterFailures int   `yaml:"disable_after_failures"` // default 4 (initial + 3 retries)
}

type ServerConfig struct {
	Port                          int    `yaml:"port"`
	Listen                        string `yaml:"listen"`
	TokenExpirationHours          int    `yaml:"token_expiration_hours"`
	PublisherTokenExpirationHours int    `yaml:"publisher_token_expiration_hours"`
	RateLimit                     int    `yaml:"rate_limit"`
	AccountMutationRateLimit      int    `yaml:"account_mutation_rate_limit"` // per-account create/update cap, requests/min; 0 = default 30 (see createUpdateLimiter)
	MaxBodyBytes                  int64  `yaml:"max_body_bytes"`
	ReadHeaderTimeoutSecs         int    `yaml:"read_header_timeout_secs"`
	ReadTimeoutSecs               int    `yaml:"read_timeout_secs"`
	WriteTimeoutSecs              int    `yaml:"write_timeout_secs"`
	IdleTimeoutSecs               int    `yaml:"idle_timeout_secs"`
	MaxConnsPerIP                 int    `yaml:"max_conns_per_ip"`
	ImagesDir                     string `yaml:"images_dir"`
	ImageXMax                     int    `yaml:"image_x_max"`
	ImageYMax                     int    `yaml:"image_y_max"`
	GalleryMaxImages              int    `yaml:"gallery_max_images"` // uploaded gallery pictures per musician (#1362); 0 = default 8
	AdminSocket                   string `yaml:"admin_socket"`
	DBPath                        string `yaml:"db_path"`
	DBMaxConns                    int    `yaml:"db_max_conns"`
	LoginRateLimit                int    `yaml:"login_rate_limit"`
	LoginMaxFailures              int    `yaml:"login_max_failures"`
	LoginFailureWindowSecs        int    `yaml:"login_failure_window_secs"`
	InviteExpiryHours             int    `yaml:"invite_expiry_hours"`
	InviteQRExpiryMinutes         int    `yaml:"invite_qr_expiry_minutes"`
	InvitePublisherExpiryMinutes  int    `yaml:"invite_publisher_expiry_minutes"`
	VerificationExpiryHours       int    `yaml:"verification_expiry_hours"`
	// CheckinOpensBeforeMinutes / CheckinClosesAfterMinutes bound how long a
	// booking's QR code may be scanned at the door, relative to the event's own
	// start and end times (#1382). The QR is a physical-world credential printed
	// on a ticket and shown in public, so this is deliberately much shorter than
	// the 90 days a booking row is retained for. Set either to 0 to fall back to
	// the default, or to a negative value to disable that side of the window
	// (e.g. an event with no end time, where the closing bound would otherwise
	// be guesswork).
	CheckinOpensBeforeMinutes int      `yaml:"checkin_opens_before_minutes"`
	CheckinClosesAfterMinutes int      `yaml:"checkin_closes_after_minutes"`
	APIKeyRenewGraceHours     int      `yaml:"api_key_renew_grace_hours"` // #1189: grace window past expires_at during which POST /apikeys/renew still succeeds
	BaseURL                   string   `yaml:"base_url"`
	TelegramBotToken          string   `yaml:"telegram_bot_token"`
	TelegramBotName           string   `yaml:"telegram_bot_name"`
	MatrixHomeserver          string   `yaml:"matrix_homeserver"`
	MatrixAccessToken         string   `yaml:"matrix_access_token"`
	MagicLoginExpirySecs      int      `yaml:"magic_login_expiry_secs"`
	MagicLoginRateSecs        int      `yaml:"magic_login_rate_secs"`
	MaxOpenTokensPerAddress   int      `yaml:"max_open_tokens_per_address"`
	HeartbeatIntervalMins     int      `yaml:"heartbeat_interval_mins"`
	SessionIdleTimeoutMins    int      `yaml:"session_idle_timeout_mins"` // 0 = disabled
	SessionMaxConcurrent      int      `yaml:"session_max_concurrent"`    // 0 = unlimited
	AllowedOrigins            []string `yaml:"allowed_origins"`
	MetricsPort               int      `yaml:"metrics_port"`
	MetricsAllowedIPs         []string `yaml:"metrics_allowed_ips"`

	// InternalSharedSecret, when set, exempts loopback requests that send a
	// matching X-Dansal-Internal header from RateLimitMiddleware and
	// ConnLimitMiddleware (see isInternalCaller in main.go). Used by
	// dansal-web's backend calls, which otherwise share dansal-web's whole
	// visitor traffic under a single loopback rate-limit bucket.
	InternalSharedSecret     string `yaml:"internal_shared_secret"`
	WebAuthnRPName           string `yaml:"webauthn_rp_name"`           // display name, default "Dansal"
	WebAuthnUserVerification string `yaml:"webauthn_user_verification"` // "preferred" (default) | "required" | "discouraged"
	ImageFormat              string `yaml:"image_format"`               // "avif" | "jpeg", default "avif"
	BoardOpenPosting         bool   `yaml:"board_open_posting"`         // true = posts visible immediately; false (default) = verify contact first
	BackupDir                string `yaml:"backup_dir"`
	BackupIntervalHours      int    `yaml:"backup_interval_hours"` // 0 = disabled
	// BackupKeep (#1407) is how many of the most recent archives to keep per
	// kind (dansal-backup-*, dansal-incremental-*, dansal-config-backup-*)
	// after each successful backup; older ones in that kind are pruned.
	// 0/unset defaults to 14 (#1493, compliance G13); set -1 to keep every
	// archive forever. Prior to #1493, 0 itself meant unlimited -- an
	// existing config.yaml with an explicit `backup_keep: 0` must change it
	// to -1 to preserve that behavior after upgrading.
	BackupKeep int `yaml:"backup_keep"`
	// BackupEncryptionKeyFile (#1492, compliance G13) points at a file whose
	// raw contents are used as the encryption key for nightly/scheduled
	// backups — a static key file, not a human-typed password, so
	// createBackup can encrypt unattended. Must not live inside BackupDir:
	// an attacker (or restore operator) with the backup archive should not
	// also automatically have the key next to it. Empty (default): backups
	// are written unencrypted, as before.
	BackupEncryptionKeyFile string `yaml:"backup_encryption_key_file"`

	// DataRetentionDays (#1440, compliance gap G3) bounds how long personal
	// data sits in stores the hourly sweep didn't previously touch: confirmed/
	// approved/checked-in/cancelled bookings (name, email, message), expired
	// unclaimed contact_requests replies (sender_email, sender_telegram),
	// pending_fetch_suggestions (email), and timetable_history (changed_by).
	// Also used as the window for the long-lived booking expiry set at
	// verify time (see bookingLongExpiry) — the age at which a confirmed
	// booking becomes eligible for the sweep is the same number. Default 90.
	DataRetentionDays int `yaml:"data_retention_days"`

	// InviteSigningKeyPath is where the ECDSA P-256 key pair used to sign
	// invite-link JWTs (see invite_jwt.go) is persisted. Generated on first
	// use if the file doesn't exist. Defaults next to db_path so it survives
	// upgrades but isn't accidentally checked into a repo.
	InviteSigningKeyPath string `yaml:"invite_signing_key_path"`

	// Signing configures opt-in HMAC request signing for publisher writes
	// (#1366). Not to be confused with InviteSigningKeyPath above (invite
	// link JWTs) — unrelated feature, unrelated key.
	Signing RequestSigningConfig `yaml:"signing,omitempty"`

	// Webhooks configures publisher webhook delivery (#1370).
	Webhooks WebhooksConfig `yaml:"webhooks,omitempty"`

	// PasswordKDF selects the key-derivation function used to hash newly
	// set passwords: "argon2id" (default) or "pbkdf2" (FIPS 140-friendly,
	// see #802). Existing hashes (including legacy bcrypt/SHA-256) remain
	// verifiable regardless of this setting and are transparently re-hashed
	// with the configured KDF on next successful login.
	PasswordKDF string `yaml:"password_kdf"`

	// Debug gates verbose logging, e.g. dumping the full event request for a
	// fetch-source entry that failed to import (see #923). Default false.
	Debug bool `yaml:"debug"`

	// TagsFile optionally overrides the embedded default tags.yaml (#1173) —
	// the event-format tag vocabulary seeded into the tags table at startup.
	// Empty (the default) uses the built-in balfolk vocabulary; set this to
	// let an instance serving a different dance/event community ship its own
	// vocabulary without a code fork. Mirrors dansal_web's i18n_file override.
	TagsFile string `yaml:"tags_file"`

	// Timezone (#1394) is the IANA zone name (e.g. "America/New_York") every
	// timezone-less event input is parsed in and every stored epoch is
	// rendered in — the authoritative instance-wide event-display zone.
	// events.start_time/end_time are stored as timezone-neutral Unix epochs,
	// so this setting is what turns one back into a wall-clock time, not a
	// per-row column. Defaults to "Europe/Berlin" for backwards
	// compatibility with every instance that predates this setting.
	// Explicit RFC3339 offsets and UTC timestamps in event input are
	// unaffected — they carry their own absolute instant regardless of this
	// setting; only naive/floating input is anchored here. See instanceLoc
	// in ical_time.go, which every parsing/rendering call site already goes
	// through. Validated with time.LoadLocation at startup — an invalid
	// value fails startup outright rather than silently falling back, since
	// serving requests with a silently-wrong zone would misdisplay every
	// event without any indication something is off.
	Timezone string `yaml:"timezone"`
}

type SMTPConfig struct {
	Host        string `yaml:"host,omitempty"`
	Port        int    `yaml:"port,omitempty"`
	Username    string `yaml:"username,omitempty"`
	Password    string `yaml:"password,omitempty"`
	PasswordKey string `yaml:"password_key,omitempty"`
	From        string `yaml:"from,omitempty"`
	FromName    string `yaml:"from_name,omitempty"`
	TLS         string `yaml:"tls,omitempty"`          // starttls | tls | none
	TimeoutSecs int    `yaml:"timeout_secs,omitempty"` // dial+send timeout; default 30
	Sendmail    string `yaml:"sendmail,omitempty"`     // path to sendmail binary; if set, used instead of SMTP
}

type Config struct {
	Server ServerConfig `yaml:"server"`
	SMTP   SMTPConfig   `yaml:"smtp,omitempty"`
}

var config *Config

func loadConfig(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	applyEnvOverrides(cfg)
	return cfg, nil
}

// applyEnvOverrides lets Docker / container deployments inject secrets and
// per-environment values without modifying the YAML config file.
func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("DANSAL_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Server.Port = p
		}
	}
	if v := os.Getenv("DANSAL_BASE_URL"); v != "" {
		cfg.Server.BaseURL = v
	}
	if v := os.Getenv("DANSAL_DB_PATH"); v != "" {
		cfg.Server.DBPath = v
	}
	if v := os.Getenv("DANSAL_SMTP_HOST"); v != "" {
		cfg.SMTP.Host = v
	}
	if v := os.Getenv("DANSAL_SMTP_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.SMTP.Port = p
		}
	}
	if v := os.Getenv("DANSAL_SMTP_USER"); v != "" {
		cfg.SMTP.Username = v
	}
	if v := os.Getenv("DANSAL_SMTP_PASS"); v != "" {
		cfg.SMTP.Password = v
	}
	if v := os.Getenv("DANSAL_SMTP_FROM"); v != "" {
		cfg.SMTP.From = v
	}
	if v := os.Getenv("DANSAL_BACKUP_DIR"); v != "" {
		cfg.Server.BackupDir = v
	}
	if v := os.Getenv("DANSAL_BACKUP_KEEP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Server.BackupKeep = n
		}
	}
}

// validateInstanceTimezone parses server.timezone (#1394) as an IANA zone
// name, wrapping the error with the setting name so it's actionable wherever
// it surfaces — a bare time.LoadLocation error ("unknown time zone Foo")
// doesn't say which config key is wrong. Shared by main()'s startup check
// (fatal) and reloadConfig's SIGHUP check (logged, keeps the previous zone).
func validateInstanceTimezone(name string) (*time.Location, error) {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("server.timezone %q is not a valid IANA time zone name: %w", name, err)
	}
	return loc, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8000
	}
	if cfg.Server.Listen == "" {
		cfg.Server.Listen = "127.0.0.1:" + strconv.Itoa(cfg.Server.Port)
	}
	if cfg.Server.TokenExpirationHours == 0 {
		cfg.Server.TokenExpirationHours = 24
	}
	if cfg.Server.PublisherTokenExpirationHours == 0 {
		cfg.Server.PublisherTokenExpirationHours = 1
	}
	if cfg.Server.MaxBodyBytes == 0 {
		cfg.Server.MaxBodyBytes = 1 << 20
	}
	if cfg.Server.ReadHeaderTimeoutSecs == 0 {
		cfg.Server.ReadHeaderTimeoutSecs = 5
	}
	if cfg.Server.ReadTimeoutSecs == 0 {
		cfg.Server.ReadTimeoutSecs = 10
	}
	if cfg.Server.WriteTimeoutSecs == 0 {
		cfg.Server.WriteTimeoutSecs = 30
	}
	if cfg.Server.IdleTimeoutSecs == 0 {
		cfg.Server.IdleTimeoutSecs = 60
	}
	if cfg.Server.MaxConnsPerIP == 0 {
		cfg.Server.MaxConnsPerIP = 10
	}
	if cfg.Server.ImagesDir == "" {
		cfg.Server.ImagesDir = "./images"
	}
	if cfg.Server.ImageXMax == 0 {
		cfg.Server.ImageXMax = 1024
	}
	if cfg.Server.ImageYMax == 0 {
		cfg.Server.ImageYMax = 1024
	}
	if cfg.Server.GalleryMaxImages <= 0 {
		cfg.Server.GalleryMaxImages = 8
	}
	if cfg.Server.ImageFormat == "" {
		cfg.Server.ImageFormat = "avif"
	}
	if cfg.Server.Timezone == "" {
		cfg.Server.Timezone = "Europe/Berlin"
	}
	if cfg.Server.AdminSocket == "" {
		cfg.Server.AdminSocket = "./dansal.sock"
	}
	if cfg.Server.DBPath == "" {
		cfg.Server.DBPath = "/var/lib/dansal/calendar.db"
	}
	if cfg.Server.DBMaxConns == 0 {
		cfg.Server.DBMaxConns = 10
	}
	if cfg.Server.BackupDir == "" {
		cfg.Server.BackupDir = filepath.Join(filepath.Dir(cfg.Server.DBPath), "backups")
	}
	if cfg.Server.InviteSigningKeyPath == "" {
		cfg.Server.InviteSigningKeyPath = filepath.Join(filepath.Dir(cfg.Server.DBPath), "invite_signing_key.pem")
	}
	if cfg.Server.RateLimit == 0 {
		// An unset rate_limit must not silently reject every request
		// (NewRateLimiter(0, ...) allows nothing — a total self-DoS, #988).
		// nginx already enforces 10r/s per IP in front; this is
		// defense-in-depth, so 30/min is a safe, generous default.
		cfg.Server.RateLimit = 30
	}
	if cfg.Server.LoginRateLimit == 0 {
		cfg.Server.LoginRateLimit = 5
	}
	if cfg.Server.AccountMutationRateLimit == 0 {
		// Same self-DoS guard as RateLimit above: an unset value must default
		// to something usable, not newAccountLimiter(0, ...)'s effective
		// one-request-per-window lockout.
		cfg.Server.AccountMutationRateLimit = 30
	}
	if cfg.Server.LoginMaxFailures == 0 {
		cfg.Server.LoginMaxFailures = 10
	}
	if cfg.Server.LoginFailureWindowSecs == 0 {
		cfg.Server.LoginFailureWindowSecs = 600
	}
	if cfg.Server.InviteExpiryHours == 0 {
		cfg.Server.InviteExpiryHours = 48
	}
	if cfg.Server.InviteQRExpiryMinutes == 0 {
		cfg.Server.InviteQRExpiryMinutes = 15
	}
	if cfg.Server.InvitePublisherExpiryMinutes == 0 {
		cfg.Server.InvitePublisherExpiryMinutes = 30
	}
	if cfg.Server.VerificationExpiryHours == 0 {
		cfg.Server.VerificationExpiryHours = 24
	}
	if cfg.Server.CheckinOpensBeforeMinutes == 0 {
		cfg.Server.CheckinOpensBeforeMinutes = 120
	}
	if cfg.Server.CheckinClosesAfterMinutes == 0 {
		cfg.Server.CheckinClosesAfterMinutes = 240
	}
	if cfg.Server.APIKeyRenewGraceHours == 0 {
		cfg.Server.APIKeyRenewGraceHours = 6
	}
	if cfg.Server.MagicLoginExpirySecs == 0 {
		cfg.Server.MagicLoginExpirySecs = 900
	}
	if cfg.Server.MagicLoginRateSecs == 0 {
		cfg.Server.MagicLoginRateSecs = 10
	}
	if cfg.Server.MaxOpenTokensPerAddress == 0 {
		cfg.Server.MaxOpenTokensPerAddress = 5
	}
	if cfg.Server.HeartbeatIntervalMins == 0 {
		cfg.Server.HeartbeatIntervalMins = 5
	}
	if cfg.Server.PasswordKDF == "" {
		cfg.Server.PasswordKDF = "argon2id"
	}
	if cfg.Server.DataRetentionDays == 0 {
		cfg.Server.DataRetentionDays = 90
	}
	// #1493 (compliance G13): unset/0 now means "use the default" (14)
	// rather than "keep forever" -- an instance that wants unlimited
	// retention must say so explicitly with -1. Anyone upgrading with an
	// existing config.yaml that wrote `backup_keep: 0` to mean unlimited
	// will silently start pruning to 14 after this change; they need to
	// change that value to -1 to keep prior behavior.
	if cfg.Server.BackupKeep == 0 {
		cfg.Server.BackupKeep = 14
	}
}

// saveConfig writes the current config back to disk atomically.
func saveConfig(path string) error {
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func getPort() string {
	if config == nil || config.Server.Port == 0 {
		return ":8000"
	}
	return ":" + strconv.Itoa(config.Server.Port)
}

// getListenAddr returns the address the API server should bind to,
// e.g. "127.0.0.1:8000". Falls back to loopback if unset.
func getListenAddr() string {
	if config == nil || config.Server.Listen == "" {
		return "127.0.0.1" + getPort()
	}
	return config.Server.Listen
}
