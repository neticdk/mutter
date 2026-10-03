package main

import (
	"context"
	"testing"
)

func TestDraftsFollowContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())           // macOS cache dir
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // Linux cache dir

	c := &client{me: "me@example.com"}
	m := newModel(context.Background(), c, nil)
	m.spaces = []space{{name: "spaces/A"}, {name: "spaces/B"}}
	m.open(0)
	m.ta.SetValue("hi @Kim")
	m.mentions["@Kim"] = "<users/1>"
	m.mentions["@Gone"] = "<users/2>" // not in the text, so not kept

	m.open(1)
	if m.ta.Value() != "" || len(m.mentions) != 0 {
		t.Fatalf("B starts with %q %v", m.ta.Value(), m.mentions)
	}
	if !m.hasDraft("spaces/A") || m.hasDraft("spaces/B") {
		t.Error("hasDraft")
	}

	m.open(0)
	if m.ta.Value() != "hi @Kim" || m.mentions["@Kim"] != "<users/1>" || len(m.mentions) != 1 {
		t.Fatalf("A restored %q %v", m.ta.Value(), m.mentions)
	}

	// A new session reads the drafts back.
	again := newModel(context.Background(), c, nil)
	if d := again.drafts["spaces/A|"]; d.Text != "hi @Kim" {
		t.Errorf("reloaded %+v", again.drafts)
	}

	// Sending empties the input and drops the draft.
	m.ta.Reset()
	m.saveDraft()
	if m.hasDraft("spaces/A") {
		t.Error("draft kept after send")
	}
	if other := newModel(context.Background(), &client{me: "else@example.com"}, nil); len(other.drafts) != 0 {
		t.Error("another user's session saw the drafts")
	}
}
