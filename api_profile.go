package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"fold/internal/audit"
	"fold/internal/mail"
	"fold/models"
	"fold/storage"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
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
		AvatarFocal *struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		} `json:"avatarFocal"`
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
	// The email is the login. Changing it sends a verification link to the
	// new address and a notice to the old one; the change applies only after
	// verification (10.1, M1.2).
	var emailChange *string
	if body.Email != nil && normalizeEmail(*body.Email) != normalizeEmail(u.Email) {
		emailChange = body.Email
	}
	if body.Phone != nil {
		phone := strings.TrimSpace(*body.Phone)
		if len(phone) > 30 {
			jsonError(c, http.StatusBadRequest, "phone must be at most 30 characters")
			return
		}
		updates["phone"] = phone
	}
	if body.AvatarFocal != nil {
		updates["avatar_focal_x"] = clampFocal(body.AvatarFocal.X)
		updates["avatar_focal_y"] = clampFocal(body.AvatarFocal.Y)
	}
	if len(updates) == 0 && emailChange == nil {
		c.JSON(http.StatusOK, gin.H{"user": a.toUserJSON(u)})
		return
	}
	updated := *u
	if len(updates) > 0 {
		if err := a.DB.Model(u).Updates(updates).Error; err != nil {
			jsonError(c, http.StatusInternalServerError, "could not save profile")
			return
		}
		// Reload so the response reflects the saved state.
		if err := a.DB.First(&updated, u.ID).Error; err != nil {
			jsonError(c, http.StatusInternalServerError, "could not reload profile")
			return
		}
	}
	// Refresh the request-scoped user so later handlers see the update.
	c.Set("apiUser", &updated)
	if emailChange != nil {
		a.apiRequestEmailChange(c, &updated, normalizeEmail(*emailChange))
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": a.toUserJSON(&updated)})
}

// apiRequestEmailChange starts a verified email change (10.1, M1.2): a
// verification link goes to the new address, a notice to the old one. The
// change applies only after verification, and then ends every other
// session.
func (a *App) apiRequestEmailChange(c *gin.Context, u *models.User, newEmail string) {
	if !validEmail(newEmail) {
		jsonError(c, http.StatusBadRequest, "enter a valid email address")
		return
	}
	var taken int64
	a.DB.Model(&models.User{}).Where("email = ? AND id <> ?", newEmail, u.ID).Count(&taken)
	if taken > 0 {
		jsonError(c, http.StatusConflict, "an account with that email already exists")
		return
	}
	oldEmail := u.Email
	var raw string
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		raw, err = createAccountToken(tx, models.TokenEmailVerify, u.ID,
			map[string]any{"new_email": newEmail}, tokenTTL)
		if err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &u.ID, Action: audit.EmailChangeRequested,
			TargetType: "user", TargetID: u.ID,
			Meta: map[string]any{"oldEmail": oldEmail, "newEmail": newEmail}, IP: c.ClientIP()})
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not start email change")
		return
	}
	if err := a.sendEmailVerification(newEmail, raw); err != nil {
		log.Printf("email verification to %s: %v", newEmail, err)
	}
	if err := a.sendEmailChangeNotice(oldEmail, newEmail); err != nil {
		log.Printf("email change notice to %s: %v", oldEmail, err)
	}
	c.JSON(http.StatusOK, gin.H{"user": a.toUserJSON(u), "verificationSent": true})
}

// sendEmailVerification sends the new-address verification link (C6).
func (a *App) sendEmailVerification(to, raw string) error {
	link := a.emailBaseURL() + "/verify-email?token=" + raw
	return a.Mailer.Send(context.Background(), mail.Message{
		To:      to,
		Subject: "Verify your new Fold email",
		Text: fmt.Sprintf(`Someone asked to change the sign-in email for a Fold account to this address.

Verify it here (the link works once and expires in 24 hours):
%s

The change takes effect only after you use the link. If that wasn't you, you can ignore this email.`,
			link),
	})
}

// sendEmailChangeNotice tells the old address what's happening (10.1).
func (a *App) sendEmailChangeNotice(to, newEmail string) error {
	return a.Mailer.Send(context.Background(), mail.Message{
		To:      to,
		Subject: "Your Fold sign-in email is changing",
		Text: fmt.Sprintf(`The sign-in email for your Fold account at %s is being changed to %s.

The change takes effect only after the new address is verified.

If this wasn't you, contact your church office right away.`,
			a.siteName(), newEmail),
	})
}

// apiVerifyEmailChange redeems an email verification token: the new address
// takes effect and every other session ends (10.1). The token is the
// credential, so this needs no login; the current session stays logged in
// when it belongs to the same user.
func (a *App) apiVerifyEmailChange(c *gin.Context) {
	var body struct {
		Token string `json:"token"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Token == "" {
		jsonError(c, http.StatusBadRequest, "token is required")
		return
	}
	var userID uint
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		tok, err := consumeAccountToken(tx, models.TokenEmailVerify, body.Token)
		if err != nil {
			return err
		}
		var payload struct {
			NewEmail string `json:"new_email"`
		}
		if err := json.Unmarshal([]byte(tok.Payload), &payload); err != nil || !validEmail(payload.NewEmail) {
			return errTokenInvalid
		}
		var u models.User
		if err := tx.First(&u, tok.UserID).Error; err != nil || u.Status == models.UserDisabled {
			return errTokenInvalid
		}
		var taken int64
		tx.Model(&models.User{}).Where("email = ? AND id <> ?", payload.NewEmail, u.ID).Count(&taken)
		if taken > 0 {
			return errEmailTaken
		}
		oldEmail := u.Email
		userID = u.ID
		if err := tx.Model(&u).Updates(map[string]any{
			"email":           payload.NewEmail,
			"session_version": gorm.Expr("session_version + 1"),
		}).Error; err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &u.ID, Action: audit.EmailChanged,
			TargetType: "user", TargetID: u.ID,
			Meta: map[string]any{"oldEmail": oldEmail, "newEmail": payload.NewEmail}, IP: c.ClientIP()})
	})
	switch {
	case errors.Is(err, errTokenInvalid):
		jsonError(c, http.StatusBadRequest, "that link is invalid or has expired")
		return
	case errors.Is(err, errEmailTaken):
		jsonError(c, http.StatusConflict, "an account with that email already exists")
		return
	case err != nil:
		jsonError(c, http.StatusInternalServerError, "could not verify email")
		return
	}
	var fresh models.User
	if err := a.DB.First(&fresh, userID).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not verify email")
		return
	}
	// This browser stays logged in; every other session ends.
	if session, serr := a.Store.Get(c.Request, sessionName); serr == nil {
		if uid, ok := session.Values["userID"].(uint); ok && uid == fresh.ID {
			session.Values["sessionVersion"] = fresh.SessionVersion
			_ = session.Save(c.Request, c.Writer)
		}
	}
	c.JSON(http.StatusOK, gin.H{"user": a.toUserJSON(&fresh)})
}

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
	tmp, _, mime, err := readImageUpload(c, "avatar")
	if err != nil {
		jsonError(c, http.StatusBadRequest, err.Error())
		return
	}
	defer tmp.Close()
	defer os.Remove(tmp.Name())

	st, err := tmp.Stat()
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "server error")
		return
	}
	avatarURL, err := a.saveAvatar(c.Request.Context(), u, tmp, st.Size(), mime)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not store avatar")
		return
	}
	c.Set("apiUser", u)
	c.JSON(http.StatusOK, gin.H{"avatarUrl": avatarURL})
}

// saveAvatar stores r as the user's avatar and deletes the previous blob.
func (a *App) saveAvatar(ctx context.Context, u *models.User, r io.Reader, size int64, mime string) (string, error) {
	ext, ok := allowedImageTypes[mime]
	if !ok {
		return "", fmt.Errorf("only JPEG, PNG, GIF, and WebP images are allowed")
	}
	id, err := randomHex(16)
	if err != nil {
		return "", err
	}
	key := storage.NewKey("avatars", strconv.FormatUint(uint64(u.ID), 10), id, strings.TrimPrefix(ext, "."))
	if err := a.Uploads.Put(ctx, key, r, size, mime); err != nil {
		return "", err
	}
	old := u.AvatarKey
	if err := a.DB.Model(u).Update("avatar_key", key).Error; err != nil {
		_ = a.Uploads.Delete(ctx, key)
		return "", err
	}
	if old != "" {
		_ = a.Uploads.Delete(ctx, old)
	}
	u.AvatarKey = key
	return a.Uploads.URL(key), nil
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
	g, _, ok := a.loadManagedGroup(c)
	if !ok {
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
	g, _, ok := a.loadManagedGroup(c)
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
