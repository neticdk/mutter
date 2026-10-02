# mutter

Terminal client for Google Chat.

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

## Setup (per user)

There is no setup with a baked-in build. The first run opens a browser for login, and the token is stored in the OS keychain.

To use a different OAuth client, for example during development, write it to `~/Library/Application Support/mutter/config.json` (macOS) or `~/.config/mutter/config.json` (Linux). The file takes precedence over the baked-in client:

```json
{"client_id": "....apps.googleusercontent.com", "client_secret": "...", "topic": "projects/<PROJECT_ID>/topics/mutter-events"}
```

## Usage

```
./mutter
```

| Key | Action |
|---|---|
| enter | send |
| shift+enter, alt+enter, ctrl+j | newline (shift+enter needs a terminal with kitty keyboard protocol, e.g. Ghostty) |
| ctrl+k | switch space |
| pgup, pgdown | scroll |
| ↑, ↓ (empty input) | select a thread, or a message inside a thread |
| `r` `e` `d` `q` `u` `o` `s` (while selecting) | react, edit, delete, quote, mark unread from here, open files, save files |
| esc | cancel editing or quoting, end selection, leave the thread |
| `/open [n]` | open file `n` of the selected or open thread, the last one by default |
| `/save [n]` | save file `n` to `~/Downloads` |
| `/unread` | mark the space unread from the selected or open thread onward |
| `/quit` | quit |
| `/logout` | delete the stored token and quit |

## Debugging

```
MUTTER_DEBUG=/tmp/mutter-debug ./mutter
```

This writes `mutter.log` to the directory, plus three files per image:

- `<key>.orig`: the bytes as downloaded
- `<key>.png`: the re-encoded PNG
- `<key>.kitty`: the escape sequences sent to the terminal, followed by the placeholder cells. `cat` it to replay the image outside the TUI.
