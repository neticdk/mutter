package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/url"

	"golang.org/x/oauth2"
	"google.golang.org/api/chat/v1"
	"google.golang.org/api/googleapi"
)

// describeErr says what went wrong in words for the status line. The log
// keeps the full error.
func describeErr(err error) string {
	if re, ok := errors.AsType[*oauth2.RetrieveError](err); ok && re.ErrorCode == "invalid_grant" {
		return "your login expired or was revoked: /logout, then start mutter to log in again"
	}
	if ge, ok := errors.AsType[*googleapi.Error](err); ok {
		switch {
		case ge.Code == http.StatusTooManyRequests:
			return "Google is rate limiting mutter, try again in a minute"
		case ge.Code == http.StatusUnauthorized:
			return "Google rejected the login: /logout, then start mutter to log in again"
		case ge.Code >= http.StatusInternalServerError:
			return fmt.Sprintf("Google had a problem (%d), try again", ge.Code)
		}
		return fmt.Sprintf("Google refused it (%d): %s", ge.Code, clean(cmp.Or(ge.Message, http.StatusText(ge.Code))))
	}
	_, isURL := errors.AsType[*url.Error](err)
	_, isNet := errors.AsType[net.Error](err)
	if isURL || isNet || errors.Is(err, context.DeadlineExceeded) {
		return "can't reach Google, check the network"
	}
	return err.Error()
}

// sendFailedMsg carries a message that didn't go out, so it isn't lost.
type sendFailedMsg struct {
	err      error
	key      string // draftKey of the context it was written in
	text     string
	mentions map[string]string
	quote    *chat.Message
	editing  *chat.Message
	files    []pending
}

// unsent returns a failed message to the input when its context is still
// open, or keeps it as that context's draft. Files and quotes only survive
// in the input.
func (m *model) unsent(msg sendFailedMsg) {
	slog.Warn("send failed", "err", msg.err)
	why := describeErr(msg.err)
	if m.draftKey() != msg.key {
		if msg.text != "" && msg.editing == nil {
			d := m.drafts[msg.key]
			d.Text = joinLines(msg.text, d.Text)
			d.Mentions = maps.Clone(msg.mentions)
			m.drafts[msg.key] = d
			m.writeDrafts()
			why += " · not sent, kept as a draft where you wrote it"
		} else {
			why += " · not sent"
		}
		m.status = errStyle.Render(why)
		return
	}
	m.ta.SetValue(joinLines(msg.text, m.ta.Value()))
	maps.Copy(m.mentions, msg.mentions)
	m.pending = append(msg.files, m.pending...)
	m.quoting = cmp.Or(m.quoting, msg.quote)
	m.editing = cmp.Or(m.editing, msg.editing)
	m.status = errStyle.Render(why + " · not sent, it's back in the input")
	m.render()
}

// joinLines joins the non-empty texts with newlines.
func joinLines(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "\n" + b
}
