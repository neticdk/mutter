package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"google.golang.org/api/chat/v1"
)

const historySize = 200

// Key names compared in several places.
const (
	keyEsc   = "esc"
	keyEnter = "enter"
)

var (
	headerStyle = lipgloss.NewStyle().Bold(true).Padding(0, 1)
	senderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5"))
	inputStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8"))
	selStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("5")).Bold(true)
	threadStyle = lipgloss.NewStyle().Border(lipgloss.HiddenBorder(), false, false, false, true).PaddingLeft(1)
	cursorStyle = threadStyle.BorderStyle(lipgloss.ThickBorder()).BorderForeground(lipgloss.Color("5"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	liveStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
)

type (
	spacesMsg []space
	titlesMsg struct {
		titles map[string]string
		rest   []space // still to look up
	}
	messagesMsg struct {
		space    string
		threads  []*thread
		next     string // page token for older history
		lastRead string // read state before opening, for marking older pages
	}
	sentMsg   *chat.Message
	errMsg    error
	eventMsg  struct{ inner tea.Msg } // from runEvents
	unreadMsg map[string]readInfo
	noticeMsg string // informational status line text
	// markedUnreadMsg reports that space now reads as last read at lastRead.
	markedUnreadMsg struct{ space, lastRead string }
	// notifyMsg reports a checked incoming message. muted carries the
	// space's mute setting, which may not have been known yet.
	notifyMsg struct {
		space       string
		muted, show bool
		title, body string
	}
)

type model struct {
	ctx context.Context
	c   *client

	spaces []space
	cur    int // index into spaces, -1 before load

	threads  []*thread // newest activity last
	cursor   int       // selected thread in the space view
	inThread *thread   // thread view when set

	imgs         *images
	titles       map[string]string // title cache, see titleCache
	titleChecked map[string]int64  // when each cached title was looked up
	readTimes    map[string]string // see readTimesFile

	events  <-chan tea.Msg
	live    bool
	liveErr error

	focused  bool   // terminal has focus, so the open space counts as read
	holdRead bool   // /unread was used, so don't mark the open space read
	newBelow int    // threads that arrived below the cursor
	notice   string // shown in place of the hints until the next key press

	// Message actions. selecting means arrows picked a message, so letter
	// keys act on it.
	selecting  bool
	msgCursor  int           // selected message in the thread view
	mode       string        // modeReact, modeDelete or modeLink while waiting for the next key
	links      []string      // what the link prompt offers
	reactQuery string        // emoji search in the reaction picker
	reactIdx   int           // highlighted search result
	editing    *chat.Message // message whose text is in the input
	quoting    *chat.Message // message the next send quotes

	olderToken   string // page token for history before the oldest thread
	openRead     string // the open space's read state before it opened
	loadingOlder bool

	// Caching, see cache.go. loading is set while the open space fetches,
	// and arrived holds the live messages that come in meanwhile.
	mem     map[string]*cachedSpace
	store   *store
	loading bool
	arrived []*chat.Message

	members  map[string][]member // by space, for @mention completion
	drafts   map[string]draft    // by draftKey
	comp     *completion         // active tab cycle, see complete
	presence *chat.Availability  // the user's own state, shown in the header
	pending  []pending           // files to send with the next message
	mentions map[string]string   // completed "@Name" in the draft to "<users/ID>"

	vp viewport.Model
	ta textarea.Model

	switching bool
	filter    textinput.Model
	matches   []int // indexes into spaces
	pick      int   // index into matches

	status string
	width  int
}

func newModel(ctx context.Context, c *client, events <-chan tea.Msg) model {
	ta := textarea.New()
	ta.Placeholder = "Message"
	ta.ShowLineNumbers = false
	ta.Prompt = "> "
	ta.SetHeight(3)
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter", "alt+enter", "ctrl+j"))
	st := ta.Styles()
	st.Focused.CursorLine = lipgloss.NewStyle()
	ta.SetStyles(st)
	ta.Focus()

	f := textinput.New()
	f.Prompt = "switch to: "

	return model{ctx: ctx, c: c, events: events, focused: true, cur: -1, members: map[string][]member{}, mentions: map[string]string{}, drafts: loadDrafts(c.me), readTimes: loadReadTimes(c.me), mem: map[string]*cachedSpace{}, ta: ta, filter: f, vp: viewport.New(), imgs: newImages(), status: "loading spaces…"}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.loadSpaces, m.waitEvent, m.loadPresence, tea.Raw(kittyClear))
}

func (m model) waitEvent() tea.Msg {
	return eventMsg{<-m.events}
}

func (m model) loadSpaces() tea.Msg {
	s, err := m.c.spaces(m.ctx)
	if err != nil {
		return errMsg(err)
	}
	return spacesMsg(s)
}

// titleChunk is how many untitled spaces one lookup round covers. Spaces
// arrive most recently active first, so the ones in use get names first.
const titleChunk = 32

func (m model) loadTitleChunk(untitled []space) tea.Cmd {
	if len(untitled) == 0 {
		return nil
	}
	n := min(titleChunk, len(untitled))
	return func() tea.Msg {
		return titlesMsg{m.c.memberTitles(m.ctx, untitled[:n]), untitled[n:]}
	}
}

// loadMessages loads space and flags what's new since lastRead, fetching
// the read state when lastRead is unknown. It marks the space read only
// afterwards, so the flags reflect the state before opening.
func (m model) loadMessages(space, lastRead string, known map[string]string) tea.Cmd {
	return func() tea.Msg {
		if lastRead == "" {
			var err error
			if lastRead, err = m.c.readState(m.ctx, space); err != nil {
				log.Printf("read state %s: %v", space, err)
			}
		}
		threads, next, err := m.c.threads(m.ctx, space, historySize, "")
		if err != nil {
			return errMsg(err)
		}
		m.c.markNew(m.ctx, threads, lastRead, known)
		if err := m.c.markRead(m.ctx, space); err != nil {
			log.Printf("mark read %s: %v", space, err)
		}
		return messagesMsg{space, threads, next, lastRead}
	}
}

func (m model) sendCmd(space, thread, text string, quote *chat.Message) tea.Cmd {
	return func() tea.Msg {
		msg, err := m.c.send(m.ctx, space, outgoing{thread: thread, text: text, quote: quote})
		if err != nil {
			return errMsg(err)
		}
		return sentMsg(msg)
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.ta.SetWidth(msg.Width - 2)
		// header, input box with border, status line
		m.vp.SetWidth(msg.Width)
		m.vp.SetHeight(max(1, msg.Height-1-(m.ta.Height()+2)-1))
		m.imgs.resize(msg.Width, m.vp.Height())
		m.render()
		return m, nil

	case spacesMsg:
		m.titles, m.titleChecked = loadTitleCache(m.c.me)
		// Picked before the placeholders below fill in the empty titles.
		cmds := []tea.Cmd{m.loadTitleChunk(titlesToRefresh(msg, m.titles, m.titleChecked, time.Now()))}
		m.spaces = msg
		m.status = ""
		for i, s := range m.spaces {
			if s.title != "" {
				continue
			}
			t, ok := m.titles[s.name]
			m.spaces[i].hidden = ok && t == ""
			m.spaces[i].title = cmp.Or(t, s.name)
		}
		spaces := slices.Clone(m.spaces)
		known := maps.Clone(m.readTimes)
		cmds = append(cmds, func() tea.Msg { return unreadMsg(m.c.unread(m.ctx, spaces, known)) }, m.loadSections)
		if len(m.spaces) > 0 {
			cmds = append(cmds, m.open(0))
		}
		return m, tea.Batch(cmds...)

	case unreadMsg:
		for i, s := range m.spaces {
			info, ok := msg[s.name]
			if !ok || i == m.cur {
				continue
			}
			m.spaces[i].unread = info.unread && !info.muted
			m.spaces[i].muted = info.muted
			m.spaces[i].lastRead = info.lastRead
			m.noteRead(s.name, info.lastRead)
		}
		if m.switching {
			m.refilter()
		}
		return m, nil

	case readStateMsg:
		m.noteRead(msg.space, msg.lastRead)
		if i := m.spaceIndex(msg.space); i >= 0 && i != m.cur {
			m.spaces[i].unread = !m.spaces[i].muted && isUnread(m.spaces[i].lastActive, msg.lastRead)
			m.spaces[i].lastRead = msg.lastRead
		}
		return m, nil

	case membersMsg:
		m.members[msg.space] = msg.members
		return m, nil

	case noticeMsg:
		m.notice = string(msg)
		return m, nil

	case markedUnreadMsg:
		m.forgetRead(msg.space) // moved back, so look it up next start
		if m.cur >= 0 && m.spaces[m.cur].name == msg.space {
			for _, t := range m.threads {
				t.unseen, t.readAt = 0, msg.lastRead
				m.c.countNew(t, msg.lastRead)
			}
			m.notice = "marked unread from the selected thread"
			m.render()
		}
		return m, nil

	case notifyMsg:
		if i := m.spaceIndex(msg.space); i >= 0 && msg.muted {
			m.spaces[i].muted, m.spaces[i].unread = true, false
		}
		if !msg.show {
			return m, nil
		}
		return m, tea.Raw(osc777(msg.title, msg.body))

	case tea.FocusMsg:
		m.focused = true
		// The state may have changed elsewhere, or a DND may have run out.
		cmds := []tea.Cmd{m.loadPresence}
		if m.cur >= 0 && m.spaces[m.cur].unread && !m.holdRead {
			m.spaces[m.cur].unread = false
			cmds = append(cmds, m.markRead(m.spaces[m.cur].name))
		}
		return m, tea.Batch(cmds...)

	case pastedMsg:
		m.pending = append(m.pending, pending(msg))
		m.notice = "attached " + msg.name + ", it goes with the next message"
		return m, nil

	case tea.PasteMsg:
		if f, ok := droppedFile(msg.Content); ok {
			m.pending = append(m.pending, f)
			m.notice = "attached " + f.name + ", it goes with the next message"
			return m, nil
		}

	case presenceMsg:
		m.presence = msg.a
		return m, nil

	case tea.BlurMsg:
		m.focused = false
		return m, nil

	case titlesMsg:
		for i, s := range m.spaces {
			t, ok := msg.titles[s.name]
			if !ok {
				continue
			}
			m.titles[s.name] = t
			m.titleChecked[s.name] = time.Now().Unix()
			m.spaces[i].hidden = t == ""
			if t != "" {
				m.spaces[i].title = t
			}
		}
		if err := saveTitleCache(m.c.me, m.titles, m.titleChecked); err != nil {
			log.Printf("title cache: %v", err)
		}
		if m.switching {
			m.refilter()
		}
		return m, m.loadTitleChunk(msg.rest)

	case messagesMsg:
		if m.cur >= 0 && m.spaces[m.cur].name == msg.space {
			arrived := m.arrived
			m.threads, m.arrived, m.loading = msg.threads, nil, false
			m.noteRead(msg.space, time.Now().UTC().Format(time.RFC3339Nano)) // the load marked it read
			m.cursor = len(m.threads) - 1
			m.olderToken, m.openRead = msg.next, msg.lastRead
			m.status = ""
			// Messages that arrived while the space loaded may postdate the
			// fetch. A disk snapshot shown meanwhile is dropped, so messages
			// deleted since it was written don't come back.
			for _, x := range arrived {
				if _, _, ok := m.find(x.Name); !ok {
					m.add(x)
				}
			}
			m.render()
			return m, tea.Batch(m.imgs.fetch(m.ctx, m.c, rootsOf(m.threads)), m.persist(msg.space, m.threads, m.olderToken))
		}
		return m, nil

	case imageMsg:
		seq, anim := m.imgs.add(msg)
		if seq == "" {
			return m, nil
		}
		m.render()
		return m, tea.Batch(tea.Raw(seq), anim)

	case animMsg:
		seq, next := m.imgs.step(msg.ref)
		if seq == "" {
			return m, next
		}
		return m, tea.Batch(tea.Raw(seq), next)

	case sentMsg:
		m.add(msg)
		return m, nil

	case eventMsg:
		next, cmd := m.Update(msg.inner)
		return next, tea.Batch(cmd, m.waitEvent)

	case liveMsg:
		if msg.err != nil && m.live {
			m.liveLost()
		}
		m.live, m.liveErr = msg.err == nil, msg.err
		return m, nil

	case messageEvent:
		m.applyCached(msg)
		switch msg.kind {
		case kindCreated:
			return m, m.incoming(msg.msg)
		case kindUpdated:
			m.replace(msg.msg)
		case kindDeleted:
			m.remove(msg.name)
		}
		return m, nil

	case spaceChangedMsg:
		return m, m.fetchSpace(msg.space, false, nil)

	case threadMsg:
		if i := slices.IndexFunc(m.threads, func(t *thread) bool { return t.name == msg.name }); i >= 0 && len(msg.msgs) > 0 {
			t := m.threads[i]
			// Keep replies that arrived while the thread was fetched.
			for _, x := range t.msgs {
				if !slices.ContainsFunc(msg.msgs, func(y *chat.Message) bool { return y.Name == x.Name }) {
					msg.msgs = append(msg.msgs, x)
				}
			}
			t.msgs = msg.msgs
			m.render()
			return m, m.imgs.fetch(m.ctx, m.c, msg.msgs[:1])
		}
		return m, nil

	case spaceInfoMsg:
		return m, m.applySpace(msg)

	case olderMsg:
		return m, m.addOlder(msg)

	case sectionsMsg:
		for i, s := range m.spaces {
			m.spaces[i].section = msg[s.name]
		}
		return m, nil

	case errMsg:
		log.Printf("error: %v", msg)
		m.loadingOlder = false // a failed page would otherwise block older history
		m.status = errStyle.Render(msg.Error())
		return m, nil

	case tea.KeyPressMsg:
		m.notice = ""
		if tabStep(msg.String()) == 0 {
			m.comp = nil
		}
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.switching {
			return m.updateSwitcher(msg)
		}
		if m.mode != "" {
			return m.updateMode(msg)
		}
		if m.selecting && m.ta.Value() == "" {
			if cmd, ok := m.action(msg.String()); ok {
				return m, cmd
			}
			if msg.Text != "" {
				// Typing ends the selection and goes to the input.
				m.selecting = false
				m.render()
			}
		}
		switch msg.String() {
		case "ctrl+k":
			m.switching = true
			m.filter.SetValue("")
			m.resetFilter()
			m.ta.Blur()
			return m, m.filter.Focus()
		case "pgup", "pgdown":
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		case "tab", "shift+tab":
			if m.complete(tabStep(msg.String())) {
				return m, nil
			}
		case "ctrl+v":
			log.Print("paste: ctrl+v")
			return m, pasteImage
		case "ctrl+x":
			if n := len(m.pending); n > 0 {
				m.notice = "removed " + m.pending[n-1].name
				m.pending = m.pending[:n-1]
				return m, nil
			}
		case keyEsc:
			switch {
			case m.editing != nil || m.quoting != nil:
				if m.editing != nil {
					m.ta.Reset()
				}
				m.editing, m.quoting = nil, nil
			case m.selecting:
				m.selecting = false
			case m.inThread != nil:
				m.saveDraft()
				m.setThread(nil)
				m.loadDraft()
			}
			m.render()
			return m, nil
		case keyEnter:
			// With files pending, enter sends them, even without text.
			if t := m.target(); m.ta.Value() == "" && len(m.pending) == 0 && m.inThread == nil && t != nil {
				m.saveDraft()
				m.setThread(t)
				m.loadDraft()
				return m, m.imgs.fetch(m.ctx, m.c, t.msgs)
			}
			return m.submit()
		case "up", "down":
			// Arrows move the thread cursor only while the input is empty, so
			// they still navigate a multi-line draft.
			if m.ta.Value() != "" {
				break
			}
			step := 1
			if msg.String() == "up" {
				step = -1
			}
			if t := m.inThread; t != nil {
				// The first press selects the last message, where the view
				// starts.
				if m.selecting {
					m.msgCursor = min(max(m.msgCursor+step, 0), len(t.msgs)-1)
				}
				m.selecting = true
				m.render()
			} else if len(m.threads) > 0 {
				if m.selecting && step < 0 && m.cursor == 0 {
					return m, m.loadOlder()
				}
				if m.selecting {
					m.cursor = min(max(m.cursor+step, 0), len(m.threads)-1)
				}
				m.selecting = true
				if m.cursor == len(m.threads)-1 {
					m.newBelow = 0
				}
				m.render()
			}
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

func (m model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.ta.Value())
	if text == "" && len(m.pending) == 0 {
		return m, nil
	}
	m.ta.Reset()
	m.saveDraft() // sent, so the context's draft goes
	fields := append(strings.Fields(text), "")
	switch fields[0] {
	case "/open", "/save":
		return m, m.fileCmd(fields)
	case "/dnd", "/away", "/active", "/status":
		return m, m.presenceCmd(fields, text)
	case "/attach":
		return m, m.attachCmd(text)
	case "/dm":
		return m, m.dmCmd(strings.TrimPrefix(text, "/dm"))
	case "/unread":
		if t := m.target(); t != nil {
			return m, m.unreadFrom(t.msgs[0].CreateTime)
		}
		return m, nil
	}
	switch text {
	case "/quit":
		return m, tea.Quit
	case "/logout":
		if err := errors.Join(logout(), wipeStore()); err != nil {
			m.status = errStyle.Render(err.Error())
			return m, nil
		}
		return m, tea.Quit
	}
	if m.cur < 0 {
		return m, nil
	}
	text = expandShortcodes(expandMentions(text, m.mentions))
	clear(m.mentions)
	if e := m.editing; e != nil {
		m.editing = nil
		m.render()
		return m, func() tea.Msg {
			msg, err := m.c.edit(m.ctx, e.Name, text)
			if err != nil {
				return errMsg(err)
			}
			return sentMsg(msg)
		}
	}
	thread := ""
	if m.inThread != nil {
		thread = m.inThread.name
	}
	quote := m.quoting
	m.quoting = nil
	m.render()
	if files := m.pending; len(files) > 0 {
		m.pending = nil
		return m, m.sendWith(m.spaces[m.cur].name, outgoing{thread: thread, text: text, quote: quote}, files)
	}
	return m, m.sendCmd(m.spaces[m.cur].name, thread, text, quote)
}

func (m model) updateSwitcher(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case keyEsc, "ctrl+k":
		m.switching = false
		m.filter.Blur()
		return m, m.ta.Focus()
	case "up", "ctrl+p":
		m.pick = max(0, m.pick-1)
		return m, nil
	case "down", "ctrl+n":
		m.pick = max(0, min(len(m.matches)-1, m.pick+1))
		return m, nil
	case keyEnter:
		m.switching = false
		m.filter.Blur()
		if len(m.matches) == 0 {
			return m, m.ta.Focus()
		}
		return m, tea.Batch(m.ta.Focus(), m.open(m.matches[m.pick]))
	}
	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(msg)
	m.resetFilter()
	return m, cmd
}

// refilter recomputes matches, unread spaces first and then by recent
// activity. The picked space stays selected when it still matches, so
// background updates don't move the selection.
func (m *model) refilter() {
	picked := -1
	if m.pick < len(m.matches) {
		picked = m.matches[m.pick]
	}
	m.matches = m.matches[:0]
	for i, s := range m.spaces {
		if !s.hidden && fuzzy(m.filter.Value(), s.title+" "+s.section) {
			m.matches = append(m.matches, i)
		}
	}
	slices.SortStableFunc(m.matches, func(a, b int) int {
		sa, sb := m.spaces[a], m.spaces[b]
		if sa.unread != sb.unread {
			if sa.unread {
				return -1
			}
			return 1
		}
		return strings.Compare(sb.lastActive, sa.lastActive)
	})
	m.pick = max(0, slices.Index(m.matches, picked))
}

// fuzzy reports whether pattern is a case-insensitive subsequence of s.
func fuzzy(pattern, s string) bool {
	s = strings.ToLower(s)
	for _, r := range strings.ToLower(pattern) {
		i := strings.IndexRune(s, r)
		if i < 0 {
			return false
		}
		s = s[i+len(string(r)):]
	}
	return true
}

func (m *model) open(i int) tea.Cmd {
	m.saveDraft()
	stash := m.stash()
	m.cur = i
	m.threads = nil
	m.loading, m.arrived = false, nil
	m.cursor = -1 // add moves it onto the first thread to arrive
	m.newBelow = 0
	if m.editing != nil {
		m.ta.Reset()
	}
	m.editing, m.quoting = nil, nil
	m.spaces[i].unread = false
	m.holdRead = false
	lastRead := m.spaces[i].lastRead
	m.spaces[i].lastRead = "" // stale once opened, so fetch it next time
	m.setThread(nil)
	m.loadDraft()
	cmds := []tea.Cmd{stash}
	if m.members[m.spaces[i].name] == nil {
		cmds = append(cmds, m.loadMembers(m.spaces[i].name))
	}
	if m.restore(m.spaces[i].name) {
		// Current from memory: no fetch, only the read marker.
		m.status = ""
		m.render()
		cmds = append(cmds, m.markRead(m.spaces[i].name), m.imgs.fetch(m.ctx, m.c, rootsOf(m.threads)))
		return tea.Batch(cmds...)
	}
	m.loading = true
	m.status = "loading…"
	if m.threads != nil {
		m.status = "refreshing…"
	}
	m.render()
	// The disk snapshot shown meanwhile carries last session's thread read
	// times.
	cmds = append(cmds, m.loadMessages(m.spaces[i].name, lastRead, threadReads(m.threads)))
	return tea.Batch(cmds...)
}

// attachCmd uploads the file from "/attach PATH [text]" and sends it into
// the open thread, or as a new thread.
func (m *model) attachCmd(cmd string) tea.Cmd {
	if m.cur < 0 {
		return nil
	}
	path, text, err := parseAttach(cmd)
	if err != nil {
		m.notice = err.Error()
		return nil
	}
	space, thread := m.spaces[m.cur].name, ""
	if m.inThread != nil {
		thread = m.inThread.name
	}
	text = expandShortcodes(expandMentions(text, m.mentions))
	clear(m.mentions)
	m.notice = "uploading " + filepath.Base(path) + "…"
	return func() tea.Msg {
		ref, err := m.c.upload(m.ctx, space, path)
		if err != nil {
			return errMsg(err)
		}
		msg, err := m.c.send(m.ctx, space, outgoing{thread: thread, text: text, att: ref})
		if err != nil {
			return errMsg(err)
		}
		return sentMsg(msg)
	}
}

// target is the thread commands act on: the open one, or the selected one.
func (m *model) target() *thread {
	if m.inThread != nil {
		return m.inThread
	}
	if m.cursor >= 0 && m.cursor < len(m.threads) {
		return m.threads[m.cursor]
	}
	return nil
}

func (m *model) markRead(space string) tea.Cmd {
	m.noteRead(space, time.Now().UTC().Format(time.RFC3339Nano))
	return func() tea.Msg {
		if err := m.c.markRead(m.ctx, space); err != nil {
			log.Printf("mark read %s: %v", space, err)
		}
		return nil
	}
}

func (m *model) spaceIndex(name string) int {
	return slices.IndexFunc(m.spaces, func(s space) bool { return s.name == name })
}

// incoming handles a new message from the event feed. Messages from others
// mark their space unread, unless it's open with the terminal focused, and
// may raise a desktop notification.
func (m *model) incoming(msg *chat.Message) tea.Cmd {
	i := m.spaceIndex(spaceOf(msg.Name))
	if i < 0 {
		// The first message in a new space can beat the membership event.
		return m.fetchSpace(spaceOf(msg.Name), false, msg)
	}
	m.spaces[i].lastActive = msg.CreateTime
	own := msg.Sender != nil && msg.Sender.Name == m.c.meID
	name := threadName(msg)
	known := slices.ContainsFunc(m.threads, func(t *thread) bool { return t.name == name })
	m.add(msg)
	var cmds []tea.Cmd
	if i == m.cur {
		cmds = append(cmds, m.imgs.fetch(m.ctx, m.c, []*chat.Message{msg}))
		if msg.ThreadReply && !known {
			// A reply to a thread outside the loaded history needs its root.
			cmds = append(cmds, m.fetchThread(spaceOf(msg.Name), name))
		}
	}
	if own {
		return tea.Batch(cmds...)
	}
	seen := i == m.cur && m.focused && !m.holdRead
	switch {
	case seen:
		cmds = append(cmds, m.markRead(m.spaces[i].name))
	case m.spaces[i].muted:
	default:
		m.spaces[i].unread = true
		if m.switching {
			m.refilter()
		}
		s := m.spaces[i]
		cmds = append(cmds, func() tea.Msg {
			setting, err := m.c.setting(m.ctx, s.name)
			if err != nil {
				log.Printf("notification setting %s: %v", s.name, err)
			}
			sender := "someone"
			if msg.Sender != nil {
				sender = cmp.Or(msg.Sender.DisplayName, sender)
			}
			return notifyMsg{
				space: s.name,
				muted: setting != nil && setting.MuteSetting == mutedSetting,
				show:  shouldNotify(setting, s.dm, mentions(msg, m.c.meID), !msg.ThreadReply),
				title: s.title,
				body:  sender + ": " + cmp.Or(msg.Text, msg.FallbackText, "[attachment]"),
			}
		})
	}
	return tea.Batch(cmds...)
}

// setThread opens t, or returns to the space view when t is nil. Leaving a
// thread counts it as read, so its new markers clear.
// resetFilter recomputes matches with the top one selected, for a new
// filter.
func (m *model) resetFilter() {
	m.matches = m.matches[:0]
	m.refilter()
}

func (m *model) setThread(t *thread) {
	if old := m.inThread; old != nil && old != t {
		old.readAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	m.inThread = t
	m.selecting = false
	if t != nil {
		t.unseen = 0
		t.rootNew = false
		m.msgCursor = len(t.msgs) - 1
	}
	m.ta.Placeholder = "New thread"
	if t != nil {
		m.ta.Placeholder = "Reply"
	}
	m.render()
}

// add files msg under its thread. A new thread goes to the bottom, and the
// cursor follows it only if it was already on the last thread. Messages from
// others count as new for the unseen markers.
func (m *model) add(msg *chat.Message) {
	fromOthers := !m.own(msg)
	if m.cur < 0 || !strings.HasPrefix(msg.Name, m.spaces[m.cur].name+"/") {
		return
	}
	// A message sent from here arrives again as an event.
	if m.replace(msg) {
		return
	}
	if m.loading {
		m.arrived = append(m.arrived, msg)
	}
	name := threadName(msg)
	if i := slices.IndexFunc(m.threads, func(t *thread) bool { return t.name == name }); i >= 0 {
		t := m.threads[i]
		t.msgs = append(t.msgs, msg)
		if fromOthers && t != m.inThread {
			t.unseen++
		}
	} else {
		atBottom := m.cursor == len(m.threads)-1
		m.threads = append(m.threads, &thread{name: name, msgs: []*chat.Message{msg}, rootNew: fromOthers})
		switch {
		case atBottom:
			m.cursor = len(m.threads) - 1
		case fromOthers:
			m.newBelow++
		}
	}
	m.render()
}

// find locates a message by name in the open space.
func (m *model) find(name string) (ti, mi int, ok bool) {
	return findMsg(m.threads, name)
}

// replace swaps in an edited message and reports whether it was shown.
func (m *model) replace(msg *chat.Message) bool {
	ti, mi, ok := m.find(msg.Name)
	if !ok {
		return false
	}
	m.threads[ti].msgs[mi] = msg
	m.render()
	return true
}

// remove drops a deleted message, and its thread once the thread is empty.
func (m *model) remove(name string) {
	ti, mi, ok := m.find(name)
	if !ok {
		return
	}
	t := m.threads[ti]
	t.msgs = slices.Delete(t.msgs, mi, mi+1)
	if t == m.inThread {
		if mi < m.msgCursor {
			m.msgCursor-- // stay on the selected message
		}
		m.msgCursor = min(m.msgCursor, len(t.msgs)-1)
	}
	if len(t.msgs) == 0 {
		m.threads = slices.Delete(m.threads, ti, ti+1)
		if ti < m.cursor {
			m.cursor-- // stay on the selected thread
		}
		m.cursor = min(m.cursor, max(0, len(m.threads)-1))
		if m.inThread == t {
			m.setThread(nil)
			return
		}
	}
	m.render()
}

func (m *model) render() {
	m.imgs.hideAll()
	if t := m.inThread; t != nil {
		blocks := make([]string, 0, len(t.msgs))
		num := 0
		for _, msg := range t.msgs {
			block := m.message(msg, &num)
			if m.c.isNew(msg, t.readAt) {
				block = newDot() + block
			}
			blocks = append(blocks, block)
		}
		if !m.selecting {
			m.setBlocks(blocks, -1)
			m.vp.GotoBottom()
			return
		}
		m.setBlocks(blocks, m.msgCursor)
		return
	}

	var blocks []string
	for _, t := range m.threads {
		num := 0
		block := m.message(t.msgs[0], &num)
		if t.rootNew {
			block = newDot() + block
		}
		if n := len(t.msgs) - 1; n > 0 {
			line := fmt.Sprintf("%d %s · last %s", n, plural(n, "reply", "replies"), when(t.last()))
			if t.unseen > 0 {
				block += "\n" + boldStyle.Render(fmt.Sprintf("%s · %d new", line, t.unseen))
			} else {
				block += "\n" + dimStyle.Render(line)
			}
		}
		blocks = append(blocks, block)
	}
	m.setBlocks(blocks, m.cursor)
}

// setBlocks shows blocks with the one at sel highlighted and scrolled into
// view. sel -1 highlights nothing.
func (m *model) setBlocks(blocks []string, sel int) {
	top, bottom := 0, 0
	for i, b := range blocks {
		if i != sel {
			blocks[i] = threadStyle.Render(b)
			continue
		}
		blocks[i] = cursorStyle.Render(b)
		if i > 0 {
			// lipgloss.Height counts "" as one line, so the first block is
			// left at 0. The +1 is the blank separator line.
			top = lipgloss.Height(strings.Join(blocks[:i], "\n\n")) + 1
		}
		bottom = top + lipgloss.Height(blocks[i])
	}
	m.vp.SetContent(strings.Join(blocks, "\n\n"))
	if sel < 0 {
		return
	}
	if top < m.vp.YOffset() {
		m.vp.SetYOffset(top)
	} else if bottom > m.vp.YOffset()+m.vp.Height() {
		m.vp.SetYOffset(bottom - m.vp.Height())
	}
}

// newDot marks a message that's new to the user.
func newDot() string { return liveStyle.Render("● ") }

// message renders msg's sender, time and body.
func (m *model) message(msg *chat.Message, num *int) string {
	name := senderName(msg)
	body := lipgloss.NewStyle().Width(max(1, m.width-4)).Render(messageBody(msg, m.imgs.render, num))
	return senderStyle.Render(name) + " " + dimStyle.Render(when(msg.CreateTime)) + "\n" + body
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func when(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return ""
	}
	t = t.Local()
	if y, mo, d := time.Now().Date(); t.Year() == y && t.Month() == mo && t.Day() == d {
		return t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
}

func (m model) View() tea.View {
	title := "mutter"
	if m.cur >= 0 {
		title = m.spaces[m.cur].title
	}
	if m.live {
		title += " " + liveStyle.Render("● live")
	} else {
		title += " " + dimStyle.Render("○ offline")
	}
	if p := presenceLabel(m.presence); p != "" {
		title += " " + p
	}
	unread := 0
	for i, s := range m.spaces {
		if s.unread && !s.hidden && i != m.cur {
			unread++
		}
	}
	if unread > 0 {
		title += " " + boldStyle.Render(fmt.Sprintf("· %d unread %s", unread, plural(unread, "space", "spaces")))
	}
	hint := "↑/↓ select · enter open thread · type to start a thread · ctrl+k switch · /open /save /unread · /quit"
	if m.newBelow > 0 {
		hint = boldStyle.Render(fmt.Sprintf("↓ %d new", m.newBelow)) + dimStyle.Render(" · ") + dimStyle.Render(hint)
	}
	if m.inThread != nil {
		title += " › thread"
		hint = "enter reply · esc back · ↑/↓ scroll · ctrl+k switch · /open /save /unread · /quit"
	}
	body := m.vp.View()
	if m.switching {
		body = m.switcherView()
	}
	// A pending react or delete prompt beats notices, which beat the
	// editing, quoting and selection hints.
	status := m.status
	if status == "" && m.mode != "" {
		status = m.modeHint()
	}
	if status == "" {
		if s, idx := m.suggestions(); len(s) > 0 {
			names := make([]string, 0, len(s))
			for i, x := range s {
				n := x.label
				if i == idx {
					n = boldStyle.Render(n)
				}
				names = append(names, n)
			}
			status = dimStyle.Render("tab → ") + strings.Join(names, dimStyle.Render(" · "))
		}
	}
	if status == "" {
		status = m.notice
	}
	if status == "" {
		status = m.modeHint()
	}
	if status == "" && m.liveErr != nil {
		status = errStyle.Render("live updates: " + m.liveErr.Error())
	}
	if status == "" {
		status = dimStyle.Render(hint)
	}
	status = m.pendingLabel() + status
	v := tea.NewView(stripC1(lipgloss.JoinVertical(lipgloss.Left,
		headerStyle.Render(title),
		body,
		inputStyle.Render(m.ta.View()),
		status,
	)))
	v.AltScreen = true
	v.ReportFocus = true
	return v
}

func (m model) switcherView() string {
	lines := []string{m.filter.View()}
	for j, i := range m.matches {
		if len(lines) >= m.vp.Height() {
			break
		}
		title := m.spaces[i].title
		if m.spaces[i].unread {
			title = liveStyle.Render("● ") + boldStyle.Render(title)
		}
		if sec := m.spaces[i].section; sec != "" {
			title += dimStyle.Render(" · " + sec)
		}
		if m.hasDraft(m.spaces[i].name) {
			title += dimStyle.Render(" ✎")
		}
		if j == m.pick {
			lines = append(lines, selStyle.Render("› ")+title)
		} else {
			lines = append(lines, "  "+title)
		}
	}
	for len(lines) < m.vp.Height() {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}
