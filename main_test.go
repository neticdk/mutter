package main

import (
	"strings"
	"testing"
)

func TestUsage(t *testing.T) {
	got := usage()
	if strings.Contains(got, "%!") || strings.Contains(got, "%s") {
		t.Errorf("usage has an unfilled format verb:\n%s", got)
	}
	// Every subcommand the dispatchers accept shows in --help.
	for _, sub := range []string{"config init", "config edit", "config import", "admin setup", "admin provision", "--version"} {
		if !strings.Contains(got, "mutter "+sub) {
			t.Errorf("usage lacks mutter %s", sub)
		}
	}
	for _, env := range []string{"MUTTER_LOG_LEVEL", "GIPHY_API_KEY", "EDITOR"} {
		if !strings.Contains(got, env) {
			t.Errorf("usage lacks %s", env)
		}
	}
}
