package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"
	"github.com/charmbracelet/x/vt"
	"google.golang.org/api/chat/v1"
)

// flow runs mutter against a fake Chat API, with its output played into a
// terminal emulator. Bubble Tea only redraws the cells that changed, so the
// emulated screen is what checks look at.
type flow struct {
	t   *testing.T
	tm  *teatest.TestModel
	f   *fakeChat
	emu *vt.SafeEmulator
}

func startFlow(t *testing.T, f *fakeChat) *flow {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("TERM_PROGRAM", "") // no images
	const w, h = 120, 40
	m := newModel(context.Background(), fakeClient(t, f), make(chan tea.Msg))
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(w, h))
	emu := vt.NewSafeEmulator(w, h)
	// The output isn't a TTY, so newlines come without the carriage return
	// a terminal driver adds. Newline mode makes a line feed return too.
	if _, err := emu.WriteString("\x1b[20h"); err != nil {
		t.Fatal(err)
	}
	// The emulator answers terminal queries through a pipe, which blocks
	// until read. It isn't closed: vt's Close races with a pending Read, so
	// the drain stays parked until the test binary exits.
	go func() { _, _ = io.Copy(io.Discard, emu) }()
	t.Cleanup(func() { _ = tm.Quit() })
	return &flow{t: t, tm: tm, f: f, emu: emu}
}

// screen plays new output into the emulator and returns the screen text.
func (fl *flow) screen() string {
	fl.t.Helper()
	if _, err := io.Copy(fl.emu, fl.tm.Output()); err != nil {
		fl.t.Fatal(err)
	}
	return fl.emu.String()
}

// see waits until the screen shows s.
func (fl *flow) see(s string) {
	fl.t.Helper()
	fl.waitFor(func() bool { return strings.Contains(fl.screen(), s) }, fmt.Sprintf("%q on screen", s))
}

func (fl *flow) key(code rune) { fl.tm.Send(tea.KeyPressMsg{Code: code}) }

func (fl *flow) typeText(s string) { fl.tm.Type(s) }

// sent waits until the fake API holds a message in space with text, and
// returns it.
func (fl *flow) sent(space, text string) *chat.Message {
	fl.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		msgs := fl.f.messages(space)
		if i := slices.IndexFunc(msgs, func(m *chat.Message) bool { return m.Text == text }); i >= 0 {
			return msgs[i]
		}
		time.Sleep(20 * time.Millisecond)
	}
	fl.t.Fatalf("no message %q in %s", text, space)
	return nil
}

func TestFlowOpenThreadAndReply(t *testing.T) {
	f := newFakeChat(t)
	f.addSpace("spaces/A", "Platform")
	root := f.post("spaces/A", "", "users/alice", "Deploy is blocked")
	f.post("spaces/A", root.Thread.Name, "users/bob", "looking into it")

	fl := startFlow(t, f)
	fl.see("Deploy is blocked")
	fl.see("1 reply")

	// Enter on an empty input opens the selected thread.
	fl.key(tea.KeyEnter)
	fl.see("looking into it")

	fl.typeText("fixed now")
	fl.key(tea.KeyEnter)
	reply := fl.sent("spaces/A", "fixed now")
	if reply.Thread.Name != root.Thread.Name {
		t.Errorf("reply went to %s, want the open thread %s", reply.Thread.Name, root.Thread.Name)
	}
	fl.see("fixed now")

	// Esc goes back to the space, where the thread now has two replies.
	fl.key(tea.KeyEscape)
	fl.see("2 replies")
}

func TestFlowNewThreadReactEditQuoteDelete(t *testing.T) {
	f := newFakeChat(t)
	f.addSpace("spaces/A", "Platform")
	root := f.post("spaces/A", "", "users/alice", "Deploy is blocked")

	fl := startFlow(t, f)
	fl.see("Deploy is blocked")

	// Text in the space view starts a thread.
	fl.typeText("separate topic")
	fl.key(tea.KeyEnter)
	topic := fl.sent("spaces/A", "separate topic")
	if topic.ThreadReply {
		t.Error("a new thread went out as a reply")
	}
	fl.see("separate topic")

	// Select alice's thread and react with the first quick pick, twice:
	// the second pick removes it.
	fl.key(tea.KeyUp)
	fl.key(tea.KeyUp)
	fl.key('r')
	fl.key('1')
	fl.see("👍")
	if n := fl.reactions(root.Name); n != 1 {
		t.Fatalf("reactions after one pick: %d", n)
	}
	fl.key('r')
	fl.key('1')
	fl.waitFor(func() bool { return fl.reactions(root.Name) == 0 }, "the second pick to remove the reaction")

	// Custom emoji come first in the search, and toggle by UID.
	fl.key('r')
	fl.typeText("party")
	fl.key(tea.KeyEnter)
	fl.see(":partyparrot: 1")
	fl.key('r')
	fl.typeText("party")
	fl.key(tea.KeyEnter)
	fl.waitFor(func() bool { return fl.reactions(root.Name) == 0 }, "the second custom pick to remove it")

	// Edit our own thread: select it, e loads its text, enter saves.
	fl.key(tea.KeyDown)
	fl.key('e')
	fl.typeText(" today")
	fl.key(tea.KeyEnter)
	fl.sent("spaces/A", "separate topic today")
	fl.see("separate topic today")

	// Quote alice in a new message.
	fl.key(tea.KeyUp)
	fl.key(tea.KeyUp)
	fl.key('q')
	fl.typeText("agreed")
	fl.key(tea.KeyEnter)
	if q := fl.sent("spaces/A", "agreed").QuotedMessageMetadata; q == nil || q.Name != root.Name {
		t.Errorf("quote metadata = %+v", q)
	}

	// Our new thread took the cursor. Delete it after confirming: any key
	// but y cancels.
	fl.key(tea.KeyUp)
	fl.key('d')
	fl.key('n')
	fl.key('d')
	fl.key('y')
	fl.waitFor(func() bool {
		return !slices.ContainsFunc(f.messages("spaces/A"), func(m *chat.Message) bool { return m.Text == "agreed" })
	}, "the confirmed delete")
	if !slices.ContainsFunc(f.messages("spaces/A"), func(m *chat.Message) bool { return m.Text == "separate topic today" }) {
		t.Error("the cancelled delete removed a message")
	}
}

func TestFlowSwitchSpace(t *testing.T) {
	f := newFakeChat(t)
	f.addSpace("spaces/A", "Platform")
	f.addSpace("spaces/B", "Incidents")
	f.post("spaces/A", "", "users/alice", "in platform")
	f.post("spaces/B", "", "users/bob", "in incidents")

	fl := startFlow(t, f)
	fl.see("in incidents") // the most recently active space opens first

	fl.tm.Send(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	fl.typeText("plat")
	fl.key(tea.KeyEnter)
	fl.see("in platform")
}

// reactions counts the reactions on a message in the fake API.
func (fl *flow) reactions(msg string) int {
	fl.f.mu.Lock()
	defer fl.f.mu.Unlock()
	return len(fl.f.reacts[msg])
}

// waitFor waits for cond, which names what's awaited in what.
func (fl *flow) waitFor(cond func() bool, what string) {
	fl.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	fl.t.Fatalf("timed out waiting for %s. Screen:\n%s", what, fl.screen())
}

func TestFlowSendCustomEmoji(t *testing.T) {
	f := newFakeChat(t)
	f.addSpace("spaces/A", "Platform")
	f.post("spaces/A", "", "users/alice", "Deploy is blocked")

	fl := startFlow(t, f)
	fl.see("Deploy is blocked")

	// Completion offers the custom emoji first, the input keeps its code,
	// and the send turns it into the token the API expands.
	fl.typeText("ship :partyp")
	fl.see("✱ partyparrot")
	fl.key(tea.KeyTab)
	fl.see("ship :partyparrot:")
	fl.key(tea.KeyEnter)
	fl.sent("spaces/A", "ship <customEmojis/ce1>")
}

func TestFlowHelp(t *testing.T) {
	f := newFakeChat(t)
	f.addSpace("spaces/A", "Platform")
	f.post("spaces/A", "", "users/alice", "Deploy is blocked")

	fl := startFlow(t, f)
	fl.see("Deploy is blocked")

	// F1 opens the overlay, and any key closes it.
	fl.key(tea.KeyF1)
	fl.see("any key closes")
	fl.key(tea.KeyEscape)
	fl.waitFor(func() bool { return !strings.Contains(fl.screen(), "any key closes") }, "the overlay to close")
	fl.see("Deploy is blocked")

	// Typing closes /help and goes to the input.
	fl.typeText("/help")
	fl.key(tea.KeyEnter)
	fl.see("/status [EMOJI] [TEXT]")
	fl.typeText("ok")
	fl.key(tea.KeyEnter)
	fl.sent("spaces/A", "ok")

	// ? is just a character, also as a whole message.
	fl.typeText("?")
	fl.key(tea.KeyEnter)
	fl.sent("spaces/A", "?")
}

func TestFlowFailedSendKeepsText(t *testing.T) {
	f := newFakeChat(t)
	f.addSpace("spaces/A", "Platform")
	f.post("spaces/A", "", "users/alice", "Deploy is blocked")

	fl := startFlow(t, f)
	fl.see("Deploy is blocked")

	f.mu.Lock()
	f.failSends = true
	f.mu.Unlock()
	fl.typeText("hello there")
	fl.key(tea.KeyEnter)
	fl.see("not sent, it's back in the input")
	fl.see("> hello there")

	// The returned text sends once the API recovers.
	f.mu.Lock()
	f.failSends = false
	f.mu.Unlock()
	fl.key(tea.KeyEnter)
	fl.sent("spaces/A", "hello there")
}

func TestFlowNextUnreadAndLinks(t *testing.T) {
	f := newFakeChat(t)
	f.addSpace("spaces/A", "Platform")
	f.addSpace("spaces/B", "Incidents")
	f.post("spaces/A", "", "users/alice", "in platform")
	f.post("spaces/B", "", "users/bob", "in incidents")

	fl := startFlow(t, f)
	fl.see("in incidents") // the most recently active space opens first

	fl.tm.Send(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	fl.see("in platform")
	fl.tm.Send(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	fl.see("no unread spaces")

	fl.key(tea.KeyUp)
	fl.key('c')
	fl.see("copied the link")
	fl.key('v')
	fl.see("viewing images needs the kitty graphics protocol")
}
