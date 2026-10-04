package main

import (
	"context"
	"testing"
	"time"

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
	if got := osc777("a;b", "c\nd\u009ce"); got != "\x1b]777;notify;a b;c d e\x1b\\" {
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

func TestNeedsReadState(t *testing.T) {
	cutoff := "2026-09-01T00:00:00Z"
	known := map[string]string{"spaces/read": "2026-10-02T12:00:00Z", "spaces/behind": "2026-10-01T00:00:00Z"}
	tests := []struct {
		s    space
		want bool
	}{
		{space{name: "spaces/read", lastActive: "2026-10-02T11:00:00Z"}, false},   // read since its last activity
		{space{name: "spaces/behind", lastActive: "2026-10-02T11:00:00Z"}, true},  // activity after the known read
		{space{name: "spaces/unknown", lastActive: "2026-10-02T11:00:00Z"}, true}, // never seen
		{space{name: "spaces/quiet", lastActive: "2026-08-01T00:00:00Z"}, false},  // outside the window
		{space{name: "spaces/hidden", lastActive: "2026-10-02T11:00:00Z", hidden: true}, false},
	}
	for _, tt := range tests {
		if got := needsReadState(tt.s, known, cutoff); got != tt.want {
			t.Errorf("%s: got %v", tt.s.name, got)
		}
	}
}

func TestMarkNewSkipsKnownThreads(t *testing.T) {
	c := &client{meID: "users/me"} // no API service: a lookup would panic
	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	th := &thread{name: "spaces/A/threads/1", msgs: []*chat.Message{
		{Name: "m1", Sender: &chat.User{Name: "users/x"}, CreateTime: at(-3 * time.Hour)},
		{Name: "m2", Sender: &chat.User{Name: "users/x"}, CreateTime: at(-2 * time.Hour)},
	}}
	c.markNew(context.Background(), []*thread{th}, at(-4*time.Hour), map[string]string{th.name: at(-time.Hour)})
	if th.readAt != at(-time.Hour) || th.unseen != 0 {
		t.Errorf("readAt %s unseen %d, want the known read time and nothing new", th.readAt, th.unseen)
	}
}

func TestThreadRead(t *testing.T) {
	c := &client{meID: "users/me"}
	msg := func(sender, at string) *chat.Message {
		return &chat.Message{Sender: &chat.User{Name: sender}, CreateTime: at}
	}
	msgs := []*chat.Message{
		msg("users/a", "2026-10-02T09:00:00Z"), // root
		msg("users/b", "2026-10-02T10:30:00Z"),
		msg("users/b", "2026-10-02T11:30:00Z"),
	}
	fresh := func() *thread {
		return &thread{msgs: msgs, readAt: "2026-10-02T08:00:00Z", rootNew: true, unseen: 2}
	}
	for _, tc := range []struct {
		name     string
		lastRead string
		changed  bool
		rootNew  bool
		unseen   int
	}{
		{"read everything elsewhere", "2026-10-02T12:00:00Z", true, false, 0},
		{"read up to the first reply", "2026-10-02T11:00:00Z", true, false, 1},
		{"older than what mutter knows", "2026-10-02T07:00:00Z", false, true, 2},
		{"empty", "", false, true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			th := fresh()
			if got := c.threadRead(th, tc.lastRead); got != tc.changed {
				t.Errorf("changed = %v, want %v", got, tc.changed)
			}
			if th.rootNew != tc.rootNew || th.unseen != tc.unseen {
				t.Errorf("rootNew=%v unseen=%d, want %v and %d", th.rootNew, th.unseen, tc.rootNew, tc.unseen)
			}
		})
	}
	if got := readStateThread("users/1/spaces/AAA/threads/T1/threadReadState"); got != "spaces/AAA/threads/T1" {
		t.Errorf("readStateThread = %s", got)
	}
}
