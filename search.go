package main

import (
	"fmt"
	"slices"
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

// mentionsFilter finds messages that mention the user. Read state is
// marked per result instead of filtered with is_unread(), since the API
// can't learn about threads read in mutter.
const mentionsFilter = "annotations.user_mentions.user.name:users/me"

type searchMsg struct {
	title   string // what the list is, such as "mentions"
	empty   string // the notice when nothing matched
	results []hit
}

// hit is a search result. unread is the server's read state, which
// showResults corrects with what mutter read itself.
type hit struct {
	msg    *chat.Message
	unread bool
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
	return m.search(q, "matches", "no messages match "+q)
}

// mentionsCmd runs /mentions: recent messages that mention the user,
// unread first.
func (m *model) mentionsCmd() tea.Cmd {
	return m.search(mentionsFilter, "mentions", "no mentions of you")
}

// search runs filter across every space. The full view carries each
// result's read state.
func (m *model) search(filter, title, empty string) tea.Cmd {
	m.notice = "searching…"
	ctx, c := m.ctx, m.c
	return func() tea.Msg {
		r, err := c.svc.Spaces.Messages.Search("spaces/-", &chat.SearchMessagesRequest{Filter: filter, PageSize: maxSearchResults, View: "SEARCH_MESSAGES_VIEW_FULL"}).Context(ctx).Do()
		if err != nil {
			return errMsg(fmt.Errorf("search: %w", err))
		}
		out := make([]hit, 0, len(r.Results))
		for _, x := range r.Results {
			if x.Message != nil {
				out = append(out, hit{x.Message, !x.Read})
			}
		}
		return searchMsg{title, empty, out}
	}
}

// readHere reports whether mutter has seen msg read: its space read since
// for a top-level message, its thread for a reply. Thread reads in mutter
// never reach the server, so search results don't reflect them.
func (m *model) readHere(msg *chat.Message) bool {
	if m.inThread != nil && msg.Thread != nil && m.inThread.name == msg.Thread.Name {
		return true
	}
	space := spaceOf(msg.Name)
	if !msg.ThreadReply {
		r := m.readTimes[space]
		return r != "" && !isUnread(msg.CreateTime, r)
	}
	threads := m.threads
	if m.cur < 0 || m.spaces[m.cur].name != space {
		threads = nil
		if c := m.mem[space]; c != nil {
			threads = c.threads
		}
	}
	for _, t := range threads {
		if msg.Thread != nil && t.name == msg.Thread.Name {
			return t.readAt != "" && !isUnread(msg.CreateTime, t.readAt)
		}
	}
	return false
}

// showResults opens the list, unread first and otherwise newest first, as
// the API returns them.
func (m *model) showResults(msg searchMsg) {
	if len(msg.results) == 0 {
		m.notice = msg.empty
		return
	}
	for i, h := range msg.results {
		msg.results[i].unread = h.unread && !m.readHere(h.msg)
	}
	slices.SortStableFunc(msg.results, func(a, b hit) int {
		switch {
		case a.unread == b.unread:
			return 0
		case a.unread:
			return -1
		}
		return 1
	})
	m.notice = ""
	m.mode, m.found, m.foundTitle, m.foundIdx = modeFind, msg.results, msg.title, 0
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
		h := m.found[m.foundIdx]
		m.mode, m.found = "", nil
		return m, m.gotoMessage(h.msg.Name)
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
	unread := 0
	for _, h := range m.found {
		if h.unread {
			unread++
		}
	}
	head := fmt.Sprintf("%d %s", len(m.found), m.foundTitle)
	if unread > 0 {
		head += fmt.Sprintf(" · %d unread", unread)
	}
	lines := []string{boldStyle.Render(head), ""}
	for i := start; i < min(len(m.found), start+rows); i++ {
		msg := m.found[i].msg
		where := spaceOf(msg.Name)
		if j := m.spaceIndex(where); j >= 0 {
			where = m.spaces[j].title
		}
		text, _, _ := strings.Cut(clean(customEmojiNames(msg)), "\n")
		meta := fmt.Sprintf("%s · %s · %s", truncate(clean(where), 24), truncate(senderName(msg), 20), when(msg.CreateTime))
		line := truncate(meta+"  "+dimStyle.Render(text), m.width-4)
		if m.found[i].unread {
			line = newDot() + line
		} else {
			line = "  " + line
		}
		if i == m.foundIdx {
			line = selStyle.Render("▶ ") + line
		} else {
			line = "  " + line
		}
		lines = append(lines, line)
	}
	return lipgloss.NewStyle().Width(m.width).Height(height).MaxHeight(height).Render(strings.Join(lines, "\n"))
}
