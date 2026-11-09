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
	"fold/internal/jobs"
	"fold/internal/mail"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"
	"fold/storage"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
}

// apiUpdateMe updates the current user's profile. Fields are pointers so
// "not provided" is distinct from "set to empty".
func (a *App) apiUpdateMe(c *gin.Context) {
	u := apiUser(c)
	var body struct {
		Name                   *string `json:"name"`
		DisplayName            *string `json:"displayName"`
		Email                  *string `json:"email"`
		CurrentPassword        string  `json:"currentPassword"`
		Phone                  *string `json:"phone"`
		Timezone               *string `json:"timezone"`
		HideContactFromLeaders *bool   `json:"hideContactFromLeaders"`
		AvatarFocal            *struct {
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
			fail(c, errNameTooLong100)
			return
		}
		updates["name"] = name
	}
	if body.DisplayName != nil {
		dn := strings.TrimSpace(*body.DisplayName)
		if utf8.RuneCountInString(dn) > 100 {
			fail(c, errDisplayNameTooLong)
			return
		}
		updates["display_name"] = dn
	}
	// The email is the login. Changing it sends a verification link to the
	// new address and a notice to the old one; the change applies only after
	// verification (10.1, M1.2).
	var emailChange *string
	if body.Email != nil && mail.NormalizeAddress(*body.Email) != mail.NormalizeAddress(u.Email) {
		emailChange = body.Email
	}
	if body.Phone != nil {
		phone := strings.TrimSpace(*body.Phone)
		if len(phone) > 30 {
			fail(c, errPhoneTooLong)
			return
		}
		updates["phone"] = phone
	}
	if body.Timezone != nil {
		tz := strings.TrimSpace(*body.Timezone)
		if _, err := events.LoadZone(tz); err != nil {
			fail(c, errInvalidTimezone)
			return
		}
		updates["timezone"] = tz
	}
	if body.HideContactFromLeaders != nil && policy.CanEditOwnContactPrivacy(asPerson(u)) {
		updates["hide_contact_from_leaders"] = *body.HideContactFromLeaders
	}
	if body.AvatarFocal != nil {
		updates["avatar_focal_x"] = clampFocal(body.AvatarFocal.X)
		updates["avatar_focal_y"] = clampFocal(body.AvatarFocal.Y)
	}
	// An email change proves the current password before anything is saved,
	// so a wrong password leaves the other fields as they were.
	if emailChange != nil {
		if !validEmail(mail.NormalizeAddress(*emailChange)) {
			fail(c, errEnterValidEmail)
			return
		}
		if !a.reauthenticate(c, u, body.CurrentPassword) {
			return
		}
	}
	if len(updates) == 0 && emailChange == nil {
		c.JSON(http.StatusOK, profileUpdateJSON{User: a.toUserJSON(u)})
		return
	}
	updated := *u
	if len(updates) > 0 {
		err := a.DB.Transaction(func(tx *gorm.DB) error {
			var locked models.User
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, u.ID).Error; err != nil {
				return err
			}
			// Recheck the account type under the lock. A teen's stored value
			// never changes through this profile preference.
			if !policy.CanEditOwnContactPrivacy(asPerson(&locked)) {
				delete(updates, "hide_contact_from_leaders")
			}
			oldHidden := locked.HideContactFromLeaders
			if err := tx.Model(&locked).Updates(updates).Error; err != nil {
				return err
			}
			if value, supplied := updates["hide_contact_from_leaders"]; supplied && value != oldHidden {
				if err := audit.Write(tx, audit.Entry{Actor: &u.ID, Action: contactPrivacyChanged,
					TargetType: "user", TargetID: u.ID, IP: c.ClientIP(),
					Meta: map[string]any{"hideContactFromLeaders": value}}); err != nil {
					return err
				}
			}
			return tx.First(&updated, u.ID).Error
		})
		if err != nil {
			jsonError(c, http.StatusInternalServerError, "could not save profile")
			return
		}
	}
	if emailChange != nil {
		a.apiRequestEmailChange(c, u, mail.NormalizeAddress(*emailChange))
		return
	}
	c.JSON(http.StatusOK, profileUpdateJSON{User: a.toUserJSON(&updated)})
}

// apiRequestEmailChange starts a verified email change (10.1, M1.2): a
// verification link goes to the new address, a notice to the old one. The
// change applies only after verification, and then ends every other
// session. The caller has already checked the new address and the current
// password.
func (a *App) apiRequestEmailChange(c *gin.Context, u *models.User, newEmail string) {
	// Validate the proof against locked credentials before any address lookup.
	// Quotas retain their independently committed transactions; token issuance
	// below rechecks this exact epoch, without nesting a second DB connection.
	var validated *models.User
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		validated, err = lockCurrentCredentials(tx, u)
		return err
	})
	if answerStaleSession(c, err) {
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not start email change")
		return
	}
	u = validated
	// Per-user and per-target limits (#92): a member can't use the change
	// form to spam an address, and the target gets at most a few links.
	// Past the limit the answer is the same; nothing is sent.
	ctx := c.Request.Context()
	userOK, err := a.targetAllow(ctx, emailChangeUserKey(u.ID))
	addrOK := false
	if err == nil && userOK {
		// Account checks consume the actor budget too (#157). An exhausted
		// actor never looks up another address or spends its target budget.
		var taken int64
		if err := a.DB.Model(&models.User{}).Where("email = ? AND id <> ?", newEmail, u.ID).Count(&taken).Error; err != nil {
			jsonError(c, http.StatusInternalServerError, "could not start email change")
			return
		}
		if taken > 0 {
			fail(c, errEmailTaken)
			return
		}
		addrOK, err = a.targetAllow(ctx, emailChangeAddrKey(newEmail))
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not start email change")
		return
	}
	if !userOK || !addrOK {
		// Answer as usual and log it (DEC-52): nothing is stored or sent.
		log.Printf("email change rate limited: user=%d", u.ID)
		c.JSON(http.StatusOK, profileUpdateJSON{User: a.toUserJSON(u), VerificationSent: true})
		return
	}
	oldEmail := u.Email
	var cancelRaw string
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if _, err := lockCurrentCredentials(tx, u); err != nil {
			return err
		}
		// The verification row is created first, so its deadline cannot pass
		// the cancellation token's. Its raw value is discarded: the queued job
		// rotates the hash and mails the only usable link.
		_, verifyID, err := issueAccountToken(tx, models.TokenEmailVerify, u.ID,
			map[string]any{"new_email": newEmail}, tokenTTL)
		if err != nil {
			return err
		}
		cancelRaw, _, err = issueAccountToken(tx, models.TokenEmailChangeCancel, u.ID, nil, tokenTTL)
		if err != nil {
			return err
		}
		if err := audit.Write(tx, audit.Entry{Actor: &u.ID, Action: audit.EmailChangeRequested,
			TargetType: "user", TargetID: u.ID,
			Meta: map[string]any{"oldEmail": oldEmail, "newEmail": newEmail}, IP: c.ClientIP()}); err != nil {
			return err
		}
		_, err = jobs.Enqueue(tx, kindEmailVerifyMail, accountTokenMailJob{AccountTokenID: verifyID}, time.Now(), "", directMailAttempts)
		return err
	})
	if answerStaleSession(c, err) {
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not start email change")
		return
	}
	// The old-address notice stays synchronous: once the change is verified
	// the cancellation capability is revoked and the old address survives only
	// in the audit row, so a delayed job could mint nothing or mail a stale
	// address. Failure is logged; the verification job is already durable.
	if err := a.sendEmailChangeNotice(oldEmail, newEmail, cancelRaw); err != nil {
		// Like every direct-mail log line: the user ID and a cleaned error, never
		// the address a provider refusal may echo back.
		log.Printf("email change notice for user %d: %s", u.ID, jobs.CleanError(err))
	}
	c.JSON(http.StatusOK, profileUpdateJSON{User: a.toUserJSON(u), VerificationSent: true})
}

// sendEmailChangeNotice tells the old address what's happening (10.1). It is
// synchronous, so its send budget leaves headroom under the HTTP write timeout
// for the transaction and response. Delivery jobs use a separate, longer bound.
func (a *App) sendEmailChangeNotice(to, newEmail, cancelRaw string) error {
	link := a.emailBaseURL() + "/cancel-email-change?token=" + cancelRaw
	ctx, cancel := context.WithTimeout(context.Background(), syncMailTimeout)
	defer cancel()
	return a.Mailer.Send(ctx, mail.Message{
		To:      to,
		Subject: "Your Fold sign-in email is changing",
		Text: fmt.Sprintf(`The sign-in email for your Fold account at %s is being changed to %s.

The change takes effect only after the new address is verified.

If this wasn't you, cancel the pending change and end every current sign-in here:
%s

Opening the link does not change anything until you confirm. After cancelling, reset your password and contact your church office. This link works once and expires in 24 hours.`,
			a.siteName(), newEmail, link),
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
	var fresh models.User
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		u, err := lockAccountTokenUser(tx, models.TokenEmailVerify, body.Token)
		if err != nil {
			return err
		}
		if !u.CanSignIn() {
			return errTokenInvalid
		}
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
		if tok.UserID != u.ID {
			return errTokenInvalid
		}
		var taken int64
		if err := tx.Model(&models.User{}).Where("email = ? AND id <> ?", payload.NewEmail, u.ID).Count(&taken).Error; err != nil {
			return err
		}
		if taken > 0 {
			return errEmailTaken
		}
		oldEmail := u.Email
		if err := endAllSessionsIn(tx, u, map[string]any{"email": payload.NewEmail}); err != nil {
			return err
		}
		if err := audit.Write(tx, audit.Entry{Actor: &u.ID, Action: audit.EmailChanged,
			TargetType: "user", TargetID: u.ID,
			Meta: map[string]any{"oldEmail": oldEmail, "newEmail": payload.NewEmail}, IP: c.ClientIP()}); err != nil {
			return err
		}
		// Capture this commit's epoch, never a later password reset's epoch.
		return tx.First(&fresh, u.ID).Error
	})
	switch {
	case errors.Is(err, errTokenInvalid):
		fail(c, errLinkExpired)
		return
	case errors.Is(err, errEmailTaken):
		fail(c, errEmailTaken)
		return
	case err != nil:
		jsonError(c, http.StatusInternalServerError, "could not verify email")
		return
	}
	// This browser stays logged in; every other session ends.
	if session, serr := a.Store.Get(c.Request, sessionName); serr == nil {
		if uid, ok := session.Values[sessionUserKey].(uint); ok && uid == fresh.ID {
			session.Values[sessionVersionKey] = fresh.SessionVersion
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

// ---------------------------------------------------------------------------
// My household (M1.9 slice 1, 5.3, 5.10, DEC-32). An account gets a
// household on first use, named after the account's last name. Any adult
// account in the household manages it (policy.CanManageHousehold).
// Adding a child or teen writes a pending guardian link and asks the
// admins to confirm it; an adult needs no approval. Nothing here touches
// registrations; household people register from slice 2.
// ---------------------------------------------------------------------------

type householdPersonJSON struct {
	Conversion              *conversionJSON `json:"conversion,omitempty"`
	CanConvert              bool            `json:"canConvert"`
	ID                      uint            `json:"id"`
	HouseholdID             uint            `json:"householdId"`
	FirstName               string          `json:"firstName"`
	LastName                string          `json:"lastName"`
	Name                    string          `json:"name"`
	Kind                    string          `json:"kind"` // child | teen | adult
	BirthDate               string          `json:"birthDate,omitempty"`
	CareNotes               *string         `json:"careNotes,omitempty"`
	RegistrationEligibility string          `json:"registrationEligibility"`
	Age                     *int            `json:"age,omitempty"`
	PhotoConsent            bool            `json:"photoConsent"`
	LinkStatus              string          `json:"linkStatus"` // pending | confirmed | rejected | revoked | none
	GuardianLinkID          *uint           `json:"guardianLinkId,omitempty"`
	ConvertedUserID         *uint           `json:"convertedUserId,omitempty"`
	CreatedBy               uint            `json:"createdBy"`
}

type householdAccountJSON struct {
	UserID   uint   `json:"userId"`
	Name     string `json:"name"`
	Relation string `json:"relation"` // guardian | adult | teen
	Status   string `json:"status"`
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

var (
	// errHouseholdForbidden is a failed policy.CanManageHousehold or
	// CanCreateHousehold check made under the user lock.
	errHouseholdForbidden = apiErr(http.StatusForbidden, "", "forbidden")
	// errHouseholdPersonGone means the person is not in the household the
	// locked seat names.
	errHouseholdPersonGone = apiErr(http.StatusNotFound, "", "household person not found")
)

// lockHouseholdSeat is the first lock of every household self-service writer:
// the acting user row FOR NO KEY UPDATE (lockActorIn), taken before
// lockHousehold (user, then household, as the erasure,
// lockConversionParticipants and lockAdminHouseholdParticipants do), plus the
// seat and the policy check, both read under the lock. It takes no contact
// gate; a writer that takes one takes it before this. The caller uses the
// returned row, never the preloaded one, for every later fact. noSeat is the error for an account with no
// seat: forbidden for the household routes, not found for the person routes.
func lockHouseholdSeat(tx *gorm.DB, me *models.User, noSeat error) (*models.User, models.HouseholdAccount, error) {
	fresh, err := lockActorIn(tx, me, actorNoKeyUpdate)
	if err != nil {
		return nil, models.HouseholdAccount{}, err
	}
	seat, found := householdAccountIn(tx, fresh.ID)
	if !found {
		return nil, seat, noSeat
	}
	if !policy.CanManageHousehold(asPerson(fresh), householdRelOf(seat, true)) {
		return nil, seat, errHouseholdForbidden
	}
	return fresh, seat, nil
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
	var household models.Household
	var acct models.HouseholdAccount
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		// The user lock comes first. Under it the account is still live, a
		// concurrent first use has either committed its seat (returned here)
		// or waits for this one, and the name comes from the locked row.
		fresh, err := lockActorIn(tx, u, actorNoKeyUpdate)
		if err != nil {
			return err
		}
		if seat, found := householdAccountIn(tx, fresh.ID); found {
			acct, household = seat, models.Household{}
			return tx.First(&household, seat.HouseholdID).Error
		}
		if !policy.CanCreateHousehold(asPerson(fresh)) {
			return errHouseholdForbidden
		}
		household = models.Household{Name: defaultHouseholdName(fresh), CreatedBy: fresh.ID}
		if err := tx.Create(&household).Error; err != nil {
			return err
		}
		acct = models.HouseholdAccount{HouseholdID: household.ID, UserID: fresh.ID, Relation: models.HouseholdRelationGuardian}
		if err := tx.Create(&acct).Error; err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &fresh.ID, Action: audit.HouseholdCreated,
			TargetType: "household", TargetID: household.ID, IP: c.ClientIP()})
	})
	if isAPIError(err) {
		fail(c, err)
		return nil, models.HouseholdAccount{}, false
	}
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
	out, _ := loadGuardianLinks(db, personIDs)
	return out
}

// loadGuardianLinks is guardianLinksForPeople for callers that can report a
// failed read; a partial map is never returned alongside an error.
func loadGuardianLinks(db *gorm.DB, personIDs []uint) (map[uint][]models.GuardianLink, error) {
	out := map[uint][]models.GuardianLink{}
	if len(personIDs) == 0 {
		return out, nil
	}
	var links []models.GuardianLink
	if err := db.Where("person_id IN ?", personIDs).Order("id").Find(&links).Error; err != nil {
		return nil, err
	}
	for _, l := range links {
		if l.PersonID != nil {
			out[*l.PersonID] = append(out[*l.PersonID], l)
		}
	}
	return out, nil
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

func (a *App) toHouseholdPersonJSON(person *models.HouseholdPerson, links []models.GuardianLink, viewer *models.User, batch *conversionBatch) (householdPersonJSON, error) {
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
	facts := householdFacts(person, links, batch.today)
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
	canConvert, err := batch.allowed(person)
	if errors.Is(err, errConversionHolder) {
		canConvert, err = false, nil
	}
	if err != nil {
		return householdPersonJSON{}, err
	}
	return householdPersonJSON{
		CanConvert:              canConvert,
		RegistrationEligibility: eligibility, Age: facts.AgeYears,
		ID: person.ID, HouseholdID: person.HouseholdID,
		FirstName: person.FirstName, LastName: person.LastName, Name: person.FullName(),
		Kind: person.Kind, BirthDate: formatBirthDate(person.BirthDate),
		CareNotes: notes, PhotoConsent: person.PhotoConsent,
		LinkStatus: status, GuardianLinkID: linkID,
		ConvertedUserID: person.ConvertedUserID, CreatedBy: person.CreatedBy,
	}, nil
}

func (a *App) toHouseholdJSON(household *models.Household, viewer *models.User) (householdJSON, error) {
	out := householdJSON{
		ID: household.ID, Name: household.Name, Address: household.Address,
		CreatedBy: household.CreatedBy,
		People:    []householdPersonJSON{}, Accounts: []householdAccountJSON{},
	}
	var people []models.HouseholdPerson
	if err := a.DB.Where("household_id = ? AND converted_user_id IS NULL", household.ID).Order("id").Find(&people).Error; err != nil {
		return out, err
	}
	ids := make([]uint, 0, len(people))
	for i := range people {
		ids = append(ids, people[i].ID)
	}
	links, err := loadGuardianLinks(a.DB, ids)
	if err != nil {
		return out, err
	}
	var accounts []models.HouseholdAccount
	if err := a.DB.Where("household_id = ?", household.ID).Order("id").Find(&accounts).Error; err != nil {
		return out, err
	}
	var rel policy.HouseholdRel
	for _, acct := range accounts {
		if viewer != nil && acct.UserID == viewer.ID {
			rel = householdRelOf(acct, true)
		}
	}
	batch, err := a.loadConversionBatchFor(a.DB, viewer, rel, ids, links)
	if err != nil {
		return out, err
	}
	var invitations []models.Invitation
	if len(ids) > 0 {
		if err := a.DB.Where("household_person_id IN ? AND accepted_at IS NULL AND revoked_at IS NULL", ids).Find(&invitations).Error; err != nil {
			return out, err
		}
	}
	conversions := map[uint]conversionJSON{}
	for i := range invitations {
		conversions[*invitations[i].HouseholdPersonID] = conversionMetadata(&invitations[i])
	}
	if len(ids) > 0 {
		var requests []models.AccountRequest
		if err := a.DB.Where("household_person_id IN ? AND source = ? AND (status = ? OR (status = ? AND approved_user_id IS NOT NULL))", ids, models.RequestHouseholdConversion, models.RequestPending, models.RequestApproved).Find(&requests).Error; err != nil {
			return out, err
		}
		for _, req := range requests {
			state := "awaiting_approval"
			if req.Status == models.RequestApproved {
				state = "awaiting_consent"
			}
			conversions[*req.HouseholdPersonID] = conversionJSON{Email: req.Email, ExpiresAt: req.ExpiresAt, State: state}
		}
	}
	for i := range people {
		row, err := a.toHouseholdPersonJSON(&people[i], links[people[i].ID], viewer, batch)
		if err != nil {
			return out, err
		}
		if conversion, ok := conversions[people[i].ID]; ok {
			row.Conversion = &conversion
		}
		out.People = append(out.People, row)
	}
	userIDs := make([]uint, 0, len(accounts))
	for _, acct := range accounts {
		userIDs = append(userIDs, acct.UserID)
	}
	users := map[uint]models.User{}
	if len(userIDs) > 0 {
		var found []models.User
		if err := a.DB.Where("id IN ?", userIDs).Find(&found).Error; err != nil {
			return out, err
		}
		for _, u := range found {
			users[u.ID] = u
		}
	}
	for _, acct := range accounts {
		user, ok := users[acct.UserID]
		if !ok {
			continue
		}
		out.Accounts = append(out.Accounts, householdAccountJSON{
			UserID: acct.UserID, Name: user.PublicName(), Relation: acct.Relation, Status: user.Status,
		})
	}
	return out, nil
}

// createGuardianLink writes a pending link for a child or teen person
// and asks the admins to confirm it, all in tx (DEC-32).
func createGuardianLink(tx *gorm.DB, guardianID uint, person *models.HouseholdPerson) (*models.GuardianLink, error) {
	link := models.GuardianLink{GuardianUserID: guardianID, PersonID: &person.ID, Status: models.GuardianPending}
	if err := tx.Create(&link).Error; err != nil {
		return nil, err
	}
	return &link, notifyGuardianLinkPending(tx, &link, person)
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
	out, err := a.toHouseholdJSON(&household, u)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load your household")
		return
	}
	c.JSON(http.StatusOK, gin.H{"household": out})
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
			fail(c, errNameRequired)
			return
		}
		if utf8.RuneCountInString(name) > 100 {
			fail(c, errNameTooLong100)
			return
		}
		updates["name"] = name
	}
	if body.Address != nil {
		address := strings.TrimSpace(*body.Address)
		if utf8.RuneCountInString(address) > 500 {
			fail(c, errAddressTooLong)
			return
		}
		updates["address"] = address
	}
	if len(updates) > 0 {
		// The household written is the one the locked seat names, which
		// may differ from the one loaded before the transaction.
		var householdID uint
		if err := a.DB.Transaction(func(tx *gorm.DB) error {
			fresh, seat, err := lockHouseholdSeat(tx, apiUser(c), errHouseholdForbidden)
			if err != nil {
				return err
			}
			householdID = seat.HouseholdID
			if err := lockHousehold(tx, householdID); err != nil {
				return err
			}
			if err := tx.Model(&models.Household{}).Where("id = ?", householdID).Updates(updates).Error; err != nil {
				return err
			}
			fields := []string{}
			for key := range updates {
				fields = append(fields, key)
			}
			sort.Strings(fields)
			return audit.Write(tx, audit.Entry{Actor: &fresh.ID, Action: audit.HouseholdUpdated, TargetType: "household", TargetID: householdID, Meta: map[string]any{"fields": fields}, IP: c.ClientIP()})
		}); err != nil {
			fail(c, err)
			return
		}
		var saved models.Household
		if err := a.DB.First(&saved, householdID).Error; err != nil {
			jsonError(c, http.StatusInternalServerError, "could not load household")
			return
		}
		household = &saved
	}
	out, err := a.toHouseholdJSON(household, apiUser(c))
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load your household")
		return
	}
	c.JSON(http.StatusOK, gin.H{"household": out})
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

func (a *App) parseHouseholdPersonInput(c *gin.Context, body householdPersonInput) (models.HouseholdPerson, bool) {
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
		fail(c, errFirstNameRequired)
		return models.HouseholdPerson{}, false
	}
	if utf8.RuneCountInString(first) > 100 || utf8.RuneCountInString(last) > 100 {
		fail(c, errNameTooLong100)
		return models.HouseholdPerson{}, false
	}
	kind := ""
	if body.Kind != nil {
		kind = strings.TrimSpace(*body.Kind)
	}
	if !validHouseholdKind(kind) {
		fail(c, errInvalidKind)
		return models.HouseholdPerson{}, false
	}
	var birth *time.Time
	if body.BirthDate != nil && strings.TrimSpace(*body.BirthDate) != "" {
		parsed, err := accounts.ParseBirthDate(strings.TrimSpace(*body.BirthDate))
		if err != nil {
			fail(c, errInvalidBirthDate)
			return models.HouseholdPerson{}, false
		}
		birth = parsed
	}
	if birth != nil {
		today, err := churchClockIn(a.DB, time.Now().UTC())
		if err != nil {
			jsonError(c, http.StatusInternalServerError, "could not check the birth date")
			return models.HouseholdPerson{}, false
		}
		if age := accounts.AgeOn(*birth, today); age < 0 || (kind == models.HouseholdAdult && age < 18) {
			fail(c, errHouseholdAgeMismatch)
			return models.HouseholdPerson{}, false
		}
	}
	careNotes := ""
	if body.CareNotes != nil {
		careNotes = strings.TrimSpace(*body.CareNotes)
	}
	if utf8.RuneCountInString(careNotes) > 5000 {
		fail(c, errCareNotesTooLong)
		return models.HouseholdPerson{}, false
	}
	photoConsent := false
	if body.PhotoConsent != nil {
		photoConsent = *body.PhotoConsent
	}
	person := models.HouseholdPerson{
		FirstName: first, LastName: last, Kind: kind,
		BirthDate: birth, CareNotes: careNotes, PhotoConsent: photoConsent,
	}
	return person, true
}

// apiAddHouseholdPerson adds someone to the user's household (created on
// first use). A child or teen also gets a pending guardian link and the
// admins a waiting task (DEC-32); an adult needs no approval.
func (a *App) apiAddHouseholdPerson(c *gin.Context) {
	if _, ok := a.manageMyHousehold(c); !ok {
		return
	}
	me := apiUser(c)
	var body householdPersonInput
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	person, ok := a.parseHouseholdPersonInput(c, body)
	if !ok {
		return
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		// User lock first, then the household the locked seat names.
		fresh, seat, err := lockHouseholdSeat(tx, me, errHouseholdForbidden)
		if err != nil {
			return err
		}
		if err := lockHousehold(tx, seat.HouseholdID); err != nil {
			return err
		}
		person.HouseholdID, person.CreatedBy = seat.HouseholdID, fresh.ID
		if err := tx.Create(&person).Error; err != nil {
			return err
		}
		meta := map[string]any{"personId": person.ID, "kind": person.Kind}
		if policy.NeedsGuardianLink(person.Kind) {
			link, err := createGuardianLink(tx, fresh.ID, &person)
			if err != nil {
				return err
			}
			meta["guardianLinkId"] = link.ID
		}
		return audit.Write(tx, audit.Entry{Actor: &fresh.ID, Action: audit.HouseholdPersonAdded,
			TargetType: "household", TargetID: seat.HouseholdID, Meta: meta, IP: c.ClientIP()})
	})
	if err != nil {
		fail(c, err)
		return
	}
	row, err := a.householdPersonRow(a.DB, &person, me)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load that person")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"person": row})
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
	if person.ConvertedUserID != nil {
		fail(c, errPersonHasAccount)
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
			fail(c, errFirstNameRequired)
			return
		}
		if utf8.RuneCountInString(first) > 100 {
			fail(c, errNameTooLong100)
			return
		}
		updates["first_name"] = first
	}
	if body.LastName != nil {
		last := strings.TrimSpace(*body.LastName)
		if utf8.RuneCountInString(last) > 100 {
			fail(c, errNameTooLong100)
			return
		}
		updates["last_name"] = last
	}
	newKind := person.Kind
	if body.Kind != nil {
		newKind = strings.TrimSpace(*body.Kind)
		if !validHouseholdKind(newKind) {
			fail(c, errInvalidKind)
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
				fail(c, errInvalidBirthDate)
				return
			}
			updates["birth_date"] = parsed
		}
	}
	if body.CareNotes != nil {
		careNotes := strings.TrimSpace(*body.CareNotes)
		if utf8.RuneCountInString(careNotes) > 5000 {
			fail(c, errCareNotesTooLong)
			return
		}
		updates["care_notes"] = careNotes
	}
	if body.PhotoConsent != nil {
		updates["photo_consent"] = *body.PhotoConsent
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		// User lock first, then the household the locked seat names; the
		// person must be in it.
		_, seat, err := lockHouseholdSeat(tx, me, errHouseholdPersonGone)
		if err != nil {
			return err
		}
		if err := lockHousehold(tx, seat.HouseholdID); err != nil {
			return err
		}
		if err := tx.First(person, person.ID).Error; err != nil {
			return err
		}
		if person.HouseholdID != seat.HouseholdID {
			return errHouseholdPersonGone
		}
		if person.ConvertedUserID != nil {
			return errConversionUnavailable
		}
		material := (body.FirstName != nil && strings.TrimSpace(*body.FirstName) != person.FirstName) ||
			(body.LastName != nil && strings.TrimSpace(*body.LastName) != person.LastName) ||
			(body.Kind != nil && newKind != person.Kind) ||
			(body.BirthDate != nil && strings.TrimSpace(*body.BirthDate) != formatBirthDate(person.BirthDate))
		if material {
			staged, err := approvedConversionForPerson(tx, person.ID)
			if err != nil {
				return err
			}
			if staged {
				return errConversionConsent
			}
			if err := revokePersonConversion(tx, person.ID, me.ID, c.ClientIP()); err != nil {
				return err
			}
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
		if merged.BirthDate != nil {
			today, err := churchClockIn(tx, time.Now().UTC())
			if err != nil {
				return err
			}
			if age := accounts.AgeOn(*merged.BirthDate, today); age < 0 || (merged.Kind == models.HouseholdAdult && age < 18) {
				return householdRegistrationError{code: "household_age_mismatch", personID: person.ID}
			}
		}
		if merged.Kind == models.HouseholdAdult && merged.BirthDate == nil {
			var n int64
			if err := tx.Model(&models.Registration{}).Where("household_person_id = ? AND status IN ?", person.ID, models.CurrentRegStatuses).Count(&n).Error; err != nil {
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
			_, err := createGuardianLink(tx, me.ID, person)
			if err != nil {
				return err
			}
		}
		err = a.reconcileHouseholdPerson(tx, person, false, &me.ID, c.ClientIP())
		if err != nil {
			return err
		}
		fields := []string{}
		for key := range updates {
			fields = append(fields, key)
		}
		sort.Strings(fields)
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.HouseholdPersonUpdated, TargetType: "household", TargetID: person.HouseholdID, Meta: map[string]any{"personId": person.ID, "fields": fields}, IP: c.ClientIP()})
	})
	if err != nil {
		fail(c, err)
		return
	}
	var fresh models.HouseholdPerson
	if err := a.DB.First(&fresh, person.ID).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load that person")
		return
	}
	row, err := a.householdPersonRow(a.DB, &fresh, me)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load that person")
		return
	}
	c.JSON(http.StatusOK, gin.H{"person": row})
}

// apiDeleteHouseholdPerson removes a person from the user's household:
// a soft delete, with live guardian links revoked and any waiting admin
// task resolved withdrawn, audited in the same transaction.
func (a *App) apiDeleteHouseholdPerson(c *gin.Context) {
	_, person, ok := a.loadMyHouseholdPerson(c)
	if !ok {
		return
	}
	me := apiUser(c)
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		// User lock first, then the household the locked seat names; the
		// person must be in it.
		_, seat, err := lockHouseholdSeat(tx, me, errHouseholdPersonGone)
		if err != nil {
			return err
		}
		if err := lockHousehold(tx, seat.HouseholdID); err != nil {
			return err
		}
		if err := tx.First(person, person.ID).Error; err != nil {
			return err
		}
		if person.HouseholdID != seat.HouseholdID {
			return errHouseholdPersonGone
		}
		if person.ConvertedUserID != nil {
			return errConversionUnavailable
		}
		staged, err := approvedConversionForPerson(tx, person.ID)
		if err != nil {
			return err
		}
		if staged {
			return errConversionConsent
		}
		if err := revokePersonConversion(tx, person.ID, me.ID, c.ClientIP()); err != nil {
			return err
		}
		err = a.reconcileHouseholdPerson(tx, person, true, &me.ID, c.ClientIP())
		if err != nil {
			return err
		}
		if err := revokeGuardianLinks(tx, c, me.ID, person); err != nil {
			return err
		}
		if err := audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.HouseholdPersonRemoved,
			TargetType: "household", TargetID: seat.HouseholdID,
			Meta: map[string]any{"personId": person.ID, "kind": person.Kind}, IP: c.ClientIP()}); err != nil {
			return err
		}
		return tx.Delete(person).Error
	})
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}
