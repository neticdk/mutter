package main

import (
	"cmp"
	"context"
	"fmt"
	"log"
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
		space   string
		threads []*thread
	}
	sentMsg  *chat.Message
	errMsg   error
	eventMsg struct{ inner tea.Msg } // from runEvents
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

	return model{ctx: ctx, c: c, events: events, cur: -1, ta: ta, filter: f, vp: viewport.New(), imgs: newImages(), status: "loading spaces…"}
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

func (m model) loadMessages(space string) tea.Cmd {
	return func() tea.Msg {
		threads, err := m.c.threads(m.ctx, space, historySize)
		if err != nil {
			return errMsg(err)
		}
		return messagesMsg{space, threads}
	}
}

func (m model) sendCmd(space, thread, text string) tea.Cmd {
	return func() tea.Msg {
		msg, err := m.c.send(m.ctx, space, thread, text)
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
		if len(m.spaces) > 0 {
			cmds = append(cmds, m.open(0))
		}
		return m, tea.Batch(cmds...)

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
		m.add(msg)
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
			m.add(msg.msg)
			return m, m.imgs.fetch(m.ctx, m.c, []*chat.Message{msg.msg})
		case "updated":
			m.replace(msg.msg)
		case "deleted":
			m.remove(msg.name)
		}
		return m, nil

	case spaceChangedMsg:
		if !slices.ContainsFunc(m.spaces, func(s space) bool { return s.name == msg.space }) {
			// ponytail: newly joined spaces show up on the next start, add them live when that matters
			return m, nil
		}
		return m, func() tea.Msg {
			t, err := m.c.spaceTitle(m.ctx, msg.space)
			if err != nil {
				return errMsg(err)
			}
			return titlesMsg{titles: map[string]string{msg.space: t}}
		}

	case errMsg:
		log.Printf("error: %v", msg)
		m.status = errStyle.Render(msg.Error())
		return m, nil

	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.switching {
			return m.updateSwitcher(msg)
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
		case "esc":
			if m.inThread != nil {
				m.setThread(nil)
			}
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
			if m.inThread != nil && step < 0 {
				m.vp.ScrollUp(1)
			} else if m.inThread != nil {
				m.vp.ScrollDown(1)
			} else if len(m.threads) > 0 {
				m.cursor = min(max(m.cursor+step, 0), len(m.threads)-1)
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
	thread := ""
	if m.inThread != nil {
		thread = m.inThread.name
	}
	return m, m.sendCmd(m.spaces[m.cur].name, thread, text)
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

// refilter recomputes matches. With keep, the picked space stays selected
// when it still matches, so background title updates don't move the
// selection. Otherwise the top match is selected.
func (m *model) refilter(keep bool) {
	picked := -1
	if keep && m.pick < len(m.matches) {
		picked = m.matches[m.pick]
	}
	m.matches = m.matches[:0]
	m.pick = 0
	for i, s := range m.spaces {
		if !s.hidden && fuzzy(m.filter.Value(), s.title) {
			if i == picked {
				m.pick = len(m.matches)
			}
			m.matches = append(m.matches, i)
		}
	}
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
	m.setThread(nil)
	m.status = "loading…"
	return m.loadMessages(m.spaces[i].name)
}

func (m *model) setThread(t *thread) {
	m.inThread = t
	m.ta.Placeholder = "New thread"
	if t != nil {
		m.ta.Placeholder = "Reply"
	}
	m.render()
}

// add files msg under its thread. A new thread goes to the bottom, and the
// cursor follows it only if it was already on the last thread.
func (m *model) add(msg *chat.Message) {
	if m.cur < 0 || !strings.HasPrefix(msg.Name, m.spaces[m.cur].name+"/") {
		return
	}
	// A message sent from here arrives again as an event.
	if m.replace(msg) {
		return
	}
	name := threadName(msg)
	if i := slices.IndexFunc(m.threads, func(t *thread) bool { return t.name == name }); i >= 0 {
		m.threads[i].msgs = append(m.threads[i].msgs, msg)
	} else {
		atBottom := m.cursor == len(m.threads)-1
		m.threads = append(m.threads, &thread{name: name, msgs: []*chat.Message{msg}})
		if atBottom {
			m.cursor = len(m.threads) - 1
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
	if m.inThread != nil {
		var blocks []string
		for _, msg := range m.inThread.msgs {
			blocks = append(blocks, threadStyle.Render(m.message(msg)))
		}
		m.vp.SetContent(strings.Join(blocks, "\n\n"))
		m.vp.GotoBottom()
		return
	}

	var blocks []string
	top, bottom := 0, 0
	for i, t := range m.threads {
		block := m.message(t.msgs[0])
		if n := len(t.msgs) - 1; n > 0 {
			block += "\n" + dimStyle.Render(fmt.Sprintf("%d %s · last %s", n, plural(n, "reply", "replies"), when(t.last())))
		}
		if i == m.cursor {
			block = cursorStyle.Render(block)
			top = lipgloss.Height(strings.Join(blocks, "\n\n"))
			if len(blocks) > 0 {
				top++ // the blank separator line
			}
			bottom = top + lipgloss.Height(block)
		} else {
			block = threadStyle.Render(block)
		}
		blocks = append(blocks, block)
	}
	m.vp.SetContent(strings.Join(blocks, "\n\n"))
	if top < m.vp.YOffset() {
		m.vp.SetYOffset(top)
	} else if bottom > m.vp.YOffset()+m.vp.Height() {
		m.vp.SetYOffset(bottom - m.vp.Height())
	}
}

func (m *model) message(msg *chat.Message) string {
	name := ""
	if msg.Sender != nil {
		name = msg.Sender.DisplayName
		if name == "" {
			name = msg.Sender.Name
		}
	}
	body := lipgloss.NewStyle().Width(max(1, m.width-4)).Render(messageBody(msg, m.imgs.render))
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
	hint := "↑/↓ select · enter open thread · type to start a thread · ctrl+k switch · /quit"
	if m.inThread != nil {
		title += " › thread"
		hint = "enter reply · esc back · ↑/↓ scroll · ctrl+k switch · /quit"
	}
	body := m.vp.View()
	if m.switching {
		body = m.switcherView()
	}
	status := m.status
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
	return v
}

func (m model) switcherView() string {
	lines := []string{m.filter.View()}
	for j, i := range m.matches {
		if len(lines) >= m.vp.Height() {
			break
		}
		if j == m.pick {
			lines = append(lines, selStyle.Render("› "+m.spaces[i].title))
		} else {
			lines = append(lines, "  "+m.spaces[i].title)
		}
	}
	for len(lines) < m.vp.Height() {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}
