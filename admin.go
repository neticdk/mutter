package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/cloudidentity/v1"
	crm "google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"google.golang.org/api/pubsub/v1"
	"google.golang.org/api/serviceusage/v1"
)

const (
	adminUsage = `usage:
  mutter admin setup                      set up a Google Cloud project for mutter, step by step
  mutter admin provision PROJECT GROUP    give each member of GROUP a topic and subscription, for per-user topics`

	sharedTopic = "mutter-events"
	userPrefix  = "mutter-user-"
	// chatPublisher is the account Google Chat publishes events as.
	chatPublisher = "serviceAccount:chat-api-push@system.gserviceaccount.com"
)

// adminAPIs are the APIs a mutter project needs. Cloud Identity looks up
// the users' group.
var adminAPIs = []string{
	"chat.googleapis.com",
	"workspaceevents.googleapis.com",
	"pubsub.googleapis.com",
	"cloudidentity.googleapis.com",
}

// adminMain runs mutter admin SUBCOMMAND.
func adminMain(args []string) error {
	ctx := context.Background()
	switch {
	case len(args) == 1 && args[0] == "setup":
		return adminSetup(ctx)
	case len(args) == 3 && args[0] == "provision":
		g, err := newGCP(ctx, args[1])
		if err != nil {
			return err
		}
		group, err := g.lookupGroup(ctx, args[2])
		if err != nil {
			return err
		}
		return g.provision(ctx, group, func(line string) { fmt.Println(line) })
	}
	return errors.New(adminUsage)
}

// gcloudToken asks gcloud for the admin's access token, so mutter acts with
// the admin's existing login. gcloud may ask to reauthenticate, so it gets
// the terminal.
type gcloudToken struct{}

func (gcloudToken) Token() (*oauth2.Token, error) {
	cmd := exec.Command("gcloud", "auth", "print-access-token")
	cmd.Stdin, cmd.Stderr = os.Stdin, os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gcloud auth print-access-token: %w, log in with gcloud auth login", err)
	}
	// Tokens last an hour, so a fresh one is fetched well before then.
	return &oauth2.Token{AccessToken: strings.TrimSpace(string(out)), Expiry: time.Now().Add(45 * time.Minute)}, nil
}

// adminTokens uses gcloud when it's installed, and Application Default
// Credentials otherwise, as Cloud Run and GitHub Actions provide.
func adminTokens(ctx context.Context) (oauth2.TokenSource, error) {
	if _, err := exec.LookPath("gcloud"); err == nil {
		return oauth2.ReuseTokenSource(nil, gcloudToken{}), nil
	}
	ts, err := google.DefaultTokenSource(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, fmt.Errorf("no gcloud and no Application Default Credentials: %w", err)
	}
	return ts, nil
}

// gcp holds the API clients for one project.
type gcp struct {
	project string
	crm     *crm.Service
	usage   *serviceusage.Service
	ps      *pubsub.Service
	ident   *cloudidentity.Service
}

func newGCP(ctx context.Context, project string) (*gcp, error) {
	ts, err := adminTokens(ctx)
	if err != nil {
		return nil, err
	}
	return gcpWith(ctx, project, option.WithTokenSource(ts))
}

// gcpWith builds the clients with opts. Cloud Identity bills to the
// project, which user credentials need named.
func gcpWith(ctx context.Context, project string, opts ...option.ClientOption) (*gcp, error) {
	g := &gcp{project: project}
	var err error
	if g.crm, err = crm.NewService(ctx, opts...); err != nil {
		return nil, err
	}
	if g.usage, err = serviceusage.NewService(ctx, opts...); err != nil {
		return nil, err
	}
	if g.ps, err = pubsub.NewService(ctx, opts...); err != nil {
		return nil, err
	}
	if g.ident, err = cloudidentity.NewService(ctx, append(opts, option.WithQuotaProject(project))...); err != nil {
		return nil, err
	}
	return g, nil
}

// projects lists the active projects the admin can see.
func projects(ctx context.Context, c *crm.Service) ([]*crm.Project, error) {
	var out []*crm.Project
	err := c.Projects.List().Filter("lifecycleState:ACTIVE").Pages(ctx, func(r *crm.ListProjectsResponse) error {
		out = append(out, r.Projects...)
		return nil
	})
	slices.SortFunc(out, func(a, b *crm.Project) int { return strings.Compare(a.ProjectId, b.ProjectId) })
	return out, err
}

// enableAPIs turns on adminAPIs and waits for it to finish.
func (g *gcp) enableAPIs(ctx context.Context) error {
	op, err := g.usage.Services.BatchEnable("projects/"+g.project, &serviceusage.BatchEnableServicesRequest{ServiceIds: adminAPIs}).Context(ctx).Do()
	for err == nil && !op.Done {
		time.Sleep(2 * time.Second)
		op, err = g.usage.Operations.Get(op.Name).Context(ctx).Do()
	}
	if err != nil {
		return fmt.Errorf("enable APIs: %w", err)
	}
	if op.Error != nil {
		return fmt.Errorf("enable APIs: %s", op.Error.Message)
	}
	return nil
}

// lookupGroup returns the Cloud Identity name of the group with email.
func (g *gcp) lookupGroup(ctx context.Context, email string) (string, error) {
	r, err := g.ident.Groups.Lookup().GroupKeyId(email).Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("group %s: %w", email, err)
	}
	return r.Name, nil
}

// groupMember is a user in the group: the account ID, which names resources and
// stays the same, and the email, which IAM uses.
type groupMember struct{ id, email string }

// members lists the group's users, including those of groups nested in
// it, so one parent group can hold several teams. Each user counts once,
// however many teams they're in.
func (g *gcp) members(ctx context.Context, group string) ([]groupMember, error) {
	var out []groupMember
	seen := map[string]bool{}
	err := g.ident.Groups.Memberships.SearchTransitiveMemberships(group).Pages(ctx, func(r *cloudidentity.SearchTransitiveMembershipsResponse) error {
		for _, m := range r.Memberships {
			id, user := strings.CutPrefix(m.Member, "users/")
			if !user || seen[id] || len(m.PreferredMemberKey) == 0 {
				continue // a nested group, or a user listed twice
			}
			seen[id] = true
			out = append(out, groupMember{id: id, email: m.PreferredMemberKey[0].Id})
		}
		return nil
	})
	return out, err
}

// setupShared creates the shared topic, lets Chat publish to it, and lets
// the group create subscriptions on it.
func (g *gcp) setupShared(ctx context.Context, groupEmail string) error {
	topic := "projects/" + g.project + "/topics/" + sharedTopic
	if err := g.ensureTopic(ctx, topic); err != nil {
		return err
	}
	if err := g.setTopicRole(ctx, topic, "roles/pubsub.publisher", chatPublisher); err != nil {
		return err
	}
	return g.addProjectRole(ctx, "roles/pubsub.editor", "group:"+groupEmail)
}

// addProjectRole grants role to principal on the project, keeping every
// other binding. Conditional bindings stay as they are.
func (g *gcp) addProjectRole(ctx context.Context, role, principal string) error {
	p, err := g.crm.Projects.GetIamPolicy(g.project, &crm.GetIamPolicyRequest{Options: &crm.GetPolicyOptions{RequestedPolicyVersion: 3}}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("project IAM: %w", err)
	}
	if !addBinding(p, role, principal) {
		return nil
	}
	p.Version = 3
	if _, err := g.crm.Projects.SetIamPolicy(g.project, &crm.SetIamPolicyRequest{Policy: p}).Context(ctx).Do(); err != nil {
		return fmt.Errorf("project IAM: %w", err)
	}
	return nil
}

// addBinding adds principal to role's unconditional binding in p, and
// reports whether p changed.
func addBinding(p *crm.Policy, role, principal string) bool {
	for _, b := range p.Bindings {
		if b.Role == role && b.Condition == nil {
			if slices.Contains(b.Members, principal) {
				return false
			}
			b.Members = append(b.Members, principal)
			return true
		}
	}
	p.Bindings = append(p.Bindings, &crm.Binding{Role: role, Members: []string{principal}})
	return true
}

// provision reconciles per-user topics with the group: every member gets a
// topic and subscription with only their own access, and resources of
// people who left are deleted. It's safe to run again at any time.
func (g *gcp) provision(ctx context.Context, group string, report func(string)) error {
	ms, err := g.members(ctx, group)
	if err != nil {
		return fmt.Errorf("group members: %w", err)
	}
	var existing []string
	err = g.ps.Projects.Topics.List("projects/"+g.project).Pages(ctx, func(r *pubsub.ListTopicsResponse) error {
		for _, t := range r.Topics {
			existing = append(existing, lastSegment(t.Name))
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("list topics: %w", err)
	}
	for _, m := range ms {
		if err := g.provisionMember(ctx, m); err != nil {
			return fmt.Errorf("%s: %w", m.email, err)
		}
		report("✓ " + m.email + " · " + userPrefix + m.id)
	}
	for _, name := range staleTopics(ms, existing) {
		topic, sub := "projects/"+g.project+"/topics/"+name, "projects/"+g.project+"/subscriptions/"+name
		if _, err := g.ps.Projects.Subscriptions.Delete(sub).Context(ctx).Do(); err != nil && !isNotFound(err) {
			return fmt.Errorf("delete %s: %w", sub, err)
		}
		if _, err := g.ps.Projects.Topics.Delete(topic).Context(ctx).Do(); err != nil && !isNotFound(err) {
			return fmt.Errorf("delete %s: %w", topic, err)
		}
		report("✗ removed " + name + ", no longer in the group")
	}
	return nil
}

// staleTopics are the per-user topics in existing whose user isn't among
// members.
func staleTopics(members []groupMember, existing []string) []string {
	var out []string
	for _, name := range existing {
		id, ok := strings.CutPrefix(name, userPrefix)
		if ok && !slices.ContainsFunc(members, func(m groupMember) bool { return m.id == id }) {
			out = append(out, name)
		}
	}
	return out
}

// provisionMember makes m's topic and subscription, and sets their IAM so
// only Chat publishes and only m reads. Setting the roles to exactly these
// principals also drops an address m had before an email change.
func (g *gcp) provisionMember(ctx context.Context, m groupMember) error {
	topic, sub := userTopic(g.project, "users/"+m.id)
	user := "user:" + m.email
	if err := g.ensureTopic(ctx, topic); err != nil {
		return err
	}
	if err := g.setTopicRole(ctx, topic, "roles/pubsub.publisher", chatPublisher); err != nil {
		return err
	}
	// Creating a Workspace Events subscription may need the user to see
	// the topic.
	if err := g.setTopicRole(ctx, topic, "roles/pubsub.viewer", user); err != nil {
		return err
	}
	_, err := g.ps.Projects.Subscriptions.Create(sub, &pubsub.Subscription{
		Topic:              topic,
		AckDeadlineSeconds: 60,
		ExpirationPolicy:   &pubsub.ExpirationPolicy{}, // never expires
	}).Context(ctx).Do()
	if err != nil && !isConflict(err) {
		return fmt.Errorf("create %s: %w", sub, err)
	}
	p, err := g.ps.Projects.Subscriptions.GetIamPolicy(sub).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("IAM on %s: %w", sub, err)
	}
	if setRole(p, "roles/pubsub.subscriber", user) {
		if _, err := g.ps.Projects.Subscriptions.SetIamPolicy(sub, &pubsub.SetIamPolicyRequest{Policy: p}).Context(ctx).Do(); err != nil {
			return fmt.Errorf("IAM on %s: %w", sub, err)
		}
	}
	return nil
}

func (g *gcp) ensureTopic(ctx context.Context, topic string) error {
	_, err := g.ps.Projects.Topics.Create(topic, &pubsub.Topic{}).Context(ctx).Do()
	if err != nil && !isConflict(err) {
		return fmt.Errorf("create %s: %w", topic, err)
	}
	return nil
}

// setTopicRole makes principal the only holder of role on topic.
func (g *gcp) setTopicRole(ctx context.Context, topic, role, principal string) error {
	p, err := g.ps.Projects.Topics.GetIamPolicy(topic).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("IAM on %s: %w", topic, err)
	}
	if !setRole(p, role, principal) {
		return nil
	}
	if _, err := g.ps.Projects.Topics.SetIamPolicy(topic, &pubsub.SetIamPolicyRequest{Policy: p}).Context(ctx).Do(); err != nil {
		return fmt.Errorf("IAM on %s: %w", topic, err)
	}
	return nil
}

// setRole makes principal the only member of role in p, and reports
// whether p changed.
func setRole(p *pubsub.Policy, role, principal string) bool {
	for _, b := range p.Bindings {
		if b.Role == role {
			if len(b.Members) == 1 && b.Members[0] == principal {
				return false
			}
			b.Members = []string{principal}
			return true
		}
	}
	p.Bindings = append(p.Bindings, &pubsub.Binding{Role: role, Members: []string{principal}})
	return true
}

func isConflict(err error) bool {
	var gerr *googleapi.Error
	return errors.As(err, &gerr) && gerr.Code == http.StatusConflict
}
