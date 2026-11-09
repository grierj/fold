package images

import (
	"bytes"
	"compress/lzw"
	"encoding/binary"
	"errors"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"reflect"
	"testing"

	"fold/internal/images/imagetest"
)

// normalizeBytes runs Normalize over data as the upload path does.
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
	_, outMime, err := Normalize(f, mime)
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

func TestNormalizeJPEGStripsEXIF(t *testing.T) {
	in := imagetest.WithEXIF(imagetest.JPEGBytes(t, imagetest.BlockImage(2, 3, imagetest.GridColors)), imagetest.ExifBlock(1))
	if !bytes.Contains(in, imagetest.GPSMarker) || !bytes.Contains(in, imagetest.GPSLatitude) || !bytes.Contains(in, []byte("Exif\x00\x00")) {
		t.Fatal("fixture should carry EXIF with GPS")
	}
	out, mime, err := normalizeBytes(t, in, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if mime != "image/jpeg" {
		t.Fatalf("mime %q", mime)
	}
	for name, pat := range map[string][]byte{"Exif marker": []byte("Exif"), "maker string": imagetest.GPSMarker, "GPS latitude": imagetest.GPSLatitude} {
		if bytes.Contains(out, pat) {
			t.Errorf("stored JPEG still contains the %s", name)
		}
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("stored bytes are not a JPEG: %v", err)
	}
	imagetest.AssertGrid(t, img, 2, 3, imagetest.GridColors)
}

func TestNormalizeJPEGOrientation(t *testing.T) {
	// Expected grids, row by row, for the 2 x 3 fixture
	//   R G
	//   B Y
	//   M C
	r, g, b, y, m, c := imagetest.CRed, imagetest.CGreen, imagetest.CBlue, imagetest.CYellow, imagetest.CMagenta, imagetest.CCyan
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
		in := imagetest.WithEXIF(imagetest.JPEGBytes(t, imagetest.BlockImage(2, 3, imagetest.GridColors)), imagetest.ExifBlock(tc.orientation))
		out, _, err := normalizeBytes(t, in, "image/jpeg")
		if err != nil {
			t.Fatalf("orientation %d: %v", tc.orientation, err)
		}
		img, err := jpeg.Decode(bytes.NewReader(out))
		if err != nil {
			t.Fatalf("orientation %d: %v", tc.orientation, err)
		}
		t.Run("orientation"+string(rune('0'+tc.orientation)), func(t *testing.T) {
			imagetest.AssertGrid(t, img, tc.cols, tc.rows, tc.want)
		})
	}
}

func TestJPEGOrientationParser(t *testing.T) {
	base := imagetest.JPEGBytes(t, imagetest.BlockImage(2, 3, imagetest.GridColors))
	if got := jpegOrientation(base); got != 1 {
		t.Errorf("no EXIF: %d, want 1", got)
	}
	for o := uint16(1); o <= 8; o++ {
		if got := jpegOrientation(imagetest.WithEXIF(base, imagetest.ExifBlock(o))); got != int(o) {
			t.Errorf("orientation %d read as %d", o, got)
		}
	}
	// Garbage never panics and reads as upright.
	bad := imagetest.ExifBlock(6)
	for cut := 0; cut < len(bad); cut += 3 {
		_ = jpegOrientation(imagetest.WithEXIF(base, bad[:cut]))
	}
	if got := jpegOrientation(imagetest.WithEXIF(base, imagetest.ExifBlock(9))); got != 1 {
		t.Errorf("orientation 9 read as %d, want 1", got)
	}
	if got := jpegOrientation([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0xFF, 0xFF}); got != 1 {
		t.Errorf("truncated segment read as %d, want 1", got)
	}
}

func TestNormalizePNGStripsTextChunk(t *testing.T) {
	in := imagetest.PNGWithText(t, imagetest.BlockImage(2, 3, imagetest.GridColors))
	if !bytes.Contains(in, []byte("tEXt")) || !bytes.Contains(in, imagetest.GPSMarker) {
		t.Fatal("fixture should carry a tEXt chunk")
	}
	out, mime, err := normalizeBytes(t, in, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if mime != "image/png" || bytes.Contains(out, []byte("tEXt")) || bytes.Contains(out, imagetest.GPSMarker) {
		t.Fatalf("PNG text survived (mime %q)", mime)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	imagetest.AssertGrid(t, img, 2, 3, imagetest.GridColors)
}

func TestNormalizeGIFKeepsAnimationAndDropsComments(t *testing.T) {
	in := imagetest.AnimatedGIF(t, 3)
	// Splice a comment extension in after the header, screen descriptor and global color table.
	pos := 13
	if in[10]&0x80 != 0 {
		pos += 3 << (in[10]&7 + 1)
	}
	comment := append([]byte{0x21, 0xFE, byte(len(imagetest.GPSMarker))}, imagetest.GPSMarker...)
	comment = append(comment, 0)
	spliced := append(append(append([]byte(nil), in[:pos]...), comment...), in[pos:]...)
	if _, err := gif.DecodeAll(bytes.NewReader(spliced)); err != nil {
		t.Fatalf("fixture is not a GIF: %v", err)
	}

	out, mime, err := normalizeBytes(t, spliced, "image/gif")
	if err != nil {
		t.Fatal(err)
	}
	if mime != "image/gif" || bytes.Contains(out, imagetest.GPSMarker) {
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
		if !imagetest.Near(f.At(4, 4), imagetest.GridColors[i]) {
			t.Errorf("frame %d color %v, want %v", i, f.At(4, 4), imagetest.GridColors[i])
		}
	}
}

func TestNormalizeGIFFrameCap(t *testing.T) {
	if _, _, err := normalizeBytes(t, imagetest.AnimatedGIF(t, MaxGIFFrames), "image/gif"); err != nil {
		t.Fatalf("%d frames should be accepted: %v", MaxGIFFrames, err)
	}
	_, _, err := normalizeBytes(t, imagetest.AnimatedGIF(t, MaxGIFFrames+1), "image/gif")
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("too many frames: %v, want ErrTooLarge", err)
	}
}

func TestNormalizeRefusesHugeDimensionsWithoutDecoding(t *testing.T) {
	// The header only: a decode would fail as unreadable, so a "too large"
	// answer proves the size was read from the header alone.
	for name, hdr := range map[string][]byte{
		"gigapixel":     imagetest.PNGHeaderOnly(100000, 100000),
		"just over cap": imagetest.PNGHeaderOnly(5001, 5000),
		"absurd side":   imagetest.PNGHeaderOnly(1, 30000),
	} {
		_, _, err := normalizeBytes(t, hdr, "image/png")
		if !errors.Is(err, ErrTooLarge) {
			t.Errorf("%s: %v, want ErrTooLarge", name, err)
		}
	}
	// The PNG reader itself refuses an empty canvas.
	if _, _, err := normalizeBytes(t, imagetest.PNGHeaderOnly(0, 10), "image/png"); !errors.Is(err, ErrUnreadable) {
		t.Errorf("zero width: %v, want ErrUnreadable", err)
	}
}

func TestNormalizeRefusesCorruptImages(t *testing.T) {
	good := imagetest.JPEGBytes(t, imagetest.BlockImage(2, 3, imagetest.GridColors))
	for name, in := range map[string][]byte{
		"truncated JPEG": good[:len(good)/2],
		"not an image":   []byte("\xFF\xD8 definitely not a jpeg"),
	} {
		if _, _, err := normalizeBytes(t, in, "image/jpeg"); !errors.Is(err, ErrUnreadable) {
			t.Errorf("%s: %v, want ErrUnreadable", name, err)
		}
	}
	if _, _, err := normalizeBytes(t, good, "image/webp"); err == nil {
		t.Error("WebP must never be normalized")
	}
}

// gifSubBlocks wraps data in GIF data sub-blocks with the zero terminator.
func gifSubBlocks(data []byte) []byte {
	var out []byte
	for len(data) > 0 {
		n := min(len(data), 255)
		out = append(append(out, byte(n)), data[:n]...)
		data = data[n:]
	}
	return append(out, 0)
}

// gifFrame is an image descriptor for a w x h frame with a two-color local table.
func gifFrame(w, h int, lzwData []byte) []byte {
	f := []byte{0x2C, 0, 0, 0, 0, byte(w), byte(w >> 8), byte(h), byte(h >> 8), 0x80}
	f = append(f, 0, 0, 0, 255, 255, 255) // local color table
	f = append(f, 2)                      // LZW minimum code size
	return append(f, gifSubBlocks(lzwData)...)
}

func gifHeader(w, h int) []byte {
	return append([]byte("GIF89a"), byte(w), byte(w>>8), byte(h), byte(h>>8), 0, 0, 0)
}

// hiddenFrameGIF is a GIF where image/gif sees `hidden` frames that a reader
// treating every extension as plain sub-blocks would miss: after "21 01" (plain
// text) Go skips a fixed 13 bytes, while a naive scanner reads the following
// "21 FE" comment and swallows the frames as comment data.
func hiddenFrameGIF(t *testing.T, hidden int) []byte {
	t.Helper()
	var lz bytes.Buffer
	zw := lzw.NewWriter(&lz, lzw.LSB, 2)
	zw.Write(make([]byte, 20*20))
	zw.Close()
	frame := gifFrame(20, 20, lz.Bytes())
	prefix := 10 + 6 + 1 // descriptor, color table and code size before the first sub-block
	unit := []byte{0x21, 0x01, 0x00, 0x21, 0xFE, byte(10 + prefix)}
	unit = append(unit, make([]byte, 9)...)
	unit = append(unit, 0x00)
	unit = append(unit, frame...)
	out := gifHeader(20, 20)
	for i := 0; i < hidden; i++ {
		out = append(out, unit...)
	}
	out = append(out, frame...)
	return append(out, 0x3B)
}

func TestNormalizeGIFRefusesFramesHiddenFromTheScan(t *testing.T) {
	in := hiddenFrameGIF(t, 12)
	g, err := gif.DecodeAll(bytes.NewReader(in))
	if err != nil || len(g.Image) <= 1 {
		t.Fatalf("fixture should show image/gif several frames; got %v, %v", g, err)
	}
	if _, _, ok := scanGIF(in); ok {
		t.Fatal("scanGIF accepted a file it reads differently from image/gif")
	}
	if _, _, err := normalizeBytes(t, in, "image/gif"); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("hidden frames: %v, want ErrUnreadable", err)
	}
}

func TestScanGIFFailsClosed(t *testing.T) {
	good := imagetest.AnimatedGIF(t, 2)
	if f, p, ok := scanGIF(good); !ok || f != 2 || p != 128 {
		t.Fatalf("plain animation: %d frames, %d pixels, ok %v", f, p, ok)
	}
	body := good[:len(good)-1] // without the trailer
	extra := func(b ...byte) []byte {
		return append(append(append([]byte(nil), body...), b...), 0x3B)
	}
	for name, in := range map[string][]byte{
		"empty":            nil,
		"truncated":        good[:len(good)-20],
		"no trailer":       body,
		"not a gif":        append([]byte("GIF90a"), good[6:]...),
		"unknown label":    extra(0x21, 0x77, 0),
		"bad control size": extra(0x21, 0xF9, 5, 0, 0, 0, 0, 0, 0),
		"bad app size":     extra(0x21, 0xFF, 10, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0),
		"plain text":       extra(0x21, 0x01, 0),
	} {
		if _, _, ok := scanGIF(in); ok {
			t.Errorf("%s: scanGIF accepted it", name)
		}
	}
}

func TestNormalizeGIFPixelBudget(t *testing.T) {
	// Five full-screen 5000 x 5000 frames: 125 million pixels, over the budget.
	// Only the structure is valid; refusal must come before any decoding.
	in := gifHeader(5000, 5000)
	for i := 0; i < 5; i++ {
		in = append(in, gifFrame(5000, 5000, []byte{0x4C, 0x01})...)
	}
	in = append(in, 0x3B)
	if _, _, err := normalizeBytes(t, in, "image/gif"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the pixel budget: %v, want ErrTooLarge", err)
	}
}

func TestJPEGOrientationAcceptsLong(t *testing.T) {
	base := imagetest.JPEGBytes(t, imagetest.BlockImage(2, 3, imagetest.GridColors))
	payload := imagetest.ExifBlock(6)
	const entry = 6 + 8 + 2 + 12 // the orientation entry follows the maker entry
	binary.LittleEndian.PutUint16(payload[entry+2:], 4)
	binary.LittleEndian.PutUint32(payload[entry+8:], 6)
	if got := jpegOrientation(imagetest.WithEXIF(base, payload)); got != 6 {
		t.Errorf("LONG orientation read as %d, want 6", got)
	}
	binary.LittleEndian.PutUint32(payload[entry+8:], 0x00010006) // high bytes set: not a valid value
	if got := jpegOrientation(imagetest.WithEXIF(base, payload)); got != 1 {
		t.Errorf("oversized LONG orientation read as %d, want 1", got)
	}
}
