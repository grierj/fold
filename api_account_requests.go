package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/jobs"
	foldmail "fold/internal/mail"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ---------------------------------------------------------------------------
// Account requests (M1.3, 5.10, DEC-4, DEC-8): self-signup and member
// invite-a-friend create a pending AccountRequest an admin decides.
// Nobody signs in until an admin approves; approval creates the account
// and emails a set-password link. Teens need separate account consent.
// Public answers never reveal whether an
// email already has an account (10.1).
// ---------------------------------------------------------------------------

const (
	selfSignupRequest = "request"
	selfSignupOff     = "off"
)

// signupThanks is the uniform on-screen answer for every stored or
// already-registered self-signup (10.1: no account enumeration).
const signupThanks = "Thanks — check your email for next steps."

const (
	accountRequestIPLimit = 10 // per IP per hour (M1.3 acceptance)
	accountRequestIPWarn  = 5  // at this many in the hour the request is flagged (DEC-44)
	maxRequestMessageLen  = 1000
	requestLifetime       = 30 * 24 * time.Hour // requests expire after 30 days (DEC-4)
)

// selfSignupMode reads the self_signup setting: off or request (the
// default; there is no open mode, DEC-4).
func (a *App) selfSignupMode() string {
	if v, _, err := settingIn(a.DB, settingSelfSignup); err == nil && v == selfSignupOff {
		return selfSignupOff
	}
	return selfSignupRequest
}

func parseRequestFlags(raw string) models.RequestFlags {
	var f models.RequestFlags
	_ = json.Unmarshal([]byte(raw), &f)
	return f
}

func formatBirthDate(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02")
}

type accountRequestJSON struct {
	GuardianEmail   string              `json:"guardianEmail,omitempty"`
	ID              uint                `json:"id"`
	Name            string              `json:"name"`
	Email           string              `json:"email"`
	Phone           string              `json:"phone"`
	AccountType     string              `json:"accountType"`
	BirthDate       string              `json:"birthDate,omitempty"`
	Message         string              `json:"message"`
	Source          string              `json:"source"`
	RequestedBy     *uint               `json:"requestedBy,omitempty"`
	RequestedByName string              `json:"requestedByName,omitempty"`
	GroupID         *uint               `json:"groupId,omitempty"`
	Status          string              `json:"status"`
	HasWarnings     bool                `json:"hasWarnings"`
	Warnings        models.RequestFlags `json:"warnings"`
	DecisionNote    string              `json:"decisionNote"`
	ExpiresAt       time.Time           `json:"expiresAt"`
	CreatedAt       time.Time           `json:"createdAt"`
	DecidedAt       *time.Time          `json:"decidedAt,omitempty"`
}

func toAccountRequestJSON(r *models.AccountRequest, names publicNames) accountRequestJSON {
	flags := parseRequestFlags(r.Flags)
	out := accountRequestJSON{
		GuardianEmail: r.GuardianEmail,
		ID:            r.ID, Name: r.Name, Email: r.Email, Phone: r.Phone,
		AccountType: r.AccountType, BirthDate: formatBirthDate(r.BirthDate),
		Message: r.Message, Source: r.Source, RequestedBy: r.RequestedBy,
		GroupID: r.GroupID, Status: r.Status, HasWarnings: flags.HasWarnings() || r.AccountType == models.AccountTeen,
		Warnings: flags, DecisionNote: r.DecisionNote,
		ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt, DecidedAt: r.DecidedAt,
	}
	if r.RequestedBy != nil {
		out.RequestedByName = names.name(*r.RequestedBy)
	}
	return out
}

type accountRequestInput struct {
	GuardianEmail string `json:"guardianEmail"`
	Name          string `json:"name"`
	Email         string `json:"email"`
	Phone         string `json:"phone"`
	AgeBand       string `json:"ageBand"`
	BirthDate     string `json:"birthDate"`
	Message       string `json:"message"`
	Website       string `json:"website"` // honeypot: people never fill this in
}

// validated is the cleaned input plus the age gate's verdict.
type validatedRequest struct {
	guardianEmail               string
	name, email, phone, message string
	accountType                 string
	birthDate                   *time.Time
	honeypot                    bool
}

// validateAccountRequest cleans one door's input and runs the age gate
// (DEC-8). Under 13 answers 400 with the household explanation and the
// caller stores nothing.
func (a *App) validateAccountRequest(c *gin.Context, body accountRequestInput) (validatedRequest, bool) {
	name, err := requiredName(body.Name)
	if err != nil {
		fail(c, err)
		return validatedRequest{}, false
	}
	email := foldmail.NormalizeAddress(body.Email)
	if !validEmail(email) {
		fail(c, errEnterValidEmail)
		return validatedRequest{}, false
	}
	phone, err := phoneNumber(body.Phone)
	if err != nil {
		fail(c, err)
		return validatedRequest{}, false
	}
	message := strings.TrimSpace(body.Message)
	if len(message) > maxRequestMessageLen {
		fail(c, errMessageTooLong1000)
		return validatedRequest{}, false
	}
	accountType, birth, ok := a.classifyAccountInput(c, body.AgeBand, body.BirthDate)
	if !ok {
		return validatedRequest{}, false
	}
	guardianEmail := ""
	if accountType == models.AccountTeen {
		guardianEmail = foldmail.NormalizeAddress(body.GuardianEmail)
		if guardianEmail != "" && (!validEmail(guardianEmail) || guardianEmail == email) {
			fail(c, errInvalidGuardianEmail)
			return validatedRequest{}, false
		}
	}
	return validatedRequest{
		guardianEmail: guardianEmail,
		name:          name, email: email, phone: phone, message: message,
		accountType: accountType, birthDate: birth,
		honeypot: strings.TrimSpace(body.Website) != "",
	}, true
}

// storeAccountRequest runs the shared door logic once the input is
// valid: rate limit, duplicate flag, the already-have-an-account path,
// then the pending row with its audit row and admin bell item. It
// answers the request itself and reports whether a row was stored.
func (a *App) storeAccountRequest(c *gin.Context, v validatedRequest, source string, requestedBy *uint) (stored bool) {
	ctx := c.Request.Context()
	hits, err := a.throttleHit(ctx, accountRequestIPKey(c.ClientIP()), time.Hour)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save your request")
		return false
	}
	if hits > accountRequestIPLimit {
		// A rate limit is not something the person can act on, and a 429
		// tells a prober the form is being watched: answer as usual and
		// log it (DEC-52). Nothing is stored and nobody is emailed.
		log.Printf("account request rate limited: ip=%s source=%s", c.ClientIP(), source)
		a.answerAccountRequest(c, source)
		return false
	}
	if ok, err := a.throttleAllow(ctx, accountRequestSiteKey(), publicSignupSiteLimit, time.Hour); err != nil || !ok {
		log.Printf("account request site limit: source=%s err=%v", source, err)
		a.answerAccountRequest(c, source)
		return false
	}
	// Per-address limit (#91): rotating IPs can't flood one inbox, or the
	// admins, with requests for that address. Same answer, nothing stored
	// or sent.
	if ok, err := a.targetAllow(ctx, accountRequestAddrKey(v.email)); err != nil || !ok {
		log.Printf("account request address limited: source=%s err=%v", source, err)
		a.answerAccountRequest(c, source)
		return false
	}
	var existing models.User
	// A guest row (M1.4) is not an account yet: its owner asking for one
	// is the normal upgrade path, not the already-have-an-account path.
	hasAccount := a.DB.Where("email = ?", v.email).Limit(1).Find(&existing).RowsAffected == 1 &&
		existing.Status != models.UserGuest
	// Both paths pay the same password-check cost. SMTP work is deferred on
	// both paths so the number of admins cannot disclose account existence.
	(&models.User{}).CheckPassword("not a real password")
	if hasAccount {
		// Both doors answer the same way, however the visitor signed in:
		// any adult member can use invite-a-friend, so a different answer
		// would let them test addresses for church membership (DEC-52).
		// Same answer and about the same cost as a stored request; the
		// hint travels by email to the address instead (10.1).
		if !v.honeypot {
			// The job carries the user's id only; delivery mails whatever
			// address the account has then. The hint's durable record is
			// the answer's only side effect, so a failed write is the same
			// failure as a failed stored request. It uses no request context,
			// like the stored paths' transactions: a client that disconnects
			// cannot turn the durable write into a failure.
			if _, err := jobs.Enqueue(a.DB, kindAccountExistsMail, userMailJob{UserID: existing.ID}, time.Now(), "", directMailAttempts); err != nil {
				log.Printf("account-exists hint: %s", jobs.CleanError(err))
				jsonError(c, http.StatusInternalServerError, "could not save your request")
				return false
			}
		}
		a.answerAccountRequest(c, source)
		return false
	}
	var pending int64
	a.DB.Model(&models.AccountRequest{}).
		Where("email = ? AND status = ?", v.email, models.RequestPending).Count(&pending)
	flags := models.RequestFlags{
		Duplicate:   pending > 0,
		Honeypot:    v.honeypot,
		RateLimited: hits >= accountRequestIPWarn,
	}
	flagsJSON, _ := json.Marshal(flags)
	req := models.AccountRequest{
		GuardianEmail: v.guardianEmail,
		Name:          v.name, Email: v.email, Phone: v.phone,
		AccountType: v.accountType, BirthDate: v.birthDate, Message: v.message,
		Source: source, RequestedBy: requestedBy, Status: models.RequestPending,
		Flags: string(flagsJSON), IP: c.ClientIP(),
		ExpiresAt: time.Now().UTC().Add(requestLifetime),
	}
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&req).Error; err != nil {
			return err
		}
		if err := audit.Write(tx, audit.Entry{Actor: requestedBy, Action: audit.AccountRequested,
			TargetType: "account_request", TargetID: req.ID,
			Meta: map[string]any{"source": source, "accountType": v.accountType, "hasWarnings": flags.HasWarnings()},
			IP:   c.ClientIP()}); err != nil {
			return err
		}
		if err := notifyAccountRequested(tx, &req, flags); err != nil {
			return err
		}
		// A duplicate of a pending request is stored and flagged on the bell,
		// but sends nothing: the requester and the admins were already
		// emailed about the first one (#91). The job is queued with the
		// request, so neither exists without the other.
		if source == models.RequestSelfSignup && !flags.Duplicate && !flags.Honeypot {
			_, err := jobs.Enqueue(tx, kindAccountRequestMail, requestMailJob{RequestID: req.ID, Case: requestMailReceived}, time.Now(), "", directMailAttempts)
			return err
		}
		return nil
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save your request")
		return false
	}
	a.answerAccountRequest(c, source)
	return true
}

// answerAccountRequest is the uniform success reply for each door.
func (a *App) answerAccountRequest(c *gin.Context, source string) {
	if source == models.RequestSelfSignup {
		c.JSON(http.StatusOK, gin.H{"message": signupThanks})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Thanks — an admin will review it, and your friend will hear from us then."})
}

// apiCreateAccountRequest is the public self-signup door (DEC-4): it
// stores a pending request and always answers the same way.
func (a *App) apiCreateAccountRequest(c *gin.Context) {
	if a.selfSignupMode() == selfSignupOff {
		fail(c, errSignupOff)
		return
	}
	var body accountRequestInput
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	v, ok := a.validateAccountRequest(c, body)
	if !ok {
		return
	}
	a.storeAccountRequest(c, v, models.RequestSelfSignup, nil)
}

// apiInviteFriend is the member door (DEC-4): the friend is emailed
// only after an admin approves, never at submission.
func (a *App) apiInviteFriend(c *gin.Context) {
	me := apiUser(c)
	if !policy.CanInviteFriend(asPerson(me)) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return
	}
	var body accountRequestInput
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	v, ok := a.validateAccountRequest(c, body)
	if !ok {
		return
	}
	a.storeAccountRequest(c, v, models.RequestMemberInvite, &me.ID)
}

// ---------------------------------------------------------------------------
// Admin queue
// ---------------------------------------------------------------------------

func (a *App) apiListAccountRequests(c *gin.Context) {
	if _, err := a.expirePendingRequests(); err != nil {
		log.Printf("account requests: expire: %v", err)
	}
	status := c.DefaultQuery("status", models.RequestPending)
	q := a.DB.Order("created_at, id")
	switch status {
	case models.RequestPending, models.RequestApproved, models.RequestRejected, models.RequestExpired, models.RequestWithdrawn:
		q = q.Where("status = ?", status)
	case "all":
	default:
		jsonError(c, http.StatusBadRequest, "unknown status")
		return
	}
	var rows []models.AccountRequest
	q.Find(&rows)
	ids := []uint{}
	for _, r := range rows {
		if r.RequestedBy != nil {
			ids = append(ids, *r.RequestedBy)
		}
	}
	names := publicNamesIn(a.DB, ids)
	out := make([]accountRequestJSON, 0, len(rows))
	for i := range rows {
		out = append(out, toAccountRequestJSON(&rows[i], names))
	}
	c.JSON(http.StatusOK, gin.H{"requests": out})
}

func (a *App) loadAccountRequest(c *gin.Context) (*models.AccountRequest, bool) {
	id, _ := strconv.Atoi(c.Param("id"))
	var req models.AccountRequest
	if err := a.DB.First(&req, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "account request not found")
		return nil, false
	}
	return &req, true
}

// apiApproveAccountRequest creates the account (or finds the one that
// appeared meanwhile) and emails the set-password link. Inline approval
// from the bell is refused for teen or flagged requests (DEC-44).
func (a *App) apiApproveAccountRequest(c *gin.Context) {
	req, ok := a.loadAccountRequest(c)
	if !ok {
		return
	}
	var body struct {
		Note                       string  `json:"note"`
		FromNotification           *uint   `json:"fromNotification"`
		AccountConsentEvidenceText *string `json:"accountConsentEvidenceText"`
	}
	_ = c.ShouldBindJSON(&body)
	if !a.fromNotification(c, body.FromNotification, notify.ActionApproveAccount, req.ID) {
		return
	}
	if body.FromNotification != nil &&
		!policy.InlineApproveAccountOK(req.AccountType, parseRequestFlags(req.Flags).HasWarnings()) {
		fail(c, errNeedsAdminQueue)
		return
	}
	a.decideAccountRequest(c, req, true, strings.TrimSpace(body.Note), body.AccountConsentEvidenceText)
}

// apiRejectAccountRequest turns a request down. Rejection is never an
// inline action, so a fromNotification naming one is refused (6.6).
func (a *App) apiRejectAccountRequest(c *gin.Context) {
	req, ok := a.loadAccountRequest(c)
	if !ok {
		return
	}
	var body struct {
		Note             string `json:"note"`
		FromNotification *uint  `json:"fromNotification"`
	}
	_ = c.ShouldBindJSON(&body)
	if !a.fromNotification(c, body.FromNotification, "reject", req.ID) {
		return
	}
	a.decideAccountRequest(c, req, false, strings.TrimSpace(body.Note), nil)
}

var errRequestHandled = errors.New("account request already decided")
var errConsentExistingAccount = apiErr(http.StatusConflict, "consent_existing_account", "that email already has an account; approve without consent and review the account separately")

func (a *App) decideAccountRequest(c *gin.Context, req *models.AccountRequest, approve bool, note string, consentEvidence *string) {
	if len(note) > 500 {
		fail(c, errNoteTooLong500)
		return
	}
	me := apiUser(c)
	if req.Status == models.RequestPending && !req.ExpiresAt.After(time.Now().UTC()) {
		if _, err := a.expirePendingRequests(); err != nil {
			log.Printf("account requests: expire: %v", err)
		}
		fail(c, errRequestExpired)
		return
	}
	outcome, auditAction := "rejected", audit.AccountRejected
	if approve {
		outcome, auditAction = "approved", audit.AccountApproved
	}
	var userID uint
	var created, upgraded bool
	err := a.contactMutation(contactChange{}, func(tx *gorm.DB) error {
		var consentActor *models.User
		var conversionLocked map[uint]*models.User
		if approve && req.Source == models.RequestHouseholdConversion && req.HouseholdPersonID != nil && req.InvitationID != nil {
			var person models.HouseholdPerson
			var inv models.Invitation
			if err := tx.First(&person, *req.HouseholdPersonID).Error; err != nil {
				return errConversionPerson
			}
			if err := tx.First(&inv, *req.InvitationID).Error; err != nil {
				return errConversionUnavailable
			}
			var err error
			conversionLocked, _, err = lockConversionParticipants(tx, &inv, &person, nil, me.ID)
			if err != nil {
				return err
			}
			actor := conversionLocked[me.ID]
			if !adminCurrent(actor, me) {
				return errConsentForbidden
			}
		}
		if consentEvidence != nil && req.AccountType != models.AccountTeen {
			return errConsentTeen
		}
		if consentEvidence != nil && conversionLocked != nil {
			consentActor = conversionLocked[me.ID]
		} else if consentEvidence != nil {
			// An existing guest can be upgraded on the same row. No other
			// existing account receives consent as a side effect of approval.
			var existing models.User
			if err := tx.Where("email = ?", req.Email).Limit(1).Find(&existing).Error; err != nil {
				return err
			}
			var target *models.User
			var err error
			if existing.ID != 0 {
				consentActor, target, err = lockConsentUsers(tx, me.ID, existing.ID)
				if err != nil {
					return err
				}
				if target.Status != models.UserGuest || foldmail.NormalizeAddress(target.Email) != foldmail.NormalizeAddress(req.Email) {
					return errConsentExistingAccount
				}
			} else {
				consentActor = &models.User{}
				if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(consentActor, me.ID).Error; err != nil {
					return err
				}
			}
			if !adminCurrent(consentActor, me) {
				return errConsentForbidden
			}
		}
		now := time.Now().UTC()
		status := models.RequestRejected
		if approve {
			status = models.RequestApproved
		}
		if err := decidePendingIn(tx, &models.AccountRequest{}, req.ID, models.RequestPending, map[string]any{
			"status": status, "decided_by": me.ID, "decided_at": now, "decision_note": note, "password_hash": nil,
		}); err != nil {
			return err
		}
		meta := map[string]any{"accountType": req.AccountType}
		if approve && req.Source == models.RequestHouseholdConversion {
			var err error
			userID, err = a.approveHouseholdConversion(tx, c, req, conversionLocked)
			if err != nil {
				return err
			}
			meta["userId"] = userID
		} else if approve {
			var existing models.User
			found := tx.Where("email = ?", req.Email).Limit(1).Find(&existing).RowsAffected == 1
			if found && existing.Status == models.UserGuest {
				// A guest becomes an account on the same row (C5), so
				// every guest registration stays theirs. From here it
				// is the created path: a set-password link, not the
				// already-have-an-account hint.
				existing.Status = newAccountStatus(req.AccountType, false)
				existing.Name, existing.Phone = req.Name, req.Phone
				existing.AccountType, existing.BirthDate = req.AccountType, req.BirthDate
				if err := upgradeGuestIn(tx, &existing, "status", "name", "phone", "account_type", "birth_date"); err != nil {
					return err
				}
				userID, upgraded = existing.ID, true
			} else if found {
				if consentEvidence != nil {
					return errConsentExistingAccount
				}
				userID = existing.ID
			} else {
				u := models.User{
					Email: req.Email, Name: req.Name, Phone: req.Phone,
					Role: models.RoleMember, Status: newAccountStatus(req.AccountType, false),
					AccountType: req.AccountType, BirthDate: req.BirthDate,
				}
				if err := tx.Create(&u).Error; err != nil {
					return err
				}
				userID, created = u.ID, true
			}
			meta["userId"] = userID
			meta["created"] = created
			if upgraded {
				meta["upgradedGuest"] = true
			}
			if err := nominateTeenGuardian(tx, req, userID); err != nil {
				return err
			}
		}
		if approve && userID != 0 {
			if err := tx.Model(&models.AccountRequest{}).Where("id = ?", req.ID).Update("approved_user_id", userID).Error; err != nil {
				return err
			}
		}
		if consentEvidence != nil {
			var teen models.User
			if err := tx.First(&teen, userID).Error; err != nil {
				return err
			}
			if err := a.changeTeenConsent(tx, c, &teen, consentActor, teenConsentChange{
				Scope: models.TeenConsentAccount, Status: models.ConsentGranted, EvidenceText: *consentEvidence,
			}); err != nil {
				return err
			}
		}
		// The decision mail is queued with the decision. It carries the
		// request id and a fixed case; delivery rebuilds the text, mints any
		// setup link, and uses the account's address as it is then.
		mailCase := requestMailRejected
		switch {
		case !approve:
		case req.Source == models.RequestHouseholdConversion:
			mailCase = requestMailConversionApproved
		case created || upgraded:
			mailCase = requestMailApproved
		default:
			mailCase = requestMailExists
		}
		if _, err := jobs.Enqueue(tx, kindAccountRequestMail, requestMailJob{RequestID: req.ID, Case: mailCase}, time.Now(), "", directMailAttempts); err != nil {
			return err
		}
		if _, err := notify.Resolve(tx, notify.AccountRequested, req.ID, &me.ID, outcome); err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: auditAction,
			TargetType: "account_request", TargetID: req.ID, Meta: meta, IP: c.ClientIP()})
	})
	if a.answerAlreadyHandled(c, err, notify.AccountRequested, req.ID) {
		return
	}
	if err != nil && req.Source == models.RequestHouseholdConversion {
		conversionError(c, err)
		return
	}
	if emailTaken(err) {
		fail(c, errConversionEmail)
		return
	}
	if err != nil {
		fail(c, err)
		return
	}
	var fresh models.AccountRequest
	a.DB.First(&fresh, req.ID)
	ids := []uint{}
	if fresh.RequestedBy != nil {
		ids = append(ids, *fresh.RequestedBy)
	}
	c.JSON(http.StatusOK, accountRequestDecisionJSON{Request: toAccountRequestJSON(&fresh, publicNamesIn(a.DB, ids)), UserID: userID})
}

// apiConfirmAccountSetup redeems the emailed set-password link. An
// active account gets a login; a pending teen gets the public consent explanation.
func (a *App) apiConfirmAccountSetup(c *gin.Context) {
	fresh, pendingConsent, ok := a.redeemPasswordLink(c, models.TokenAccountSetup, audit.AccountSetupCompleted, "could not set your password")
	if !ok {
		return
	}
	c.JSON(http.StatusOK, accountRequestConfirmJSON{User: a.toUserJSON(&fresh), PendingConsent: pendingConsent})
}

// apiSetSelfSignup turns self-signup between request (default) and off
// (DEC-4). The setup wizard sets the same key in M4.2.
func (a *App) apiSetSelfSignup(c *gin.Context) {
	var body struct {
		Mode string `json:"mode"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || (body.Mode != selfSignupRequest && body.Mode != selfSignupOff) {
		jsonError(c, http.StatusBadRequest, "mode must be request or off")
		return
	}
	me := apiUser(c)
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := setSettingIn(tx, settingSelfSignup, body.Mode); err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.SelfSignupChanged,
			TargetType: "setting", TargetID: 0,
			Meta: map[string]any{"mode": body.Mode}, IP: c.ClientIP()})
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save the setting")
		return
	}
	c.JSON(http.StatusOK, gin.H{"selfSignup": body.Mode})
}

// expirePendingRequests marks every overdue pending request expired,
// resolves its admin bell items, and audits each one (DEC-4). It runs
// lazily from the admin queue and daily as the accounts.expire_requests job (5.5).
func (a *App) expirePendingRequests() (int64, error) {
	var ids []uint
	if err := a.DB.Model(&models.AccountRequest{}).
		Where("status = ? AND expires_at <= now()", models.RequestPending).Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	var done int64
	for _, id := range ids {
		err := a.DB.Transaction(func(tx *gorm.DB) error {
			res := tx.Model(&models.AccountRequest{}).
				Where("id = ? AND status = ?", id, models.RequestPending).
				Updates(map[string]any{"status": models.RequestExpired, "password_hash": nil})
			if res.Error != nil || res.RowsAffected == 0 {
				return res.Error
			}
			done++
			if _, err := notify.Resolve(tx, notify.AccountRequested, id, nil, "expired"); err != nil {
				return err
			}
			return audit.Write(tx, audit.Entry{Action: audit.AccountExpired,
				TargetType: "account_request", TargetID: id})
		})
		if err != nil {
			return done, err
		}
	}
	return done, nil
}

// ---------------------------------------------------------------------------
// Decision and hint emails (C6: plain text through the minimal sender)
// ---------------------------------------------------------------------------

func (a *App) requestReceivedMessage(to, site string) foldmail.Message {
	return foldmail.Message{
		To:      to,
		Subject: fmt.Sprintf("We got your %s account request", site),
		Text: fmt.Sprintf(`Thanks for asking for an account at %s.

An admin will review your request. We'll email you again when there's a decision — if you're approved, that email has the link to set your password.

If that wasn't you, you can ignore this email.`,
			site),
	}
}

func (a *App) existingAccountMessage(to, site string) foldmail.Message {
	return foldmail.Message{
		To:      to,
		Subject: fmt.Sprintf("You already have a %s account", site),
		Text: fmt.Sprintf(`Someone asked for a %s account with this email address, but you already have one.

Sign in here:
%s/login

Forgot your password? Reset it here:
%s/forgot-password`,
			site, a.emailBaseURL(), a.emailBaseURL()),
	}
}

// accountApprovedMessage carries the set-password link. The consent sentence
// follows the account's state at delivery, so a teen whose guardian already
// consented is not told to wait for it.
func (a *App) accountApprovedMessage(to, raw, site string, needsConsent bool) foldmail.Message {
	link := a.emailBaseURL() + "/set-password?token=" + raw
	extra := ""
	if needsConsent {
		extra = "\n\nBecause you're 13 to 17, your account also needs a parent or guardian's consent before you can use groups and events. Setting your password now is still the right first step."
	}
	return foldmail.Message{
		To:      to,
		Subject: fmt.Sprintf("Your %s account is approved", site),
		Text: fmt.Sprintf(`Good news — your account at %s is approved.

Set your password here (the link works once and expires in 24 hours):
%s%s

After that you can sign in with this email address.`, site, link, extra),
	}
}

func (a *App) conversionApprovedMessage(to, site string, teen bool) foldmail.Message {
	extra := ""
	if teen {
		extra = "\n\nYour account is waiting for consent and a fresh guardian-link check. You can sign in to check its status. Your household keeps managing your sign-ups until those checks are complete."
	}
	return foldmail.Message{
		To:      to,
		Subject: fmt.Sprintf("Your %s account is approved", site),
		Text: fmt.Sprintf(`Good news — your account at %s is approved.

You can now sign in at %s/login with this email address and the password you chose.%s`, site, a.emailBaseURL(), extra),
	}
}

func (a *App) accountRejectedMessage(to, site, note string) foldmail.Message {
	notePart := ""
	if note != "" {
		notePart = "\n\nNote from the church office: " + note
	}
	return foldmail.Message{
		To:      to,
		Subject: fmt.Sprintf("About your %s account request", site),
		Text: fmt.Sprintf(`Thanks for asking for an account at %s.

We're not able to approve this request.%s

If you think this is a mistake, contact your church office and they'll help.`, site, notePart),
	}
}
