package main

import "testing"

func TestAPIPattern(t *testing.T) {
	tests := map[string]string{
		"/v1/spaces/AAQA/messages/abc.def":             "/v1/spaces/*/messages/*",
		"/v1/users/me/spaces/AAQA/spaceReadState":      "/v1/users/*/spaces/*/spaceReadState",
		"/v1/users/me/availability:markAsDoNotDisturb": "/v1/users/*/availability:markAsDoNotDisturb",
		"/v1/projects/p/subscriptions/mutter-1:pull":   "/v1/projects/*/subscriptions/*:pull",
		"/v1/media/ClxzcGFjZXMvQUFRQQ":                 "/v1/media/*",
		"/upload/v1/spaces/AAQA/attachments:upload":    "/upload/v1/spaces/*/attachments:upload",
	}
	for in, want := range tests {
		if got := apiPattern(in); got != want {
			t.Errorf("apiPattern(%q) = %q, want %q", in, got, want)
		}
	}
}
