package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// recordingWriter remembers each record and the priority it was sent at.
type recordingWriter struct {
	got []string // "<priority> <message>"
}

func (r *recordingWriter) add(p, msg string) error {
	r.got = append(r.got, p+" "+msg)
	return nil
}
func (r *recordingWriter) Debug(m string) error   { return r.add("debug", m) }
func (r *recordingWriter) Info(m string) error    { return r.add("info", m) }
func (r *recordingWriter) Warning(m string) error { return r.add("warning", m) }
func (r *recordingWriter) Err(m string) error     { return r.add("err", m) }

func newTestLogger(level slog.Level) (*slog.Logger, *recordingWriter) {
	w := &recordingWriter{}
	lv := new(slog.LevelVar)
	lv.Set(level)
	return slog.New(&syslogHandler{w: w, level: lv}), w
}

// TestSyslogHandlerMapsLevelsToPriorities covers #1645: each slog level is
// sent to the syslog priority journald filters on.
func TestSyslogHandlerMapsLevelsToPriorities(t *testing.T) {
	logger, w := newTestLogger(slog.LevelDebug)
	logger.Debug("d")
	logger.Info("i")
	logger.Warn("w")
	logger.Error("e")

	want := []string{"debug d", "info i", "warning w", "err e"}
	if fmt.Sprint(w.got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", w.got, want)
	}
}

// TestSyslogHandlerLevelFilters covers the configured minimum: records below
// it are not written at all.
func TestSyslogHandlerLevelFilters(t *testing.T) {
	tests := []struct {
		name      string
		level     slog.Level
		logs      func(*slog.Logger)
		wantN     int
		wantFirst string
	}{
		{"warning drops info, keeps warn and error", slog.LevelWarn,
			func(l *slog.Logger) { l.Info("i"); l.Warn("w"); l.Error("e") }, 2, "warning w"},
		{"error keeps only errors", slog.LevelError,
			func(l *slog.Logger) { l.Info("i"); l.Warn("w"); l.Error("e") }, 1, "err e"},
		{"info drops debug", slog.LevelInfo,
			func(l *slog.Logger) { l.Debug("d"); l.Info("i") }, 1, "info i"},
		{"debug keeps everything", slog.LevelDebug,
			func(l *slog.Logger) { l.Debug("d") }, 1, "debug d"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logger, w := newTestLogger(tc.level)
			tc.logs(logger)
			if len(w.got) != tc.wantN {
				t.Fatalf("got %d records %v, want %d", len(w.got), w.got, tc.wantN)
			}
			if tc.wantN > 0 && w.got[0] != tc.wantFirst {
				t.Fatalf("first = %q, want %q", w.got[0], tc.wantFirst)
			}
		})
	}
}

// TestSyslogHandlerFormatsAttributes checks that record and WithAttrs
// attributes are appended as key=value pairs.
func TestSyslogHandlerFormatsAttributes(t *testing.T) {
	logger, w := newTestLogger(slog.LevelInfo)
	logger.With("instance", "dev").Info("import done", "events", 3)

	if len(w.got) != 1 {
		t.Fatalf("got %v, want one record", w.got)
	}
	for _, part := range []string{"import done", "instance=dev", "events=3"} {
		if !strings.Contains(w.got[0], part) {
			t.Fatalf("%q missing %q", w.got[0], part)
		}
	}
}

// TestSyslogHandlerEnabledTracksLevelChanges checks that the handler reads
// the level on each call, so setting it at startup takes effect for all
// records afterwards.
func TestSyslogHandlerEnabledTracksLevelChanges(t *testing.T) {
	lv := new(slog.LevelVar)
	h := &syslogHandler{w: &recordingWriter{}, level: lv}
	if h.Enabled(context.Background(), slog.LevelInfo) != true {
		t.Fatal("info should be enabled at the default level")
	}
	lv.Set(slog.LevelError)
	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("info should be disabled after raising to error")
	}
}

// TestParseLogLevel covers server.log_level: the documented values, the
// default for an empty value, and rejection of anything else.
func TestParseLogLevel(t *testing.T) {
	ok := map[string]slog.Level{
		"":        slog.LevelInfo,
		"info":    slog.LevelInfo,
		"debug":   slog.LevelDebug,
		"WARNING": slog.LevelWarn,
		" error ": slog.LevelError,
	}
	for in, want := range ok {
		got, err := parseLogLevel(in)
		if err != nil {
			t.Fatalf("parseLogLevel(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("parseLogLevel(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := parseLogLevel("verbose"); err == nil {
		t.Fatal("parseLogLevel(\"verbose\") should fail")
	}
}

// TestLogLevelDefaultsToInfo checks the config default is info when the key
// is absent.
func TestLogLevelDefaultsToInfo(t *testing.T) {
	cfg := &Config{}
	applyDefaults(cfg)
	if cfg.Server.LogLevel != "info" {
		t.Fatalf("LogLevel = %q, want info", cfg.Server.LogLevel)
	}
}
