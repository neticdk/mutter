package main

import (
	"encoding/json"
	"testing"
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
