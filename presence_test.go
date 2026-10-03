package main

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/api/chat/v1"
)

func TestParseStatus(t *testing.T) {
	tests := []struct{ in, emoji, msg string }{
		{"", "", ""},
		{"🍕 lunch", "🍕", "lunch"},
		{"in a meeting", defaultEmoji, "in a meeting"},
		{"Ørsted visit", defaultEmoji, "Ørsted visit"}, // a letter, not an emoji
	}
	for _, tt := range tests {
		e, m := parseStatus(tt.in)
		if e != tt.emoji || m != tt.msg {
			t.Errorf("parseStatus(%q) = %q, %q, want %q, %q", tt.in, e, m, tt.emoji, tt.msg)
		}
	}
}

func TestDurationArg(t *testing.T) {
	if d, err := durationArg([]string{"/dnd"}, time.Hour); err != nil || d != time.Hour {
		t.Errorf("default: %v %v", d, err)
	}
	if d, err := durationArg([]string{"/dnd", "30m"}, time.Hour); err != nil || d != 30*time.Minute {
		t.Errorf("30m: %v %v", d, err)
	}
	for _, bad := range []string{"soon", "-1h", "0s"} {
		if _, err := durationArg([]string{"/dnd", bad}, time.Hour); err == nil {
			t.Errorf("%s should fail", bad)
		}
	}
	if ttl(90*time.Minute) != "5400s" {
		t.Error("ttl")
	}
}

func TestPresenceLabel(t *testing.T) {
	if presenceLabel(&chat.Availability{State: "ACTIVE"}) != "" {
		t.Error("active should show nothing")
	}
	got := presenceLabel(&chat.Availability{
		State:        "DO_NOT_DISTURB",
		CustomStatus: &chat.CustomStatus{Emoji: &chat.Emoji{Unicode: "🍕"}, Text: "lunch\x1b[31m"},
	})
	if !strings.Contains(got, "DND") || !strings.Contains(got, "🍕 lunch") || strings.Contains(got, "lunch\x1b") {
		t.Errorf("label = %q", got)
	}
}
