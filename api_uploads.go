package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"fold/internal/images"
	"fold/internal/policy"
	"fold/storage"

	"github.com/gin-gonic/gin"
)

func clampFocal(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// The API's answers for an image it will not take. imageRefusal is the one place
// images' plain refusals become these (internal/images knows nothing of the API).
var (
	errImageTooLarge   = apiErr(http.StatusBadRequest, "image_too_large", "that image is too large to process")
	errImageUnreadable = apiErr(http.StatusBadRequest, "file_unreadable", "cannot read file")
	errImageWebP       = apiErr(http.StatusBadRequest, "image_unsupported", "WebP images are not supported; use a JPEG or PNG")
)

// imageRefusal maps an images.ReadUpload error to its declared answer; anything
// it does not name is a server fault (errUploadFailed).
func imageRefusal(err error) error {
	switch {
	case errors.Is(err, images.ErrFileSize):
		return errFileTooLarge
	case errors.Is(err, images.ErrEmpty):
		return errEmptyFile
	case errors.Is(err, images.ErrNotAllowed):
		return errFileType
	case errors.Is(err, images.ErrWebP):
		return errImageWebP
	case errors.Is(err, images.ErrTooLarge):
		return errImageTooLarge
	case errors.Is(err, images.ErrUnreadable):
		return errImageUnreadable
	}
	return errUploadFailed
}

func readImageUpload(c *gin.Context, field string) (*os.File, string, string, error) {
	fh, err := c.FormFile(field)
	if err != nil {
		return nil, "", "", errMissingFile
	}
	tmp, ext, mime, err := images.ReadUpload(fh)
	if err != nil {
		return nil, "", "", imageRefusal(err)
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
	c.JSON(http.StatusOK, avatarUploadResponseJSON{AvatarURL: avatarURL})
}

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

// uploadsURLPrefix is where the local blob store is served (serveUpload); the
// store builds URLs under it and uploadKeyFromURL reads them back.
const uploadsURLPrefix = "/uploads"

// uploadKeyFromURL returns the storage key for an /uploads/... URL, or
// false when the value isn't a locally stored upload (a group without
// a cover has an empty URL; covers are only ever set by upload).
func uploadKeyFromURL(raw string) (string, bool) {
	key, found := strings.CutPrefix(raw, uploadsURLPrefix+"/")
	if !found {
		return "", false
	}
	if err := storage.ValidateKey(key); err != nil {
		return "", false
	}
	return key, true
}

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
	c.JSON(http.StatusOK, coverUploadResponseJSON{ImageURL: g.ImageURL})
}

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
