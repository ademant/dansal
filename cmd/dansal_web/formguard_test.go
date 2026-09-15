package main

import (
	"strconv"
	"testing"
	"time"
)

// TestValidGeoToken covers the stateless HMAC token gating the public
// geocode search proxy (#1314) — the max-age direction is the opposite of
// validFormToken's min-age check, so it gets its own test rather than
// reusing that one's cases.
func TestValidGeoToken(t *testing.T) {
	t.Run("a freshly minted token is valid", func(t *testing.T) {
		if !validGeoToken(newFormToken(), 30*time.Minute) {
			t.Error("expected a fresh token to be valid")
		}
	})

	t.Run("a token older than maxAge is rejected", func(t *testing.T) {
		ts := time.Now().Add(-31 * time.Minute).Unix()
		tok := strconv.FormatInt(ts, 10) + "." + formMAC(ts)
		if validGeoToken(tok, 30*time.Minute) {
			t.Error("expected a stale token to be rejected")
		}
	})

	t.Run("a token exactly at the boundary is still valid", func(t *testing.T) {
		ts := time.Now().Add(-30 * time.Minute).Unix()
		tok := strconv.FormatInt(ts, 10) + "." + formMAC(ts)
		if !validGeoToken(tok, 30*time.Minute) {
			t.Error("expected a token exactly at maxAge to be valid (inclusive)")
		}
	})

	t.Run("a tampered MAC is rejected", func(t *testing.T) {
		ts := time.Now().Unix()
		tok := strconv.FormatInt(ts, 10) + ".notarealmac"
		if validGeoToken(tok, 30*time.Minute) {
			t.Error("expected a tampered token to be rejected")
		}
	})

	t.Run("a timestamp forged for a different instant is rejected", func(t *testing.T) {
		realTs := time.Now().Unix()
		real := strconv.FormatInt(realTs, 10) + "." + formMAC(realTs)
		mac := real[len(strconv.FormatInt(realTs, 10))+1:]
		forged := strconv.FormatInt(realTs+3600, 10) + "." + mac // reuse a real MAC under a different timestamp
		if validGeoToken(forged, 30*time.Minute) {
			t.Error("expected a forged timestamp/MAC mismatch to be rejected")
		}
	})

	t.Run("malformed input is rejected, not panics", func(t *testing.T) {
		for _, bad := range []string{"", "no-dot", "abc.def", "123", "123."} {
			if validGeoToken(bad, 30*time.Minute) {
				t.Errorf("expected %q to be rejected", bad)
			}
		}
	})

	t.Run("empty token never validates regardless of minSecs semantics elsewhere", func(t *testing.T) {
		// Unlike validFormToken, an empty/malformed token is never valid here
		// even with maxAge effectively unbounded — there's no minAge==0
		// "feature disabled" escape hatch for this check.
		if validGeoToken("", 24*time.Hour) {
			t.Error("expected an empty token to be rejected")
		}
	})
}
