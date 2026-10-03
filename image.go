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
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/sys/unix"
	"google.golang.org/api/chat/v1"
)

const (
	maxImageBytes = 20 << 20
	maxImageCols  = 60
	maxGIFFrames  = 150

	// Decoded size limits. A still decodes to 4 bytes per pixel, a GIF frame
	// to 1.
	maxImagePixels = 1 << 24
	maxGIFPixels   = 1 << 26
)

// Images use the kitty graphics protocol with Unicode placeholders. The
// placeholders are ordinary text cells, so Bubble Tea's renderer lays them out
// and redraws them like any other text.
type images struct {
	enabled bool
	store   *store // caches processed images on disk, nil to skip
	layout  layout
	nextID  int
	byRef   map[string]*img
	byID    map[int]*img
}

// layout is the room images get, captured when a download starts.
type layout struct {
	cellW, cellH     int // pixels, from cellSize
	maxCols, maxRows int
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
	ref        string
	pngs       [][]byte // one per frame, scaled to fill cols×rows
	delays     []time.Duration
	cols, rows int
	err        error
}

type animMsg struct{ ref string }

func newImages() *images {
	enabled := os.Getenv("TERM_PROGRAM") == "ghostty" || os.Getenv("TERM") == "xterm-kitty" || os.Getenv("KITTY_WINDOW_ID") != ""
	// #nosec G706 -- %q escapes the values, and they're the user's own environment
	log.Printf("images: enabled=%v TERM=%q TERM_PROGRAM=%q TERM_PROGRAM_VERSION=%q", enabled, os.Getenv("TERM"), os.Getenv("TERM_PROGRAM"), os.Getenv("TERM_PROGRAM_VERSION"))
	return &images{enabled: enabled, layout: layout{8, 16, maxImageCols, 20}, byRef: map[string]*img{}, byID: map[int]*img{}}
}

// resize records the message area's size, in cells, for later downloads.
func (im *images) resize(width, height int) {
	cw, ch := cellSize()
	im.layout = layout{
		cellW:   cw,
		cellH:   ch,
		maxCols: min(maxImageCols, max(1, width-6)),
		maxRows: min(len(diacritics), max(5, height*2/3)),
	}
	log.Printf("images: layout %+v", im.layout)
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
		lay := im.layout
		st := im.store
		cmds = append(cmds, func() tea.Msg { return cachedDownload(st, ref, do, lay) })
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

// download fetches an image, shrinks it when it's larger than its cells, and
// re-encodes it as PNG, the one format every kitty-protocol terminal accepts.
// GIFs become one PNG per frame. The terminal scales the result to fill the
// cells.
// cachedImage is a processed image as stored on disk.
type cachedImage struct {
	PNGs   [][]byte        `json:"pngs"`
	Delays []time.Duration `json:"delays"`
	Cols   int             `json:"cols"`
	Rows   int             `json:"rows"`
}

// cachedDownload serves an image from the disk cache, or downloads and
// caches it. Frames are sized for a layout, so the layout is part of the
// key.
func cachedDownload(st *store, ref string, do func() (*http.Response, error), lay layout) imageMsg {
	key := fmt.Sprintf("%s|%d|%d|%d|%d", ref, lay.cellW, lay.cellH, lay.maxCols, lay.maxRows)
	if st != nil {
		var ci cachedImage
		if err := st.get("img", key, &ci); err == nil && len(ci.PNGs) > 0 {
			return imageMsg{ref: ref, pngs: ci.PNGs, delays: ci.Delays, cols: ci.Cols, rows: ci.Rows}
		}
	}
	msg := download(ref, do, lay)
	if st != nil && msg.err == nil && len(msg.pngs) > 0 {
		ci := cachedImage{PNGs: msg.pngs, Delays: msg.delays, Cols: msg.cols, Rows: msg.rows}
		if err := st.put("img", key, ci); err != nil {
			log.Printf("image cache: %v", err)
		}
	}
	return msg
}

func download(ref string, do func() (*http.Response, error), lay layout) imageMsg {
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
	// Decoding allocates by the declared size, which a small file can set
	// to gigabytes, so it's checked first.
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		log.Printf("image %s: decode: %v", key, err)
		return imageMsg{ref: ref, err: err}
	}
	pixels := cfg.Width * cfg.Height
	if pixels == 0 || pixels > maxImagePixels {
		return imageMsg{ref: ref, err: fmt.Errorf("image is %dx%d pixels", cfg.Width, cfg.Height)}
	}
	if format == "gif" {
		// Every frame decodes to the full canvas at most, so capping the
		// frames bounds the total.
		frames := min(maxGIFFrames, maxGIFPixels/pixels)
		g, err := gif.DecodeAll(bytes.NewReader(gifPrefix(data, frames)))
		if err != nil {
			log.Printf("image %s: decode: %v", key, err)
			return imageMsg{ref: ref, err: err}
		}
		msg := decodeGIF(g, lay)
		msg.ref = ref
		log.Printf("image %s: format=gif frames=%d cells=%dx%d err=%v", key, len(msg.pngs), msg.cols, msg.rows, msg.err)
		return msg
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		log.Printf("image %s: decode: %v", key, err)
		return imageMsg{ref: ref, err: err}
	}
	b := src.Bounds()
	pw, ph, cols, rows := fitSize(b.Dx(), b.Dy(), lay)
	out, err := encodePNG(src, pw, ph, xdraw.CatmullRom)
	log.Printf("image %s: format=%s bounds=%v scaled=%dx%d cells=%dx%d err=%v", key, format, b, pw, ph, cols, rows, err)
	if err != nil {
		return imageMsg{ref: ref, err: err}
	}
	return imageMsg{ref: ref, pngs: [][]byte{out}, cols: cols, rows: rows}
}

// gifPrefix cuts a GIF after n frames by walking its blocks. A GIF it can't
// walk comes back truncated, so decoding fails instead of running unbounded.
func gifPrefix(data []byte, n int) []byte {
	const header = 13 // signature and logical screen descriptor
	if len(data) < header {
		return data
	}
	i := header
	if data[10]&0x80 != 0 {
		i += 3 << (data[10]&7 + 1) // global color table
	}
	subBlocks := func() bool {
		for i < len(data) {
			size := int(data[i])
			i += 1 + size
			if size == 0 {
				return true
			}
		}
		return false
	}
	for frames := 0; i < len(data); {
		switch data[i] {
		case 0x21: // extension: introducer, label, sub-blocks
			i += 2
			if !subBlocks() {
				return data[:0]
			}
		case 0x2c: // image descriptor
			if frames == n {
				return append(data[:i:i], 0x3b)
			}
			frames++
			if i+10 > len(data) {
				return data[:0]
			}
			if flags := data[i+9]; flags&0x80 != 0 {
				i += 3 << (flags&7 + 1) // local color table
			}
			i += 11 // descriptor and LZW minimum code size
			if !subBlocks() {
				return data[:0]
			}
		case 0x3b: // trailer
			return data[:i+1]
		default:
			return data[:0]
		}
	}
	return data // no trailer, but every frame was counted
}

// decodeGIF composites each frame onto a canvas following the frame's
// disposal method, because GIF frames are often partial updates.
func decodeGIF(g *gif.GIF, lay layout) imageMsg {
	w, h := g.Config.Width, g.Config.Height
	if w == 0 || h == 0 {
		b := g.Image[0].Bounds()
		w, h = b.Max.X, b.Max.Y
	}
	canvas := image.NewRGBA(image.Rect(0, 0, w, h))
	pw, ph, cols, rows := fitSize(w, h, lay)
	// ponytail: frames past maxGIFFrames are dropped, so very long GIFs loop early
	msg := imageMsg{cols: cols, rows: rows}
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
		// Bilinear keeps scaling every frame fast.
		b, err := encodePNG(canvas, pw, ph, xdraw.ApproxBiLinear)
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

// encodePNG scales src to a w×h RGBA PNG anchored at the origin. RGBA at
// the origin also normalizes GIF frames, which decode to paletted images that
// may be offset.
func encodePNG(src image.Image, w, h int, scaler xdraw.Scaler) ([]byte, error) {
	rgba := image.NewRGBA(image.Rect(0, 0, w, h))
	if b := src.Bounds(); b.Dx() == w && b.Dy() == h {
		draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)
	} else {
		scaler.Scale(rgba, rgba.Bounds(), src, b, draw.Src, nil)
	}
	var buf bytes.Buffer
	err := png.Encode(&buf, rgba)
	return buf.Bytes(), err
}

// cellSize reads the terminal's cell size in pixels from the window size
// ioctl, falling back to 8x16 when the terminal doesn't report pixels.
func cellSize() (w, h int) {
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Xpixel == 0 || ws.Ypixel == 0 || ws.Col == 0 || ws.Row == 0 {
		return 8, 16
	}
	return int(ws.Xpixel / ws.Col), int(ws.Ypixel / ws.Row)
}

// fitSize fits a w×h pixel image to lay, keeping its aspect ratio. The cells
// allow up to 2x enlargement, which the terminal does when it draws. The
// pixels only ever shrink, so sending an image never costs more than its
// original size.
func fitSize(w, h int, lay layout) (pw, ph, cols, rows int) {
	scale := min(2, float64(lay.maxCols*lay.cellW)/float64(w), float64(lay.maxRows*lay.cellH)/float64(h))
	cols = min(lay.maxCols, max(1, int(math.Ceil(float64(w)*scale/float64(lay.cellW)))))
	rows = min(lay.maxRows, max(1, int(math.Ceil(float64(h)*scale/float64(lay.cellH)))))
	px := min(1, scale)
	pw = max(1, int(math.Round(float64(w)*px)))
	ph = max(1, int(math.Round(float64(h)*px)))
	return pw, ph, cols, rows
}

// add registers a downloaded image. It returns the escape sequence that
// transmits the first frame, and for animations the command that starts them.
func (im *images) add(msg imageMsg) (string, tea.Cmd) {
	i := im.byRef[msg.ref]
	if i == nil || msg.err != nil || len(msg.pngs) == 0 {
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

	i.cols, i.rows = msg.cols, msg.rows
	i.ready = true
	seq := kittyTransmit(i.id, i.cols, i.rows, msg.pngs[0])
	log.Printf("image %s: id=%d cells=%dx%d frames=%d seq=%d bytes", debugKey(msg.ref), i.id, i.cols, i.rows, len(msg.pngs), len(seq))
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

// kittyClear deletes every image ID mutter uses, along with their
// placements. Ghostty keeps images per screen across programs, so without
// this a placeholder can show a previous run's image under a reused ID.
const kittyClear = "\x1b_Ga=d,d=R,x=1,y=255,q=2\x1b\\"

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
// or column n, so the table length caps image height in rows.
var diacritics = []rune{
	0x0305, 0x030D, 0x030E, 0x0310, 0x0312, 0x033D, 0x033E, 0x033F, 0x0346, 0x034A,
	0x034B, 0x034C, 0x0350, 0x0351, 0x0352, 0x0357, 0x035B, 0x0363, 0x0364, 0x0365,
	0x0366, 0x0367, 0x0368, 0x0369, 0x036A, 0x036B, 0x036C, 0x036D, 0x036E, 0x036F,
	0x0483, 0x0484, 0x0485, 0x0486, 0x0487, 0x0592, 0x0593, 0x0594, 0x0595, 0x0597,
	0x0598, 0x0599, 0x059C, 0x059D, 0x059E, 0x059F, 0x05A0, 0x05A1, 0x05A8, 0x05A9,
	0x05AB, 0x05AC, 0x05AF, 0x05C4, 0x0610, 0x0611, 0x0612, 0x0613, 0x0614, 0x0615,
	0x0616, 0x0617, 0x0657, 0x0658,
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
