package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLogLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{"": slog.LevelWarn, "trace": LevelTrace, "DEBUG": slog.LevelDebug, " info ": slog.LevelInfo, "error": slog.LevelError} {
		got, err := logLevel(in)
		if err != nil || got != want {
			t.Errorf("logLevel(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := logLevel("loud"); err == nil {
		t.Error("an unknown level should fail")
	}
}

func TestSetupLogging(t *testing.T) {
	defer slog.SetDefault(slog.Default())
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("MUTTER_LOG_LEVEL", "info")

	dir, level, err := setupLogging()
	if err != nil || level != slog.LevelInfo {
		t.Fatalf("setupLogging: %v, %v", level, err)
	}
	want := filepath.Join(home, "state", "mutter")
	if runtime.GOOS == darwin {
		want = filepath.Join(home, "Library", "Logs", "mutter")
	}
	if dir != want {
		t.Errorf("dir = %s, want %s", dir, want)
	}
	slog.Info("first run")
	slog.Debug("below the level")

	// A second start keeps the first run's log as mutter.log.1.
	if _, _, err := setupLogging(); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(filepath.Join(dir, "mutter.log.1"))
	if err != nil || !strings.Contains(string(old), "first run") || strings.Contains(string(old), "below the level") {
		t.Errorf("previous log = %q, %v", old, err)
	}
	if info, err := os.Stat(filepath.Join(dir, "mutter.log")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("log file: %v %v", info, err)
	}
}
