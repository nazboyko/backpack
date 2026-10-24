package reader

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// halves is a w x h image, red on the left half and blue on the right.
func halves(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{220, 20, 20, 255}
			if x >= w/2 {
				c = color.RGBA{20, 20, 220, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func encodeJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// withOrientation inserts an EXIF block carrying orientation o right after
// the JPEG start marker, the way a phone camera writes it.
func withOrientation(jpg []byte, o uint16) []byte {
	tiff := make([]byte, 8+2+12+4)
	copy(tiff, "MM")
	binary.BigEndian.PutUint16(tiff[2:], 42)
	binary.BigEndian.PutUint32(tiff[4:], 8)
	binary.BigEndian.PutUint16(tiff[8:], 1)       // one entry
	binary.BigEndian.PutUint16(tiff[10:], 0x0112) // orientation
	binary.BigEndian.PutUint16(tiff[12:], 3)      // SHORT
	binary.BigEndian.PutUint32(tiff[14:], 1)      // count
	binary.BigEndian.PutUint16(tiff[18:], o)
	seg := append([]byte("Exif\x00\x00"), tiff...)
	app1 := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(app1[2:], uint16(len(seg)+2))
	out := append([]byte{}, jpg[:2]...)
	out = append(out, app1...)
	out = append(out, seg...)
	return append(out, jpg[2:]...)
}

func decode(t *testing.T, b []byte) image.Image {
	t.Helper()
	if sniff(b) != "jpeg" {
		t.Fatalf("output is not a JPEG")
	}
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func isRed(c color.Color) bool {
	r, _, b, _ := c.RGBA()
	return r > 0x9000 && b < 0x6000
}

func isBlue(c color.Color) bool {
	r, _, b, _ := c.RGBA()
	return b > 0x9000 && r < 0x6000
}

func TestPrepareTurnsOrientationSixUpright(t *testing.T) {
	src := withOrientation(encodeJPEG(t, halves(80, 40)), 6)
	if got := exifOrientation(src); got != 6 {
		t.Fatalf("exifOrientation = %d, want 6", got)
	}
	out, err := Prepare(src)
	if err != nil {
		t.Fatal(err)
	}
	img := decode(t, out)
	if b := img.Bounds(); b.Dx() != 40 || b.Dy() != 80 {
		t.Fatalf("size %dx%d, want 40x80", b.Dx(), b.Dy())
	}
	// Turned clockwise: the red left half is now on top.
	if !isRed(img.At(20, 10)) || !isBlue(img.At(20, 70)) {
		t.Fatalf("not turned clockwise: top %v bottom %v", img.At(20, 10), img.At(20, 70))
	}
}

func TestOrientAllEight(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	src.SetRGBA(0, 0, color.RGBA{255, 0, 0, 255}) // marker in the top-left corner
	want := map[int]image.Point{
		1: {0, 0}, 2: {2, 0}, 3: {2, 1}, 4: {0, 1},
		5: {0, 0}, 6: {1, 0}, 7: {1, 2}, 8: {0, 2},
	}
	for o, p := range want {
		got := orient(src, o)
		if c := got.RGBAAt(p.X, p.Y); c.R != 255 {
			t.Errorf("orientation %d: marker not at %v", o, p)
		}
	}
}

func TestPrepareScalesLongSideTo1600(t *testing.T) {
	cases := []struct{ w, h, ww, wh int }{
		{4000, 3000, 1600, 1200},
		{3000, 4000, 1200, 1600},
		{1200, 900, 1200, 900}, // small photos are not enlarged
	}
	for _, c := range cases {
		out, err := Prepare(encodeJPEG(t, halves(c.w, c.h)))
		if err != nil {
			t.Fatal(err)
		}
		b := decode(t, out).Bounds()
		if b.Dx() != c.ww || b.Dy() != c.wh {
			t.Errorf("%dx%d became %dx%d, want %dx%d", c.w, c.h, b.Dx(), b.Dy(), c.ww, c.wh)
		}
	}
}

func TestPreparePNGBecomesJPEGOnWhite(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 50, 50)) // fully transparent
	out, err := Prepare(encodePNG(t, img))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := decode(t, out).At(25, 25).RGBA()
	if r < 0xF000 || g < 0xF000 || b < 0xF000 {
		t.Fatalf("transparent PNG became %x %x %x, want white", r, g, b)
	}
}

func TestPrepareRejectsNonImage(t *testing.T) {
	for _, data := range [][]byte{
		[]byte("%PDF-1.7 a permission slip"),
		[]byte("hello"),
		nil,
		{0xFF, 0xD8, 0xFF, 0x00, 0x01}, // starts like a JPEG, is not one
	} {
		if _, err := Prepare(data); !errors.Is(err, ErrNotPhoto) {
			t.Errorf("Prepare(%q) = %v, want ErrNotPhoto", data, err)
		}
	}
}

func TestPrepareRejectsHugePhoto(t *testing.T) {
	// A PNG header that claims 10000 x 10000 pixels; decoding stops at the header.
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], 10000)
	binary.BigEndian.PutUint32(ihdr[4:], 10000)
	ihdr[8], ihdr[9] = 8, 2 // 8-bit RGB
	chunk := append([]byte("IHDR"), ihdr...)
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	binary.Write(&b, binary.BigEndian, uint32(len(ihdr)))
	b.Write(chunk)
	binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	if _, err := Prepare(b.Bytes()); !errors.Is(err, ErrTooBig) {
		t.Fatalf("got %v, want ErrTooBig", err)
	}
}

func TestPrepareHEICOffMacSaysWhatToDo(t *testing.T) {
	defer func(g string) { goos = g }(goos)
	goos = "linux"
	heic := append([]byte{0, 0, 0, 24}, []byte("ftypheic")...)
	heic = append(heic, make([]byte, 16)...)
	_, err := Prepare(heic)
	if !errors.Is(err, ErrHEICNotMac) {
		t.Fatalf("got %v, want ErrHEICNotMac", err)
	}
}

func TestPrepareHEICOnMac(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sips is part of macOS")
	}
	dir := t.TempDir()
	in, heic := filepath.Join(dir, "in.jpg"), filepath.Join(dir, "in.heic")
	if err := os.WriteFile(in, encodeJPEG(t, halves(64, 48)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("sips", "-s", "format", "heic", in, "--out", heic).Run(); err != nil {
		t.Skipf("this Mac cannot write HEIC: %v", err)
	}
	data, err := os.ReadFile(heic)
	if err != nil {
		t.Fatal(err)
	}
	if sniff(data) != "heic" {
		t.Fatalf("sips output not recognised as HEIC: % x", data[:12])
	}
	out, err := Prepare(data)
	if err != nil {
		t.Fatal(err)
	}
	if b := decode(t, out).Bounds(); b.Dx() != 64 || b.Dy() != 48 {
		t.Fatalf("size %dx%d, want 64x48", b.Dx(), b.Dy())
	}
}

func BenchmarkPreparePhonePhoto(b *testing.B) {
	var buf bytes.Buffer
	jpeg.Encode(&buf, halves(4032, 3024), &jpeg.Options{Quality: 90})
	data := buf.Bytes()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Prepare(data); err != nil {
			b.Fatal(err)
		}
	}
}
