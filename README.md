# mutter

Terminal client for Google Chat, for Workspace organizations.

## Features

- Spaces, DMs and group chats, with a fuzzy switcher that lists unread spaces first and shows web-client sidebar sections
- Threads collapsed to their root, with reply counts, opened in their own view
- Live updates through the Workspace Events API and Pub/Sub
- Unread state for spaces and threads synced from the web client and phone, muted spaces respected
- Your Do Not Disturb, away state and status follow changes made on other devices
- Desktop notifications following each space's notification setting, with a bell that marks the terminal tab, and the unread count in the tab title
- Reactions, edits, deletes, quotes, and completion for @mentions and `:emoji:`, including the organization's custom emoji
- Do Not Disturb, away and status from the input line
- Drafts kept per space and thread, across restarts
- Spaces and images cached locally and encrypted, so switching spaces and restarting are fast
- Cards rendered as text, images and animated GIFs drawn in the terminal, and Drive, Meet and Calendar links labeled
- Search across all spaces with `/find`
- Starts without network from the local cache, and catches up once Google is reachable
- File upload, download and open, and images pasted from the clipboard or dropped on the terminal
- GIF search through GIPHY with `/gif`, when you set your own API key

## Setup (once per organization)

An admin does this once. Users then only log in.

### Prerequisites

- A GCP project inside your Workspace organization, and the Owner role on it.
- A Google group containing everyone who will use mutter.
- `gcloud` logged in as that owner (`gcloud auth login`).
- Go 1.27 or newer (see `go.mod`).

### 1. Run the setup script

```
./scripts/setup.sh PROJECT_ID USERS_GROUP_EMAIL
```

The script does the following, and it is safe to re-run:

- enables the Chat, Workspace Events and Pub/Sub APIs
- creates the Pub/Sub topic `mutter-events`
- lets Google Chat publish to the topic (`chat-api-push@system.gserviceaccount.com` gets Pub/Sub Publisher)
- lets the group create Pub/Sub subscriptions (`roles/pubsub.editor` on the project)

It finishes by printing direct links for the three console steps below.

`roles/pubsub.editor` covers the whole project, which exposes event metadata between users. See [Isolating users](#isolating-users).

### 2. Configure the Chat app

The Chat API rejects calls until the project has a Chat app configuration, even though mutter only acts as the user.

1. Open *APIs & Services → Google Chat API → Configuration*.
2. Set an app name (`mutter`), an avatar URL and a description.
3. Turn off *Interactive features*. mutter never receives events as a bot.
4. Save.

### 3. Configure OAuth branding

1. Open *Google Auth Platform → Branding*.
2. Set the app name (`mutter`) and the support email.
3. Under *Audience*, choose **Internal**. That limits logins to your organization and skips Google's app verification.

### 4. Create the OAuth client

1. Open *Google Auth Platform → Clients → Create client*.
2. Choose application type **Desktop app** and name it `mutter`.
3. Copy the client ID and client secret.

### 5. Build and distribute

Bake the client into the binary. Desktop client secrets are not confidential, because Google treats installed apps as public clients.

```
go build -ldflags "-X main.clientID=<ID> -X main.clientSecret=<SECRET> -X main.topic=projects/<PROJECT_ID>/topics/mutter-events" -o mutter .
```

Hand out the resulting `mutter` binary. Without `main.topic`, mutter runs without live updates.

On start, mutter subscribes the user to events from all their spaces through the Workspace Events API, delivered to the topic. Each machine pulls from its own filtered Pub/Sub subscription, which deletes itself after 31 days unused.

### 6. Publish releases

Releases build in GitHub Actions when a `v*` tag is pushed, with GoReleaser (`.goreleaser.yaml`, `.github/workflows/release.yml`). They cover macOS and Linux on amd64 and arm64, with the OAuth client and topic baked in from repository secrets:

| Secret | Value |
|---|---|
| `MUTTER_CLIENT_ID` | the Desktop OAuth client ID |
| `MUTTER_CLIENT_SECRET` | its client secret |
| `MUTTER_TOPIC` | `projects/<PROJECT_ID>/topics/mutter-events` |
| `TAP_APP_PRIVATE_KEY` | private key of a GitHub App installed on `neticdk/netic-homebrew-tap` with Contents and Pull requests write access |

The app's Client ID goes in the repository variable `TAP_APP_CLIENT_ID`. Each release mints a token from the app that lasts an hour and reaches only the tap.

```
git tag v0.1.0 && git push origin v0.1.0
```

Each release opens a pull request on [`neticdk/netic-homebrew-tap`][tap] that updates the `mutter` cask. Merging it publishes the release to Homebrew.

Users install with Homebrew. The tap is internal to the organization, so git needs GitHub credentials, which `gh auth setup-git` provides:

```
gh auth setup-git
brew tap neticdk/tap https://github.com/neticdk/netic-homebrew-tap
brew install neticdk/tap/mutter
```

The tap needs its URL because its repository name doesn't start with `homebrew-`.

Without Homebrew, `scripts/install.sh` downloads the release with `gh`:

```
gh api repos/neticdk/mutter/contents/scripts/install.sh -H 'Accept: application/vnd.github.raw' | bash
```

[tap]: https://github.com/neticdk/netic-homebrew-tap

The script downloads the archive for the machine, checks it against `checksums.txt`, and installs to `~/.local/bin`, or `$BINDIR` when set. Pass a tag to pin a version. The cask clears the macOS quarantine attribute after installing, and `gh` never sets it, so the unsigned binary runs without Gatekeeper prompts. A binary downloaded through a browser is blocked until it's signed and notarized.

### Isolating users

**Status: planned.** mutter currently uses one shared topic, as set up above.

#### Exposure today

- All users' events go to the shared `mutter-events` topic. Each client filters for its own events with a per-machine subscription.
- A filter only limits what its own subscription receives. Anyone who can attach a subscription to the topic receives every event on it.
- `roles/pubsub.editor` lets every group member attach one.
- Events carry no message content, because mutter subscribes with `includeResource=false`. Reading a message still needs the reader's own Chat access.
- What leaks is activity metadata: space and message IDs, timestamps, and read-state changes, meaning who reads which space and when.

#### Recommended setup

Per-user topics in the shared project, with resources an admin creates:

| Resource | Per | Notes |
|---|---|---|
| Topic `mutter-<user>` | user | `chat-api-push@system.gserviceaccount.com` has Pub/Sub Publisher on it |
| Subscription `mutter-<user>` | user | the user has `roles/pubsub.subscriber` on this subscription only |
| Project-wide Pub/Sub role | nobody | users can't attach to topics they weren't given |

- The setup script creates these for every member of the group, and is re-run when people join.
- mutter derives the topic and subscription names from the user's email and stops creating subscriptions itself.
- One subscription per user means two machines running mutter at once split the events between them. mutter should detect this and warn.

#### Alternatives considered

| Option | Isolation | Cost |
|---|---|---|
| Shared topic (current) | metadata visible within the group | none |
| Topic per user in a shared project (recommended) | full | admin-created topics and subscriptions |
| Project per user | full | each user needs a billing-enabled project they own, and runs setup themselves |

## Setup (per user)

There is no setup with a baked-in build. The first run opens a browser for login, and the token is stored in the OS keychain. On Linux, the keychain is the Secret Service over D-Bus, such as GNOME Keyring or KeePassXC. Without one, mutter can't store the token.

To use a different OAuth client, for example during development, write it to `~/Library/Application Support/mutter/config.json` (macOS) or `~/.config/mutter/config.json` (Linux). The file takes precedence over the baked-in client. A file with only `topic` keeps the baked-in client:

```json
{"client_id": "....apps.googleusercontent.com", "client_secret": "...", "topic": "projects/<PROJECT_ID>/topics/mutter-events"}
```

## Usage

```
./mutter
```

### Terminal support

| Feature | Needs |
|---|---|
| Images and animated GIFs | kitty graphics protocol with Unicode placeholders, as in Ghostty and kitty. Other terminals show `[n · name]`. |
| Desktop notifications | OSC 777, as in Ghostty |
| shift+enter for newline | kitty keyboard protocol. alt+enter and ctrl+j work everywhere. |

### Keys

| Key | Action |
|---|---|
| enter | send, or open the selected thread when the input is empty |
| shift+enter, alt+enter, ctrl+j | newline. Pasted text keeps its newlines and never sends early. |
| ctrl+v | paste an image from the clipboard to send with the next message, or text when there's no image. cmd+v belongs to the terminal, which pastes text and file paths but never images. |
| ctrl+x | remove the last image waiting to be sent |
| ctrl+b | show or hide the sidebar of unread and recent spaces. It hides itself in windows narrower than 100 columns. |
| alt+1…9, ctrl+1…9 | open a sidebar entry. alt needs option-as-alt on macOS, ctrl works without it. |
| ctrl+n | open the next thread with new messages in the open space, oldest first. With none left, open the next unread space, in the switcher's order. |
| ctrl+k | switch space. The filter matches names and sidebar sections, and `✎` marks spaces with a draft. |
| ↑ ↓ with an empty input | select a thread, or a message inside a thread |
| ↑ on the oldest thread | load older history |
| pgup, pgdown | scroll |
| tab, shift+tab | complete an @mention, a `/dm` argument or a `:shortcode`. Repeated presses cycle through the suggestions. |
| esc | cancel editing or quoting, end selection, leave the thread |
| F1 | show keys and commands, as `/help` does. Any key closes it, and typed characters go to the input. |

While a thread or message is selected, letter keys act on it. Any other key goes to the input.

| Key | Action |
|---|---|
| `r` | react: `1`–`6` pick a quick reaction, or type a name to search all emoji, your organization's custom emoji first, tab to move, enter to pick. Picking one again removes it. |
| `e` | edit your own message |
| `d` | delete your own message, confirmed with `y` |
| `q` | quote it in your next message |
| `y` | copy its text to the clipboard, over OSC 52 |
| `l` | open a link in it. With several, pick one with `1`–`9`. |
| `c` | copy a link to it in the web client |
| `b` | open it in the web client, for what mutter can't do, such as calls, polls and card buttons |
| `v` | view its first image or GIF at the size of the message pane. Clicking an image does the same. Any key or click closes it. |
| `u` | mark the space unread from this message onward |
| `o` | open its files |
| `s` | save its files to `~/Downloads` |

While the sidebar or images show, the terminal passes clicks to mutter: clicking a sidebar entry opens it, clicking an image views it, and the scroll wheel scrolls messages. Select text with shift held.

Dropping an image file on the terminal pastes its path, and mutter attaches the file in place of the path.

### Commands

| Command | Action |
|---|---|
| `/find QUERY` | search messages in every space you're in, newest first. ↑ ↓ pick, enter opens the thread with the message selected. Plain words search text, and the [API's filters](https://developers.google.com/workspace/chat/api/reference/rest/v1/spaces.messages/search) work too, such as `has_link()`, `is_unread()` or `sender.name = "users/kn@example.com"`. |
| `/mentions` | recent messages that mention you across all spaces, unread first and marked `●`. Enter opens the thread. Mentions read in mutter count as read even though the API can't learn it. |
| `/dm WHO` | open or start a DM. `WHO` is an email address, a short name such as `kn` tried at your own domain first, or part of a name. |
| `/new NAME` | create a space with only you in it, and open it |
| `/rename NAME` | rename the open space |
| `/invite WHO` | add someone to the open space. `WHO` works as in `/dm`. |
| `/leave` | leave the open space, confirmed with `y` |
| `/mute`, `/unmute` | mute or unmute the open space, as in the web client. A muted space never notifies and doesn't count as unread. |
| `/attach PATH [text]` | upload a file into the open thread, or as a new thread. Quote paths with spaces. |
| `/open [n]` | open file `n` of the selected or open thread, the last one by default |
| `/save [n]` | save file `n` to `~/Downloads` |
| `/read` | mark the open space and its threads read |
| `/read all` | mark every unread space read |
| `/unread` | mark the space unread from the selected or open thread onward |
| `/dnd [DURATION]` | Do Not Disturb, for an hour or a duration such as `30m` or `2h` |
| `/away` | show as away until you're active again |
| `/active [DURATION]` | show as active, back to activity-based after `DURATION` |
| `/status [EMOJI] [TEXT]` | set your status, with 💬 when no emoji is given. Without text, it's cleared. |
| `/gif QUERY` | search GIPHY and pick a GIF with ← → or tab. Enter attaches it to your next message. Needs `GIPHY_API_KEY`, see [GIFs](#gifs). |
| `/web` | open the space, or the open thread, in the web client |
| `/help` | show keys and commands |
| `/logout` | delete the stored token and the local cache, and quit |
| `/quit` | quit |

Saved files get the macOS quarantine attribute, so Gatekeeper checks them before they run. Leading dots are stripped from their names, so a file can't land as a hidden dotfile. `/save` creates `~/Downloads` when it's missing. `/open` downloads into mutter's directory in the user cache dir. `/open` only follows http and https links, and only http and https links in cards are clickable.

### GIFs

`/gif` uses your own GIPHY API key, so mutter ships without one and you accept GIPHY's terms yourself:

1. Create an app at [developers.giphy.com](https://developers.giphy.com) and copy its API key.
2. Set it in your shell profile, for example `set -Ux GIPHY_API_KEY <key>` in fish or `export GIPHY_API_KEY=<key>` in bash and zsh.

Each search is one API request, and a new key allows 100 an hour. Previews and the GIF you send come from GIPHY's media servers and don't count. The picked GIF is downloaded and sent as an uploaded image, since the Chat API can't post GIFs the way the web client's picker does. Searches use the `pg` rating.

### Local cache

mutter keeps spaces and processed images in the user cache directory, `~/Library/Caches/mutter/store` on macOS:

- **Encryption**: files are encrypted with AES-GCM. The key is in the OS keychain next to the login token, so a copied cache file can't be read.
- **Size**: spaces are capped at 50 MB and images at 200 MB. The least recently used are removed at startup.
- **Freshness**: while live updates run, a space opened earlier in the session shows from memory with no fetch. After a restart, a space shows its cached copy, marked `refreshing…`, until the fresh load replaces it.
- **Offline**: when Google can't be reached at startup, mutter starts with the cached space list and shows each space's cached copy. Once live updates connect, the list and the open space reload. Your identity is kept unencrypted in `identity.json` next to the cache.
- **Removal**: `/logout` deletes the cache and its key.

### Permissions

The first run asks the user to grant these OAuth scopes. mutter asks again whenever the set changes.

| Scope | Used for |
|---|---|
| `openid`, `email` | identifying the user |
| `chat.spaces` | listing and renaming spaces |
| `chat.spaces.create` | starting DMs with `/dm` |
| `chat.messages` | reading, sending, editing and deleting messages, reactions, attachments |
| `chat.memberships` | DM titles, @mention completion, `/invite` and `/leave` |
| `chat.users.readstate` | unread state |
| `chat.users.spacesettings` | notification and mute settings, `/mute` and `/unmute` |
| `chat.users.sections.readonly` | sidebar sections |
| `chat.users.availability` | your Do Not Disturb, away state and status |
| `chat.customemojis.readonly` | custom emoji in completion, the reaction picker, reactions and messages |
| `pubsub` | pulling live events |

## Limitations

Planned improvements are in [ROADMAP.md](ROADMAP.md). The ones under Chat API can't be fixed in mutter.

### Chat API

- Opening a thread clears its markers only in mutter. The API can read a thread's read state but not write it. Reading a thread in the web client or on your phone does clear it in mutter.
- `FOR_YOU` notifications fire on @mentions only. The API doesn't expose which threads you follow.
- Card buttons that call Chat apps don't work, and interactive card widgets are skipped. Both need app authentication.
- Other people's presence isn't shown. The API only returns the user's own availability.

### Reading

- Opening a space marks all of it read, as the web client does, however far you scroll.
- At startup, spaces with no activity for 30 days count as read.
- History loads 200 messages at a time.
- Without live updates, every space switch fetches again, since nothing keeps the cached copy current.
- There's no message search.
- DMs and group chats whose other members have all left the organization are hidden.
- Only one account at a time.

### Writing

- A message that fails to send goes back into the input with its images, quote and mentions. If you switched space or thread meanwhile, it becomes that thread's draft, and its images and quote are dropped.
- An @mention notifies only when completed with tab. A typed `@name` stays plain text.
- Editing a message sends its mentions back as plain text.
- Several pasted images go out as one message each, with the text on the first.
- Pasted text has its tabs turned into spaces.
- `/dm` matches names and completes only members of the spaces opened since startup. Short names at your own domain and full email addresses work for anyone.

### Images and files

- Images need a terminal with kitty graphics and Unicode placeholders, such as Ghostty or kitty.
- WebP images and images inside cards show as text.
- GIFs play their first 150 frames.
- Images keep their size when the window is resized, until mutter restarts.
- Drive files and GIFs open in the browser. `/save` can't download them without a Drive scope.

### Startup and live updates

- The first start takes about 30 seconds to resolve DM and group-chat names. Later starts use a cache.
- Live updates need a Pub/Sub topic. Without one, mutter only refreshes when a space is opened.
- On the shared topic, group members can see each other's event metadata. See [Isolating users](#isolating-users).

### Platforms

- macOS and Linux only.
- On Linux, storing the login token needs a Secret Service, such as GNOME Keyring or KeePassXC.
- The macOS binary isn't signed. Homebrew and `scripts/install.sh` install it without Gatekeeper prompts, but a browser download gets blocked.

## Development

Needs Go, [just](https://github.com/casey/just) and golangci-lint. gosec and govulncheck run with `go tool`, pinned in `go.mod`. `AGENTS.md` describes the code layout and the rules for changing it.

| Recipe | Does |
|---|---|
| `just check` | format check, lint, gosec, govulncheck and tests, as CI runs them |
| `just cover` | print total test coverage and write `coverage.html` |
| `just build` | build `./mutter`, baking in `MUTTER_CLIENT_ID`, `MUTTER_CLIENT_SECRET` and `MUTTER_TOPIC` when set |
| `just run ARGS` | build and run |
| `just fmt` | format with gofumpt and goimports |
| `just fix` | modernize the code with `go fix` |
| `just golden` | rewrite the golden render files after an intended change |
| `just bench` | run the benchmarks, such as rendering a 200-thread space |
| `just snapshot` | build all release targets into `dist/` |

`just` alone lists every recipe.

Planned work is in [ROADMAP.md](ROADMAP.md), and ideas that aren't planned are in [IDEAS.md](IDEAS.md).

## Logging

mutter logs to `~/Library/Logs/mutter/mutter.log` on macOS, and to `$XDG_STATE_HOME/mutter/mutter.log`, usually `~/.local/state/mutter/mutter.log`, elsewhere. Each start keeps the previous run's log as `mutter.log.1`.

`MUTTER_LOG_LEVEL` sets how much goes in, `warn` by default:

| Level | Adds |
|---|---|
| `error` | errors shown in the status line |
| `warn` | failures the user may notice, such as cache writes or API calls in the background |
| `info` | login, live-update subscriptions |
| `debug` | each event, image processing, paste steps, and at exit the API calls per method |
| `trace` | raw API responses, and `tty.out` next to the log with every byte sent to the terminal. `tty.out` grows fast while GIFs animate, so keep runs short. |

```
MUTTER_LOG_LEVEL=debug ./mutter
```

## License

MIT, see [LICENSE](LICENSE).
