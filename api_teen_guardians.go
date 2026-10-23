package main

import (
	"errors"
	"log"
	"net/http"
	"sort"
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
	FROM account_requests r JOIN users u ON u.email = r.email AND u.deleted_at IS NULL
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
			guardian, target, err := lockGuardianUsers(tx, me.ID, teen.ID)
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
func lockGuardianUsers(tx *gorm.DB, guardianID, teenID uint) (*models.User, *models.User, error) {
	ids := []uint{guardianID, teenID}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var users []models.User
	if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).Where("id IN ?", ids).Order("id").Find(&users).Error; err != nil {
		return nil, nil, err
	}
	var guardian, teen *models.User
	for i := range users {
		if users[i].ID == guardianID {
			guardian = &users[i]
		}
		if users[i].ID == teenID {
			teen = &users[i]
		}
	}
	if guardian == nil || teen == nil || guardianID == teenID {
		return nil, nil, errGuardianUnavailable
	}
	return guardian, teen, nil
}

func lockedTeenGuardian(tx *gorm.DB, guardianID, teenID, linkID uint) (*models.User, *models.User, *models.GuardianLink, error) {
	guardian, teen, err := lockGuardianUsers(tx, guardianID, teenID)
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
