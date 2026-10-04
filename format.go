package main

import (
	"cmp"
	"fmt"
	"html"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf16"

	"charm.land/lipgloss/v2"
	"google.golang.org/api/chat/v1"
)

var (
	boldStyle   = lipgloss.NewStyle().Bold(true)
	italicStyle = lipgloss.NewStyle().Italic(true)
	strikeStyle = lipgloss.NewStyle().Strikethrough(true)
	codeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	// A bar marks fenced code blocks, and reads on light and dark themes
	// alike, unlike a background color.
	codeBlockStyle = codeStyle.Border(lipgloss.ThickBorder(), false, false, false, true).BorderForeground(lipgloss.Color("8")).PaddingLeft(1)
	dimStyle       = lipgloss.NewStyle().Faint(true)
)

// Chat markers only apply at word boundaries, so "a*b*c" and "snake_case_name"
// stay literal.
func marker(m string) *regexp.Regexp {
	m = regexp.QuoteMeta(m)
	return regexp.MustCompile(`(^|[^\pL\pN])` + m + `([^\s` + m + `](?:[^` + m + `\n]*[^\s` + m + `])?)` + m + `($|[^\pL\pN])`)
}

var inline = []struct {
	re    *regexp.Regexp
	style lipgloss.Style
}{
	{marker("*"), boldStyle},
	{marker("_"), italicStyle},
	{marker("~"), strikeStyle},
}

// clean drops control characters other than newline and tab from untrusted
// text, so it can't smuggle escape sequences into the terminal.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}

// stripC1 drops C1 controls from a whole frame. The renderer filters 7-bit
// sequences but passes C1 through, and names, titles and notices reach the
// frame uncleaned. The styles only emit 7-bit escapes, so none are lost.
func stripC1(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 0x80 && r <= 0x9f {
			return -1
		}
		return r
	}, s)
}

// formatText renders Google Chat markup for the terminal.
func formatText(s string) string {
	s = clean(s)
	var b strings.Builder
	for i, block := range strings.Split(s, "```") {
		if i%2 == 1 {
			// A block starts on its own line even when the fence doesn't.
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
				b.WriteString("\n")
			}
			b.WriteString(codeBlockStyle.Render(strings.Trim(block, "\n")))
			continue
		}
		for j, part := range strings.Split(block, "`") {
			if j%2 == 1 {
				b.WriteString(codeStyle.Render(part))
				continue
			}
			b.WriteString(formatInline(part))
		}
	}
	return b.String()
}

// formatInline repeats each marker until nothing changes, because a match
// consumes the boundary character after it, which the next match needs.
func formatInline(s string) string {
	for _, in := range inline {
		for {
			next := in.re.ReplaceAllStringFunc(s, func(m string) string {
				sub := in.re.FindStringSubmatch(m)
				return sub[1] + in.style.Render(sub[2]) + sub[3]
			})
			if next == s {
				break
			}
			s = next
		}
	}
	return s
}

// driveKinds names Drive MIME types for rich link chips.
var driveKinds = map[string]string{
	"application/vnd.google-apps.document":     "Google Doc",
	"application/vnd.google-apps.spreadsheet":  "Google Sheet",
	"application/vnd.google-apps.presentation": "Google Slides",
	"application/vnd.google-apps.form":         "Google Form",
	"application/vnd.google-apps.folder":       "Drive folder",
	"application/pdf":                          "PDF",
}

// richLinkChip renders a rich link as one line naming what it points to,
// or "" for plain links. The API sends no titles, only the kind and IDs.
func richLinkChip(r *chat.RichLinkMetadata) string {
	if r == nil {
		return ""
	}
	var kind string
	switch r.RichLinkType {
	case "DRIVE_FILE":
		kind = "Drive file"
		if d := r.DriveLinkData; d != nil {
			kind = cmp.Or(driveKinds[d.MimeType], kind)
		}
	case "CHAT_SPACE":
		kind = "Chat space"
		if d := r.ChatSpaceLinkData; d != nil {
			switch {
			case d.Message != "":
				kind = "Chat message"
			case d.Thread != "":
				kind = "Chat thread"
			}
		}
	case "MEET_SPACE":
		kind = "Meet"
		if d := r.MeetSpaceLinkData; d != nil {
			if d.Type == "HUDDLE" {
				kind = "Meet huddle"
				if st := strings.ToLower(d.HuddleStatus); st != "" && st != "huddle_status_unspecified" {
					kind += " " + st
				}
			}
			if d.MeetingCode != "" {
				kind += " " + clean(d.MeetingCode)
			}
		}
	case "CALENDAR_EVENT":
		kind = "Calendar event"
	case "GMAIL_MESSAGE":
		kind = "Gmail message"
	default:
		return ""
	}
	return dimStyle.Render("◆ " + kind + " · " + clean(shortURL(r.Uri)))
}

// customEmojiNames returns m's text with each custom emoji, which the API
// sends as one placeholder character, spelled out as :name:. Annotation
// indexes count UTF-16 code units.
func customEmojiNames(m *chat.Message) string {
	var anns []*chat.Annotation
	for _, a := range m.Annotations {
		if a.CustomEmojiMetadata != nil && a.CustomEmojiMetadata.CustomEmoji != nil {
			anns = append(anns, a)
		}
	}
	if len(anns) == 0 {
		return m.Text
	}
	// Splicing from the end keeps the earlier indexes valid.
	slices.SortFunc(anns, func(a, b *chat.Annotation) int { return cmp.Compare(b.StartIndex, a.StartIndex) })
	u := utf16.Encode([]rune(m.Text))
	for _, a := range anns {
		start, end := a.StartIndex, a.StartIndex+a.Length
		if start < 0 || end > int64(len(u)) || start >= end {
			continue
		}
		name := utf16.Encode([]rune(":" + customName(a.CustomEmojiMetadata.CustomEmoji) + ":"))
		u = slices.Concat(u[:start], name, u[end:])
	}
	return string(utf16.Decode(u))
}

// customEmojiText swaps each annotated custom emoji's :name: in text for
// its image, once img has it.
func customEmojiText(m *chat.Message, text string, img func(ref string) string) string {
	for _, a := range m.Annotations {
		if a.CustomEmojiMetadata == nil || a.CustomEmojiMetadata.CustomEmoji == nil {
			continue
		}
		c := a.CustomEmojiMetadata.CustomEmoji
		if s := img(emojiRef(c.Uid)); s != "" {
			text = strings.ReplaceAll(text, ":"+customName(c)+":", s)
		}
	}
	return text
}

// messageBody renders text, cards, attachments and GIFs. img renders the
// image with the given ref, or returns "" to fall back to a text placeholder.
// num counts files across a thread, numbering them in filesOf order for
// /open and /save.
func messageBody(m *chat.Message, img func(ref string) string, num *int) string {
	var parts []string
	if q := m.QuotedMessageMetadata; q != nil && q.QuotedMessageSnapshot != nil {
		parts = append(parts, quote(q))
	}
	if m.Text != "" {
		parts = append(parts, customEmojiText(m, formatText(customEmojiNames(m)), img))
	}
	for _, a := range m.Annotations {
		if c := richLinkChip(a.RichLinkMetadata); c != "" {
			parts = append(parts, c)
		}
	}
	for _, c := range m.CardsV2 {
		if c.Card != nil {
			parts = append(parts, cardStyle.Render(cardText(c.Card)))
		}
	}
	if len(parts) == 0 && m.FallbackText != "" {
		parts = append(parts, clean(m.FallbackText))
	}
	for _, a := range m.Attachment {
		*num++
		label := dimStyle.Render(fmt.Sprintf("[%d · %s]", *num, clean(cmp.Or(a.ContentName, a.ContentType))))
		if s := img(imageRef(a)); s != "" {
			parts = append(parts, s)
		}
		parts = append(parts, label)
	}
	for _, g := range m.AttachedGifs {
		*num++
		if s := img(g.Uri); s != "" {
			parts = append(parts, s)
		}
		parts = append(parts, dimStyle.Render(fmt.Sprintf("[%d · gif]", *num)))
	}
	if r := reactions(m, img); r != "" {
		parts = append(parts, r)
	}
	return strings.Join(parts, "\n")
}

var quoteStyle = lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, false, false, true).BorderForeground(lipgloss.Color("8")).PaddingLeft(1).Faint(true)

const maxQuoteLines = 3

// quote renders the quoted or forwarded message above a reply, cut to a few
// lines so the reply stays the focus.
func quote(q *chat.QuotedMessageMetadata) string {
	snap := q.QuotedMessageSnapshot
	label := clean(snap.Sender)
	if q.QuoteType == "FORWARD" {
		label = "Forwarded from " + label
	}
	lines := strings.Split(formatText(snap.Text), "\n")
	if len(lines) > maxQuoteLines {
		lines = append(lines[:maxQuoteLines], "…")
	}
	return quoteStyle.Render(boldStyle.Render(label) + "\n" + strings.Join(lines, "\n"))
}

var cardStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8")).Padding(0, 1)

// cardText renders the read-only parts of a card. Inputs, pickers and other
// interactive widgets are skipped.
func cardText(c *chat.GoogleAppsCardV1Card) string {
	var lines []string
	if h := c.Header; h != nil {
		if h.Title != "" {
			lines = append(lines, boldStyle.Render(clean(h.Title)))
		}
		if h.Subtitle != "" {
			lines = append(lines, dimStyle.Render(clean(h.Subtitle)))
		}
	}
	for _, sec := range c.Sections {
		if sec.Header != "" {
			lines = append(lines, "", boldStyle.Render(cardHTML(sec.Header)))
		}
		for _, w := range sec.Widgets {
			lines = append(lines, widgetLines(w)...)
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func widgetLines(w *chat.GoogleAppsCardV1Widget) []string {
	var lines []string
	if t := w.TextParagraph; t != nil {
		lines = append(lines, cardHTML(t.Text))
	}
	if d := w.DecoratedText; d != nil {
		if d.TopLabel != "" {
			lines = append(lines, dimStyle.Render(cardHTML(d.TopLabel)))
		}
		lines = append(lines, cardHTML(d.Text))
		if d.BottomLabel != "" {
			lines = append(lines, dimStyle.Render(cardHTML(d.BottomLabel)))
		}
	}
	if b := w.ButtonList; b != nil {
		buttons := make([]string, 0, len(b.Buttons))
		for _, btn := range b.Buttons {
			label := "[ " + clean(btn.Text) + " ]"
			if btn.OnClick != nil && btn.OnClick.OpenLink != nil && webLink(btn.OnClick.OpenLink.Url) {
				label = linkStyle.Hyperlink(btn.OnClick.OpenLink.Url).Render(label)
			}
			buttons = append(buttons, label)
		}
		lines = append(lines, strings.Join(buttons, " "))
	}
	if i := w.Image; i != nil {
		lines = append(lines, dimStyle.Render("[image: "+clean(cmp.Or(i.AltText, i.ImageUrl))+"]"))
	}
	if w.Divider != nil {
		lines = append(lines, dimStyle.Render("───"))
	}
	if c := w.Columns; c != nil {
		for _, col := range c.ColumnItems {
			for _, cw := range col.Widgets {
				lines = append(lines, widgetLines(&chat.GoogleAppsCardV1Widget{
					TextParagraph: cw.TextParagraph,
					DecoratedText: cw.DecoratedText,
					ButtonList:    cw.ButtonList,
					Image:         cw.Image,
				})...)
			}
		}
	}
	return lines
}

var (
	linkStyle = lipgloss.NewStyle().Underline(true)
	brTag     = regexp.MustCompile(`(?i)<br\s*/?>`)
	bTag      = regexp.MustCompile(`(?is)<b>(.*?)</b>`)
	iTag      = regexp.MustCompile(`(?is)<i>(.*?)</i>`)
	aTag      = regexp.MustCompile(`(?is)<a\s[^>]*href="([^"]*)"[^>]*>(.*?)</a>`)
	anyTag    = regexp.MustCompile(`<[^>]+>`)
	entity    = regexp.MustCompile(`&#?[0-9A-Za-z]+;?`)
)

// cardHTML renders the HTML subset that card text fields accept. Tags other
// than b, i, a and br are dropped and their text kept. Only http(s) links
// become clickable, because terminals hand other schemes to local apps.
func cardHTML(s string) string {
	s = brTag.ReplaceAllString(clean(s), "\n")
	s = aTag.ReplaceAllStringFunc(s, func(m string) string {
		sub := aTag.FindStringSubmatch(m)
		href := html.UnescapeString(sub[1])
		if !webLink(href) {
			return sub[2]
		}
		return linkStyle.Hyperlink(href).Render(sub[2])
	})
	s = bTag.ReplaceAllStringFunc(s, func(m string) string { return boldStyle.Render(bTag.FindStringSubmatch(m)[1]) })
	s = iTag.ReplaceAllStringFunc(s, func(m string) string { return italicStyle.Render(iTag.FindStringSubmatch(m)[1]) })

	// Entities unescape one at a time, so &#27; can't become a raw ESC
	// among the styling escapes.
	return entity.ReplaceAllStringFunc(anyTag.ReplaceAllString(s, ""), func(e string) string {
		return clean(html.UnescapeString(e))
	})
}
