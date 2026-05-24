package main

import (
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v2"
)

type ServerConfig struct {
	Port                 int      `yaml:"port"`
	TokenExpirationHours int      `yaml:"token_expiration_hours"`
	AdminAllowedIPs      []string `yaml:"admin_allowed_ips"`
	MaxBodyBytes         int64    `yaml:"max_body_bytes"`
	ReadTimeoutSecs      int      `yaml:"read_timeout_secs"`
	WriteTimeoutSecs     int      `yaml:"write_timeout_secs"`
	IdleTimeoutSecs      int      `yaml:"idle_timeout_secs"`
	MaxConnsPerIP        int      `yaml:"max_conns_per_ip"`
	ImagesDir            string   `yaml:"images_dir"`
	ImageXMax            int      `yaml:"image_x_max"`
	ImageYMax            int      `yaml:"image_y_max"`
	AdminSocket          string   `yaml:"admin_socket"`
	DBPath               string   `yaml:"db_path"`
	DBMaxConns           int      `yaml:"db_max_conns"`
	LoginRateLimit          int      `yaml:"login_rate_limit"`
	LoginTarpitSecs         int      `yaml:"login_tarpit_secs"`
	LoginMaxFailures        int      `yaml:"login_max_failures"`
	LoginFailureWindowSecs  int      `yaml:"login_failure_window_secs"`
	InviteExpiryHours          int    `yaml:"invite_expiry_hours"`
	VerificationExpiryHours    int    `yaml:"verification_expiry_hours"`
	BaseURL                    string `yaml:"base_url"`
	TelegramBotToken           string `yaml:"telegram_bot_token"`
	TelegramBotName            string `yaml:"telegram_bot_name"`
	MatrixHomeserver           string `yaml:"matrix_homeserver"`
	MatrixAccessToken          string `yaml:"matrix_access_token"`
	MagicLoginExpirySecs    int      `yaml:"magic_login_expiry_secs"`
	MagicLoginRateSecs      int      `yaml:"magic_login_rate_secs"`
	HeartbeatIntervalMins   int      `yaml:"heartbeat_interval_mins"`
	ReservedUsernames    []string `yaml:"reserved_usernames"`
	AllowedOrigins       []string `yaml:"allowed_origins"`
	MetricsPort          int      `yaml:"metrics_port"`
	MetricsAllowedIPs    []string `yaml:"metrics_allowed_ips"`
	
	// Passwordless Authentication Enhancement (#258)
	SessionPersistenceDays int      `yaml:"session_persistence_days"`
	RefreshTokenExpirationDays int `yaml:"refresh_token_expiration_days"`
	MaxDevicesPerUser int           `yaml:"max_devices_per_user"`
	GuestModeEnabled bool           `yaml:"guest_mode_enabled"`
	MobileDeepLinkBaseURL string    `yaml:"mobile_deep_link_base_url"`
	
	// Performance Optimization (#260)
	CacheEnabled      bool   `yaml:"cache_enabled"`
	CacheTTLMinutes   int    `yaml:"cache_ttl_minutes"`
	RedisURL          string `yaml:"redis_url"`
	GzipEnabled       bool   `yaml:"gzip_enabled"`
	RateLimit         int    `yaml:"rate_limit"`
	RateLimitWindow   int    `yaml:"rate_limit_window"`
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
	return cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8000
	}
	if cfg.Server.TokenExpirationHours == 0 {
		cfg.Server.TokenExpirationHours = 24
	}
	if cfg.Server.MaxBodyBytes == 0 {
		cfg.Server.MaxBodyBytes = 1 << 20
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
	if cfg.Server.AdminSocket == "" {
		cfg.Server.AdminSocket = "./dansal.sock"
	}
	if cfg.Server.DBPath == "" {
		cfg.Server.DBPath = "/var/lib/dansal/calendar.db"
	}
	if cfg.Server.DBMaxConns == 0 {
		cfg.Server.DBMaxConns = 10
	}
	if cfg.Server.LoginRateLimit == 0 {
		cfg.Server.LoginRateLimit = 5
	}
	if cfg.Server.LoginTarpitSecs == 0 {
		cfg.Server.LoginTarpitSecs = 10
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
	if cfg.Server.VerificationExpiryHours == 0 {
		cfg.Server.VerificationExpiryHours = 24
	}
	if cfg.Server.MagicLoginExpirySecs == 0 {
		cfg.Server.MagicLoginExpirySecs = 900
	}
	if cfg.Server.MagicLoginRateSecs == 0 {
		cfg.Server.MagicLoginRateSecs = 10
	}
	if cfg.Server.HeartbeatIntervalMins == 0 {
		cfg.Server.HeartbeatIntervalMins = 5
	}
	if len(cfg.Server.ReservedUsernames) == 0 {
		cfg.Server.ReservedUsernames = []string{
			"admin", "administrator", "root", "superuser", "sysadmin", "system", "su",
		}
	}
	
	// Passwordless Authentication Enhancement (#258) defaults
	if cfg.Server.SessionPersistenceDays == 0 {
		cfg.Server.SessionPersistenceDays = 30
	}
	if cfg.Server.RefreshTokenExpirationDays == 0 {
		cfg.Server.RefreshTokenExpirationDays = 90
	}
	if cfg.Server.MaxDevicesPerUser == 0 {
		cfg.Server.MaxDevicesPerUser = 5
	}
	if cfg.Server.MobileDeepLinkBaseURL == "" {
		cfg.Server.MobileDeepLinkBaseURL = cfg.Server.BaseURL
	}
	
	// Performance Optimization (#260) defaults
	if cfg.Server.CacheTTLMinutes == 0 {
		cfg.Server.CacheTTLMinutes = 30
	}
	if cfg.Server.RateLimit == 0 {
		cfg.Server.RateLimit = 100
	}
	if cfg.Server.RateLimitWindow == 0 {
		cfg.Server.RateLimitWindow = 60
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
