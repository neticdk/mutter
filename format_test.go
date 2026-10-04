package main

import (
	"strings"
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

func TestRichLinkChip(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    *chat.RichLinkMetadata
		want string // "" means no chip
	}{
		{"none", nil, ""},
		{"doc", &chat.RichLinkMetadata{RichLinkType: "DRIVE_FILE", Uri: "https://docs.google.com/document/d/1", DriveLinkData: &chat.DriveLinkData{MimeType: "application/vnd.google-apps.document"}}, "◆ Google Doc"},
		{"other drive file", &chat.RichLinkMetadata{RichLinkType: "DRIVE_FILE", Uri: "https://drive.google.com/file/d/1", DriveLinkData: &chat.DriveLinkData{MimeType: "image/png"}}, "◆ Drive file"},
		{"chat thread", &chat.RichLinkMetadata{RichLinkType: "CHAT_SPACE", Uri: "https://chat.google.com/room/A/T", ChatSpaceLinkData: &chat.ChatSpaceLinkData{Space: "spaces/A", Thread: "spaces/A/threads/T"}}, "◆ Chat thread"},
		{"huddle", &chat.RichLinkMetadata{RichLinkType: "MEET_SPACE", Uri: "https://meet.google.com/abc", MeetSpaceLinkData: &chat.MeetSpaceLinkData{Type: "HUDDLE", HuddleStatus: "STARTED", MeetingCode: "abc-defg-hij"}}, "◆ Meet huddle started abc-defg-hij"},
		{"unknown type", &chat.RichLinkMetadata{RichLinkType: "SOMETHING_NEW", Uri: "https://x"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := richLinkChip(tc.r)
			got := visible(raw)
			if tc.want == "" {
				if raw != "" {
					t.Errorf("chip = %q, want none", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("chip = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}
