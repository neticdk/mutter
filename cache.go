package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"slices"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/api/chat/v1"
)

const (
	maxMessageCache = 50 << 20  // bytes of encrypted space snapshots on disk
	maxImageCache   = 200 << 20 // bytes of encrypted images on disk
)

// cachedSpace is a space loaded earlier in the session. Live events keep it
// current while another space is open, so switching back needs no fetch.
type cachedSpace struct {
	threads    []*thread
	olderToken string
	openRead   string
}

// snapshot is a space as stored on disk. After a restart it shows at once
// while the fresh load runs, since edits and deletions made while mutter was
// closed can only be caught by fetching again.
type snapshot struct {
	Threads []snapThread `json:"threads"`
	Older   string       `json:"older"`
}

type snapThread struct {
	Name   string          `json:"name"`
	Msgs   []*chat.Message `json:"msgs"`
	ReadAt string          `json:"readAt,omitempty"`
}

// findMsg locates a message by name in threads.
func findMsg(threads []*thread, name string) (ti, mi int, ok bool) {
	for ti, t := range threads {
		for mi, msg := range t.msgs {
			if msg.Name == name {
				return ti, mi, true
			}
		}
	}
	return 0, 0, false
}

// apply updates a cached space from a live event for the user meID. It
// reports false when the cache can't follow, for a reply to a thread outside
// its history, and the caller then drops the cache.
func (c *cachedSpace) apply(ev messageEvent, meID string) bool {
	fromOthers := ev.msg != nil && (ev.msg.Sender == nil || ev.msg.Sender.Name != meID)
	switch ev.kind {
	case kindCreated, kindUpdated:
		if ti, mi, ok := findMsg(c.threads, ev.msg.Name); ok {
			c.threads[ti].msgs[mi] = ev.msg
			return true
		}
		if ev.kind == kindUpdated {
			return true
		}
		name := threadName(ev.msg)
		if i := slices.IndexFunc(c.threads, func(t *thread) bool { return t.name == name }); i >= 0 {
			t := c.threads[i]
			t.msgs = append(t.msgs, ev.msg)
			if fromOthers {
				t.unseen++
			}
			return true
		}
		if ev.msg.ThreadReply {
			return false
		}
		c.threads = append(c.threads, &thread{name: name, msgs: []*chat.Message{ev.msg}, rootNew: fromOthers})
	case kindDeleted:
		if ti, mi, ok := findMsg(c.threads, ev.name); ok {
			t := c.threads[ti]
			t.msgs = slices.Delete(t.msgs, mi, mi+1)
			if len(t.msgs) == 0 {
				c.threads = slices.Delete(c.threads, ti, ti+1)
			}
		}
	}
	return true
}

// useMemory reports whether cached spaces can be trusted, which takes live
// updates. liveLost drops the cache when they break.
func (m *model) useMemory() bool { return m.live }

// stash keeps the open space for switching back, and stores it on disk.
func (m *model) stash() tea.Cmd {
	if m.cur < 0 || m.loading || m.loadFailed || m.threads == nil {
		return nil
	}
	name := m.spaces[m.cur].name
	if m.useMemory() {
		m.mem[name] = &cachedSpace{threads: m.threads, olderToken: m.olderToken, openRead: m.openRead}
	}
	return m.persist(name, m.threads, m.olderToken)
}

// persist encodes a space now, while the UI owns its threads, and writes it
// in the background.
func (m *model) persist(name string, threads []*thread, older string) tea.Cmd {
	if m.store == nil {
		return nil
	}
	snap := snapshot{Older: older}
	for _, t := range threads {
		snap.Threads = append(snap.Threads, snapThread{Name: t.name, Msgs: t.msgs, ReadAt: t.readAt})
	}
	plain, err := json.Marshal(snap)
	if err != nil {
		slog.Warn("cache encode", "space", name, "err", err)
		return nil
	}
	st := m.store
	return func() tea.Msg {
		if err := st.put("msgs", name, json.RawMessage(plain)); err != nil {
			slog.Warn("cache write", "space", name, "err", err)
		}
		return nil
	}
}

// restore fills the open space from memory, which is current, and reports
// true. Otherwise it shows the disk snapshot, if any, until the load
// replaces it, and reports false.
func (m *model) restore(name string) bool {
	if c := m.mem[name]; c != nil && m.useMemory() {
		m.threads, m.olderToken, m.openRead = c.threads, c.olderToken, c.openRead
		m.cursor = len(m.threads) - 1
		return true
	}
	if m.store == nil {
		return false
	}
	var snap snapshot
	if err := m.store.get("msgs", name, &snap); err != nil {
		return false
	}
	for _, t := range snap.Threads {
		if len(t.Msgs) > 0 {
			m.threads = append(m.threads, &thread{name: t.Name, msgs: t.Msgs, readAt: t.ReadAt})
		}
	}
	m.cursor = len(m.threads) - 1
	return false
}

// threadReads collects the read times of threads, for markNew.
func threadReads(threads []*thread) map[string]string {
	out := map[string]string{}
	for _, t := range threads {
		if t.readAt != "" {
			out[t.name] = t.readAt
		}
	}
	return out
}

// applyCached updates a space that isn't open from a live event.
func (m *model) applyCached(ev messageEvent) {
	name := ev.name
	if ev.msg != nil {
		name = ev.msg.Name
	}
	space := spaceOf(name)
	c := m.mem[space]
	if c == nil || (m.cur >= 0 && m.spaces[m.cur].name == space) {
		return
	}
	if !c.apply(ev, m.c.meID) {
		delete(m.mem, space)
	}
}

// rootsOf returns each thread's root message.
func rootsOf(threads []*thread) []*chat.Message {
	roots := make([]*chat.Message, 0, len(threads))
	for _, t := range threads {
		roots = append(roots, t.msgs[0])
	}
	return roots
}

// liveLost drops the memory cache when live updates break, since events
// missed in the gap would leave it stale.
func (m *model) liveLost() { clear(m.mem) }

// storedSpace is a space in the cached space list.
type storedSpace struct {
	Name       string `json:"name"`
	Title      string `json:"title"`
	LastActive string `json:"lastActive"`
	DM         bool   `json:"dm"`
}

// saveSpaces keeps the space list, so a start without network still shows
// the spaces.
func (m *model) saveSpaces(spaces []space) tea.Cmd {
	st := m.store
	if st == nil {
		return nil
	}
	out := make([]storedSpace, 0, len(spaces))
	for _, s := range spaces {
		out = append(out, storedSpace{s.name, s.title, s.lastActive, s.dm})
	}
	return func() tea.Msg {
		if err := st.put("meta", "spaces", out); err != nil {
			slog.Warn("space list cache write", "err", err)
		}
		return nil
	}
}

// cachedSpaces reads the space list saveSpaces kept.
func cachedSpaces(st *store) ([]space, error) {
	if st == nil {
		return nil, errors.New("no cache")
	}
	var in []storedSpace
	if err := st.get("meta", "spaces", &in); err != nil {
		return nil, err
	}
	out := make([]space, 0, len(in))
	for _, s := range in {
		out = append(out, space{name: s.Name, title: s.Title, lastActive: s.LastActive, dm: s.DM})
	}
	return out, nil
}
