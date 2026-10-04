package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/api/chat/v1"
	"google.golang.org/api/option"
)

// fakeChat is an in-memory Chat API for flow tests. It serves what mutter
// calls when it starts, opens spaces and threads, sends, edits and reacts,
// and answers 404 to the rest.
type fakeChat struct {
	t      testing.TB
	mu     sync.Mutex
	spaces []*chat.Space
	msgs   map[string][]*chat.Message // by space, oldest first
	reacts map[string][]*chat.Reaction
	clock  time.Time
	nextID int
	// uploads holds uploaded files by name.
	uploads map[string][]byte
	// failSends makes message creation fail with 503.
	failSends bool
	// down makes every request fail with 503.
	down bool
	// invited holds members added through the API, by space.
	invited map[string][]string
}

const fakeMe = "users/me1"

// fakeParrot is the organization's one custom emoji.
var fakeParrot = &chat.CustomEmoji{Name: "customEmojis/ce1", Uid: "ce1", EmojiName: ":partyparrot:"}

// emojiKey tells reactions apart, custom ones by UID.
func emojiKey(e *chat.Emoji) string {
	if e.CustomEmoji != nil {
		return "custom:" + e.CustomEmoji.Uid
	}
	return e.Unicode
}

func newFakeChat(t testing.TB) *fakeChat {
	return &fakeChat{t: t, uploads: map[string][]byte{}, invited: map[string][]string{}, msgs: map[string][]*chat.Message{}, reacts: map[string][]*chat.Reaction{}, clock: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
}

// tick returns a timestamp later than every earlier one.
func (f *fakeChat) tick() string {
	f.clock = f.clock.Add(time.Minute)
	return f.clock.Format(time.RFC3339)
}

func (f *fakeChat) addSpace(name, title string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spaces = append(f.spaces, &chat.Space{Name: name, DisplayName: title, SpaceType: "SPACE", LastActiveTime: f.tick()})
}

// post adds a message from sender, as a reply when thread is set, and
// returns it.
func (f *fakeChat) post(space, thread, sender, text string) *chat.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.postLocked(space, thread, sender, text)
}

func (f *fakeChat) postLocked(space, thread, sender, text string) *chat.Message {
	f.nextID++
	msg := &chat.Message{
		Name:       fmt.Sprintf("%s/messages/m%d", space, f.nextID),
		Text:       text,
		CreateTime: f.tick(),
		Sender:     &chat.User{Name: sender, DisplayName: strings.TrimPrefix(sender, "users/"), Type: "HUMAN"},
	}
	if thread == "" {
		msg.Thread = &chat.Thread{Name: fmt.Sprintf("%s/threads/t%d", space, f.nextID)}
	} else {
		msg.Thread, msg.ThreadReply = &chat.Thread{Name: thread}, true
	}
	f.msgs[space] = append(f.msgs[space], msg)
	for _, s := range f.spaces {
		if s.Name == space {
			s.LastActiveTime = msg.CreateTime
		}
	}
	return msg
}

// messages returns copies of space's messages, oldest first, which stay
// put while the fake edits its own.
func (f *fakeChat) messages(space string) []*chat.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*chat.Message, 0, len(f.msgs[space]))
	for _, m := range f.msgs[space] {
		c := *m
		out = append(out, &c)
	}
	return out
}

func (f *fakeChat) find(name string) *chat.Message {
	for _, msg := range f.msgs[spaceOf(name)] {
		if msg.Name == name {
			return msg
		}
	}
	return nil
}

// withReactions returns msg with its reaction summary filled in, as the API
// does.
func (f *fakeChat) withReactions(msg *chat.Message) *chat.Message {
	out := *msg
	out.EmojiReactionSummaries = nil
	for _, r := range f.reacts[msg.Name] {
		i := slices.IndexFunc(out.EmojiReactionSummaries, func(s *chat.EmojiReactionSummary) bool { return emojiKey(s.Emoji) == emojiKey(r.Emoji) })
		if i < 0 {
			out.EmojiReactionSummaries = append(out.EmojiReactionSummaries, &chat.EmojiReactionSummary{Emoji: r.Emoji})
			i = len(out.EmojiReactionSummaries) - 1
		}
		out.EmojiReactionSummaries[i].ReactionCount++
	}
	return &out
}

var (
	threadFilter = regexp.MustCompile(`thread\.name = (\S+)`)
	emojiFilter  = regexp.MustCompile(`emoji\.unicode = "([^"]+)"`)
	customFilter = regexp.MustCompile(`emoji\.custom_emoji\.uid = "([^"]+)"`)
)

func (f *fakeChat) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v1/")
	seg := strings.Split(path, "/")
	reply := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(v); err != nil {
			f.t.Errorf("fake chat: %v", err)
		}
	}
	decode := func(v any) {
		if err := json.NewDecoder(r.Body).Decode(v); err != nil {
			f.t.Errorf("fake chat: decode %s: %v", r.URL.Path, err)
		}
	}

	switch {
	case f.down:
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/upload/v1/") && strings.HasSuffix(path, "/attachments:upload"):
		f.upload(w, r)
	case r.Method == http.MethodPost && path == "spaces/-/messages:search":
		// The real API parses a query language. Substring matching on the
		// text covers keyword searches.
		var in chat.SearchMessagesRequest
		decode(&in)
		var out []*chat.SearchMessageResult
		for _, sp := range f.spaces {
			for _, msg := range f.msgs[sp.Name] {
				if strings.Contains(msg.Text, in.Filter) {
					out = append(out, &chat.SearchMessageResult{Message: msg})
				}
			}
		}
		reply(chat.SearchMessagesResponse{Results: out})
	case r.Method == http.MethodPost && path == "spaces":
		var in chat.Space
		decode(&in)
		f.nextID++
		in.Name, in.LastActiveTime = fmt.Sprintf("spaces/S%d", f.nextID), f.tick()
		f.spaces = append(f.spaces, &in)
		reply(in)
	case r.Method == http.MethodPatch && len(seg) == 2 && seg[0] == "spaces":
		var in chat.Space
		decode(&in)
		for _, sp := range f.spaces {
			if sp.Name == path {
				sp.DisplayName = in.DisplayName
				reply(sp)
				return
			}
		}
		http.NotFound(w, r)
	case r.Method == http.MethodPost && len(seg) == 3 && seg[2] == "members":
		var in chat.Membership
		decode(&in)
		space := seg[0] + "/" + seg[1]
		f.invited[space] = append(f.invited[space], in.Member.Name)
		reply(in)
	case r.Method == http.MethodDelete && len(seg) == 4 && seg[2] == "members":
		space := seg[0] + "/" + seg[1]
		if "users/"+seg[3] != fakeMe {
			http.Error(w, "only yourself", http.StatusForbidden)
			return
		}
		f.spaces = slices.DeleteFunc(f.spaces, func(sp *chat.Space) bool { return sp.Name == space })
		reply(struct{}{})
	case r.Method == http.MethodGet && path == "customEmojis":
		reply(chat.ListCustomEmojisResponse{CustomEmojis: []*chat.CustomEmoji{fakeParrot}})
	case r.Method == http.MethodGet && path == "spaces":
		reply(chat.ListSpacesResponse{Spaces: f.spaces})
	case r.Method == http.MethodGet && len(seg) == 3 && seg[0] == "spaces" && seg[2] == "members":
		reply(chat.ListMembershipsResponse{Memberships: []*chat.Membership{
			{Member: &chat.User{Name: "users/kim", DisplayName: "Kim Larsen", Email: "kla@example.com", Type: "HUMAN"}},
		}})
	case r.Method == http.MethodGet && len(seg) == 3 && seg[2] == "messages":
		msgs := slices.Clone(f.msgs[seg[0]+"/"+seg[1]])
		if m := threadFilter.FindStringSubmatch(r.URL.Query().Get("filter")); m != nil {
			msgs = slices.DeleteFunc(msgs, func(x *chat.Message) bool { return x.Thread.Name != m[1] })
		}
		if strings.Contains(r.URL.Query().Get("orderBy"), "desc") {
			slices.Reverse(msgs)
		}
		out := make([]*chat.Message, 0, len(msgs))
		for _, msg := range msgs {
			out = append(out, f.withReactions(msg))
		}
		reply(chat.ListMessagesResponse{Messages: out})
	case r.Method == http.MethodPost && len(seg) == 3 && seg[2] == "messages" && f.failSends:
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	case r.Method == http.MethodPost && len(seg) == 3 && seg[2] == "messages":
		var in chat.Message
		decode(&in)
		thread := ""
		if in.Thread != nil {
			thread = in.Thread.Name
		}
		msg := f.postLocked(seg[0]+"/"+seg[1], thread, fakeMe, in.Text)
		msg.QuotedMessageMetadata = in.QuotedMessageMetadata
		reply(msg)
	case len(seg) == 4 && seg[2] == "messages":
		msg := f.find(path)
		if msg == nil {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			reply(f.withReactions(msg))
		case http.MethodPatch:
			var in chat.Message
			decode(&in)
			msg.Text, msg.LastUpdateTime = in.Text, f.tick()
			reply(msg)
		case http.MethodDelete:
			space := spaceOf(path)
			f.msgs[space] = slices.DeleteFunc(f.msgs[space], func(x *chat.Message) bool { return x.Name == path })
			reply(struct{}{})
		}
	case len(seg) == 5 && seg[4] == "reactions":
		msgName := strings.Join(seg[:4], "/")
		switch r.Method {
		case http.MethodGet:
			var out []*chat.Reaction
			want := ""
			if m := emojiFilter.FindStringSubmatch(r.URL.Query().Get("filter")); m != nil {
				want = m[1]
			} else if m := customFilter.FindStringSubmatch(r.URL.Query().Get("filter")); m != nil {
				want = "custom:" + m[1]
			}
			for _, x := range f.reacts[msgName] {
				if want != "" && emojiKey(x.Emoji) == want && x.User.Name == fakeMe {
					out = append(out, x)
				}
			}
			reply(chat.ListReactionsResponse{Reactions: out})
		case http.MethodPost:
			var in chat.Reaction
			decode(&in)
			f.nextID++
			if c := in.Emoji.CustomEmoji; c != nil {
				// The API rejects a custom emoji without its resource name.
				if c.Uid != "" || c.Name != fakeParrot.Name {
					http.Error(w, "invalid custom emoji", http.StatusBadRequest)
					return
				}
				in.Emoji.CustomEmoji = fakeParrot
			}
			in.Name = fmt.Sprintf("%s/reactions/r%d", msgName, f.nextID)
			in.User = &chat.User{Name: fakeMe}
			f.reacts[msgName] = append(f.reacts[msgName], &in)
			reply(in)
		}
	case len(seg) == 6 && seg[4] == "reactions" && r.Method == http.MethodDelete:
		msgName := strings.Join(seg[:4], "/")
		f.reacts[msgName] = slices.DeleteFunc(f.reacts[msgName], func(x *chat.Reaction) bool { return x.Name == path })
		reply(struct{}{})
	case strings.HasSuffix(path, "/spaceReadState"):
		// Read up to the start, so everything posted in the test is new.
		reply(chat.SpaceReadState{Name: path, LastReadTime: "2026-10-01T09:00:00Z"})
	case strings.HasSuffix(path, "/spaceNotificationSetting"):
		reply(chat.SpaceNotificationSetting{Name: path, NotificationSetting: "ALL", MuteSetting: "UNMUTED"})
	case path == "users/me/availability":
		reply(chat.Availability{Name: path, State: "ACTIVE"})
	case path == "users/me/sections":
		reply(chat.ListSectionsResponse{})
	default:
		http.NotFound(w, r)
	}
}

// upload takes a multipart media upload: JSON metadata with the file
// name, then the file.
func (f *fakeChat) upload(w http.ResponseWriter, r *http.Request) {
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		f.t.Errorf("fake chat: upload: %v", err)
		return
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	var meta chat.UploadAttachmentRequest
	if err := json.NewDecoder(must(mr.NextPart())).Decode(&meta); err != nil {
		f.t.Errorf("fake chat: upload metadata: %v", err)
		return
	}
	data, err := io.ReadAll(must(mr.NextPart()))
	if err != nil {
		f.t.Errorf("fake chat: upload data: %v", err)
		return
	}
	f.uploads[meta.Filename] = data
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(chat.UploadAttachmentResponse{AttachmentDataRef: &chat.AttachmentDataRef{ResourceName: "uploads/" + meta.Filename}}) // a failed write fails the client's call
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// fakeClient serves a client from a fake Chat API.
func fakeClient(t testing.TB, f *fakeChat) *client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	svc, err := chat.NewService(context.Background(), option.WithEndpoint(srv.URL+"/"), option.WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	return &client{svc: svc, me: "me@example.com", meID: fakeMe, settings: map[string]*chat.SpaceNotificationSetting{}}
}
