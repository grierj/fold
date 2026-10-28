package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var (
	errConsentForbidden    = errors.New("forbidden")
	errConsentTeen         = errors.New("consent is only available for teen accounts; refresh the user list")
	errConsentEvidence     = errors.New("record the consent evidence before granting consent")
	errConsentInvalid      = errors.New("choose account, sms or photos and granted or revoked")
	errConsentEvidenceLong = errors.New("consent evidence must be at most 1000 characters")
)

type teenConsentJSON struct {
	ID           uint      `json:"id"`
	TeenUserID   uint      `json:"teenUserId"`
	Scope        string    `json:"scope"`
	Status       string    `json:"status"`
	GrantedBy    uint      `json:"grantedBy"`
	GranterRole  string    `json:"granterRole"`
	EvidenceText string    `json:"evidenceText"`
	At           time.Time `json:"at"`
}

func consentJSON(row models.TeenConsent) teenConsentJSON {
	return teenConsentJSON{ID: row.ID, TeenUserID: row.TeenUserID, Scope: row.Scope, Status: row.Status,
		GrantedBy: row.GrantedBy, GranterRole: row.GranterRole, EvidenceText: row.EvidenceText, At: row.At}
}

// latestTeenConsentIn must be read after the teen user lock when deciding
// status. The user row serializes appends even when no consent exists yet.
func latestTeenConsentIn(tx *gorm.DB, teenID uint, scope string) (bool, error) {
	var row models.TeenConsent
	err := tx.Where("teen_user_id = ? AND scope = ?", teenID, scope).Order("at DESC, id DESC").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return row.Status == models.ConsentGranted, err
}

// lockConsentUsers follows the registration/conversion lifecycle lock order:
// users by ID first, then guardian/household rows, groups, and occurrences.
func lockConsentUsers(tx *gorm.DB, actorID, teenID uint) (*models.User, *models.User, error) {
	users, err := lockTeenLifecycleUsers(tx, teenID, actorID)
	if err != nil {
		return nil, nil, err
	}
	actor, teen := users[actorID], users[teenID]
	if actor == nil {
		return nil, nil, errConsentForbidden
	}
	if teen == nil {
		return nil, nil, gorm.ErrRecordNotFound
	}
	return actor, teen, nil
}

type teenConsentChange struct {
	Scope        string
	Status       string
	EvidenceText string
	// The caller must load the guardian link under a lock after locking users.
	ConfirmedGuardian bool
}

// changeTeenConsent is shared by admin, guardian, email and approval paths.
// The caller holds the actor and teen user locks, rechecks authentication,
// and supplies any guardian authority from a locked, confirmed link. It
// returns notification IDs for delivery only after the transaction commits.
func (a *App) changeTeenConsent(tx *gorm.DB, c *gin.Context, teen, actor *models.User, change teenConsentChange) ([]uint, error) {
	if actor.Status != models.UserActive || !policy.CanChangeTeenConsent(asPerson(actor), change.ConfirmedGuardian) {
		return nil, errConsentForbidden
	}
	if teen.AccountType != models.AccountTeen || teen.DeletedAt.Valid {
		return nil, errConsentTeen
	}
	if (change.Scope != models.TeenConsentAccount && change.Scope != models.TeenConsentSMS && change.Scope != models.TeenConsentPhotos) ||
		(change.Status != models.ConsentGranted && change.Status != models.ConsentRevoked) {
		return nil, errConsentInvalid
	}
	evidence := strings.TrimSpace(change.EvidenceText)
	if len(evidence) > 1000 {
		return nil, errConsentEvidenceLong
	}
	role := models.ConsentGuardian
	if policy.IsAdmin(asPerson(actor)) && !change.ConfirmedGuardian {
		role = models.ConsentAdmin
		if change.Status == models.ConsentGranted && evidence == "" {
			return nil, errConsentEvidence
		}
	}
	row := models.TeenConsent{TeenUserID: teen.ID, Scope: change.Scope, Status: change.Status,
		GrantedBy: actor.ID, GranterRole: role, EvidenceText: evidence}
	if err := tx.Create(&row).Error; err != nil {
		return nil, err
	}
	var mailed []uint
	if change.Scope == models.TeenConsentAccount {
		status := teen.Status
		if change.Status == models.ConsentRevoked {
			status = models.UserDisabled
		} else if !teen.AdministrativelyDisabled {
			status = models.UserActive
		}
		if status != teen.Status {
			// Restore while the row still has disabled status, so its kept
			// memberships and seats are not counted twice. Pending teens are
			// already listed, so their initial grant needs no restore.
			if teen.Status == models.UserDisabled && status != models.UserDisabled {
				ids, err := restoreMemberships(tx, teen.ID, actor.ID, c.ClientIP())
				if err != nil {
					return nil, err
				}
				mailed = append(mailed, ids...)
				ids, err = a.restoreRegistrations(tx, teen.ID, status, actor.ID, c.ClientIP(), time.Now().UTC())
				if err != nil {
					return nil, err
				}
				mailed = append(mailed, ids...)
			}
			// Every account-status transition ends old sessions, including
			// legacy pending cookies. A newly consented teen logs in afresh.
			updates := map[string]any{"status": status, "session_version": gorm.Expr("session_version + 1")}
			if err := tx.Model(teen).Updates(updates).Error; err != nil {
				return nil, err
			}
			if status == models.UserDisabled {
				ids, err := a.promoteOnDisable(tx, teen.ID)
				if err != nil {
					return nil, err
				}
				mailed = append(mailed, ids...)
			}
			teen.Status = status
		}
	}
	if change.Scope == models.TeenConsentAccount {
		if err := a.completeTeenConversionIfReady(tx, teen, a.householdClock(time.Now().UTC()), c.ClientIP(), &mailed); err != nil {
			return nil, err
		}
	}
	action := audit.TeenConsentGranted
	if change.Status == models.ConsentRevoked {
		action = audit.TeenConsentRevoked
	}
	if err := audit.Write(tx, audit.Entry{Actor: &actor.ID, Action: action, TargetType: "teen_consent", TargetID: row.ID,
		Meta: map[string]any{"teenUserId": teen.ID, "scope": change.Scope, "status": change.Status, "granterRole": role, "evidenceText": evidence}, IP: c.ClientIP()}); err != nil {
		return nil, err
	}
	// Evidence stays in the admin history and audit. Notifications contain no
	// birth date, contact information or consent evidence.
	var admins []uint
	if err := tx.Model(&models.User{}).Where("role = ? AND status = ?", models.RoleAdmin, models.UserActive).Pluck("id", &admins).Error; err != nil {
		return nil, err
	}
	ids, err := notify.Send(tx, notify.Msg{Kind: notify.TeenConsentChanged, To: admins, Actor: &actor.ID,
		SubjectType: "teen_consent", SubjectID: row.ID, Link: "/admin?tab=users",
		Vars: notify.Vars{Person: teen.PublicName(), Teen: true, Change: change.Scope + " consent " + change.Status}})
	mailed = append(mailed, ids...)
	if err != nil {
		return nil, err
	}
	var family []uint
	if err := tx.Model(&models.GuardianLink{}).
		Joins("JOIN users ON users.id = guardian_links.guardian_user_id AND users.deleted_at IS NULL AND users.status = ?", models.UserActive).
		Where("teen_user_id = ? AND guardian_links.status = ?", teen.ID, models.GuardianConfirmed).
		Pluck("guardian_user_id", &family).Error; err != nil {
		return nil, err
	}
	if teen.Status == models.UserActive {
		family = append(family, teen.ID)
	}
	adminSet := make(map[uint]bool, len(admins))
	for _, id := range admins {
		adminSet[id] = true
	}
	familyOnly := family[:0]
	for _, id := range family {
		if !adminSet[id] {
			familyOnly = append(familyOnly, id)
		}
	}
	ids, err = notify.Send(tx, notify.Msg{Kind: notify.TeenConsentChanged, To: familyOnly, Actor: &actor.ID,
		SubjectType: "teen_consent", SubjectID: row.ID, Link: "/profile",
		Vars: notify.Vars{Person: teen.PublicName(), Teen: true, Change: change.Scope + " consent " + change.Status}})
	mailed = append(mailed, ids...)
	return mailed, err
}

func consentError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, errConversionRetry), errors.Is(err, errConversionHolder), errors.Is(err, errConversionDetails):
		conversionError(c, err)
	case errors.Is(err, errConsentForbidden):
		jsonError(c, http.StatusForbidden, err.Error())
	case errors.Is(err, gorm.ErrRecordNotFound):
		jsonError(c, http.StatusNotFound, "user not found")
	case errors.Is(err, errConsentTeen):
		jsonError(c, http.StatusConflict, err.Error())
	case errors.Is(err, errConsentEvidence), errors.Is(err, errConsentEvidenceLong), errors.Is(err, errConsentInvalid):
		jsonError(c, http.StatusBadRequest, err.Error())
	default:
		jsonError(c, http.StatusInternalServerError, "could not save teen consent")
	}
}

func (a *App) apiListTeenConsents(c *gin.Context) {
	u, ok := a.adminTargetUser(c)
	if !ok {
		return
	}
	var rows []models.TeenConsent
	if err := a.DB.Where("teen_user_id = ?", u.ID).Order("at DESC, id DESC").Find(&rows).Error; err != nil {
		consentError(c, err)
		return
	}
	consents := make([]teenConsentJSON, 0, len(rows))
	for _, row := range rows {
		consents = append(consents, consentJSON(row))
	}
	c.JSON(http.StatusOK, gin.H{"consents": consents, "administrativelyDisabled": u.AdministrativelyDisabled})
}

func (a *App) apiChangeTeenConsent(c *gin.Context) {
	var body struct {
		Scope        string `json:"scope"`
		Status       string `json:"status"`
		EvidenceText string `json:"evidenceText"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		jsonError(c, http.StatusNotFound, "user not found")
		return
	}
	me := apiUser(c)
	var mailed []uint
	err = a.contactMutation(contactChange{UserID: uint(id)}, func(tx *gorm.DB) error {
		actor, teen, err := lockConsentUsers(tx, me.ID, uint(id))
		if err != nil {
			return err
		}
		if !policy.IsAdmin(asPerson(actor)) || actor.SessionVersion != me.SessionVersion {
			return errConsentForbidden
		}
		mailed, err = a.changeTeenConsent(tx, c, teen, actor, teenConsentChange{Scope: body.Scope, Status: body.Status, EvidenceText: body.EvidenceText})
		return err
	})
	if err != nil {
		consentError(c, err)
		return
	}
	a.deliverNotificationEmails(mailed)
	c.JSON(http.StatusOK, gin.H{})
}
