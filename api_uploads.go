package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"fold/internal/policy"
	"fold/storage"

	"github.com/gin-gonic/gin"
)

// maxUploadBytes caps a single image upload (avatars, group covers).
const maxUploadBytes = 5 << 20 // 5MB

// allowedImageTypes maps sniffed MIME types to canonical extensions. The
// client-supplied Content-Type is never trusted; uploads are sniffed.
var allowedImageTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
}

// apiUpdateMe updates the current user's profile. Fields are pointers so

// clampFocal keeps a focal-point percentage in the 0-100 range.
func clampFocal(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// readImageUpload validates a multipart image upload: real byte-count size
// limit and content sniffing. Returns a temp file (caller closes/removes
// it), the canonical extension, and the MIME type.
func readImageUpload(c *gin.Context, field string) (*os.File, string, string, error) {
	refuse := func(err error) (*os.File, string, string, error) {
		return nil, "", "", err
	}
	fh, err := c.FormFile(field)
	if err != nil {
		return refuse(errMissingFile)
	}
	src, err := fh.Open()
	if err != nil {
		return refuse(errImageUnreadable)
	}
	defer src.Close()

	tmp, err := os.CreateTemp("", "upload-*")
	if err != nil {
		return refuse(errUploadFailed)
	}
	cleanup := func() { tmp.Close(); os.Remove(tmp.Name()) }
	// Enforce the real byte count, not the client-declared size.
	n, err := io.Copy(tmp, io.LimitReader(src, maxUploadBytes+1))
	if err != nil {
		cleanup()
		return refuse(errUploadFailed)
	}
	if n > maxUploadBytes {
		cleanup()
		return refuse(errFileTooLarge)
	}
	if n == 0 {
		cleanup()
		return refuse(errEmptyFile)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return refuse(errUploadFailed)
	}
	head := make([]byte, 512)
	rn, _ := io.ReadFull(tmp, head)
	mime := http.DetectContentType(head[:rn])
	if mime == "image/webp" {
		// The standard library can neither read nor write WebP, so its metadata
		// could not be stripped. Refuse it rather than store it as it came.
		cleanup()
		return nil, "", "", errImageWebP
	}
	if _, ok := allowedImageTypes[mime]; !ok {
		cleanup()
		return refuse(errFileType)
	}
	// Re-encode so no metadata (EXIF, text chunks, comments) is stored.
	ext, mime, err := normalizeImage(tmp, mime)
	if err != nil {
		cleanup()
		if errors.Is(err, errImageTooLarge) || errors.Is(err, errImageUnreadable) {
			return nil, "", "", err
		}
		return refuse(errUploadFailed)
	}
	return tmp, ext, mime, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// apiUploadAvatar replaces the current user's avatar. The file is stored and
// the key switched under the user's row lock; old files are removed later by
// the avatar.cleanup job (avatar_cleanup.go), never on this path.
func (a *App) apiUploadAvatar(c *gin.Context) {
	start := requestStart(c)
	u := apiUser(c)
	if !policy.CanUploadAvatar(asPerson(u)) {
		fail(c, errTeenAvatar)
		return
	}
	tmp, _, mime, err := readImageUpload(c, "avatar")
	if err != nil {
		fail(c, err)
		return
	}
	defer tmp.Close()
	defer os.Remove(tmp.Name())

	st, err := tmp.Stat()
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "server error")
		return
	}
	avatarURL, err := a.storeAvatar(c.Request.Context(), u, tmp, st.Size(), mime, avatarTxDeadline(start, time.Now()))
	switch {
	case errors.Is(err, errSecuritySession):
		fail(c, errSecuritySession)
		return
	case errors.Is(err, errTeenAvatar):
		fail(c, errTeenAvatar)
		return
	case err != nil:
		jsonError(c, http.StatusInternalServerError, "could not store avatar")
		return
	}
	c.JSON(http.StatusOK, gin.H{"avatarUrl": avatarURL})
}

// apiDeleteAvatar removes the current user's avatar.
func (a *App) apiDeleteAvatar(c *gin.Context) {
	start := requestStart(c)
	u := apiUser(c)
	err := a.removeAvatar(c.Request.Context(), u, avatarTxDeadline(start, start))
	switch {
	case errors.Is(err, errSecuritySession):
		fail(c, errSecuritySession)
		return
	case err != nil:
		jsonError(c, http.StatusInternalServerError, "could not remove avatar")
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

// uploadKeyFromURL returns the storage key for an /uploads/... URL, or
// false when the value isn't a locally stored upload (a group without
// a cover has an empty URL; covers are only ever set by upload).
func uploadKeyFromURL(raw string) (string, bool) {
	const prefix = "/uploads/"
	if !strings.HasPrefix(raw, prefix) {
		return "", false
	}
	key := strings.TrimPrefix(raw, prefix)
	if err := storage.ValidateKey(key); err != nil {
		return "", false
	}
	return key, true
}

// apiUploadGroupCover replaces a group's cover image. Leader or admin only.
func (a *App) apiUploadGroupCover(c *gin.Context) {
	g, ok := a.loadManagedGroup(c)
	if !ok {
		return
	}
	tmp, ext, mime, err := readImageUpload(c, "cover")
	if err != nil {
		fail(c, err)
		return
	}
	defer tmp.Close()
	defer os.Remove(tmp.Name())

	rid, err := randomHex(16)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "server error")
		return
	}
	key := storage.NewKey(storage.KindCovers, strconv.Itoa(int(g.ID)), rid, strings.TrimPrefix(ext, "."))
	st, err := tmp.Stat()
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "server error")
		return
	}
	ctx := c.Request.Context()
	if err := a.Uploads.Put(ctx, key, tmp, st.Size(), mime); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not store cover")
		return
	}
	oldURL := g.ImageURL
	g.ImageURL = a.Uploads.URL(key)
	if err := a.DB.Model(g).Update("image_url", g.ImageURL).Error; err != nil {
		_ = a.Uploads.Delete(ctx, key)
		jsonError(c, http.StatusInternalServerError, "could not save cover")
		return
	}
	if oldKey, ok := uploadKeyFromURL(oldURL); ok {
		_ = a.Uploads.Delete(ctx, oldKey)
	}
	c.JSON(http.StatusOK, gin.H{"imageUrl": g.ImageURL})
}

// apiDeleteGroupCover removes a group's uploaded cover image.
func (a *App) apiDeleteGroupCover(c *gin.Context) {
	g, ok := a.loadManagedGroup(c)
	if !ok {
		return
	}
	oldURL := g.ImageURL
	if err := a.DB.Model(g).Update("image_url", "").Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not remove cover")
		return
	}
	if oldKey, ok := uploadKeyFromURL(oldURL); ok {
		_ = a.Uploads.Delete(c.Request.Context(), oldKey)
	}
	c.JSON(http.StatusOK, gin.H{})
}

// serveUpload serves a stored upload. Keys are validated so nothing outside
// the upload directory is reachable. A future S3 implementation would
// redirect to a presigned URL here instead of serving the file.
func (a *App) serveUpload(c *gin.Context) {
	key := strings.TrimPrefix(c.Param("filepath"), "/")
	if err := storage.ValidateKey(key); err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	// Open returns only a regular file that is the exact entry named, never
	// a symlink's target. A wrapper that embeds *storage.LocalStore promotes
	// it; a store without Open (S3) redirects.
	opener, ok := a.Uploads.(interface {
		Open(key string) (*os.File, error)
	})
	if !ok {
		c.Redirect(http.StatusFound, a.Uploads.URL(key))
		return
	}
	f, err := opener.Open(key)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "public, max-age=86400")
	http.ServeContent(c.Writer, c.Request, st.Name(), st.ModTime(), f)
}
