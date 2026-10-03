package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMentionQuery(t *testing.T) {
	tests := []struct {
		in string
		q  string
		ok bool
	}{
		{"hi @ki", "ki", true},
		{"@", "", true},
		{"@Kim Nø", "Kim Nø", true}, // names have spaces
		{"mail kn@netic.dk", "", false},
		{"@kim\nnext line", "", false},
		{"no mention", "", false},
	}
	for _, tt := range tests {
		q, _, ok := mentionQuery(tt.in)
		if q != tt.q || ok != tt.ok {
			t.Errorf("mentionQuery(%q) = %q, %v, want %q, %v", tt.in, q, ok, tt.q, tt.ok)
		}
	}
}

func TestSuggestAndExpand(t *testing.T) {
	members := []member{{id: "users/1", name: "Kim Nørgaard"}, {id: "users/2", name: "Mads Nygaard"}, {id: "users/3", name: "Nils Lundberg"}}
	got := suggest(members, "nø")
	if len(got) != 1 || got[0].id != "users/1" {
		t.Errorf("suggest by second word = %v", got)
	}
	if got := suggest(members, "al"); len(got) != 1 || got[0].id != "users/all" {
		t.Errorf("suggest @all = %v", got)
	}

	text := expandMentions("@Kim Nørgaard and @Kim, see @all",
		map[string]string{"@Kim": "<users/9>", "@Kim Nørgaard": "<users/1>", "@all": "<users/all>"})
	if want := "<users/1> and <users/9>, see <users/all>"; text != want {
		t.Errorf("expandMentions = %q, want %q", text, want)
	}
}

func TestParseAttach(t *testing.T) {
	home, _ := os.UserHomeDir()
	tests := []struct{ in, path, text string }{
		{"/attach ~/a.pdf", filepath.Join(home, "a.pdf"), ""},
		{"/attach /tmp/a.pdf see this", "/tmp/a.pdf", "see this"},
		{`/attach "/tmp/my file.pdf" here`, "/tmp/my file.pdf", "here"},
	}
	for _, tt := range tests {
		path, text, err := parseAttach(tt.in)
		if err != nil || path != tt.path || text != tt.text {
			t.Errorf("parseAttach(%q) = %q, %q, %v", tt.in, path, text, err)
		}
	}
	if _, _, err := parseAttach("/attach"); err == nil {
		t.Error("missing path should fail")
	}
	if _, _, err := parseAttach(`/attach "/tmp/x`); err == nil {
		t.Error("unclosed quote should fail")
	}
}
