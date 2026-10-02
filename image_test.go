package main

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"strings"
	"testing"
	"time"

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
	got := cardHTML(`a<br>b &amp; <font color="#f00">c</font>`)
	if got != "a\nb & c" {
		t.Errorf("cardHTML = %q", got)
	}
}

func TestEncodePNGFlattensPalettedFrame(t *testing.T) {
	// An offset paletted frame, as gif.Decode can return.
	src := image.NewPaletted(image.Rect(5, 5, 9, 7), color.Palette{color.Transparent, color.White})
	src.SetColorIndex(5, 5, 1)

	b, err := encodePNG(src)
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
	})
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
