package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
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

// suggestion is one completion candidate: what the status line shows and
// what tab inserts. A mention also records how the send expands it.
type suggestion struct {
	label   string
	insert  string
	mention string // "@Name", empty unless a mention
	token   string // "<users/ID>" for the mention
}

// completion is an active tab cycle through suggestions.
type completion struct {
	list  []suggestion
	idx   int
	start int    // where the completed text begins in the input
	text  string // the input right after the last completion
}

// Completion kinds.
const (
	completeMention = iota
	completeDM
	completeEmoji
	completeCommand
)

// completionQuery finds what's being completed at the end of text: the
// argument of /dm, an @mention or a :shortcode. When both a mention and a
// shortcode are open, the later one wins.
func completionQuery(text string) (q string, start, kind int, ok bool) {
	if rest, found := strings.CutPrefix(text, "/dm "); found && rest != "" && !strings.ContainsRune(rest, '\n') {
		return rest, len("/dm "), completeDM, true
	}
	if strings.HasPrefix(text, "/") && !strings.ContainsAny(text, " \t\n") {
		return text, 0, completeCommand, true
	}
	mq, mat, mok := mentionQuery(text)
	eq, eat, eok := emojiQuery(text)
	switch {
	case eok && (!mok || eat > mat):
		return eq, eat, completeEmoji, true
	case mok:
		return mq, mat, completeMention, true
	}
	return "", 0, 0, false
}

// suggestions returns the completion candidates and the highlighted one:
// the active cycle while the input is unchanged since the last tab,
// otherwise fresh matches for what's being typed.
func (m *model) suggestions() ([]suggestion, int) {
	if c := m.comp; c != nil && m.ta.Value() == c.text {
		return c.list, c.idx
	}
	q, _, kind, ok := completionQuery(m.ta.Value())
	if !ok || m.cur < 0 {
		return nil, 0
	}
	var out []suggestion
	switch kind {
	case completeDM:
		for _, p := range suggestPeople(m.knownPeople(), q) {
			label := p.name
			if p.email != "" {
				label += " " + dimStyle.Render(p.email)
			}
			out = append(out, suggestion{label: label, insert: cmp.Or(p.email, p.name)})
		}
	case completeCommand:
		cmds := suggestCommands(q)
		for _, c := range cmds[:min(len(cmds), maxCommandSuggestions)] {
			label := c[0]
			if len(cmds) == 1 {
				label += " " + dimStyle.Render(c[1])
			}
			out = append(out, suggestion{label: label, insert: c[0] + " "})
		}
	case completeMention:
		for _, p := range suggest(m.members[m.spaces[m.cur].name], q) {
			out = append(out, suggestion{label: "@" + p.name, insert: "@" + p.name + " ", mention: "@" + p.name, token: "<" + p.id + ">"})
		}
		// @all notifies everyone in a space, which a DM doesn't need.
		if !m.spaces[m.cur].dm && strings.HasPrefix("all", strings.ToLower(q)) { //nolint:gocritic // what's typed is a prefix of "all", not the reverse
			out = append(out, suggestion{label: "@all", insert: "@all ", mention: "@all", token: "<" + allUsers + ">"})
		}
	case completeEmoji:
		for _, e := range m.reactResults(q) {
			if e.uid != "" {
				// The input keeps the readable code, and the send expands it.
				char := cmp.Or(m.imgs.render(emojiRef(e.uid)), "✱")
				out = append(out, suggestion{label: char + " " + dimStyle.Render(e.code), insert: ":" + e.code + ": "})
				continue
			}
			out = append(out, suggestion{label: e.char + " " + dimStyle.Render(e.code), insert: e.char + " "})
		}
	}
	return out, 0
}

// complete inserts a suggestion. The first tab inserts the top one, and
// further tabs, step 1, or shift+tabs, step -1, cycle through the rest.
// Completed @mentions are remembered, so the send turns them into real
// mentions.
func (m *model) complete(step int) bool {
	if c := m.comp; c != nil && m.ta.Value() == c.text {
		c.idx = (c.idx + step + len(c.list)) % len(c.list)
	} else {
		list, _ := m.suggestions()
		if len(list) == 0 {
			return false
		}
		_, start, _, _ := completionQuery(m.ta.Value())
		m.comp = &completion{list: list, start: start}
		if step < 0 {
			m.comp.idx = len(list) - 1
		}
	}
	c := m.comp
	x := c.list[c.idx]
	if x.mention != "" {
		m.mentions[x.mention] = x.token
	}
	c.text = m.ta.Value()[:c.start] + x.insert
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
			// Members only feed completion, so a failure stays in the log.
			slog.Warn("members", "space", space, "err", err)
			return nil
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
	return c.uploadData(ctx, space, filepath.Base(path), f)
}

// uploadData sends data named name to space.
func (c *client) uploadData(ctx context.Context, space, name string, data io.Reader) (*chat.AttachmentDataRef, error) {
	r, err := c.svc.Media.Upload(space, &chat.UploadAttachmentRequest{Filename: name}).Media(data).Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	return r.AttachmentDataRef, nil
}

type editedMsg struct {
	text string
	err  error
}

// editDraft opens the input in $VISUAL or $EDITOR, vi by default, and puts
// the result back when the editor exits. The temp file is private and
// removed afterwards.
func (m *model) editDraft() tea.Cmd {
	f, err := os.CreateTemp("", "mutter-*.md")
	if err != nil {
		return func() tea.Msg { return errMsg(err) }
	}
	path := f.Name()
	_, werr := f.WriteString(m.ta.Value())
	if err := errors.Join(werr, f.Close()); err != nil {
		_ = os.Remove(path) // best effort, the write already failed
		return func() tea.Msg { return errMsg(err) }
	}
	editor := strings.Fields(cmp.Or(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi"))
	cmd := exec.Command(editor[0], append(editor[1:], path)...) // #nosec G204 G702 -- the editor is the user's own setting
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		defer os.Remove(path)
		if err != nil {
			return editedMsg{err: fmt.Errorf("editor: %w", err)}
		}
		b, err := os.ReadFile(path) // #nosec G304 -- the temp file created above
		return editedMsg{strings.TrimRight(string(b), "\n"), err}
	})
}

// maxCommandSuggestions keeps command suggestions on the status line.
const maxCommandSuggestions = 8

// suggestCommands returns the commands starting with prefix, with their
// help text, in the help overlay's order. A help row such as "/read [all]"
// or "/mute /unmute" names one or more commands.
func suggestCommands(prefix string) [][2]string {
	var out [][2]string
	for _, row := range helpCommands {
		for _, w := range strings.Fields(row[0]) {
			if strings.HasPrefix(w, "/") && strings.HasPrefix(w, prefix) {
				out = append(out, [2]string{w, row[1]})
			}
		}
	}
	return out
}
