// Package imagetest builds the image fixtures the upload tests share: pictures
// with GPS-carrying EXIF blocks, PNG text chunks and animated GIFs, so a test
// can prove none of it is stored. Test-only; production code must not import it.
package imagetest

import (
	"bytes"
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
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 6 // 8 bit RGBA
	return append([]byte("\x89PNG\r\n\x1a\n"), PNGChunk("IHDR", ihdr)...)
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
