package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// LevelTrace is below debug, for raw API responses and terminal output.
const LevelTrace = slog.Level(-8)

// darwin is runtime.GOOS on macOS.
const darwin = "darwin"

// logLevel parses MUTTER_LOG_LEVEL. Unset means warn.
func logLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace":
		return LevelTrace, nil
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return slog.LevelWarn, fmt.Errorf("MUTTER_LOG_LEVEL=%q: use trace, debug, info, warn or error", s)
}

// logDir is where logs go: ~/Library/Logs on macOS, and the XDG state
// directory elsewhere.
func logDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == darwin {
		return filepath.Join(home, "Library", "Logs", "mutter"), nil
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "mutter"), nil
	}
	return filepath.Join(home, ".local", "state", "mutter"), nil
}

// setupLogging sends slog to mutter.log in logDir, keeping the previous
// run's log as mutter.log.1. The terminal belongs to the UI, so logs never
// go there. It returns the log directory and level.
func setupLogging() (string, slog.Level, error) {
	level, err := logLevel(os.Getenv("MUTTER_LOG_LEVEL"))
	if err != nil {
		return "", level, err
	}
	dir, err := logDir()
	if err != nil {
		return "", level, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", level, err
	}
	path := filepath.Join(dir, "mutter.log")
	_ = os.Rename(path, path+".1") // the first run has nothing to keep
	// #nosec G304 -- the path is in the user's own log directory
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", level, err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.LevelKey && a.Value.Any() == LevelTrace {
				a.Value = slog.StringValue("TRACE")
			}
			return a
		},
	})))
	return dir, level, nil
}

func enabled(level slog.Level) bool {
	return slog.Default().Enabled(context.Background(), level)
}

// traceJSON logs v as JSON at trace level, to inspect raw API responses.
// kind says what v is.
func traceJSON(kind string, v any) {
	if !enabled(LevelTrace) {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		slog.Log(context.Background(), LevelTrace, "api response", "kind", kind, "err", err)
		return
	}
	slog.Log(context.Background(), LevelTrace, "api response", "kind", kind, "json", string(b))
}

// imageKey turns a long resource name into a short key for log lines.
func imageKey(ref string) string {
	sum := sha256.Sum256([]byte(ref))
	return hex.EncodeToString(sum[:6])
}

// teeFile records everything written to the terminal, at trace level. It
// keeps the terminal's Fd so Bubble Tea still detects a TTY.
type teeFile struct {
	*os.File
	rec io.Writer
}

func (t teeFile) Write(b []byte) (int, error) {
	_, _ = t.rec.Write(b)
	return t.File.Write(b)
}
