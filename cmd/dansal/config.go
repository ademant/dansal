package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v2"
)

type ServerConfig struct {
	Port                          int      `yaml:"port"`
	Listen                        string   `yaml:"listen"`
	TokenExpirationHours          int      `yaml:"token_expiration_hours"`
	PublisherTokenExpirationHours int      `yaml:"publisher_token_expiration_hours"`
	RateLimit                     int      `yaml:"rate_limit"`
	MaxBodyBytes                  int64    `yaml:"max_body_bytes"`
	ReadHeaderTimeoutSecs         int      `yaml:"read_header_timeout_secs"`
	ReadTimeoutSecs               int      `yaml:"read_timeout_secs"`
	WriteTimeoutSecs              int      `yaml:"write_timeout_secs"`
	IdleTimeoutSecs               int      `yaml:"idle_timeout_secs"`
	MaxConnsPerIP                 int      `yaml:"max_conns_per_ip"`
	ImagesDir                     string   `yaml:"images_dir"`
	ImageXMax                     int      `yaml:"image_x_max"`
	ImageYMax                     int      `yaml:"image_y_max"`
	AdminSocket                   string   `yaml:"admin_socket"`
	DBPath                        string   `yaml:"db_path"`
	DBMaxConns                    int      `yaml:"db_max_conns"`
	LoginRateLimit                int      `yaml:"login_rate_limit"`
	LoginMaxFailures              int      `yaml:"login_max_failures"`
	LoginFailureWindowSecs        int      `yaml:"login_failure_window_secs"`
	InviteExpiryHours             int      `yaml:"invite_expiry_hours"`
	InviteQRExpiryMinutes         int      `yaml:"invite_qr_expiry_minutes"`
	InvitePublisherExpiryMinutes  int      `yaml:"invite_publisher_expiry_minutes"`
	VerificationExpiryHours       int      `yaml:"verification_expiry_hours"`
	BaseURL                       string   `yaml:"base_url"`
	TelegramBotToken              string   `yaml:"telegram_bot_token"`
	TelegramBotName               string   `yaml:"telegram_bot_name"`
	MatrixHomeserver              string   `yaml:"matrix_homeserver"`
	MatrixAccessToken             string   `yaml:"matrix_access_token"`
	MagicLoginExpirySecs          int      `yaml:"magic_login_expiry_secs"`
	MagicLoginRateSecs            int      `yaml:"magic_login_rate_secs"`
	MaxOpenTokensPerAddress       int      `yaml:"max_open_tokens_per_address"`
	HeartbeatIntervalMins         int      `yaml:"heartbeat_interval_mins"`
	SessionIdleTimeoutMins        int      `yaml:"session_idle_timeout_mins"` // 0 = disabled
	SessionMaxConcurrent          int      `yaml:"session_max_concurrent"`    // 0 = unlimited
	AllowedOrigins                []string `yaml:"allowed_origins"`
	MetricsPort                   int      `yaml:"metrics_port"`
	MetricsAllowedIPs             []string `yaml:"metrics_allowed_ips"`

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

	// InviteSigningKeyPath is where the ECDSA P-256 key pair used to sign
	// invite-link JWTs (see invite_jwt.go) is persisted. Generated on first
	// use if the file doesn't exist. Defaults next to db_path so it survives
	// upgrades but isn't accidentally checked into a repo.
	InviteSigningKeyPath string `yaml:"invite_signing_key_path"`

	// PasswordKDF selects the key-derivation function used to hash newly
	// set passwords: "argon2id" (default) or "pbkdf2" (FIPS 140-friendly,
	// see #802). Existing hashes (including legacy bcrypt/SHA-256) remain
	// verifiable regardless of this setting and are transparently re-hashed
	// with the configured KDF on next successful login.
	PasswordKDF string `yaml:"password_kdf"`
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
	if cfg.Server.ImageFormat == "" {
		cfg.Server.ImageFormat = "avif"
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
	if cfg.Server.LoginRateLimit == 0 {
		cfg.Server.LoginRateLimit = 5
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
