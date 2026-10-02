package main

import (
	"cmp"
	"context"
	"fmt"
	"log"
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

	imgs   *images
	titles map[string]string // title cache, see titleCache

	events  <-chan tea.Msg
	live    bool
	liveErr error

	focused  bool   // terminal has focus, so the open space counts as read
	holdRead bool   // /unread was used, so don't mark the open space read
	newBelow int    // threads that arrived below the cursor
	notice   string // shown in place of the hints until the next key press

	// Message actions. selecting means arrows picked a message, so letter
	// keys act on it.
	selecting bool
	msgCursor int           // selected message in the thread view
	mode      string        // "react" or "delete" while waiting for the next key
	editing   *chat.Message // message whose text is in the input
	quoting   *chat.Message // message the next send quotes

	olderToken   string // page token for history before the oldest thread
	openRead     string // the open space's read state before it opened
	loadingOlder bool

	members  map[string][]member // by space, for @mention completion
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

	return model{ctx: ctx, c: c, events: events, focused: true, cur: -1, members: map[string][]member{}, mentions: map[string]string{}, ta: ta, filter: f, vp: viewport.New(), imgs: newImages(), status: "loading spaces…"}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.loadSpaces, m.waitEvent, tea.Raw(kittyClear))
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

// loadTitles looks up names for the untitled spaces among spaces, one chunk
// per round. It must be called before the spacesMsg handler fills in
// placeholder titles.
func (m model) loadTitles(spaces []space) tea.Cmd {
	var untitled []space
	for _, s := range spaces {
		if s.title == "" {
			untitled = append(untitled, s)
		}
	}
	return m.loadTitleChunk(untitled)
}

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
func (m model) loadMessages(space, lastRead string) tea.Cmd {
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
		m.c.markNew(m.ctx, threads, lastRead)
		if err := m.c.markRead(m.ctx, space); err != nil {
			log.Printf("mark read %s: %v", space, err)
		}
		return messagesMsg{space, threads, next, lastRead}
	}
}

func (m model) sendCmd(space, thread, text string, quote *chat.Message) tea.Cmd {
	return func() tea.Msg {
		msg, err := m.c.send(m.ctx, space, thread, text, quote, nil)
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
		cmds := []tea.Cmd{m.loadTitles(msg)}
		m.spaces = msg
		m.status = ""
		m.titles = loadTitleCache(m.c.me)
		for i, s := range m.spaces {
			if s.title != "" {
				continue
			}
			t, ok := m.titles[s.name]
			m.spaces[i].hidden = ok && t == ""
			m.spaces[i].title = cmp.Or(t, s.name)
		}
		spaces := slices.Clone(m.spaces)
		cmds = append(cmds, func() tea.Msg { return unreadMsg(m.c.unread(m.ctx, spaces)) }, m.loadSections)
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
		}
		if m.switching {
			m.refilter(true)
		}
		return m, nil

	case readStateMsg:
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
		if m.cur >= 0 && m.spaces[m.cur].unread && !m.holdRead {
			m.spaces[m.cur].unread = false
			return m, m.markRead(m.spaces[m.cur].name)
		}
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
			m.spaces[i].hidden = t == ""
			if t != "" {
				m.spaces[i].title = t
			}
		}
		if err := saveTitleCache(m.c.me, m.titles); err != nil {
			log.Printf("title cache: %v", err)
		}
		if m.switching {
			m.refilter(true)
		}
		return m, m.loadTitleChunk(msg.rest)

	case messagesMsg:
		if m.cur >= 0 && m.spaces[m.cur].name == msg.space {
			m.threads = msg.threads
			m.cursor = len(m.threads) - 1
			m.olderToken, m.openRead = msg.next, msg.lastRead
			m.status = ""
			m.render()
			var roots []*chat.Message
			for _, t := range m.threads {
				roots = append(roots, t.msgs[0])
			}
			return m, m.imgs.fetch(m.ctx, m.c, roots)
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
		m.add(msg, false)
		return m, nil

	case eventMsg:
		next, cmd := m.Update(msg.inner)
		return next, tea.Batch(cmd, m.waitEvent)

	case liveMsg:
		m.live, m.liveErr = msg.err == nil, msg.err
		return m, nil

	case messageEvent:
		switch msg.kind {
		case "created":
			return m, m.incoming(msg.msg)
		case "updated":
			m.replace(msg.msg)
		case "deleted":
			m.remove(msg.name)
		}
		return m, nil

	case spaceChangedMsg:
		return m, m.fetchSpace(msg.space, false)

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
		m.status = errStyle.Render(msg.Error())
		return m, nil

	case tea.KeyPressMsg:
		m.notice = ""
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
			m.refilter(false)
			m.ta.Blur()
			return m, m.filter.Focus()
		case "pgup", "pgdown":
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		case "tab":
			if m.complete() {
				return m, nil
			}
		case "esc":
			switch {
			case m.editing != nil || m.quoting != nil:
				if m.editing != nil {
					m.ta.Reset()
				}
				m.editing, m.quoting = nil, nil
			case m.selecting:
				m.selecting = false
			case m.inThread != nil:
				m.setThread(nil)
			}
			m.render()
			return m, nil
		case "enter":
			if m.ta.Value() == "" && m.inThread == nil && len(m.threads) > 0 {
				t := m.threads[m.cursor]
				m.setThread(t)
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
	if text == "" {
		return m, nil
	}
	m.ta.Reset()
	fields := strings.Fields(text)
	switch fields[0] {
	case "/open", "/save":
		return m, m.fileCmd(fields)
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
		if err := logout(); err != nil {
			m.status = errStyle.Render(err.Error())
			return m, nil
		}
		return m, tea.Quit
	}
	if m.cur < 0 {
		return m, nil
	}
	text = expandMentions(text, m.mentions)
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
	return m, m.sendCmd(m.spaces[m.cur].name, thread, text, quote)
}

func (m model) updateSwitcher(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+k":
		m.switching = false
		m.filter.Blur()
		return m, m.ta.Focus()
	case "up", "ctrl+p":
		m.pick = max(0, m.pick-1)
		return m, nil
	case "down", "ctrl+n":
		m.pick = min(len(m.matches)-1, m.pick+1)
		return m, nil
	case "enter":
		m.switching = false
		m.filter.Blur()
		if len(m.matches) == 0 {
			return m, m.ta.Focus()
		}
		return m, tea.Batch(m.ta.Focus(), m.open(m.matches[m.pick]))
	}
	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(msg)
	m.refilter(false)
	return m, cmd
}

// refilter recomputes matches, unread spaces first and then by recent
// activity. With keep, the picked space stays selected when it still
// matches, so background updates don't move the selection. Otherwise the
// top match is selected.
func (m *model) refilter(keep bool) {
	picked := -1
	if keep && m.pick < len(m.matches) {
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
	m.cur = i
	m.threads = nil
	m.newBelow = 0
	m.spaces[i].unread = false
	m.holdRead = false
	lastRead := m.spaces[i].lastRead
	m.spaces[i].lastRead = "" // stale once opened, so fetch it next time
	m.setThread(nil)
	m.status = "loading…"
	cmds := []tea.Cmd{m.loadMessages(m.spaces[i].name, lastRead)}
	if m.members[m.spaces[i].name] == nil {
		cmds = append(cmds, m.loadMembers(m.spaces[i].name))
	}
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
	text = expandMentions(text, m.mentions)
	clear(m.mentions)
	m.notice = "uploading " + filepath.Base(path) + "…"
	return func() tea.Msg {
		ref, err := m.c.upload(m.ctx, space, path)
		if err != nil {
			return errMsg(err)
		}
		msg, err := m.c.send(m.ctx, space, thread, text, nil, ref)
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

func (m model) markRead(space string) tea.Cmd {
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
		return nil
	}
	m.spaces[i].lastActive = msg.CreateTime
	own := msg.Sender != nil && msg.Sender.Name == m.c.meID
	m.add(msg, !own)
	var cmds []tea.Cmd
	if i == m.cur {
		cmds = append(cmds, m.imgs.fetch(m.ctx, m.c, []*chat.Message{msg}))
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
			m.refilter(true)
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
				muted: setting != nil && setting.MuteSetting == "MUTED",
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
// cursor follows it only if it was already on the last thread. fromOthers
// counts the message as new for the unseen markers.
func (m *model) add(msg *chat.Message, fromOthers bool) {
	if m.cur < 0 || !strings.HasPrefix(msg.Name, m.spaces[m.cur].name+"/") {
		return
	}
	// A message sent from here arrives again as an event.
	if m.replace(msg) {
		return
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
	for ti, t := range m.threads {
		for mi, msg := range t.msgs {
			if msg.Name == name {
				return ti, mi, true
			}
		}
	}
	return 0, 0, false
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
	if len(t.msgs) == 0 {
		m.threads = slices.Delete(m.threads, ti, ti+1)
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
		var blocks []string
		num := 0
		for _, msg := range t.msgs {
			blocks = append(blocks, m.message(msg, m.c.isNew(msg, t.readAt), &num))
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
		block := m.message(t.msgs[0], t.rootNew, &num)
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
		top = lipgloss.Height(strings.Join(blocks[:i], "\n\n"))
		if i > 0 {
			top++ // the blank separator line
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

// message renders msg, with a dot when it's new to the user.
func (m *model) message(msg *chat.Message, isNew bool, num *int) string {
	name := ""
	if msg.Sender != nil {
		name = msg.Sender.DisplayName
		if name == "" {
			name = msg.Sender.Name
		}
	}
	body := lipgloss.NewStyle().Width(max(1, m.width-4)).Render(messageBody(msg, m.imgs.render, num))
	head := senderStyle.Render(name) + " " + dimStyle.Render(when(msg.CreateTime))
	if isNew {
		head = liveStyle.Render("● ") + head
	}
	return head + "\n" + body
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
		if s := m.suggestions(); len(s) > 0 {
			var names []string
			for i, x := range s {
				n := "@" + x.name
				if i == 0 {
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
	v := tea.NewView(lipgloss.JoinVertical(lipgloss.Left,
		headerStyle.Render(title),
		body,
		inputStyle.Render(m.ta.View()),
		status,
	))
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
