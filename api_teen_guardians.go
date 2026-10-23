package main

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"fold/internal/audit"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const guardianClaimThanks = "If that teen has an account, an admin will review your guardian request."

var errGuardianUnavailable = errors.New("guardian authority is unavailable")

type guardianNominationJSON struct {
	RequestID     uint   `json:"requestId"`
	TeenUserID    uint   `json:"teenUserId"`
	TeenName      string `json:"teenName"`
	GuardianEmail string `json:"guardianEmail"`
}

func unresolvedGuardianNominations(db *gorm.DB) ([]guardianNominationJSON, error) {
	rows := make([]guardianNominationJSON, 0)
	err := db.Raw(`SELECT r.id AS request_id, u.id AS teen_user_id,
	COALESCE(NULLIF(u.display_name, ''), NULLIF(u.name, ''), 'Member') AS teen_name, r.guardian_email
	FROM account_requests r JOIN users u ON u.id = r.approved_user_id AND u.deleted_at IS NULL
	WHERE r.status = 'approved' AND r.guardian_email <> '' AND u.account_type = 'teen'
	AND NOT EXISTS (SELECT 1 FROM guardian_links l JOIN users g ON g.id = l.guardian_user_id
	WHERE l.teen_user_id = u.id AND g.email = r.guardian_email::citext)
	ORDER BY r.created_at, r.id`).Scan(&rows).Error
	return rows, err
}

// A nomination is contact information on an approved request, never authority.
// Unknown addresses remain on the request so the admin can help the adult join.
func nominateTeenGuardian(tx *gorm.DB, req *models.AccountRequest, teenID uint) ([]uint, error) {
	if req.AccountType != models.AccountTeen || req.GuardianEmail == "" {
		return nil, nil
	}
	var guardian models.User
	res := tx.Where("email = ? AND account_type = ? AND status = ?", req.GuardianEmail, models.AccountAdult, models.UserActive).Limit(1).Find(&guardian)
	if res.Error != nil || res.RowsAffected == 0 {
		return nil, res.Error
	}
	var teen models.User
	if err := tx.First(&teen, teenID).Error; err != nil {
		return nil, err
	}
	if teen.AccountType != models.AccountTeen || guardian.ID == teen.ID {
		return nil, nil
	}
	return requestTeenGuardian(tx, guardian.ID, teen.ID, nil, "signup", req.IP)
}

func requestTeenGuardian(tx *gorm.DB, guardianID, teenID uint, actor *uint, source, ip string) ([]uint, error) {
	link := models.GuardianLink{GuardianUserID: guardianID, TeenUserID: &teenID, Status: models.GuardianPending}
	res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&link)
	if res.Error != nil || res.RowsAffected == 0 {
		return nil, res.Error
	}
	if err := audit.Write(tx, audit.Entry{Actor: actor, Action: audit.GuardianLinkRequested, TargetType: "guardian_link", TargetID: link.ID,
		Meta: map[string]any{"guardianUserId": guardianID, "teenUserId": teenID, "source": source}, IP: ip}); err != nil {
		return nil, err
	}
	return notifyGuardianLinkPending(tx, &link, nil)
}

func (a *App) apiClaimTeenGuardian(c *gin.Context) {
	me := apiUser(c)
	if !policy.CanRequestGuardianLink(asPerson(me)) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || !validEmail(normalizeEmail(body.Email)) {
		jsonError(c, http.StatusBadRequest, "enter a valid email address")
		return
	}
	email := normalizeEmail(body.Email)
	hits, err := a.throttleHit(c.Request.Context(), "guardian:ip:"+c.ClientIP(), time.Hour)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save your request")
		return
	}
	allowed, err := a.targetAllow(c.Request.Context(), "guardian:addr:"+email)
	if err != nil || !allowed || hits > accountRequestIPLimit {
		log.Printf("guardian claim limited: actor=%d err=%v", me.ID, err)
		c.JSON(http.StatusOK, gin.H{"message": guardianClaimThanks})
		return
	}
	var teen models.User
	res := a.DB.Where("email = ? AND account_type = ?", email, models.AccountTeen).Limit(1).Find(&teen)
	var mailed []uint
	if res.Error == nil && res.RowsAffected == 1 && teen.ID != me.ID {
		err = a.DB.Transaction(func(tx *gorm.DB) error {
			guardian, target, err := lockConsentUsers(tx, me.ID, teen.ID)
			if err != nil {
				return err
			}
			if guardian.Status != models.UserActive || guardian.SessionVersion != me.SessionVersion || !policy.CanRequestGuardianLink(asPerson(guardian)) || target.AccountType != models.AccountTeen {
				return errGuardianUnavailable
			}
			mailed, err = requestTeenGuardian(tx, me.ID, teen.ID, &me.ID, "claim", c.ClientIP())
			return err
		})
	} else {
		err = res.Error
	}
	if err != nil {
		log.Printf("guardian claim: actor=%d err=%v", me.ID, err)
	}
	a.deliverNotificationEmails(mailed)
	c.JSON(http.StatusOK, gin.H{"message": guardianClaimThanks})
}

// Consent and link changes share this order: both users by ID, then link.
func lockedTeenGuardian(tx *gorm.DB, guardianID, teenID, linkID uint) (*models.User, *models.User, *models.GuardianLink, error) {
	guardian, teen, err := lockConsentUsers(tx, guardianID, teenID)
	if err != nil {
		return nil, nil, nil, err
	}
	var link models.GuardianLink
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND guardian_user_id = ? AND teen_user_id = ?", linkID, guardianID, teenID).First(&link).Error; err != nil {
		return nil, nil, nil, errGuardianUnavailable
	}
	if guardian.Status != models.UserActive || !policy.CanRequestGuardianLink(asPerson(guardian)) || teen.AccountType != models.AccountTeen || link.Status != models.GuardianConfirmed {
		return nil, nil, nil, errGuardianUnavailable
	}
	return guardian, teen, &link, nil
}

type guardianTeenJSON struct {
	LinkID         uint   `json:"linkId"`
	TeenUserID     uint   `json:"teenUserId"`
	TeenName       string `json:"teenName"`
	AccountConsent bool   `json:"accountConsent"`
	SMSConsent     bool   `json:"smsConsent"`
	PhotosConsent  bool   `json:"photosConsent"`
}

func (a *App) apiMyGuardianTeens(c *gin.Context) {
	me := apiUser(c)
	if !policy.CanRequestGuardianLink(asPerson(me)) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return
	}
	var rows []struct {
		LinkID      uint
		TeenUserID  uint
		Name        string
		DisplayName string
	}
	if err := a.DB.Table("guardian_links l").Select("l.id AS link_id, t.id AS teen_user_id, t.name, t.display_name").
		Joins("JOIN users t ON t.id = l.teen_user_id AND t.deleted_at IS NULL AND t.account_type = ?", models.AccountTeen).
		Where("l.guardian_user_id = ? AND l.status = ?", me.ID, models.GuardianConfirmed).Order("l.id").Scan(&rows).Error; err != nil {
		consentError(c, err)
		return
	}
	out := make([]guardianTeenJSON, len(rows))
	ids := make([]uint, 0, len(rows))
	byTeen := make(map[uint]*guardianTeenJSON, len(rows))
	for i, row := range rows {
		person := models.User{Name: row.Name, DisplayName: row.DisplayName}
		out[i] = guardianTeenJSON{LinkID: row.LinkID, TeenUserID: row.TeenUserID, TeenName: person.PublicName()}
		byTeen[row.TeenUserID] = &out[i]
		ids = append(ids, row.TeenUserID)
	}
	var latest []models.TeenConsent
	if len(ids) > 0 {
		if err := a.DB.Raw("SELECT DISTINCT ON (teen_user_id, scope) * FROM teen_consents WHERE teen_user_id IN ? ORDER BY teen_user_id, scope, at DESC, id DESC", ids).Scan(&latest).Error; err != nil {
			consentError(c, err)
			return
		}
	}
	for _, consent := range latest {
		row := byTeen[consent.TeenUserID]
		granted := consent.Status == models.ConsentGranted
		switch consent.Scope {
		case models.TeenConsentAccount:
			row.AccountConsent = granted
		case models.TeenConsentSMS:
			row.SMSConsent = granted
		case models.TeenConsentPhotos:
			row.PhotosConsent = granted
		}
	}
	c.JSON(http.StatusOK, gin.H{"teens": out})
}

func ownTeenLink(db *gorm.DB, c *gin.Context) (*models.GuardianLink, error) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		return nil, errGuardianUnavailable
	}
	var link models.GuardianLink
	err = db.Where("id = ? AND guardian_user_id = ? AND teen_user_id IS NOT NULL", id, apiUser(c).ID).First(&link).Error
	return &link, err
}

func (a *App) apiGuardianChangeConsent(c *gin.Context) {
	link, err := ownTeenLink(a.DB, c)
	if err != nil {
		jsonError(c, http.StatusForbidden, "forbidden")
		return
	}
	var body struct {
		Scope  string `json:"scope"`
		Status string `json:"status"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	me := apiUser(c)
	var mailed []uint
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		guardian, teen, _, err := lockedTeenGuardian(tx, me.ID, *link.TeenUserID, link.ID)
		if err != nil || guardian.SessionVersion != me.SessionVersion {
			return errConsentForbidden
		}
		mailed, err = a.changeTeenConsent(tx, c, teen, guardian, teenConsentChange{Scope: body.Scope, Status: body.Status, ConfirmedGuardian: true})
		return err
	})
	if err != nil {
		consentError(c, err)
		return
	}
	a.deliverNotificationEmails(mailed)
	c.JSON(http.StatusOK, gin.H{})
}
