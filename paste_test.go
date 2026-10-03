package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/api/chat/v1"
)

func pasteModel(t *testing.T) model {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	m := newModel(context.Background(), &client{me: "me@example.com"}, nil)
	m.spaces = []space{{name: "spaces/A"}, {name: "spaces/B"}}
	m.cur = 0
	return m
}

func TestMultilinePaste(t *testing.T) {
	m := pasteModel(t)
	// A send would empty the input, so the whole paste still being there
	// shows nothing went out.
	next, _ := m.Update(tea.PasteMsg{Content: "line one\nline two\n\tindented"})
	m = next.(model)
	if got := m.ta.Value(); got != "line one\nline two\n    indented" {
		t.Errorf("input = %q", got)
	}
}

func TestDroppedFile(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "my shot.png")
	if err := os.WriteFile(img, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	txt := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(txt, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{
		img,
		"'" + img + "'",
		`"` + img + `"`,
		filepath.Join(dir, `my\ shot.png`),
		"file://" + filepath.ToSlash(img),
		" " + img + "\n",
	} {
		f, ok := droppedFile(in)
		if !ok || f.name != "my shot.png" || string(f.data) != "png" {
			t.Errorf("droppedFile(%q) = %q, %v", in, f.name, ok)
		}
	}
	for _, in := range []string{txt, "my shot.png", img + "\n" + img, "hello", filepath.Join(dir, "missing.png")} {
		if _, ok := droppedFile(in); ok {
			t.Errorf("droppedFile(%q) attached", in)
		}
	}
}

func TestPendingFiles(t *testing.T) {
	m := pasteModel(t)
	next, _ := m.Update(pastedMsg{name: "a.png", data: []byte("a")})
	m = next.(model)
	next, _ = m.Update(pastedMsg{name: "b.png", data: []byte("b")})
	m = next.(model)
	if m.pendingLabel() == "" || len(m.pending) != 2 {
		t.Fatalf("pending %d", len(m.pending))
	}

	next, _ = m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	m = next.(model)
	if len(m.pending) != 1 || m.pending[0].name != "a.png" {
		t.Errorf("ctrl+x left %v", m.pending)
	}

	// Enter with no text sends the image.
	next, cmd := m.submit()
	m = next.(model)
	if cmd == nil || len(m.pending) != 0 {
		t.Errorf("send with only an image: cmd %v, pending %d", cmd != nil, len(m.pending))
	}

	// Switching space drops what's pending.
	m.pending = []pending{{name: "c.png"}}
	m.open(1)
	if len(m.pending) != 0 {
		t.Error("pending file followed the switch")
	}
}

func TestEnterSendsPendingWithoutOpeningThread(t *testing.T) {
	m := pasteModel(t)
	m.threads = []*thread{{name: "spaces/A/threads/1", msgs: []*chat.Message{{Name: "spaces/A/messages/1"}}}}
	m.cursor = 0
	m.pending = []pending{{name: "a.png", data: []byte("a")}}
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if m.inThread != nil {
		t.Error("enter opened the thread")
	}
	if cmd == nil || len(m.pending) != 0 {
		t.Errorf("enter didn't send: cmd %v, pending %d", cmd != nil, len(m.pending))
	}
}
