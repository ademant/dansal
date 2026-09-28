package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The keys documented in packaging/config.yaml must actually parse into the
// config struct. A rename or a typo in the yaml tag would otherwise leave the
// documentation describing settings that silently do nothing.
func TestPackagingConfigParsesCheckinWindow(t *testing.T) {
	src := filepath.Join("..", "..", "packaging", "config.yaml")
	if _, err := os.Stat(src); err != nil {
		t.Skipf("packaging/config.yaml not available: %v", err)
	}
	abs, err := filepath.Abs(src)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(abs)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got := cfg.Server.CheckinOpensBeforeMinutes; got != 120 {
		t.Errorf("checkin_opens_before_minutes = %d, want 120", got)
	}
	if got := cfg.Server.CheckinClosesAfterMinutes; got != 240 {
		t.Errorf("checkin_closes_after_minutes = %d, want 240", got)
	}
}

func TestCheckinWindowDefaultsAndOverrides(t *testing.T) {
	t.Run("unset falls back to the default", func(t *testing.T) {
		cfg := &Config{}
		applyDefaults(cfg)
		if got := cfg.Server.CheckinOpensBeforeMinutes; got != 120 {
			t.Errorf("default opens_before = %d, want 120", got)
		}
		if got := cfg.Server.CheckinClosesAfterMinutes; got != 240 {
			t.Errorf("default closes_after = %d, want 240", got)
		}
	})

	t.Run("explicit value is preserved", func(t *testing.T) {
		cfg := &Config{}
		cfg.Server.CheckinOpensBeforeMinutes = 15
		cfg.Server.CheckinClosesAfterMinutes = 45
		applyDefaults(cfg)
		if got := cfg.Server.CheckinOpensBeforeMinutes; got != 15 {
			t.Errorf("opens_before = %d, want 15", got)
		}
		if got := cfg.Server.CheckinClosesAfterMinutes; got != 45 {
			t.Errorf("closes_after = %d, want 45", got)
		}
	})

	t.Run("negative is preserved so a side can be disabled", func(t *testing.T) {
		cfg := &Config{}
		cfg.Server.CheckinClosesAfterMinutes = -1
		applyDefaults(cfg)
		if got := cfg.Server.CheckinClosesAfterMinutes; got != -1 {
			t.Errorf("closes_after = %d, want -1 to survive as the disable signal", got)
		}
	})
}
