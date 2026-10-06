package main

import (
	"context"
	"errors"
	"fmt"
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
	if len(os.Args) > 1 && os.Args[1] == "config" {
		if err := configMain(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "mutter config:", err)
			os.Exit(1)
		}
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
