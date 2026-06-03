package main

import (
	"flag"
	"log"
	"os"

	"gopkg.in/yaml.v2"
)

var Version   = "dev"
var BuildTime = "unknown"

const defaultConfigPath = "/etc/dansal/webmin.yaml"

type Config struct {
	Listen        string `yaml:"listen"`
	DansalURL     string `yaml:"dansal_url"`
	AdminSocket   string `yaml:"admin_socket"`
	SessionSecret string `yaml:"session_secret"`
	SiteName      string `yaml:"site_name"`
	Instance      string `yaml:"instance"`
	WebDBPath        string `yaml:"web_db_path"`        // path to web.db for site-config editing
	ImagesDir        string `yaml:"images_dir"`         // path to images dir for logo/banner/favicon
	ReadTimeoutSecs  int    `yaml:"read_timeout_secs"`
	WriteTimeoutSecs int    `yaml:"write_timeout_secs"`
	IdleTimeoutSecs  int    `yaml:"idle_timeout_secs"`
	configPath       string
}

func loadConfig() *Config {
	configPath := flag.String("config", defaultConfigPath, "path to webmin.yaml")
	flag.Parse()
	return loadConfigFrom(*configPath)
}

func reloadConfig(path string) *Config {
	cfg := loadConfigFrom(path)
	return cfg
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
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:8090"
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
	if cfg.SessionSecret == "" {
		log.Fatal("session_secret must be set in webmin.yaml")
	}
	if cfg.SiteName == "" {
		cfg.SiteName = "Dansal Webmin"
	}
	cfg.configPath = path
	return &cfg
}
