package images

import (
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"sync"
)

// MaxUploadBytes caps a single image upload (avatars, group covers).
const MaxUploadBytes = 5 << 20 // 5MB

// MaxConcurrentProcessing is how many uploads may be copied, decoded and
// re-encoded at once, in the whole process. Each is bounded by maxWorkingBytes,
// so this times that is the most the pictures may allocate at once.
const MaxConcurrentProcessing = 2

// ProcessAllowanceBytes is the resident memory the picture work may add to the
// process with every slot busy: 2.5 times the slots' budgets. It is not a limit
// the process enforces; it is what the opt-in measurements (docs/agents/
// testing.md) hold the worst accepted pictures to, and they fail above it. The
// factor is the collector's headroom (the heap may reach about twice what is
// live before garbage is freed) and the runtime's slack for freed pages and
// fragmentation. The allowance leaves the rest of a 1 GB machine (docs/
// DEPLOY-DIGITALOCEAN.md) to the application, the database, the proxy and the
// system, which is why the budget is as small as it is.
const ProcessAllowanceBytes = 5 * MaxConcurrentProcessing * maxWorkingBytes / 2

// ErrBusy means every processing slot is taken. It is an answer, not a queue:
// a caller that is refused tells its client to try again.
var ErrBusy = errors.New("image processing is at capacity")

var processing = make(chan struct{}, MaxConcurrentProcessing)

// Admit takes a processing slot without waiting, or returns ErrBusy. The
// release it returns frees the slot once however often it is called. Hold the
// slot from before the upload is read until ReadUpload returns, and no longer:
// storage writes and database transactions do not need it. A cancelled request
// keeps its slot until ReadUpload returns, because the decoders cannot be
// stopped part way.
func Admit() (release func(), err error) {
	select {
	case processing <- struct{}{}:
	default:
		return nil, ErrBusy
	}
	var once sync.Once
	return func() { once.Do(func() { <-processing }) }, nil
}

// ReadUpload validates a multipart image upload: real byte-count size limit and
// content sniffing, then Normalize. It returns a temp file (the caller closes
// and removes it), the canonical extension and the MIME type. A refusal is one
// of the Err values; any other error is a server fault.
func ReadUpload(fh *multipart.FileHeader) (*os.File, string, string, error) {
	src, err := fh.Open()
	if err != nil {
		return nil, "", "", ErrUnreadable
	}
	defer src.Close()

	tmp, err := os.CreateTemp("", "upload-*")
	if err != nil {
		return nil, "", "", err
	}
	fail := func(err error) (*os.File, string, string, error) {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, "", "", err
	}
	// Enforce the real byte count, not the client-declared size.
	n, err := io.Copy(tmp, io.LimitReader(src, MaxUploadBytes+1))
	if err != nil {
		return fail(err)
	}
	if n > MaxUploadBytes {
		return fail(ErrFileSize)
	}
	if n == 0 {
		return fail(ErrEmpty)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fail(err)
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
		return fail(err)
	}
	return tmp, ext, mime, nil
}
