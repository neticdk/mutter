package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"google.golang.org/api/chat/v1"
	oauth2api "google.golang.org/api/oauth2/v2"
	"google.golang.org/api/option"
)

// Chat API values compared in several places.
const (
	allUsers       = "users/all"
	mutedSetting   = "MUTED"
	unmutedSetting = "UNMUTED"
	directMessage  = "DIRECT_MESSAGE"
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
	section    string // custom sidebar section in the web client
}

func newClient(ctx context.Context, hc *http.Client) (*client, error) {
	svc, err := chat.NewService(ctx, option.WithHTTPClient(hc))
	if err != nil {
		return nil, err
	}
	ui, err := oauth2api.NewService(ctx, option.WithHTTPClient(hc))
	if err != nil {
		return nil, err
	}
	var id identity
	info, err := ui.Userinfo.Get().Context(ctx).Do()
	switch {
	case err == nil:
		id = identity{Email: info.Email, ID: info.Id}
		if err := writeCache("identity.json", id); err != nil {
			slog.Warn("identity cache write", "err", err)
		}
	case offline(err) && readCache("identity.json", &id) == nil && id.ID != "":
		// Offline at start: the cached identity lets cached spaces show.
		slog.Warn("offline at start, using the cached identity", "err", err)
	default:
		return nil, err
	}
	slog.Info("logged in", "email", id.Email, "user", "users/"+id.ID)
	return &client{svc: svc, me: id.Email, meID: "users/" + id.ID, settings: map[string]*chat.SpaceNotificationSetting{}}, nil
}

// identity is the logged-in user, kept for starting offline.
type identity struct {
	Email string `json:"email"`
	ID    string `json:"id"`
}

// spaces lists the user's spaces, most recently active first.
func (c *client) spaces(ctx context.Context) ([]space, error) {
	var out []space
	err := c.svc.Spaces.List().PageSize(1000).Pages(ctx, func(r *chat.ListSpacesResponse) error {
		for _, s := range r.Spaces {
			traceJSON("space", s)
			out = append(out, space{name: s.Name, title: s.DisplayName, lastActive: s.LastActiveTime, dm: s.SpaceType == directMessage})
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
				slog.Warn("list members", "space", s.name, "err", err)
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
	_ = g.Wait() // the lookups log their own errors and return nil
	return titles
}

// getSpace fetches one space with its title. gone reports that the user can
// no longer see it, because they left or were removed.
func (c *client) getSpace(ctx context.Context, name string) (s space, gone bool, err error) {
	r, err := c.svc.Spaces.Get(name).Context(ctx).Do()
	if isNotFound(err) || isForbidden(err) {
		return space{}, true, nil
	}
	if err != nil {
		return space{}, false, err
	}
	s = space{name: r.Name, title: r.DisplayName, lastActive: r.LastActiveTime, dm: r.SpaceType == directMessage}
	if s.title == "" {
		t, ok := c.memberTitles(ctx, []space{s})[name]
		if !ok {
			return space{}, false, fmt.Errorf("no title for %s", name)
		}
		s.title, s.hidden = t, t == ""
	}
	return s, false, nil
}

// dm finds the DM with user, users/{id} or users/{email}, creating it when
// there's none yet.
func (c *client) dm(ctx context.Context, user string) (string, error) {
	s, err := c.svc.Spaces.FindDirectMessage().Name(user).Context(ctx).Do()
	if err == nil {
		return s.Name, nil
	}
	if !isNotFound(err) {
		return "", err
	}
	s, err = c.svc.Spaces.Setup(&chat.SetUpSpaceRequest{
		Space:       &chat.Space{SpaceType: directMessage},
		Memberships: []*chat.Membership{{Member: &chat.User{Name: user, Type: "HUMAN"}}},
	}).Context(ctx).Do()
	if err != nil {
		return "", err
	}
	return s.Name, nil
}

// sections maps spaces to the custom sidebar section they're filed under in
// the web client.
func (c *client) sections(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	err := c.svc.Users.Sections.List("users/me").PageSize(100).Pages(ctx, func(r *chat.ListSectionsResponse) error {
		for _, sec := range r.Sections {
			if sec.Type != "CUSTOM_SECTION" {
				continue
			}
			err := c.svc.Users.Sections.Items.List(sec.Name).PageSize(1000).Pages(ctx, func(r *chat.ListSectionItemsResponse) error {
				for _, it := range r.SectionItems {
					out[it.Space] = sec.DisplayName
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// otherMembers returns the names of the space's other human members and
// whether a Chat app is among them. Deleted users come back nameless, or not
// at all, so they are skipped.
func (c *client) otherMembers(ctx context.Context, space string) (names []string, bot bool, err error) {
	err = c.svc.Spaces.Members.List(space).Pages(ctx, func(r *chat.ListMembershipsResponse) error {
		for _, m := range r.Memberships {
			traceJSON("membership", m)
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
		slog.Debug("app name", "space", space, "err", err)
		return ""
	}
	for _, m := range r.Messages {
		if m.Sender != nil && m.Sender.Type == "BOT" {
			traceJSON("app sender", m.Sender)
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
func (c *client) threads(ctx context.Context, space string, n int64, pageToken string) ([]*thread, string, error) {
	r, err := c.svc.Spaces.Messages.List(space).OrderBy("createTime desc").PageSize(n).PageToken(pageToken).Context(ctx).Do()
	if err != nil {
		return nil, "", err
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
			msgs, err := c.threadMessages(gctx, space, t.name)
			if err != nil {
				return err
			}
			t.msgs = msgs
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, "", err
	}

	slices.SortStableFunc(out, func(a, b *thread) int { return strings.Compare(a.msgs[0].CreateTime, b.msgs[0].CreateTime) })
	return out, r.NextPageToken, nil
}

// threadMessages fetches every message in thread, oldest first.
func (c *client) threadMessages(ctx context.Context, space, thread string) ([]*chat.Message, error) {
	var msgs []*chat.Message
	err := c.svc.Spaces.Messages.List(space).Filter("thread.name = "+thread).PageSize(1000).Pages(ctx, func(r *chat.ListMessagesResponse) error {
		msgs = append(msgs, r.Messages...)
		return nil
	})
	return msgs, err
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

// outgoing is a message to send. thread, quote and att are optional.
type outgoing struct {
	thread string
	text   string
	quote  *chat.Message
	att    *chat.AttachmentDataRef
}

// send posts out to space, as a reply when out.thread is set.
func (c *client) send(ctx context.Context, space string, out outgoing) (*chat.Message, error) {
	msg := &chat.Message{Text: out.text}
	thread, quote := out.thread, out.quote
	if out.att != nil {
		msg.Attachment = []*chat.Attachment{{AttachmentDataRef: out.att}}
	}
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
	User    string            `json:"user"`
	Titles  map[string]string `json:"titles"`            // "" marks a hidden space
	Checked map[string]int64  `json:"checked,omitempty"` // Unix seconds of each title's last lookup
}

const (
	// titleTTL is how long a cached title is trusted. Membership events
	// update titles while mutter runs, so this only covers changes made
	// while it was closed.
	titleTTL = 14 * 24 * time.Hour
	// maxStaleTitles caps the refreshes of stale titles per start, so a
	// cache that ages all at once refreshes over several starts.
	maxStaleTitles = 32
)

// titlesToRefresh picks the untitled spaces to look up, in the order given:
// every space missing from the cache, and up to maxStaleTitles whose title
// is older than titleTTL.
func titlesToRefresh(spaces []space, titles map[string]string, checked map[string]int64, now time.Time) []space {
	var out []space
	stale := 0
	for _, s := range spaces {
		if s.title != "" {
			continue
		}
		if _, ok := titles[s.name]; !ok {
			out = append(out, s)
			continue
		}
		if now.Sub(time.Unix(checked[s.name], 0)) > titleTTL && stale < maxStaleTitles {
			stale++
			out = append(out, s)
		}
	}
	return out
}

func loadTitleCache(user string) (map[string]string, map[string]int64) {
	var tc titleCache
	if err := readCache("titles.json", &tc); err != nil || tc.User != user || tc.Titles == nil {
		slog.Debug("title cache ignored", "err", err)
		return map[string]string{}, map[string]int64{}
	}
	if tc.Checked == nil {
		tc.Checked = map[string]int64{}
	}
	return tc.Titles, tc.Checked
}

func saveTitleCache(user string, titles map[string]string, checked map[string]int64) error {
	return writeCache("titles.json", titleCache{User: user, Titles: titles, Checked: checked})
}

// cachePath is where mutter keeps the named cache file.
func cachePath(name string) (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mutter", name), nil
}

// readCache decodes the named cache file into v.
func readCache(name string, v any) error {
	path, err := cachePath(name)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(path) // #nosec G304 -- path is in the user's cache dir
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// writeCache writes v to the named cache file through a temp file, so a
// crash mid-write can't leave it truncated.
func writeCache(name string, v any) error {
	path, err := cachePath(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
