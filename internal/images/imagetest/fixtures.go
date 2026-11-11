// Package imagetest builds the image fixtures the upload tests share: pictures
// with GPS-carrying EXIF blocks, PNG text chunks and animated GIFs, so a test
// can prove none of it is stored. Test-only; production code must not import it.
package imagetest

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

// Block colors for the orientation fixtures, row by row of a 2 x 3 grid.
var (
	CRed     = color.RGBA{R: 230, A: 255}
	CGreen   = color.RGBA{G: 200, A: 255}
	CBlue    = color.RGBA{B: 230, A: 255}
	CYellow  = color.RGBA{R: 230, G: 230, A: 255}
	CMagenta = color.RGBA{R: 230, B: 230, A: 255}
	CCyan    = color.RGBA{G: 230, B: 230, A: 255}

	GridColors = []color.RGBA{CRed, CGreen, CBlue, CYellow, CMagenta, CCyan}
)

const BlockPx = 32 // a block is big enough to survive JPEG chroma subsampling

// Byte patterns that must never reach storage.
var (
	GPSMarker   = []byte("GPSLEAK-HOME")
	GPSLatitude = []byte{37, 0, 0, 0, 1, 0, 0, 0, 46, 0, 0, 0, 1, 0, 0, 0} // 37/1, 46/1
)

// BlockImage draws a cols x rows grid of solid blocks, left to right, top to bottom.
func BlockImage(cols, rows int, colors []color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, cols*BlockPx, rows*BlockPx))
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			draw.Draw(img, image.Rect(c*BlockPx, r*BlockPx, (c+1)*BlockPx, (r+1)*BlockPx),
				&image.Uniform{colors[r*cols+c]}, image.Point{}, draw.Src)
		}
	}
	return img
}

func JPEGBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// ExifBlock builds an Exif APP1 payload (little endian) with an orientation,
// a maker string carrying GPSMarker and a GPS IFD with a latitude.
func ExifBlock(orientation uint16) []byte {
	le := binary.LittleEndian
	maker := append(append([]byte(nil), GPSMarker...), 0)
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
	b.Write(GPSLatitude)
	return append([]byte("Exif\x00\x00"), b.Bytes()...)
}

// WithEXIF inserts an APP1 segment with the payload right after the SOI marker.
func WithEXIF(jpg, payload []byte) []byte {
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	out := append([]byte(nil), jpg[:2]...)
	out = append(out, seg...)
	out = append(out, payload...)
	return append(out, jpg[2:]...)
}

func PNGChunk(typ string, data []byte) []byte {
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

// PNGWithText inserts a tEXt chunk right after the 8-byte signature and IHDR (33 bytes).
func PNGWithText(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	p := b.Bytes()
	chunk := PNGChunk("tEXt", append([]byte("Comment\x00"), GPSMarker...))
	out := append([]byte(nil), p[:33]...)
	out = append(out, chunk...)
	return append(out, p[33:]...)
}

// PNGHeaderOnly is a PNG signature and IHDR claiming w x h and nothing else, so
// any attempt to decode the pixels would fail differently from the size refusal.
func PNGHeaderOnly(w, h uint32) []byte {
	return PNGHeader(w, h, 8, 6, 0) // 8 bit RGBA
}

// PNGHeader is a signature and an IHDR with the given bit depth, color type
// (0 gray, 2 RGB, 3 palette, 4 gray and alpha, 6 RGBA) and interlace method
// (0 none, 1 Adam7), and nothing else.
func PNGHeader(w, h uint32, depth, colorType, interlace byte) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9], ihdr[12] = depth, colorType, interlace
	return append([]byte("\x89PNG\r\n\x1a\n"), PNGChunk("IHDR", ihdr)...)
}

// PNGWithChunkBeforeIDAT inserts chunk (from PNGChunk) in front of the first
// IDAT of an encoded PNG, where tRNS belongs.
func PNGWithChunkBeforeIDAT(t *testing.T, p, chunk []byte) []byte {
	t.Helper()
	for pos := 8; pos+8 <= len(p); {
		if string(p[pos+4:pos+8]) == "IDAT" {
			out := append([]byte(nil), p[:pos]...)
			out = append(out, chunk...)
			return append(out, p[pos:]...)
		}
		pos += 12 + int(binary.BigEndian.Uint32(p[pos:]))
	}
	t.Fatal("no IDAT in the PNG")
	return nil
}

// GrayTransparentPNG is a real w x h 16-bit grayscale PNG with a tRNS chunk,
// which image/png decodes to a 16-bit NRGBA picture, four times what its
// header (and DecodeConfig) suggests.
func GrayTransparentPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray16(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return PNGWithChunkBeforeIDAT(t, b.Bytes(), PNGChunk("tRNS", []byte{0x12, 0x34}))
}

// Adam7RGBPNG is a real w x h 8-bit RGB PNG with Adam7 interlacing, all black.
// The standard library cannot write one, so the seven passes are laid out here.
func Adam7RGBPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	passes := [7][4]int{{0, 0, 8, 8}, {4, 0, 8, 8}, {0, 4, 4, 8}, {2, 0, 4, 4}, {0, 2, 2, 4}, {1, 0, 2, 2}, {0, 1, 1, 2}}
	var raw bytes.Buffer
	for _, p := range passes {
		pw, ph := (w-p[0]+p[2]-1)/p[2], (h-p[1]+p[3]-1)/p[3]
		if pw <= 0 || ph <= 0 {
			continue
		}
		raw.Write(make([]byte, ph*(1+3*pw))) // each row: filter byte 0, then black
	}
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	if _, err := zw.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	out := PNGHeader(uint32(w), uint32(h), 8, 2, 1)
	out = append(out, PNGChunk("IDAT", z.Bytes())...)
	return append(out, PNGChunk("IEND", nil)...)
}

// ProgressiveJPEG is a real progressive JPEG of w x h: one DC scan of every
// component, all zero (a mid-gray picture), small on the wire however large
// the canvas. ids are the component identifiers and hv their sampling factors
// (high nibble horizontal, low vertical), one entry per component; adobe is
// the APP14 color transform byte, or -1 for no Adobe segment (four components
// need one). image/jpeg keeps every block's coefficients while it decodes one.
func ProgressiveJPEG(w, h int, ids, hv []byte, adobe int) []byte {
	n := len(ids)
	var b bytes.Buffer
	seg := func(marker byte, payload []byte) {
		b.Write([]byte{0xFF, marker, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)})
		b.Write(payload)
	}
	b.Write([]byte{0xFF, 0xD8})
	if adobe >= 0 {
		seg(0xEE, append([]byte("Adobe\x00\x64\x00\x00\x00\x00"), byte(adobe)))
	}
	seg(0xDB, append([]byte{0}, bytes.Repeat([]byte{1}, 64)...))
	sof := []byte{8, byte(h >> 8), byte(h), byte(w >> 8), byte(w), byte(n)}
	maxH, maxV, perMCU := 1, 1, 0
	for i := range ids {
		sof = append(sof, ids[i], hv[i], 0)
		if n > 1 {
			maxH, maxV = max(maxH, int(hv[i]>>4)), max(maxV, int(hv[i]&15))
			perMCU += int(hv[i]>>4) * int(hv[i]&15)
		}
	}
	if n == 1 {
		perMCU = 1
	}
	seg(0xC2, sof)
	// A DC table with one one-bit code, 0, for a difference of zero.
	dht := append([]byte{0, 1}, make([]byte, 15)...)
	seg(0xC4, append(dht, 0))
	sos := []byte{byte(n)}
	for _, id := range ids {
		sos = append(sos, id, 0)
	}
	seg(0xDA, append(sos, 0, 0, 0))
	mxx, myy := (w+8*maxH-1)/(8*maxH), (h+8*maxV-1)/(8*maxV)
	b.Write(make([]byte, (mxx*myy*perMCU+7)/8+8)) // a 0 bit per block, then padding
	b.Write([]byte{0xFF, 0xD9})
	return b.Bytes()
}

func AnimatedGIF(t *testing.T, frames int) []byte {
	t.Helper()
	g := &gif.GIF{LoopCount: 5}
	for i := 0; i < frames; i++ {
		p := image.NewPaletted(image.Rect(0, 0, 8, 8), palette.Plan9)
		draw.Draw(p, p.Bounds(), &image.Uniform{GridColors[i%len(GridColors)]}, image.Point{}, draw.Src)
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

func Near(a, b color.Color) bool {
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

// AssertGrid checks the center of every block of img against want (row by row).
func AssertGrid(t *testing.T, img image.Image, cols, rows int, want []color.RGBA) {
	t.Helper()
	b := img.Bounds()
	if b.Dx() != cols*BlockPx || b.Dy() != rows*BlockPx {
		t.Fatalf("size %dx%d, want %dx%d", b.Dx(), b.Dy(), cols*BlockPx, rows*BlockPx)
	}
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			got := img.At(c*BlockPx+BlockPx/2, r*BlockPx+BlockPx/2)
			if !Near(got, want[r*cols+c]) {
				t.Errorf("block (col %d, row %d) = %v, want %v", c, r, got, want[r*cols+c])
			}
		}
	}
}

// WithLateAdobe inserts an Adobe APP14 segment with the given color transform
// just before the end-of-image marker, after every scan. A decoder that reads
// that far finishes the picture by it (transform 0 means RGB), though
// DecodeConfig stops at the first scan and does not see it.
func WithLateAdobe(jpg []byte, transform byte) []byte {
	payload := append([]byte("Adobe\x00\x64\x00\x00\x00\x00"), transform)
	seg := []byte{0xFF, 0xEE, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}
	end := len(jpg) - 2 // the EOI marker the fixture ends with
	out := append([]byte(nil), jpg[:end]...)
	out = append(out, seg...)
	out = append(out, payload...)
	return append(out, jpg[end:]...)
}

// SixteenBitPNG is a real w x h PNG of 16 bits a channel: RGB (color type 2)
// when opaque, RGBA (type 6) when not. image/png decodes it to a 16-bit RGBA or
// NRGBA picture and reads it back a pixel at a time to write it again.
func SixteenBitPNG(t *testing.T, w, h int, opaque bool) []byte {
	t.Helper()
	img := image.NewRGBA64(image.Rect(0, 0, w, h))
	for i := 0; i+8 <= len(img.Pix); i += 8 {
		if opaque {
			img.Pix[i], img.Pix[i+2], img.Pix[i+4] = byte(i>>3), byte(i>>11), byte(i>>19)
			img.Pix[i+6], img.Pix[i+7] = 0xFF, 0xFF
		} else {
			img.Pix[i+6] = 0x80 // half transparent black: premultiplied, so no color
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Gray16PNG is a real w x h 16-bit grayscale PNG whose every sample is 256 or
// more, so reading it back through image.Image.At allocates a color for each
// pixel (the runtime shares the boxes of the smallest values).
func Gray16PNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewGray16(image.Rect(0, 0, w, h))
	for i := 0; i+2 <= len(img.Pix); i += 2 {
		img.Pix[i], img.Pix[i+1] = 1, byte(i>>1)
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
