package main

import (
	"log/slog"
	"maps"
	"strings"
)

// draft is unsent input, kept per space and thread across switches and
// restarts. Completed @mentions travel with it, so they still notify.
type draft struct {
	Text     string            `json:"text"`
	Mentions map[string]string `json:"mentions,omitempty"`
}

// draftFile is drafts.json. Drafts belong to the logged-in user, so a file
// written for someone else is ignored.
type draftFile struct {
	User   string           `json:"user"`
	Drafts map[string]draft `json:"drafts"`
}

func loadDrafts(user string) map[string]draft {
	var f draftFile
	if err := readCache("drafts.json", &f); err != nil || f.User != user || f.Drafts == nil {
		return map[string]draft{}
	}
	return f.Drafts
}

// draftKey names the input's context: the open space, and the open thread
// or "" for the space view.
func (m *model) draftKey() string {
	if m.cur < 0 {
		return ""
	}
	thread := ""
	if m.inThread != nil {
		thread = m.inThread.name
	}
	return m.spaces[m.cur].name + "|" + thread
}

// saveDraft keeps the input for the current context, or drops the context's
// draft when the input is empty. Text being edited belongs to a sent
// message, so it isn't a draft.
func (m *model) saveDraft() {
	key := m.draftKey()
	if key == "" || m.editing != nil {
		return
	}
	text := m.ta.Value()
	if strings.TrimSpace(text) == "" {
		if _, ok := m.drafts[key]; !ok {
			return
		}
		delete(m.drafts, key)
	} else {
		d := draft{Text: text}
		for name, tok := range m.mentions {
			if strings.Contains(text, name) {
				if d.Mentions == nil {
					d.Mentions = map[string]string{}
				}
				d.Mentions[name] = tok
			}
		}
		m.drafts[key] = d
	}
	if err := writeCache("drafts.json", draftFile{User: m.c.me, Drafts: m.drafts}); err != nil {
		slog.Warn("drafts write", "err", err)
	}
}

// loadDraft puts the current context's draft in the input, or empties it.
// Pending files belong to the context left, so they go.
func (m *model) loadDraft() {
	m.ta.Reset()
	m.pending = nil
	clear(m.mentions)
	if d, ok := m.drafts[m.draftKey()]; ok {
		m.ta.SetValue(d.Text)
		maps.Copy(m.mentions, d.Mentions)
	}
}

// hasDraft reports whether space has a draft in any of its contexts.
func (m *model) hasDraft(space string) bool {
	for k := range m.drafts {
		if strings.HasPrefix(k, space+"|") {
			return true
		}
	}
	return false
}
