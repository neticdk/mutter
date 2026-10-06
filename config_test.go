package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigCheck(t *testing.T) {
	ok := config{ClientID: "id", ClientSecret: "secret"}
	for _, tc := range []struct {
		name string
		cfg  config
		want string // "" means valid
	}{
		{"no live updates", ok, ""},
		{"shared topic", config{ClientID: "id", ClientSecret: "s", Topic: "projects/acme-chat/topics/mutter_events"}, ""},
		{"per-user topics", config{ClientID: "id", ClientSecret: "s", TopicProject: "acme-chat"}, ""},
		{"no client", config{Topic: "projects/acme-chat/topics/t1x"}, "client_id and client_secret"},
		{"both modes", config{ClientID: "id", ClientSecret: "s", Topic: "projects/acme-chat/topics/t1x", TopicProject: "acme-chat"}, "not both"},
		{"bad topic", config{ClientID: "id", ClientSecret: "s", Topic: "mutter-events"}, "isn't projects/"},
		{"bad project", config{ClientID: "id", ClientSecret: "s", TopicProject: "Acme Chat"}, "isn't a project ID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.check()
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("check = %v, want valid", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("check = %v, want %q", err, tc.want)
			}
		})
	}
	if _, err := parseConfig([]byte(`{"client_id":"id","client_secret":"s","topik":"x"}`)); err == nil || !strings.Contains(err.Error(), "topik") {
		t.Errorf("a misspelled key should be reported, got %v", err)
	}
}

func TestConfigCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}

	// No config and nothing baked in points at the commands.
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "mutter config import") {
		t.Errorf("loadConfig without a config = %v", err)
	}

	if err := configMain([]string{"init"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	for _, key := range []string{"client_id", "client_secret", "topic", "topic_project"} {
		if !strings.Contains(string(b), `"`+key+`"`) {
			t.Errorf("template lacks %s:\n%s", key, b)
		}
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %v, want 0600", info.Mode().Perm())
	}
	if err := configMain([]string{"init"}); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("init over an existing config = %v, want a --force hint", err)
	}

	// Import from a file needs --force over the existing config.
	src := filepath.Join(t.TempDir(), "acme.json")
	good := `{"client_id":"id","client_secret":"s","topic_project":"acme-chat"}`
	if err := os.WriteFile(src, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := configMain([]string{"import", src}); err == nil {
		t.Error("import over an existing config should need --force")
	}
	if err := configMain([]string{"import", "--force", src}); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig()
	if err != nil || cfg.TopicProject != "acme-chat" {
		t.Errorf("loadConfig after import = %+v, %v", cfg, err)
	}

	// Import from a URL takes https only, and checks what it gets.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"client_id":"id"}`)) // a failed write fails the import
	}))
	defer srv.Close()
	old := http.DefaultClient
	http.DefaultClient = srv.Client()
	defer func() { http.DefaultClient = old }()
	if err := configMain([]string{"import", "--force", srv.URL}); err == nil || !strings.Contains(err.Error(), "client_secret") {
		t.Errorf("importing an incomplete config = %v", err)
	}
	if err := configMain([]string{"import", "--force", "http://example.com/c.json"}); err == nil || !strings.Contains(err.Error(), "https") {
		t.Errorf("importing over http = %v", err)
	}
	if err := configMain([]string{"bogus"}); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("unknown subcommand = %v", err)
	}
}
