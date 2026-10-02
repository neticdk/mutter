# mutter

Terminal client for Google Chat, for Workspace organizations.

## Features

- Spaces, DMs and group chats, with a fuzzy switcher that lists unread spaces first and shows web-client sidebar sections
- Threads collapsed to their root, with reply counts, opened in their own view
- Live updates through the Workspace Events API and Pub/Sub
- Unread state synced with the web client, muted spaces respected
- Desktop notifications following each space's notification setting
- Reactions, edits, deletes, quotes and @mention completion
- Cards rendered as text, images and animated GIFs drawn in the terminal
- File upload, download and open

## Setup (once per organization)

An admin does this once. Users then only log in.

### Prerequisites

- A GCP project inside your Workspace organization, and the Owner role on it.
- A Google group containing everyone who will use mutter.
- `gcloud` logged in as that owner (`gcloud auth login`).
- Go 1.27 or newer (see `go.mod`).

### 1. Run the setup script

```
./setup.sh PROJECT_ID USERS_GROUP_EMAIL
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
| `TAP_APP_PRIVATE_KEY` | private key of a GitHub App installed on `neticdk/homebrew-tap` with Contents and Pull requests write access |

The App ID goes in the repository variable `TAP_APP_ID`. Each release mints a token from the app that lasts an hour and reaches only the tap.

```
git tag v0.1.0 && git push origin v0.1.0
```

Each release opens a pull request on [`neticdk/homebrew-tap`][tap] that updates the `mutter` cask. Merging it publishes the release to Homebrew.

Users install with Homebrew. The tap is internal to the organization, so git needs GitHub credentials, which `gh auth setup-git` provides:

```
gh auth setup-git
brew install neticdk/tap/mutter
```

Without Homebrew, `install.sh` downloads the release with `gh`:

```
gh api repos/neticdk/mutter/contents/install.sh -H 'Accept: application/vnd.github.raw' | bash
```

[tap]: https://github.com/neticdk/homebrew-tap

`install.sh` downloads the archive for the machine, checks it against `checksums.txt`, and installs to `~/.local/bin`, or `$BINDIR` when set. Pass a tag to pin a version. The cask clears the macOS quarantine attribute after installing, and `gh` never sets it, so the unsigned binary runs without Gatekeeper prompts. A binary downloaded through a browser is blocked until it's signed and notarized.

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

To use a different OAuth client, for example during development, write it to `~/Library/Application Support/mutter/config.json` (macOS) or `~/.config/mutter/config.json` (Linux). The file takes precedence over the baked-in client:

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
| shift+enter, alt+enter, ctrl+j | newline |
| ctrl+k | switch space. The filter matches names and sidebar sections. |
| ↑ ↓ with an empty input | select a thread, or a message inside a thread |
| ↑ on the oldest thread | load older history |
| pgup, pgdown | scroll |
| tab after `@name` | complete an @mention, so the person is notified |
| esc | cancel editing or quoting, end selection, leave the thread |

While a thread or message is selected, letter keys act on it. Any other key goes to the input.

| Key | Action |
|---|---|
| `r` | react: `1`–`6` pick an emoji, picking it again removes it |
| `e` | edit your own message |
| `d` | delete your own message, confirmed with `y` |
| `q` | quote it in your next message |
| `u` | mark the space unread from this message onward |
| `o` | open its files |
| `s` | save its files to `~/Downloads` |

### Commands

| Command | Action |
|---|---|
| `/dm NAME` or `/dm EMAIL` | open or start a DM. Names match members of spaces opened this session. |
| `/attach PATH [text]` | upload a file into the open thread, or as a new thread. Quote paths with spaces. |
| `/open [n]` | open file `n` of the selected or open thread, the last one by default |
| `/save [n]` | save file `n` to `~/Downloads` |
| `/unread` | mark the space unread from the selected or open thread onward |
| `/logout` | delete the stored token and quit |
| `/quit` | quit |

Saved files get the macOS quarantine attribute, so Gatekeeper checks them before they run. `/open` only follows http and https links.

### Permissions

The first run asks the user to grant these OAuth scopes. mutter asks again whenever the set changes.

| Scope | Used for |
|---|---|
| `openid`, `email` | identifying the user |
| `chat.spaces.readonly` | listing spaces |
| `chat.spaces.create` | starting DMs with `/dm` |
| `chat.messages` | reading, sending, editing and deleting messages, reactions, attachments |
| `chat.memberships.readonly` | DM titles and @mention completion |
| `chat.users.readstate` | unread state |
| `chat.users.spacesettings` | notification and mute settings |
| `chat.users.sections.readonly` | sidebar sections |
| `pubsub` | pulling live events |

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
| `just snapshot` | build all release targets into `dist/` |

`just` alone lists every recipe.

## Debugging

```
MUTTER_DEBUG=/tmp/mutter-debug ./mutter
```

This writes to the directory:

- `mutter.log`: API errors, raw spaces, memberships and events, image decoding
- `tty.out`: every byte sent to the terminal. It grows fast while GIFs animate, so keep runs short.

## License

MIT, see [LICENSE](LICENSE).
