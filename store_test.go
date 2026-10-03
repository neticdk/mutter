package main

import (
	"bytes"
	"os"
	"testing"
	"time"
)

func testStore(t *testing.T) *store {
	t.Helper()
	s, err := newStore(t.TempDir(), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStoreRoundTrip(t *testing.T) {
	s := testStore(t)
	if err := s.put("msgs", "spaces/A", map[string]string{"hello": "world"}); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := s.get("msgs", "spaces/A", &got); err != nil || got["hello"] != "world" {
		t.Fatalf("get = %v, %v", got, err)
	}

	// The plaintext isn't on disk.
	b, _ := os.ReadFile(s.path("msgs", "spaces/A"))
	if len(b) == 0 || bytes.Contains(b, []byte("world")) {
		t.Error("cache file holds plaintext")
	}

	// A file moved to another key fails authentication.
	if err := os.Rename(s.path("msgs", "spaces/A"), s.path("msgs", "spaces/B")); err != nil {
		t.Fatal(err)
	}
	if err := s.get("msgs", "spaces/B", &got); err == nil {
		t.Error("swapped file decrypted")
	}

	// A different key can't read it.
	other, _ := newStore(s.dir, append(make([]byte, 31), 1))
	if err := s.put("msgs", "spaces/C", "x"); err != nil {
		t.Fatal(err)
	}
	var x string
	if err := other.get("msgs", "spaces/C", &x); err == nil {
		t.Error("wrong key decrypted")
	}
}

func TestStorePrune(t *testing.T) {
	s := testStore(t)
	for i, k := range []string{"old", "mid", "new"} {
		if err := s.put("img", k, make([]byte, 1000)); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(time.Duration(i-3) * time.Hour)
		if err := os.Chtimes(s.path("img", k), at, at); err != nil {
			t.Fatal(err)
		}
	}
	info, _ := os.Stat(s.path("img", "new"))
	if err := s.prune("img", 2*info.Size()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.path("img", "old")); !os.IsNotExist(err) {
		t.Error("oldest file kept")
	}
	for _, k := range []string{"mid", "new"} {
		if _, err := os.Stat(s.path("img", k)); err != nil {
			t.Errorf("%s removed: %v", k, err)
		}
	}
	if err := s.prune("missing", 0); err != nil {
		t.Errorf("prune of a missing kind: %v", err)
	}
}
