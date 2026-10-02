package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mutter:", err)
		os.Exit(1)
	}
}

func run() error {
	if err := setupDebug(); err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	ctx := context.Background()
	ts, err := tokenSource(ctx, cfg.oauth())
	if err != nil {
		return err
	}
	c, err := newClient(ctx, ts)
	if err != nil {
		return err
	}
	_, err = tea.NewProgram(newModel(ctx, c)).Run()
	return err
}

// Set with -ldflags "-X main.clientID=... -X main.clientSecret=...".
// Desktop OAuth client secrets are not confidential.
var clientID, clientSecret string

func loadConfig() (config, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return config{}, err
	}
	path := filepath.Join(dir, "mutter", "config.json")
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) && clientID != "" {
		return config{ClientID: clientID, ClientSecret: clientSecret}, nil
	}
	if err != nil {
		return config{}, fmt.Errorf("read OAuth client config: %w", err)
	}
	var cfg config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return config{}, fmt.Errorf("%s: %w", path, err)
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return config{}, fmt.Errorf("%s: client_id and client_secret are required", path)
	}
	return cfg, nil
}

// debugDir is set from MUTTER_DEBUG. It receives mutter.log and a copy of
// each image at every pipeline stage.
var debugDir string

func setupDebug() error {
	debugDir = os.Getenv("MUTTER_DEBUG")
	if debugDir == "" {
		log.SetOutput(io.Discard)
		return nil
	}
	if err := os.MkdirAll(debugDir, 0o700); err != nil {
		return err
	}
	_, err := tea.LogToFile(filepath.Join(debugDir, "mutter.log"), "")
	return err
}

func debugWrite(name string, b []byte) {
	if debugDir == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(debugDir, name), b, 0o600); err != nil {
		log.Printf("debug write %s: %v", name, err)
	}
}

// debugJSON logs v as JSON, to inspect raw API responses.
func debugJSON(label string, v any) {
	if debugDir == "" {
		return
	}
	b, err := json.Marshal(v)
	log.Printf("%s: %s err=%v", label, b, err)
}

// debugKey turns a long resource name into a short file name.
func debugKey(ref string) string {
	sum := sha256.Sum256([]byte(ref))
	return hex.EncodeToString(sum[:6])
}
