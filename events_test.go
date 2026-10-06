package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/api/pubsub/v1"
)

func TestMessageKind(t *testing.T) {
	for typ, want := range map[string]string{
		"google.workspace.chat.message.v1.created":      "created",
		"google.workspace.chat.message.v1.batchCreated": "created",
		"google.workspace.chat.message.v1.batchDeleted": "deleted",
		"google.workspace.chat.message.v1.updated":      "updated",
	} {
		if got := messageKind(typ); got != want {
			t.Errorf("messageKind(%s) = %s, want %s", typ, got, want)
		}
	}
}

func TestEventDataBatch(t *testing.T) {
	var d eventData
	err := json.Unmarshal([]byte(`{"messages":[{"message":{"name":"spaces/A/messages/1"}},{"message":{"name":"spaces/A/messages/2"}}]}`), &d)
	if err != nil || len(d.Messages) != 2 || d.Messages[1].Message.Name != "spaces/A/messages/2" {
		t.Fatalf("batch payload parsed as %+v, err %v", d, err)
	}
	if got := spaceOf("spaces/A/members/123"); got != "spaces/A" {
		t.Errorf("spaceOf = %s", got)
	}
}

func TestHandleUserEvents(t *testing.T) {
	out := make(chan tea.Msg, 4)
	e := &events{c: fakeClient(t, newFakeChat(t)), out: out}
	event := func(typ, data string) *pubsub.PubsubMessage {
		return &pubsub.PubsubMessage{
			Attributes: map[string]string{"ce-type": typ, "ce-time": "2026-10-01T12:00:00Z"},
			Data:       base64.StdEncoding.EncodeToString([]byte(data)),
		}
	}

	err := e.handle(context.Background(), event("google.workspace.chat.threadReadState.v1.updated",
		`{"threadReadState":{"name":"users/me1/spaces/A/threads/T1/threadReadState"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := <-out; got != (threadReadMsg{"spaces/A/threads/T1", "2026-10-01T09:00:00Z"}) {
		t.Errorf("thread read event sent %#v", got)
	}

	err = e.handle(context.Background(), event("google.workspace.chat.availability.v1.updated",
		`{"availability":{"name":"users/me1/availability"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := (<-out).(presenceMsg); !ok || got.a.State != "ACTIVE" {
		t.Errorf("availability event sent %#v", got)
	}
}

func TestUserTopic(t *testing.T) {
	topic, sub := userTopic("acme-chat", "users/112233")
	if topic != "projects/acme-chat/topics/mutter-user-112233" || sub != "projects/acme-chat/subscriptions/mutter-user-112233" {
		t.Errorf("userTopic = %s, %s", topic, sub)
	}
	if !topicName.MatchString(topic) {
		t.Errorf("%s isn't a valid topic name", topic)
	}
}
