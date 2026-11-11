// Package kitty shows images in the terminal with the kitty graphics
// protocol using Unicode placeholders: the image is transmitted once and
// drawn with ordinary (styled) text cells, so it works inside a TUI that
// redraws freely. Supported by kitty ≥ 0.28 and Ghostty.
package kitty

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"os"
	"strings"
)

const placeholder = '\U0010EEEE'

// Supported reports whether the terminal is known to support placeholders.
// ANISYNC_COVERS=0/1 overrides detection.
func Supported() bool {
	switch os.Getenv("ANISYNC_COVERS") {
	case "0", "off", "no":
		return false
	case "1", "on", "yes":
		return true
	}
	if os.Getenv("TMUX") != "" || strings.HasPrefix(os.Getenv("TERM"), "screen") {
		return false // needs passthrough; not worth the breakage
	}
	return os.Getenv("KITTY_WINDOW_ID") != "" || os.Getenv("TERM") == "xterm-kitty" ||
		os.Getenv("TERM_PROGRAM") == "ghostty" || os.Getenv("GHOSTTY_RESOURCES_DIR") != ""
}

// ToPNG converts a JPEG/PNG image to PNG (kitty only accepts PNG or raw pixels).
func ToPNG(data []byte) ([]byte, error) {
	if bytes.HasPrefix(data, []byte("\x89PNG")) {
		return data, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&b, img); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Transmit sends a PNG and creates a virtual placement of cols×rows cells
// for image id. Responses are suppressed (q=2) so nothing lands on stdin.
// Each chunk is one Write, so it never interleaves with other output on a
// serialized writer.
func Transmit(w io.Writer, id uint32, pngData []byte, cols, rows int) error {
	enc := base64.StdEncoding.EncodeToString(pngData)
	const chunk = 4096
	for i := 0; i < len(enc); i += chunk {
		end := min(i+chunk, len(enc))
		more := 0
		if end < len(enc) {
			more = 1
		}
		var hdr string
		if i == 0 {
			hdr = fmt.Sprintf("a=T,U=1,q=2,f=100,i=%d,c=%d,r=%d,m=%d", id, cols, rows, more)
		} else {
			hdr = fmt.Sprintf("q=2,m=%d", more)
		}
		if _, err := io.WriteString(w, "\x1b_G"+hdr+";"+enc[i:end]+"\x1b\\"); err != nil {
			return err
		}
	}
	return nil
}

// Delete frees the image and its placements in the terminal.
func Delete(w io.Writer, id uint32) {
	io.WriteString(w, fmt.Sprintf("\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", id))
}

// Placeholder returns rows lines of cols cells that display image id. The
// image id is carried in the 24-bit foreground color.
func Placeholder(id uint32, cols, rows int) []string {
	rows = min(rows, len(diacritics))
	cols = min(cols, len(diacritics))
	fg := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", (id>>16)&0xff, (id>>8)&0xff, id&0xff)
	lines := make([]string, rows)
	for r := 0; r < rows; r++ {
		var b strings.Builder
		b.WriteString(fg)
		for c := 0; c < cols; c++ {
			b.WriteRune(placeholder)
			b.WriteRune(diacritics[r])
			b.WriteRune(diacritics[c])
		}
		b.WriteString("\x1b[39m")
		lines[r] = b.String()
	}
	return lines
}
