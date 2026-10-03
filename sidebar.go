package main

import (
	"fmt"
	"log/slog"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	sidebarWidth = 28
	// minSidebarWindow is the narrowest window that shows the sidebar, so
	// messages keep room to read.
	minSidebarWindow = 100
)

var sidebarStyle = lipgloss.NewStyle().Width(sidebarWidth).Border(lipgloss.NormalBorder(), false, true, false, false).BorderForeground(lipgloss.Color("8"))

// prefs is prefs.json, choices kept across restarts.
type prefs struct {
	Sidebar bool `json:"sidebar"`
}

func loadPrefs() prefs {
	var p prefs
	_ = readCache("prefs.json", &p) // a missing file means the defaults
	return p
}

func (m *model) savePrefs() {
	if err := writeCache("prefs.json", prefs{Sidebar: m.sidebarOn}); err != nil {
		slog.Warn("prefs write", "err", err)
	}
}

// sidebarShown reports whether the sidebar is on and the window has room.
func (m *model) sidebarShown() bool { return m.sidebarOn && m.width >= minSidebarWindow }

// layout sizes the panes for the window and the sidebar. Images are sized
// for the message pane, so they follow its width.
func (m *model) layout() {
	w := m.width
	if m.sidebarShown() {
		w -= sidebarWidth + 1 // the border
	}
	m.ta.SetWidth(m.width - 2)
	// header, input box with border, status line
	m.vp.SetWidth(w)
	m.vp.SetHeight(max(1, m.height-1-(m.ta.Height()+2)-1))
	m.imgs.resize(w, m.vp.Height())
	m.render()
}

// sidebarItems are the spaces the sidebar lists, in the switcher's order:
// unread first, then by recent activity. They're as many as fit.
func (m *model) sidebarItems() []int {
	var out []int
	for i, s := range m.spaces {
		if !s.hidden {
			out = append(out, i)
		}
	}
	sortSpaces(m.spaces, out)
	return out[:min(len(out), m.vp.Height())]
}

func (m *model) sidebarView() string {
	items := m.sidebarItems()
	lines := make([]string, 0, m.vp.Height())
	for n, i := range items {
		s := m.spaces[i]
		num := "  "
		if n < 9 {
			num = fmt.Sprintf("%d ", n+1)
		}
		title := truncate(s.title, sidebarWidth-5)
		switch {
		case i == m.cur:
			title = selStyle.Render(title)
		case s.unread:
			title = boldStyle.Render(title)
		case s.muted:
			title = dimStyle.Render(title)
		}
		dot := "  "
		if s.unread && i != m.cur {
			dot = liveStyle.Render("● ")
		}
		lines = append(lines, dimStyle.Render(num)+dot+title)
	}
	for len(lines) < m.vp.Height() {
		lines = append(lines, "")
	}
	return sidebarStyle.Render(strings.Join(lines, "\n"))
}

// truncate shortens s to n cells with an ellipsis.
func truncate(s string, n int) string {
	if lipgloss.Width(s) <= n {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > n {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// jump opens the sidebar's nth entry, counting from 1.
func (m *model) jump(n int) tea.Cmd {
	items := m.sidebarItems()
	if n < 1 || n > len(items) {
		return nil
	}
	return m.open(items[n-1])
}

// sidebarKey reports the entry number for alt+1…9 or ctrl+1…9, or 0.
// Option only works as alt on macOS when the terminal is set up for it, so
// ctrl works too.
func sidebarKey(k string) int {
	for _, prefix := range []string{"alt+", "ctrl+"} {
		if d, ok := strings.CutPrefix(k, prefix); ok && len(d) == 1 && d >= "1" && d <= "9" {
			return int(d[0] - '0')
		}
	}
	return 0
}

// sidebarClick opens the entry clicked at x, y, if any. The sidebar starts
// below the header line.
func (m *model) sidebarClick(x, y int) (tea.Cmd, bool) {
	if !m.sidebarShown() || x >= sidebarWidth {
		return nil, false
	}
	items := m.sidebarItems()
	row := y - 1
	if row < 0 || row >= len(items) {
		return nil, true
	}
	return m.open(items[row]), true
}
