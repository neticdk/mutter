package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"golang.design/x/clipboard"
)

// pending is a file waiting to go out with the next message.
type pending struct {
	name string
	data []byte
}

type pastedMsg pending

// imageExts are the files a pasted or dropped path attaches.
var imageExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".heic": true}

// maxPasteBytes caps an attached image, in line with Chat's upload limit.
const maxPasteBytes = 200 << 20

var clipboardReady = sync.OnceValue(clipboard.Init)

// pasteImage reads an image from the system clipboard. Terminal paste only
// carries text, so this is the only way an image gets in. Without an image
// it falls back to the input's own text paste.
func pasteImage() tea.Msg {
	if err := clipboardReady(); err != nil {
		slog.Warn("clipboard", "err", err)
		return textarea.Paste()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	png, err := clipboard.Read(ctx, clipboard.FmtImage)
	if err != nil || len(png) == 0 {
		slog.Debug("paste: no image, pasting text", "err", err)
		if text, terr := clipboard.Read(ctx, clipboard.FmtText); terr != nil || len(text) == 0 {
			return noticeMsg("nothing to paste: the clipboard has no image or text")
		}
		return textarea.Paste()
	}
	slog.Debug("paste: image", "bytes", len(png))
	return pastedMsg{name: "pasted-" + time.Now().Format("150405") + ".png", data: png}
}

// droppedFile turns pasted text into an image file to attach, when it's a
// single path to one, as dropping a file on the terminal pastes. Terminals
// paste paths plain, quoted, with backslash escapes or as file:// URLs.
func droppedFile(text string) (pending, bool) {
	p := strings.TrimSpace(text)
	if p == "" || strings.ContainsRune(p, '\n') {
		return pending{}, false
	}
	if u, err := url.Parse(p); err == nil && u.Scheme == "file" {
		p = u.Path
	}
	if len(p) > 1 && (p[0] == '\'' || p[0] == '"') && p[len(p)-1] == p[0] {
		p = p[1 : len(p)-1]
	} else {
		p = strings.ReplaceAll(p, `\ `, " ")
	}
	if !filepath.IsAbs(p) || !imageExts[strings.ToLower(filepath.Ext(p))] {
		return pending{}, false
	}
	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxPasteBytes {
		return pending{}, false
	}
	data, err := os.ReadFile(p) // #nosec G304 -- the user dropped this file on the terminal
	if err != nil {
		return pending{}, false
	}
	return pending{name: filepath.Base(p), data: data}, true
}

// sendWith sends text with the pending files. The first file goes with the
// text and quote, and any more follow as their own messages. On a failure,
// failed carries back what didn't go out.
func (m *model) sendWith(space string, out outgoing, files []pending, failed sendFailedMsg) tea.Cmd {
	m.notice = fmt.Sprintf("uploading %d %s…", len(files), plural(len(files), "file", "files"))
	return func() tea.Msg {
		var sent []tea.Msg
		fail := func(i int, err error) tea.Msg {
			failed.err, failed.files = err, files[i:]
			if i > 0 {
				failed.text, failed.quote, failed.mentions = "", nil, nil
			}
			return tea.BatchMsg{func() tea.Msg { return batchMsgs(sent) }, func() tea.Msg { return failed }}
		}
		for i, f := range files {
			ref, err := m.c.uploadData(m.ctx, space, f.name, bytes.NewReader(f.data))
			if err != nil {
				return fail(i, fmt.Errorf("upload %s: %w", f.name, err))
			}
			o := outgoing{thread: out.thread, att: ref}
			if i == 0 {
				o.text, o.quote = out.text, out.quote
			}
			msg, err := m.c.send(m.ctx, space, o)
			if err != nil {
				return fail(i, err)
			}
			sent = append(sent, sentMsg(msg))
		}
		if len(sent) == 0 {
			return errMsg(errors.New("nothing to send"))
		}
		return batchMsgs(sent)
	}
}

// batchMsgs delivers several messages from one command.
func batchMsgs(msgs []tea.Msg) tea.Msg {
	cmds := make([]tea.Cmd, len(msgs))
	for i, msg := range msgs {
		cmds[i] = func() tea.Msg { return msg }
	}
	return tea.BatchMsg(cmds)
}

// pendingLabel lists the files waiting to be sent, for the status line.
func (m *model) pendingLabel() string {
	if len(m.pending) == 0 {
		return ""
	}
	names := make([]string, 0, len(m.pending))
	for _, p := range m.pending {
		names = append(names, p.name)
	}
	return "📎 " + clean(strings.Join(names, ", ")) + dimStyle.Render(" · ctrl+x removes · ")
}
