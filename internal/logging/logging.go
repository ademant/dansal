// Package logging sends dansal's log output to journald with a priority per
// level, and lets the instance choose a minimum level (#1645, #1646).
//
// Every binary calls Setup once at startup and SetLevel after its config is
// loaded. Call sites use Debugf, Infof, Warnf and Errorf; log.Fatal stays for
// startup failures.
package logging

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"log/syslog"
	"strings"
)

// priorityWriter is the part of *syslog.Writer the handler writes through. It
// is an interface so tests can record which priority each record was sent at.
type priorityWriter interface {
	Debug(string) error
	Info(string) error
	Warning(string) error
	Err(string) error
}

// level is the minimum level. It starts at Info and is set by SetLevel.
var level = new(slog.LevelVar)

// handler is a slog.Handler that sends each record to syslog at the priority
// matching its level, so journald can filter with `journalctl -p`.
type handler struct {
	w      priorityWriter
	level  *slog.LevelVar
	prefix string // attributes added with WithAttrs, already formatted
}

func (h *handler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level.Level()
}

func (h *handler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	b.WriteString(h.prefix)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value.Any())
		return true
	})
	msg := b.String()
	switch {
	case r.Level >= slog.LevelError:
		return h.w.Err(msg)
	case r.Level >= slog.LevelWarn:
		return h.w.Warning(msg)
	case r.Level >= slog.LevelInfo:
		return h.w.Info(msg)
	default:
		return h.w.Debug(msg)
	}
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	nh := *h
	var b strings.Builder
	b.WriteString(h.prefix)
	for _, a := range attrs {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value.Any())
	}
	nh.prefix = b.String()
	return &nh
}

// WithGroup is a no-op: dansal logs flat key=value pairs only.
func (h *handler) WithGroup(string) slog.Handler { return h }

// Setup sends slog records and the standard log package to syslog under tag.
// If syslog is unavailable, logging stays on stderr.
func Setup(tag string) {
	w, err := syslog.New(syslog.LOG_INFO|syslog.LOG_DAEMON, tag)
	if err != nil {
		return
	}
	setupWriter(w)
}

// setupWriter installs the handler on w. Setup uses it with the syslog
// writer; tests use it with a fake.
//
// The standard log package is sent straight to syslog at Err, not through the
// level filter: log.Fatal messages must stay visible even when the level is
// error, and nothing in dansal calls log.Print any more (#1646).
func setupWriter(w priorityWriter) {
	slog.SetDefault(slog.New(&handler{w: w, level: level}))
	log.SetOutput(errWriter{w: w})
	log.SetFlags(0)
}

// errWriter sends standard-log output to syslog at Err priority.
type errWriter struct{ w priorityWriter }

func (e errWriter) Write(p []byte) (int, error) {
	msg := strings.TrimRight(string(p), "\n")
	return len(p), e.w.Err(msg)
}

// SetLevel sets the minimum level from a config value: debug, info (also the
// default for an empty value), warning or error. Records below it are dropped.
func SetLevel(name string) error {
	lvl, err := ParseLevel(name)
	if err != nil {
		return err
	}
	level.Set(lvl)
	return nil
}

// Debugging reports whether debug records are written. Callers use it to
// switch on diagnostics that cost something, such as tracking open database
// result sets (#1732).
func Debugging() bool { return level.Level() <= slog.LevelDebug }

// ParseLevel maps a config value to a slog level.
func ParseLevel(name string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("log_level %q: want debug, info, warning or error", name)
}

// Debugf logs routine per-request or per-item detail.
func Debugf(format string, args ...any) { slog.Debug(fmt.Sprintf(format, args...)) }

// Infof logs normal events worth a line on a healthy system.
func Infof(format string, args ...any) { slog.Info(fmt.Sprintf(format, args...)) }

// Warnf logs something unexpected that the service handled, or a degraded state.
func Warnf(format string, args ...any) { slog.Warn(fmt.Sprintf(format, args...)) }

// Errorf logs a failed request or job that an operator should look at.
func Errorf(format string, args ...any) { slog.Error(fmt.Sprintf(format, args...)) }
