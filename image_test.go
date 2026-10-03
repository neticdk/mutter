package main

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	xdraw "golang.org/x/image/draw"

	"charm.land/lipgloss/v2"
)

func TestKittyPlaceholderWidth(t *testing.T) {
	p := kittyPlaceholder(7, 10, 3)
	if w, h := lipgloss.Width(p), lipgloss.Height(p); w != 10 || h != 3 {
		t.Fatalf("placeholder is %dx%d cells, want 10x3", w, h)
	}
	// Wrapping to the message width must leave the rows intact.
	if got := lipgloss.NewStyle().Width(20).Render(p); lipgloss.Height(got) != 3 {
		t.Fatalf("wrapping changed placeholder height to %d", lipgloss.Height(got))
	}
	for r, line := range strings.Split(p, "\n") {
		if !strings.ContainsRune(line, diacritics[r]) {
			t.Errorf("row %d lacks its row diacritic", r)
		}
	}
}

func TestCardHTML(t *testing.T) {
	for in, want := range map[string]string{
		`a<br>b &amp; <font color="#f00">c</font>`:    "a\nb & c",
		`<a href="file:///etc/passwd">x</a>`:          "x",
		`<a href="vscode://x">x</a> &#27;]8;;y&#x9b;`: "x ]8;;y›",
		"a\x1b]52;c;aGk=\x07b\u009b2J":                "a]52;c;aGk=b2J",
	} {
		if got := cardHTML(in); got != want {
			t.Errorf("cardHTML(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatTextDropsControls(t *testing.T) {
	if got := formatText("a\x1b]8;;file:///x\x07b\u009dc\n\td"); got != "a]8;;file:///xbc\n\td" {
		t.Errorf("formatText = %q", got)
	}
	if got := stripC1("a\u009b2J\x1b[1mb"); got != "a2J\x1b[1mb" {
		t.Errorf("stripC1 = %q", got)
	}
}

func TestGIFPrefix(t *testing.T) {
	pal := color.Palette{color.Black, color.White}
	g := &gif.GIF{Delay: []int{0, 0, 0}}
	for range 3 {
		g.Image = append(g.Image, image.NewPaletted(image.Rect(0, 0, 2, 2), pal))
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{1, 2, 3, 5} {
		got, err := gif.DecodeAll(bytes.NewReader(gifPrefix(buf.Bytes(), n)))
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if want := min(n, 3); len(got.Image) != want {
			t.Errorf("n=%d: %d frames, want %d", n, len(got.Image), want)
		}
	}
	if got := gifPrefix([]byte("GIF89a\x01\x00\x01\x00\x00\x00\x00junk"), 1); len(got) != 0 {
		t.Errorf("unwalkable GIF kept %d bytes", len(got))
	}
}

func TestDownloadRejectsHugeImage(t *testing.T) {
	// A header declaring 65535x65535 pixels, which would decode to gigabytes.
	data := []byte("GIF89a\xff\xff\xff\xff\x00\x00\x00;")
	msg := download("ref", func() (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(bytes.NewReader(data))}, nil
	}, layout{8, 16, 60, 20})
	if msg.err == nil {
		t.Fatal("huge image accepted")
	}
}

func TestEncodePNGFlattensPalettedFrame(t *testing.T) {
	// An offset paletted frame, as gif.Decode can return.
	src := image.NewPaletted(image.Rect(5, 5, 9, 7), color.Palette{color.Transparent, color.White})
	src.SetColorIndex(5, 5, 1)

	b, err := encodePNG(src, 4, 2, xdraw.NearestNeighbor)
	if err != nil {
		t.Fatal(err)
	}
	out, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.(*image.NRGBA); !ok {
		t.Errorf("decoded %T, want truecolor PNG", out)
	}
	if out.Bounds() != image.Rect(0, 0, 4, 2) {
		t.Errorf("bounds %v, want origin-anchored 4x2", out.Bounds())
	}
	if _, _, _, a := out.At(0, 0).RGBA(); a == 0 {
		t.Error("top-left pixel lost when moving the frame to the origin")
	}
}

func TestDecodeGIFComposites(t *testing.T) {
	pal := color.Palette{color.Transparent, color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255}}
	full := image.NewPaletted(image.Rect(0, 0, 4, 4), pal)
	for i := range full.Pix {
		full.Pix[i] = 1 // red
	}
	patch := image.NewPaletted(image.Rect(0, 0, 2, 2), pal)
	for i := range patch.Pix {
		patch.Pix[i] = 2 // blue
	}
	msg := decodeGIF(&gif.GIF{
		Image:    []*image.Paletted{full, patch},
		Delay:    []int{0, 5},
		Disposal: []byte{gif.DisposalNone, gif.DisposalNone},
		Config:   image.Config{Width: 4, Height: 4},
	}, layout{cellW: 1, cellH: 1, maxCols: 4, maxRows: 4})
	if msg.err != nil || len(msg.pngs) != 2 {
		t.Fatalf("got %d frames, err %v", len(msg.pngs), msg.err)
	}
	if msg.delays[0] != 100*time.Millisecond || msg.delays[1] != 50*time.Millisecond {
		t.Errorf("delays = %v, want [100ms 50ms]", msg.delays)
	}
	second, err := png.Decode(bytes.NewReader(msg.pngs[1]))
	if err != nil {
		t.Fatal(err)
	}
	// The patch covers the top-left only, so the rest keeps frame one's red.
	if r, _, b, _ := second.At(0, 0).RGBA(); b == 0 || r != 0 {
		t.Error("patch not drawn at (0,0)")
	}
	if r, _, _, _ := second.At(3, 3).RGBA(); r == 0 {
		t.Error("frame one lost at (3,3), frames are not composited")
	}
}

func TestFitSize(t *testing.T) {
	lay := layout{cellW: 16, cellH: 32, maxCols: 60, maxRows: 40}
	tests := []struct {
		name       string
		w, h       int
		lay        layout
		cols, rows int
	}{
		// 480px square at 2x in 16x32 cells is 60x30, which fits.
		{"square gif", 480, 480, lay, 60, 30},
		// Limited by height: 20 rows of 32px is 640px, so 640px wide.
		{"tall", 400, 800, layout{16, 32, 60, 20}, 20, 20},
		// A 10:1 screenshot fills the width, 960x96px.
		{"wide", 4000, 400, lay, 60, 3},
		// Tiny images scale up at most 2x.
		{"icon", 16, 16, lay, 2, 1},
	}
	// Pixels never grow, since the terminal enlarges into the cells.
	if pw, ph, _, _ := fitSize(480, 480, lay); pw != 480 || ph != 480 {
		t.Errorf("square gif sent at %dx%d px, want original 480x480", pw, ph)
	}
	if pw, _, _, _ := fitSize(4000, 400, lay); pw != 960 {
		t.Errorf("wide screenshot sent %d px wide, want 960", pw)
	}
	for _, tt := range tests {
		_, _, cols, rows := fitSize(tt.w, tt.h, tt.lay)
		if cols != tt.cols || rows != tt.rows {
			t.Errorf("%s: got %dx%d, want %dx%d", tt.name, cols, rows, tt.cols, tt.rows)
		}
	}
}
