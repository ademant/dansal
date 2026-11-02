package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestSdNotifySendsToNotifySocket covers #1742: the state line reaches the
// socket systemd named in NOTIFY_SOCKET.
func TestSdNotifySendsToNotifySocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	t.Setenv("NOTIFY_SOCKET", path)

	if err := sdNotify("READY=1"); err != nil {
		t.Fatalf("sdNotify: %v", err)
	}
	buf := make([]byte, 64)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := conn.ReadFromUnix(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := string(buf[:n]); got != "READY=1" {
		t.Fatalf("got %q, want READY=1", got)
	}
}

// TestSdNotifyIsNoOpWithoutSocket covers #1742: outside systemd nothing is sent
// and nothing fails.
func TestSdNotifyIsNoOpWithoutSocket(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	if err := sdNotify("WATCHDOG=1"); err != nil {
		t.Fatalf("sdNotify without socket: %v", err)
	}
}

// TestRunWatchdogPingsOnlyWhenDBAnswers covers #1742: a failed check is not
// pinged, a passing one is.
func TestRunWatchdogPingsOnlyWhenDBAnswers(t *testing.T) {
	var checks, successes, pings atomic.Int32
	check := func(context.Context) error {
		n := checks.Add(1)
		if n%2 == 0 { // every second check fails
			return os.ErrDeadlineExceeded
		}
		successes.Add(1)
		return nil
	}
	notify := func(state string) error {
		if state != "WATCHDOG=1" {
			t.Errorf("notify state %q, want WATCHDOG=1", state)
		}
		pings.Add(1)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runWatchdog(ctx, 5*time.Millisecond, check, notify)
		close(done)
	}()
	time.Sleep(80 * time.Millisecond)
	cancel()
	<-done

	if checks.Load() < 4 {
		t.Fatalf("only %d checks ran; test too short to mean anything", checks.Load())
	}
	if pings.Load() != successes.Load() {
		t.Errorf("pings = %d, successful checks = %d: a failed check must not ping", pings.Load(), successes.Load())
	}
}

// TestHealthReports503WhenPoolIsExhausted covers #1742: with the only pool
// connection held, /api/v1/health reports the database as down instead of ok.
func TestHealthReports503WhenPoolIsExhausted(t *testing.T) {
	conn := setupDedupTestDB(t)
	conn.SetMaxOpenConns(1)
	held, err := conn.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	healthHandler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d with exhausted pool, want 503 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"error"`) {
		t.Errorf("body %s lacks status error", rec.Body.String())
	}
}

// TestHealthReportsOKWithDatabase covers #1742: a healthy database gives 200
// with db ok.
func TestHealthReportsOKWithDatabase(t *testing.T) {
	setupDedupTestDB(t)
	rec := httptest.NewRecorder()
	healthHandler(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"db":"ok"`) {
		t.Fatalf("status %d body %s, want 200 with db ok", rec.Code, rec.Body.String())
	}
}
