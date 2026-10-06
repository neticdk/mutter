package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"golang.org/x/oauth2"
	crm "google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/option"
	"google.golang.org/api/pubsub/v1"
)

func TestStaleTopics(t *testing.T) {
	members := []groupMember{{"111", "a@x.dk"}, {"222", "b@x.dk"}}
	got := staleTopics(members, []string{"mutter-user-111", "mutter-user-999", "mutter-events", "other"})
	if !slices.Equal(got, []string{"mutter-user-999"}) {
		t.Errorf("staleTopics = %v, want only the leaver's topic", got)
	}
}

func TestAddBinding(t *testing.T) {
	p := &crm.Policy{Bindings: []*crm.Binding{
		{Role: "roles/owner", Members: []string{"user:kn@x.dk"}},
		{Role: "roles/pubsub.editor", Members: []string{"user:x@x.dk"}, Condition: &crm.Expr{Expression: "true"}},
	}}
	if !addBinding(p, "roles/pubsub.editor", "group:team@x.dk") {
		t.Fatal("a new principal should change the policy")
	}
	if len(p.Bindings) != 3 || p.Bindings[1].Members[0] != "user:x@x.dk" {
		t.Errorf("the conditional binding should stay apart: %+v", p.Bindings)
	}
	if addBinding(p, "roles/pubsub.editor", "group:team@x.dk") {
		t.Error("adding the same principal again should change nothing")
	}
}

func TestSetRole(t *testing.T) {
	p := &pubsub.Policy{Bindings: []*pubsub.Binding{{Role: "roles/pubsub.subscriber", Members: []string{"user:old@x.dk", "user:new@x.dk"}}}}
	if !setRole(p, "roles/pubsub.subscriber", "user:new@x.dk") || !slices.Equal(p.Bindings[0].Members, []string{"user:new@x.dk"}) {
		t.Errorf("setRole should leave only the new address: %+v", p.Bindings[0])
	}
	if setRole(p, "roles/pubsub.subscriber", "user:new@x.dk") {
		t.Error("setting the same principal again should change nothing")
	}
}

// fakeGCP serves the Pub/Sub and Cloud Identity calls provisioning makes,
// with IAM policies kept per resource.
type fakeGCP struct {
	mu       sync.Mutex
	members  []groupMember
	topics   map[string]bool // full names
	subs     map[string]bool
	policies map[string]*pubsub.Policy
}

func (f *fakeGCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v1/")
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) } // a failed write fails the client's call
	resource, verb, _ := strings.Cut(path, ":")
	switch {
	case strings.HasSuffix(path, "/memberships:searchTransitiveMemberships"):
		// A nested team group and a user in two teams, as the API lists them.
		out := []map[string]any{{"member": "groups/team2", "preferredMemberKey": []map[string]string{{"id": "team2@x.dk"}}}}
		for _, m := range append(f.members, f.members[0]) {
			out = append(out, map[string]any{"member": "users/" + m.id, "preferredMemberKey": []map[string]string{{"id": m.email}}})
		}
		reply(map[string]any{"memberships": out})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/topics"):
		var out []*pubsub.Topic
		for name := range f.topics {
			out = append(out, &pubsub.Topic{Name: name})
		}
		reply(pubsub.ListTopicsResponse{Topics: out})
	case verb == "getIamPolicy":
		reply(cmpOr(f.policies[resource], &pubsub.Policy{}))
	case verb == "setIamPolicy":
		var in pubsub.SetIamPolicyRequest
		_ = json.NewDecoder(r.Body).Decode(&in) // the test checks what landed
		f.policies[resource] = in.Policy
		reply(in.Policy)
	case r.Method == http.MethodPut:
		set := f.topics
		if strings.Contains(path, "/subscriptions/") {
			set = f.subs
		}
		if set[path] {
			http.Error(w, `{"error":{"code":409,"message":"exists"}}`, http.StatusConflict)
			return
		}
		set[path] = true
		reply(map[string]string{"name": path})
	case r.Method == http.MethodDelete:
		delete(f.topics, path)
		delete(f.subs, path)
		reply(struct{}{})
	default:
		http.NotFound(w, r)
	}
}

func cmpOr(p, fallback *pubsub.Policy) *pubsub.Policy {
	if p != nil {
		return p
	}
	return fallback
}

func (f *fakeGCP) holders(policy, role string) []string {
	if p := f.policies[policy]; p != nil {
		for _, b := range p.Bindings {
			if b.Role == role {
				return b.Members
			}
		}
	}
	return nil
}

func TestProvision(t *testing.T) {
	f := &fakeGCP{
		members:  []groupMember{{"111", "a@x.dk"}, {"222", "b@x.dk"}},
		topics:   map[string]bool{"projects/p1/topics/mutter-user-111": true, "projects/p1/topics/mutter-user-999": true, "projects/p1/topics/mutter-events": true},
		subs:     map[string]bool{"projects/p1/subscriptions/mutter-user-111": true, "projects/p1/subscriptions/mutter-user-999": true},
		policies: map[string]*pubsub.Policy{"projects/p1/subscriptions/mutter-user-111": {Bindings: []*pubsub.Binding{{Role: "roles/pubsub.subscriber", Members: []string{"user:a-old@x.dk"}}}}},
	}
	srv := httptest.NewServer(f)
	defer srv.Close()
	g, err := gcpWith(context.Background(), "p1", option.WithEndpoint(srv.URL+"/"), option.WithTokenSource(oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "t"})))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	if err := g.provision(context.Background(), "groups/g1", func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatal(err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range []string{"111", "222"} {
		if !f.topics["projects/p1/topics/mutter-user-"+id] || !f.subs["projects/p1/subscriptions/mutter-user-"+id] {
			t.Errorf("user %s lacks a topic or subscription", id)
		}
		if got := f.holders("projects/p1/topics/mutter-user-"+id, "roles/pubsub.publisher"); !slices.Equal(got, []string{chatPublisher}) {
			t.Errorf("publishers on %s's topic = %v", id, got)
		}
	}
	if got := f.holders("projects/p1/subscriptions/mutter-user-111", "roles/pubsub.subscriber"); !slices.Equal(got, []string{"user:a@x.dk"}) {
		t.Errorf("subscribers after an email change = %v, want only the new address", got)
	}
	if got := f.holders("projects/p1/topics/mutter-user-222", "roles/pubsub.viewer"); !slices.Equal(got, []string{"user:b@x.dk"}) {
		t.Errorf("viewers on a new topic = %v", got)
	}
	if f.topics["projects/p1/topics/mutter-user-999"] || f.subs["projects/p1/subscriptions/mutter-user-999"] {
		t.Error("the leaver's topic and subscription should be gone")
	}
	if !f.topics["projects/p1/topics/mutter-events"] {
		t.Error("topics that aren't per-user stay")
	}
	if len(lines) != 3 {
		t.Errorf("report = %q", lines)
	}
}
