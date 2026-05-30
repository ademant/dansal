package main

import (
	"database/sql"
	"flag"
	"log"
	"os"
	"strings"

	"gopkg.in/yaml.v2"
)

type Config struct {
	Listen    string `yaml:"listen"`
	Domain    string `yaml:"domain"`
	BaseURL   string `yaml:"base_url"`   // optional; defaults to https://{domain}
	DansalURL string `yaml:"dansal_url"`
	DBPath    string `yaml:"db_path"`
	PollSecs  int    `yaml:"poll_secs"`
	I18nFile  string `yaml:"i18n_file"`  // optional path to override embedded i18n.yaml
	PagesFile string `yaml:"pages_file"` // optional path to impressum/contact YAML
	HelpDir   string `yaml:"help_dir"`   // optional path to per-language help markdown overrides, e.g. /etc/dansal/help

	// NodeInfo metadata (served at /nodeinfo/2.1)
	NodeInfoDescription     string `yaml:"nodeinfo_description"`
	NodeInfoMaintainerName  string `yaml:"nodeinfo_maintainer_name"`
	NodeInfoMaintainerEmail string `yaml:"nodeinfo_maintainer_email"`

	// Federation
	RelayActorName      string `yaml:"relay_actor_name"`
	ShowFederatedEvents bool   `yaml:"show_federated_events"`

	// Layout
	ImagesDir        string `yaml:"images_dir"`         // directory for logo.svg, banner.svg, favicon.svg
	BannerHeightMain int    `yaml:"banner_height_main"` // px; 0 = hidden
	BannerHeightSub  int    `yaml:"banner_height_sub"`  // px; 0 = hidden (default)
	LogoHeightMain   int    `yaml:"logo_height_main"`   // px in nav on main page
	LogoHeightSub    int    `yaml:"logo_height_sub"`    // px in nav on sub pages
	DarkMode         string `yaml:"dark_mode"`          // "auto" (default), "light", "dark"

	pagesContent *PagesContent
	configPath   string // path from which config was loaded; used for reload

	// Telegram
	TelegramWebhookSecret string `yaml:"telegram_webhook_secret"`
	TelegramBotToken      string `yaml:"telegram_bot_token"` // used only to gate suggest feature

	// SMTP — mirrors API config; used only to determine suggest hint text
	SMTPHost     string `yaml:"smtp_host"`
	SMTPSendmail string `yaml:"smtp_sendmail"`

	// Captcha (Cloudflare Turnstile)
	CaptchaSiteKey   string `yaml:"captcha_site_key"`
	CaptchaSecretKey string `yaml:"captcha_secret_key"`

	// Rate limiting for auth endpoints
	LoginMaxFailures   int `yaml:"login_max_failures"`    // max bad login attempts before block; default 5
	LoginWindowMins    int `yaml:"login_window_mins"`     // sliding window for login failures; default 10
	AuthRateLimit      int `yaml:"auth_rate_limit"`       // max requests per window for register/magic/verify; default 10
	AuthRateWindowMins int `yaml:"auth_rate_window_mins"` // window in minutes; default 10
	MinSubmitSecs      int `yaml:"min_submit_secs"`       // minimum seconds between form load and submit; 0 disables; default 3

	// Rate limiting for public form endpoints (booking, board, suggest)
	PublicRateLimit           int `yaml:"public_rate_limit"`            // max requests per window; default 10
	PublicRateWindowMins      int `yaml:"public_rate_window_mins"`      // window in minutes; default 10
	PublicThrottleForgetHours int `yaml:"public_throttle_forget_hours"` // forget inactive entries after N hours; default 1

	// Form token anti-bot protection
	FormTokenMaxAgeMins  int  `yaml:"form_token_max_age_mins"`  // token validity window; default 10
	FormTokenCleanupMins int  `yaml:"form_token_cleanup_mins"`  // cleanup interval; default 5
	FormTokenBindIP      bool `yaml:"form_token_bind_ip"`       // bind token to client IP; default false

	// Session management
	SessionIdleTimeoutMins int `yaml:"session_idle_timeout_mins"` // 0 = disabled; shown as client-side warning

	// Per-user rate limiting for authorized POST endpoints
	UserRateLimitGlobal int            `yaml:"user_rate_limit_global"` // max POST requests/minute per user; default 100
	UserRateLimits      map[string]int `yaml:"user_rate_limits"`       // endpoint-specific limits; default 5/minute each

	// Loaded from web.yaml; overridden via admin site-config page (stored in web.db).
	SiteName          string `yaml:"site_name"`
	ContactOverride   string
	ImpressumOverride map[string]string
}

var impressumLangs = []string{"de", "en", "fr", "nl", "it", "es", "br", "bzh"}

// publicBaseURL returns the canonical public URL of the web app.
func (cfg *Config) publicBaseURL() string {
	if cfg.BaseURL != "" {
		return strings.TrimRight(cfg.BaseURL, "/")
	}
	return "https://" + cfg.Domain
}

func loadConfig() *Config {
	cfg := &Config{
		Listen:                    ":8080",
		DBPath:                    "web.db",
		PollSecs:                  300,
		ImagesDir:                 "/var/lib/dansal-web",
		BannerHeightMain:          200,
		BannerHeightSub:           0,
		LogoHeightMain:            48,
		LogoHeightSub:             32,
		DarkMode:                  "auto",
		LoginMaxFailures:          5,
		LoginWindowMins:           10,
		AuthRateLimit:             10,
		AuthRateWindowMins:        10,
		MinSubmitSecs:             3,
		RelayActorName:            "relay",
		PublicRateLimit:           10,
		PublicRateWindowMins:      10,
		PublicThrottleForgetHours: 1,
		FormTokenMaxAgeMins:       10,
		FormTokenCleanupMins:      5,
		UserRateLimitGlobal:       100,
	}

	configPath := ""
	flag.StringVar(&configPath, "config", "", "path to YAML config file")
	flag.Parse()
	if configPath == "" && flag.NArg() > 0 {
		configPath = flag.Arg(0)
	}

	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err != nil {
			log.Fatalf("read config: %v", err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			log.Fatalf("parse config: %v", err)
		}
	}

	if v := os.Getenv("DANSAL_DOMAIN"); v != "" {
		cfg.Domain = v
	}
	if v := os.Getenv("DANSAL_URL"); v != "" {
		cfg.DansalURL = v
	}

	if cfg.Domain == "" {
		log.Fatal("domain is required (set via config file or DANSAL_DOMAIN env var)")
	}
	if cfg.DansalURL == "" {
		log.Fatal("dansal_url is required (set via config file or DANSAL_URL env var)")
	}

	cfg.configPath = configPath
	return cfg
}

// reloadConfig re-reads the YAML file at path and applies DB overrides.
// Returns nil on any error so the caller can keep the current config.
func reloadConfig(path string, db *sql.DB) *Config {
	cfg := &Config{
		Listen:                    ":8080",
		DBPath:                    "web.db",
		PollSecs:                  300,
		ImagesDir:                 "/var/lib/dansal-web",
		BannerHeightMain:          200,
		BannerHeightSub:           0,
		LogoHeightMain:            48,
		LogoHeightSub:             32,
		DarkMode:                  "auto",
		LoginMaxFailures:          5,
		LoginWindowMins:           10,
		AuthRateLimit:             10,
		AuthRateWindowMins:        10,
		MinSubmitSecs:             3,
		RelayActorName:            "relay",
		PublicRateLimit:           10,
		PublicRateWindowMins:      10,
		PublicThrottleForgetHours: 1,
		FormTokenMaxAgeMins:       10,
		FormTokenCleanupMins:      5,
		UserRateLimitGlobal:       100,
	}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			log.Printf("reload: read %s: %v", path, err)
			return nil
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			log.Printf("reload: parse %s: %v", path, err)
			return nil
		}
	}
	if v := os.Getenv("DANSAL_DOMAIN"); v != "" {
		cfg.Domain = v
	}
	if v := os.Getenv("DANSAL_URL"); v != "" {
		cfg.DansalURL = v
	}
	if cfg.Domain == "" || cfg.DansalURL == "" {
		log.Print("reload: domain and dansal_url are required; keeping current config")
		return nil
	}
	if v := getSiteSetting(db, "site_name"); v != "" {
		cfg.SiteName = v
	}
	if v := getSiteSetting(db, "contact"); v != "" {
		cfg.ContactOverride = v
	}
	imp := make(map[string]string)
	for _, lang := range impressumLangs {
		if v := getSiteSetting(db, "impressum_"+lang); v != "" {
			imp[lang] = v
		}
	}
	cfg.ImpressumOverride = imp
	cfg.pagesContent = loadPagesContent(cfg.PagesFile)
	cfg.configPath = path
	return cfg
}
