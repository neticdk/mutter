package main

import (
	"testing"

	"google.golang.org/api/chat/v1"
)

func TestFormatText(t *testing.T) {
	b, i, s, c := boldStyle.Render, italicStyle.Render, strikeStyle.Render, codeStyle.Render
	if b("x") == "x" {
		t.Fatal("styles render without ANSI codes, so the cases below would pass vacuously")
	}

	tests := []struct{ in, want string }{
		{"plain", "plain"},
		{"*bold*", b("bold")},
		{"a *b c* d", "a " + b("b c") + " d"},
		{"_it_ and ~gone~", i("it") + " and " + s("gone")},
		{"snake_case_name", "snake_case_name"},
		{"2*3*4", "2*3*4"},
		{"* not bold *", "* not bold *"},
		{"(*x*)", "(" + b("x") + ")"},
		{"*a* *b*", b("a") + " " + b("b")},
		{"`*raw*`", c("*raw*")},
		{"```\nfn *x*\n```", codeBlockStyle.Render("fn *x*")},
		{"see ```x```", "see \n" + codeBlockStyle.Render("x")}, // a block starts on its own line
	}
	for _, tt := range tests {
		if got := formatText(tt.in); got != tt.want {
			t.Errorf("formatText(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCustomEmojiText(t *testing.T) {
	parrot := &chat.Annotation{Type: "CUSTOM_EMOJI", CustomEmojiMetadata: &chat.CustomEmojiMetadata{
		CustomEmoji: &chat.CustomEmoji{Uid: "ce1", EmojiName: ":partyparrot:"},
	}}
	loaded := func(ref string) string {
		if ref == emojiRef("ce1") {
			return "[img]"
		}
		return ""
	}
	none := func(string) string { return "" }
	for _, tc := range []struct {
		name string
		ann  []*chat.Annotation
		img  func(string) string
		want string
	}{
		{"image loaded", []*chat.Annotation{parrot}, loaded, "ship it [img] [img]"},
		{"image pending", []*chat.Annotation{parrot}, none, "ship it :partyparrot: :partyparrot:"},
		{"not annotated", nil, loaded, "ship it :partyparrot: :partyparrot:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &chat.Message{Annotations: tc.ann}
			if got := customEmojiText(m, "ship it :partyparrot: :partyparrot:", tc.img); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCustomEmojiNames(t *testing.T) {
	ann := func(start, length int64) *chat.Annotation {
		return &chat.Annotation{Type: "CUSTOM_EMOJI", StartIndex: start, Length: length, CustomEmojiMetadata: &chat.CustomEmojiMetadata{
			CustomEmoji: &chat.CustomEmoji{Uid: "ce1", EmojiName: ":partyparrot:"},
		}}
	}
	for _, tc := range []struct {
		name string
		text string
		ann  []*chat.Annotation
		want string
	}{
		{"one", "ship it \ufffc", []*chat.Annotation{ann(8, 1)}, "ship it :partyparrot:"},
		{"two", "\ufffc and \ufffc", []*chat.Annotation{ann(0, 1), ann(6, 1)}, ":partyparrot: and :partyparrot:"},
		// 👍 is two UTF-16 units, so the placeholder after it is at 3.
		{"after astral", "👍 \ufffc", []*chat.Annotation{ann(3, 1)}, "👍 :partyparrot:"},
		{"out of range", "hi", []*chat.Annotation{ann(5, 1)}, "hi"},
		{"none", "plain", nil, "plain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := customEmojiNames(&chat.Message{Text: tc.text, Annotations: tc.ann}); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
