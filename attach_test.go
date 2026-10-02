package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"report.pdf":        "report.pdf",
		"../../.ssh/config": "config",
		`..\..\evil.exe`:    "evil.exe",
		"..":                "attachment",
		"":                  "attachment",
		"dir/":              "dir",
	} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCreateUnique(t *testing.T) {
	dir := t.TempDir()
	for _, want := range []string{"a.txt", "a (2).txt", "a (3).txt"} {
		f, path, err := createUnique(dir, "a.txt")
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
		if got := filepath.Base(path); got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); err != nil {
		t.Error(err)
	}
}
