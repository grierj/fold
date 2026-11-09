// Package images validates and cleans uploaded pictures for avatars and group
// covers (ReadUpload, Normalize). It owns the size limit, the allowed types and
// the wording of their refusals.
package images

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
)

// Uploaded pictures are decoded and encoded again before they are stored, so
// no EXIF block (GPS position, timestamps, device serials), PNG text chunk or
// GIF comment survives. Only the standard library is used.

const (
	// maxImagePixels refuses a canvas over 25 megapixels. A 5 MB file can
	// describe a far larger one, so this is checked from the header before
	// any decoding.
	maxImagePixels = 25_000_000
	// maxImageSide refuses absurd widths and heights (a 1 x 20 000 000 strip
	// stays under the pixel cap but is no avatar).
	maxImageSide = 20_000
	// MaxGIFFrames and maxGIFPixels bound an animation: frames, and the total
	// pixels of all frame rectangles (pixels x frames).
	MaxGIFFrames = 200
	maxGIFPixels = 100_000_000
	// jpegQuality is the quality of re-encoded JPEGs.
	jpegQuality = 85
)

// MaxUploadBytes caps a single image upload (avatars, group covers).
const MaxUploadBytes = 5 << 20 // 5MB

// allowedTypes maps sniffed MIME types to canonical extensions. The
// client-supplied Content-Type is never trusted; uploads are sniffed.
var allowedTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
}

// Ext is the canonical extension for a sniffed MIME type, and whether the type
// is allowed at all.
func Ext(mime string) (string, bool) {
	ext, ok := allowedTypes[mime]
	return ext, ok
}

// These strings are the API's error messages; api_errors.go gives each a code,
// so their text must not change on its own.
var (
	ErrMissing    = errors.New("missing file")
	ErrEmpty      = errors.New("empty file")
	ErrFileSize   = fmt.Errorf("file too large (max %dMB)", MaxUploadBytes>>20)
	ErrNotAllowed = errors.New("only JPEG, PNG, and GIF images are allowed")
	ErrServer     = errors.New("server error")
	ErrTooLarge   = errors.New("that image is too large to process")
	ErrUnreadable = errors.New("cannot read file")
	ErrWebP       = errors.New("WebP images are not supported; use a JPEG or PNG")
)

// Normalize re-encodes the image in f (already sniffed as mime) and
// rewrites f in place with the clean bytes, leaving it positioned at the
// start. It returns the extension and MIME type of what is now stored.
func Normalize(f *os.File, mime string) (string, string, error) {
	ext, ok := allowedTypes[mime]
	if !ok {
		return "", "", ErrNotAllowed
	}
	data, err := io.ReadAll(io.NewSectionReader(f, 0, MaxUploadBytes+1))
	if err != nil {
		return "", "", err
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", "", ErrUnreadable
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxImageSide || cfg.Height > maxImageSide ||
		cfg.Width*cfg.Height > maxImagePixels {
		return "", "", ErrTooLarge
	}

	var out bytes.Buffer
	switch mime {
	case "image/jpeg":
		err = normalizeJPEG(&out, data)
	case "image/png":
		err = normalizePNG(&out, data)
	case "image/gif":
		err = normalizeGIF(&out, data)
	}
	if err != nil {
		return "", "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	if err := f.Truncate(0); err != nil {
		return "", "", err
	}
	if _, err := f.Write(out.Bytes()); err != nil {
		return "", "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	return ext, mime, nil
}

func normalizeJPEG(out *bytes.Buffer, data []byte) error {
	orientation := jpegOrientation(data)
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return ErrUnreadable
	}
	img = applyOrientation(img, orientation)
	return jpeg.Encode(out, img, &jpeg.Options{Quality: jpegQuality})
}

func normalizePNG(out *bytes.Buffer, data []byte) error {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return ErrUnreadable
	}
	return png.Encode(out, img)
}

func normalizeGIF(out *bytes.Buffer, data []byte) error {
	frames, pixels, ok := scanGIF(data)
	if !ok {
		return ErrUnreadable
	}
	if frames > MaxGIFFrames || pixels > maxGIFPixels {
		return ErrTooLarge
	}
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		return ErrUnreadable
	}
	// DecodeAll keeps frames, delays, disposal, loop count and the canvas;
	// EncodeAll writes none of the comment or application extensions.
	return gif.EncodeAll(out, g)
}

// scanGIF walks a GIF's blocks without decoding any pixels and returns the
// number of frames and the total area of their rectangles, so an animation
// can be refused before DecodeAll allocates for it.
//
// It must never read a file differently from image/gif in the permissive
// direction, or a crafted file could hide frames from this count. So it
// follows image/gif's reader block for block and fails closed (ok false) on
// anything it does not fully understand: plain text extensions, unknown
// extension labels, a graphic control block whose size is not 4, an
// application extension whose size is not 11, and frames outside the screen.
func scanGIF(b []byte) (frames, pixels int, ok bool) {
	if len(b) < 13 || (string(b[:6]) != "GIF87a" && string(b[:6]) != "GIF89a") {
		return 0, 0, false
	}
	screenW := int(binary.LittleEndian.Uint16(b[6:]))
	screenH := int(binary.LittleEndian.Uint16(b[8:]))
	pos := 13
	if b[10]&0x80 != 0 {
		pos += 3 << (b[10]&7 + 1)
	}
	// skipSub skips data sub-blocks up to and including the zero terminator.
	skipSub := func() bool {
		for pos < len(b) {
			n := int(b[pos])
			pos += 1 + n
			if n == 0 {
				return pos <= len(b)
			}
		}
		return false
	}
	for pos < len(b) {
		switch b[pos] {
		case 0x3B: // trailer
			return frames, pixels, frames > 0
		case 0x21: // extension
			if pos+2 > len(b) {
				return 0, 0, false
			}
			label := b[pos+1]
			pos += 2
			switch label {
			case 0xF9: // graphic control: size 4, four bytes, terminator
				if pos+6 > len(b) || b[pos] != 4 || b[pos+5] != 0 {
					return 0, 0, false
				}
				pos += 6
			case 0xFE: // comment
				if !skipSub() {
					return 0, 0, false
				}
			case 0xFF: // application: size 11, identifier and code, then sub-blocks
				if pos+12 > len(b) || b[pos] != 11 {
					return 0, 0, false
				}
				pos += 12
				if !skipSub() {
					return 0, 0, false
				}
			default: // plain text (0x01) and anything unknown
				return 0, 0, false
			}
		case 0x2C: // image descriptor
			if pos+10 > len(b) {
				return 0, 0, false
			}
			left := int(binary.LittleEndian.Uint16(b[pos+1:]))
			top := int(binary.LittleEndian.Uint16(b[pos+3:]))
			w := int(binary.LittleEndian.Uint16(b[pos+5:]))
			h := int(binary.LittleEndian.Uint16(b[pos+7:]))
			flags := b[pos+9]
			if left+w > screenW || top+h > screenH {
				return 0, 0, false
			}
			pos += 10
			if flags&0x80 != 0 {
				pos += 3 << (flags&7 + 1)
			}
			pos++ // LZW minimum code size
			if !skipSub() {
				return 0, 0, false
			}
			frames++
			pixels += w * h
			if frames > MaxGIFFrames || pixels > maxGIFPixels {
				return frames, pixels, true
			}
		default:
			return 0, 0, false
		}
	}
	return 0, 0, false
}

// jpegOrientation reads the EXIF orientation (1 to 8) from a JPEG's APP1
// segment. It returns 1 (upright) when there is none or it is malformed.
func jpegOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 1
	}
	pos := 2
	for pos+4 <= len(b) {
		if b[pos] != 0xFF {
			return 1
		}
		marker := b[pos+1]
		if marker == 0xFF { // fill byte
			pos++
			continue
		}
		if marker == 0xDA || marker == 0xD9 { // image data or end: no EXIF after this
			return 1
		}
		if marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) { // no length
			pos += 2
			continue
		}
		n := int(binary.BigEndian.Uint16(b[pos+2:]))
		if n < 2 || pos+2+n > len(b) {
			return 1
		}
		if marker == 0xE1 {
			if o := exifOrientation(b[pos+4 : pos+2+n]); o != 0 {
				return o
			}
		}
		pos += 2 + n
	}
	return 1
}

// exifOrientation parses the payload of an APP1 segment ("Exif\0\0" and a
// TIFF block) and returns tag 0x0112 from the first IFD, or 0 if absent.
func exifOrientation(p []byte) int {
	if len(p) < 14 || string(p[:6]) != "Exif\x00\x00" {
		return 0
	}
	t := p[6:]
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0
	}
	if bo.Uint16(t[2:]) != 42 {
		return 0
	}
	ifd := int(bo.Uint32(t[4:]))
	if ifd < 8 || ifd+2 > len(t) {
		return 0
	}
	count := int(bo.Uint16(t[ifd:]))
	for i := 0; i < count; i++ {
		e := ifd + 2 + i*12
		if e+12 > len(t) {
			return 0
		}
		if bo.Uint16(t[e:]) != 0x0112 {
			continue
		}
		// One value, stored in the value field: a SHORT (3) in its first two
		// bytes, or a LONG (4) across all four.
		if bo.Uint32(t[e+4:]) != 1 {
			return 0
		}
		v := 0
		switch bo.Uint16(t[e+2:]) {
		case 3:
			v = int(bo.Uint16(t[e+8:]))
		case 4:
			if n := bo.Uint32(t[e+8:]); n <= 8 {
				v = int(n)
			}
		}
		if v >= 1 && v <= 8 {
			return v
		}
		return 0
	}
	return 0
}

// applyOrientation turns img upright for an EXIF orientation value. 1 (and
// anything unknown) returns img untouched, with no copy. Otherwise pixels are
// read straight from img into the rotated image.
func applyOrientation(img image.Image, orientation int) image.Image {
	if orientation < 2 || orientation > 8 {
		return img
	}
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dw, dh := sw, sh
	if orientation >= 5 {
		dw, dh = sh, sw
	}
	pixel := func(x, y int) (uint8, uint8, uint8, uint8) {
		x, y = x+b.Min.X, y+b.Min.Y
		if yc, ok := img.(*image.YCbCr); ok { // what a color JPEG decodes to; avoids boxing
			c := yc.YCbCrAt(x, y)
			r, g, bl := color.YCbCrToRGB(c.Y, c.Cb, c.Cr)
			return r, g, bl, 0xFF
		}
		r, g, bl, a := img.At(x, y).RGBA()
		return uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8), uint8(a >> 8)
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for dy := 0; dy < dh; dy++ {
		for dx := 0; dx < dw; dx++ {
			var sx, sy int
			switch orientation {
			case 2: // mirror horizontally
				sx, sy = sw-1-dx, dy
			case 3: // rotate 180
				sx, sy = sw-1-dx, sh-1-dy
			case 4: // mirror vertically
				sx, sy = dx, sh-1-dy
			case 5: // transpose
				sx, sy = dy, dx
			case 6: // rotate 90 clockwise
				sx, sy = dy, sh-1-dx
			case 7: // transverse
				sx, sy = sw-1-dy, sh-1-dx
			case 8: // rotate 90 counterclockwise
				sx, sy = sw-1-dy, dx
			}
			r, g, bl, a := pixel(sx, sy)
			di := dst.PixOffset(dx, dy)
			dst.Pix[di], dst.Pix[di+1], dst.Pix[di+2], dst.Pix[di+3] = r, g, bl, a
		}
	}
	return dst
}
