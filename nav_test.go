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
