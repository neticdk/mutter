package main

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/api/chat/v1"
)

func testModel() model {
	m := newModel(context.Background(), &client{meID: "users/me"}, nil)
	m.spaces = []space{{name: "spaces/A", title: "alpha"}, {name: "spaces/B", title: "beta"}}
	m.cur = 0
	return m
}

func msgIn(space, thread, id string) *chat.Message {
	return &chat.Message{Name: space + "/messages/" + id, Thread: &chat.Thread{Name: space + "/threads/" + thread}, Sender: &chat.User{Name: "users/other"}}
}

func TestSwitcherDownWithoutMatches(t *testing.T) {
	m := testModel()
	m.switching = true
	m.filter.SetValue("zzz")
	m.resetFilter()
	next, _ := m.updateSwitcher(tea.KeyPressMsg{Code: tea.KeyDown})
	m = next.(model)
	m.refilter() // panicked on pick -1
	if m.pick != 0 {
		t.Errorf("pick = %d, want 0", m.pick)
	}
}

func TestSetBlocksFirstBlockAtTop(t *testing.T) {
	m := testModel()
	m.vp.SetWidth(20)
	m.vp.SetHeight(3)
	blocks := func() []string { return []string{"a\nb", "c\nd", "e\nf", "g\nh"} }
	m.setBlocks(blocks(), 3)
	m.setBlocks(blocks(), 0)
	if m.vp.YOffset() != 0 {
		t.Errorf("offset = %d, want 0", m.vp.YOffset())
	}
}

func TestEventWhileLoading(t *testing.T) {
	m := testModel()
	m.cursor = 5
	m.open(1)
	early, late := msgIn("spaces/B", "t1", "1"), msgIn("spaces/B", "t2", "2")
	m.add(early)
	m.add(late)
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.cursor)
	}
	// The load saw only the early message.
	next, _ := m.Update(messagesMsg{space: "spaces/B", threads: groupThreads([]*chat.Message{early})})
	m = next.(model)
	if len(m.threads) != 2 || m.threads[1].msgs[0] != late {
		t.Fatalf("got %d threads, the late message was dropped", len(m.threads))
	}
}

func TestRemoveKeepsSelection(t *testing.T) {
	m := testModel()
	m.threads = groupThreads([]*chat.Message{msgIn("spaces/A", "t1", "1"), msgIn("spaces/A", "t2", "2"), msgIn("spaces/A", "t3", "3")})
	m.cursor = 2
	m.remove("spaces/A/messages/1")
	if m.cursor != 1 || m.threads[m.cursor].name != "spaces/A/threads/t3" {
		t.Errorf("cursor = %d, want 1 on t3", m.cursor)
	}
}

func TestIncomingUnknownSpaceFetchesIt(t *testing.T) {
	m := testModel()
	if m.incoming(msgIn("spaces/NEW", "t", "1")) == nil {
		t.Error("message in an unknown space was dropped")
	}
}
