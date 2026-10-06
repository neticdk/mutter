package main

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"strings"

	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	crm "google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/option"
)

const (
	modePerUser = "per-user"
	modeShared  = "shared"
)

var (
	adminTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5"))
	adminBox   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8")).Padding(0, 1)
	adminOK    = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render("✓")
)

// adminSetup walks an admin through setting up a project: it asks for the
// project, mode and group, does what the APIs allow, guides through the
// console steps they don't, and writes the config users import. Every step
// is safe to repeat, so a setup that stopped halfway runs again from the
// start.
func adminSetup(ctx context.Context) error {
	fmt.Println(adminTitle.Render("mutter admin setup"))
	fmt.Println(dimStyle.Render("Uses your gcloud login, or Application Default Credentials without gcloud."))
	fmt.Println()

	ts, err := adminTokens(ctx)
	if err != nil {
		return err
	}
	if _, err := ts.Token(); err != nil {
		return err
	}
	c, err := crm.NewService(ctx, option.WithTokenSource(ts))
	if err != nil {
		return err
	}
	ps, err := projects(ctx, c)
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}
	if len(ps) == 0 {
		return errors.New("no projects to choose from: create one at https://console.cloud.google.com/projectcreate, inside your Workspace organization")
	}

	var project, mode, group string
	opts := make([]huh.Option[string], 0, len(ps))
	for _, p := range ps {
		opts = append(opts, huh.NewOption(p.ProjectId+"  "+dimStyle.Render(p.Name), p.ProjectId))
	}
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Project").
				Description("Holds mutter's OAuth client and Pub/Sub topics. Type to filter.").
				Options(opts...).Filtering(true).Height(12).Value(&project),
		),
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Live updates").
				Options(
					huh.NewOption("Per-user topics: isolated, cost stays flat, needs a scheduled mutter admin provision", modePerUser),
					huh.NewOption("Shared topic: for one team, no provisioning, members can see each other's event metadata", modeShared),
				).Value(&mode),
			huh.NewInput().
				Title("Users' group").
				Description("The Google group whose members use mutter").
				Placeholder("team@example.com").
				Validate(isEmail).Value(&group),
		),
	)
	if err := form.RunWithContext(ctx); err != nil {
		return err
	}

	g, err := gcpWith(ctx, project, option.WithTokenSource(ts))
	if err != nil {
		return err
	}
	if err := step("Enabled the Chat, Workspace Events, Pub/Sub and Cloud Identity APIs", g.enableAPIs(ctx)); err != nil {
		return err
	}
	groupName, err := g.lookupGroup(ctx, group)
	if err := step("Found the group "+group, err); err != nil {
		return err
	}
	if mode == modeShared {
		err := g.setupShared(ctx, group)
		if err := step("Created the topic "+sharedTopic+", and let "+group+" create subscriptions on the project", err); err != nil {
			return err
		}
	} else {
		fmt.Println("Provisioning a topic and subscription per member:")
		if err := g.provision(ctx, groupName, func(line string) { fmt.Println("  " + line) }); err != nil {
			return err
		}
	}

	fmt.Println()
	fmt.Println(consoleSteps(project))
	done := true
	err = huh.NewForm(huh.NewGroup(huh.NewConfirm().
		Title("Done with the three console steps?").
		Affirmative("Yes, continue").Negative("Stop here").Value(&done))).RunWithContext(ctx)
	if err != nil {
		return err
	}
	if !done {
		fmt.Println(dimStyle.Render("Run mutter admin setup again when you're done. The steps so far are safe to repeat."))
		return nil
	}

	var id, secret string
	err = huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Client ID").Description("From step 3").Validate(func(s string) error {
			if !strings.HasSuffix(strings.TrimSpace(s), ".apps.googleusercontent.com") {
				return errors.New("a client ID ends in .apps.googleusercontent.com")
			}
			return nil
		}).Value(&id),
		huh.NewInput().Title("Client secret").EchoMode(huh.EchoModePassword).Validate(func(s string) error {
			if strings.TrimSpace(s) == "" {
				return errors.New("required")
			}
			return nil
		}).Value(&secret),
	)).RunWithContext(ctx)
	if err != nil {
		return err
	}
	cfg := config{ClientID: strings.TrimSpace(id), ClientSecret: strings.TrimSpace(secret)}
	if mode == modePerUser {
		cfg.TopicProject = project
	} else {
		cfg.Topic = "projects/" + project + "/topics/" + sharedTopic
	}

	out, err := filepath.Abs("mutter-" + project + ".json")
	if err != nil {
		return err
	}
	if err := writeAfterConfirm(ctx, out, cfg); err != nil {
		return err
	}
	if self, err := configPath(); err == nil {
		use := true
		err := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Use this config for yourself too?").Value(&use))).RunWithContext(ctx)
		if err != nil {
			return err
		}
		if use {
			if err := writeAfterConfirm(ctx, self, cfg); err != nil {
				return err
			}
		}
	}

	fmt.Println()
	fmt.Println(nextSteps(out, mode, project, group))
	return nil
}

// step prints a finished step, or returns its error.
func step(done string, err error) error {
	if err != nil {
		return err
	}
	fmt.Println(adminOK, done)
	return nil
}

func isEmail(s string) error {
	if _, err := mail.ParseAddress(s); err != nil || !strings.Contains(s, "@") {
		return errors.New("an email address, such as team@example.com")
	}
	return nil
}

// writeAfterConfirm writes cfg to path, asking first when it would replace
// a file.
func writeAfterConfirm(ctx context.Context, path string, cfg config) error {
	if _, err := os.Stat(path); err == nil {
		replace := false
		err := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Replace " + path + "?").Value(&replace))).RunWithContext(ctx)
		if err != nil {
			return err
		}
		if !replace {
			return nil
		}
	}
	if err := writeConfig(path, cfg); err != nil {
		return err
	}
	fmt.Println(adminOK, "Wrote", path)
	return nil
}

// consoleSteps explains the three steps that have no API.
func consoleSteps(project string) string {
	link := func(path string) string {
		return dimStyle.Render("https://console.cloud.google.com/" + path + "?project=" + project)
	}
	return adminBox.Render(strings.Join([]string{
		boldStyle.Render("Three steps have no API. Do them in the console, then come back."),
		"",
		boldStyle.Render("1. Configure the Chat app"),
		"   Name mutter, an avatar URL and a description. Turn off Interactive features.",
		"   " + link("apis/api/chat.googleapis.com/hangouts-chat"),
		"",
		boldStyle.Render("2. Configure OAuth branding"),
		"   Name mutter and a support email. Set the audience to Internal.",
		"   " + link("auth/branding"),
		"",
		boldStyle.Render("3. Create an OAuth client"),
		"   Type Desktop app, named mutter. Keep the client ID and secret for the next step.",
		"   " + link("auth/clients/create"),
	}, "\n"))
}

// nextSteps says what the admin does after setup.
func nextSteps(path, mode, project, group string) string {
	lines := []string{
		boldStyle.Render("Next"),
		"",
		"1. Publish " + filepath.Base(path) + " where your users can fetch it, such as an intranet page.",
		"   Desktop client secrets aren't confidential.",
		"2. Users install mutter and run: mutter config import <file or https URL>",
	}
	if mode == modePerUser {
		lines = append(lines,
			"3. Run this daily, so people who join get a topic and people who leave lose theirs:",
			"   mutter admin provision "+project+" "+group,
			dimStyle.Render("   Scheduling options are in docs/organizations.md."))
	}
	return adminBox.Render(strings.Join(lines, "\n"))
}
