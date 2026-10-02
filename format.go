package main

import (
	"cmp"
	"fmt"
	"html"
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
	"google.golang.org/api/chat/v1"
)

var (
	boldStyle   = lipgloss.NewStyle().Bold(true)
	italicStyle = lipgloss.NewStyle().Italic(true)
	strikeStyle = lipgloss.NewStyle().Strikethrough(true)
	codeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	dimStyle    = lipgloss.NewStyle().Faint(true)
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

// formatText renders Google Chat markup for the terminal.
func formatText(s string) string {
	var b strings.Builder
	for i, block := range strings.Split(s, "```") {
		if i%2 == 1 {
			b.WriteString(codeStyle.Render(strings.Trim(block, "\n")))
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

func formatInline(s string) string {
	for _, in := range inline {
		s = in.re.ReplaceAllStringFunc(s, func(m string) string {
			sub := in.re.FindStringSubmatch(m)
			return sub[1] + in.style.Render(sub[2]) + sub[3]
		})
	}
	return s
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
		parts = append(parts, formatText(m.Text))
	}
	for _, c := range m.CardsV2 {
		if c.Card != nil {
			parts = append(parts, cardStyle.Render(cardText(c.Card)))
		}
	}
	if len(parts) == 0 && m.FallbackText != "" {
		parts = append(parts, m.FallbackText)
	}
	for _, a := range m.Attachment {
		*num++
		label := dimStyle.Render(fmt.Sprintf("[%d · %s]", *num, cmp.Or(a.ContentName, a.ContentType)))
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
	if r := reactions(m); r != "" {
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
	label := snap.Sender
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
			lines = append(lines, boldStyle.Render(h.Title))
		}
		if h.Subtitle != "" {
			lines = append(lines, dimStyle.Render(h.Subtitle))
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
			label := "[ " + btn.Text + " ]"
			if btn.OnClick != nil && btn.OnClick.OpenLink != nil {
				label = linkStyle.Hyperlink(btn.OnClick.OpenLink.Url).Render(label)
			}
			buttons = append(buttons, label)
		}
		lines = append(lines, strings.Join(buttons, " "))
	}
	if i := w.Image; i != nil {
		lines = append(lines, dimStyle.Render("[image: "+cmp.Or(i.AltText, i.ImageUrl)+"]"))
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
)

// cardHTML renders the HTML subset that card text fields accept. Tags other
// than b, i, a and br are dropped and their text kept.
func cardHTML(s string) string {
	s = brTag.ReplaceAllString(s, "\n")
	s = aTag.ReplaceAllStringFunc(s, func(m string) string {
		sub := aTag.FindStringSubmatch(m)
		return linkStyle.Hyperlink(html.UnescapeString(sub[1])).Render(sub[2])
	})
	s = bTag.ReplaceAllStringFunc(s, func(m string) string { return boldStyle.Render(bTag.FindStringSubmatch(m)[1]) })
	s = iTag.ReplaceAllStringFunc(s, func(m string) string { return italicStyle.Render(iTag.FindStringSubmatch(m)[1]) })
	return html.UnescapeString(anyTag.ReplaceAllString(s, ""))
}
