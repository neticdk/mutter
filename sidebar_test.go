package main

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSidebarKey(t *testing.T) {
	for k, want := range map[string]int{"alt+1": 1, "ctrl+9": 9, "alt+0": 0, "1": 0, "ctrl+b": 0, "alt+12": 0} {
		if got := sidebarKey(k); got != want {
			t.Errorf("sidebarKey(%q) = %d, want %d", k, got, want)
		}
	}
	if got := truncate("Contain Core Platform", 10); got != "Contain C…" {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate short = %q", got)
	}
}

func TestSidebarLayoutAndOrder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	m := newModel(context.Background(), &client{me: "me@example.com"}, nil)
	m.spaces = []space{
		{name: "spaces/old", title: "Old", lastActive: "2026-01-01T00:00:00Z"},
		{name: "spaces/new", title: "New", lastActive: "2026-10-01T00:00:00Z"},
		{name: "spaces/unread", title: "Unread", lastActive: "2025-01-01T00:00:00Z", unread: true},
		{name: "spaces/hidden", title: "Hidden", lastActive: "2026-10-02T00:00:00Z", hidden: true},
	}

	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = next.(model)
	if m.vp.Width() != 120 {
		t.Fatalf("off by default, message pane %d wide", m.vp.Width())
	}
	next, _ = m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	m = next.(model)
	if !m.sidebarShown() || m.vp.Width() != 120-sidebarWidth-1 {
		t.Fatalf("ctrl+b: shown %v, pane %d", m.sidebarShown(), m.vp.Width())
	}
	if !loadPrefs().Sidebar {
		t.Error("the choice wasn't saved")
	}

	items := m.sidebarItems()
	var names []string
	for _, i := range items {
		names = append(names, m.spaces[i].name)
	}
	if len(names) != 3 || names[0] != "spaces/unread" || names[1] != "spaces/new" || names[2] != "spaces/old" {
		t.Errorf("order = %v", names)
	}

	// A click on the second row opens the second entry. Row 0 is the header.
	if _, ok := m.sidebarClick(3, 2); !ok || m.spaces[m.cur].name != "spaces/new" {
		t.Errorf("click opened %v", m.cur)
	}
	if _, ok := m.sidebarClick(sidebarWidth+5, 2); ok {
		t.Error("a click in the message pane hit the sidebar")
	}

	// Narrow windows hide it, and the message pane gets the full width.
	next, _ = m.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
	m = next.(model)
	if m.sidebarShown() || m.vp.Width() != 90 {
		t.Errorf("narrow: shown %v, pane %d", m.sidebarShown(), m.vp.Width())
	}
}
