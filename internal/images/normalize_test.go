package images

import (
	"bytes"
	"compress/lzw"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"reflect"
	"runtime"
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

// junkIDAT follows a header where a test needs the chunk walk to see the start
// of the pixel data but no pixel is ever decoded: a decode that was attempted
// would fail as unreadable, so ErrTooLarge shows the refusal came from the
// estimate alone.
func junkIDAT() []byte { return imagetest.PNGChunk("IDAT", []byte("not zlib")) }

var trns16 = imagetest.PNGChunk("tRNS", []byte{0x12, 0x34})

func TestPNGPixelBytes(t *testing.T) {
	for _, tc := range []struct {
		m           color.Model
		transparent bool
		want        int64
	}{
		{color.Palette{color.Black}, false, 1}, {color.Palette{color.Black}, true, 1},
		{color.GrayModel, false, 1}, {color.GrayModel, true, 4},
		{color.Gray16Model, false, 2}, {color.Gray16Model, true, 8},
		{color.RGBAModel, false, 4}, {color.NRGBAModel, false, 4},
		{color.RGBA64Model, false, 8}, {color.NRGBA64Model, false, 8}, {nil, false, 8},
	} {
		if got := pngPixelBytes(tc.m, tc.transparent); got != tc.want {
			t.Errorf("pngPixelBytes(%v, %v) = %d, want %d", tc.m, tc.transparent, got, tc.want)
		}
	}
}

func TestPNGMayBeTransparent(t *testing.T) {
	plain := append(imagetest.PNGHeader(4, 4, 8, 0, 0), junkIDAT()...)
	withTRNS := append(append(imagetest.PNGHeader(4, 4, 8, 0, 0), trns16...), junkIDAT()...)
	for name, tc := range map[string]struct {
		in   []byte
		want bool
	}{
		"pixel data follows the header":  {plain, false},
		"tRNS before the pixel data":     {withTRNS, true},
		"header and nothing else":        {imagetest.PNGHeader(4, 4, 8, 0, 0), true},
		"chunk longer than the file":     {append(imagetest.PNGHeader(4, 4, 8, 0, 0), 0x7F, 0xFF, 0xFF, 0xFF, 't', 'E', 'X', 't'), true},
		"tRNS hidden behind a long text": {append(append(imagetest.PNGHeader(4, 4, 8, 0, 0), imagetest.PNGChunk("tEXt", make([]byte, 9000))...), append(trns16, junkIDAT()...)...), true},
	} {
		if got := pngMayBeTransparent(tc.in); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

// A 5000 x 5000 16-bit PNG is a few hundred KB on the wire but decodes to
// 200 MB. The header is enough to refuse it.
func TestNormalizeRefusesSixteenBitPNGByMemoryEstimate(t *testing.T) {
	for name, hdr := range map[string][]byte{
		"16-bit RGBA":         imagetest.PNGHeader(5000, 5000, 16, 6, 0),
		"16-bit RGB":          imagetest.PNGHeader(5000, 5000, 16, 2, 0),
		"8-bit RGBA at 25 MP": imagetest.PNGHeaderOnly(5000, 5000), // 100 MB to decode
	} {
		if _, _, err := normalizeBytes(t, hdr, "image/png"); !errors.Is(err, ErrTooLarge) {
			t.Errorf("%s: %v, want ErrTooLarge", name, err)
		}
	}
	// 8-bit RGBA at 3500 x 3500 needs 49 MB: inside the budget, so it is read as
	// far as its (absent) pixels.
	if _, _, err := normalizeBytes(t, imagetest.PNGHeaderOnly(3500, 3500), "image/png"); !errors.Is(err, ErrUnreadable) {
		t.Errorf("8-bit RGBA at 3500 x 3500: %v, want it to pass the estimate and fail as unreadable", err)
	}
}

// DecodeConfig reports Gray16 for a 16-bit grayscale PNG without reading on,
// but a tRNS chunk makes the decoder build a 16-bit NRGBA picture, four times
// as large. The same canvas is refused with the chunk and accepted without it.
func TestNormalizeCountsGrayscaleTransparency(t *testing.T) {
	plain := append(imagetest.PNGHeader(2000, 2000, 16, 0, 0), junkIDAT()...)
	transparent := append(append(imagetest.PNGHeader(2000, 2000, 16, 0, 0), trns16...), junkIDAT()...)
	if _, _, err := normalizeBytes(t, transparent, "image/png"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("16-bit gray with tRNS at 2000 x 2000: %v, want ErrTooLarge", err)
	}
	if _, _, err := normalizeBytes(t, plain, "image/png"); !errors.Is(err, ErrUnreadable) {
		t.Errorf("16-bit gray at 2000 x 2000: %v, want it to pass the estimate and fail as unreadable", err)
	}
}

// An Adam7 PNG allocates its canvas and then its seven passes, twice a plain
// one. 3600 x 3600 of 8-bit color is inside the budget plain and over it
// interlaced.
func TestNormalizeCountsAdam7Passes(t *testing.T) {
	plain := append(imagetest.PNGHeader(3600, 3600, 8, 2, 0), junkIDAT()...)
	interlaced := append(imagetest.PNGHeader(3600, 3600, 8, 2, 1), junkIDAT()...)
	if _, _, err := normalizeBytes(t, interlaced, "image/png"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("interlaced: %v, want ErrTooLarge", err)
	}
	if _, _, err := normalizeBytes(t, plain, "image/png"); !errors.Is(err, ErrUnreadable) {
		t.Errorf("plain: %v, want it to pass the estimate and fail as unreadable", err)
	}
}

// png.Encode reads a 16-bit picture a pixel at a time and allocates every
// color, which is about one more canvas on top of the decode. A canvas whose
// decode alone fits the budget is refused once the encode is counted, for each
// 16-bit kind; the same pixels at 8 bits need no such copy and are accepted.
func TestNormalizeCountsTheEncodeOfSixteenBitPNG(t *testing.T) {
	for name, tc := range map[string]struct {
		hdr     []byte
		refused bool
	}{
		"16-bit gray, decode alone 32 MB":     {imagetest.PNGHeader(4000, 4000, 16, 0, 0), true},
		"16-bit RGBA, decode alone 46 MB":     {imagetest.PNGHeader(2400, 2400, 16, 6, 0), true},
		"16-bit RGB, decode alone 46 MB":      {imagetest.PNGHeader(2400, 2400, 16, 2, 0), true},
		"16-bit gray, decode and encode 55MB": {imagetest.PNGHeader(3700, 3700, 16, 0, 0), false},
		"8-bit RGBA, 46 MB, nothing to copy":  {imagetest.PNGHeader(3400, 3400, 8, 6, 0), false},
	} {
		_, _, err := normalizeBytes(t, append(tc.hdr, junkIDAT()...), "image/png")
		if tc.refused && !errors.Is(err, ErrTooLarge) {
			t.Errorf("%s: %v, want ErrTooLarge", name, err)
		}
		if !tc.refused && !errors.Is(err, ErrUnreadable) {
			t.Errorf("%s: %v, want it to pass the estimate and fail as unreadable", name, err)
		}
	}
}

func TestPNGBoxesPixels(t *testing.T) {
	for _, tc := range []struct {
		m    color.Model
		want bool
	}{
		{color.Palette{color.Black}, false}, {color.GrayModel, false}, {color.RGBAModel, false}, {color.NRGBAModel, false},
		{color.Gray16Model, true}, {color.RGBA64Model, true}, {color.NRGBA64Model, true}, {nil, true},
	} {
		if got := pngBoxesPixels(tc.m); got != tc.want {
			t.Errorf("pngBoxesPixels(%v) = %v, want %v", tc.m, got, tc.want)
		}
	}
}

// allocatedBy is the bytes f allocated in all (the cumulative TotalAlloc, so
// what was freed counts too): an upper bound on what it held at once.
func allocatedBy(f func()) int64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return int64(after.TotalAlloc - before.TotalAlloc)
}

// decoderOverhead is what a decode allocates beyond the pixels: the zlib and
// Huffman state and row buffers. The pictures below are large enough that the
// pixels it would hide are many times this.
const decoderOverhead = 512 << 10

// normalizeOverhead is the same for a whole Normalize: the encoders' state
// (flate's hash tables, Huffman tables, row buffers) and the copy of the
// result back into the upload. It stays far under scratchBytes, which budgets
// for it.
const normalizeOverhead = 4 << 20

func decodeConfig(t *testing.T, data []byte) image.Config {
	t.Helper()
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// allocatedByNormalize is what Normalize allocates in all for data.
func allocatedByNormalize(t *testing.T, data []byte, mime string) int64 {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "img-*")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	return allocatedBy(func() {
		if _, _, err := Normalize(f, mime); err != nil {
			t.Fatal(err)
		}
	})
}

// pngCases are real PNGs of every layout the decoder builds a different
// picture for, each large enough that its pixels dwarf the fixed overheads.
func pngCases(t *testing.T) map[string][]byte {
	t.Helper()
	enc := func(img image.Image) []byte {
		var b bytes.Buffer
		if err := png.Encode(&b, img); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	canvas := image.Rect(0, 0, 1200, 1200)
	gray := enc(image.NewGray(canvas))
	return map[string][]byte{
		"RGBA":                       enc(image.NewRGBA(canvas)),
		"palette":                    enc(image.NewPaletted(canvas, color.Palette{color.Black, color.White})),
		"gray":                       gray,
		"16-bit gray":                imagetest.Gray16PNG(t, 1200, 1200),
		"16-bit gray with tRNS":      imagetest.GrayTransparentPNG(t, 1200, 1200),
		"16-bit RGB":                 imagetest.SixteenBitPNG(t, 1200, 1200, true),
		"16-bit RGBA":                imagetest.SixteenBitPNG(t, 1200, 1200, false),
		"Adam7 RGB":                  imagetest.Adam7RGBPNG(t, 1200, 1200),
		"Adam7 RGB, an odd canvas":   imagetest.Adam7RGBPNG(t, 1201, 1197),
		"gray with tRNS, depth 8":    imagetest.PNGWithChunkBeforeIDAT(t, gray, imagetest.PNGChunk("tRNS", []byte{0, 7})),
		"RGBA PNG with a text chunk": imagetest.PNGWithText(t, image.NewRGBA(canvas)),
	}
}

// The PNG estimate is held against what image/png really allocates for real
// files (cumulatively, so it bounds the heap whenever the collector runs): if
// it is lower than the decoder, the budget is not a bound.
func TestPNGEstimateCoversTheDecoder(t *testing.T) {
	for name, in := range pngCases(t) {
		decode, _ := pngBytes(in, decodeConfig(t, in))
		got := allocatedBy(func() {
			if _, err := png.Decode(bytes.NewReader(in)); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		})
		if got > decode+decoderOverhead {
			t.Errorf("%s: png.Decode allocated %d bytes, the estimate is %d", name, got, decode)
		}
	}
	// The files above are what makes the transparent and interlaced estimates
	// matter: without them the decoder would need several times as much.
	decode, encode := pngBytes(imagetest.GrayTransparentPNG(t, 1200, 1200), image.Config{ColorModel: color.Gray16Model, Width: 1200, Height: 1200})
	if decode != 1200*1200*8 || encode != 1200*1200*8 {
		t.Errorf("16-bit gray with tRNS counted %d and %d bytes, want 8 a pixel each", decode, encode)
	}
	decode, encode = pngBytes(imagetest.Adam7RGBPNG(t, 1200, 1200), image.Config{ColorModel: color.RGBAModel, Width: 1200, Height: 1200})
	if decode != 1200*1200*4*2 || encode != 0 {
		t.Errorf("Adam7 RGB counted %d and %d bytes, want two canvases and no encode copy", decode, encode)
	}
}

// The whole of Normalize, the decode and the encode, is held against the
// estimate for every PNG layout, so a per-pixel allocation in the encoder
// cannot hide behind a decoder-only check: 16-bit pictures allocate a color a
// pixel there.
func TestNormalizeAllocationIsCoveredForPNG(t *testing.T) {
	for name, in := range pngCases(t) {
		decode, encode := pngBytes(in, decodeConfig(t, in))
		got := allocatedByNormalize(t, in, "image/png")
		if want := decode + encode + int64(len(in)) + normalizeOverhead; got > want {
			t.Errorf("%s: Normalize allocated %d bytes, the estimate is %d", name, got, want)
		}
	}
}

// jpegCase is a real JPEG and the reason it matters.
type jpegCase struct {
	name string
	data []byte
}

func jpegCases(t *testing.T) []jpegCase {
	t.Helper()
	rgba := image.NewRGBA(image.Rect(0, 0, 1201, 1003)) // not a whole number of MCUs
	for i := range rgba.Pix {
		rgba.Pix[i] = byte(i)
	}
	var baseline, grayBaseline bytes.Buffer
	if err := jpeg.Encode(&baseline, rgba, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&grayBaseline, image.NewGray(image.Rect(0, 0, 1201, 1003)), nil); err != nil {
		t.Fatal(err)
	}
	turn := func(in []byte) []byte { return imagetest.WithEXIF(in, imagetest.ExifBlock(6)) }
	cmyk := imagetest.ProgressiveJPEG(1200, 1200, []byte{1, 2, 3, 4}, []byte{0x11, 0x11, 0x11, 0x11}, 0)
	return []jpegCase{
		{"baseline 4:2:0, an odd canvas", baseline.Bytes()},
		{"baseline 4:2:0, turned", turn(baseline.Bytes())},
		{"baseline gray", grayBaseline.Bytes()},
		{"progressive 4:4:4", imagetest.ProgressiveJPEG(1200, 1200, []byte{1, 2, 3}, []byte{0x11, 0x11, 0x11}, -1)},
		{"progressive 4:2:0, an odd canvas", imagetest.ProgressiveJPEG(1201, 1003, []byte{1, 2, 3}, []byte{0x22, 0x11, 0x11}, -1)},
		{"progressive 4:2:2", imagetest.ProgressiveJPEG(1200, 1200, []byte{1, 2, 3}, []byte{0x21, 0x11, 0x11}, -1)},
		{"progressive gray", imagetest.ProgressiveJPEG(1201, 1003, []byte{1}, []byte{0x11}, -1)},
		{"progressive RGB", imagetest.ProgressiveJPEG(1200, 1200, []byte{'R', 'G', 'B'}, []byte{0x11, 0x11, 0x11}, -1)},
		{"progressive CMYK", cmyk},
		{"progressive CMYK, turned", turn(cmyk)},
		{"progressive YCCK", imagetest.ProgressiveJPEG(1200, 1200, []byte{1, 2, 3, 4}, []byte{0x22, 0x11, 0x11, 0x22}, 2)},
		// DecodeConfig stops at the first scan and calls these YCbCr; the decoder
		// reads the Adobe segment after the scans and makes an RGB picture.
		{"progressive 4:4:4, a late Adobe segment says RGB", imagetest.WithLateAdobe(imagetest.ProgressiveJPEG(1200, 1200, []byte{1, 2, 3}, []byte{0x11, 0x11, 0x11}, -1), 0)},
		{"progressive 4:2:0, a late Adobe segment says RGB", imagetest.WithLateAdobe(imagetest.ProgressiveJPEG(1201, 1003, []byte{1, 2, 3}, []byte{0x22, 0x11, 0x11}, -1), 0)},
		// An early JFIF segment makes DecodeConfig call RGB ids YCbCr; a later APP0
		// that is not JFIF clears the flag and the decoder ends with RGB.
		{"progressive RGB ids, early JFIF and a late non-JFIF APP0", imagetest.WithLateAPP0(imagetest.WithEarlyJFIF(imagetest.ProgressiveJPEG(1200, 1200, []byte("RGB"), []byte{0x11, 0x11, 0x11}, -1)))},
		{"progressive 4:2:0 RGB ids, early JFIF and a late non-JFIF APP0", imagetest.WithLateAPP0(imagetest.WithEarlyJFIF(imagetest.ProgressiveJPEG(1201, 1003, []byte("RGB"), []byte{0x22, 0x11, 0x11}, -1)))},
	}
}

// The JPEG estimate is held against what image/jpeg really allocates for real
// baseline and progressive files in each color layout, including the converted
// copy of an RGB or CMYK file and of one an Adobe segment after the scans turns
// into RGB.
func TestJPEGEstimateCoversTheDecoder(t *testing.T) {
	for _, tc := range jpegCases(t) {
		cfg := decodeConfig(t, tc.data)
		frame, ok := scanJPEGFrame(tc.data)
		if !ok {
			t.Fatalf("%s: frame not found", tc.name)
		}
		est := jpegDecodeBytes(frame, cfg, frame.converted(cfg, tc.data))
		got := allocatedBy(func() {
			if _, err := jpeg.Decode(bytes.NewReader(tc.data)); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
		})
		if got > est+decoderOverhead {
			t.Errorf("%s: jpeg.Decode allocated %d bytes, the estimate is %d", tc.name, got, est)
		}
	}
}

// The whole of Normalize, decode, turn and encode, is held against the
// estimate: a CMYK picture is written back a pixel at a time, and a turned one
// is copied.
func TestNormalizeAllocationIsCoveredForJPEG(t *testing.T) {
	for _, tc := range jpegCases(t) {
		cfg := decodeConfig(t, tc.data)
		frame, ok := scanJPEGFrame(tc.data)
		if !ok {
			t.Fatalf("%s: frame not found", tc.name)
		}
		got := allocatedByNormalize(t, tc.data, "image/jpeg")
		if want := jpegWorkingBytes(frame, cfg, tc.data, jpegOrientation(tc.data)) + int64(len(tc.data)) + normalizeOverhead; got > want {
			t.Errorf("%s: Normalize allocated %d bytes, the estimate is %d", tc.name, got, want)
		}
	}
}

// converted decides whether the decoder may build a second picture. The
// pictures of the first group never need one; DecodeConfig's color model hides
// that an Adobe segment after the first scan makes the second group RGB, and
// that only a segment after the frame header can.
func TestJPEGConverted(t *testing.T) {
	prog := func(ids string, hv []byte, adobe int) []byte {
		return imagetest.ProgressiveJPEG(64, 64, []byte(ids), hv, adobe)
	}
	yuv := []byte{0x11, 0x11, 0x11}
	for name, tc := range map[string]struct {
		in   []byte
		want bool
	}{
		"gray":                                {prog("\x01", []byte{0x11}, -1), false},
		"YCbCr":                               {prog("\x01\x02\x03", yuv, -1), false},
		"YCbCr, Adobe before the frame":       {prog("\x01\x02\x03", yuv, 1), false},
		"CMYK":                                {prog("\x01\x02\x03\x04", []byte{0x11, 0x11, 0x11, 0x11}, 0), true},
		"RGB by its component ids":            {prog("RGB", yuv, -1), true},
		"RGB by an Adobe segment up front":    {prog("\x01\x02\x03", yuv, 0), true},
		"YCbCr, an Adobe segment at the end":  {imagetest.WithLateAdobe(prog("\x01\x02\x03", yuv, -1), 0), true},
		"YCbCr, an Adobe YCbCr at the end":    {imagetest.WithLateAdobe(prog("\x01\x02\x03", yuv, -1), 1), true},
		"YCbCr, an APP0 at the end":           {imagetest.WithLateAPP0(prog("\x01\x02\x03", yuv, -1)), true},
		"RGB ids, JFIF up front":              {imagetest.WithEarlyJFIF(prog("RGB", yuv, -1)), false},
		"RGB ids, JFIF up front, APP0 at end": {imagetest.WithLateAPP0(imagetest.WithEarlyJFIF(prog("RGB", yuv, -1))), true},
	} {
		frame, ok := scanJPEGFrame(tc.in)
		if !ok {
			t.Fatalf("%s: frame not found", name)
		}
		if got := frame.converted(decodeConfig(t, tc.in), tc.in); got != tc.want {
			t.Errorf("%s: converted = %v, want %v", name, got, tc.want)
		}
	}
}

// A progressive 4:4:4 picture of 1800 x 1800 is inside the budget, and
// DecodeConfig calls it YCbCr with or without an Adobe segment after the scans,
// but with one the decoder builds an RGBA copy too, which with the copy the
// encoder reads it through takes it over the budget. Only the plain one may
// be decoded.
func TestNormalizeCountsALateAdobeSegment(t *testing.T) {
	plain := imagetest.ProgressiveJPEG(1800, 1800, []byte{1, 2, 3}, []byte{0x11, 0x11, 0x11}, -1)
	late := imagetest.WithLateAdobe(plain, 0)
	if cfg := decodeConfig(t, late); cfg.ColorModel != color.YCbCrModel {
		t.Fatalf("DecodeConfig reports %v for the late-Adobe file, the test needs YCbCr", cfg.ColorModel)
	}
	if _, _, err := normalizeBytes(t, plain, "image/jpeg"); err != nil {
		t.Errorf("plain: %v, want it accepted", err)
	}
	if _, _, err := normalizeBytes(t, late, "image/jpeg"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("late Adobe: %v, want ErrTooLarge", err)
	}
}

// An early JFIF segment makes DecodeConfig report YCbCr for a file with RGB
// component identifiers, and a non-JFIF APP0 after the scans clears the flag,
// so the decoder builds an RGBA picture DecodeConfig did not announce. A 1800 x
// 1800 progressive picture is inside the budget without that copy and over it
// with it, so only the file with the late APP0 is refused, and before decoding.
func TestNormalizeCountsALateAPP0Segment(t *testing.T) {
	plain := imagetest.WithEarlyJFIF(imagetest.ProgressiveJPEG(1800, 1800, []byte("RGB"), []byte{0x11, 0x11, 0x11}, -1))
	late := imagetest.WithLateAPP0(plain)
	for name, in := range map[string][]byte{"plain": plain, "late": late} {
		if cfg := decodeConfig(t, in); cfg.ColorModel != color.YCbCrModel {
			t.Fatalf("DecodeConfig reports %v for the %s file, the test needs YCbCr", cfg.ColorModel, name)
		}
	}
	img, err := jpeg.Decode(bytes.NewReader(late))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := img.(*image.RGBA); !ok {
		t.Fatalf("full decode of the late-APP0 file is %T, the test needs *image.RGBA", img)
	}
	img, err = jpeg.Decode(bytes.NewReader(plain))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := img.(*image.YCbCr); !ok {
		t.Fatalf("full decode of the plain file is %T, the test needs *image.YCbCr", img)
	}
	if _, _, err := normalizeBytes(t, plain, "image/jpeg"); err != nil {
		t.Errorf("plain: %v, want it accepted", err)
	}
	got := allocatedBy(func() {
		if _, _, err := normalizeBytes(t, late, "image/jpeg"); !errors.Is(err, ErrTooLarge) {
			t.Errorf("late APP0: %v, want ErrTooLarge", err)
		}
	})
	// A refusal before decoding allocates nothing like the 22 MB of planes and
	// coefficients, let alone the RGBA copy.
	if got > 8<<20 {
		t.Errorf("the refusal allocated %d bytes, so the file was decoded", got)
	}
}

func TestScanJPEGFrame(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []byte
		want jpegFrame
	}{
		{"progressive 4:2:0", imagetest.ProgressiveJPEG(64, 64, []byte{1, 2, 3}, []byte{0x22, 0x11, 0x11}, -1),
			jpegFrame{progressive: true, n: 3, h: [4]int{2, 1, 1}, v: [4]int{2, 1, 1}}},
		{"gray is always 1 x 1", imagetest.ProgressiveJPEG(64, 64, []byte{1}, []byte{0x22}, -1),
			jpegFrame{progressive: true, n: 1, h: [4]int{1}, v: [4]int{1}}},
		{"CMYK behind an APP14 segment", imagetest.ProgressiveJPEG(64, 64, []byte{1, 2, 3, 4}, []byte{0x11, 0x11, 0x11, 0x11}, 0),
			jpegFrame{progressive: true, n: 4, h: [4]int{1, 1, 1, 1}, v: [4]int{1, 1, 1, 1}}},
	} {
		got, ok := scanJPEGFrame(tc.in)
		// end is the offset of the segment after the SOF, which the fixtures
		// make a DHT.
		if !ok || got.end < 2 || got.end+1 >= len(tc.in) || tc.in[got.end] != 0xFF || tc.in[got.end+1] != 0xC4 {
			t.Errorf("%s: end %d is not where the segment after the SOF starts", tc.name, got.end)
		}
		got.end = 0
		if !ok || got != tc.want {
			t.Errorf("%s: %+v %v, want %+v", tc.name, got, ok, tc.want)
		}
	}
	base := imagetest.JPEGBytes(t, imagetest.BlockImage(2, 3, imagetest.GridColors))
	if f, ok := scanJPEGFrame(base); !ok || f.progressive || f.n != 3 {
		t.Errorf("baseline: %+v %v", f, ok)
	}
	// Extraneous bytes between segments are skipped as image/jpeg skips them.
	spaced := append(append([]byte(nil), base[:2]...), append([]byte{0, 0, 0xFF}, base[2:]...)...)
	if f, ok := scanJPEGFrame(spaced); !ok || f.n != 3 {
		t.Errorf("extraneous bytes before a marker: %+v %v", f, ok)
	}
	for name, in := range map[string][]byte{
		"empty":                 nil,
		"no SOI":                base[2:],
		"ends before a SOF":     {0xFF, 0xD8, 0xFF, 0xD9},
		"scan before a SOF":     {0xFF, 0xD8, 0xFF, 0xDA, 0, 2},
		"truncated segment":     {0xFF, 0xD8, 0xFF, 0xE1, 0xFF, 0xFF},
		"two components":        {0xFF, 0xD8, 0xFF, 0xC0, 0, 12, 8, 0, 8, 0, 8, 2, 1, 0x11, 0, 2, 0x11, 0},
		"component count lies":  {0xFF, 0xD8, 0xFF, 0xC0, 0, 11, 8, 0, 8, 0, 8, 3, 1, 0x11, 0},
		"sampling factor of 0":  {0xFF, 0xD8, 0xFF, 0xC0, 0, 11, 8, 0, 8, 0, 8, 1, 1, 0x01, 0},
		"sampling factor of 5":  {0xFF, 0xD8, 0xFF, 0xC0, 0, 11, 8, 0, 8, 0, 8, 1, 1, 0x51, 0},
		"truncated at the SOF":  {0xFF, 0xD8, 0xFF, 0xC0, 0, 11, 8, 0, 8},
		"only fill bytes after": {0xFF, 0xD8, 0xFF, 0xFF},
	} {
		if f, ok := scanJPEGFrame(in); ok {
			t.Errorf("%s: accepted as %+v", name, f)
		}
	}
}

func TestJPEGFlexLayouts(t *testing.T) {
	frame := func(hv ...int) jpegFrame {
		f := jpegFrame{n: len(hv) / 2}
		for i := range f.n {
			f.h[i], f.v[i] = hv[2*i], hv[2*i+1]
		}
		return f
	}
	for name, tc := range map[string]struct {
		f    jpegFrame
		want bool
	}{
		"4:4:4":                    {frame(1, 1, 1, 1, 1, 1), false},
		"4:2:0":                    {frame(2, 2, 1, 1, 1, 1), false},
		"4:1:1":                    {frame(4, 1, 1, 1, 1, 1), false},
		"4:1:0":                    {frame(4, 2, 1, 1, 1, 1), false},
		"chroma 4 x 4 down":        {frame(4, 4, 1, 1, 1, 1), true},
		"Cb and Cr differ":         {frame(2, 2, 1, 1, 2, 1), true},
		"luma is not the largest":  {frame(1, 1, 2, 2, 2, 2), true},
		"gray":                     {frame(1, 1), false},
		"four components, 4:2:0 K": {frame(2, 2, 1, 1, 1, 1, 2, 2), false},
	} {
		maxH, maxV := 1, 1
		for i := 0; i < tc.f.n; i++ {
			maxH, maxV = max(maxH, tc.f.h[i]), max(maxV, tc.f.v[i])
		}
		if got := tc.f.flex(maxH, maxV); got != tc.want {
			t.Errorf("%s: flex = %v, want %v", name, got, tc.want)
		}
	}
	// Flex pictures keep full-size chroma planes, so they cost more than the
	// same sampling would on its own.
	cfg := image.Config{ColorModel: color.YCbCrModel, Width: 1024, Height: 1024}
	if flex, std := jpegDecodeBytes(frame(1, 1, 2, 2, 2, 2), cfg, false), jpegDecodeBytes(frame(2, 2, 1, 1, 1, 1), cfg, false); flex <= std {
		t.Errorf("flex layout counted %d bytes, a standard one %d", flex, std)
	}
}

// Progressive JPEGs keep 256 bytes of coefficients for every 8 x 8 block of
// every component: a 5000 x 5000 4:4:4 file of 150 KB needs 300 MB of them, and
// a CMYK one more. The files are real, so a decode would succeed if attempted.
func TestNormalizeRefusesProgressiveAndCMYKJPEGByMemoryEstimate(t *testing.T) {
	for name, in := range map[string][]byte{
		"progressive 4:4:4": imagetest.ProgressiveJPEG(5000, 5000, []byte{1, 2, 3}, []byte{0x11, 0x11, 0x11}, -1),
		"progressive 4:2:0": imagetest.ProgressiveJPEG(5000, 5000, []byte{1, 2, 3}, []byte{0x22, 0x11, 0x11}, -1),
		"progressive RGB":   imagetest.ProgressiveJPEG(5000, 5000, []byte{'R', 'G', 'B'}, []byte{0x11, 0x11, 0x11}, -1),
		"progressive CMYK":  imagetest.ProgressiveJPEG(5000, 5000, []byte{1, 2, 3, 4}, []byte{0x11, 0x11, 0x11, 0x11}, 0),
	} {
		if len(in) > MaxUploadBytes {
			t.Fatalf("%s: fixture is %d bytes, over the upload limit", name, len(in))
		}
		if _, _, err := normalizeBytes(t, in, "image/jpeg"); !errors.Is(err, ErrTooLarge) {
			t.Errorf("%s: %v, want ErrTooLarge", name, err)
		}
	}
}

// What a baseline or progressive picture needs, with and without the turned
// copy, against the budget: a 6 megapixel picture in each form fits except a
// progressive one that must also be turned, and at 12 megapixels only a
// baseline one that stays upright does. The pictures are real and small; their
// config is passed in as 3000 x 2000 or 4000 x 3000, so the estimate is under
// test, not a large decode. Accepted means normalizeJPEG went on and decoded the
// small file.
func TestNormalizeJPEGBudgetAtPhoneSize(t *testing.T) {
	six := image.Config{ColorModel: color.YCbCrModel, Width: 3000, Height: 2000}
	twelve := image.Config{ColorModel: color.YCbCrModel, Width: 4000, Height: 3000}
	baseline := imagetest.JPEGBytes(t, imagetest.BlockImage(2, 3, imagetest.GridColors))
	progressive := imagetest.ProgressiveJPEG(64, 64, []byte{1, 2, 3}, []byte{0x22, 0x11, 0x11}, -1)
	turn := func(in []byte) []byte { return imagetest.WithEXIF(in, imagetest.ExifBlock(6)) }
	for name, tc := range map[string]struct {
		in      []byte
		cfg     image.Config
		refused bool
	}{
		"6 MP baseline, upright":                          {baseline, six, false},
		"6 MP baseline, turned":                           {turn(baseline), six, false},
		"6 MP progressive, upright":                       {progressive, six, false},
		"6 MP progressive, turned (its copy too)":         {turn(progressive), six, true},
		"12 MP baseline, upright":                         {baseline, twelve, false},
		"12 MP baseline, turned (its copy is 48 MB)":      {turn(baseline), twelve, true},
		"12 MP progressive, upright (coefficients 72 MB)": {progressive, twelve, true},
	} {
		err := normalizeJPEG(io.Discard, tc.in, tc.cfg)
		if tc.refused != errors.Is(err, ErrTooLarge) || (!tc.refused && err != nil) {
			t.Errorf("%s: %v, refused = %v", name, err, tc.refused)
		}
	}
	// A turned 4:4:4 progressive picture of 25 MP is far over it.
	big := image.Config{ColorModel: color.YCbCrModel, Width: 5000, Height: 5000}
	if err := normalizeJPEG(io.Discard, turn(imagetest.ProgressiveJPEG(64, 64, []byte{1, 2, 3}, []byte{0x11, 0x11, 0x11}, -1)), big); !errors.Is(err, ErrTooLarge) {
		t.Errorf("turned 25 MP: %v, want ErrTooLarge", err)
	}
}

// Four full-screen 5000 x 5000 frames are exactly the old pixel budget, so
// only the memory estimate (two bytes a pixel, 200 MB) refuses them; an invalid
// body would otherwise fail as unreadable.
func TestNormalizeGIFRefusesAggregateFramesByMemoryEstimate(t *testing.T) {
	in := gifHeader(5000, 5000)
	for i := 0; i < 4; i++ {
		in = append(in, gifFrame(5000, 5000, []byte{0x4C, 0x01})...)
	}
	in = append(in, 0x3B)
	if _, _, err := normalizeBytes(t, in, "image/gif"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("100 million pixels of frames: %v, want ErrTooLarge", err)
	}
}

// The re-encoded picture may not pass its allowance, and the allowance is exact:
// a picture whose output is n bytes is accepted at n and refused at n-1, for
// every format, with the upload left as it was and no output file left behind.
func TestNormalizeEnforcesTheOutputAllowance(t *testing.T) {
	cases := map[string][]byte{
		"image/jpeg": imagetest.WithEXIF(imagetest.JPEGBytes(t, imagetest.BlockImage(2, 3, imagetest.GridColors)), imagetest.ExifBlock(1)),
		"image/png":  imagetest.PNGWithText(t, imagetest.BlockImage(2, 3, imagetest.GridColors)),
		"image/gif":  imagetest.AnimatedGIF(t, 5),
	}
	for mime, in := range cases {
		t.Run(mime, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			run := func(limit int64) ([]byte, error) {
				f, err := os.CreateTemp(t.TempDir(), "img-*")
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				if _, err := f.Write(in); err != nil {
					t.Fatal(err)
				}
				_, _, err = normalize(f, mime, limit)
				got, rerr := os.ReadFile(f.Name())
				if rerr != nil {
					t.Fatal(rerr)
				}
				return got, err
			}
			out, err := run(maxOutputBytes)
			if err != nil {
				t.Fatal(err)
			}
			n := int64(len(out))
			if exact, err := run(n); err != nil || int64(len(exact)) != n {
				t.Fatalf("limit %d (the output's size): %d bytes, %v", n, len(exact), err)
			}
			kept, err := run(n - 1)
			if !errors.Is(err, ErrTooLarge) {
				t.Fatalf("limit %d: %v, want ErrTooLarge", n-1, err)
			}
			if !bytes.Equal(kept, in) {
				t.Error("a refused picture changed the upload")
			}
			if left, _ := os.ReadDir(tmp); len(left) != 0 {
				t.Errorf("%d output files left in the temp directory", len(left))
			}
		})
	}
}

// Every encoder passes the writer's refusal back unchanged, so it is ErrTooLarge
// whatever the format, not a server fault.
func TestEncodersReturnTheOutputRefusal(t *testing.T) {
	small := &cappedWriter{w: io.Discard, left: 10}
	img := imagetest.BlockImage(2, 3, imagetest.GridColors)
	if err := jpeg.Encode(small, img, nil); !errors.Is(err, ErrTooLarge) {
		t.Errorf("jpeg: %v", err)
	}
	small.left = 10
	if err := png.Encode(small, img); !errors.Is(err, ErrTooLarge) {
		t.Errorf("png: %v", err)
	}
	small.left = 10
	g, err := gif.DecodeAll(bytes.NewReader(imagetest.AnimatedGIF(t, 2)))
	if err != nil {
		t.Fatal(err)
	}
	if err := gif.EncodeAll(small, g); !errors.Is(err, ErrTooLarge) {
		t.Errorf("gif: %v", err)
	}
	if n, err := (&cappedWriter{w: io.Discard, left: 4}).Write(make([]byte, 5)); n != 0 || !errors.Is(err, ErrTooLarge) {
		t.Errorf("a write over the allowance: %d, %v", n, err)
	}
}
