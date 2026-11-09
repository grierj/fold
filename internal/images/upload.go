package images

import (
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
)

// ReadUpload validates a multipart image upload: real byte-count size limit and
// content sniffing, then Normalize. It returns a temp file (the caller closes
// and removes it), the canonical extension and the MIME type. A refusal's
// message is what the API shows, so the callers send err.Error() as is.
func ReadUpload(fh *multipart.FileHeader) (*os.File, string, string, error) {
	src, err := fh.Open()
	if err != nil {
		return nil, "", "", ErrUnreadable
	}
	defer src.Close()

	tmp, err := os.CreateTemp("", "upload-*")
	if err != nil {
		return nil, "", "", ErrServer
	}
	fail := func(err error) (*os.File, string, string, error) {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, "", "", err
	}
	// Enforce the real byte count, not the client-declared size.
	n, err := io.Copy(tmp, io.LimitReader(src, MaxUploadBytes+1))
	if err != nil {
		return fail(ErrServer)
	}
	if n > MaxUploadBytes {
		return fail(ErrFileSize)
	}
	if n == 0 {
		return fail(ErrEmpty)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fail(ErrServer)
	}
	head := make([]byte, 512)
	rn, _ := io.ReadFull(tmp, head)
	mime := http.DetectContentType(head[:rn])
	if mime == "image/webp" {
		// The standard library can neither read nor write WebP, so its metadata
		// could not be stripped. Refuse it rather than store it as it came.
		return fail(ErrWebP)
	}
	// Re-encode so no metadata (EXIF, text chunks, comments) is stored; this
	// is also where a type that is not JPEG, PNG or GIF is refused.
	ext, mime, err := Normalize(tmp, mime)
	if err != nil {
		if errors.Is(err, ErrNotAllowed) || errors.Is(err, ErrTooLarge) || errors.Is(err, ErrUnreadable) {
			return fail(err)
		}
		return fail(ErrServer)
	}
	return tmp, ext, mime, nil
}
