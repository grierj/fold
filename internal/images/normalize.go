// Package images validates and cleans uploaded pictures for avatars and group
// covers (ReadUpload, Normalize). It owns the size limit, the allowed types and
// the wording of their refusals.
package images

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
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
	// maxWorkingBytes is the most one Normalize call may allocate for a picture,
	// worked out from the header before any pixel is decoded: the encoded input
	// (bufferBytes), what the standard decoder allocates for the picture, the
	// copy that turns it upright, and what the standard encoder allocates to
	// write it again (jpegDecodeBytes, jpegAfterBytes, pngBytes, gifBytes). It counts allocation, not what is live at one
	// moment, so it is above what the call holds; the collector still lets the
	// heap grow past what is live, which ProcessAllowanceBytes covers. The pixel
	// caps alone are not enough: a 25 MP 16-bit PNG decodes to 200 MB from a few
	// hundred KB, and a progressive JPEG keeps 256 bytes of coefficients for every
	// 8 x 8 block of every component. Admit allows MaxConcurrentProcessing calls
	// at once. The figure is derived from the decoders and encoders of the pinned
	// toolchain; the opt-in measurements (docs/agents/testing.md) check it
	// against real runs and fail when a call allocates more than it.
	maxWorkingBytes = 64 << 20
	// scratchBytes covers what the estimate does not count per pixel: row
	// buffers, flate state, Huffman tables, the output chunk buffer. A GIF's LZW
	// state is not here: it grows with the frames and is counted by gifBytes.
	scratchBytes = 8 << 20
	// gifFrameBytes is what decoding and encoding one GIF frame allocate beyond
	// its pixels, whatever its size: in image/gif and compress/lzw of the pinned
	// toolchain, the decoder's LZW reader (about 21 KiB of tables and output
	// buffer), the frame's color table, the cloned palette a transparent index
	// makes and its boxed colors (about 10 KiB), and the encoder's LZW writer,
	// a fresh 64 KiB hash table for every frame (about 72 KiB as the runtime
	// rounds a large object). About 104 KiB in all; 128 KiB leaves room for the
	// frame, delay and disposal slices growing.
	gifFrameBytes = 128 << 10
	// maxOutputBytes is the most a re-encoded picture may be. Re-encoding does
	// not keep the compressed size (a PNG can grow, a GIF loses its
	// interlacing), so the output has its own allowance, twice the upload
	// limit, enforced as it is written to disk.
	maxOutputBytes = 2 * MaxUploadBytes
	// outputChunkBytes is the buffer between an encoder and that file.
	outputChunkBytes = 64 << 10
)

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

// The reasons an upload is refused. The package knows nothing of the API:
// the root package maps each to its declared answer (imageRefusal), and any
// other error from ReadUpload is a server fault.
var (
	ErrEmpty      = errors.New("empty upload")
	ErrFileSize   = errors.New("upload over the size limit")
	ErrNotAllowed = errors.New("not a JPEG, PNG or GIF")
	ErrTooLarge   = errors.New("image dimensions over the limit")
	ErrUnreadable = errors.New("image cannot be decoded")
	ErrWebP       = errors.New("WebP is not supported")
)

// Normalize re-encodes the image in f (already sniffed as mime) and
// rewrites f in place with the clean bytes, leaving it positioned at the
// start. It returns the extension and MIME type of what is now stored.
//
// The re-encoded picture is written to a second temporary file through a
// writer that refuses more than maxOutputBytes (ErrTooLarge), so the output
// never sits in memory and cannot grow past its allowance.
func Normalize(f *os.File, mime string) (string, string, error) {
	return normalize(f, mime, maxOutputBytes)
}

// normalize is Normalize with the output allowance as a parameter.
func normalize(f *os.File, mime string, outputLimit int64) (string, string, error) {
	ext, ok := allowedTypes[mime]
	if !ok {
		return "", "", ErrNotAllowed
	}
	data, err := readInput(f)
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

	out, err := os.CreateTemp("", "upload-out-*")
	if err != nil {
		return "", "", err
	}
	defer func() {
		out.Close()
		os.Remove(out.Name())
	}()
	bw := bufio.NewWriterSize(&cappedWriter{w: out, left: outputLimit}, outputChunkBytes)
	switch mime {
	case "image/jpeg":
		err = normalizeJPEG(bw, data, cfg)
	case "image/png":
		err = normalizePNG(bw, data, cfg)
	case "image/gif":
		err = normalizeGIF(bw, data)
	}
	if err == nil {
		err = bw.Flush()
	}
	if err != nil {
		return "", "", err
	}
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	if err := f.Truncate(0); err != nil {
		return "", "", err
	}
	if _, err := io.Copy(f, out); err != nil {
		return "", "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	return ext, mime, nil
}

// readInput reads f (at most MaxUploadBytes+1 bytes) into a buffer of exactly
// its size. io.ReadAll would grow one by appending, which allocates several
// times the file in all and holds the old and the new copy at once.
func readInput(f *os.File) ([]byte, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	data := make([]byte, min(fi.Size(), MaxUploadBytes+1))
	if _, err := io.ReadFull(io.NewSectionReader(f, 0, int64(len(data))), data); err != nil {
		return nil, err
	}
	return data, nil
}

// cappedWriter passes bytes to w until left runs out, then refuses with
// ErrTooLarge. The encoders return a writer's error as it is, so Normalize's
// caller sees ErrTooLarge and nothing past the allowance reaches the disk.
type cappedWriter struct {
	w    io.Writer
	left int64
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > c.left {
		return 0, ErrTooLarge
	}
	n, err := c.w.Write(p)
	c.left -= int64(n)
	return n, err
}

// bufferBytes is what the encoded input holds in memory: the file as read
// (readInput) and the decoders' and encoders' own state (scratchBytes). The
// re-encoded output is not here: it goes to disk through a small bufio.Writer.
func bufferBytes(dataLen int) int64 {
	return int64(dataLen) + scratchBytes
}

// refuseHeavy returns ErrTooLarge when the estimated allocation for the
// picture (pixels, what the standard library allocates to decode, turn and
// encode it) and the encoded buffers exceeds maxWorkingBytes. It runs from the
// header, before any pixel is decoded.
func refuseHeavy(pixels int64, dataLen int) error {
	if pixels+bufferBytes(dataLen) > maxWorkingBytes {
		return ErrTooLarge
	}
	return nil
}

// jpegFrame is the part of a JPEG's SOF segment that decides what the decoder
// allocates: whether it is progressive, and the sampling factors of each
// component (h, v), already as image/jpeg uses them (1 x 1 for a grayscale
// picture).
type jpegFrame struct {
	progressive bool
	n           int
	h, v        [4]int
	// end is the offset just past the SOF segment: what the decoder still reads
	// after it can change how the picture is finished.
	end int
}

// scanJPEGFrame finds the first SOF segment the way image/jpeg walks markers
// (extraneous bytes skipped, fill bytes allowed) and reads it. It reports
// false for anything it does not follow, which refuses the file rather than
// guess at how much memory it needs.
func scanJPEGFrame(b []byte) (jpegFrame, bool) {
	var f jpegFrame
	if len(b) < 2 || b[0] != 0xFF || b[1] != 0xD8 {
		return f, false
	}
	pos := 2
	for {
		if pos+2 > len(b) {
			return f, false
		}
		t0, t1 := b[pos], b[pos+1]
		pos += 2
		for t0 != 0xFF {
			if pos >= len(b) {
				return f, false
			}
			t0, t1 = t1, b[pos]
			pos++
		}
		marker := t1
		if marker == 0 {
			continue
		}
		for marker == 0xFF {
			if pos >= len(b) {
				return f, false
			}
			marker = b[pos]
			pos++
		}
		if marker == 0xD9 || marker == 0xDA { // no SOF before the end or the scan
			return f, false
		}
		if marker >= 0xD0 && marker <= 0xD7 { // restart marker: no length
			continue
		}
		if pos+2 > len(b) {
			return f, false
		}
		n := int(binary.BigEndian.Uint16(b[pos:])) - 2
		pos += 2
		if n < 0 || pos+n > len(b) {
			return f, false
		}
		if marker >= 0xC0 && marker <= 0xC2 { // baseline, extended sequential, progressive
			seg := b[pos : pos+n]
			switch n {
			case 6 + 3*1:
				f.n = 1
			case 6 + 3*3:
				f.n = 3
			case 6 + 3*4:
				f.n = 4
			default:
				return f, false
			}
			if int(seg[5]) != f.n {
				return f, false
			}
			f.progressive = marker == 0xC2
			f.end = pos + n
			for i := 0; i < f.n; i++ {
				h, v := int(seg[7+3*i]>>4), int(seg[7+3*i]&0x0F)
				if h < 1 || h > 4 || v < 1 || v > 4 {
					return f, false
				}
				if f.n == 1 {
					h, v = 1, 1
				}
				f.h[i], f.v[i] = h, v
			}
			return f, true
		}
		pos += n
	}
}

// flex reports whether image/jpeg falls back to full-size chroma planes: a
// 3-component picture whose sampling is not one of its standard ratios
// (4:4:4, 4:4:0, 4:2:2, 4:2:0, 4:1:1, 4:1:0).
func (f jpegFrame) flex(maxH, maxV int) bool {
	if f.n != 3 {
		return false
	}
	if f.h[1] != f.h[2] || f.v[1] != f.v[2] || f.h[0] != maxH || f.v[0] != maxV {
		return true
	}
	return maxV/f.v[1] > 2
}

// converted reports whether image/jpeg may end by building a second full-size
// picture: always for four components (CMYK), and for three when they may be
// RGB. DecodeConfig stops at the first scan, so its color model (RGBA when the
// file says RGB, else YCbCr) is not final: the decoder decides isRGB only when
// it has read every marker, and two segments after the frame header can turn a
// YCbCr picture into an RGB one: an Adobe APP14 (transform 0), and an APP0 that
// is not JFIF, which clears the JFIF flag an earlier APP0 set (DecodeConfig
// returns at the frame header while that flag is set, so it reports YCbCr for
// RGB component ids). Any 0xFF 0xEE or 0xFF 0xE0 after the SOF could be one
// (entropy-coded data never holds either pair), so each counts.
func (f jpegFrame) converted(cfg image.Config, data []byte) bool {
	if f.n == 4 {
		return true
	}
	if f.n != 3 {
		return false
	}
	late := data[f.end:]
	return cfg.ColorModel != color.YCbCrModel || bytes.Contains(late, []byte{0xFF, 0xEE}) || bytes.Contains(late, []byte{0xFF, 0xE0})
}

// jpegDecodeBytes is what image/jpeg allocates to decode a picture of cfg's
// size laid out as f: the sample planes (padded to whole MCUs), for a
// progressive file also one 256-byte coefficient block per 8 x 8 block of every
// component, and for a converted file the RGBA or CMYK copy it builds at the
// end. The planes and coefficients are counted as still held when that copy is
// made, because nothing frees them before the decoder returns.
func jpegDecodeBytes(f jpegFrame, cfg image.Config, converted bool) int64 {
	w, h := int64(cfg.Width), int64(cfg.Height)
	maxH, maxV := 1, 1
	for i := 0; i < f.n; i++ {
		maxH, maxV = max(maxH, f.h[i]), max(maxV, f.v[i])
	}
	mxx := (w + 8*int64(maxH) - 1) / (8 * int64(maxH))
	myy := (h + 8*int64(maxV) - 1) / (8 * int64(maxV))
	var blocks int64
	for i := 0; i < f.n; i++ {
		blocks += mxx * myy * int64(f.h[i]) * int64(f.v[i])
	}
	total := blocks * 64 // one byte a sample, in the planes
	if f.flex(maxH, maxV) {
		total = 3 * (8 * int64(maxH) * mxx) * (8 * int64(maxV) * myy)
	}
	if f.progressive {
		total += blocks * 256 // a [64]int32 a block
	}
	if converted {
		total += 4 * w * h
	}
	return total
}

// jpegAfterBytes is what normalizeJPEG allocates after the decode. A turned
// picture is copied into an upright RGBA one (applyOrientation). A converted
// picture (RGBA or CMYK) is read a pixel at a time through image.Image.At,
// which allocates every color it returns, by that turn or, when upright, by
// the encoder's generic path; a YCbCr or gray one is not (the turn reads
// YCbCrAt, the encoder has a path for each).
func jpegAfterBytes(orientation int, cfg image.Config, converted bool) int64 {
	px := 4 * int64(cfg.Width) * int64(cfg.Height)
	var n int64
	if orientation >= 2 && orientation <= 8 {
		n += px
	}
	if converted {
		n += px
	}
	return n
}

// jpegWorkingBytes is what the whole of normalizeJPEG allocates for a picture,
// beside the encoded buffers.
func jpegWorkingBytes(f jpegFrame, cfg image.Config, data []byte, orientation int) int64 {
	converted := f.converted(cfg, data)
	return jpegDecodeBytes(f, cfg, converted) + jpegAfterBytes(orientation, cfg, converted)
}

func normalizeJPEG(out io.Writer, data []byte, cfg image.Config) error {
	frame, ok := scanJPEGFrame(data)
	if !ok {
		return ErrUnreadable
	}
	orientation := jpegOrientation(data)
	if err := refuseHeavy(jpegWorkingBytes(frame, cfg, data, orientation), len(data)); err != nil {
		return err
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return ErrUnreadable
	}
	img = applyOrientation(img, orientation)
	return jpeg.Encode(out, img, &jpeg.Options{Quality: jpegQuality})
}

// pngPixelBytes is what image/png allocates a pixel for the color model
// DecodeConfig reports. DecodeConfig stops at the header, so it cannot say
// whether a later tRNS chunk turns a gray picture into an NRGBA one;
// transparent says it might (pngMayBeTransparent). An unknown model is counted
// as the largest.
func pngPixelBytes(m color.Model, transparent bool) int64 {
	if _, ok := m.(color.Palette); ok {
		return 1
	}
	switch m {
	case color.GrayModel:
		if transparent {
			return 4 // NRGBA
		}
		return 1
	case color.Gray16Model:
		if transparent {
			return 8 // NRGBA64
		}
		return 2
	case color.RGBAModel, color.NRGBAModel:
		return 4
	}
	return 8
}

// pngMayBeTransparent reports whether a tRNS chunk could precede the pixel
// data. tRNS may only come before the first IDAT, so only the chunks up to it
// are walked; anything that does not end in an IDAT counts as transparent.
func pngMayBeTransparent(b []byte) bool {
	pos := int64(8)
	for pos+8 <= int64(len(b)) {
		switch string(b[pos+4 : pos+8]) {
		case "tRNS":
			return true
		case "IDAT":
			return false
		}
		pos += 8 + int64(binary.BigEndian.Uint32(b[pos:])) + 4
	}
	return true
}

// pngBoxesPixels reports whether png.Encode reads the decoded picture a pixel
// at a time through image.Image.At, which allocates every color it returns:
// every 16-bit picture does (Gray16, RGBA64, NRGBA64, whatever the pixels).
// The 8-bit kinds are written from their pixel arrays. An unknown model counts
// as 16-bit.
func pngBoxesPixels(m color.Model) bool {
	if _, ok := m.(color.Palette); ok {
		return false
	}
	switch m {
	case color.GrayModel, color.RGBAModel, color.NRGBAModel:
		return false
	}
	return true
}

// pngBytes is what normalizing a PNG allocates for the pixels of cfg's size:
// decode, what image/png allocates to read it, and encode, what it allocates to
// write the picture again. An Adam7 picture allocates the whole canvas and then
// an image for each of its seven passes, which together are about one more
// canvas; the passes are garbage only once the collector runs, so both are
// counted. Encoding a 16-bit picture allocates a color per pixel, about one
// more canvas (pngBoxesPixels).
func pngBytes(b []byte, cfg image.Config) (decode, encode int64) {
	canvas := int64(cfg.Width) * int64(cfg.Height) * pngPixelBytes(cfg.ColorModel, pngMayBeTransparent(b))
	decode = canvas
	if len(b) > 28 && b[28] != 0 { // the interlace method byte of IHDR
		decode *= 2
	}
	if pngBoxesPixels(cfg.ColorModel) {
		encode = canvas
	}
	return decode, encode
}

func normalizePNG(out io.Writer, data []byte, cfg image.Config) error {
	decode, encode := pngBytes(data, cfg)
	if err := refuseHeavy(decode+encode, len(data)); err != nil {
		return err
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return ErrUnreadable
	}
	return png.Encode(out, img)
}

// gifBytes is what normalizing a GIF of frames frames and pixels pixels
// allocates (scanGIF's counts): DecodeAll keeps one palette index per pixel of
// every frame, and a frame stored interlaced is read into one array and then
// copied into a second in order (uninterlace), so two bytes a pixel, counted
// for every frame because the flag is per frame; EncodeAll writes the frames it
// was given without copying their pixels. On top of that, every frame costs
// gifFrameBytes of LZW state in the decoder and the encoder, which a many-frame
// animation of small frames would otherwise hide from a pixel count.
func gifBytes(frames, pixels int) int64 {
	return int64(pixels)*2 + int64(frames)*gifFrameBytes
}

func normalizeGIF(out io.Writer, data []byte) error {
	frames, pixels, ok := scanGIF(data)
	if !ok {
		return ErrUnreadable
	}
	if frames > MaxGIFFrames || pixels > maxGIFPixels {
		return ErrTooLarge
	}
	if err := refuseHeavy(gifBytes(frames, pixels), len(data)); err != nil {
		return err
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
