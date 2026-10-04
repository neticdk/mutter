# Ideas

Things considered but not planned. [ROADMAP.md](ROADMAP.md) holds what is planned. An idea moves there once someone decides to build it.

## Later

- **tmux support**: wrap kitty image sequences and OSC 777 in tmux passthrough (`ESC P tmux; … ESC \`, ESC doubled), which needs `allow-passthrough on` in tmux 3.3 or newer. Open questions:
    - Detecting the outer terminal, since `TERM_PROGRAM` is `tmux` inside it. Candidates: `tmux display -p '#{client_termname}'`, inherited Ghostty variables, a `MUTTER_IMAGES` override.
    - Whether tmux reports pixel sizes through `TIOCGWINSZ`. If not, query the outer terminal with `CSI 16 t`.
- **Configuration**: key bindings and a light theme from a config file
- **Token storage fallback** for Linux without a Secret Service

## Dropped

- **`mutter send`**: posting from scripts with the user's login
- **Export**: a thread or space to Markdown
- **Keyword notifications**: local notifications on chosen words, even in muted spaces

## Blocked by the Chat API

- Writing thread read state. The API can read it but not update it.
- Followed threads
- Card buttons that call Chat apps, which need app authentication
- Other people's presence. The API only returns the user's own availability.
- Typing indicators and read receipts
- Polls
