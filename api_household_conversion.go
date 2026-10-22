package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"time"

	"fold/internal/audit"
	"fold/internal/mail"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	errConversionUnavailable = errors.New("this person cannot create an account yet; check their birth date, account type and guardian link")
	errConversionEmail       = errors.New("that email is unavailable; use a different email")
	errConversionOpen        = errors.New("cancel the current invitation before choosing a different email")
	errConversionHolder      = errors.New("a responsible account is unavailable; ask your church office to check it")
	errConversionRetry       = errors.New("household changed while locking")
)

type conversionJSON struct {
	ID        uint      `json:"id"`
	Email     string    `json:"email"`
	ExpiresAt time.Time `json:"expiresAt"`
	State     string    `json:"state"`
}

func conversionMetadata(inv *models.Invitation) conversionJSON {
	state := "pending"
	if !inv.ExpiresAt.After(time.Now()) {
		state = "expired"
	}
	return conversionJSON{ID: inv.ID, Email: inv.Email, ExpiresAt: inv.ExpiresAt, State: state}
}

// Ordinary and household invitations reserve the same email namespace.
func lockInvitationEmail(tx *gorm.DB, email string) error {
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 7316193))", normalizeEmail(email)).Error
}

func conversionEmailAvailable(tx *gorm.DB, email string, except uint) error {
	var n int64
	if err := tx.Unscoped().Model(&models.User{}).Where("email = ?", email).Count(&n).Error; err != nil {
		return err
	}
	if n != 0 {
		return errConversionEmail
	}
	if err := tx.Model(&models.Invitation{}).Where("email = ? AND id <> ? AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()", email, except).Count(&n).Error; err != nil {
		return err
	}
	if n != 0 {
		return errConversionEmail
	}
	return nil
}

func conversionRelation(tx *gorm.DB, actor uint, household uint) policy.HouseholdRel {
	acct, found := householdAccountIn(tx, actor)
	return householdRelOf(acct, found && acct.HouseholdID == household)
}

func conversionGuardians(tx *gorm.DB, person *models.HouseholdPerson) ([]uint, error) {
	var ids []uint
	err := tx.Model(&models.GuardianLink{}).
		Joins("JOIN users ON users.id = guardian_links.guardian_user_id AND users.deleted_at IS NULL AND users.status = 'active' AND users.account_type = 'adult'").
		Joins("JOIN household_accounts ha ON ha.user_id = users.id AND ha.household_id = ? AND ha.relation IN ('adult','guardian')", person.HouseholdID).
		Where("guardian_links.person_id = ? AND guardian_links.status IN ?", person.ID, []string{models.GuardianPending, models.GuardianConfirmed}).
		Distinct("guardian_links.guardian_user_id").Pluck("guardian_links.guardian_user_id", &ids).Error
	return ids, err
}

func (a *App) conversionAllowed(tx *gorm.DB, actor *models.User, person *models.HouseholdPerson, today time.Time) (bool, error) {
	guardians, err := conversionGuardians(tx, person)
	if err != nil {
		return false, err
	}
	var unavailable int64
	if err := tx.Model(&models.Registration{}).Joins("JOIN users ON users.id = registrations.registered_by").Where("registrations.household_person_id = ? AND (users.deleted_at IS NOT NULL OR users.status <> ?)", person.ID, models.UserActive).Count(&unavailable).Error; err != nil {
		return false, err
	}
	if unavailable > 0 {
		return false, nil
	}
	return policy.CanConvertHouseholdPerson(asPerson(actor), conversionRelation(tx, actor.ID, person.HouseholdID), householdFacts(person, nil, today), actor.Status == models.UserActive && !actor.DeletedAt.Valid, len(guardians) > 0), nil
}

// Conversion loaders find the target first so admins can act across households.
// Household authority is rechecked inside every mutation's household lock.
func (a *App) loadConversionPerson(c *gin.Context) (*models.HouseholdPerson, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		jsonError(c, http.StatusNotFound, "household person not found")
		return nil, false
	}
	var person models.HouseholdPerson
	if err := a.DB.First(&person, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "household person not found")
		return nil, false
	}
	me := apiUser(c)
	if !policy.CanCancelHouseholdConversion(asPerson(me), conversionRelation(a.DB, me.ID, person.HouseholdID), me.Status == models.UserActive) {
		jsonError(c, http.StatusNotFound, "household person not found")
		return nil, false
	}
	return &person, true
}

func conversionError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, errConversionUnavailable), errors.Is(err, errConversionEmail), errors.Is(err, errConversionOpen), errors.Is(err, errConversionHolder):
		jsonError(c, http.StatusConflict, err.Error())
	case errors.Is(err, errTokenInvalid), errors.Is(err, gorm.ErrRecordNotFound):
		jsonError(c, http.StatusBadRequest, "that invitation is invalid or has expired")
	case emailTaken(err):
		jsonError(c, http.StatusConflict, errConversionEmail.Error())
	default:
		log.Printf("household conversion: %v", err)
		jsonError(c, http.StatusInternalServerError, "could not save household invitation")
	}
}

// Conversion mail uses the invitation limit, including resends. Audits are
// durable counters; the actor advisory lock serializes different households.
func conversionSendAllowed(tx *gorm.DB, actor uint, email string) (bool, error) {
	if err := tx.Exec("SELECT pg_advisory_xact_lock(7316194, ?)", actor).Error; err != nil {
		return false, err
	}
	var byActor, byEmail int64
	kinds := []string{audit.InvitationCreated, audit.InvitationResent}
	if err := tx.Model(&models.AuditLog{}).Where("actor_id = ? AND action IN ? AND at > now() - interval '24 hours'", actor, kinds).Count(&byActor).Error; err != nil {
		return false, err
	}
	// Join the invitation instead of putting destination email in conversion audits.
	if err := tx.Model(&models.AuditLog{}).Joins("JOIN invitations ON invitations.id = audit_logs.target_id AND audit_logs.target_type = 'invitation'").Where("invitations.email = ? AND audit_logs.action IN ? AND audit_logs.at > now() - interval '24 hours'", email, kinds).Count(&byEmail).Error; err != nil {
		return false, err
	}
	return byActor < leaderInviteDailyLimit && byEmail < leaderInviteDailyLimit, nil
}

func (a *App) apiCreateHouseholdConversion(c *gin.Context) { a.issueHouseholdConversion(c, false) }
func (a *App) apiResendHouseholdConversion(c *gin.Context) { a.issueHouseholdConversion(c, true) }

func (a *App) issueHouseholdConversion(c *gin.Context, resend bool) {
	person, ok := a.loadConversionPerson(c)
	if !ok {
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if !resend {
		if c.ShouldBindJSON(&body) != nil {
			jsonError(c, http.StatusBadRequest, "invalid request body")
			return
		}
		body.Email = normalizeEmail(body.Email)
		if !validEmail(body.Email) {
			jsonError(c, http.StatusBadRequest, "enter a valid email address")
			return
		}
	}
	raw, hash, err := createInvitationToken()
	if err != nil {
		conversionError(c, err)
		return
	}
	var inv models.Invitation
	actor := *apiUser(c)
	send := false
	today := a.householdClock(time.Now().UTC())
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&actor, actor.ID).Error; err != nil {
			return err
		}
		if err := lockHousehold(tx, person.HouseholdID); err != nil {
			return err
		}
		if err := tx.First(person, person.ID).Error; err != nil {
			return err
		}
		allowed, err := a.conversionAllowed(tx, &actor, person, today)
		if err != nil {
			return err
		}
		if !allowed {
			return errConversionUnavailable
		}
		result := tx.Where("household_person_id = ? AND accepted_at IS NULL AND revoked_at IS NULL", person.ID).First(&inv)
		if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return result.Error
		}
		if resend && inv.ID == 0 {
			return errTokenInvalid
		}
		if inv.ID != 0 {
			if !resend && inv.Email != body.Email {
				return errConversionOpen
			}
			body.Email = inv.Email
		}
		if err := lockInvitationEmail(tx, body.Email); err != nil {
			return err
		}
		if err := conversionEmailAvailable(tx, body.Email, inv.ID); err != nil {
			return err
		}
		if !resend && inv.ID != 0 && inv.ExpiresAt.After(time.Now()) {
			return nil
		}
		allowed, err = conversionSendAllowed(tx, actor.ID, body.Email)
		if err != nil {
			return err
		}
		if !allowed {
			// Keep the normal response shape without creating a deliverable token.
			log.Printf("household invitation send suppressed for actor %d", actor.ID)
			if inv.ID == 0 {
				inv = models.Invitation{Email: body.Email, ExpiresAt: time.Now().UTC().Add(tokenTTL)}
			}
			return nil
		}
		if !resend && inv.ID != 0 {
			if err := revokeConversionInvitation(tx, &inv, actor.ID, c.ClientIP()); err != nil {
				return err
			}
			inv = models.Invitation{}
		}
		action := audit.InvitationCreated
		if inv.ID == 0 {
			inv = models.Invitation{HouseholdPersonID: &person.ID, Email: body.Email, Name: person.FullName(), InvitedBy: actor.ID, Role: models.MembershipMember, AccountType: person.Kind, TokenHash: hash, ExpiresAt: time.Now().UTC().Add(tokenTTL)}
			if err := tx.Create(&inv).Error; err != nil {
				return err
			}
		} else {
			action = audit.InvitationResent
			inv.TokenHash, inv.ExpiresAt, inv.InvitedBy = hash, time.Now().UTC().Add(tokenTTL), actor.ID
			if err := tx.Model(&inv).Updates(map[string]any{"token_hash": hash, "expires_at": inv.ExpiresAt, "invited_by": actor.ID}).Error; err != nil {
				return err
			}
		}
		send = true
		return audit.Write(tx, audit.Entry{Actor: &actor.ID, Action: action, TargetType: "invitation", TargetID: inv.ID, Meta: map[string]any{"personId": person.ID, "householdId": person.HouseholdID}, IP: c.ClientIP()})
	})
	if err != nil {
		conversionError(c, err)
		return
	}
	if send {
		if err := a.sendConversionEmail(&inv, &actor, raw); err != nil {
			log.Printf("household invitation delivery failed for invitation %d: %v", inv.ID, err)
		}
	}
	status := http.StatusCreated
	if resend {
		status = http.StatusOK
	}
	c.JSON(status, gin.H{"conversion": conversionMetadata(&inv)})
}

func revokeConversionInvitation(tx *gorm.DB, inv *models.Invitation, actor uint, ip string) error {
	result := tx.Model(inv).Where("accepted_at IS NULL AND revoked_at IS NULL").Update("revoked_at", time.Now().UTC())
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return nil
	}
	return audit.Write(tx, audit.Entry{Actor: &actor, Action: audit.InvitationRevoked, TargetType: "invitation", TargetID: inv.ID, Meta: map[string]any{"personId": inv.HouseholdPersonID}, IP: ip})
}

func revokePersonConversion(tx *gorm.DB, person uint, actor uint, ip string) error {
	var inv models.Invitation
	err := tx.Where("household_person_id = ? AND accepted_at IS NULL AND revoked_at IS NULL", person).First(&inv).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return revokeConversionInvitation(tx, &inv, actor, ip)
}

func (a *App) apiCancelHouseholdConversion(c *gin.Context) {
	person, ok := a.loadConversionPerson(c)
	if !ok {
		return
	}
	actor := *apiUser(c)
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&actor, actor.ID).Error; err != nil {
			return err
		}
		if err := lockHousehold(tx, person.HouseholdID); err != nil {
			return err
		}
		if !policy.CanCancelHouseholdConversion(asPerson(&actor), conversionRelation(tx, actor.ID, person.HouseholdID), actor.Status == models.UserActive) {
			return errConversionUnavailable
		}
		return revokePersonConversion(tx, person.ID, actor.ID, c.ClientIP())
	})
	if err != nil {
		conversionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

func (a *App) sendConversionEmail(inv *models.Invitation, actor *models.User, raw string) error {
	text := fmt.Sprintf("%s invited you to create an account for %s. When you accept, your past and upcoming event sign-ups will move to your account.\n\nChoose your password: %s/invite?token=%s\n\nThis link expires in 24 hours. If this invitation is not for you, ignore it.", actor.PublicName(), inv.Name, a.emailBaseURL(), raw)
	if inv.AccountType == models.AccountTeen {
		text += "\n\nYour account will wait for consent. An admin must also check your guardian link. You cannot sign in yet."
	}
	return a.Mailer.Send(context.Background(), mail.Message{To: inv.Email, Subject: "Create your own account at " + a.siteName(), Text: text})
}

// Discover every earlier-order user lock before taking the household lock.
func conversionLockIDs(tx *gorm.DB, inv *models.Invitation, person *models.HouseholdPerson) ([]uint, error) {
	ids := []uint{inv.InvitedBy}
	var found []uint
	if err := tx.Model(&models.HouseholdAccount{}).Where("household_id = ?", person.HouseholdID).Pluck("user_id", &found).Error; err != nil {
		return nil, err
	}
	ids = append(ids, found...)
	if err := tx.Model(&models.GuardianLink{}).Where("person_id = ?", person.ID).Pluck("guardian_user_id", &found).Error; err != nil {
		return nil, err
	}
	ids = append(ids, found...)
	if err := tx.Model(&models.Registration{}).Where("household_person_id = ?", person.ID).Distinct("registered_by").Pluck("registered_by", &found).Error; err != nil {
		return nil, err
	}
	ids = append(ids, found...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	unique := ids[:0]
	for _, id := range ids {
		if id != 0 && (len(unique) == 0 || unique[len(unique)-1] != id) {
			unique = append(unique, id)
		}
	}
	return unique, nil
}

func (a *App) acceptHouseholdConversion(c *gin.Context, raw, password string, initial *models.Invitation) {
	// Password hashing never holds database locks.
	prepared := models.User{}
	if err := prepared.SetPassword(password); err != nil {
		conversionError(c, err)
		return
	}
	var user models.User
	var mailed []uint
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		mailed = nil
		user = models.User{}
		err = a.DB.Transaction(func(tx *gorm.DB) error {
			discovery, err := liveInvitation(tx, raw)
			if err != nil || discovery.HouseholdPersonID == nil {
				return errTokenInvalid
			}
			initial = discovery
			var person models.HouseholdPerson
			if err := tx.First(&person, *initial.HouseholdPersonID).Error; err != nil {
				return errTokenInvalid
			}
			ids, err := conversionLockIDs(tx, initial, &person)
			if err != nil {
				return err
			}
			var users []models.User
			if err := tx.Unscoped().Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).Where("id IN ?", ids).Order("id").Find(&users).Error; err != nil {
				return err
			}
			locked := map[uint]*models.User{}
			for i := range users {
				locked[users[i].ID] = &users[i]
			}
			if err := lockHousehold(tx, person.HouseholdID); err != nil {
				return err
			}
			if err := tx.First(&person, person.ID).Error; err != nil {
				return errTokenInvalid
			}
			freshIDs, err := conversionLockIDs(tx, initial, &person)
			if err != nil {
				return err
			}
			for _, id := range freshIDs {
				if locked[id] == nil {
					return errConversionRetry
				}
			}
			var inv models.Invitation
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&inv, initial.ID).Error; err != nil {
				return errTokenInvalid
			}
			if inv.TokenHash != hashToken(raw) || inv.RevokedAt != nil || inv.AcceptedAt != nil || !inv.ExpiresAt.After(time.Now()) || inv.HouseholdPersonID == nil || *inv.HouseholdPersonID != person.ID {
				return errTokenInvalid
			}
			inviter := locked[inv.InvitedBy]
			if inviter == nil {
				return errConversionRetry
			}
			allowed, err := a.conversionAllowed(tx, inviter, &person, a.householdClock(time.Now().UTC()))
			if err != nil {
				return err
			}
			if !allowed {
				return errConversionUnavailable
			}
			// Re-enabling an inviter requires a fresh issue or resend.
			var disabled int64
			if err := tx.Model(&models.AuditLog{}).Where("action = ? AND target_type = 'user' AND target_id = ? AND at >= COALESCE((SELECT max(at) FROM audit_logs WHERE target_type = 'invitation' AND target_id = ? AND action IN ?), ?)", audit.UserDisabled, inviter.ID, inv.ID, []string{audit.InvitationCreated, audit.InvitationResent}, inv.CreatedAt).Count(&disabled).Error; err != nil {
				return err
			}
			if disabled > 0 {
				return errConversionUnavailable
			}
			if err := lockInvitationEmail(tx, inv.Email); err != nil {
				return err
			}
			if err := conversionEmailAvailable(tx, inv.Email, inv.ID); err != nil {
				return err
			}
			guardians, err := conversionGuardians(tx, &person)
			if err != nil {
				return err
			}
			var occurrenceIDs []uint
			if err := tx.Model(&models.Registration{}).Where("household_person_id = ?", person.ID).Distinct("occurrence_id").Order("occurrence_id").Pluck("occurrence_id", &occurrenceIDs).Error; err != nil {
				return err
			}
			for _, id := range occurrenceIDs {
				if _, err := lockOccurrence(tx, id); err != nil {
					return err
				}
			}
			var regs []models.Registration
			if err := tx.Where("household_person_id = ?", person.ID).Order("id").Find(&regs).Error; err != nil {
				return err
			}
			for _, r := range regs {
				holder := locked[registrationHolder(r)]
				if holder == nil {
					return errConversionRetry
				}
				if holder.Status != models.UserActive || holder.DeletedAt.Valid {
					return errConversionHolder
				}
			}
			user = models.User{Email: inv.Email, Name: person.FullName(), Role: models.RoleMember, Status: models.UserActive, AccountType: person.Kind, BirthDate: person.BirthDate, PasswordHash: prepared.PasswordHash}
			if person.Kind == models.HouseholdTeen {
				user.Status = models.UserPendingConsent
			}
			if err := tx.Create(&user).Error; err != nil {
				return err
			}
			// One statement satisfies all subject, holder and named-care constraints.
			res := tx.Model(&models.Registration{}).Where("household_person_id = ?", person.ID).UpdateColumns(map[string]any{"user_id": user.ID, "household_person_id": nil, "registered_by": nil, "childcare_requested": false})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != int64(len(regs)) {
				return errors.New("registration transfer count changed")
			}
			for _, r := range regs {
				if err := audit.Write(tx, audit.Entry{Actor: &inv.InvitedBy, Action: audit.RegistrationChanged, TargetType: "registration", TargetID: r.ID, Meta: map[string]any{"personId": person.ID, "userId": user.ID, "formerRegisteredBy": r.RegisteredBy, "clearedChildcareRequested": r.ChildcareRequested, "invitationId": inv.ID}, IP: c.ClientIP()}); err != nil {
					return err
				}
			}
			if err := tx.Model(&person).Update("converted_user_id", user.ID).Error; err != nil {
				return err
			}
			if err := tx.Create(&models.HouseholdAccount{HouseholdID: person.HouseholdID, UserID: user.ID, Relation: person.Kind}).Error; err != nil {
				return err
			}
			if person.Kind == models.HouseholdTeen {
				if err := revokeGuardianLinks(tx, c, inv.InvitedBy, &person); err != nil {
					return err
				}
				for _, guardian := range guardians {
					link := models.GuardianLink{GuardianUserID: guardian, TeenUserID: &user.ID, Status: models.GuardianPending}
					if err := tx.Create(&link).Error; err != nil {
						return err
					}
					notices, err := notifyGuardianLinkPending(tx, &link, nil)
					if err != nil {
						return err
					}
					mailed = append(mailed, notices...)
				}
			}
			claim := tx.Model(&inv).Where("accepted_at IS NULL AND revoked_at IS NULL AND token_hash = ?", hashToken(raw)).Update("accepted_at", time.Now().UTC())
			if claim.Error != nil {
				return claim.Error
			}
			if claim.RowsAffected != 1 {
				return errTokenInvalid
			}
			meta := map[string]any{"personId": person.ID, "householdId": person.HouseholdID, "invitationId": inv.ID, "userId": user.ID, "accountType": user.AccountType, "transferCount": len(regs)}
			for _, action := range []string{audit.InvitationAccepted, audit.HouseholdPersonConverted} {
				target, targetID := "invitation", inv.ID
				if action == audit.HouseholdPersonConverted {
					target, targetID = "household", person.HouseholdID
				}
				if err := audit.Write(tx, audit.Entry{Actor: &inv.InvitedBy, Action: action, TargetType: target, TargetID: targetID, Meta: meta, IP: c.ClientIP()}); err != nil {
					return err
				}
			}
			recipients := []uint{}
			for _, id := range freshIDs {
				candidate := locked[id]
				if candidate != nil && policy.CanCancelHouseholdConversion(asPerson(candidate), conversionRelation(tx, id, person.HouseholdID), candidate.Status == models.UserActive) {
					recipients = append(recipients, id)
				}
			}
			notices, err := notify.Send(tx, notify.Msg{Kind: notify.HouseholdConverted, To: recipients, SubjectType: "household", SubjectID: person.HouseholdID, Link: "/profile", Vars: notify.Vars{Person: user.PublicName(), Teen: user.AccountType == models.AccountTeen}, Data: meta, DedupeKey: fmt.Sprintf("household.converted:%d", inv.ID)})
			mailed = append(mailed, notices...)
			return err
		})
		if !errors.Is(err, errConversionRetry) {
			break
		}
	}
	if err != nil {
		conversionError(c, err)
		return
	}
	a.deliverNotificationEmails(mailed)
	if user.CanSignIn() {
		if err := a.signInSession(c, &user); err != nil {
			jsonError(c, http.StatusInternalServerError, "session error")
			return
		}
		c.JSON(http.StatusOK, invitationAcceptanceJSON{User: userJSONPtr(a.toUserJSON(&user))})
	} else {
		c.JSON(http.StatusOK, invitationAcceptanceJSON{PendingConsent: true})
	}
}

func userJSONPtr(u userJSON) *userJSON { return &u }
