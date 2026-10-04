package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"google.golang.org/api/chat/v1"
)

const (
	modeFind = "find"
	// maxSearchResults is one page of the search API.
	maxSearchResults = 100
)

type searchMsg struct {
	query   string
	results []*chat.Message
}

// findCmd runs /find QUERY across every space the user is in. The query
// goes to the API as typed, so its filters work too, such as
// sender.name = "users/kn@example.com" or has_link().
func (m *model) findCmd(q string) tea.Cmd {
	q = strings.TrimSpace(q)
	if q == "" {
		m.notice = "usage: /find QUERY"
		return nil
	}
	m.notice = "searching…"
	ctx, c := m.ctx, m.c
	return func() tea.Msg {
		r, err := c.svc.Spaces.Messages.Search("spaces/-", &chat.SearchMessagesRequest{Filter: q, PageSize: maxSearchResults}).Context(ctx).Do()
		if err != nil {
			return errMsg(fmt.Errorf("search: %w", err))
		}
		out := make([]*chat.Message, 0, len(r.Results))
		for _, x := range r.Results {
			if x.Message != nil {
				out = append(out, x.Message)
			}
		}
		return searchMsg{q, out}
	}
}

func (m *model) showResults(msg searchMsg) {
	if len(msg.results) == 0 {
		m.notice = "no messages match " + msg.query
		return
	}
	m.notice = ""
	m.mode, m.found, m.foundIdx = modeFind, msg.results, 0
}

// updateFind handles the results: arrows move, enter opens the message's
// thread, anything else closes the list.
func (m model) updateFind(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case keyUp:
		m.foundIdx = max(0, m.foundIdx-1)
		return m, nil
	case keyDown:
		m.foundIdx = min(len(m.found)-1, m.foundIdx+1)
		return m, nil
	case keyEnter:
		hit := m.found[m.foundIdx]
		m.mode, m.found = "", nil
		return m, m.gotoMessage(hit.Name)
	}
	m.mode, m.found = "", nil
	return m, nil
}

// gotoMessage opens the space holding the message name and, once its
// messages are in, the thread with the message selected.
func (m *model) gotoMessage(name string) tea.Cmd {
	i := m.spaceIndex(spaceOf(name))
	if i < 0 {
		m.notice = "that space isn't in your list"
		return nil
	}
	m.gotoMsg = name
	var cmd tea.Cmd
	if i != m.cur {
		cmd = m.open(i)
	}
	if !m.loading {
		m.finishGoto()
	}
	return cmd
}

// finishGoto opens the thread holding gotoMsg, when the loaded history has
// it.
func (m *model) finishGoto() {
	name := m.gotoMsg
	if name == "" {
		return
	}
	m.gotoMsg = ""
	if m.cur < 0 || spaceOf(name) != m.spaces[m.cur].name {
		return // the user went elsewhere meanwhile
	}
	ti, mi, ok := m.find(name)
	if !ok {
		m.notice = "the message is older than the loaded history: ↑ on the oldest thread loads more, or /web opens the space"
		return
	}
	m.cursor = ti
	m.setThread(m.threads[ti])
	m.msgCursor, m.selecting = mi, true
	m.render()
}

// findView lists the results with their space, sender, time and first
// line, scrolled to keep the selected one in view.
func (m *model) findView(height int) string {
	rows := max(1, height-2)
	start := min(max(0, m.foundIdx-rows/2), max(0, len(m.found)-rows))
	lines := []string{boldStyle.Render(fmt.Sprintf("%d %s", len(m.found), plural(len(m.found), "match", "matches"))), ""}
	for i := start; i < min(len(m.found), start+rows); i++ {
		msg := m.found[i]
		where := spaceOf(msg.Name)
		if j := m.spaceIndex(where); j >= 0 {
			where = m.spaces[j].title
		}
		text, _, _ := strings.Cut(clean(customEmojiNames(msg)), "\n")
		head := fmt.Sprintf("%s · %s · %s", truncate(clean(where), 24), truncate(senderName(msg), 20), when(msg.CreateTime))
		line := truncate(head+"  "+dimStyle.Render(text), m.width-2)
		if i == m.foundIdx {
			line = selStyle.Render("▶ ") + line
		} else {
			line = "  " + line
		}
		lines = append(lines, line)
	}
	return lipgloss.NewStyle().Width(m.width).Height(height).MaxHeight(height).Render(strings.Join(lines, "\n"))
}
