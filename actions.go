package main

import (
	"cmp"
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"google.golang.org/api/chat/v1"
)

// Prompts that wait for the next key.
const (
	modeReact  = "react"
	modeDelete = "delete"
	modeLink   = "link"
	modeLeave  = "leave"
)

// maxLinks is how many links the link prompt offers, one per digit key.
const maxLinks = 9

// quickReactions are the emoji the react prompt offers on keys 1 to 6.
var quickReactions = []string{"👍", "❤️", "😂", "🎉", "👀", "✅"}

// toggleReaction adds emoji to the message as the user, or removes it when
// the user already reacted with it, and returns the updated message.
func (c *client) toggleReaction(ctx context.Context, msg string, emoji *chat.Emoji) (*chat.Message, error) {
	filter := fmt.Sprintf(`emoji.unicode = %q AND user.name = %q`, emoji.Unicode, c.meID)
	if emoji.CustomEmoji != nil {
		filter = fmt.Sprintf(`emoji.custom_emoji.uid = %q AND user.name = %q`, emoji.CustomEmoji.Uid, c.meID)
	}
	r, err := c.svc.Spaces.Messages.Reactions.List(msg).Filter(filter).Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	if len(r.Reactions) > 0 {
		for _, x := range r.Reactions {
			if _, err := c.svc.Spaces.Messages.Reactions.Delete(x.Name).Context(ctx).Do(); err != nil {
				return nil, err
			}
		}
	} else {
		// The UID is output only, so a custom emoji goes out by resource name.
		create := emoji
		if emoji.CustomEmoji != nil {
			create = &chat.Emoji{CustomEmoji: &chat.CustomEmoji{Name: emoji.CustomEmoji.Name}}
		}
		_, err := c.svc.Spaces.Messages.Reactions.Create(msg, &chat.Reaction{Emoji: create}).Context(ctx).Do()
		if err != nil {
			return nil, err
		}
	}
	return c.svc.Spaces.Messages.Get(msg).Context(ctx).Do()
}

func (c *client) edit(ctx context.Context, msg, text string) (*chat.Message, error) {
	return c.svc.Spaces.Messages.Patch(msg, &chat.Message{Text: text}).UpdateMask("text").Context(ctx).Do()
}

func (c *client) del(ctx context.Context, msg string) error {
	_, err := c.svc.Spaces.Messages.Delete(msg).Context(ctx).Do()
	return err
}

// selected is the message actions apply to: the cursor's message in a
// thread, or the selected thread's root in the space view.
func (m *model) selected() *chat.Message {
	if t := m.inThread; t != nil {
		if m.msgCursor >= 0 && m.msgCursor < len(t.msgs) {
			return t.msgs[m.msgCursor]
		}
		return nil
	}
	if m.cursor >= 0 && m.cursor < len(m.threads) {
		return m.threads[m.cursor].msgs[0]
	}
	return nil
}

func (m *model) own(msg *chat.Message) bool {
	return msg.Sender != nil && msg.Sender.Name == m.c.meID
}

// action runs the selection key k on the selected message. ok is false when
// k isn't an action key.
func (m *model) action(k string) (cmd tea.Cmd, ok bool) {
	sel := m.selected()
	if sel == nil {
		return nil, false
	}
	switch k {
	case "r":
		m.mode = modeReact
	case "d":
		if !m.own(sel) {
			m.notice = "you can only delete your own messages"
			return nil, true
		}
		m.mode = modeDelete
	case "e":
		if !m.own(sel) {
			m.notice = "you can only edit your own messages"
			return nil, true
		}
		m.editing, m.selecting = sel, false
		m.ta.SetValue(customEmojiNames(sel))
		m.render()
	case "q":
		m.quoting, m.selecting = sel, false
		m.render()
	case "u":
		return m.unreadFrom(sel.CreateTime), true
	case "y":
		m.notice = "copied the message"
		return tea.SetClipboard(cmp.Or(sel.Text, sel.FallbackText)), true
	case "l":
		links := linksOf(sel)
		switch len(links) {
		case 0:
			m.notice = "no links in this message"
		case 1:
			m.openLink(links[0])
		default:
			m.mode, m.links = modeLink, links
		}
	case "b":
		m.openLink(m.msgURL(sel))
	case "c":
		m.notice = "copied the link"
		return tea.SetClipboard(m.msgURL(sel)), true
	case "v":
		return m.viewImage(sel), true
	case "o", "s":
		files := filesOf(sel)
		if len(files) == 0 {
			m.notice = "no files in this message"
			return nil, true
		}
		op := m.openFile
		if k == "s" {
			op = m.saveToDownloads
		}
		return m.filesCmd(files, op), true
	default:
		return nil, false
	}
	return nil, true
}

// updateMode handles the key after r (pick a reaction), l (pick a link) or
// d (confirm).
func (m model) updateMode(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeReact:
		return m.updateReact(msg)
	case modeGIF:
		return m.updateGIF(msg)
	case modeFind:
		return m.updateFind(msg)
	case modeLeave:
		m.mode = ""
		if msg.String() != "y" {
			return m, nil
		}
		return m, m.leave()
	}
	mode, sel := m.mode, m.selected()
	m.mode = ""
	if sel == nil {
		return m, nil
	}
	switch mode {
	case modeLink:
		var n int
		if _, err := fmt.Sscanf(msg.String(), "%d", &n); err != nil || n < 1 || n > len(m.links) {
			return m, nil
		}
		m.openLink(m.links[n-1])
		return m, nil
	case modeDelete:
		if msg.String() != "y" {
			return m, nil
		}
		name := sel.Name
		return m, func() tea.Msg {
			if err := m.c.del(m.ctx, name); err != nil {
				return errMsg(err)
			}
			return messageEvent{kind: kindDeleted, name: name}
		}
	}
	return m, nil
}

// updateReact handles the reaction picker. Digits pick a quick reaction.
// Typing searches all emoji by name, tab and shift+tab move through the
// results, and enter picks one.
func (m model) updateReact(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	var results []emojiEntry
	if m.reactQuery != "" {
		results = m.reactResults(m.reactQuery)
	}
	switch {
	case k == keyEsc:
	case m.reactQuery == "" && len(k) == 1 && k >= "1" && k <= "9":
		if n := int(k[0] - '0'); n <= len(quickReactions) {
			return m.react(&chat.Emoji{Unicode: quickReactions[n-1]})
		}
	case k == keyEnter && len(results) > 0:
		return m.react(results[min(m.reactIdx, len(results)-1)].emoji())
	case k == "backspace":
		m.reactQuery = m.reactQuery[:max(0, len(m.reactQuery)-1)]
		m.reactIdx = 0
		return m, nil
	case tabStep(k) != 0 && len(results) > 0:
		m.reactIdx = (m.reactIdx + tabStep(k) + len(results)) % len(results)
		return m, nil
	case msg.Text != "" && strings.IndexFunc(msg.Text, func(r rune) bool { return !isCodeRune(r) }) < 0:
		m.reactQuery += strings.ToLower(msg.Text)
		m.reactIdx = 0
		var cmds []tea.Cmd
		for _, e := range m.reactResults(m.reactQuery) {
			if e.uid != "" {
				cmds = append(cmds, m.imgs.fetchEmoji(m.ctx, &chat.CustomEmoji{Uid: e.uid}))
			}
		}
		return m, tea.Batch(cmds...)
	}
	m.mode, m.reactQuery = "", ""
	return m, nil
}

// react toggles emoji on the selected message and closes the picker.
func (m model) react(emoji *chat.Emoji) (tea.Model, tea.Cmd) {
	m.mode, m.reactQuery = "", ""
	sel := m.selected()
	if sel == nil {
		return m, nil
	}
	name := sel.Name
	return m, func() tea.Msg {
		updated, err := m.c.toggleReaction(m.ctx, name, emoji)
		if err != nil {
			return errMsg(err)
		}
		return messageEvent{kind: kindUpdated, name: name, msg: updated}
	}
}

// fileCmd runs /open [n] or /save [n] on file n of the target thread, the
// last one by default.
func (m *model) fileCmd(fields []string) tea.Cmd {
	t := m.target()
	if t == nil {
		return nil
	}
	files := threadFiles(t)
	if len(files) == 0 {
		m.notice = "no files in this thread"
		return nil
	}
	n := len(files)
	if len(fields) > 1 {
		if _, err := fmt.Sscanf(fields[1], "%d", &n); err != nil || n < 1 || n > len(files) {
			m.notice = fmt.Sprintf("pick a file from 1 to %d", len(files))
			return nil
		}
	}
	op := m.openFile
	if fields[0] == "/save" {
		op = m.saveToDownloads
	}
	return m.filesCmd(files[n-1:n], op)
}

// filesCmd runs op, openFile or saveToDownloads, on each of files.
func (m *model) filesCmd(files []file, op func(file) (string, error)) tea.Cmd {
	m.notice = "fetching…"
	return func() tea.Msg {
		done := make([]string, 0, len(files))
		for _, f := range files {
			s, err := op(f)
			if err != nil {
				return errMsg(err)
			}
			done = append(done, s)
		}
		return noticeMsg(strings.Join(done, " · "))
	}
}

// openFile downloads an uploaded file to a temp directory and opens it, or
// opens a Drive file or GIF in the browser.
func (m *model) openFile(f file) (string, error) {
	if f.media == "" {
		if err := openURL(f); err != nil {
			return "", err
		}
		return "opened " + f.label + " in the browser", nil
	}
	dir, err := openDir()
	if err != nil {
		return "", err
	}
	path, err := m.c.saveFile(m.ctx, f, dir)
	if err != nil {
		return "", err
	}
	openBrowser(path)
	return "opened " + path, nil
}

// saveToDownloads saves an uploaded file. Drive files and GIFs can't be
// downloaded without a Drive scope, so they open in the browser instead.
func (m *model) saveToDownloads(f file) (string, error) {
	if f.media == "" {
		if err := openURL(f); err != nil {
			return "", err
		}
		return "opened " + f.label + " in the browser, Drive files and GIFs can't be saved from here", nil
	}
	dir, err := downloadsDir()
	if err != nil {
		return "", err
	}
	path, err := m.c.saveFile(m.ctx, f, dir)
	if err != nil {
		return "", err
	}
	return "saved " + path, nil
}

// openURL opens f's link in the browser. Links come from other people's
// messages, and open hands custom schemes to local apps, so only http(s)
// passes.
func openURL(f file) error {
	if !webLink(f.url) {
		return fmt.Errorf("won't open %s, its link isn't http(s)", f.label)
	}
	openBrowser(f.url)
	return nil
}

func webLink(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http")
}

// unreadFrom marks the open space unread from at onward. It holds off
// marking the space read again until it's reopened.
func (m *model) unreadFrom(at string) tea.Cmd {
	if m.cur < 0 {
		return nil
	}
	space := m.spaces[m.cur].name
	m.holdRead = true
	m.spaces[m.cur].unread = true
	return func() tea.Msg {
		before, err := m.c.markUnread(m.ctx, space, at)
		if err != nil {
			return errMsg(err)
		}
		return markedUnreadMsg{space, before}
	}
}

// urlInText finds URLs in message text. Chat writes labeled links as
// <url|label>, so | and > end a URL too.
var urlInText = regexp.MustCompile(`https?://[^\s<>"|]+`)

// linksOf returns the links in msg, in order and without duplicates: URLs in
// the text, link previews, and card buttons and links. It returns at most
// maxLinks.
func linksOf(msg *chat.Message) []string {
	var out []string
	add := func(u string) {
		// Links come from other people and reach the status line.
		u = strings.TrimRight(clean(u), ".,;:!?)]'")
		if u != "" && !slices.Contains(out, u) && len(out) < maxLinks {
			out = append(out, u)
		}
	}
	for _, u := range urlInText.FindAllString(msg.Text, -1) {
		add(u)
	}
	for _, a := range msg.Annotations {
		if a.RichLinkMetadata != nil {
			add(a.RichLinkMetadata.Uri)
		}
	}
	for _, c := range msg.CardsV2 {
		if c.Card == nil {
			continue
		}
		for _, sec := range c.Card.Sections {
			for _, w := range sec.Widgets {
				if w.ButtonList != nil {
					for _, b := range w.ButtonList.Buttons {
						if b.OnClick != nil && b.OnClick.OpenLink != nil {
							add(b.OnClick.OpenLink.Url)
						}
					}
				}
				if w.TextParagraph != nil {
					for _, sub := range aTag.FindAllStringSubmatch(w.TextParagraph.Text, -1) {
						add(html.UnescapeString(sub[1]))
					}
				}
			}
		}
	}
	return out
}

// openLink opens u in the browser, through the same http(s) check as files.
func (m *model) openLink(u string) {
	if err := openURL(file{label: shortURL(u), url: u}); err != nil {
		m.notice = err.Error()
		return
	}
	m.notice = "opened " + shortURL(u)
}

// shortURL trims a URL for the status line.
func shortURL(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if r := []rune(u); len(r) > 40 {
		return string(r[:39]) + "…"
	}
	return u
}

// reactions renders a message's reaction summary, such as "👍 3  🎉 1".
// Custom emoji show their image once img has it, and :name: until then.
func reactions(msg *chat.Message, img func(ref string) string) string {
	var parts []string
	for _, r := range msg.EmojiReactionSummaries {
		if r.Emoji == nil {
			continue
		}
		e := r.Emoji.Unicode
		if c := r.Emoji.CustomEmoji; e == "" && c != nil {
			e = cmp.Or(img(emojiRef(c.Uid)), ":"+customName(c)+":")
		}
		parts = append(parts, e+" "+dimStyle.Render(fmt.Sprint(r.ReactionCount)))
	}
	return strings.Join(parts, "  ")
}

// modeHint is the status line for the current interaction, or "".
func (m *model) modeHint() string {
	switch {
	case m.mode == modeReact && m.reactQuery != "":
		results := m.reactResults(m.reactQuery)
		picks := make([]string, 0, len(results))
		for i, e := range results {
			char := e.char
			if e.uid != "" {
				char = cmp.Or(m.imgs.render(emojiRef(e.uid)), "✱")
			}
			p := char + " " + e.code
			if i == m.reactIdx {
				p = boldStyle.Render(p)
			}
			picks = append(picks, p)
		}
		if len(picks) == 0 {
			picks = append(picks, dimStyle.Render("no match"))
		}
		return "react :" + m.reactQuery + " → " + strings.Join(picks, dimStyle.Render(" · ")) + dimStyle.Render(" · enter picks · tab next · esc cancels")
	case m.mode == modeReact:
		picks := make([]string, 0, len(quickReactions))
		for i, e := range quickReactions {
			picks = append(picks, fmt.Sprintf("%d %s", i+1, e))
		}
		return "react: " + strings.Join(picks, "  ") + dimStyle.Render(" · or type a name · again removes it · esc cancels")
	case m.mode == modeLeave:
		return boldStyle.Render("leave "+clean(m.spaces[m.cur].title)+"?") + dimStyle.Render(" y to confirm, any other key cancels")
	case m.mode == modeFind:
		return "find: ↑ ↓ move · enter opens the thread · esc closes"
	case m.mode == modeGIF:
		return "gif: ← → or tab move · enter attaches it to your next message · esc cancels"
	case m.mode == modeLink:
		picks := make([]string, 0, len(m.links))
		for i, l := range m.links {
			picks = append(picks, fmt.Sprintf("%d %s", i+1, shortURL(l)))
		}
		return "open: " + strings.Join(picks, "  ") + dimStyle.Render(" · any other key cancels")
	case m.mode == modeDelete:
		return boldStyle.Render("delete this message?") + dimStyle.Render(" y to confirm, any other key cancels")
	case m.editing != nil:
		return boldStyle.Render("editing") + dimStyle.Render(" · enter save · esc cancel")
	case m.quoting != nil:
		return boldStyle.Render("quoting "+senderName(m.quoting)) + dimStyle.Render(" · enter send · esc cancel")
	case m.selecting:
		return dimStyle.Render("r react · e edit · d delete · q quote · y copy · c link · b browser · l links · v view · u unread · o open · s save · esc")
	}
	return ""
}

func senderName(msg *chat.Message) string {
	if msg.Sender == nil {
		return ""
	}
	return clean(cmp.Or(msg.Sender.DisplayName, msg.Sender.Name))
}

// webURL links to a space, thread or message in the web client. Message
// IDs are "thread.message", which the web client's links split into path
// segments.
func webURL(sp space, thread, msg string) string {
	kind := "room"
	if sp.dm {
		kind = "dm"
	}
	u := "https://chat.google.com/" + kind + "/" + strings.TrimPrefix(sp.name, "spaces/")
	if id := lastSegment(msg); id != "" {
		if t, m, ok := strings.Cut(id, "."); ok {
			return u + "/" + t + "/" + m
		}
		return u + "/" + cmp.Or(lastSegment(thread), id) + "/" + id
	}
	if t := lastSegment(thread); t != "" {
		return u + "/" + t
	}
	return u
}

// lastSegment is the ID at the end of a resource name.
func lastSegment(name string) string {
	return name[strings.LastIndexByte(name, '/')+1:]
}

// msgURL is webURL for msg in the open space.
func (m *model) msgURL(msg *chat.Message) string {
	thread := ""
	if msg.Thread != nil {
		thread = msg.Thread.Name
	}
	return webURL(m.spaces[m.cur], thread, msg.Name)
}

// webCmd runs /web: the open thread, or the space, in the browser.
func (m *model) webCmd() {
	if m.cur < 0 {
		return
	}
	thread := ""
	if m.inThread != nil {
		thread = m.inThread.name
	}
	m.openLink(webURL(m.spaces[m.cur], thread, ""))
}

const needsKitty = "viewing images needs the kitty graphics protocol, as in Ghostty"

// viewImage shows msg's first image or GIF at the size of the message pane.
// ponytail: only the first image, add paging when messages with several
// images come up.
func (m *model) viewImage(msg *chat.Message) tea.Cmd {
	if !m.imgs.enabled {
		m.notice = needsKitty
		return nil
	}
	for _, a := range msg.Attachment {
		if ref := imageRef(a); ref != "" {
			return m.viewRef(ref)
		}
	}
	for _, g := range msg.AttachedGifs {
		if firstHTTPS(g.Uri) != "" {
			return m.viewRef(g.Uri)
		}
	}
	m.notice = "no image in this message"
	return nil
}

// viewRef opens the viewer on image ref: an attachment's resource name or
// a GIF's URL.
func (m *model) viewRef(ref string) tea.Cmd {
	if !m.imgs.enabled {
		m.notice = needsKitty
		return nil
	}
	lay := layout{
		cellW:   m.imgs.layout.cellW,
		cellH:   m.imgs.layout.cellH,
		maxCols: max(1, m.vp.Width()-2),
		maxRows: min(len(diacritics), max(1, m.vp.Height()-1)),
	}
	m.viewing = viewRef(ref)
	if strings.HasPrefix(ref, "https://") {
		return m.imgs.fetchURL(m.ctx, m.viewing, ref, lay)
	}
	ctx := m.ctx
	return m.imgs.fetchWith(m.viewing, lay, func() (*http.Response, error) {
		return m.c.svc.Media.Download(ref).Context(ctx).Download()
	})
}

// imageClick opens the viewer on the image clicked at x, y on screen, if
// any. Custom emoji aren't images to view.
func (m *model) imageClick(x, y int) (tea.Cmd, bool) {
	if m.sidebarShown() {
		x -= sidebarWidth + 1 // the border
	}
	row := y - 1 + m.vp.YOffset() // the header takes the first line
	lines := strings.Split(m.vp.GetContent(), "\n")
	if x < 0 || y < 1 || y > m.vp.Height() || row >= len(lines) {
		return nil, false
	}
	id, ok := placeholderAt(lines[row], x)
	if !ok {
		return nil, false
	}
	i := m.imgs.byID[id]
	if i == nil || strings.HasPrefix(i.ref, "emoji/") || strings.HasPrefix(i.ref, "view|") {
		return nil, false
	}
	return m.viewRef(i.ref), true
}

// viewerView is the image viewer's pane: the image centered, or a note
// while it loads.
func (m *model) viewerView() string {
	s := m.imgs.render(m.viewing)
	if s == "" {
		s = dimStyle.Render("loading the image…")
	}
	return lipgloss.Place(m.vp.Width(), m.vp.Height(), lipgloss.Center, lipgloss.Center, s)
}
