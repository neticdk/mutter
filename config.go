package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Set with -ldflags "-X main.clientID=... -X main.clientSecret=... -X main.topic=...
// -X main.topicProject=...". Public releases leave them empty, and an
// organization that builds its own binary may bake its values in. Desktop
// OAuth client secrets are not confidential.
var clientID, clientSecret, topic, topicProject string

// config is config.json. Topic and TopicProject pick the live update mode:
// one topic shared by everyone, or a topic per user in a project.
type config struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Topic        string `json:"topic,omitempty"`         // projects/P/topics/T, shared by all users
	TopicProject string `json:"topic_project,omitempty"` // P, holding a topic per user
}

// maxConfigBytes bounds an imported config, which is a few hundred bytes.
const maxConfigBytes = 64 << 10

var (
	topicName   = regexp.MustCompile(`^projects/[a-z][a-z0-9-]{4,28}[a-z0-9]/topics/[A-Za-z][A-Za-z0-9._~+%_-]{2,254}$`)
	projectName = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
)

// check reports what's wrong with c, or nil.
func (c config) check() error {
	var errs []error
	if c.ClientID == "" || c.ClientSecret == "" {
		errs = append(errs, errors.New("client_id and client_secret are required"))
	}
	if c.Topic != "" && c.TopicProject != "" {
		errs = append(errs, errors.New("set topic for a shared topic or topic_project for per-user topics, not both"))
	}
	if c.Topic != "" && !topicName.MatchString(c.Topic) {
		errs = append(errs, fmt.Errorf("topic %q isn't projects/PROJECT/topics/TOPIC", c.Topic))
	}
	if c.TopicProject != "" && !projectName.MatchString(c.TopicProject) {
		errs = append(errs, fmt.Errorf("topic_project %q isn't a project ID", c.TopicProject))
	}
	return errors.Join(errs...)
}

func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mutter", "config.json"), nil
}

// loadConfig reads config.json, or uses the baked-in values when there is
// none. A file that sets only the topic keeps the baked-in client.
func loadConfig() (config, error) {
	path, err := configPath()
	if err != nil {
		return config{}, err
	}
	b, err := os.ReadFile(path) // #nosec G304 -- path is in the user's config dir
	baked := config{ClientID: clientID, ClientSecret: clientSecret, Topic: topic, TopicProject: topicProject}
	switch {
	case errors.Is(err, fs.ErrNotExist) && clientID != "":
		return baked, nil
	case errors.Is(err, fs.ErrNotExist):
		return config{}, fmt.Errorf("no config at %s: run mutter config import or mutter config init, see the README", path)
	case err != nil:
		return config{}, err
	}
	var cfg config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return config{}, fmt.Errorf("%s: %w", path, err)
	}
	if cfg.ClientID == "" && cfg.ClientSecret == "" {
		cfg.ClientID, cfg.ClientSecret = baked.ClientID, baked.ClientSecret
	}
	if cfg.Topic == "" && cfg.TopicProject == "" {
		cfg.Topic, cfg.TopicProject = baked.Topic, baked.TopicProject
	}
	if err := cfg.check(); err != nil {
		return config{}, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// parseConfig reads a config strictly, so a misspelled key is reported
// instead of ignored.
func parseConfig(b []byte) (config, error) {
	var cfg config
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return config{}, err
	}
	return cfg, cfg.check()
}

const configUsage = `usage:
  mutter config init [--force]               write an empty config to fill in
  mutter config edit                         open the config in $VISUAL or $EDITOR
  mutter config import [--force] FILE|URL    install a config from a file or an https URL`

// configMain runs mutter config SUBCOMMAND.
func configMain(args []string) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	force := false
	var rest []string
	for _, a := range args {
		if a == "--force" {
			force = true
		} else {
			rest = append(rest, a)
		}
	}
	switch {
	case len(rest) == 1 && rest[0] == "init":
		if !force {
			if err := noOverwrite(path); err != nil {
				return err
			}
		}
		if err := writeConfig(path, config{}); err != nil {
			return err
		}
		fmt.Println("wrote", path, "- fill it in with mutter config edit")
		return nil
	case len(rest) == 1 && rest[0] == "edit":
		return editConfig(path)
	case len(rest) == 2 && rest[0] == "import":
		b, err := readSource(rest[1])
		if err != nil {
			return err
		}
		cfg, err := parseConfig(b)
		if err != nil {
			return fmt.Errorf("%s: %w", rest[1], err)
		}
		if !force {
			if err := noOverwrite(path); err != nil {
				return err
			}
		}
		if err := writeConfig(path, cfg); err != nil {
			return err
		}
		fmt.Println("wrote", path)
		return nil
	}
	return errors.New(configUsage)
}

// noOverwrite fails when path exists, so a config is only replaced with
// --force.
func noOverwrite(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s exists, add --force to replace it", path)
	}
	return nil
}

// writeConfig writes cfg to path, private to the user.
func writeConfig(path string, cfg config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Every key shows in the template, empty ones included.
	b, err := json.MarshalIndent(struct { // #nosec G117 -- the config holds the client secret by design, written 0600
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		Topic        string `json:"topic"`
		TopicProject string `json:"topic_project"`
	}(cfg), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600) // #nosec G703 -- path is in the user's config dir
}

// readSource reads a config from a file or an https URL.
func readSource(src string) ([]byte, error) {
	if !strings.Contains(src, "://") {
		f, err := os.Open(src) // #nosec G304 G703 -- the user named this file
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return io.ReadAll(io.LimitReader(f, maxConfigBytes))
	}
	if !strings.HasPrefix(src, "https://") {
		return nil, fmt.Errorf("%s: only https URLs", src)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil) // #nosec G704 -- the user named this URL
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req) // #nosec G107 G704 -- the user named this URL
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", src, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxConfigBytes))
}

// editConfig opens path in the editor, starting from the template when
// there is no config yet, and checks the result.
func editConfig(path string) error {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		if err := writeConfig(path, config{}); err != nil {
			return err
		}
	}
	cmd := editorCmd(path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor: %w", err)
	}
	b, err := os.ReadFile(path) // #nosec G304 -- path is in the user's config dir
	if err != nil {
		return err
	}
	if _, err := parseConfig(b); err != nil {
		return fmt.Errorf("%s: %w\nrun mutter config edit again to fix it", path, err)
	}
	fmt.Println(path, "is valid")
	return nil
}

// editorCmd runs $VISUAL or $EDITOR on path, vi by default.
func editorCmd(path string) *exec.Cmd {
	editor := strings.Fields(cmp.Or(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi"))
	return exec.Command(editor[0], append(editor[1:], path)...) // #nosec G204 G702 -- the editor is the user's own setting
}
