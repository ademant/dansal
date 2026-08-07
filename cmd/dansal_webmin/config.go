package main

import (
	"flag"
	"log"
	"os"
	"strings"

	"gopkg.in/yaml.v2"
)

var Version = "dev"
var BuildTime = "unknown"

const defaultConfigPath = "/etc/dansal/webmin.yaml"

type Config struct {
	Listen                string `yaml:"listen"`
	DansalURL             string `yaml:"dansal_url"`
	AdminSocket           string `yaml:"admin_socket"`
	SiteName              string `yaml:"site_name"`
	Instance              string `yaml:"instance"`
	OrgID                 int    `yaml:"org_id"`       // optional: filter dashboard events to this org
	WebDBPath             string `yaml:"web_db_path"`  // path to web.db for site-config editing
	ImagesDir             string `yaml:"images_dir"`   // path to images dir for logo/banner/favicon/relay assets
	WebURL                string `yaml:"web_url"`      // internal URL of dansal-web (for relay redeliver)
	BotStatsDBPath        string `yaml:"bot_stats_db"` // path to bot-stats.db; defaults to /var/lib/dansal/bot-stats.db
	CertOnly              bool   `yaml:"cert_only"`    // when true, disable password login entirely — mTLS client cert required (#994)
	ReadHeaderTimeoutSecs int    `yaml:"read_header_timeout_secs"`
	ReadTimeoutSecs       int    `yaml:"read_timeout_secs"`
	WriteTimeoutSecs      int    `yaml:"write_timeout_secs"`
	IdleTimeoutSecs       int    `yaml:"idle_timeout_secs"`
	configPath            string
}

func loadConfig() *Config {
	configPath := flag.String("config", defaultConfigPath, "path to webmin.yaml")
	flag.Parse()
	return loadConfigFrom(*configPath)
}

func loadConfigFrom(path string) *Config {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("read config %s: %v", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		log.Fatalf("parse config: %v", err)
	}
	applyWebminEnvOverrides(&cfg)
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:8090"
	}
	if cfg.ReadHeaderTimeoutSecs == 0 {
		cfg.ReadHeaderTimeoutSecs = 5
	}
	if cfg.ReadTimeoutSecs == 0 {
		cfg.ReadTimeoutSecs = 10
	}
	if cfg.WriteTimeoutSecs == 0 {
		cfg.WriteTimeoutSecs = 30
	}
	if cfg.IdleTimeoutSecs == 0 {
		cfg.IdleTimeoutSecs = 60
	}
	if cfg.DansalURL == "" {
		cfg.DansalURL = "http://127.0.0.1:8000"
	}
	if cfg.AdminSocket == "" {
		cfg.AdminSocket = "/run/dansal/dansal.sock"
	}
	if cfg.SiteName == "" {
		cfg.SiteName = "Dansal Webmin"
	}
	if cfg.BotStatsDBPath == "" {
		cfg.BotStatsDBPath = "/var/lib/dansal/bot-stats.db"
	}
	if cfg.WebURL == "" {
		cfg.WebURL = "http://127.0.0.1:8080"
	}
	cfg.configPath = path
	return &cfg
}

// applyWebminEnvOverrides overlays environment variables so container
// deployments can inject secrets without editing the YAML config file.
func applyWebminEnvOverrides(cfg *Config) {
	if v := os.Getenv("DANSAL_WEBMIN_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv("DANSAL_WEBMIN_DANSAL_URL"); v != "" {
		cfg.DansalURL = strings.TrimRight(v, "/")
	}
	if v := os.Getenv("DANSAL_WEBMIN_CERT_ONLY"); v != "" {
		cfg.CertOnly = v == "1" || strings.EqualFold(v, "true")
	}
}
