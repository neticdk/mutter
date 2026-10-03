package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/oauth2"
	"google.golang.org/api/googleapi"
)

func TestDescribeErr(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"revoked login", &url.Error{Op: "Get", URL: "https://x", Err: &oauth2.RetrieveError{ErrorCode: "invalid_grant"}}, "login expired or was revoked"},
		{"rate limit", &googleapi.Error{Code: 429}, "rate limiting"},
		{"unauthorized", &googleapi.Error{Code: 401}, "rejected the login"},
		{"server error", fmt.Errorf("send: %w", &googleapi.Error{Code: 503}), "Google had a problem (503)"},
		{"refused", &googleapi.Error{Code: 403, Message: "The caller does not have permission"}, "Google refused it (403): The caller does not have permission"},
		{"network", &url.Error{Op: "Get", URL: "https://chat.googleapis.com/v1/spaces", Err: errors.New("read tcp: connection reset")}, "can't reach Google"},
		{"timeout", context.DeadlineExceeded, "can't reach Google"},
		{"other", errors.New("unclosed \" in path"), "unclosed \" in path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := describeErr(tc.err)
			if !strings.Contains(got, tc.want) {
				t.Errorf("describeErr = %q, want it to contain %q", got, tc.want)
			}
			if strings.Contains(got, "googleapis.com") {
				t.Errorf("describeErr = %q leaks the request URL", got)
			}
		})
	}
}
