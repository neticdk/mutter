package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/api/chat/v1"
)

// The API reads and sets only the user's own availability, not other
// people's.
const availabilityName = "users/me/availability"

const (
	defaultDND      = time.Hour
	defaultEmoji    = "💬"
	maxStatusLength = 64
)

type presenceMsg struct{ a *chat.Availability }

// loadPresence fetches the user's state. The header label is only a hint,
// so a failure is logged, not shown.
func (m model) loadPresence() tea.Msg {
	a, err := m.c.svc.Users.Availability.Get(availabilityName).Context(m.ctx).Do()
	if err != nil {
		slog.Warn("availability", "err", err)
		return nil
	}
	return presenceMsg{a}
}

// presenceCmd runs /dnd, /away, /active or /status, then reloads the state.
func (m *model) presenceCmd(fields []string, text string) tea.Cmd {
	var do func(ctx context.Context) error
	av := m.c.svc.Users.Availability
	switch fields[0] {
	case "/dnd":
		d, err := durationArg(fields, defaultDND)
		if err != nil {
			m.notice = err.Error()
			return nil
		}
		do = func(ctx context.Context) error {
			_, err := av.MarkAsDoNotDisturb(availabilityName, &chat.MarkAsDoNotDisturbRequest{Ttl: ttl(d)}).Context(ctx).Do()
			return err
		}
	case "/away":
		do = func(ctx context.Context) error {
			_, err := av.MarkAsAway(availabilityName, &chat.MarkAsAwayRequest{}).Context(ctx).Do()
			return err
		}
	case "/active":
		d, err := durationArg(fields, 0)
		if err != nil {
			m.notice = err.Error()
			return nil
		}
		req := &chat.MarkAsActiveRequest{}
		if d > 0 {
			req.Ttl = ttl(d)
		}
		do = func(ctx context.Context) error {
			_, err := av.MarkAsActive(availabilityName, req).Context(ctx).Do()
			return err
		}
	case "/status":
		a := &chat.Availability{NullFields: []string{"CustomStatus"}}
		if emoji, msg := parseStatus(strings.TrimSpace(strings.TrimPrefix(text, "/status"))); msg != "" {
			if utf8.RuneCountInString(msg) > maxStatusLength {
				m.notice = fmt.Sprintf("a status can be at most %d characters", maxStatusLength)
				return nil
			}
			a = &chat.Availability{CustomStatus: &chat.CustomStatus{Emoji: &chat.Emoji{Unicode: emoji}, Text: msg}}
		}
		do = func(ctx context.Context) error {
			_, err := av.Patch(availabilityName, a).UpdateMask("customStatus").Context(ctx).Do()
			return err
		}
	default:
		return nil
	}
	return func() tea.Msg {
		if err := do(m.ctx); err != nil {
			return errMsg(fmt.Errorf("%s: %w", fields[0], err))
		}
		return m.loadPresence()
	}
}

// durationArg parses the optional duration after a command, such as 30m or
// 2h.
func durationArg(fields []string, def time.Duration) (time.Duration, error) {
	if len(fields) < 2 {
		return def, nil
	}
	d, err := time.ParseDuration(fields[1])
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s needs a duration such as 30m or 2h", fields[0])
	}
	return d, nil
}

func ttl(d time.Duration) string { return fmt.Sprintf("%ds", int(d.Seconds())) }

// parseStatus splits "/status" text into its emoji and message. The API
// requires an emoji, so a message without one gets defaultEmoji.
func parseStatus(s string) (emoji, msg string) {
	if s == "" {
		return "", ""
	}
	first, rest, _ := strings.Cut(s, " ")
	if r, _ := utf8.DecodeRuneInString(first); r >= 0x80 && !unicode.IsLetter(r) && !unicode.IsNumber(r) {
		return first, strings.TrimSpace(rest)
	}
	return defaultEmoji, s
}

// presenceLabel renders the user's state for the header: Do Not Disturb,
// away and a custom status. Being active is the norm, so it shows nothing.
func presenceLabel(a *chat.Availability) string {
	if a == nil {
		return ""
	}
	var parts []string
	switch a.State {
	case "DO_NOT_DISTURB":
		dnd := "DND"
		if md := a.DoNotDisturbMetadata; md != nil && md.ExpirationTime != "" {
			dnd += " until " + when(md.ExpirationTime)
		}
		parts = append(parts, errStyle.Render(dnd))
	case "AWAY":
		parts = append(parts, dimStyle.Render("away"))
	}
	if cs := a.CustomStatus; cs != nil && cs.Text != "" {
		e := ""
		if cs.Emoji != nil {
			e = clean(cs.Emoji.Unicode) + " "
		}
		parts = append(parts, dimStyle.Render(e+clean(cs.Text)))
	}
	return strings.Join(parts, " ")
}
