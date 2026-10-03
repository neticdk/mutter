package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// fakeGIF is a 1×1 GIF.
var fakeGIF = []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;")

// fakeGiphy serves GIPHY search with two usable results and one with an
// http URL, which the client skips, and the GIF files behind them. It
// answers searches without the key with 401.
func fakeGiphy(t *testing.T, key string) {
	t.Helper()
	srv := httptest.NewTLSServer(nil)
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			if r.URL.Query().Get("api_key") != key {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			u := srv.URL
			fmt.Fprintf(w, `{"data":[
				{"id":"g1","title":"Party Parrot","images":{"fixed_height_small":{"url":"%[1]s/g1s.gif"},"downsized":{"url":"%[1]s/g1.gif"}}},
				{"id":"g2","title":"Plain HTTP","images":{"fixed_height_small":{"url":"http://example.com/s.gif"},"downsized":{"url":"http://example.com/l.gif"}}},
				{"id":"g3","title":"Dancing Cat","images":{"fixed_height":{"url":"%[1]s/g3s.gif"},"original":{"url":"%[1]s/g3.gif"}}}
			]}`, u)
		default:
			_, _ = w.Write(fakeGIF) // a failed write fails the client's read
		}
	})
	t.Cleanup(srv.Close)
	old, oldClient := giphySearch, http.DefaultClient
	giphySearch, http.DefaultClient = srv.URL+"/search", srv.Client()
	t.Cleanup(func() { giphySearch, http.DefaultClient = old, oldClient })
}

func TestSearchGIFs(t *testing.T) {
	fakeGiphy(t, "k3y")
	got, err := searchGIFs(context.Background(), "k3y", "party")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].title != "Party Parrot" || got[1].title != "Dancing Cat" {
		t.Fatalf("results = %+v, want Party Parrot and Dancing Cat", got)
	}
	if !strings.HasSuffix(got[1].thumb, "/g3s.gif") || !strings.HasSuffix(got[1].large, "/g3.gif") {
		t.Errorf("fallback renditions = %+v", got[1])
	}
	if _, err := searchGIFs(context.Background(), "wrong", "party"); err == nil {
		t.Error("a rejected key should fail")
	}
}

func TestSearchGIFsHidesKey(t *testing.T) {
	old := giphySearch
	giphySearch = "https://127.0.0.1:1/search" // nothing listens
	t.Cleanup(func() { giphySearch = old })
	_, err := searchGIFs(context.Background(), "s3cret", "party")
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("err = %v, want a failure without the key", err)
	}
}

func TestFlowGIF(t *testing.T) {
	fakeGiphy(t, "k3y")
	t.Setenv("GIPHY_API_KEY", "k3y")
	f := newFakeChat(t)
	f.addSpace("spaces/A", "Platform")
	f.post("spaces/A", "", "users/alice", "Deploy is blocked")

	fl := startFlow(t, f)
	fl.see("Deploy is blocked")

	// Search, move to the second result, attach it, and send it.
	fl.typeText("/gif party")
	fl.key(tea.KeyEnter)
	fl.see("Powered by GIPHY")
	fl.see("Party Parrot")
	fl.key(tea.KeyRight)
	fl.see("Dancing Cat")
	fl.key(tea.KeyEnter)
	fl.see("attached giphy-g3.gif")
	fl.key(tea.KeyEnter)
	fl.waitFor(func() bool {
		fl.f.mu.Lock()
		defer fl.f.mu.Unlock()
		return string(fl.f.uploads["giphy-g3.gif"]) == string(fakeGIF)
	}, "the GIF upload")
}
