package main

import (
	"testing"

	"google.golang.org/api/chat/v1"
)

func TestShouldNotify(t *testing.T) {
	set := func(level, mute string) *chat.SpaceNotificationSetting {
		return &chat.SpaceNotificationSetting{NotificationSetting: level, MuteSetting: mute}
	}
	tests := []struct {
		name                     string
		s                        *chat.SpaceNotificationSetting
		dm, mentioned, newThread bool
		want                     bool
	}{
		{"muted beats mention", set("ALL", "MUTED"), false, true, true, false},
		{"all", set("ALL", "UNMUTED"), false, false, false, true},
		{"main: new thread", set("MAIN_CONVERSATIONS", ""), false, false, true, true},
		{"main: reply", set("MAIN_CONVERSATIONS", ""), false, false, false, false},
		{"for you: mention", set("FOR_YOU", ""), false, true, false, true},
		{"for you: plain", set("FOR_YOU", ""), false, false, true, false},
		{"off", set("OFF", ""), true, true, true, false},
		{"unknown: dm", nil, true, false, false, true},
		{"unknown: space", nil, false, false, true, false},
	}
	for _, tt := range tests {
		if got := shouldNotify(tt.s, tt.dm, tt.mentioned, tt.newThread); got != tt.want {
			t.Errorf("%s: got %v", tt.name, got)
		}
	}
}

func TestMentionsAndReadState(t *testing.T) {
	msg := &chat.Message{Annotations: []*chat.Annotation{{UserMention: &chat.UserMentionMetadata{User: &chat.User{Name: "users/1"}}}}}
	if !mentions(msg, "users/1") || mentions(msg, "users/2") {
		t.Error("mention detection")
	}
	// Fractional digits differ, which breaks string comparison.
	if !isUnread("2026-10-02T11:35:00.5Z", "2026-10-02T11:35:00Z") || isUnread("2026-10-02T11:35:00Z", "2026-10-02T11:35:00.5Z") {
		t.Error("isUnread")
	}
	if !isUnread("2026-10-02T11:35:00Z", "") {
		t.Error("never read should be unread")
	}
	if got := readStateSpace("users/1/spaces/AAA/spaceReadState"); got != "spaces/AAA" {
		t.Errorf("readStateSpace = %s", got)
	}
	if got := osc777("a;b", "c\nd"); got != "\x1b]777;notify;a b;c d\x1b\\" {
		t.Errorf("osc777 = %q", got)
	}
}

func TestCountNew(t *testing.T) {
	c := &client{meID: "users/me"}
	msg := func(sender, at string) *chat.Message {
		return &chat.Message{Sender: &chat.User{Name: sender}, CreateTime: at}
	}
	th := &thread{
		readAt: "2026-10-02T10:00:00Z", // the thread's own read state
		msgs: []*chat.Message{
			msg("users/a", "2026-10-02T09:00:00Z"),  // root, after the space was read
			msg("users/b", "2026-10-02T09:30:00Z"),  // reply before the thread was read
			msg("users/b", "2026-10-02T10:30:00Z"),  // new reply
			msg("users/me", "2026-10-02T11:00:00Z"), // own reply never counts
		},
	}
	c.countNew(th, "2026-10-02T08:00:00Z")
	if !th.rootNew || th.unseen != 1 {
		t.Errorf("rootNew=%v unseen=%d, want true and 1", th.rootNew, th.unseen)
	}

	unknown := &thread{msgs: th.msgs}
	c.countNew(unknown, "")
	if unknown.rootNew || unknown.unseen != 0 {
		t.Error("unknown read state should mark nothing")
	}
}
