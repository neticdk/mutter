package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/draw"
	"image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"google.golang.org/api/chat/v1"
)

const (
	maxImageBytes = 20 << 20
	maxImageCols  = 60
	maxImageRows  = 20
	maxGIFFrames  = 150
)

// Images use the kitty graphics protocol with Unicode placeholders. The
// placeholders are ordinary text cells, so Bubble Tea's renderer lays them out
// and redraws them like any other text.
type images struct {
	enabled bool
	nextID  int
	byRef   map[string]*img
	byID    map[int]*img
}

type img struct {
	id, cols, rows int
	ready          bool

	// Animated GIFs only. Ghostty lacks the kitty animation commands, so
	// mutter retransmits each frame on its own timer.
	frames  []string // transmit sequences
	delays  []time.Duration
	frame   int
	visible bool // drawn in the current view
}

type imageMsg struct {
	ref    string
	pngs   [][]byte // one per frame
	delays []time.Duration
	w, h   int
	err    error
}

type animMsg struct{ ref string }

func newImages() *images {
	enabled := os.Getenv("TERM_PROGRAM") == "ghostty" || os.Getenv("TERM") == "xterm-kitty" || os.Getenv("KITTY_WINDOW_ID") != ""
	log.Printf("images: enabled=%v TERM=%q TERM_PROGRAM=%q TERM_PROGRAM_VERSION=%q", enabled, os.Getenv("TERM"), os.Getenv("TERM_PROGRAM"), os.Getenv("TERM_PROGRAM_VERSION"))
	return &images{enabled: enabled, byRef: map[string]*img{}, byID: map[int]*img{}}
}

func imageRef(a *chat.Attachment) string {
	if a.AttachmentDataRef == nil || !strings.HasPrefix(a.ContentType, "image/") {
		return ""
	}
	return a.AttachmentDataRef.ResourceName
}

// fetch returns a command per image in msgs not yet requested. Uploaded
// images go through the Chat media API. GIFs from the picker are public URLs,
// fetched without credentials so the OAuth token stays with Google.
func (im *images) fetch(ctx context.Context, c *client, msgs []*chat.Message) tea.Cmd {
	if !im.enabled {
		return nil
	}
	var cmds []tea.Cmd
	get := func(ref string, do func() (*http.Response, error)) {
		if ref == "" || im.byRef[ref] != nil {
			return
		}
		im.byRef[ref] = &img{}
		cmds = append(cmds, func() tea.Msg { return download(ref, do) })
	}
	for _, m := range msgs {
		for _, a := range m.Attachment {
			ref := imageRef(a)
			get(ref, func() (*http.Response, error) { return c.svc.Media.Download(ref).Context(ctx).Download() })
		}
		for _, g := range m.AttachedGifs {
			get(g.Uri, func() (*http.Response, error) {
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.Uri, nil)
				if err != nil {
					return nil, err
				}
				return http.DefaultClient.Do(req)
			})
		}
	}
	return tea.Batch(cmds...)
}

// download fetches an image and re-encodes it as PNG, the one format every
// kitty-protocol terminal accepts. GIFs become one PNG per frame.
func download(ref string, do func() (*http.Response, error)) imageMsg {
	resp, err := do()
	if err != nil {
		return imageMsg{ref: ref, err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("image %s: status=%s", debugKey(ref), resp.Status)
		return imageMsg{ref: ref, err: fmt.Errorf("fetch image: %s", resp.Status)}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes))
	key := debugKey(ref)
	log.Printf("image %s: status=%s content-type=%q bytes=%d err=%v", key, resp.Status, resp.Header.Get("Content-Type"), len(data), err)
	if err != nil {
		return imageMsg{ref: ref, err: err}
	}
	debugWrite(key+".orig", data)
	if g, err := gif.DecodeAll(bytes.NewReader(data)); err == nil {
		msg := decodeGIF(g)
		msg.ref = ref
		log.Printf("image %s: format=gif frames=%d size=%dx%d err=%v", key, len(msg.pngs), msg.w, msg.h, msg.err)
		return msg
	}
	src, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		log.Printf("image %s: decode: %v", key, err)
		return imageMsg{ref: ref, err: err}
	}
	log.Printf("image %s: format=%s type=%T bounds=%v", key, format, src, src.Bounds())
	b, err := encodePNG(src)
	if err != nil {
		log.Printf("image %s: encode: %v", key, err)
		return imageMsg{ref: ref, err: err}
	}
	debugWrite(key+".png", b)
	return imageMsg{ref: ref, pngs: [][]byte{b}, w: src.Bounds().Dx(), h: src.Bounds().Dy()}
}

// decodeGIF composites each frame onto a canvas following the frame's
// disposal method, because GIF frames are often partial updates.
func decodeGIF(g *gif.GIF) imageMsg {
	w, h := g.Config.Width, g.Config.Height
	if w == 0 || h == 0 {
		b := g.Image[0].Bounds()
		w, h = b.Max.X, b.Max.Y
	}
	canvas := image.NewRGBA(image.Rect(0, 0, w, h))
	// ponytail: frames past maxGIFFrames are dropped, so very long GIFs loop early
	msg := imageMsg{w: w, h: h}
	for i, f := range g.Image[:min(len(g.Image), maxGIFFrames)] {
		disposal := byte(gif.DisposalNone)
		if i < len(g.Disposal) {
			disposal = g.Disposal[i]
		}
		var prev *image.RGBA
		if disposal == gif.DisposalPrevious {
			prev = image.NewRGBA(canvas.Bounds())
			copy(prev.Pix, canvas.Pix)
		}
		draw.Draw(canvas, f.Bounds(), f, f.Bounds().Min, draw.Over)
		b, err := encodePNG(canvas)
		if err != nil {
			return imageMsg{err: err}
		}
		msg.pngs = append(msg.pngs, b)
		// Browsers play delays under 20ms at 100ms, and GIFs are authored
		// against that.
		d := 10
		if i < len(g.Delay) && g.Delay[i] >= 2 {
			d = g.Delay[i]
		}
		msg.delays = append(msg.delays, time.Duration(d)*10*time.Millisecond)
		switch disposal {
		case gif.DisposalBackground:
			draw.Draw(canvas, f.Bounds(), image.Transparent, image.Point{}, draw.Src)
		case gif.DisposalPrevious:
			canvas = prev
		}
	}
	return msg
}

// encodePNG writes src as an RGBA PNG anchored at the origin, since GIF
// frames decode to paletted images that may be offset.
func encodePNG(src image.Image) ([]byte, error) {
	// ponytail: full-resolution PNG goes over the pty, downscale first if large photos make rendering slow
	b := src.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)
	var buf bytes.Buffer
	err := png.Encode(&buf, rgba)
	return buf.Bytes(), err
}

// add registers a downloaded image. It returns the escape sequence that
// transmits the first frame, and for animations the command that starts them.
func (im *images) add(msg imageMsg, width int) (string, tea.Cmd) {
	i := im.byRef[msg.ref]
	if i == nil || msg.err != nil || msg.w == 0 || msg.h == 0 || len(msg.pngs) == 0 {
		return "", nil
	}
	// IDs travel as 256-color foreground indexes, which survive color
	// downsampling. ponytail: IDs wrap at 255, so a long session can show a newer image in an old slot
	im.nextID = im.nextID%255 + 1
	i.id = im.nextID
	if old := im.byID[i.id]; old != nil {
		old.ready, old.frames = false, nil // stops its animation
	}
	im.byID[i.id] = i

	// Assume cells are about 8px wide and twice as tall as wide.
	i.cols = min(maxImageCols, max(1, width-6), max(1, msg.w/8))
	i.rows = max(1, i.cols*msg.h/msg.w/2)
	if i.rows > maxImageRows {
		i.rows = maxImageRows
		i.cols = max(1, i.rows*2*msg.w/msg.h)
	}
	i.ready = true
	seq := kittyTransmit(i.id, i.cols, i.rows, msg.pngs[0])
	key := debugKey(msg.ref)
	log.Printf("image %s: id=%d cells=%dx%d frames=%d seq=%d bytes", key, i.id, i.cols, i.rows, len(msg.pngs), len(seq))
	// cat this file in the terminal to replay the image outside the TUI.
	debugWrite(key+".kitty", []byte(seq+kittyPlaceholder(i.id, i.cols, i.rows)+"\x1b[0m\n"))
	if len(msg.pngs) == 1 {
		return seq, nil
	}
	for _, p := range msg.pngs {
		i.frames = append(i.frames, kittyTransmit(i.id, i.cols, i.rows, p))
	}
	i.delays = msg.delays
	return seq, tea.Tick(i.delays[0], func(time.Time) tea.Msg { return animMsg{msg.ref} })
}

// step advances an animation. It returns the next frame to transmit, empty
// while the image is off screen, and the command for the frame after.
func (im *images) step(ref string) (string, tea.Cmd) {
	i := im.byRef[ref]
	if i == nil || len(i.frames) == 0 {
		return "", nil
	}
	i.frame = (i.frame + 1) % len(i.frames)
	next := tea.Tick(i.delays[i.frame], func(time.Time) tea.Msg { return animMsg{ref} })
	if !i.visible {
		return "", next
	}
	return i.frames[i.frame], next
}

// hideAll marks every image off screen. render marks the ones it draws.
func (im *images) hideAll() {
	for _, i := range im.byRef {
		i.visible = false
	}
}

// render returns placeholder text for the ready image ref, or "" when the
// caller should show a text placeholder instead.
func (im *images) render(ref string) string {
	i := im.byRef[ref]
	if !im.enabled || i == nil || !i.ready {
		return ""
	}
	i.visible = true
	return kittyPlaceholder(i.id, i.cols, i.rows)
}

func kittyTransmit(id, cols, rows int, data []byte) string {
	enc := base64.StdEncoding.EncodeToString(data)
	var b strings.Builder
	for first := true; len(enc) > 0 || first; first = false {
		chunk := enc[:min(4096, len(enc))]
		enc = enc[len(chunk):]
		more := 0
		if len(enc) > 0 {
			more = 1
		}
		if first {
			// a=T transmits and places, U=1 makes the placement virtual so
			// placeholder cells show it, and the fixed p=1 makes each
			// animation frame replace the placement instead of adding one.
			// q=2 suppresses replies that would otherwise arrive as keyboard
			// input.
			fmt.Fprintf(&b, "\x1b_Ga=T,U=1,f=100,q=2,i=%d,p=1,c=%d,r=%d,m=%d;%s\x1b\\", id, cols, rows, more, chunk)
		} else {
			fmt.Fprintf(&b, "\x1b_Gm=%d;%s\x1b\\", more, chunk)
		}
	}
	return b.String()
}

// First entries of kitty's rowcolumn-diacritics.txt. Diacritic n encodes row
// or column n.
var diacritics = []rune{
	0x0305, 0x030D, 0x030E, 0x0310, 0x0312, 0x033D, 0x033E, 0x033F, 0x0346, 0x034A,
	0x034B, 0x034C, 0x0350, 0x0351, 0x0352, 0x0357, 0x035B, 0x0363, 0x0364, 0x0365,
}

// kittyPlaceholder draws rows of U+10EEEE cells colored with the image ID.
// Only the first cell of a row carries row and column diacritics. The terminal
// infers the rest as the next column of the same row.
func kittyPlaceholder(id, cols, rows int) string {
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(strconv.Itoa(id)))
	lines := make([]string, rows)
	for r := range rows {
		line := "\U0010EEEE" + string(diacritics[r]) + string(diacritics[0]) + strings.Repeat("\U0010EEEE", cols-1)
		lines[r] = style.Render(line)
	}
	return strings.Join(lines, "\n")
}
