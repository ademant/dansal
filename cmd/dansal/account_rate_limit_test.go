package main

import (
	"testing"
	"time"
)

// TestAccountMutationRateLimitConfigurable covers making createUpdateLimiter's
// 30/min cap configurable via config.Server.AccountMutationRateLimit (raised
// per-instance, e.g. for a dev instance running the e2e suite, without
// weakening the default everywhere else).
func TestAccountMutationRateLimitConfigurable(t *testing.T) {
	t.Run("applyDefaults fills in 30 when unset", func(t *testing.T) {
		cfg := &Config{}
		applyDefaults(cfg)
		if cfg.Server.AccountMutationRateLimit != 30 {
			t.Errorf("AccountMutationRateLimit = %d, want default 30", cfg.Server.AccountMutationRateLimit)
		}
	})

	t.Run("applyDefaults leaves an explicit value untouched", func(t *testing.T) {
		cfg := &Config{Server: ServerConfig{AccountMutationRateLimit: 120}}
		applyDefaults(cfg)
		if cfg.Server.AccountMutationRateLimit != 120 {
			t.Errorf("AccountMutationRateLimit = %d, want the configured 120", cfg.Server.AccountMutationRateLimit)
		}
	})

	t.Run("a higher configured limit actually allows more requests", func(t *testing.T) {
		lim := newAccountLimiter(120, time.Minute)
		allowed := 0
		for i := 0; i < 100; i++ {
			if lim.Allow(1) {
				allowed++
			}
		}
		if allowed != 100 {
			t.Errorf("allowed %d/100 requests under a limit of 120, want all 100", allowed)
		}
	})

	t.Run("the default 30 limit still rejects past the 30th request in a window", func(t *testing.T) {
		lim := newAccountLimiter(30, time.Minute)
		allowed := 0
		for i := 0; i < 40; i++ {
			if lim.Allow(1) {
				allowed++
			}
		}
		if allowed != 30 {
			t.Errorf("allowed %d/40 requests under a limit of 30, want exactly 30", allowed)
		}
	})
}
