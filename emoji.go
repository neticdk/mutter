package main

import (
	"cmp"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/kyokomi/emoji/v2"
	"google.golang.org/api/chat/v1"
)

// maxEmojiSuggestions is how many emoji the status line offers.
const maxEmojiSuggestions = 6

type emojiEntry struct {
	code string // without colons, such as thumbsup
	char string
	uid  string // set for custom emoji, which have no char
	name string // a custom emoji's resource name, which creating a reaction takes
}

// emojiList is every code but skin tone variants, which would crowd the
// suggestions, sorted by code. Aliases stay, so 👍 is found as thumbsup and
// as +1.
var emojiList = sync.OnceValue(func() []emojiEntry {
	var out []emojiEntry
	for code, char := range emoji.CodeMap() {
		code = strings.Trim(code, ":")
		if !strings.Contains(code, "_tone") {
			out = append(out, emojiEntry{code: code, char: char})
		}
	}
	slices.SortFunc(out, func(a, b emojiEntry) int { return strings.Compare(a.code, b.code) })
	return out
})

// suggestEmoji returns emoji whose code, or a word of it, starts with q.
// Whole-code matches come first, shortest first, so "thu" offers thumbsup
// before longer codes. Each emoji shows once, under its best match.
func suggestEmoji(q string) []emojiEntry {
	q = strings.ToLower(q)
	var whole, part []emojiEntry
	for _, e := range emojiList() {
		switch {
		case strings.HasPrefix(e.code, q):
			whole = append(whole, e)
		case slices.ContainsFunc(strings.Split(e.code, "_"), func(w string) bool { return strings.HasPrefix(w, q) }):
			part = append(part, e)
		}
	}
	byLen := func(a, b emojiEntry) int {
		return cmp.Or(cmp.Compare(len(a.code), len(b.code)), strings.Compare(a.code, b.code))
	}
	slices.SortStableFunc(whole, byLen)
	slices.SortStableFunc(part, byLen)
	var out []emojiEntry
	seen := map[string]bool{}
	for _, e := range append(whole, part...) {
		if !seen[e.char] {
			seen[e.char] = true
			out = append(out, e)
		}
		if len(out) == maxEmojiSuggestions {
			break
		}
	}
	return out
}

// emojiQuery finds a :shortcode being typed at the end of text: a colon at
// the start of a word, followed by at least two code characters.
func emojiQuery(text string) (q string, at int, ok bool) {
	at = strings.LastIndexByte(text, ':')
	if at < 0 {
		return "", 0, false
	}
	if at > 0 && !unicode.IsSpace(rune(text[at-1])) {
		return "", 0, false // a time such as 12:30, or a URL
	}
	q = text[at+1:]
	if len(q) < 2 || strings.IndexFunc(q, func(r rune) bool { return !isCodeRune(r) }) >= 0 {
		return "", 0, false
	}
	return q, at, true
}

func isCodeRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '+' || r == '-'
}

// shortcode matches a complete :code: in a message.
var shortcode = regexp.MustCompile(`:[a-zA-Z0-9_+\-]+:`)

// expandShortcodes turns complete :codes: into emoji, as the web client does.
// A code naming one of custom becomes <customEmojis/ID>, which the API turns
// into the custom emoji. Unknown codes stay as typed.
func expandShortcodes(text string, custom []*chat.CustomEmoji) string {
	codes := emoji.CodeMap()
	return shortcode.ReplaceAllStringFunc(text, func(c string) string {
		if e, ok := codes[strings.ToLower(c)]; ok {
			return e
		}
		for _, e := range custom {
			if strings.EqualFold(c, ":"+customName(e)+":") && e.Name != "" {
				return "<" + e.Name + ">"
			}
		}
		return c
	})
}

type customEmojiMsg []*chat.CustomEmoji

// loadCustomEmoji lists the organization's custom emoji. They only add to
// the picker, so a failure is logged, not shown.
func (m model) loadCustomEmoji() tea.Msg {
	var out []*chat.CustomEmoji
	err := m.c.svc.CustomEmojis.List().PageSize(200).Pages(m.ctx, func(r *chat.ListCustomEmojisResponse) error {
		out = append(out, r.CustomEmojis...)
		return nil
	})
	if err != nil {
		slog.Warn("custom emoji", "err", err)
		return nil
	}
	slog.Debug("custom emoji", "count", len(out))
	traceJSON("customEmojis", out)
	return customEmojiMsg(out)
}

// customName is a custom emoji's name without the colons the API wraps it
// in.
func customName(e *chat.CustomEmoji) string {
	return strings.Trim(cmp.Or(e.EmojiName, "custom"), ":")
}

// reactResults are the picker's matches for q: custom emoji whose name
// starts with q, then Unicode emoji.
func (m *model) reactResults(q string) []emojiEntry {
	var out []emojiEntry
	for _, e := range m.custom {
		if name := customName(e); strings.HasPrefix(name, q) && len(out) < maxEmojiSuggestions/2 {
			out = append(out, emojiEntry{code: name, uid: e.Uid, name: e.Name})
		}
	}
	for _, e := range suggestEmoji(q) {
		if len(out) == maxEmojiSuggestions {
			break
		}
		out = append(out, e)
	}
	return out
}

// emoji is the reaction emoji for a picker entry.
func (e emojiEntry) emoji() *chat.Emoji {
	if e.uid != "" {
		return &chat.Emoji{CustomEmoji: &chat.CustomEmoji{Uid: e.uid, Name: e.name}}
	}
	return &chat.Emoji{Unicode: e.char}
}
