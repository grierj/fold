package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/draw"
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
	// maxGIFFrames and maxGIFPixels bound an animation: frames, and the total
	// pixels of all frame rectangles (pixels x frames).
	maxGIFFrames = 200
	maxGIFPixels = 100_000_000
	// jpegQuality is the quality of re-encoded JPEGs.
	jpegQuality = 85
)

// These strings are the API's error messages; api_errors.go gives each a code.
var (
	errImageTooLarge   = errors.New("that image is too large to process")
	errImageUnreadable = errors.New("cannot read file")
	errImageWebP       = errors.New("WebP images are not supported; use a JPEG or PNG")
)

// normalizeImage re-encodes the image in f (already sniffed as mime) and
// rewrites f in place with the clean bytes, leaving it positioned at the
// start. It returns the extension and MIME type of what is now stored.
func normalizeImage(f *os.File, mime string) (string, string, error) {
	data, err := io.ReadAll(io.NewSectionReader(f, 0, maxUploadBytes+1))
	if err != nil {
		return "", "", err
	}
	if _, ok := allowedImageTypes[mime]; !ok {
		return "", "", errImageUnreadable
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", "", errImageUnreadable
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxImageSide || cfg.Height > maxImageSide ||
		cfg.Width*cfg.Height > maxImagePixels {
		return "", "", errImageTooLarge
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
	return allowedImageTypes[mime], mime, nil
}

func normalizeJPEG(out *bytes.Buffer, data []byte) error {
	orientation := jpegOrientation(data)
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return errImageUnreadable
	}
	img = applyOrientation(img, orientation)
	return jpeg.Encode(out, img, &jpeg.Options{Quality: jpegQuality})
}

func normalizePNG(out *bytes.Buffer, data []byte) error {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return errImageUnreadable
	}
	return png.Encode(out, img)
}

func normalizeGIF(out *bytes.Buffer, data []byte) error {
	frames, pixels, ok := scanGIF(data)
	if !ok {
		return errImageUnreadable
	}
	if frames > maxGIFFrames || pixels > maxGIFPixels {
		return errImageTooLarge
	}
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		return errImageUnreadable
	}
	// DecodeAll keeps frames, delays, disposal, loop count and the canvas;
	// EncodeAll writes none of the comment or application extensions.
	return gif.EncodeAll(out, g)
}

// scanGIF walks a GIF's blocks without decoding any pixels and returns the
// number of frames and the total area of their rectangles, so an animation
// can be refused before DecodeAll allocates for it.
func scanGIF(b []byte) (frames, pixels int, ok bool) {
	if len(b) < 13 {
		return 0, 0, false
	}
	pos := 13
	if b[10]&0x80 != 0 {
		pos += 3 << (b[10]&7 + 1)
	}
	skipSub := func() bool { // sub-blocks up to the zero terminator
		for pos < len(b) {
			n := int(b[pos])
			pos += 1 + n
			if n == 0 {
				return true
			}
		}
		return false
	}
	for pos < len(b) {
		switch b[pos] {
		case 0x3B: // trailer
			return frames, pixels, frames > 0
		case 0x21: // extension: label, then sub-blocks
			pos += 2
			if !skipSub() {
				return 0, 0, false
			}
		case 0x2C: // image descriptor
			if pos+10 > len(b) {
				return 0, 0, false
			}
			w := int(binary.LittleEndian.Uint16(b[pos+5:]))
			h := int(binary.LittleEndian.Uint16(b[pos+7:]))
			flags := b[pos+9]
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
			if frames > maxGIFFrames || pixels > maxGIFPixels {
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
		// type 3 (SHORT), one value, stored in the first two bytes of the value field
		if bo.Uint16(t[e+2:]) != 3 || bo.Uint32(t[e+4:]) != 1 {
			return 0
		}
		if v := int(bo.Uint16(t[e+8:])); v >= 1 && v <= 8 {
			return v
		}
		return 0
	}
	return 0
}

// applyOrientation turns img upright for an EXIF orientation value. 1 (and
// anything unknown) returns img untouched.
func applyOrientation(img image.Image, orientation int) image.Image {
	if orientation < 2 || orientation > 8 {
		return img
	}
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	src := image.NewRGBA(image.Rect(0, 0, sw, sh))
	draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)
	dw, dh := sw, sh
	if orientation >= 5 {
		dw, dh = sh, sw
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
			si := src.PixOffset(sx, sy)
			di := dst.PixOffset(dx, dy)
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
	return dst
}
