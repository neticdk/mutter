package main

import (
	"fmt"
	"testing"
	"time"

	"google.golang.org/api/chat/v1"
)

func TestGroupThreads(t *testing.T) {
	msg := func(name, thread string, reply bool) *chat.Message {
		return &chat.Message{Name: name, Thread: &chat.Thread{Name: thread}, ThreadReply: reply}
	}
	got := groupThreads([]*chat.Message{
		msg("m1", "a", true), // reply whose root is outside the window
		msg("m2", "b", false),
		msg("m3", "a", true),
		msg("m4", "b", true),
		{Name: "m5"}, // no thread falls back to its own name
	})

	want := map[string][]string{"a": {"m1", "m3"}, "b": {"m2", "m4"}, "m5": {"m5"}}
	if len(got) != len(want) {
		t.Fatalf("got %d threads, want %d", len(got), len(want))
	}
	for i, name := range []string{"a", "b", "m5"} {
		if got[i].name != name {
			t.Fatalf("thread %d = %s, want %s", i, got[i].name, name)
		}
		for j, m := range got[i].msgs {
			if m.Name != want[name][j] {
				t.Errorf("thread %s msg %d = %s, want %s", name, j, m.Name, want[name][j])
			}
		}
	}
	if !got[0].msgs[0].ThreadReply {
		t.Error("thread a should start with a reply, which is what triggers the root fetch")
	}
}

func TestTitleCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())           // macOS cache dir
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // Linux cache dir

	in := map[string]string{"spaces/a": "@Alice", "spaces/gone": ""}
	if err := saveTitleCache("me@example.com", in, map[string]int64{"spaces/a": 42}); err != nil {
		t.Fatal(err)
	}
	got, checked := loadTitleCache("me@example.com")
	if checked["spaces/a"] != 42 {
		t.Errorf("checked = %v", checked)
	}
	if len(got) != 2 || got["spaces/a"] != "@Alice" {
		t.Errorf("round trip = %v", got)
	}
	if v, ok := got["spaces/gone"]; !ok || v != "" {
		t.Errorf("hidden marker lost: %q, %v", v, ok)
	}
	if other, _ := loadTitleCache("someone@example.com"); len(other) != 0 {
		t.Errorf("another user's cache was used: %v", other)
	}
}

func TestTitlesToRefresh(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-time.Hour).Unix()
	old := now.Add(-2 * titleTTL).Unix()
	spaces := []space{
		{name: "spaces/named", title: "Platform"}, // has a display name
		{name: "spaces/missing"},
		{name: "spaces/fresh"},
	}
	titles := map[string]string{"spaces/fresh": "@Kim"}
	checked := map[string]int64{"spaces/fresh": fresh}
	for i := range maxStaleTitles + 5 {
		name := fmt.Sprintf("spaces/old%d", i)
		spaces = append(spaces, space{name: name})
		titles[name] = "@Old"
		checked[name] = old
	}

	got := titlesToRefresh(spaces, titles, checked, now)
	if len(got) != 1+maxStaleTitles {
		t.Fatalf("picked %d, want the missing one and %d stale", len(got), maxStaleTitles)
	}
	if got[0].name != "spaces/missing" || got[1].name != "spaces/old0" {
		t.Errorf("order: %s, %s", got[0].name, got[1].name)
	}
	for _, s := range got {
		if s.name == "spaces/fresh" || s.name == "spaces/named" {
			t.Errorf("picked %s", s.name)
		}
	}
}
