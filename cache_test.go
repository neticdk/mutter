package main

import (
	"context"
	"testing"

	"google.golang.org/api/chat/v1"
)

func cmsg(name, thread string, reply bool, sender string) *chat.Message {
	return &chat.Message{Name: name, Thread: &chat.Thread{Name: thread}, ThreadReply: reply, Sender: &chat.User{Name: sender}}
}

func TestCachedSpaceApply(t *testing.T) {
	c := &cachedSpace{threads: []*thread{{name: "spaces/A/threads/1", msgs: []*chat.Message{cmsg("spaces/A/messages/1", "spaces/A/threads/1", false, "users/x")}}}}

	if !c.apply(messageEvent{kind: kindCreated, msg: cmsg("spaces/A/messages/2", "spaces/A/threads/1", true, "users/x")}, "users/me") {
		t.Fatal("reply to a cached thread")
	}
	if len(c.threads[0].msgs) != 2 || c.threads[0].unseen != 1 {
		t.Errorf("reply not added: %+v", c.threads[0])
	}
	if !c.apply(messageEvent{kind: kindCreated, msg: cmsg("spaces/A/messages/3", "spaces/A/threads/3", false, "users/x")}, "users/me") || len(c.threads) != 2 || !c.threads[1].rootNew {
		t.Error("new thread not appended")
	}
	if c.apply(messageEvent{kind: kindCreated, msg: cmsg("spaces/A/messages/4", "spaces/A/threads/9", true, "users/x")}, "users/me") {
		t.Error("a reply to an unknown thread must drop the cache")
	}

	edited := cmsg("spaces/A/messages/2", "spaces/A/threads/1", true, "users/x")
	edited.Text = "edited"
	c.apply(messageEvent{kind: kindUpdated, msg: edited}, "users/me")
	if c.threads[0].msgs[1].Text != "edited" {
		t.Error("edit not applied")
	}
	c.apply(messageEvent{kind: kindDeleted, name: "spaces/A/messages/3"}, "users/me")
	if len(c.threads) != 1 {
		t.Error("emptied thread kept")
	}
}

func TestOpenUsesCaches(t *testing.T) {
	m := newModel(context.Background(), &client{meID: "users/me"}, nil)
	m.store = testStore(t)
	m.spaces = []space{{name: "spaces/A"}, {name: "spaces/B"}}
	m.live = true

	// A loads from the network, then B opens.
	m.open(0)
	if !m.loading {
		t.Fatal("first open should load")
	}
	next, _ := m.Update(messagesMsg{space: "spaces/A", threads: []*thread{
		{name: "spaces/A/threads/1", msgs: []*chat.Message{cmsg("spaces/A/messages/1", "spaces/A/threads/1", false, "users/x")}},
		{name: "spaces/A/threads/2", msgs: []*chat.Message{cmsg("spaces/A/messages/2", "spaces/A/threads/2", false, "users/x")}},
	}})
	m = next.(model)
	if write := m.stash(); write != nil {
		write() // what open does in the background
	}
	m.open(1)

	// A new root in A while B is open reaches the cache.
	m.applyCached(messageEvent{kind: kindCreated, msg: cmsg("spaces/A/messages/3", "spaces/A/threads/3", false, "users/x")})
	m.open(0)
	if m.loading || len(m.threads) != 3 {
		t.Fatalf("switching back fetched or missed the event: loading=%v threads=%d", m.loading, len(m.threads))
	}

	// After a restart, only the disk snapshot is left. It shows while the
	// load runs, and the load's result replaces it.
	fresh := newModel(context.Background(), &client{meID: "users/me"}, nil)
	fresh.store = m.store
	fresh.spaces = m.spaces
	fresh.open(0)
	if !fresh.loading || len(fresh.threads) != 2 {
		t.Fatalf("disk preview: loading=%v threads=%d", fresh.loading, len(fresh.threads))
	}
	// A message arrives during the load, and thread 2 was deleted while
	// mutter was closed.
	fresh.add(cmsg("spaces/A/messages/5", "spaces/A/threads/5", false, "users/x"))
	next, _ = fresh.Update(messagesMsg{space: "spaces/A", threads: []*thread{
		{name: "spaces/A/threads/1", msgs: []*chat.Message{cmsg("spaces/A/messages/1", "spaces/A/threads/1", false, "users/x")}},
	}})
	fresh = next.(model)
	var names []string
	for _, th := range fresh.threads {
		names = append(names, th.name)
	}
	if len(names) != 2 || names[0] != "spaces/A/threads/1" || names[1] != "spaces/A/threads/5" {
		t.Errorf("after load: %v, want threads 1 and 5", names)
	}
}
