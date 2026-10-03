package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
)

// version is set by the release build.
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("mutter", version)
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mutter:", err)
		os.Exit(1)
	}
}

func run() error {
	logs, level, err := setupLogging()
	if err != nil {
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
	hc := apiClient(ctx, ts)
	defer logAPICalls(hc)
	c, err := newClient(ctx, hc)
	if err != nil {
		return err
	}
	ch := make(chan tea.Msg, 64)
	if cfg.Topic != "" {
		go runEvents(ctx, hc, c, cfg.Topic, ch)
	} else {
		ch <- liveMsg{errors.New("no topic configured, live updates off")}
	}
	var opts []tea.ProgramOption
	if level <= LevelTrace {
		// #nosec G304 -- the path is in the user's own log directory
		f, err := os.OpenFile(filepath.Join(logs, "tty.out"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		opts = append(opts, tea.WithOutput(teeFile{os.Stdout, f}))
	}
	mdl := newModel(ctx, c, ch)
	if st, err := openStore(c.me); err != nil {
		slog.Warn("cache unavailable, running without it", "err", err)
	} else {
		mdl.store, mdl.imgs.store = st, st
		go func() {
			if err := errors.Join(st.prune("msgs", maxMessageCache), st.prune("img", maxImageCache)); err != nil {
				slog.Warn("cache prune", "err", err)
			}
		}()
	}
	final, err := tea.NewProgram(mdl, opts...).Run()
	fmt.Print(kittyClear)
	if fm, ok := final.(model); ok {
		fm.saveDraft() // the context open at exit
		if write := fm.stash(); write != nil {
			write()
		}
	}
	return err
}

// Set with -ldflags "-X main.clientID=... -X main.clientSecret=... -X main.topic=...".
// Desktop OAuth client secrets are not confidential.
var clientID, clientSecret, topic string

func loadConfig() (config, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return config{}, err
	}
	path := filepath.Join(dir, "mutter", "config.json")
	b, err := os.ReadFile(path) // #nosec G304 -- path is in the user's config dir
	if errors.Is(err, fs.ErrNotExist) && clientID != "" {
		return config{ClientID: clientID, ClientSecret: clientSecret, Topic: topic}, nil
	}
	if err != nil {
		return config{}, fmt.Errorf("read OAuth client config: %w", err)
	}
	var cfg config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return config{}, fmt.Errorf("%s: %w", path, err)
	}
	if cfg.ClientID == "" && cfg.ClientSecret == "" {
		// A file that only sets the topic keeps the baked-in client.
		cfg.ClientID, cfg.ClientSecret = clientID, clientSecret
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return config{}, fmt.Errorf("%s: client_id and client_secret are required", path)
	}
	cfg.Topic = cmp.Or(cfg.Topic, topic)
	return cfg, nil
}
