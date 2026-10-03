package main

import (
	"slices"
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

func TestLinksOf(t *testing.T) {
	msg := &chat.Message{
		Text: "see https://a.example/x, and <https://b.example/y|the doc> or (https://a.example/x)",
		Annotations: []*chat.Annotation{
			{RichLinkMetadata: &chat.RichLinkMetadata{Uri: "https://c.example/z"}},
		},
		CardsV2: []*chat.CardWithId{{Card: &chat.GoogleAppsCardV1Card{Sections: []*chat.GoogleAppsCardV1Section{{
			Widgets: []*chat.GoogleAppsCardV1Widget{
				{TextParagraph: &chat.GoogleAppsCardV1TextParagraph{Text: `<a href="https://d.example/?a=1&amp;b=2">d</a>`}},
				{ButtonList: &chat.GoogleAppsCardV1ButtonList{Buttons: []*chat.GoogleAppsCardV1Button{
					{OnClick: &chat.GoogleAppsCardV1OnClick{OpenLink: &chat.GoogleAppsCardV1OpenLink{Url: "https://e.example"}}},
				}}},
			},
		}}}}},
	}
	want := []string{"https://a.example/x", "https://b.example/y", "https://c.example/z", "https://e.example", "https://d.example/?a=1&b=2"}
	got := linksOf(msg)
	if len(got) != len(want) {
		t.Fatalf("linksOf = %v, want %v", got, want)
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("missing %s in %v", w, got)
		}
	}
	if got[0] != want[0] || got[1] != want[1] {
		t.Errorf("text links out of order: %v", got)
	}
}

func TestLinksOfStripsControls(t *testing.T) {
	got := linksOf(&chat.Message{Text: "https://a.example/\x1b]8;;evil\x07x"})
	if len(got) != 1 || strings.ContainsAny(got[0], "\x1b\x07") {
		t.Errorf("linksOf kept control characters: %q", got)
	}
}
