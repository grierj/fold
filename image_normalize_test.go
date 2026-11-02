package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"reflect"
	"testing"
)

// Block colors for the orientation fixtures, row by row of a 2 x 3 grid.
var (
	cRed     = color.RGBA{R: 230, A: 255}
	cGreen   = color.RGBA{G: 200, A: 255}
	cBlue    = color.RGBA{B: 230, A: 255}
	cYellow  = color.RGBA{R: 230, G: 230, A: 255}
	cMagenta = color.RGBA{R: 230, B: 230, A: 255}
	cCyan    = color.RGBA{G: 230, B: 230, A: 255}

	gridColors = []color.RGBA{cRed, cGreen, cBlue, cYellow, cMagenta, cCyan}
)

const blockPx = 32 // a block is big enough to survive JPEG chroma subsampling

// Byte patterns that must never reach storage.
var (
	gpsMarker   = []byte("GPSLEAK-HOME")
	gpsLatitude = []byte{37, 0, 0, 0, 1, 0, 0, 0, 46, 0, 0, 0, 1, 0, 0, 0} // 37/1, 46/1
)

// blockImage draws a cols x rows grid of solid blocks, left to right, top to bottom.
func blockImage(cols, rows int, colors []color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, cols*blockPx, rows*blockPx))
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			draw.Draw(img, image.Rect(c*blockPx, r*blockPx, (c+1)*blockPx, (r+1)*blockPx),
				&image.Uniform{colors[r*cols+c]}, image.Point{}, draw.Src)
		}
	}
	return img
}

func jpegBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// exifBlock builds an Exif APP1 payload (little endian) with an orientation,
// a maker string carrying gpsMarker and a GPS IFD with a latitude.
func exifBlock(orientation uint16) []byte {
	le := binary.LittleEndian
	maker := append(append([]byte(nil), gpsMarker...), 0)
	const ifd0 = 8
	const nIFD0 = 3
	makerOff := ifd0 + 2 + nIFD0*12 + 4
	gpsOff := makerOff + len(maker)
	if gpsOff%2 == 1 {
		gpsOff++
	}
	latOff := gpsOff + 2 + 2*12 + 4

	var b bytes.Buffer
	entry := func(tag, typ uint16, count uint32, val []byte) {
		var e [12]byte
		le.PutUint16(e[0:], tag)
		le.PutUint16(e[2:], typ)
		le.PutUint32(e[4:], count)
		copy(e[8:], val)
		b.Write(e[:])
	}
	u32 := func(v int) []byte { var x [4]byte; le.PutUint32(x[:], uint32(v)); return x[:] }
	u16 := func(v uint16) []byte { var x [2]byte; le.PutUint16(x[:], v); return x[:] }

	b.WriteString("II")
	b.Write(u16(42))
	b.Write(u32(ifd0))
	b.Write(u16(nIFD0))
	entry(0x010F, 2, uint32(len(maker)), u32(makerOff))
	entry(0x0112, 3, 1, u16(orientation))
	entry(0x8825, 4, 1, u32(gpsOff))
	b.Write(u32(0))
	b.Write(maker)
	for b.Len() < gpsOff {
		b.WriteByte(0)
	}
	b.Write(u16(2))
	entry(0x0001, 2, 2, []byte{'N', 0})
	entry(0x0002, 5, 2, u32(latOff))
	b.Write(u32(0))
	b.Write(gpsLatitude)
	return append([]byte("Exif\x00\x00"), b.Bytes()...)
}

// withEXIF inserts an APP1 segment with the payload right after the SOI marker.
func withEXIF(jpg, payload []byte) []byte {
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	out := append([]byte(nil), jpg[:2]...)
	out = append(out, seg...)
	out = append(out, payload...)
	return append(out, jpg[2:]...)
}

func pngChunk(typ string, data []byte) []byte {
	var b bytes.Buffer
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(data)))
	b.Write(n[:])
	b.WriteString(typ)
	b.Write(data)
	binary.BigEndian.PutUint32(n[:], crc32.ChecksumIEEE(append([]byte(typ), data...)))
	b.Write(n[:])
	return b.Bytes()
}

// pngWithText inserts a tEXt chunk right after the 8-byte signature and IHDR (33 bytes).
func pngWithText(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	p := b.Bytes()
	chunk := pngChunk("tEXt", append([]byte("Comment\x00"), gpsMarker...))
	out := append([]byte(nil), p[:33]...)
	out = append(out, chunk...)
	return append(out, p[33:]...)
}

// pngHeaderOnly is a PNG signature and IHDR claiming w x h and nothing else, so
// any attempt to decode the pixels would fail differently from the size refusal.
func pngHeaderOnly(w, h uint32) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 6 // 8 bit RGBA
	return append([]byte("\x89PNG\r\n\x1a\n"), pngChunk("IHDR", ihdr)...)
}

// normalizeBytes runs normalizeImage over data as the upload path does.
func normalizeBytes(t *testing.T, data []byte, mime string) ([]byte, string, error) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "img-*")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	_, outMime, err := normalizeImage(f, mime)
	if err != nil {
		return nil, "", err
	}
	if pos, _ := f.Seek(0, 1); pos != 0 {
		t.Fatalf("file left at offset %d, want 0", pos)
	}
	out, rerr := os.ReadFile(f.Name())
	if rerr != nil {
		t.Fatal(rerr)
	}
	return out, outMime, nil
}

func near(a, b color.Color) bool {
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	d := func(x, y uint32) int {
		v := int(x>>8) - int(y>>8)
		if v < 0 {
			v = -v
		}
		return v
	}
	return d(ar, br) <= 40 && d(ag, bg) <= 40 && d(ab, bb) <= 40
}

// assertGrid checks the center of every block of img against want (row by row).
func assertGrid(t *testing.T, img image.Image, cols, rows int, want []color.RGBA) {
	t.Helper()
	b := img.Bounds()
	if b.Dx() != cols*blockPx || b.Dy() != rows*blockPx {
		t.Fatalf("size %dx%d, want %dx%d", b.Dx(), b.Dy(), cols*blockPx, rows*blockPx)
	}
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			got := img.At(c*blockPx+blockPx/2, r*blockPx+blockPx/2)
			if !near(got, want[r*cols+c]) {
				t.Errorf("block (col %d, row %d) = %v, want %v", c, r, got, want[r*cols+c])
			}
		}
	}
}

func TestNormalizeJPEGStripsEXIF(t *testing.T) {
	in := withEXIF(jpegBytes(t, blockImage(2, 3, gridColors)), exifBlock(1))
	if !bytes.Contains(in, gpsMarker) || !bytes.Contains(in, gpsLatitude) || !bytes.Contains(in, []byte("Exif\x00\x00")) {
		t.Fatal("fixture should carry EXIF with GPS")
	}
	out, mime, err := normalizeBytes(t, in, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if mime != "image/jpeg" {
		t.Fatalf("mime %q", mime)
	}
	for name, pat := range map[string][]byte{"Exif marker": []byte("Exif"), "maker string": gpsMarker, "GPS latitude": gpsLatitude} {
		if bytes.Contains(out, pat) {
			t.Errorf("stored JPEG still contains the %s", name)
		}
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("stored bytes are not a JPEG: %v", err)
	}
	assertGrid(t, img, 2, 3, gridColors)
}

func TestNormalizeJPEGOrientation(t *testing.T) {
	// Expected grids, row by row, for the 2 x 3 fixture
	//   R G
	//   B Y
	//   M C
	r, g, b, y, m, c := cRed, cGreen, cBlue, cYellow, cMagenta, cCyan
	cases := []struct {
		orientation uint16
		cols, rows  int
		want        []color.RGBA
	}{
		{1, 2, 3, []color.RGBA{r, g, b, y, m, c}},
		{3, 2, 3, []color.RGBA{c, m, y, b, g, r}}, // rotate 180
		{6, 3, 2, []color.RGBA{m, b, r, c, y, g}}, // rotate 90 clockwise
		{8, 3, 2, []color.RGBA{g, y, c, r, b, m}}, // rotate 90 counterclockwise
		{2, 2, 3, []color.RGBA{g, r, y, b, c, m}}, // mirror horizontally
		{4, 2, 3, []color.RGBA{m, c, b, y, r, g}}, // mirror vertically
		{5, 3, 2, []color.RGBA{r, b, m, g, y, c}}, // transpose
		{7, 3, 2, []color.RGBA{c, y, g, m, b, r}}, // transverse
	}
	for _, tc := range cases {
		in := withEXIF(jpegBytes(t, blockImage(2, 3, gridColors)), exifBlock(tc.orientation))
		out, _, err := normalizeBytes(t, in, "image/jpeg")
		if err != nil {
			t.Fatalf("orientation %d: %v", tc.orientation, err)
		}
		img, err := jpeg.Decode(bytes.NewReader(out))
		if err != nil {
			t.Fatalf("orientation %d: %v", tc.orientation, err)
		}
		t.Run("orientation"+string(rune('0'+tc.orientation)), func(t *testing.T) {
			assertGrid(t, img, tc.cols, tc.rows, tc.want)
		})
	}
}

func TestJPEGOrientationParser(t *testing.T) {
	base := jpegBytes(t, blockImage(2, 3, gridColors))
	if got := jpegOrientation(base); got != 1 {
		t.Errorf("no EXIF: %d, want 1", got)
	}
	for o := uint16(1); o <= 8; o++ {
		if got := jpegOrientation(withEXIF(base, exifBlock(o))); got != int(o) {
			t.Errorf("orientation %d read as %d", o, got)
		}
	}
	// Garbage never panics and reads as upright.
	bad := exifBlock(6)
	for cut := 0; cut < len(bad); cut += 3 {
		_ = jpegOrientation(withEXIF(base, bad[:cut]))
	}
	if got := jpegOrientation(withEXIF(base, exifBlock(9))); got != 1 {
		t.Errorf("orientation 9 read as %d, want 1", got)
	}
	if got := jpegOrientation([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0xFF, 0xFF}); got != 1 {
		t.Errorf("truncated segment read as %d, want 1", got)
	}
}

func TestNormalizePNGStripsTextChunk(t *testing.T) {
	in := pngWithText(t, blockImage(2, 3, gridColors))
	if !bytes.Contains(in, []byte("tEXt")) || !bytes.Contains(in, gpsMarker) {
		t.Fatal("fixture should carry a tEXt chunk")
	}
	out, mime, err := normalizeBytes(t, in, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if mime != "image/png" || bytes.Contains(out, []byte("tEXt")) || bytes.Contains(out, gpsMarker) {
		t.Fatalf("PNG text survived (mime %q)", mime)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	assertGrid(t, img, 2, 3, gridColors)
}

func animatedGIF(t *testing.T, frames int) []byte {
	t.Helper()
	g := &gif.GIF{LoopCount: 5}
	for i := 0; i < frames; i++ {
		p := image.NewPaletted(image.Rect(0, 0, 8, 8), palette.Plan9)
		draw.Draw(p, p.Bounds(), &image.Uniform{gridColors[i%len(gridColors)]}, image.Point{}, draw.Src)
		g.Image = append(g.Image, p)
		g.Delay = append(g.Delay, 10*(i+1))
		g.Disposal = append(g.Disposal, gif.DisposalNone)
	}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, g); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestNormalizeGIFKeepsAnimationAndDropsComments(t *testing.T) {
	in := animatedGIF(t, 3)
	// Splice a comment extension in after the header, screen descriptor and global color table.
	pos := 13
	if in[10]&0x80 != 0 {
		pos += 3 << (in[10]&7 + 1)
	}
	comment := append([]byte{0x21, 0xFE, byte(len(gpsMarker))}, gpsMarker...)
	comment = append(comment, 0)
	spliced := append(append(append([]byte(nil), in[:pos]...), comment...), in[pos:]...)
	if _, err := gif.DecodeAll(bytes.NewReader(spliced)); err != nil {
		t.Fatalf("fixture is not a GIF: %v", err)
	}

	out, mime, err := normalizeBytes(t, spliced, "image/gif")
	if err != nil {
		t.Fatal(err)
	}
	if mime != "image/gif" || bytes.Contains(out, gpsMarker) {
		t.Fatalf("GIF comment survived (mime %q)", mime)
	}
	g, err := gif.DecodeAll(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Image) != 3 {
		t.Fatalf("%d frames, want 3", len(g.Image))
	}
	if want := []int{10, 20, 30}; !reflect.DeepEqual(g.Delay, want) {
		t.Errorf("delays %v, want %v", g.Delay, want)
	}
	if want := []byte{gif.DisposalNone, gif.DisposalNone, gif.DisposalNone}; !reflect.DeepEqual(g.Disposal, want) {
		t.Errorf("disposal %v, want %v", g.Disposal, want)
	}
	if g.LoopCount != 5 {
		t.Errorf("loop count %d, want 5", g.LoopCount)
	}
	for i, f := range g.Image {
		if !near(f.At(4, 4), gridColors[i]) {
			t.Errorf("frame %d color %v, want %v", i, f.At(4, 4), gridColors[i])
		}
	}
}

func TestNormalizeGIFFrameCap(t *testing.T) {
	if _, _, err := normalizeBytes(t, animatedGIF(t, maxGIFFrames), "image/gif"); err != nil {
		t.Fatalf("%d frames should be accepted: %v", maxGIFFrames, err)
	}
	_, _, err := normalizeBytes(t, animatedGIF(t, maxGIFFrames+1), "image/gif")
	if !errors.Is(err, errImageTooLarge) {
		t.Fatalf("too many frames: %v, want errImageTooLarge", err)
	}
}

func TestNormalizeRefusesHugeDimensionsWithoutDecoding(t *testing.T) {
	// The header only: a decode would fail as unreadable, so a "too large"
	// answer proves the size was read from the header alone.
	for name, hdr := range map[string][]byte{
		"gigapixel":     pngHeaderOnly(100000, 100000),
		"just over cap": pngHeaderOnly(5001, 5000),
		"absurd side":   pngHeaderOnly(1, 30000),
	} {
		_, _, err := normalizeBytes(t, hdr, "image/png")
		if !errors.Is(err, errImageTooLarge) {
			t.Errorf("%s: %v, want errImageTooLarge", name, err)
		}
	}
	// The PNG reader itself refuses an empty canvas.
	if _, _, err := normalizeBytes(t, pngHeaderOnly(0, 10), "image/png"); !errors.Is(err, errImageUnreadable) {
		t.Errorf("zero width: %v, want errImageUnreadable", err)
	}
}

func TestNormalizeRefusesCorruptImages(t *testing.T) {
	good := jpegBytes(t, blockImage(2, 3, gridColors))
	for name, in := range map[string][]byte{
		"truncated JPEG": good[:len(good)/2],
		"not an image":   []byte("\xFF\xD8 definitely not a jpeg"),
	} {
		if _, _, err := normalizeBytes(t, in, "image/jpeg"); !errors.Is(err, errImageUnreadable) {
			t.Errorf("%s: %v, want errImageUnreadable", name, err)
		}
	}
	if _, _, err := normalizeBytes(t, good, "image/webp"); err == nil {
		t.Error("WebP must never be normalized")
	}
}
