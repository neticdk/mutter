package main

import (
	"cmp"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/api/chat/v1"
)

type member struct {
	id   string // users/123
	name string
}

type membersMsg struct {
	space   string
	members []member
}

// members lists the space's human members other than the user, for
// @mention completion.
func (c *client) members(ctx context.Context, space string) ([]member, error) {
	var out []member
	err := c.svc.Spaces.Members.List(space).Filter(`member.type = "HUMAN"`).PageSize(1000).Pages(ctx, func(r *chat.ListMembershipsResponse) error {
		for _, m := range r.Memberships {
			if u := m.Member; u != nil && u.Name != c.meID && u.DisplayName != "" {
				out = append(out, member{id: u.Name, name: u.DisplayName})
			}
		}
		return nil
	})
	slices.SortFunc(out, func(a, b member) int { return strings.Compare(a.name, b.name) })
	return out, err
}

const maxMentionQuery = 40

// mentionQuery finds an @mention being typed at the end of text. It returns
// what follows the @ and where the @ is.
func mentionQuery(text string) (q string, at int, ok bool) {
	at = strings.LastIndexByte(text, '@')
	if at < 0 {
		return "", 0, false
	}
	if at > 0 {
		prev, _ := utf8.DecodeLastRuneInString(text[:at])
		if !unicode.IsSpace(prev) {
			return "", 0, false // an email address, not a mention
		}
	}
	q = text[at+1:]
	if strings.ContainsRune(q, '\n') || utf8.RuneCountInString(q) > maxMentionQuery {
		return "", 0, false
	}
	return q, at, true
}

// suggest returns members whose name, or a word of it, starts with q,
// plus @all, at most five.
func suggest(members []member, q string) []member {
	q = strings.ToLower(q)
	var out []member
	for _, m := range append(members, member{id: "users/all", name: "all"}) {
		name := strings.ToLower(m.name)
		match := strings.HasPrefix(name, q)
		for _, w := range strings.Fields(name) {
			match = match || strings.HasPrefix(w, q)
		}
		if match {
			out = append(out, m)
		}
		if len(out) == 5 {
			break
		}
	}
	return out
}

// suggestions are the completions for the mention being typed, if any.
func (m *model) suggestions() []member {
	if m.cur < 0 {
		return nil
	}
	q, _, ok := mentionQuery(m.ta.Value())
	if !ok {
		return nil
	}
	return suggest(m.members[m.spaces[m.cur].name], q)
}

// complete replaces the mention being typed with the top suggestion and
// remembers it, so the send turns it into a real mention.
func (m *model) complete() bool {
	s := m.suggestions()
	if len(s) == 0 {
		return false
	}
	text := m.ta.Value()
	_, at, _ := mentionQuery(text)
	display := "@" + s[0].name
	m.mentions[display] = "<" + s[0].id + ">"
	m.ta.SetValue(text[:at] + display + " ")
	return true
}

// expandMentions turns completed @names into Chat's <users/ID> syntax,
// longest first so "@Kim Nørgaard" wins over "@Kim".
func expandMentions(text string, mentions map[string]string) string {
	names := make([]string, 0, len(mentions))
	for n := range mentions {
		names = append(names, n)
	}
	slices.SortFunc(names, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	for _, n := range names {
		text = strings.ReplaceAll(text, n, mentions[n])
	}
	return text
}

func (m model) loadMembers(space string) tea.Cmd {
	return func() tea.Msg {
		members, err := m.c.members(m.ctx, space)
		if err != nil {
			return errMsg(err)
		}
		return membersMsg{space, members}
	}
}

// parseAttach splits "/attach PATH [text]" into the path, with ~ expanded,
// and the text. A path with spaces goes in double quotes.
func parseAttach(cmd string) (path, text string, err error) {
	rest := strings.TrimSpace(strings.TrimPrefix(cmd, "/attach"))
	if strings.HasPrefix(rest, `"`) {
		end := strings.IndexByte(rest[1:], '"')
		if end < 0 {
			return "", "", errors.New(`unclosed " in path`)
		}
		path, text = rest[1:end+1], rest[end+2:]
	} else {
		path, text, _ = strings.Cut(rest, " ")
	}
	if path == "" {
		return "", "", errors.New("usage: /attach PATH [text]")
	}
	if p, ok := strings.CutPrefix(path, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", err
		}
		path = filepath.Join(home, p)
	}
	return path, strings.TrimSpace(text), nil
}

// upload sends the file at path to space and returns the reference to
// attach to a message.
func (c *client) upload(ctx context.Context, space, path string) (*chat.AttachmentDataRef, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r, err := c.svc.Media.Upload(space, &chat.UploadAttachmentRequest{Filename: filepath.Base(path)}).Media(f).Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	return r.AttachmentDataRef, nil
}
