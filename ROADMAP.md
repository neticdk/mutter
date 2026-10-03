# Roadmap

Ordered by priority: make what exists reliable, add daily-driver features, then prepare the rollout to colleagues.

## 1. Verify existing features

Built, but not yet seen working end to end.

- [x] Desktop notifications through OSC 777 in Ghostty, following each space's notification setting
- [x] @mention detection, which assumes the Chat user ID equals the Google account ID
- [x] @mention completion sends real mentions
- [x] Quoting, which fails when the quoted message's `lastUpdateTime` is stale
- [x] Reaction toggling
- [x] Editing messages that have cards or attachments
- [x] Read state synced from other devices
- [x] Creating a new DM with `/dm` through `spaces.setup`
- [ ] Behavior on token expiry, network loss, Pub/Sub outages and API rate limits

## 2. Daily driver

- [x] **Presence and Do Not Disturb**: your own state in the header, set with `/dnd`, `/away`, `/active` and `/status`. The API doesn't return other people's.
- [x] **Local message cache**: spaces in memory for the session, encrypted snapshots and images on disk across restarts. Not yet readable offline.
- [x] **Drafts**: unsent text kept per space and thread, across switches and restarts
- [x] **Copy a message**: OSC 52, works over SSH
- [x] **Open links in messages**: `l` on the selected message, picking with `1`–`9` when there are several
- [x] **Code blocks**: fenced blocks get a left bar and start on their own line. Inline code stays cyan. No syntax highlighting.
- [x] **Emoji**: `:shortcode:` completion with tab, complete codes expanded on send, and a reaction picker that searches all emoji
- [ ] **Configuration**: key bindings and a light theme from a config file
- [x] **Cycling @mention suggestions**: tab moves through the suggestions shown, shift+tab back.
- [x] **`/dm` by short name**: `/dm kn` tries `kn@` the user's own domain first, then a single known member whose address starts with `kn@`, and lists the candidates when several match. Tab completes names and addresses.
- [x] **Multiline paste**: bracketed paste lands in the input whole and never sends a line early. Covered by a test.
- [x] **Pasting images**: ctrl+v reads an image from the system clipboard with `golang.design/x/clipboard`, falling back to text paste. A dropped image path attaches the file. Pending images show before the status line and go out with the next message.
- [x] **Optional sidebar**: unread and recently active spaces, display only. alt+1…9, ctrl+1…9 or a click jumps, search stays in ctrl+k. ctrl+b toggles and is remembered, hidden below 100 columns, off by default.

## 3. Performance

Measure with the API call counts in `mutter.log` at `MUTTER_LOG_LEVEL=debug` before and after each change.

- [x] Count API calls per method
- [x] Refresh cached DM and group-chat titles only when missing or older than 14 days, at most 32 stale per start. Membership events refresh them while running.
- [x] Skip the startup read-state lookup for spaces whose last activity is older than their known read time
- [x] Keep loaded spaces in memory and update them from live events, so switching back costs no fetch
- [x] Cache processed images on disk, so restarts don't download them again
- [x] Keep thread read times in the disk snapshot and skip the lookup for threads with no activity since
- [ ] Cache rendered messages per width, so cursor moves don't reformat every message
- [ ] Skip `messages.get` for events in muted spaces that aren't open

## 4. Logging

- [x] Replace `MUTTER_DEBUG` with `MUTTER_LOG_LEVEL`, defaulting to `warn`
- [x] Log with `log/slog` in place of `log`, and enable the `sloglint` linter
- [x] Add a `trace` level below `debug`, since slog has none
- [x] Write `tty.out` only at `trace`
- [x] Log to `~/Library/Logs/mutter` on macOS and the XDG state directory elsewhere, keeping the previous run's log

## 5. UI tests

Coverage is 17%, mostly pure functions. The Bubble Tea model has the most logic and the fewest tests.

- [ ] `teatest` flows: select, open a thread, reply, react
- [ ] Golden-file tests for messages, cards and quotes

## 6. Rollout to colleagues

- [ ] Per-user Pub/Sub topics, see [Isolating users](README.md#isolating-users). Blocks rollout beyond one team.
- [ ] Token storage fallback for Linux without a Secret Service
- [ ] `?` help overlay with keys and commands

## Out of scope

The Chat API doesn't support these.

- Writing thread read state. The API can read it but not update it.
- Followed threads
- Card buttons that call Chat apps, which need app authentication
- Message search, unverified. If the API has none for users, search can only cover cached history.
