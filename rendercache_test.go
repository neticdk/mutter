package main

import (
	"strings"
	"testing"

	"google.golang.org/api/chat/v1"
)

func TestRenderCacheFollowsChanges(t *testing.T) {
	m := benchModel(t, 3)
	if !strings.Contains(m.vp.GetContent(), "Message 1 about") {
		t.Fatal("first render")
	}

	// An edit arrives as a new message object, so it renders fresh.
	edited := *m.threads[1].msgs[0]
	edited.Text = "edited text"
	m.replace(&edited)
	if c := m.vp.GetContent(); !strings.Contains(c, "edited text") || strings.Contains(c, "Message 1 about") {
		t.Error("edit not shown")
	}

	// A narrower pane rewraps.
	before := m.vp.GetContent()
	m.vp.SetWidth(60)
	m.render()
	if m.vp.GetContent() == before {
		t.Error("width change kept the old wrapping")
	}
}

func TestRenderCacheKeepsImagesVisible(t *testing.T) {
	m := benchModel(t, 1)
	m.imgs.enabled = true
	m.imgs.byRef["ref"] = &img{id: 1, cols: 2, rows: 1, ready: true}
	m.threads[0].msgs[0].Attachment = []*chat.Attachment{{ContentType: "image/png", AttachmentDataRef: &chat.AttachmentDataRef{ResourceName: "ref"}}}
	m.rc.clear()
	m.render() // renders and caches
	m.render() // served from the cache
	if !m.imgs.byRef["ref"].visible {
		t.Error("an image in a cached message stopped counting as visible, which stops GIFs")
	}
}
