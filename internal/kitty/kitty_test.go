package kitty

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestPlaceholderWidth(t *testing.T) {
	lines := Placeholder(154587, 17, 12)
	if len(lines) != 12 {
		t.Fatalf("rows %d", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != 17 {
			t.Fatalf("line %d width %d, want 17", i, w)
		}
	}
	// id 154587 = 0x025BDB → rgb(2,91,219)
	if !strings.HasPrefix(lines[0], "\x1b[38;2;2;91;219m") {
		t.Fatalf("color encoding: %q", lines[0][:20])
	}
	// joined next to text, the layout must not shift
	box := lipgloss.JoinHorizontal(lipgloss.Top, strings.Join(lines, "\n"), " info")
	for _, l := range strings.Split(box, "\n") {
		if w := lipgloss.Width(l); w != 22 {
			t.Fatalf("joined width %d", w)
		}
	}
}

func TestTransmitChunks(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 230, 325))
	for x := 0; x < 230; x++ {
		img.Set(x, x, color.RGBA{255, 0, 0, 255})
	}
	var jb bytes.Buffer
	jpeg.Encode(&jb, img, nil)
	pngData, err := ToPNG(jb.Bytes())
	if err != nil || !bytes.HasPrefix(pngData, []byte("\x89PNG")) {
		t.Fatal("png conversion", err)
	}
	var out bytes.Buffer
	if err := Transmit(&out, 7, pngData, 17, 12); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	n := strings.Count(s, "\x1b_G")
	if n != strings.Count(s, "\x1b\\") || n < 1 {
		t.Fatalf("unbalanced APC sequences")
	}
	if !strings.HasPrefix(s, "\x1b_Ga=T,U=1,q=2,f=100,i=7,c=17,r=12,m=") {
		t.Fatalf("header %q", s[:40])
	}
	if last := s[strings.LastIndex(s, "\x1b_G"):]; !strings.Contains(last[:20], "m=0;") {
		t.Fatal("last chunk must have m=0")
	}
	for _, part := range strings.Split(s, "\x1b\\") {
		if i := strings.Index(part, ";"); i >= 0 && len(part)-i-1 > 4096 {
			t.Fatal("chunk too big")
		}
	}
}
