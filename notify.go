package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/sync/errgroup"
	"google.golang.org/api/chat/v1"
)

// unreadWindow bounds the startup read-state lookups. Spaces quiet for longer
// count as read, which saves one call per old space.
const unreadWindow = 30 * 24 * time.Hour

func readStateName(space string) string { return "users/me/" + space + "/spaceReadState" }

type readInfo struct {
	unread, muted bool
	lastRead      string
}

// readTimesFile is readtimes.json: when the user last read each space, as
// mutter last learned it. A space with no activity since can't be unread,
// so startup skips looking it up. Times belong to the logged-in user.
type readTimesFile struct {
	User  string            `json:"user"`
	Times map[string]string `json:"times"`
}

func loadReadTimes(user string) map[string]string {
	var f readTimesFile
	if err := readCache("readtimes.json", &f); err != nil || f.User != user || f.Times == nil {
		return map[string]string{}
	}
	return f.Times
}

// noteRead records that the user read space at lastRead.
func (m *model) noteRead(space, lastRead string) {
	if lastRead == "" || m.readTimes[space] == lastRead {
		return
	}
	m.readTimes[space] = lastRead
	m.saveReadTimes()
}

// forgetRead drops space's read time, so the next start looks it up.
func (m *model) forgetRead(space string) {
	delete(m.readTimes, space)
	m.saveReadTimes()
}

func (m *model) saveReadTimes() {
	if err := writeCache("readtimes.json", readTimesFile{User: m.c.me, Times: m.readTimes}); err != nil {
		log.Printf("read times: %v", err)
	}
}

// needsReadState reports whether startup has to look up s's read state:
// visible, active within unreadWindow, and with activity after its known
// read time, if any.
func needsReadState(s space, known map[string]string, cutoff string) bool {
	if s.hidden || s.lastActive < cutoff {
		return false
	}
	// ponytail: a space marked unread on another device while mutter was closed shows as read until its next activity. Live read-state events cover it while mutter runs.
	r, ok := known[s.name]
	return !ok || isUnread(s.lastActive, r)
}

// unread reports which of spaces have activity after the user last read
// them. Spaces whose known read time covers their last activity are read
// without a lookup. Unread spaces also get their mute setting, since the
// web client doesn't show muted spaces as unread.
func (c *client) unread(ctx context.Context, spaces []space, known map[string]string) map[string]readInfo {
	cutoff := time.Now().Add(-unreadWindow).UTC().Format(time.RFC3339)
	out := map[string]readInfo{}
	var mu sync.Mutex
	var g errgroup.Group
	g.SetLimit(8)
	for _, s := range spaces {
		if !needsReadState(s, known, cutoff) {
			continue
		}
		g.Go(func() error {
			rs, err := c.svc.Users.Spaces.GetSpaceReadState(readStateName(s.name)).Context(ctx).Do()
			if err != nil {
				log.Printf("read state %s: %v", s.name, err)
				return nil
			}
			info := readInfo{unread: isUnread(s.lastActive, rs.LastReadTime), lastRead: rs.LastReadTime}
			if info.unread {
				if set, err := c.setting(ctx, s.name); err == nil {
					info.muted = set.MuteSetting == mutedSetting
				}
			}
			mu.Lock()
			out[s.name] = info
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait() // the lookups log their own errors and return nil
	return out
}

// isUnread compares RFC 3339 timestamps by parsing them, because the API
// returns varying fractional digits that break lexical comparison.
func isUnread(lastActive, lastRead string) bool {
	a, err := time.Parse(time.RFC3339Nano, lastActive)
	if err != nil {
		return false
	}
	r, err := time.Parse(time.RFC3339Nano, lastRead)
	return err != nil || a.After(r)
}

func (c *client) markRead(ctx context.Context, space string) error {
	_, err := c.svc.Users.Spaces.UpdateSpaceReadState(readStateName(space), &chat.SpaceReadState{
		LastReadTime: time.Now().UTC().Format(time.RFC3339Nano),
	}).UpdateMask("lastReadTime").Context(ctx).Do()
	return err
}

// readState returns when the user last read space.
func (c *client) readState(ctx context.Context, space string) (string, error) {
	rs, err := c.svc.Users.Spaces.GetSpaceReadState(readStateName(space)).Context(ctx).Do()
	if err != nil {
		return "", err
	}
	return rs.LastReadTime, nil
}

// markNew flags what's new in threads for someone who last read the space
// at spaceRead. Threads with replies use their own read state, because Chat
// tracks thread replies separately from the space.
//
// known holds thread read times from the last session. A thread with no
// activity since its known read time has nothing new, so it needs no lookup.
func (c *client) markNew(ctx context.Context, threads []*thread, spaceRead string, known map[string]string) {
	cutoff := time.Now().Add(-unreadWindow).UTC().Format(time.RFC3339)
	var g errgroup.Group
	g.SetLimit(8)
	for _, t := range threads {
		t.readAt = spaceRead
		if len(t.msgs) == 1 || t.last() < cutoff {
			c.countNew(t, spaceRead)
			continue
		}
		if r, ok := known[t.name]; ok && !isUnread(t.last(), r) {
			t.readAt = r
			c.countNew(t, spaceRead)
			continue
		}
		g.Go(func() error {
			rs, err := c.svc.Users.Spaces.Threads.GetThreadReadState("users/me/" + t.name + "/threadReadState").Context(ctx).Do()
			if err == nil && rs.LastReadTime != "" {
				t.readAt = rs.LastReadTime
			}
			c.countNew(t, spaceRead)
			return nil
		})
	}
	_ = g.Wait() // the lookups log their own errors and return nil
}

func (c *client) countNew(t *thread, spaceRead string) {
	if spaceRead == "" {
		return // read state unknown, so mark nothing rather than everything
	}
	t.rootNew = c.isNew(t.msgs[0], spaceRead)
	for _, m := range t.msgs[1:] {
		if c.isNew(m, t.readAt) {
			t.unseen++
		}
	}
}

// isNew reports whether someone else posted msg after readAt.
func (c *client) isNew(msg *chat.Message, readAt string) bool {
	own := msg.Sender != nil && msg.Sender.Name == c.meID
	return readAt != "" && !own && isUnread(msg.CreateTime, readAt)
}

// setting returns the user's notification setting for space, cached because
// it changes rarely and is needed for every incoming message.
func (c *client) setting(ctx context.Context, space string) (*chat.SpaceNotificationSetting, error) {
	c.settingsMu.Lock()
	s, ok := c.settings[space]
	c.settingsMu.Unlock()
	if ok {
		return s, nil
	}
	s, err := c.svc.Users.Spaces.SpaceNotificationSetting.Get("users/me/" + space + "/spaceNotificationSetting").Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	c.settingsMu.Lock()
	c.settings[space] = s
	c.settingsMu.Unlock()
	return s, nil
}

// mentions reports whether msg @mentions the user, directly or as @all.
func mentions(msg *chat.Message, meID string) bool {
	for _, a := range msg.Annotations {
		if u := a.UserMention; u != nil && u.User != nil && (u.User.Name == meID || u.User.Name == allUsers) {
			return true
		}
	}
	return false
}

// shouldNotify mirrors the Chat notification levels. FOR_YOU also covers
// followed threads, which the API doesn't expose, so only mentions count.
func shouldNotify(s *chat.SpaceNotificationSetting, dm, mentioned, newThread bool) bool {
	if s != nil && s.MuteSetting == mutedSetting {
		return false
	}
	level := ""
	if s != nil {
		level = s.NotificationSetting
	}
	switch level {
	case "OFF":
		return false
	case "ALL":
		return true
	case "MAIN_CONVERSATIONS":
		return mentioned || newThread
	case "FOR_YOU":
		return mentioned
	}
	// Unknown setting, as for DMs, which have no levels: DMs and mentions.
	return dm || mentioned
}

// osc777 raises a desktop notification. Fields can't contain the ';'
// separator or control characters.
func osc777(title, body string) string {
	clean := func(s string) string {
		return strings.Map(func(r rune) rune {
			if r == ';' || unicode.IsControl(r) {
				return ' '
			}
			return r
		}, s)
	}
	return fmt.Sprintf("\x1b]777;notify;%s;%s\x1b\\", clean(title), clean(body))
}
