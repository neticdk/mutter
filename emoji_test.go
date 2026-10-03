package main

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/api/chat/v1"
)

func TestSuggestEmoji(t *testing.T) {
	got := suggestEmoji("thu")
	if len(got) == 0 || got[0].char != "👍" {
		t.Fatalf("thu → %v, want 👍 first", got)
	}
	for _, e := range suggestEmoji("hand") {
		if strings.Contains(e.code, "_tone") {
			t.Errorf("skin tone variant offered: %s", e.code)
		}
	}
	// A word inside a code matches too.
	if got := suggestEmoji("check"); len(got) == 0 {
		t.Error("check found nothing")
	}
}

func TestEmojiQuery(t *testing.T) {
	tests := []struct {
		in string
		q  string
		ok bool
	}{
		{"nice :ta", "ta", true},
		{":+1", "+1", true},
		{"at 12:30", "", false},            // not at a word start
		{"see :t", "", false},              // too short
		{"done :tada: ok", "", false},      // already complete, then more text
		{"https://example.com", "", false}, // a URL
	}
	for _, tt := range tests {
		q, _, ok := emojiQuery(tt.in)
		if q != tt.q || ok != tt.ok {
			t.Errorf("emojiQuery(%q) = %q, %v", tt.in, q, ok)
		}
	}
}

func TestExpandShortcodes(t *testing.T) {
	if got := expandShortcodes("ship it :tada: at 12:30:00 :nope:"); got != "ship it 🎉 at 12:30:00 :nope:" {
		t.Errorf("expandShortcodes = %q", got)
	}
}

func TestEmojiCompletionAndPicker(t *testing.T) {
	m := newModel(context.Background(), &client{meID: "users/me"}, nil)
	m.spaces = []space{{name: "spaces/A"}}
	m.cur = 0
	m.ta.SetValue("nice :tad")
	if !m.complete(1) || m.ta.Value() != "nice 🎉 " {
		t.Fatalf("emoji completion: %q", m.ta.Value())
	}

	// The picker: typing searches, enter picks, and the picker closes.
	m.threads = []*thread{{name: "spaces/A/threads/1", msgs: []*chat.Message{{Name: "spaces/A/messages/1"}}}}
	m.cursor = 0
	m.mode = modeReact
	for _, r := range "tad" {
		next, _ := m.updateReact(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = next.(model)
	}
	if m.reactQuery != "tad" {
		t.Fatalf("query = %q", m.reactQuery)
	}
	next, cmd := m.updateReact(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if cmd == nil || m.mode != "" || m.reactQuery != "" {
		t.Errorf("enter: cmd %v, mode %q, query %q", cmd != nil, m.mode, m.reactQuery)
	}
}
