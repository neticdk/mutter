package main

import (
	"context"
	"testing"

	"google.golang.org/api/chat/v1"
)

func TestAddOlder(t *testing.T) {
	th := func(name string) *thread {
		return &thread{name: name, msgs: []*chat.Message{{Name: name + "/m", Thread: &chat.Thread{Name: name}}}}
	}
	m := newModel(context.Background(), &client{}, nil)
	m.spaces = []space{{name: "spaces/A"}}
	m.cur = 0
	m.threads = []*thread{th("c"), th("d")}
	m.cursor = 0 // on the oldest thread, where pressing up loads more

	// c is already shown and must not appear twice.
	m.addOlder(olderMsg{space: "spaces/A", threads: []*thread{th("a"), th("b"), th("c")}, next: "tok"})

	var names []string
	for _, x := range m.threads {
		names = append(names, x.name)
	}
	if got := len(names); got != 4 || names[0] != "a" || names[1] != "b" || names[2] != "c" {
		t.Fatalf("threads = %v, want [a b c d]", names)
	}
	if m.cursor != 1 {
		t.Errorf("cursor = %d, want 1, the newest older thread", m.cursor)
	}
	if m.olderToken != "tok" || m.loadingOlder {
		t.Errorf("token %q loading %v", m.olderToken, m.loadingOlder)
	}
}

func TestDMMatch(t *testing.T) {
	people := []member{
		{id: "users/1", name: "Kim Nørgaard", email: "kn@netic.dk"},
		{id: "users/2", name: "Kim Larsen", email: "kla@netic.dk"},
		{id: "users/3", name: "Karen Nielsen", email: "kn@other.dk"},
		{id: "users/4", name: "Mads Nygaard", email: "mny@netic.dk"},
	}
	tests := []struct {
		who        string
		id         string
		candidates int
	}{
		{"mny", "users/4", 0},  // address prefix, one match
		{"kn", "", 2},          // two addresses start with kn@
		{"mads", "users/4", 0}, // name, one match
		{"kim", "", 2},         // two names match
		{"zed", "", 0},
	}
	for _, tt := range tests {
		id, candidates := dmMatch(people, tt.who)
		if id != tt.id || len(candidates) != tt.candidates {
			t.Errorf("dmMatch(%q) = %q, %d candidates, want %q, %d", tt.who, id, len(candidates), tt.id, tt.candidates)
		}
	}
}

func TestCompleteCycles(t *testing.T) {
	m := newModel(context.Background(), &client{}, nil)
	m.spaces = []space{{name: "spaces/A"}}
	m.cur = 0
	m.members["spaces/A"] = []member{
		{id: "users/1", name: "Kim Nørgaard", email: "kn@netic.dk"},
		{id: "users/2", name: "Kim Larsen", email: "kla@netic.dk"},
	}

	m.ta.SetValue("hi @ki")
	for _, want := range []string{"hi @Kim Nørgaard ", "hi @Kim Larsen ", "hi @Kim Nørgaard "} {
		if !m.complete(1) {
			t.Fatal("no completion")
		}
		if got := m.ta.Value(); got != want {
			t.Fatalf("after tab: %q, want %q", got, want)
		}
	}
	if !m.complete(-1) || m.ta.Value() != "hi @Kim Larsen " {
		t.Errorf("shift+tab: %q", m.ta.Value())
	}
	if got := expandMentions(m.ta.Value(), m.mentions); got != "hi <users/2> " {
		t.Errorf("expanded = %q", got)
	}

	m.comp = nil
	m.ta.SetValue("/dm kl")
	if !m.complete(1) || m.ta.Value() != "/dm kla@netic.dk" {
		t.Errorf("/dm completion: %q", m.ta.Value())
	}
}

func TestWantMessage(t *testing.T) {
	m := newModel(context.Background(), &client{}, nil)
	m.spaces = []space{
		{name: "spaces/open"},
		{name: "spaces/muted", muted: true, lastActive: "2026-01-01T00:00:00Z"},
		{name: "spaces/mutedCached", muted: true},
		{name: "spaces/normal"},
	}
	m.cur = 0
	m.mem["spaces/mutedCached"] = &cachedSpace{}
	for name, want := range map[string]bool{
		"spaces/open/messages/1":        true,
		"spaces/normal/messages/1":      true,
		"spaces/mutedCached/messages/1": true,
		"spaces/unknown/messages/1":     true,
		"spaces/muted/messages/1":       false,
	} {
		if got := m.wantMessage(messageRef{kind: kindCreated, name: name, at: "2026-10-03T12:00:00Z"}); got != want {
			t.Errorf("%s: got %v", name, got)
		}
	}
	if m.spaces[1].lastActive != "2026-10-03T12:00:00Z" {
		t.Error("a skipped message didn't move the muted space's last activity")
	}
}
