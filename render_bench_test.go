package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/api/chat/v1"
)

// benchModel is a space with n threads of varied messages, sized like a
// busy space.
func benchModel(b testing.TB, n int) model {
	b.Helper()
	m := newModel(context.Background(), &client{meID: "users/me"}, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 50})
	m = next.(model)
	m.spaces = []space{{name: "spaces/A"}}
	m.cur = 0
	for i := range n {
		text := fmt.Sprintf("Message %d about *deploys* and _rollouts_ with `code` and a link https://example.com/%d. ", i, i) + strings.Repeat("More words to wrap across the pane. ", i%8)
		root := &chat.Message{Name: fmt.Sprintf("spaces/A/messages/%d", i), Text: text, CreateTime: "2026-10-01T10:00:00Z", Sender: &chat.User{Name: "users/x", DisplayName: "Someone Else"}}
		if i%5 == 0 {
			root.QuotedMessageMetadata = &chat.QuotedMessageMetadata{QuotedMessageSnapshot: &chat.QuotedMessageSnapshot{Sender: "Quoted", Text: "an earlier point\nover two lines"}}
		}
		t := &thread{name: fmt.Sprintf("spaces/A/threads/%d", i), msgs: []*chat.Message{root}}
		for r := range i % 4 {
			t.msgs = append(t.msgs, &chat.Message{Name: fmt.Sprintf("spaces/A/messages/%d-%d", i, r), Text: "a reply", CreateTime: "2026-10-01T11:00:00Z", Sender: &chat.User{Name: "users/y", DisplayName: "Replier"}})
		}
		m.threads = append(m.threads, t)
	}
	m.cursor = len(m.threads) - 1
	m.render()
	return m
}

// BenchmarkRender is one cursor move in a 200-thread space.
func BenchmarkRender(b *testing.B) {
	m := benchModel(b, 200)
	b.ResetTimer()
	for i := range b.N {
		m.cursor = len(m.threads) - 1 - i%len(m.threads)
		m.render()
	}
}

// BenchmarkRenderThread is one cursor move in a thread of 200 messages.
func BenchmarkRenderThread(b *testing.B) {
	m := benchModel(b, 200)
	t := &thread{name: "spaces/A/threads/big"}
	for _, th := range m.threads {
		t.msgs = append(t.msgs, th.msgs[0])
	}
	m.inThread, m.selecting = t, true
	b.ResetTimer()
	for i := range b.N {
		m.msgCursor = i % len(t.msgs)
		m.render()
	}
}
