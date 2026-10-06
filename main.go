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

// usage is the --help text, with this machine's paths filled in.
func usage() string {
	path := func(p string, err error) string {
		if err != nil {
			return "unknown: " + err.Error()
		}
		return p
	}
	cache, err := cachePath("")
	cfg, cerr := configPath()
	logs, lerr := logDir()
	return fmt.Sprintf(`mutter, a terminal client for Google Chat

Usage:
  mutter                                   start the client
  mutter config init [--force]             write an empty config to fill in
  mutter config edit                       open the config in $VISUAL or $EDITOR, and check it
  mutter config import [--force] FILE|URL  install a config from a file or an https URL
  mutter admin setup                       set up a Google Cloud project for mutter (admins)
  mutter admin provision PROJECT GROUP     give each member of GROUP a topic, for per-user topics
  mutter --version                         print the version
  mutter --help                            print this help

Inside mutter, F1 or /help lists keys and commands.

Environment:
  MUTTER_LOG_LEVEL   trace, debug, info, warn or error. Default warn.
  GIPHY_API_KEY      your GIPHY API key, which turns on /gif
  VISUAL, EDITOR     the editor for ctrl+e and mutter config edit. Default vi.

Files:
  config  %s
  logs    %s
  cache   %s
`, path(cfg, cerr), path(logs, lerr), path(cache, err))
}

// version is set by the release build.
var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-h", "--help", "help":
			fmt.Print(usage())
			return
		case "--version":
			fmt.Println("mutter", version)
			return
		case "config", "admin":
		default:
			fmt.Fprintf(os.Stderr, "mutter: unknown argument %q\n\n%s", os.Args[1], usage())
			os.Exit(2)
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "admin" {
		if err := adminMain(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "mutter admin:", err)
			os.Exit(1)
		}
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
	if cfg.Topic != "" || cfg.TopicProject != "" {
		go runEvents(ctx, hc, c, cfg, ch)
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
