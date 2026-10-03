package main

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"

	"github.com/kyokomi/emoji/v2"
)

// maxEmojiSuggestions is how many emoji the status line offers.
const maxEmojiSuggestions = 6

type emojiEntry struct {
	code string // without colons, such as thumbsup
	char string
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
// Unknown codes stay as typed.
func expandShortcodes(text string) string {
	codes := emoji.CodeMap()
	return shortcode.ReplaceAllStringFunc(text, func(c string) string {
		if e, ok := codes[strings.ToLower(c)]; ok {
			return e
		}
		return c
	})
}
