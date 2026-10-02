package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/oauth2"
	"golang.org/x/sync/errgroup"
	"google.golang.org/api/chat/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"google.golang.org/api/pubsub/v1"
	"google.golang.org/api/workspaceevents/v1"
)

// Chat delivers batch variants (message.v1.batchCreated and so on) for these
// without subscribing to them separately.
var eventTypes = []string{
	"google.workspace.chat.message.v1.created",
	"google.workspace.chat.message.v1.updated",
	"google.workspace.chat.message.v1.deleted",
	"google.workspace.chat.membership.v1.created",
	"google.workspace.chat.membership.v1.deleted",
	"google.workspace.chat.space.v1.updated",
	"google.workspace.chat.reaction.v1.created",
	"google.workspace.chat.reaction.v1.deleted",
}

// userEventTypes keeps unread state in step with other devices.
var userEventTypes = []string{"google.workspace.chat.spaceReadState.v1.updated"}

const (
	eventTarget = "//chat.googleapis.com/spaces/-"

	// 7 days is the maximum TTL for subscriptions without resource data.
	eventTTL   = "604800s"
	renewEvery = 24 * time.Hour

	// A Pub/Sub subscription unused this long deletes itself, which cleans up
	// after machines that stop running mutter.
	pubsubExpiry = "2678400s" // 31 days
)

// Messages sent into the Bubble Tea program.
type (
	// messageEvent carries a fetched message, or only its name when deleted.
	messageEvent struct {
		kind string // created, updated or deleted
		name string
		msg  *chat.Message
	}
	// spaceChangedMsg reports a rename or membership change that can change
	// the space's title.
	spaceChangedMsg struct{ space string }
	// readStateMsg reports that the user read space, possibly elsewhere.
	readStateMsg struct{ space, lastRead string }
	liveMsg      struct{ err error }
)

var errResubscribe = errors.New("workspace events subscription ended")

type events struct {
	c     *client
	ws    *workspaceevents.Service
	ps    *pubsub.Service
	topic string // projects/P/topics/T
	start time.Time
	out   chan<- tea.Msg
}

// runEvents delivers Chat events into out until ctx ends, resubscribing after
// errors.
func runEvents(ctx context.Context, ts oauth2.TokenSource, c *client, topic string, out chan<- tea.Msg) {
	ws, err := workspaceevents.NewService(ctx, option.WithTokenSource(ts))
	if err != nil {
		out <- liveMsg{err}
		return
	}
	ps, err := pubsub.NewService(ctx, option.WithTokenSource(ts))
	if err != nil {
		out <- liveMsg{err}
		return
	}
	e := &events{c: c, ws: ws, ps: ps, topic: topic, start: time.Now(), out: out}
	for ctx.Err() == nil {
		err := e.run(ctx)
		log.Printf("events: %v", err)
		out <- liveMsg{err}
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
		}
	}
}

func (e *events) run(ctx context.Context) error {
	spaces, err := e.ensureWorkspaceSub(ctx, eventTarget, eventTypes)
	if err != nil {
		return fmt.Errorf("workspace events subscription: %w", err)
	}
	// Read state lives on the user, so it needs its own subscription.
	user, err := e.ensureWorkspaceSub(ctx, "//cloudidentity.googleapis.com/"+e.c.meID, userEventTypes)
	if err != nil {
		return fmt.Errorf("read state subscription: %w", err)
	}
	// One Pub/Sub subscription each, because a filter matching both
	// subscription names exceeds Pub/Sub's 256-character filter limit.
	spacesSub, err := e.ensurePubsubSub(ctx, spaces, "")
	if err != nil {
		return fmt.Errorf("pub/sub subscription: %w", err)
	}
	userSub, err := e.ensurePubsubSub(ctx, user, "-readstate")
	if err != nil {
		return fmt.Errorf("pub/sub read state subscription: %w", err)
	}
	log.Printf("events: live on %s and %s via %s and %s", spaces, user, spacesSub, userSub)
	e.out <- liveMsg{}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return e.pull(gctx, spacesSub) })
	g.Go(func() error { return e.pull(gctx, userSub) })
	g.Go(func() error {
		for {
			select {
			case <-gctx.Done():
				return gctx.Err()
			case <-time.After(renewEvery):
			}
			for _, n := range []string{spaces, user} {
				if err := e.renew(gctx, n); err != nil {
					return err
				}
			}
		}
	})
	return g.Wait()
}

// pull handles events from sub until an error or a lifecycle event that
// needs a new subscription.
func (e *events) pull(ctx context.Context, sub string) error {
	for {
		r, err := e.ps.Projects.Subscriptions.Pull(sub, &pubsub.PullRequest{MaxMessages: 50}).Context(ctx).Do()
		if err != nil {
			return fmt.Errorf("pull: %w", err)
		}
		var acks []string
		for _, rm := range r.ReceivedMessages {
			herr := e.handle(ctx, rm.Message)
			if errors.Is(herr, errResubscribe) {
				return herr
			}
			if herr != nil {
				log.Printf("events: %v", herr)
			}
			acks = append(acks, rm.AckId)
		}
		if len(acks) > 0 {
			if _, err := e.ps.Projects.Subscriptions.Acknowledge(sub, &pubsub.AcknowledgeRequest{AckIds: acks}).Context(ctx).Do(); err != nil {
				return fmt.Errorf("ack: %w", err)
			}
		}
	}
}

// ensureWorkspaceSub reuses this user's subscription to target on our topic,
// or creates one. It returns the subscription name.
func (e *events) ensureWorkspaceSub(ctx context.Context, target string, types []string) (string, error) {
	filter := fmt.Sprintf(`event_types:%q AND target_resource=%q`, types[0], target)
	r, err := e.ws.Subscriptions.List().Filter(filter).Context(ctx).Do()
	if err != nil {
		return "", err
	}
	for _, s := range r.Subscriptions {
		if s.NotificationEndpoint == nil || s.NotificationEndpoint.PubsubTopic != e.topic {
			continue
		}
		if s.State == "ACTIVE" && sameSet(s.EventTypes, types) {
			return s.Name, e.renew(ctx, s.Name)
		}
		log.Printf("events: replacing %s, state=%s types=%v", s.Name, s.State, s.EventTypes)
		if _, err := e.ws.Subscriptions.Delete(s.Name).Context(ctx).Do(); err != nil {
			return "", err
		}
	}

	op, err := e.ws.Subscriptions.Create(&workspaceevents.Subscription{
		TargetResource:       target,
		EventTypes:           types,
		NotificationEndpoint: &workspaceevents.NotificationEndpoint{PubsubTopic: e.topic},
		PayloadOptions:       &workspaceevents.PayloadOptions{IncludeResource: false},
		Ttl:                  eventTTL,
	}).Context(ctx).Do()
	if err != nil {
		return "", err
	}
	for !op.Done {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
		if op, err = e.ws.Operations.Get(op.Name).Context(ctx).Do(); err != nil {
			return "", err
		}
	}
	if op.Error != nil {
		return "", fmt.Errorf("create: %s", op.Error.Message)
	}
	var s workspaceevents.Subscription
	if err := json.Unmarshal(op.Response, &s); err != nil {
		return "", err
	}
	log.Printf("events: created %s", s.Name)
	return s.Name, nil
}

func (e *events) renew(ctx context.Context, name string) error {
	_, err := e.ws.Subscriptions.Patch(name, &workspaceevents.Subscription{Ttl: eventTTL}).UpdateMask("ttl").Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("renew %s: %w", name, err)
	}
	return nil
}

// ensurePubsubSub makes sure this machine has a Pub/Sub subscription, named
// with suffix, that filters the shared topic down to wsName's events. Each
// machine gets its own, because machines sharing one would split the events
// between them.
func (e *events) ensurePubsubSub(ctx context.Context, wsName, suffix string) (string, error) {
	project, _, _ := strings.Cut(strings.TrimPrefix(e.topic, "projects/"), "/")
	host, _ := os.Hostname()
	sum := sha256.Sum256([]byte(e.c.me + "\x00" + host))
	name := fmt.Sprintf("projects/%s/subscriptions/mutter-%s%s", project, hex.EncodeToString(sum[:8]), suffix)
	// ponytail: anyone with pubsub.editor can attach an unfiltered subscription to the shared topic and see other users' event metadata. Message bodies stay protected. Per-user topics close this.
	filter := fmt.Sprintf(`attributes.ce-source = "//workspaceevents.googleapis.com/%s"`, wsName)

	s, err := e.ps.Projects.Subscriptions.Get(name).Context(ctx).Do()
	switch {
	case isNotFound(err):
	case err != nil:
		return "", err
	case s.Topic == e.topic && s.Filter == filter:
		return name, nil
	default:
		// Filters are immutable, so a new workspace subscription needs a new
		// Pub/Sub subscription.
		log.Printf("events: replacing %s, filter=%q", name, s.Filter)
		if _, err := e.ps.Projects.Subscriptions.Delete(name).Context(ctx).Do(); err != nil {
			return "", err
		}
	}
	_, err = e.ps.Projects.Subscriptions.Create(name, &pubsub.Subscription{
		Topic:              e.topic,
		Filter:             filter,
		AckDeadlineSeconds: 60,
		ExpirationPolicy:   &pubsub.ExpirationPolicy{Ttl: pubsubExpiry},
	}).Context(ctx).Do()
	return name, err
}

// eventData covers the payload shapes without resource data, single and
// batched.
type eventData struct {
	Message  *named `json:"message"`
	Messages []struct {
		Message named `json:"message"`
	} `json:"messages"`
	Membership  *named `json:"membership"`
	Memberships []struct {
		Membership named `json:"membership"`
	} `json:"memberships"`
	Space  *named `json:"space"`
	Spaces []struct {
		Space named `json:"space"`
	} `json:"spaces"`
	SpaceReadState *named `json:"spaceReadState"`
	Reaction       *named `json:"reaction"`
	Reactions      []struct {
		Reaction named `json:"reaction"`
	} `json:"reactions"`
}

type named struct {
	Name string `json:"name"`
}

func (e *events) handle(ctx context.Context, m *pubsub.PubsubMessage) error {
	typ := m.Attributes["ce-type"]
	data, err := base64.StdEncoding.DecodeString(m.Data)
	if err != nil {
		return fmt.Errorf("decode %s: %w", typ, err)
	}
	log.Printf("event: %s time=%s data=%s", typ, m.Attributes["ce-time"], data)

	switch typ {
	case "google.workspace.events.subscription.v1.expirationReminder":
		// ce-source is //workspaceevents.googleapis.com/subscriptions/ID.
		return e.renew(ctx, strings.TrimPrefix(m.Attributes["ce-source"], "//workspaceevents.googleapis.com/"))
	case "google.workspace.events.subscription.v1.expired",
		"google.workspace.events.subscription.v1.suspended",
		"google.workspace.events.subscription.v1.deleted":
		return errResubscribe
	}

	// The open space loads its history at startup, so older backlog only
	// costs API calls.
	if t, err := time.Parse(time.RFC3339, m.Attributes["ce-time"]); err == nil && t.Before(e.start) {
		return nil
	}

	var d eventData
	if err := json.Unmarshal(data, &d); err != nil {
		return fmt.Errorf("parse %s: %w", typ, err)
	}
	switch {
	case strings.Contains(typ, ".message.v1."):
		names := []string{}
		if d.Message != nil {
			names = append(names, d.Message.Name)
		}
		for _, x := range d.Messages {
			names = append(names, x.Message.Name)
		}
		kind := messageKind(typ)
		for _, n := range names {
			ev := messageEvent{kind: kind, name: n}
			if kind != "deleted" {
				if ev.msg, err = e.c.svc.Spaces.Messages.Get(n).Context(ctx).Do(); err != nil {
					log.Printf("events: get %s: %v", n, err)
					continue
				}
			}
			e.out <- ev
		}
	case strings.Contains(typ, ".reaction.v1."):
		// A reaction changes its message's summary, so refetch the message.
		var names []string
		if d.Reaction != nil {
			names = append(names, d.Reaction.Name)
		}
		for _, x := range d.Reactions {
			names = append(names, x.Reaction.Name)
		}
		for _, n := range names {
			msgName, _, _ := strings.Cut(n, "/reactions/")
			msg, err := e.c.svc.Spaces.Messages.Get(msgName).Context(ctx).Do()
			if err != nil {
				log.Printf("events: get %s: %v", msgName, err)
				continue
			}
			e.out <- messageEvent{kind: "updated", name: msgName, msg: msg}
		}
	case strings.Contains(typ, ".spaceReadState.v1."):
		if d.SpaceReadState == nil {
			return nil
		}
		space := readStateSpace(d.SpaceReadState.Name)
		lastRead, err := e.c.readState(ctx, space)
		if err != nil {
			return fmt.Errorf("read state %s: %w", space, err)
		}
		e.out <- readStateMsg{space, lastRead}
	case strings.Contains(typ, ".membership.v1."), strings.Contains(typ, ".space.v1."):
		var names []string
		if d.Membership != nil {
			names = append(names, d.Membership.Name)
		}
		for _, x := range d.Memberships {
			names = append(names, x.Membership.Name)
		}
		if d.Space != nil {
			names = append(names, d.Space.Name)
		}
		for _, x := range d.Spaces {
			names = append(names, x.Space.Name)
		}
		for _, n := range names {
			e.out <- spaceChangedMsg{spaceOf(n)}
		}
	}
	return nil
}

// messageKind maps message.v1.created and message.v1.batchCreated to
// "created", and likewise for updated and deleted.
func messageKind(typ string) string {
	k := typ[strings.LastIndex(typ, ".")+1:]
	return strings.ToLower(strings.TrimPrefix(k, "batch"))
}

// spaceOf returns spaces/X for any resource name under it.
func spaceOf(name string) string {
	parts := strings.SplitN(name, "/", 3)
	if len(parts) < 2 {
		return name
	}
	return parts[0] + "/" + parts[1]
}

// readStateSpace returns spaces/S for users/U/spaces/S/spaceReadState.
func readStateSpace(name string) string {
	parts := strings.Split(name, "/")
	if len(parts) < 4 {
		return name
	}
	return parts[2] + "/" + parts[3]
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func isNotFound(err error) bool {
	var gerr *googleapi.Error
	return errors.As(err, &gerr) && gerr.Code == http.StatusNotFound
}

func isForbidden(err error) bool {
	var gerr *googleapi.Error
	return errors.As(err, &gerr) && gerr.Code == http.StatusForbidden
}
