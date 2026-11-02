package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/ademant/dansal/internal/logging"
)

// systemd watchdog (#1742). A hung pool keeps the process alive, so
// Restart=on-failure never fires. The API pings systemd only while a
// connection can still be taken from the shared pool, so a pool hang ends in
// a watchdog restart, and the journal gets a goroutine dump (GOTRACEBACK=all
// in the unit).

// sdNotify sends one state line to systemd over $NOTIFY_SOCKET. It does
// nothing when the variable is unset (tests, manual runs, non-systemd installs)
// and never fails the server: a missed notification is only logged.
func sdNotify(state string) error {
	addr := os.Getenv("NOTIFY_SOCKET")
	if addr == "" {
		return nil
	}
	if addr[0] == '@' {
		addr = "\x00" + addr[1:] // abstract socket
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte(state))
	return err
}

// watchdogInterval returns the watchdog period systemd asks for (WatchdogSec),
// or 0 when no watchdog is configured or it was addressed to another process.
func watchdogInterval() time.Duration {
	usec, err := strconv.ParseInt(os.Getenv("WATCHDOG_USEC"), 10, 64)
	if err != nil || usec <= 0 {
		return 0
	}
	if pid := os.Getenv("WATCHDOG_PID"); pid != "" && pid != strconv.Itoa(os.Getpid()) {
		return 0
	}
	return time.Duration(usec) * time.Microsecond
}

// dbAlive takes one connection from the shared pool and runs SELECT 1. It uses
// the shared pool on purpose: pool exhaustion is the failure to detect.
func dbAlive(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var one int
	return conn.QueryRowContext(ctx, "SELECT 1").Scan(&one)
}

// runWatchdog calls check every interval and sends WATCHDOG=1 only when it
// succeeds. A single failure is skipped without a log line beyond a warning:
// systemd acts only after the full WatchdogSec with no ping.
func runWatchdog(ctx context.Context, interval time.Duration, check func(context.Context) error, notify func(string) error) {
	if interval <= 0 {
		return // a zero ticker would panic; watchdogInterval/4 is 0 only for a tiny WatchdogSec
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := check(ctx); err != nil {
				logging.Warnf("watchdog: db check failed: %v", err)
				continue
			}
			if err := notify("WATCHDOG=1"); err != nil {
				logging.Debugf("watchdog: notify: %v", err)
			}
		}
	}
}

// healthHandler serves GET /api/v1/health. It reports 503 when the database
// cannot answer, so manual checks, monitors and the watchdog agree (#1742).
func healthHandler(w http.ResponseWriter, r *http.Request) {
	status, code, dbStatus := "ok", http.StatusOK, "ok"
	if err := dbAlive(r.Context()); err != nil {
		status, code, dbStatus = "error", http.StatusServiceUnavailable, err.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{
		"status":  status,
		"db":      dbStatus,
		"version": Version,
		"time":    time.Now().UTC().Format(time.RFC3339),
	})
}
