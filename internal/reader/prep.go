// Package reader turns a photo of a school paper into a Reading: it
// prepares the image, asks the local model through Ollama, and checks
// everything the model says before anyone sees it.
package reader

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/jpeg"
	_ "image/png" // PNG uploads
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/image/draw"
)

const (
	maxSide     = 1600       // long side of the image sent to the model
	maxPixels   = 60_000_000 // larger than any phone photo
	jpegQuality = 85
)

// A Problem is an error whose message a parent can read and act on.
type Problem string

func (p Problem) Error() string { return string(p) }

const (
	ErrNotPhoto   Problem = "This file is not a photo. Drop a JPEG, PNG or HEIC picture."
	ErrHEICNotMac Problem = "HEIC photos open on a Mac only. Save the photo as JPEG and drop it again."
	ErrTooBig     Problem = "This photo is too large. Take a smaller picture and drop it again."
)

// goos and heicToJPEG are variables so tests can play another platform.
var (
	goos       = runtime.GOOS
	heicToJPEG = sipsToJPEG
)

// Prepare turns an uploaded photo into the JPEG the model reads: upright,
// on white, at most 1600 px on the long side.
func Prepare(data []byte) ([]byte, error) {
	switch sniff(data) {
	case "jpeg", "png":
	case "heic":
		if goos != "darwin" {
			return nil, ErrHEICNotMac
		}
		jpg, err := heicToJPEG(data)
		if err != nil {
			return nil, ErrNotPhoto
		}
		data = jpg
	default:
		return nil, ErrNotPhoto
	}

	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, ErrNotPhoto
	}
	if cfg.Width*cfg.Height > maxPixels {
		return nil, ErrTooBig
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrNotPhoto
	}

	out := orient(fit(img, maxSide), exifOrientation(data))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func sniff(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return "jpeg"
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case len(b) >= 12 && string(b[4:8]) == "ftyp":
		switch string(b[8:12]) {
		case "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1":
			return "heic"
		}
	}
	return ""
}

// fit scales img down so its long side is at most side and paints it on
// white, so transparent PNGs do not turn black in the JPEG.
func fit(img image.Image, side int) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > side || h > side {
		if w >= h {
			w, h = side, max(1, h*side/w)
		} else {
			w, h = max(1, w*side/h), side
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	if w == b.Dx() && h == b.Dy() {
		draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Over)
	} else {
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	}
	return dst
}

// orient applies an EXIF orientation (1-8) so the paper reads upright.
func orient(src *image.RGBA, o int) *image.RGBA {
	if o < 2 || o > 8 {
		return src
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2: // mirrored
				dx, dy = w-1-x, y
			case 3: // upside down
				dx, dy = w-1-x, h-1-y
			case 4: // mirrored upside down
				dx, dy = x, h-1-y
			case 5: // transposed
				dx, dy = y, x
			case 6: // turn 90 degrees clockwise
				dx, dy = h-1-y, x
			case 7: // transversed
				dx, dy = h-1-y, w-1-x
			case 8: // turn 90 degrees counter-clockwise
				dx, dy = y, w-1-x
			}
			s, d := src.PixOffset(x, y), dst.PixOffset(dx, dy)
			copy(dst.Pix[d:d+4], src.Pix[s:s+4])
		}
	}
	return dst
}

// exifOrientation reads the orientation tag from a JPEG's EXIF block.
// It returns 1 (upright) when there is none.
func exifOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 1
	}
	for i := 2; i+4 <= len(b); {
		if b[i] != 0xFF {
			return 1
		}
		marker := b[i+1]
		if marker == 0xDA || marker == 0xD9 { // image data starts, no EXIF before it
			return 1
		}
		size := int(binary.BigEndian.Uint16(b[i+2:]))
		if size < 2 || i+2+size > len(b) {
			return 1
		}
		seg := b[i+4 : i+2+size]
		if marker == 0xE1 && len(seg) > 6 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + size
	}
	return 1
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	off := int(bo.Uint32(t[4:]))
	if off < 8 || off+2 > len(t) {
		return 1
	}
	n := int(bo.Uint16(t[off:]))
	for k := 0; k < n; k++ {
		e := off + 2 + 12*k
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:]) == 0x0112 {
			if v := int(bo.Uint16(t[e+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// sipsToJPEG converts a HEIC photo with the sips tool that ships with macOS.
func sipsToJPEG(data []byte) ([]byte, error) {
	dir, err := os.MkdirTemp("", "backpack-heic-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	in, out := filepath.Join(dir, "in.heic"), filepath.Join(dir, "out.jpg")
	if err := os.WriteFile(in, data, 0o600); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "sips", "-s", "format", "jpeg", in, "--out", out).Run(); err != nil {
		return nil, err
	}
	return os.ReadFile(out)
}
