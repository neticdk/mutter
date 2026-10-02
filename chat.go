package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/sync/errgroup"
	"google.golang.org/api/chat/v1"
	oauth2api "google.golang.org/api/oauth2/v2"
	"google.golang.org/api/option"
)

type client struct {
	svc  *chat.Service
	me   string // email of the logged-in user
	meID string // users/{id}, the same ID Google accounts use

	settingsMu sync.Mutex
	settings   map[string]*chat.SpaceNotificationSetting // by space
}

type space struct {
	name       string // spaces/AAA
	title      string
	lastActive string // RFC 3339, sorts lexically
	hidden     bool   // DM or group chat whose other members are all deleted
	dm         bool
	unread     bool
	muted      bool
	lastRead   string // from the startup scan, consumed when the space opens
}

func newClient(ctx context.Context, ts oauth2.TokenSource) (*client, error) {
	svc, err := chat.NewService(ctx, option.WithTokenSource(ts))
	if err != nil {
		return nil, err
	}
	ui, err := oauth2api.NewService(ctx, option.WithTokenSource(ts))
	if err != nil {
		return nil, err
	}
	info, err := ui.Userinfo.Get().Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	log.Printf("user: %s users/%s", info.Email, info.Id)
	return &client{svc: svc, me: info.Email, meID: "users/" + info.Id, settings: map[string]*chat.SpaceNotificationSetting{}}, nil
}

// spaces lists the user's spaces, most recently active first.
func (c *client) spaces(ctx context.Context) ([]space, error) {
	var out []space
	err := c.svc.Spaces.List().PageSize(1000).Pages(ctx, func(r *chat.ListSpacesResponse) error {
		for _, s := range r.Spaces {
			debugJSON("space", s)
			out = append(out, space{name: s.Name, title: s.DisplayName, lastActive: s.LastActiveTime, dm: s.SpaceType == "DIRECT_MESSAGE"})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	slices.SortFunc(out, func(a, b space) int { return strings.Compare(b.lastActive, a.lastActive) })
	return out, nil
}

// memberTitles names DMs and unnamed group chats after their other members,
// since they have no display name. App DMs take the app's name from its
// messages. A space whose other members are all deleted maps to "", and a
// space whose lookup fails is left out.
func (c *client) memberTitles(ctx context.Context, spaces []space) map[string]string {
	var mu sync.Mutex
	titles := map[string]string{}
	var g errgroup.Group
	g.SetLimit(8)
	for _, s := range spaces {
		if s.title != "" {
			continue
		}
		g.Go(func() error {
			names, bot, err := c.otherMembers(ctx, s.name)
			if err != nil {
				log.Printf("space %s: list members: %v", s.name, err)
				return nil
			}
			title := ""
			switch {
			case len(names) > 0:
				title = "@" + strings.Join(names, ", @")
			case bot:
				title = "@" + cmp.Or(c.appName(ctx, s.name), "app")
			}
			mu.Lock()
			titles[s.name] = title
			mu.Unlock()
			return nil
		})
	}
	g.Wait()
	return titles
}

// spaceTitle returns a space's current title, or "" when the space should be
// hidden.
func (c *client) spaceTitle(ctx context.Context, name string) (string, error) {
	s, err := c.svc.Spaces.Get(name).Context(ctx).Do()
	if err != nil {
		return "", err
	}
	if s.DisplayName != "" {
		return s.DisplayName, nil
	}
	t, ok := c.memberTitles(ctx, []space{{name: name}})[name]
	if !ok {
		return "", fmt.Errorf("no title for %s", name)
	}
	return t, nil
}

// otherMembers returns the names of the space's other human members and
// whether a Chat app is among them. Deleted users come back nameless, or not
// at all, so they are skipped.
func (c *client) otherMembers(ctx context.Context, space string) (names []string, bot bool, err error) {
	err = c.svc.Spaces.Members.List(space).Pages(ctx, func(r *chat.ListMembershipsResponse) error {
		for _, m := range r.Memberships {
			debugJSON("membership", m)
			if m.Member == nil || m.Member.Email == c.me {
				continue
			}
			if m.Member.Type == "BOT" {
				bot = true
			} else if n := cmp.Or(m.Member.DisplayName, m.Member.Email); n != "" {
				names = append(names, n)
			}
		}
		return nil
	})
	return names, bot, err
}

// appName finds a Chat app's name from the sender of its recent messages,
// since app memberships carry no display name.
func (c *client) appName(ctx context.Context, space string) string {
	r, err := c.svc.Spaces.Messages.List(space).OrderBy("createTime desc").PageSize(25).Context(ctx).Do()
	if err != nil {
		log.Printf("space %s: app name: %v", space, err)
		return ""
	}
	for _, m := range r.Messages {
		if m.Sender != nil && m.Sender.Type == "BOT" {
			debugJSON("app sender", m.Sender)
			if m.Sender.DisplayName != "" {
				return m.Sender.DisplayName
			}
		}
	}
	return ""
}

type thread struct {
	name    string
	msgs    []*chat.Message // oldest first, msgs[0] is the root
	unseen  int             // replies from others since the thread was last read
	rootNew bool            // root from someone else, posted since the space was last read
	readAt  string          // when the thread was last read, marks new messages in it
}

func (t *thread) last() string { return t.msgs[len(t.msgs)-1].CreateTime }

// threads groups the newest n messages of space by thread, ordered by when
// the root was posted with the newest last, as Chat orders them. A reply
// whose root falls outside the window triggers a fetch of its whole thread,
// so every thread starts at its root.
func (c *client) threads(ctx context.Context, space string, n int64) ([]*thread, error) {
	r, err := c.svc.Spaces.Messages.List(space).OrderBy("createTime desc").PageSize(n).Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	slices.Reverse(r.Messages)
	out := groupThreads(r.Messages)

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for _, t := range out {
		if !t.msgs[0].ThreadReply {
			continue
		}
		g.Go(func() error {
			var msgs []*chat.Message
			err := c.svc.Spaces.Messages.List(space).Filter("thread.name = "+t.name).PageSize(1000).Pages(gctx, func(r *chat.ListMessagesResponse) error {
				msgs = append(msgs, r.Messages...)
				return nil
			})
			if err != nil {
				return err
			}
			t.msgs = msgs
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	slices.SortStableFunc(out, func(a, b *thread) int { return strings.Compare(a.msgs[0].CreateTime, b.msgs[0].CreateTime) })
	return out, nil
}

// groupThreads groups msgs, oldest first, by thread in order of first
// appearance.
func groupThreads(msgs []*chat.Message) []*thread {
	var out []*thread
	byName := map[string]*thread{}
	for _, m := range msgs {
		name := threadName(m)
		t, ok := byName[name]
		if !ok {
			t = &thread{name: name}
			byName[name] = t
			out = append(out, t)
		}
		t.msgs = append(t.msgs, m)
	}
	return out
}

func threadName(m *chat.Message) string {
	if m.Thread != nil && m.Thread.Name != "" {
		return m.Thread.Name
	}
	return m.Name
}

// send posts text to space, as a reply when thread is set, quoting quote
// when set.
func (c *client) send(ctx context.Context, space, thread, text string, quote *chat.Message) (*chat.Message, error) {
	msg := &chat.Message{Text: text}
	if quote != nil {
		msg.QuotedMessageMetadata = &chat.QuotedMessageMetadata{
			Name:           quote.Name,
			LastUpdateTime: cmp.Or(quote.LastUpdateTime, quote.CreateTime),
		}
	}
	call := c.svc.Spaces.Messages.Create(space, msg)
	if thread != "" {
		msg.Thread = &chat.Thread{Name: thread}
		call.MessageReplyOption("REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD")
	}
	return call.Context(ctx).Do()
}

// titleCache persists memberTitles results across runs, so names show at
// startup while the background lookup refreshes them. Titles are relative to
// the logged-in user, so a cache written for someone else is ignored.
type titleCache struct {
	User   string            `json:"user"`
	Titles map[string]string `json:"titles"` // "" marks a hidden space
}

func titleCachePath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mutter", "titles.json"), nil
}

func loadTitleCache(user string) map[string]string {
	empty := map[string]string{}
	path, err := titleCachePath()
	if err != nil {
		return empty
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return empty
	}
	var tc titleCache
	if err := json.Unmarshal(b, &tc); err != nil || tc.User != user || tc.Titles == nil {
		log.Printf("title cache: ignored, err=%v", err)
		return empty
	}
	return tc.Titles
}

// saveTitleCache writes through a temp file so a crash mid-write can't leave
// a truncated cache.
func saveTitleCache(user string, titles map[string]string) error {
	path, err := titleCachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(titleCache{User: user, Titles: titles})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
