package main

import (
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"google.golang.org/api/chat/v1"
)

// maxRenderCache bounds each render cache. Edits and reactions arrive as new
// message objects, so stale entries pile up until a clear.
const maxRenderCache = 5000

// renderCache keeps rendered messages and styled blocks between renders, so
// a cursor move doesn't reformat and rewrap every message. Everything
// rendered depends on the pane width, which images are loaded and the day,
// since timestamps drop the date for today. A change to any of them clears
// it.
type renderCache struct {
	width int
	day   string
	msgs  map[msgKey]rendered
	// styled holds blocks with their border style applied, selected or not.
	styled map[styledKey]string
}

// msgKey identifies a message render. num is the attachment number the
// message starts from in its thread.
type msgKey struct {
	msg *chat.Message
	num int
}

type rendered struct {
	text  string
	files int      // attachments and GIFs numbered in it
	refs  []string // images shown in it, to mark visible again on a hit
}

type styledKey struct {
	block string
	style int // index into blockStyles
}

// blockStyles are the border styles a block gets: plain, or selected.
var blockStyles = [...]lipgloss.Style{threadStyle, cursorStyle}

const (
	plainBlock = iota
	selectedBlock
)

func newRenderCache() *renderCache {
	return &renderCache{msgs: map[msgKey]rendered{}, styled: map[styledKey]string{}}
}

// clear drops everything rendered.
func (rc *renderCache) clear() {
	clear(rc.msgs)
	clear(rc.styled)
}

// check clears the cache when the width or the day changed, or it grew too
// big.
func (rc *renderCache) check(width int) {
	day := time.Now().Format(time.DateOnly)
	if width != rc.width || day != rc.day || len(rc.msgs) > maxRenderCache || len(rc.styled) > maxRenderCache {
		rc.clear()
		rc.width, rc.day = width, day
	}
}

// style returns block with blockStyles[style] applied, from the cache when
// it can.
func (m *model) style(block string, style int) string {
	k := styledKey{block, style}
	if s, ok := m.rc.styled[k]; ok {
		return s
	}
	s := blockStyles[style].Render(block)
	m.rc.styled[k] = s
	return s
}

// height counts the lines in s, as lipgloss.Height does.
func height(s string) int { return strings.Count(s, "\n") + 1 }
