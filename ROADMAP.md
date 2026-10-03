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

- [ ] **Presence and Do Not Disturb**: `users.availability`: show next to names, set with `/dnd` and `/away`
- [ ] **Local message cache**: instant startup and offline reading
- [ ] **Drafts**: unsent text kept per space and thread, across switches and restarts
- [x] **Copy a message**: OSC 52, works over SSH
- [x] **Open links in messages**: `l` on the selected message, picking with `1`–`9` when there are several
- [ ] **Code blocks**: syntax highlighting with chroma
- [ ] **Emoji**: `:shortcode:` input and a full reaction picker
- [ ] **Configuration**: key bindings and a light theme from a config file
- [x] **Cycling @mention suggestions**: tab moves through the suggestions shown, shift+tab back.
- [x] **`/dm` by short name**: `/dm kn` tries `kn@` the user's own domain first, then a single known member whose address starts with `kn@`, and lists the candidates when several match. Tab completes names and addresses.
- [ ] **Optional sidebar**: unread and recently active spaces, display only. alt+1…9 or a click jumps, search stays in ctrl+k. ctrl+b toggles, hidden below ~100 columns, off by default

## 3. Logging

- [ ] Replace `MUTTER_DEBUG` with `MUTTER_LOG_LEVEL`, defaulting to `warn`
- [ ] Log with `log/slog` in place of `log`, and enable the `sloglint` linter
- [ ] Add a `trace` level below `debug`, since slog has none
- [ ] Write `tty.out` only at `trace`
- [ ] Pick a fixed log location now that no directory is passed in, such as the user's cache directory

## 4. UI tests

Coverage is 17%, mostly pure functions. The Bubble Tea model has the most logic and the fewest tests.

- [ ] `teatest` flows: select, open a thread, reply, react
- [ ] Golden-file tests for messages, cards and quotes

## 5. Rollout to colleagues

- [ ] Per-user Pub/Sub topics, see [Isolating users](README.md#isolating-users). Blocks rollout beyond one team.
- [ ] Lower startup cost: about 1,350 member lookups and a read-state scan per start, against a project-wide quota of 3,000 message reads per minute
- [ ] Token storage fallback for Linux without a Secret Service
- [ ] `?` help overlay with keys and commands

## Out of scope

The Chat API doesn't support these.

- Writing thread read state. The API can read it but not update it.
- Followed threads
- Card buttons that call Chat apps, which need app authentication
- Message search, unverified. If the API has none for users, search can only cover cached history.
