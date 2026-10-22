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
	errConversionTeen        = errors.New("teens can get their own account once parent consent is available")
	errConversionUnavailable = errors.New("this person cannot create an account yet; check their birth date, account type and guardian link")
	errConversionEmail       = errors.New("that email is unavailable; use a different email")
	errConversionOpen        = errors.New("cancel the current invitation before choosing a different email")
	errConversionHolder      = errors.New("a responsible account is unavailable; ask your church office to check it")
	errConversionPerson      = errors.New("this household person was removed or already has an account; reject this request")
	errConversionPending     = errors.New("this account is waiting for an admin; wait for their decision")
	errConversionRetry       = errors.New("household changed while locking")
	errConversionDetails     = errors.New("this person's details changed after they accepted; ask the household to send a new invitation")
	errConversionAdult       = errors.New("this person no longer meets the adult account rules; check their birth date and account type before sending a new invitation")
	errConversionInviter     = errors.New("the person who sent the invitation can no longer manage this household; ask an active household adult or admin to send a new invitation")
)

// conversionJSON has no id, so a suppressed send can't be told apart from
// a real one (Warden, PR 111 N3).
type conversionJSON struct {
	Email     string    `json:"email"`
	ExpiresAt time.Time `json:"expiresAt"`
	State     string    `json:"state"`
}

func conversionMetadata(inv *models.Invitation) conversionJSON {
	state := "pending"
	if !inv.ExpiresAt.After(time.Now()) {
		state = "expired"
	}
	return conversionJSON{Email: inv.Email, ExpiresAt: inv.ExpiresAt, State: state}
}

// Ordinary and household invitations reserve the same email namespace.
func lockInvitationEmail(tx *gorm.DB, email string) error {
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 7316193))", normalizeEmail(email)).Error
}

func conversionEmailAvailable(tx *gorm.DB, email string, except uint, exceptRequest ...uint) error {
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
	q := tx.Model(&models.AccountRequest{}).Where("email = ? AND source = ? AND status = ?", email, models.RequestHouseholdConversion, models.RequestPending)
	if len(exceptRequest) > 0 {
		q = q.Where("id <> ?", exceptRequest[0])
	}
	if err := q.Count(&n).Error; err != nil {
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

// conversionBatch holds what canConvert needs for a set of household
// people, loaded once per request: the church-local date, the actor's
// relation to the household, and people whose sign-ups have a disabled
// or deleted holder (PR 111, Whittler).
type conversionBatch struct {
	actor   *models.User
	today   time.Time
	rel     policy.HouseholdRel
	blocked map[uint]bool
}

func (a *App) loadConversionBatch(tx *gorm.DB, actor *models.User, householdID uint, personIDs []uint) (*conversionBatch, error) {
	if actor == nil || len(personIDs) == 0 {
		return a.loadConversionBatchFor(tx, actor, policy.HouseholdRel{}, personIDs)
	}
	return a.loadConversionBatchFor(tx, actor, conversionRelation(tx, actor.ID, householdID), personIDs)
}

// loadConversionBatchFor takes a relation the caller already knows, so
// GET /me/household doesn't read the viewer's account twice (Whittler, PR 111).
func (a *App) loadConversionBatchFor(tx *gorm.DB, actor *models.User, rel policy.HouseholdRel, personIDs []uint) (*conversionBatch, error) {
	b := &conversionBatch{actor: actor, today: a.householdClock(time.Now().UTC()), blocked: map[uint]bool{}, rel: rel}
	if actor == nil || len(personIDs) == 0 {
		return b, nil
	}
	var blocked []uint
	if err := tx.Model(&models.Registration{}).Joins("JOIN users ON users.id = registrations.registered_by").
		Where("registrations.household_person_id IN ? AND (users.deleted_at IS NOT NULL OR users.status <> ?)", personIDs, models.UserActive).
		Distinct("registrations.household_person_id").Pluck("registrations.household_person_id", &blocked).Error; err != nil {
		return nil, err
	}
	for _, id := range blocked {
		b.blocked[id] = true
	}
	return b, nil
}

// allowed reports whether the actor may convert person.
// A blocked holder returns errConversionHolder.
func (b *conversionBatch) allowed(person *models.HouseholdPerson) (bool, error) {
	if b == nil || b.actor == nil {
		return false, nil
	}
	if b.blocked[person.ID] {
		return false, errConversionHolder
	}
	actor := b.actor
	return policy.CanConvertHouseholdPerson(asPerson(actor), b.rel, householdFacts(person, nil, b.today), actor.Status == models.UserActive && !actor.DeletedAt.Valid), nil
}

func (a *App) conversionAllowed(tx *gorm.DB, actor *models.User, person *models.HouseholdPerson) (bool, error) {
	b, err := a.loadConversionBatch(tx, actor, person.HouseholdID, []uint{person.ID})
	if err != nil {
		return false, err
	}
	return b.allowed(person)
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
	case errors.Is(err, errConversionRetry):
		jsonError(c, http.StatusConflict, "this household changed while approving; try again")
	case errors.Is(err, errConversionDetails), errors.Is(err, errConversionAdult), errors.Is(err, errConversionInviter), errors.Is(err, errConversionTeen), errors.Is(err, errConversionPerson), errors.Is(err, errConversionPending), errors.Is(err, errConversionUnavailable), errors.Is(err, errConversionEmail), errors.Is(err, errConversionOpen), errors.Is(err, errConversionHolder):
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
		if person.Kind == models.HouseholdTeen {
			return errConversionTeen
		}
		allowed, err := a.conversionAllowed(tx, &actor, person)
		if err != nil {
			return err
		}
		if !allowed {
			return errConversionUnavailable
		}
		var pending int64
		if err := tx.Model(&models.AccountRequest{}).Where("household_person_id = ? AND source = ? AND status = ?", person.ID, models.RequestHouseholdConversion, models.RequestPending).Count(&pending).Error; err != nil {
			return err
		}
		if pending > 0 {
			return errConversionPending
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
			log.Printf("household invitation send suppressed for actor %d", actor.ID)
			// Keep an existing token intact. A first invitation isn't saved,
			// so a limited sender can't reserve a newcomer's email (Warden,
			// PR 111 F2). The answer still looks like a normal send.
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
		send = allowed
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

// createConversionApprovalRequest holds adult credentials until approval.
func createConversionApprovalRequest(tx *gorm.DB, person *models.HouseholdPerson, inv *models.Invitation, hash *string, ip string) ([]uint, error) {
	req := models.AccountRequest{
		Name: person.FullName(), Email: inv.Email, BirthDate: person.BirthDate,
		PasswordHash: hash, HouseholdPersonID: &person.ID, InvitationID: &inv.ID,
		AccountType: models.AccountAdult, Source: models.RequestHouseholdConversion,
		RequestedBy: &inv.InvitedBy, Status: models.RequestPending, Flags: "{}", IP: ip,
		ExpiresAt: time.Now().UTC().Add(requestLifetime),
	}
	if err := tx.Create(&req).Error; err != nil {
		return nil, err
	}
	if err := audit.Write(tx, audit.Entry{Action: audit.AccountRequested, TargetType: "account_request", TargetID: req.ID,
		Meta: map[string]any{"source": models.RequestHouseholdConversion, "accountType": models.AccountAdult, "hasWarnings": false, "personId": person.ID}, IP: ip}); err != nil {
		return nil, err
	}
	return notifyAccountRequested(tx, &req, models.RequestFlags{})
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
	timing := "Your past and upcoming event sign-ups will move to your account after an admin approves. Until then, your household keeps managing them."
	text := fmt.Sprintf("%s invited you to create an account for %s. %s\n\nChoose your password: %s/invite?token=%s\n\nThis link expires in 24 hours. If this invitation is not for you, ignore it.", actor.PublicName(), inv.Name, timing, a.emailBaseURL(), raw)
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
	var mailed []uint
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		mailed = nil
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
			if person.Kind == models.HouseholdTeen {
				return errConversionTeen
			}
			inviter := locked[inv.InvitedBy]
			if inviter == nil {
				return errConversionRetry
			}
			allowed, err := a.conversionAllowed(tx, inviter, &person)
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
			mailed, err = createConversionApprovalRequest(tx, &person, &inv, prepared.PasswordHash, c.ClientIP())
			if err != nil {
				return err
			}

			claim := tx.Model(&inv).Where("accepted_at IS NULL AND revoked_at IS NULL AND token_hash = ?", hashToken(raw)).Update("accepted_at", time.Now().UTC())
			if claim.Error != nil {
				return claim.Error
			}
			if claim.RowsAffected != 1 {
				return errTokenInvalid
			}
			return audit.Write(tx, audit.Entry{Actor: &inv.InvitedBy, Action: audit.InvitationAccepted, TargetType: "invitation", TargetID: inv.ID, Meta: map[string]any{"personId": person.ID}, IP: c.ClientIP()})
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
	c.JSON(http.StatusOK, invitationAcceptanceJSON{PendingApproval: true})
}

func userJSONPtr(u userJSON) *userJSON { return &u }

// moveHouseholdConversion runs after user and household locks are held.
func (a *App) moveHouseholdConversion(tx *gorm.DB, c *gin.Context, inv *models.Invitation, person *models.HouseholdPerson, locked map[uint]*models.User, ids []uint, passwordHash *string, result *models.User, mailed *[]uint) error {
	var occurrenceIDs []uint
	if err := tx.Model(&models.Registration{}).Where("household_person_id = ?", person.ID).Distinct("occurrence_id").Order("occurrence_id").Pluck("occurrence_id", &occurrenceIDs).Error; err != nil {
		return err
	}
	// One statement locks every affected date, in id order like
	// lockOccurrence's callers, so it can't deadlock with them.
	if len(occurrenceIDs) > 0 {
		var occurrences []models.EventOccurrence
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id IN ?", occurrenceIDs).Order("id").Find(&occurrences).Error; err != nil {
			return err
		}
		if len(occurrences) != len(occurrenceIDs) {
			return gorm.ErrRecordNotFound
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
	user := models.User{Email: inv.Email, Name: person.FullName(), Role: models.RoleMember, Status: models.UserActive, AccountType: person.Kind, BirthDate: person.BirthDate, PasswordHash: passwordHash}
	if err := tx.Create(&user).Error; err != nil {
		return err
	}
	*result = user
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
	if err := tx.Model(person).Update("converted_user_id", user.ID).Error; err != nil {
		return err
	}
	if err := tx.Create(&models.HouseholdAccount{HouseholdID: person.HouseholdID, UserID: user.ID, Relation: person.Kind}).Error; err != nil {
		return err
	}
	meta := map[string]any{"personId": person.ID, "householdId": person.HouseholdID, "invitationId": inv.ID, "userId": user.ID, "accountType": user.AccountType, "transferCount": len(regs)}
	if err := audit.Write(tx, audit.Entry{Actor: &inv.InvitedBy, Action: audit.HouseholdPersonConverted, TargetType: "household", TargetID: person.HouseholdID, Meta: meta, IP: c.ClientIP()}); err != nil {
		return err
	}

	recipients := []uint{}
	for _, id := range ids {
		candidate := locked[id]
		if candidate != nil && policy.CanCancelHouseholdConversion(asPerson(candidate), conversionRelation(tx, id, person.HouseholdID), candidate.Status == models.UserActive) {
			recipients = append(recipients, id)
		}
	}
	notices, err := notify.Send(tx, notify.Msg{Kind: notify.HouseholdConverted, To: recipients, SubjectType: "household", SubjectID: person.HouseholdID, Link: "/profile", Vars: notify.Vars{Person: user.PublicName()}, Data: meta, DedupeKey: fmt.Sprintf("household.converted:%d", inv.ID)})
	*mailed = append(*mailed, notices...)
	return err
}

// approveHouseholdConversion discovers earlier-order locks before the household.
// A changed lock set refuses the decision so the admin can retry safely.
func (a *App) approveHouseholdConversion(tx *gorm.DB, c *gin.Context, req *models.AccountRequest, mailed *[]uint) (uint, error) {
	if req.HouseholdPersonID == nil || req.InvitationID == nil || req.PasswordHash == nil {
		return 0, errConversionUnavailable
	}
	var person models.HouseholdPerson
	if err := tx.First(&person, *req.HouseholdPersonID).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, err
		}
		return 0, errConversionPerson
	}
	var inv models.Invitation
	if err := tx.First(&inv, *req.InvitationID).Error; err != nil {
		return 0, errConversionUnavailable
	}
	ids, err := conversionLockIDs(tx, &inv, &person)
	if err != nil {
		return 0, err
	}
	var users []models.User
	if err := tx.Unscoped().Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).Where("id IN ?", ids).Order("id").Find(&users).Error; err != nil {
		return 0, err
	}
	locked := map[uint]*models.User{}
	for i := range users {
		locked[users[i].ID] = &users[i]
	}
	if err := lockHousehold(tx, person.HouseholdID); err != nil {
		return 0, err
	}
	if err := tx.First(&person, person.ID).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, err
		}
		return 0, errConversionPerson
	}
	if person.ConvertedUserID != nil {
		return 0, errConversionPerson
	}
	if person.FullName() != req.Name || formatBirthDate(person.BirthDate) != formatBirthDate(req.BirthDate) {
		return 0, errConversionDetails
	}
	if !policy.MeetsAdultAccountRules(householdFacts(&person, nil, a.householdClock(time.Now().UTC()))) {
		return 0, errConversionAdult
	}
	inviter := locked[inv.InvitedBy]
	if req.RequestedBy == nil || *req.RequestedBy != inv.InvitedBy || inviter == nil || inviter.Status != models.UserActive || inviter.DeletedAt.Valid {
		return 0, errConversionInviter
	}
	fresh, err := conversionLockIDs(tx, &inv, &person)
	if err != nil {
		return 0, err
	}
	for _, id := range fresh {
		if locked[id] == nil {
			return 0, errConversionRetry
		}
	}
	if err := lockInvitationEmail(tx, req.Email); err != nil {
		return 0, err
	}
	if err := conversionEmailAvailable(tx, req.Email, inv.ID, req.ID); err != nil {
		return 0, err
	}
	inv.Email = req.Email
	var user models.User
	if err := a.moveHouseholdConversion(tx, c, &inv, &person, locked, fresh, req.PasswordHash, &user, mailed); err != nil {
		return 0, err
	}
	return user.ID, nil
}
