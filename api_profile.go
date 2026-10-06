package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"os"
	"strconv"
	"strings"

	"church-groups/models"
	"church-groups/storage"

	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// Profile endpoints (Phase 2)
// ---------------------------------------------------------------------------

// maxUploadBytes caps a single image upload (avatars, group covers).
const maxUploadBytes = 5 << 20 // 5MB

// allowedImageTypes maps sniffed MIME types to canonical extensions. The
// client-supplied Content-Type is never trusted; uploads are sniffed.
var allowedImageTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// apiUpdateMe updates the current user's profile. Fields are pointers so
// "not provided" is distinct from "set to empty".
func (a *App) apiUpdateMe(c *gin.Context) {
	u := apiUser(c)
	var body struct {
		Name        *string `json:"name"`
		DisplayName *string `json:"displayName"`
		Email       *string `json:"email"`
		Phone       *string `json:"phone"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	updates := map[string]interface{}{}
	if body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		if len(name) > 100 {
			jsonError(c, http.StatusBadRequest, "name must be at most 100 characters")
			return
		}
		updates["name"] = name
	}
	if body.DisplayName != nil {
		dn := strings.TrimSpace(*body.DisplayName)
		if len(dn) > 100 {
			jsonError(c, http.StatusBadRequest, "display name must be at most 100 characters")
			return
		}
		updates["display_name"] = dn
	}
	if body.Email != nil {
		email := strings.TrimSpace(*body.Email)
		if email != "" {
			if len(email) > 254 {
				jsonError(c, http.StatusBadRequest, "email must be at most 254 characters")
				return
			}
			if _, err := mail.ParseAddress(email); err != nil {
				jsonError(c, http.StatusBadRequest, "invalid email address")
				return
			}
		}
		updates["email"] = email
	}
	if body.Phone != nil {
		phone := strings.TrimSpace(*body.Phone)
		if len(phone) > 30 {
			jsonError(c, http.StatusBadRequest, "phone must be at most 30 characters")
			return
		}
		updates["phone"] = phone
	}
	if len(updates) == 0 {
		c.JSON(http.StatusOK, gin.H{"user": a.toUserJSON(u)})
		return
	}
	if err := a.DB.Model(u).Updates(updates).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save profile")
		return
	}
	// Reload so the response reflects the saved state.
	var updated models.User
	if err := a.DB.First(&updated, u.ID).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not reload profile")
		return
	}
	// Refresh the request-scoped user so later handlers see the update.
	c.Set("apiUser", &updated)
	c.JSON(http.StatusOK, gin.H{"user": a.toUserJSON(&updated)})
}

// readImageUpload validates a multipart image upload: real byte-count size
// limit and content sniffing. Returns a temp file (caller closes/removes
// it), the canonical extension, and the MIME type.
func readImageUpload(c *gin.Context, field string) (*os.File, string, string, error) {
	fail := func(format string, args ...interface{}) (*os.File, string, string, error) {
		return nil, "", "", fmt.Errorf(format, args...)
	}
	fh, err := c.FormFile(field)
	if err != nil {
		return fail("missing file")
	}
	src, err := fh.Open()
	if err != nil {
		return fail("cannot read file")
	}
	defer src.Close()

	tmp, err := os.CreateTemp("", "upload-*")
	if err != nil {
		return fail("server error")
	}
	cleanup := func() { tmp.Close(); os.Remove(tmp.Name()) }
	// Enforce the real byte count, not the client-declared size.
	n, err := io.Copy(tmp, io.LimitReader(src, maxUploadBytes+1))
	if err != nil {
		cleanup()
		return fail("server error")
	}
	if n > maxUploadBytes {
		cleanup()
		return fail("file too large (max 5MB)")
	}
	if n == 0 {
		cleanup()
		return fail("empty file")
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return fail("server error")
	}
	head := make([]byte, 512)
	rn, _ := io.ReadFull(tmp, head)
	mime := http.DetectContentType(head[:rn])
	ext, ok := allowedImageTypes[mime]
	if !ok {
		cleanup()
		return fail("only JPEG, PNG, GIF, and WebP images are allowed")
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return fail("server error")
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

// apiUploadAvatar replaces the current user's avatar.
func (a *App) apiUploadAvatar(c *gin.Context) {
	u := apiUser(c)
	tmp, ext, mime, err := readImageUpload(c, "avatar")
	if err != nil {
		jsonError(c, http.StatusBadRequest, err.Error())
		return
	}
	defer tmp.Close()
	defer os.Remove(tmp.Name())

	id, err := randomHex(16)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "server error")
		return
	}
	key := storage.NewKey("avatars", strconv.FormatUint(uint64(u.ID), 10), id, strings.TrimPrefix(ext, "."))
	st, err := tmp.Stat()
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "server error")
		return
	}
	ctx := c.Request.Context()
	if err := a.Uploads.Put(ctx, key, tmp, st.Size(), mime); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not store avatar")
		return
	}
	old := u.AvatarKey
	if err := a.DB.Model(u).Update("avatar_key", key).Error; err != nil {
		_ = a.Uploads.Delete(ctx, key)
		jsonError(c, http.StatusInternalServerError, "could not save avatar")
		return
	}
	if old != "" {
		_ = a.Uploads.Delete(ctx, old) // best effort; the DB is source of truth
	}
	u.AvatarKey = key
	c.Set("apiUser", u)
	c.JSON(http.StatusOK, gin.H{"avatarUrl": a.Uploads.URL(key)})
}

// apiDeleteAvatar removes the current user's avatar.
func (a *App) apiDeleteAvatar(c *gin.Context) {
	u := apiUser(c)
	if u.AvatarKey == "" {
		c.JSON(http.StatusOK, gin.H{})
		return
	}
	old := u.AvatarKey
	if err := a.DB.Model(u).Update("avatar_key", "").Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not remove avatar")
		return
	}
	_ = a.Uploads.Delete(c.Request.Context(), old)
	u.AvatarKey = ""
	c.Set("apiUser", u)
	c.JSON(http.StatusOK, gin.H{})
}

// uploadKeyFromURL returns the storage key for an /uploads/... URL, or
// false when the URL isn't a locally stored upload (e.g. an external link).
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
	u := apiUser(c)
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	if g.LeaderID != u.ID && !u.IsAdmin() {
		jsonError(c, http.StatusForbidden, "only the group leader or an admin can change the cover")
		return
	}
	tmp, ext, mime, err := readImageUpload(c, "cover")
	if err != nil {
		jsonError(c, http.StatusBadRequest, err.Error())
		return
	}
	defer tmp.Close()
	defer os.Remove(tmp.Name())

	rid, err := randomHex(16)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "server error")
		return
	}
	key := storage.NewKey("covers", strconv.Itoa(int(g.ID)), rid, strings.TrimPrefix(ext, "."))
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
	if err := a.DB.Model(&g).Update("image_url", g.ImageURL).Error; err != nil {
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
	u := apiUser(c)
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	if g.LeaderID != u.ID && !u.IsAdmin() {
		jsonError(c, http.StatusForbidden, "only the group leader or an admin can change the cover")
		return
	}
	oldURL := g.ImageURL
	if err := a.DB.Model(&g).Update("image_url", "").Error; err != nil {
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
	local, ok := a.Uploads.(*storage.LocalStore)
	if !ok {
		c.Redirect(http.StatusFound, a.Uploads.URL(key))
		return
	}
	path, err := local.LocalPath(key)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "public, max-age=86400")
	http.ServeContent(c.Writer, c.Request, st.Name(), st.ModTime(), f)
}
