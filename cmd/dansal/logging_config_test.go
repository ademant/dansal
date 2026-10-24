package main

import "testing"

// TestLogLevelDefaultsToInfo checks the config default is info when the key
// is absent (#1645).
func TestLogLevelDefaultsToInfo(t *testing.T) {
	cfg := &Config{}
	applyDefaults(cfg)
	if cfg.Server.LogLevel != "info" {
		t.Fatalf("LogLevel = %q, want info", cfg.Server.LogLevel)
	}
}
