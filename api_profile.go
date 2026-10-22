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
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"fold/internal/accounts"
	"fold/internal/audit"
	"fold/internal/events"
	"fold/internal/mail"
	"fold/internal/notify"
	"fold/internal/policy"
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
		Timezone    *string `json:"timezone"`
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
		if utf8.RuneCountInString(name) > 100 {
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
	if body.Timezone != nil {
		tz := strings.TrimSpace(*body.Timezone)
		if _, err := events.LoadZone(tz); err != nil {
			jsonError(c, http.StatusBadRequest, "timezone must be an IANA name, for example America/Denver")
			return
		}
		updates["timezone"] = tz
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
	// Per-user and per-target limits (#92): a member can't use the change
	// form to spam an address, and the target gets at most a few links.
	// Past the limit the answer is the same; nothing is sent.
	ctx := c.Request.Context()
	userOK, err := a.targetAllow(ctx, "emailchange:user:"+strconv.FormatUint(uint64(u.ID), 10))
	addrOK := false
	if err == nil {
		addrOK, err = a.targetAllow(ctx, "emailchange:addr:"+normalizeEmail(newEmail))
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not start email change")
		return
	}
	if !userOK || !addrOK {
		// Answer as usual and log it (DEC-52): nothing is stored or sent.
		log.Printf("email change rate limited: user=%d", u.ID)
		c.JSON(http.StatusOK, gin.H{"user": a.toUserJSON(u), "verificationSent": true})
		return
	}
	oldEmail := u.Email
	var raw string
	err = a.DB.Transaction(func(tx *gorm.DB) error {
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

// ---------------------------------------------------------------------------
// My household (M1.9 slice 1, 5.3, 5.10, DEC-32). An account gets a
// household on first use, named after the account's last name. Any adult
// account in the household manages it (policy.CanManageHousehold).
// Adding a child or teen writes a pending guardian link and asks the
// admins to confirm it; an adult needs no approval. Nothing here touches
// registrations; household people register from slice 2.
// ---------------------------------------------------------------------------

type householdPersonJSON struct {
	ID                      uint    `json:"id"`
	HouseholdID             uint    `json:"householdId"`
	FirstName               string  `json:"firstName"`
	LastName                string  `json:"lastName"`
	Name                    string  `json:"name"`
	Kind                    string  `json:"kind"` // child | teen | adult
	BirthDate               string  `json:"birthDate,omitempty"`
	CareNotes               *string `json:"careNotes,omitempty"`
	RegistrationEligibility string  `json:"registrationEligibility"`
	Age                     *int    `json:"age,omitempty"`
	PhotoConsent            bool    `json:"photoConsent"`
	LinkStatus              string  `json:"linkStatus"` // pending | confirmed | rejected | revoked | none
	GuardianLinkID          *uint   `json:"guardianLinkId,omitempty"`
	ConvertedUserID         *uint   `json:"convertedUserId,omitempty"`
	CreatedBy               uint    `json:"createdBy"`
}

type householdAccountJSON struct {
	UserID   uint   `json:"userId"`
	Name     string `json:"name"`
	Relation string `json:"relation"` // guardian | adult | teen
}

type householdJSON struct {
	ID        uint                   `json:"id"`
	Name      string                 `json:"name"`
	Address   string                 `json:"address"`
	CreatedBy uint                   `json:"createdBy"`
	People    []householdPersonJSON  `json:"people"`
	Accounts  []householdAccountJSON `json:"accounts"`
}

// householdAccountIn loads a user's household account row in db (their
// only one: user_id is unique).
func householdAccountIn(db *gorm.DB, userID uint) (models.HouseholdAccount, bool) {
	var acct models.HouseholdAccount
	if err := db.Where("user_id = ?", userID).First(&acct).Error; err != nil {
		return acct, false
	}
	return acct, true
}

// householdRelOf turns a household account row into policy facts.
func householdRelOf(acct models.HouseholdAccount, found bool) policy.HouseholdRel {
	return policy.HouseholdRel{
		Account: found,
		Adult:   found && (acct.Relation == models.HouseholdRelationGuardian || acct.Relation == models.HouseholdRelationAdult),
	}
}

// defaultHouseholdName names a new household after the account's last
// name: "Smith household" for John Smith.
func defaultHouseholdName(u *models.User) string {
	name := strings.TrimSpace(u.Name)
	if name == "" {
		name = strings.TrimSpace(u.DisplayName)
	}
	fields := strings.Fields(name)
	if len(fields) == 0 {
		return "My household"
	}
	return fields[len(fields)-1] + " household"
}

// ensureHousehold loads the user's household, creating it on first use
// (audited household.created). It answers and reports false on failure.
func (a *App) ensureHousehold(c *gin.Context, u *models.User) (*models.Household, models.HouseholdAccount, bool) {
	if acct, found := householdAccountIn(a.DB, u.ID); found {
		var household models.Household
		if err := a.DB.First(&household, acct.HouseholdID).Error; err != nil {
			jsonError(c, http.StatusInternalServerError, "could not load household")
			return nil, acct, false
		}
		return &household, acct, true
	}
	if !policy.CanCreateHousehold(asPerson(u)) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return nil, models.HouseholdAccount{}, false
	}
	household := models.Household{Name: defaultHouseholdName(u), CreatedBy: u.ID}
	var acct models.HouseholdAccount
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&household).Error; err != nil {
			return err
		}
		acct = models.HouseholdAccount{HouseholdID: household.ID, UserID: u.ID, Relation: models.HouseholdRelationGuardian}
		if err := tx.Create(&acct).Error; err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &u.ID, Action: audit.HouseholdCreated,
			TargetType: "household", TargetID: household.ID, IP: c.ClientIP()})
	})
	if err != nil {
		// A concurrent first use may have won the unique user row.
		if acct, found := householdAccountIn(a.DB, u.ID); found {
			var household models.Household
			if ferr := a.DB.First(&household, acct.HouseholdID).Error; ferr == nil {
				return &household, acct, true
			}
		}
		jsonError(c, http.StatusInternalServerError, "could not create household")
		return nil, acct, false
	}
	return &household, acct, true
}

// manageMyHousehold is ensureHousehold plus policy.CanManageHousehold:
// a teen household account sees the household exists but manages none of
// it (403, section 6).
func (a *App) manageMyHousehold(c *gin.Context) (*models.Household, bool) {
	u := apiUser(c)
	household, acct, ok := a.ensureHousehold(c, u)
	if !ok {
		return nil, false
	}
	if !policy.CanManageHousehold(asPerson(u), householdRelOf(acct, true)) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return nil, false
	}
	return household, true
}

// guardianLinksForPeople loads every guardian link for the people, by
// person id, oldest first.
func guardianLinksForPeople(db *gorm.DB, personIDs []uint) map[uint][]models.GuardianLink {
	out := map[uint][]models.GuardianLink{}
	if len(personIDs) == 0 {
		return out
	}
	var links []models.GuardianLink
	db.Where("person_id IN ?", personIDs).Order("id").Find(&links)
	for _, l := range links {
		if l.PersonID != nil {
			out[*l.PersonID] = append(out[*l.PersonID], l)
		}
	}
	return out
}

// effectiveLink is a person's link status for display: a confirmed link
// wins, then a pending one (still waiting), then the latest decided one.
// No link at all is "none" (an adult, who needs no approval).
func effectiveLink(links []models.GuardianLink) (string, *uint) {
	if len(links) == 0 {
		return "none", nil
	}
	for _, status := range []string{models.GuardianConfirmed, models.GuardianPending} {
		for _, l := range links {
			if l.Status == status {
				id := l.ID
				return status, &id
			}
		}
	}
	last := links[len(links)-1]
	return last.Status, &last.ID
}

func (a *App) toHouseholdPersonJSON(person *models.HouseholdPerson, links []models.GuardianLink, viewer *models.User) householdPersonJSON {
	status, linkID := effectiveLink(links)
	if person.Kind == models.HouseholdAdult {
		// An adult needs no approval, whatever links a former kind left.
		status, linkID = "none", nil
	}
	var notes *string
	rel := policy.HouseholdRel{}
	if viewer != nil {
		for _, link := range links {
			if link.GuardianUserID == viewer.ID && link.Status == models.GuardianConfirmed {
				rel.Guardian = true
			}
		}
	}
	facts := householdFacts(person, links, a.householdClock(time.Now().UTC()))
	if policy.CanSeeCareNotes(asPerson(viewer), facts, rel, nil, time.Now().UTC()) {
		notes = &person.CareNotes
	}
	eligibility := policy.HouseholdRegistrationProblem(facts)
	if eligibility == "" {
		eligibility = "eligible"
	} else {
		eligibility = strings.TrimPrefix(eligibility, "household_")
	}
	if person.ConvertedUserID != nil {
		eligibility = "converted"
	}
	return householdPersonJSON{
		RegistrationEligibility: eligibility, Age: facts.AgeYears,
		ID: person.ID, HouseholdID: person.HouseholdID,
		FirstName: person.FirstName, LastName: person.LastName, Name: person.FullName(),
		Kind: person.Kind, BirthDate: formatBirthDate(person.BirthDate),
		CareNotes: notes, PhotoConsent: person.PhotoConsent,
		LinkStatus: status, GuardianLinkID: linkID,
		ConvertedUserID: person.ConvertedUserID, CreatedBy: person.CreatedBy,
	}
}

func (a *App) toHouseholdJSON(household *models.Household, viewer *models.User) householdJSON {
	out := householdJSON{
		ID: household.ID, Name: household.Name, Address: household.Address,
		CreatedBy: household.CreatedBy,
		People:    []householdPersonJSON{}, Accounts: []householdAccountJSON{},
	}
	var people []models.HouseholdPerson
	a.DB.Where("household_id = ?", household.ID).Order("id").Find(&people)
	ids := make([]uint, 0, len(people))
	for i := range people {
		ids = append(ids, people[i].ID)
	}
	links := guardianLinksForPeople(a.DB, ids)
	for i := range people {
		out.People = append(out.People, a.toHouseholdPersonJSON(&people[i], links[people[i].ID], viewer))
	}
	var accounts []models.HouseholdAccount
	a.DB.Where("household_id = ?", household.ID).Order("id").Find(&accounts)
	for _, acct := range accounts {
		out.Accounts = append(out.Accounts, householdAccountJSON{
			UserID: acct.UserID, Name: nameIn(a.DB, acct.UserID), Relation: acct.Relation,
		})
	}
	return out
}

// createGuardianLink writes a pending link for a child or teen person
// and asks the admins to confirm it, all in tx (DEC-32). It returns the
// notification ids to deliver email for after the commit.
func createGuardianLink(tx *gorm.DB, guardianID uint, person *models.HouseholdPerson) (*models.GuardianLink, []uint, error) {
	link := models.GuardianLink{GuardianUserID: guardianID, PersonID: &person.ID, Status: models.GuardianPending}
	if err := tx.Create(&link).Error; err != nil {
		return nil, nil, err
	}
	ids, err := notifyGuardianLinkPending(tx, &link, person)
	return &link, ids, err
}

// revokeGuardianLinks ends every live (pending or confirmed) link to a
// person in tx: a pending task is resolved withdrawn, and each revoked
// link is audited (DEC-32 rows are never purged).
func revokeGuardianLinks(tx *gorm.DB, c *gin.Context, actor uint, person *models.HouseholdPerson) error {
	var links []models.GuardianLink
	tx.Where("person_id = ? AND status IN ?", person.ID,
		[]string{models.GuardianPending, models.GuardianConfirmed}).Find(&links)
	now := time.Now().UTC()
	for _, link := range links {
		res := tx.Model(&models.GuardianLink{}).Where("id = ? AND status = ?", link.ID, link.Status).
			Updates(map[string]any{"status": models.GuardianRevoked, "decided_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			continue
		}
		if link.Status == models.GuardianPending {
			if _, err := notify.Resolve(tx, notify.HouseholdLinkPending, link.ID, &actor, "withdrawn"); err != nil {
				return err
			}
		}
		if err := audit.Write(tx, audit.Entry{Actor: &actor, Action: audit.GuardianLinkRevoked,
			TargetType: "guardian_link", TargetID: link.ID,
			Meta: map[string]any{"guardianUserId": link.GuardianUserID, "personId": person.ID},
			IP:   c.ClientIP()}); err != nil {
			return err
		}
	}
	return nil
}

// apiGetMyHousehold returns the user's household, or {household: null}
// when they have none yet. It is read-only (Warden, PR 106): the household
// is created by PUT /me/household or POST /me/household/people through
// ensureHousehold.
func (a *App) apiGetMyHousehold(c *gin.Context) {
	u := apiUser(c)
	acct, found := householdAccountIn(a.DB, u.ID)
	if !found {
		c.JSON(http.StatusOK, gin.H{"household": nil})
		return
	}
	if !policy.CanManageHousehold(asPerson(u), householdRelOf(acct, true)) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return
	}
	var household models.Household
	if err := a.DB.First(&household, acct.HouseholdID).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load household")
		return
	}
	c.JSON(http.StatusOK, gin.H{"household": a.toHouseholdJSON(&household, u)})
}

// apiUpdateMyHousehold renames the household or sets its address.
func (a *App) apiUpdateMyHousehold(c *gin.Context) {
	household, ok := a.manageMyHousehold(c)
	if !ok {
		return
	}
	var body struct {
		Name    *string `json:"name"`
		Address *string `json:"address"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	updates := map[string]any{}
	if body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		if name == "" {
			jsonError(c, http.StatusBadRequest, "name is required")
			return
		}
		if len(name) > 100 {
			jsonError(c, http.StatusBadRequest, "name must be at most 100 characters")
			return
		}
		updates["name"] = name
	}
	if body.Address != nil {
		address := strings.TrimSpace(*body.Address)
		if utf8.RuneCountInString(address) > 500 {
			jsonError(c, http.StatusBadRequest, "address is too long")
			return
		}
		updates["address"] = address
	}
	if len(updates) > 0 {
		if err := a.DB.Transaction(func(tx *gorm.DB) error {
			if err := lockHousehold(tx, household.ID); err != nil {
				return err
			}
			if err := tx.Model(household).Updates(updates).Error; err != nil {
				return err
			}
			fields := []string{}
			for key := range updates {
				fields = append(fields, key)
			}
			sort.Strings(fields)
			return audit.Write(tx, audit.Entry{Actor: &apiUser(c).ID, Action: audit.HouseholdUpdated, TargetType: "household", TargetID: household.ID, Meta: map[string]any{"fields": fields}, IP: c.ClientIP()})
		}); err != nil {
			jsonError(c, http.StatusInternalServerError, "could not save household")
			return
		}
		if err := a.DB.First(household, household.ID).Error; err != nil {
			jsonError(c, http.StatusInternalServerError, "could not load household")
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"household": a.toHouseholdJSON(household, apiUser(c))})
}

// householdPersonInput is the add/edit body. Fields are pointers so an
// edit can tell "not provided" from "set to empty". Name is a fallback
// for clients that send one field: it splits into first and last.
type householdPersonInput struct {
	FirstName    *string `json:"firstName"`
	LastName     *string `json:"lastName"`
	Name         *string `json:"name"`
	Kind         *string `json:"kind"`
	BirthDate    *string `json:"birthDate"`
	CareNotes    *string `json:"careNotes"`
	PhotoConsent *bool   `json:"photoConsent"`
}

func validHouseholdKind(kind string) bool {
	return kind == models.HouseholdChild || kind == models.HouseholdTeen || kind == models.HouseholdAdult
}

// splitPersonName splits a single name field into first and last names.
func splitPersonName(name string) (string, string) {
	fields := strings.Fields(strings.TrimSpace(name))
	if len(fields) == 0 {
		return "", ""
	}
	if len(fields) == 1 {
		return fields[0], ""
	}
	return fields[0], strings.Join(fields[1:], " ")
}

// apiAddHouseholdPerson adds someone to the user's household (created on
// first use). A child or teen also gets a pending guardian link and the
// admins a waiting task (DEC-32); an adult needs no approval.
func (a *App) apiAddHouseholdPerson(c *gin.Context) {
	household, ok := a.manageMyHousehold(c)
	if !ok {
		return
	}
	me := apiUser(c)
	var body householdPersonInput
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	first, last := "", ""
	if body.FirstName != nil {
		first = strings.TrimSpace(*body.FirstName)
	}
	if body.LastName != nil {
		last = strings.TrimSpace(*body.LastName)
	}
	if first == "" && body.Name != nil {
		first, last = splitPersonName(*body.Name)
	}
	if first == "" {
		jsonError(c, http.StatusBadRequest, "first name is required")
		return
	}
	if len(first) > 100 || len(last) > 100 {
		jsonError(c, http.StatusBadRequest, "name must be at most 100 characters")
		return
	}
	kind := ""
	if body.Kind != nil {
		kind = strings.TrimSpace(*body.Kind)
	}
	if !validHouseholdKind(kind) {
		jsonError(c, http.StatusBadRequest, "kind must be child, teen, or adult")
		return
	}
	var birth *time.Time
	if body.BirthDate != nil && strings.TrimSpace(*body.BirthDate) != "" {
		parsed, err := accounts.ParseBirthDate(strings.TrimSpace(*body.BirthDate))
		if err != nil {
			jsonError(c, http.StatusBadRequest, "birth date must be a real date (YYYY-MM-DD)")
			return
		}
		birth = parsed
	}
	if birth != nil && (accounts.AgeOn(*birth, a.householdClock(time.Now().UTC())) < 0 || (kind == models.HouseholdAdult && accounts.AgeOn(*birth, a.householdClock(time.Now().UTC())) < 18)) {
		jsonErrorCode(c, 409, "household_age_mismatch", "household_age_mismatch")
		return
	}
	careNotes := ""
	if body.CareNotes != nil {
		careNotes = strings.TrimSpace(*body.CareNotes)
	}
	if len(careNotes) > 5000 {
		jsonError(c, http.StatusBadRequest, "care notes are too long")
		return
	}
	photoConsent := false
	if body.PhotoConsent != nil {
		photoConsent = *body.PhotoConsent
	}
	person := models.HouseholdPerson{
		HouseholdID: household.ID, FirstName: first, LastName: last, Kind: kind,
		BirthDate: birth, CareNotes: careNotes, PhotoConsent: photoConsent, CreatedBy: me.ID,
	}
	var emailed []uint
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockHousehold(tx, household.ID); err != nil {
			return err
		}
		if err := tx.Create(&person).Error; err != nil {
			return err
		}
		meta := map[string]any{"personId": person.ID, "kind": person.Kind}
		if policy.NeedsGuardianLink(person.Kind) {
			link, ids, err := createGuardianLink(tx, me.ID, &person)
			if err != nil {
				return err
			}
			emailed = append(emailed, ids...)
			meta["guardianLinkId"] = link.ID
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.HouseholdPersonAdded,
			TargetType: "household", TargetID: household.ID, Meta: meta, IP: c.ClientIP()})
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not add that person")
		return
	}
	a.deliverNotificationEmails(emailed)
	links := guardianLinksForPeople(a.DB, []uint{person.ID})
	c.JSON(http.StatusCreated, gin.H{"person": a.toHouseholdPersonJSON(&person, links[person.ID], me)})
}

// loadMyHouseholdPerson loads a person in the user's household for an
// edit or removal: 404 when they have no household or the person is not
// in it, 403 when their account in it may not manage it (a teen account).
func (a *App) loadMyHouseholdPerson(c *gin.Context) (*models.Household, *models.HouseholdPerson, bool) {
	u := apiUser(c)
	id, _ := strconv.Atoi(c.Param("id"))
	acct, found := householdAccountIn(a.DB, u.ID)
	if !found {
		jsonError(c, http.StatusNotFound, "household person not found")
		return nil, nil, false
	}
	if !policy.CanManageHousehold(asPerson(u), householdRelOf(acct, true)) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return nil, nil, false
	}
	var household models.Household
	if err := a.DB.First(&household, acct.HouseholdID).Error; err != nil {
		jsonError(c, http.StatusNotFound, "household person not found")
		return nil, nil, false
	}
	var person models.HouseholdPerson
	if err := a.DB.Where("id = ? AND household_id = ?", id, household.ID).First(&person).Error; err != nil {
		jsonError(c, http.StatusNotFound, "household person not found")
		return nil, nil, false
	}
	return &household, &person, true
}

// apiUpdateHouseholdPerson edits a person in the user's household. A
// kind change keeps DEC-32 true: becoming a child or teen starts a
// pending guardian link, and becoming an adult revokes live links.
func (a *App) apiUpdateHouseholdPerson(c *gin.Context) {
	_, person, ok := a.loadMyHouseholdPerson(c)
	if !ok {
		return
	}
	me := apiUser(c)
	var body householdPersonInput
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	updates := map[string]any{}
	if body.Name != nil && body.FirstName == nil && body.LastName == nil {
		first, last := splitPersonName(*body.Name)
		body.FirstName, body.LastName = &first, &last
	}
	if body.FirstName != nil {
		first := strings.TrimSpace(*body.FirstName)
		if first == "" {
			jsonError(c, http.StatusBadRequest, "first name is required")
			return
		}
		if len(first) > 100 {
			jsonError(c, http.StatusBadRequest, "name must be at most 100 characters")
			return
		}
		updates["first_name"] = first
	}
	if body.LastName != nil {
		last := strings.TrimSpace(*body.LastName)
		if len(last) > 100 {
			jsonError(c, http.StatusBadRequest, "name must be at most 100 characters")
			return
		}
		updates["last_name"] = last
	}
	newKind := person.Kind
	if body.Kind != nil {
		newKind = strings.TrimSpace(*body.Kind)
		if !validHouseholdKind(newKind) {
			jsonError(c, http.StatusBadRequest, "kind must be child, teen, or adult")
			return
		}
		updates["kind"] = newKind
	}
	if body.BirthDate != nil {
		if strings.TrimSpace(*body.BirthDate) == "" {
			updates["birth_date"] = nil
		} else {
			parsed, err := accounts.ParseBirthDate(strings.TrimSpace(*body.BirthDate))
			if err != nil {
				jsonError(c, http.StatusBadRequest, "birth date must be a real date (YYYY-MM-DD)")
				return
			}
			updates["birth_date"] = parsed
		}
	}
	if body.CareNotes != nil {
		careNotes := strings.TrimSpace(*body.CareNotes)
		if len(careNotes) > 5000 {
			jsonError(c, http.StatusBadRequest, "care notes are too long")
			return
		}
		updates["care_notes"] = careNotes
	}
	if body.PhotoConsent != nil {
		updates["photo_consent"] = *body.PhotoConsent
	}
	var emailed []uint
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockHousehold(tx, person.HouseholdID); err != nil {
			return err
		}
		if err := tx.First(person, person.ID).Error; err != nil {
			return err
		}
		oldKind := person.Kind
		merged := *person
		if body.Kind != nil {
			merged.Kind = newKind
		} else {
			newKind = person.Kind
		}
		if birth, set := updates["birth_date"]; set {
			if birth == nil {
				merged.BirthDate = nil
			} else {
				merged.BirthDate = birth.(*time.Time)
			}
		}
		if merged.BirthDate != nil && (accounts.AgeOn(*merged.BirthDate, a.householdClock(time.Now().UTC())) < 0 || (merged.Kind == models.HouseholdAdult && accounts.AgeOn(*merged.BirthDate, a.householdClock(time.Now().UTC())) < 18)) {
			return householdRegistrationError{code: "household_age_mismatch", personID: person.ID}
		}
		if merged.Kind == models.HouseholdAdult && merged.BirthDate == nil {
			var n int64
			if err := tx.Model(&models.Registration{}).Where("household_person_id = ? AND status IN ?", person.ID, []string{models.RegGoing, models.RegMaybe, models.RegWaitlisted}).Count(&n).Error; err != nil {
				return err
			}
			if n > 0 {
				return householdRegistrationError{code: "household_birth_date_required", personID: person.ID}
			}
		}
		if len(updates) > 0 {
			if err := tx.Model(person).Updates(updates).Error; err != nil {
				return err
			}
		}
		if err := tx.First(person, person.ID).Error; err != nil {
			return err
		}
		switch {
		case policy.NeedsGuardianLink(oldKind) && !policy.NeedsGuardianLink(newKind):
			if err := revokeGuardianLinks(tx, c, me.ID, person); err != nil {
				return err
			}
		case !policy.NeedsGuardianLink(oldKind) && policy.NeedsGuardianLink(newKind):
			_, ids, err := createGuardianLink(tx, me.ID, person)
			if err != nil {
				return err
			}
			emailed = append(emailed, ids...)
		}
		ids, err := a.reconcileHouseholdPerson(tx, person, false, me.ID, c.ClientIP())
		if err != nil {
			return err
		}
		emailed = append(emailed, ids...)
		fields := []string{}
		for key := range updates {
			fields = append(fields, key)
		}
		sort.Strings(fields)
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.HouseholdPersonUpdated, TargetType: "household", TargetID: person.HouseholdID, Meta: map[string]any{"personId": person.ID, "fields": fields}, IP: c.ClientIP()})
	})
	if err != nil {
		registrationError(c, err)
		return
	}
	a.deliverNotificationEmails(emailed)
	var fresh models.HouseholdPerson
	if err := a.DB.First(&fresh, person.ID).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load that person")
		return
	}
	links := guardianLinksForPeople(a.DB, []uint{fresh.ID})
	c.JSON(http.StatusOK, gin.H{"person": a.toHouseholdPersonJSON(&fresh, links[fresh.ID], me)})
}

// apiDeleteHouseholdPerson removes a person from the user's household:
// a soft delete, with live guardian links revoked and any waiting admin
// task resolved withdrawn, audited in the same transaction.
func (a *App) apiDeleteHouseholdPerson(c *gin.Context) {
	household, person, ok := a.loadMyHouseholdPerson(c)
	if !ok {
		return
	}
	me := apiUser(c)
	var emailed []uint
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockHousehold(tx, household.ID); err != nil {
			return err
		}
		if err := tx.First(person, person.ID).Error; err != nil {
			return err
		}
		ids, err := a.reconcileHouseholdPerson(tx, person, true, me.ID, c.ClientIP())
		if err != nil {
			return err
		}
		emailed = append(emailed, ids...)
		if err := revokeGuardianLinks(tx, c, me.ID, person); err != nil {
			return err
		}
		if err := audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.HouseholdPersonRemoved,
			TargetType: "household", TargetID: household.ID,
			Meta: map[string]any{"personId": person.ID, "kind": person.Kind}, IP: c.ClientIP()}); err != nil {
			return err
		}
		return tx.Delete(person).Error
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not remove that person")
		return
	}
	a.deliverNotificationEmails(emailed)
	c.JSON(http.StatusOK, gin.H{})
}
