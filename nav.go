package main

import (
	"log"
	"slices"
	"strings"

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
		open  bool // open it once known, for /dm
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
		m.c.markNew(m.ctx, threads, read)
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
	var roots []*chat.Message
	for _, t := range older {
		roots = append(roots, t.msgs[0])
	}
	return m.imgs.fetch(m.ctx, m.c, roots)
}

func (m model) fetchSpace(name string, open bool) tea.Cmd {
	return func() tea.Msg {
		s, gone, err := m.c.getSpace(m.ctx, name)
		if err != nil {
			return errMsg(err)
		}
		return spaceInfoMsg{space: s, name: name, gone: gone, open: open}
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
		if err := saveTitleCache(m.c.me, m.titles); err != nil {
			log.Printf("title cache: %v", err)
		}
	}
	if m.switching {
		m.refilter()
	}
	if msg.open {
		return m.open(i)
	}
	return nil
}

// dmCmd opens the DM with who, an email address or a name from the
// members of loaded spaces, creating the DM when needed.
func (m *model) dmCmd(who string) tea.Cmd {
	who = strings.TrimSpace(strings.TrimPrefix(who, "@"))
	if who == "" {
		m.notice = "usage: /dm NAME or /dm EMAIL"
		return nil
	}
	user := ""
	if strings.Contains(who, "@") {
		user = "users/" + who
	} else {
		var all []member
		seen := map[string]bool{}
		for _, ms := range m.members {
			for _, x := range ms {
				if !seen[x.id] {
					seen[x.id] = true
					all = append(all, x)
				}
			}
		}
		matches := slices.DeleteFunc(suggest(all, who), func(x member) bool { return x.id == allUsers })
		switch len(matches) {
		case 0:
			m.notice = "no one called " + who + " in the spaces opened so far, use their email"
			return nil
		case 1:
			user = matches[0].id
		default:
			var names []string
			for _, x := range matches {
				names = append(names, x.name)
			}
			m.notice = "which one: " + strings.Join(names, ", ")
			return nil
		}
	}
	m.notice = "opening DM…"
	return func() tea.Msg {
		name, err := m.c.dm(m.ctx, user)
		if err != nil {
			return errMsg(err)
		}
		s, gone, err := m.c.getSpace(m.ctx, name)
		if err != nil {
			return errMsg(err)
		}
		return spaceInfoMsg{space: s, name: name, gone: gone, open: true}
	}
}

func (m model) loadSections() tea.Msg {
	s, err := m.c.sections(m.ctx)
	if err != nil {
		log.Printf("sections: %v", err)
		return nil
	}
	return sectionsMsg(s)
}
