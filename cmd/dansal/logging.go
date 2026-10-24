package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"log/syslog"
	"strings"
)

// priorityWriter is the part of *syslog.Writer the logger writes through. It
// is an interface so tests can record which priority each record was sent at.
type priorityWriter interface {
	Debug(string) error
	Info(string) error
	Warning(string) error
	Err(string) error
}

// logLevel is the instance's minimum level. It starts at Info and is set from
// server.log_level once the config is loaded (#1645).
var logLevel = new(slog.LevelVar)

// syslogHandler is a slog.Handler that sends each record to syslog at the
// priority matching its level, so journald can filter with `journalctl -p`.
type syslogHandler struct {
	w      priorityWriter
	level  *slog.LevelVar
	prefix string // attributes added with WithAttrs, already formatted
}

func (h *syslogHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level.Level()
}

func (h *syslogHandler) Handle(_ context.Context, r slog.Record) error {
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

func (h *syslogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
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
func (h *syslogHandler) WithGroup(string) slog.Handler { return h }

// setupLogging sends slog records and the standard log package to syslog under
// the given tag. The level is set separately, after the config is loaded. If
// syslog is unavailable, logging stays on stderr, as before.
func setupLogging(tag string) {
	w, err := syslog.New(syslog.LOG_INFO|syslog.LOG_DAEMON, tag)
	if err != nil {
		return
	}
	slog.SetDefault(slog.New(&syslogHandler{w: w, level: logLevel}))
	log.SetFlags(0)
}

// parseLogLevel maps server.log_level to a slog level. An empty value means
// the default, info.
func parseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("server.log_level %q: want debug, info, warning or error", s)
}
