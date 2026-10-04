# Project instructions

mutter is a terminal client for Google Chat, written in Go with Bubble Tea v2.
The README covers setup, features and usage.
This file covers how the code is organized and the rules for changing it.

## Layout

Everything is one `main` package, split into files by concern:

| File | Owns |
|---|---|
| `main.go` | config, wiring, debug mode |
| `auth.go` | OAuth login with PKCE, token storage in the keychain |
| `chat.go` | Chat API client: spaces, threads, sending, title cache |
| `events.go` | Workspace Events and Pub/Sub subscriptions, event handling |
| `notify.go` | read state, unread markers, notification rules |
| `ui.go` | the Bubble Tea model, key handling, rendering |
| `actions.go` | actions on a selected message: react, edit, delete, files |
| `compose.go` | completion for @mentions, `/dm` and emoji, `/attach` |
| `emoji.go` | emoji shortcodes: search, completion and expansion |
| `sidebar.go` | the optional sidebar and saved preferences |
| `help.go` | the help overlay with keys and commands |
| `gif.go` | GIPHY search and the `/gif` picker |
| `search.go` | `/find` and jumping to a result |
| `paste.go` | images from the clipboard or dropped files, sent with the next message |
| `nav.go` | older history, live spaces, `/dm`, sections, `/new`, `/rename`, `/invite`, `/leave` |
| `presence.go` | the user's own availability: `/dnd`, `/away`, `/active`, `/status` |
| `drafts.go` | unsent input per space and thread, kept in the cache directory |
| `rendercache.go` | rendered messages and styled blocks kept between renders |
| `cache.go` | spaces kept in memory and followed by live events, snapshots on disk |
| `store.go` | the encrypted on-disk cache for spaces and images |
| `attach.go` | downloading, saving and opening files |
| `image.go` | images and GIFs over the kitty graphics protocol |
| `format.go` | Chat markup, cards, quotes, message bodies |
| `logging.go` | slog setup, levels including trace, the log location |
| `errors.go` | error messages for the status line, returning failed sends to the input |

`scripts/` holds `setup.sh`, the one-time GCP setup for admins, and `install.sh`, the install fallback for machines without Homebrew.

Add a package only when a second consumer needs the code.

## Commands

- `just check` runs what CI runs: format check, golangci-lint, gosec, govulncheck and the tests.
- `just fmt` formats, `just fix` runs `go fix`, `just bench` runs the benchmarks.
- `just` lists every recipe.

Run `just check` before committing.

## Rules

- Keep the code in the style around it, and keep comments to non-obvious rationale in the present tense.
- Every non-trivial pure function gets a table test in the matching `_test.go`.
- User flows get a `teatest` test in `flow_test.go`, against the fake Chat API in `fakechat_test.go`. Checks read the emulated screen, since Bubble Tea only redraws changed cells.
- A rendering change updates the golden files with `just golden`, and the diff is part of the review.
- Add a dependency only when the standard library or an existing dependency can't do the job.
- A `#nosec` or `_ =` needs a comment saying why the input is trusted or the error is safe to drop.
- Log with `slog`, key-value pairs and a constant lowercase message. Failures the user may notice are `warn`, diagnostics `debug`, raw dumps `trace`.
- Content from other people's messages is untrusted:
    - Only open http and https links.
    - Reduce file names with `safeName` and write through an `os.Root`.
    - Mark downloads with the macOS quarantine attribute.
- Message content reaches the client only through the Chat API with the user's own token.
  Workspace Events subscriptions stay on `includeResource=false`.
- New OAuth scopes go in `scopes` in `auth.go` and the README permissions table.
  Changing the set forces every user to log in again.
- User-visible changes update the README in the same commit.

## Commits

- Subjects use the imperative and name the change, such as `Add reaction toggling`.
- The body says what changed and why.
- No co-author lines.
