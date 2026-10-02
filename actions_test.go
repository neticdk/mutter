package main

import (
	"strings"
	"testing"

	"google.golang.org/api/chat/v1"
)

func TestReactions(t *testing.T) {
	msg := &chat.Message{EmojiReactionSummaries: []*chat.EmojiReactionSummary{
		{Emoji: &chat.Emoji{Unicode: "👍"}, ReactionCount: 3},
		{Emoji: &chat.Emoji{CustomEmoji: &chat.CustomEmoji{EmojiName: "partyparrot"}}, ReactionCount: 1},
		{ReactionCount: 9}, // no emoji, skipped
	}}
	got := reactions(msg)
	for _, want := range []string{"👍", "3", ":partyparrot:", "1"} {
		if !strings.Contains(got, want) {
			t.Errorf("reactions = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "9") {
		t.Errorf("reactions = %q, rendered a summary without emoji", got)
	}
	if reactions(&chat.Message{}) != "" {
		t.Error("no reactions should render nothing")
	}
}
