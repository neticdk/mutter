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
	id    string // users/123
	name  string
	email string
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
				// Names and addresses reach the status line in suggestions.
				out = append(out, member{id: u.Name, name: clean(u.DisplayName), email: clean(u.Email)})
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
	var out []member
	for _, m := range append(members, member{id: allUsers, name: "all"}) {
		if nameMatches(m.name, q) {
			out = append(out, m)
		}
		if len(out) == 5 {
			break
		}
	}
	return out
}

// nameMatches reports whether name, or a word of it, starts with q,
// ignoring case.
func nameMatches(name, q string) bool {
	name, q = strings.ToLower(name), strings.ToLower(q)
	if strings.HasPrefix(name, q) {
		return true
	}
	for w := range strings.FieldsSeq(name) {
		if strings.HasPrefix(w, q) {
			return true
		}
	}
	return false
}

// completion is an active tab cycle through suggestions.
type completion struct {
	list  []member
	idx   int
	start int    // where the completed text begins in the input
	dm    bool   // completing a /dm argument, not an @mention
	text  string // the input right after the last completion
}

// completionQuery finds what's being completed at the end of text: the
// argument of /dm, or an @mention.
func completionQuery(text string) (q string, start int, dm, ok bool) {
	if rest, found := strings.CutPrefix(text, "/dm "); found && rest != "" && !strings.ContainsRune(rest, '\n') {
		return rest, len("/dm "), true, true
	}
	q, at, ok := mentionQuery(text)
	return q, at, false, ok
}

// suggestions returns the completion candidates and the highlighted one:
// the active cycle while the input is unchanged since the last tab,
// otherwise fresh matches for what's being typed.
func (m *model) suggestions() ([]member, int, bool) {
	if c := m.comp; c != nil && m.ta.Value() == c.text {
		return c.list, c.idx, c.dm
	}
	q, _, dm, ok := completionQuery(m.ta.Value())
	if !ok || m.cur < 0 {
		return nil, 0, false
	}
	if dm {
		return suggestPeople(m.knownPeople(), q), 0, true
	}
	return suggest(m.members[m.spaces[m.cur].name], q), 0, false
}

// complete inserts a suggestion. The first tab inserts the top one, and
// further tabs, step 1, or shift+tabs, step -1, cycle through the rest.
// Completed @mentions are remembered, so the send turns them into real
// mentions.
func (m *model) complete(step int) bool {
	if c := m.comp; c != nil && m.ta.Value() == c.text {
		c.idx = (c.idx + step + len(c.list)) % len(c.list)
	} else {
		list, _, dm := m.suggestions()
		if len(list) == 0 {
			return false
		}
		_, start, _, _ := completionQuery(m.ta.Value())
		m.comp = &completion{list: list, start: start, dm: dm}
		if step < 0 {
			m.comp.idx = len(list) - 1
		}
	}
	c := m.comp
	x := c.list[c.idx]
	ins := cmp.Or(x.email, x.name)
	if !c.dm {
		ins = "@" + x.name + " "
		m.mentions["@"+x.name] = "<" + x.id + ">"
	}
	c.text = m.ta.Value()[:c.start] + ins
	m.ta.SetValue(c.text)
	return true
}

// tabStep is the completion direction for key k: 1 for tab, -1 for
// shift+tab, 0 for any other key.
func tabStep(k string) int {
	switch k {
	case "tab":
		return 1
	case "shift+tab":
		return -1
	}
	return 0
}

// knownPeople is every member of the spaces loaded this session, once each.
func (m *model) knownPeople() []member {
	var out []member
	seen := map[string]bool{}
	for _, ms := range m.members {
		for _, x := range ms {
			if !seen[x.id] {
				seen[x.id] = true
				out = append(out, x)
			}
		}
	}
	slices.SortFunc(out, func(a, b member) int { return strings.Compare(a.name, b.name) })
	return out
}

// suggestPeople matches q against names, as suggest does, and against the
// start of email addresses, at most five and without @all.
func suggestPeople(people []member, q string) []member {
	lq := strings.ToLower(q)
	var out []member
	for _, p := range people {
		if strings.HasPrefix(strings.ToLower(p.email), lq) || nameMatches(p.name, q) {
			out = append(out, p)
		}
		if len(out) == 5 {
			break
		}
	}
	return out
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
	f, err := os.Open(path) // #nosec G304 -- the user picked this file to upload
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
