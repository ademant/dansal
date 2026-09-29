package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRunFetchSourceUsesConfiguredTimeout covers #1395: RunFetchSource used
// to share fastCallTimeout (15s) with every other DansalClient call, cutting
// off a slow-but-legitimate feed fetch before the API's own (much longer)
// allowance had a real chance to finish. It must use FetchRunTimeout
// instead, and fall back to fastCallTimeout when unset (a client built
// without it, e.g. in another test, keeps its old behavior).
func TestRunFetchSourceUsesConfiguredTimeout(t *testing.T) {
	const handlerDelay = 80 * time.Millisecond
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(handlerDelay)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"events": []any{}, "new": 1, "updated": 0, "unchanged": 0})
	}))
	defer upstream.Close()

	newClient := func(timeout time.Duration) *DansalClient {
		return &DansalClient{
			BaseURL:         upstream.URL,
			HTTP:            &http.Client{},
			FetchRunTimeout: timeout,
		}
	}

	t.Run("shorter than the handler delay: times out", func(t *testing.T) {
		c := newClient(20 * time.Millisecond)
		_, err := c.RunFetchSource(context.Background(), 19, "")
		if err == nil {
			t.Fatal("expected a timeout error")
		}
	})

	t.Run("longer than the handler delay: succeeds", func(t *testing.T) {
		c := newClient(2 * time.Second)
		res, err := c.RunFetchSource(context.Background(), 19, "")
		if err != nil {
			t.Fatalf("RunFetchSource: %v", err)
		}
		if res.New != 1 {
			t.Fatalf("New = %d, want 1", res.New)
		}
	})

	t.Run("unset FetchRunTimeout falls back to fastCallTimeout, not zero", func(t *testing.T) {
		c := newClient(0)
		// fastCallTimeout (15s) comfortably covers handlerDelay, so this
		// must succeed rather than failing instantly on a zero timeout.
		if _, err := c.RunFetchSource(context.Background(), 19, ""); err != nil {
			t.Fatalf("RunFetchSource with unset FetchRunTimeout: %v", err)
		}
	})
}

// TestFetchRunTimeoutSecsDefault covers the config default (#1395): an
// instance that doesn't set fetch_run_timeout_secs in web.yaml gets 25s, not
// 0 (which would make RunFetchSource fall back to the much shorter shared
// fastCallTimeout, silently undoing the fix for anyone who upgrades without
// touching their config). reloadConfig requires domain/dansal_url, so those
// are set; fetch_run_timeout_secs is deliberately left out of the YAML to
// prove the pre-populated struct default survives yaml.Unmarshal untouched.
func TestFetchRunTimeoutSecsDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.yaml")
	yaml := "domain: example.test\ndansal_url: http://127.0.0.1:8000\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := reloadConfig(path)
	if cfg == nil {
		t.Fatal("reloadConfig returned nil")
	}
	if cfg.FetchRunTimeoutSecs != 25 {
		t.Fatalf("FetchRunTimeoutSecs default = %d, want 25", cfg.FetchRunTimeoutSecs)
	}
}
