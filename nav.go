package main

import (
	"cmp"
	"log/slog"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/api/chat/v1"
)

type (
	// olderMsg carries the page of history before what's shown.
	olderMsg struct {
		space   string
		threads []*thread
		next    string
	}
	// spaceInfoMsg carries a fetched space, or reports it gone.
	spaceInfoMsg struct {
		space space
		name  string
		gone  bool
		open  bool          // open it once known, for /dm
		msg   *chat.Message // handle it once known, for a new space
	}
	// threadMsg carries every message of a thread, oldest first.
	threadMsg struct {
		name string
		msgs []*chat.Message
	}
	sectionsMsg map[string]string
)

// loadOlder fetches the page of history before the oldest thread shown.
func (m *model) loadOlder() tea.Cmd {
	if m.cur < 0 || m.loadingOlder {
		return nil
	}
	if m.olderToken == "" {
		m.notice = "start of history"
		return nil
	}
	m.loadingOlder = true
	m.notice = "loading older messages…"
	space, token, read := m.spaces[m.cur].name, m.olderToken, m.openRead
	return func() tea.Msg {
		threads, next, err := m.c.threads(m.ctx, space, historySize, token)
		if err != nil {
			return errMsg(err)
		}
		m.c.markNew(m.ctx, threads, read, nil)
		return olderMsg{space, threads, next}
	}
}

// addOlder puts older threads above the current ones. A thread already
// shown is complete, because its root is loaded and all replies are newer,
// so older copies of it are dropped.
func (m *model) addOlder(msg olderMsg) tea.Cmd {
	m.loadingOlder = false
	if m.cur < 0 || m.spaces[m.cur].name != msg.space {
		return nil
	}
	m.olderToken = msg.next
	var older []*thread
	for _, t := range msg.threads {
		if !slices.ContainsFunc(m.threads, func(x *thread) bool { return x.name == t.name }) {
			older = append(older, t)
		}
	}
	m.threads = append(older, m.threads...)
	m.cursor += len(older)
	m.notice = ""
	if len(older) > 0 {
		m.cursor-- // step onto the newest of the older threads
	}
	m.render()
	return m.imgs.fetch(m.ctx, m.c, rootsOf(older))
}

func (m model) fetchSpace(name string, open bool, msg *chat.Message) tea.Cmd {
	return func() tea.Msg {
		s, gone, err := m.c.getSpace(m.ctx, name)
		if err != nil {
			return errMsg(err)
		}
		return spaceInfoMsg{space: s, name: name, gone: gone, open: open, msg: msg}
	}
}

func (m model) fetchThread(space, name string) tea.Cmd {
	return func() tea.Msg {
		msgs, err := m.c.threadMessages(m.ctx, space, name)
		if err != nil {
			return errMsg(err)
		}
		return threadMsg{name, msgs}
	}
}

// applySpace updates or adds a fetched space. A space the user left is
// hidden, so the cursor indexes stay valid.
func (m *model) applySpace(msg spaceInfoMsg) tea.Cmd {
	i := m.spaceIndex(msg.name)
	switch {
	case msg.gone:
		if i >= 0 {
			m.spaces[i].hidden = true
		}
		return nil
	case i >= 0:
		m.spaces[i].title, m.spaces[i].hidden = msg.space.title, msg.space.hidden
	default:
		m.spaces = append(m.spaces, msg.space)
		i = len(m.spaces) - 1
	}
	if m.titles != nil && !strings.HasPrefix(msg.space.title, "spaces/") {
		m.titles[msg.name] = msg.space.title
		m.titleChecked[msg.name] = time.Now().Unix()
		if err := saveTitleCache(m.c.me, m.titles, m.titleChecked); err != nil {
			slog.Warn("title cache write", "err", err)
		}
	}
	if m.switching {
		m.refilter()
	}
	if msg.open {
		return m.open(i)
	}
	if msg.msg != nil {
		return m.incoming(msg.msg)
	}
	return nil
}

// dmCmd opens the DM with who, creating it when needed. who is an email
// address, a short name such as "kn", or part of a name. A short name tries
// the user's own domain first, so kn becomes kn@example.com for a user at
// example.com, then the people known from loaded spaces.
func (m *model) dmCmd(who string) tea.Cmd {
	who = strings.TrimSpace(strings.TrimPrefix(who, "@"))
	if who == "" {
		m.notice = "usage: /dm NAME or /dm EMAIL"
		return nil
	}
	if strings.Contains(who, "@") {
		return m.openDM([]string{"users/" + who}, "")
	}
	var tries []string
	if _, domain, ok := strings.Cut(m.c.me, "@"); ok && !strings.ContainsAny(who, " \t") {
		tries = append(tries, "users/"+who+"@"+domain)
	}
	match, candidates := dmMatch(m.knownPeople(), who)
	if match != "" {
		tries = append(tries, match)
	}
	fail := "no one called " + who + " in the spaces opened so far, use their email"
	if len(candidates) > 1 {
		names := make([]string, 0, len(candidates))
		for _, x := range candidates {
			names = append(names, cmp.Or(x.email, x.name))
		}
		fail = "which one: " + strings.Join(names, ", ")
	}
	return m.openDM(tries, fail)
}

// dmMatch picks the person who refers to from people: the only one whose
// address starts with who@, or else the only one whose name matches. With
// no single match it returns every candidate.
func dmMatch(people []member, who string) (string, []member) {
	var byEmail, byName []member
	for _, p := range people {
		if local, _, _ := strings.Cut(p.email, "@"); strings.EqualFold(local, who) {
			byEmail = append(byEmail, p)
		}
		if nameMatches(p.name, who) {
			byName = append(byName, p)
		}
	}
	switch {
	case len(byEmail) == 1:
		return byEmail[0].id, nil
	case len(byEmail) > 1:
		return "", byEmail
	case len(byName) == 1:
		return byName[0].id, nil
	}
	return "", byName
}

// openDM opens the DM with the first user in tries that works. A user that
// doesn't exist fails when its DM is created, so the next one gets a try.
// fail is the notice when none works.
func (m *model) openDM(tries []string, fail string) tea.Cmd {
	m.notice = "opening DM…"
	return func() tea.Msg {
		var lastErr error
		for _, user := range tries {
			name, err := m.c.dm(m.ctx, user)
			if err != nil {
				slog.Debug("dm attempt", "user", user, "err", err)
				lastErr = err
				continue
			}
			s, gone, err := m.c.getSpace(m.ctx, name)
			if err != nil {
				return errMsg(err)
			}
			return spaceInfoMsg{space: s, name: name, gone: gone, open: true}
		}
		if fail != "" {
			return noticeMsg(fail)
		}
		return errMsg(lastErr)
	}
}

func (m model) loadSections() tea.Msg {
	s, err := m.c.sections(m.ctx)
	if err != nil {
		slog.Warn("sections", "err", err)
		return nil
	}
	return sectionsMsg(s)
}

// wantMessage reports whether a message event in space is worth fetching:
// the space is open or cached, so the message shows, or it can go unread and
// notify. Unknown spaces are fetched, since their first message adds them.
// A muted space that's neither open nor cached only gets its last activity
// updated.
func (m *model) wantMessage(ref messageRef) bool {
	space := spaceOf(ref.name)
	i := m.spaceIndex(space)
	switch {
	case i < 0, i == m.cur, m.mem[space] != nil, !m.spaces[i].muted:
		return true
	}
	if ref.at != "" {
		m.spaces[i].lastActive = ref.at
	}
	return false
}

// fetchMessage gets a message named by an event and hands it on as a
// messageEvent.
func (m model) fetchMessage(ref messageRef) tea.Cmd {
	return func() tea.Msg {
		msg, err := m.c.svc.Spaces.Messages.Get(ref.name).Context(m.ctx).Do()
		if err != nil {
			slog.Warn("get message", "name", ref.name, "err", err)
			return nil
		}
		return messageEvent{kind: ref.kind, name: ref.name, msg: msg}
	}
}

// nextUnread opens the first unread space in the switcher's order, other
// than the open one.
func (m *model) nextUnread() tea.Cmd {
	var idx []int
	for i, s := range m.spaces {
		if s.unread && !s.hidden && i != m.cur {
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		m.notice = "no unread spaces"
		return nil
	}
	sortSpaces(m.spaces, idx)
	return m.open(idx[0])
}
