package main

import (
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/golden"
	"google.golang.org/api/chat/v1"
)

// visible spells out escape sequences, one quoted line per rendered line,
// so golden files read and diff as text.
func visible(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strconv.Quote(l)
	}
	return strings.Join(lines, "\n") + "\n"
}

// TestRenderGolden pins how message bodies render, wrapped to 60 columns.
// Run with -update after an intended change and review the diff. Headers
// are left out, since their timestamps follow the local time zone.
func TestRenderGolden(t *testing.T) {
	noImages := func(string) string { return "" }
	tests := map[string]*chat.Message{
		"markup":    {Text: "*bold* _italic_ ~struck~ `code` and a long line that has to wrap across the sixty column width"},
		"codeblock": {Text: "before\n```\nfunc main() {\n\tfmt.Println(1)\n}\n```\nafter"},
		"quote": {
			Text: "agreed",
			QuotedMessageMetadata: &chat.QuotedMessageMetadata{QuotedMessageSnapshot: &chat.QuotedMessageSnapshot{
				Sender: "Alice", Text: "one\ntwo\nthree\nfour lines, so the last is cut",
			}},
		},
		"forward": {
			Text:                  "fyi",
			QuotedMessageMetadata: &chat.QuotedMessageMetadata{QuoteType: "FORWARD", QuotedMessageSnapshot: &chat.QuotedMessageSnapshot{Sender: "Bob", Text: "the original"}},
		},
		"card": {CardsV2: []*chat.CardWithId{{Card: &chat.GoogleAppsCardV1Card{
			Header: &chat.GoogleAppsCardV1CardHeader{Title: "bob v0.8.5", Subtitle: "New release"},
			Sections: []*chat.GoogleAppsCardV1Section{{
				Header: "Changes",
				Widgets: []*chat.GoogleAppsCardV1Widget{
					{TextParagraph: &chat.GoogleAppsCardV1TextParagraph{Text: "<b>Breaking</b>: flag removed<br>other fixes"}},
					{DecoratedText: &chat.GoogleAppsCardV1DecoratedText{TopLabel: "Status", Text: "passed", BottomLabel: "2m"}},
					{Divider: &chat.GoogleAppsCardV1Divider{}},
					{ButtonList: &chat.GoogleAppsCardV1ButtonList{Buttons: []*chat.GoogleAppsCardV1Button{
						{Text: "Changelog", OnClick: &chat.GoogleAppsCardV1OnClick{OpenLink: &chat.GoogleAppsCardV1OpenLink{Url: "https://example.com"}}},
					}}},
				},
			}},
		}}}},
		"attachments": {
			Text:         "files",
			Attachment:   []*chat.Attachment{{ContentType: "application/pdf", ContentName: "report.pdf"}, {ContentType: "image/png", ContentName: "shot.png"}},
			AttachedGifs: []*chat.AttachedGif{{Uri: "https://example.com/x.gif"}},
		},
		"reactions": {
			Text: "ship it",
			EmojiReactionSummaries: []*chat.EmojiReactionSummary{
				{Emoji: &chat.Emoji{Unicode: "👍"}, ReactionCount: 3},
				{Emoji: &chat.Emoji{CustomEmoji: &chat.CustomEmoji{EmojiName: "parrot"}}, ReactionCount: 1},
			},
		},
		"richlinks": {
			Text: "notes in https://docs.google.com/document/d/1 and join https://meet.google.com/abc-defg-hij",
			Annotations: []*chat.Annotation{
				{Type: "RICH_LINK", RichLinkMetadata: &chat.RichLinkMetadata{RichLinkType: "DRIVE_FILE", Uri: "https://docs.google.com/document/d/1", DriveLinkData: &chat.DriveLinkData{MimeType: "application/vnd.google-apps.document"}}},
				{Type: "RICH_LINK", RichLinkMetadata: &chat.RichLinkMetadata{RichLinkType: "MEET_SPACE", Uri: "https://meet.google.com/abc-defg-hij", MeetSpaceLinkData: &chat.MeetSpaceLinkData{Type: "MEETING", MeetingCode: "abc-defg-hij"}}},
			},
		},
	}
	for name, msg := range tests {
		t.Run(name, func(t *testing.T) {
			num := 0
			out := lipgloss.NewStyle().Width(60).Render(messageBody(msg, noImages, &num))
			golden.RequireEqual(t, visible(out))
		})
	}
}
