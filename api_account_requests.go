package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fold/internal/accounts"
	"fold/internal/audit"
	foldmail "fold/internal/mail"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// Account requests (M1.3, 5.10, DEC-4, DEC-8): self-signup and member
// invite-a-friend create a pending AccountRequest an admin decides.
// Nobody signs in until an admin approves; approval creates the account
// and emails a set-password link. Teens start pending_consent (consent
// itself arrives in M1.10). Public answers never reveal whether an
// email already has an account (10.1).
// ---------------------------------------------------------------------------

const settingSelfSignup = "self_signup"

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
	var s models.Setting
	if err := a.DB.Where("key = ?", settingSelfSignup).First(&s).Error; err != nil {
		return selfSignupRequest
	}
	if s.Value == selfSignupOff {
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

func (a *App) toAccountRequestJSON(r *models.AccountRequest) accountRequestJSON {
	flags := parseRequestFlags(r.Flags)
	out := accountRequestJSON{
		ID: r.ID, Name: r.Name, Email: r.Email, Phone: r.Phone,
		AccountType: r.AccountType, BirthDate: formatBirthDate(r.BirthDate),
		Message: r.Message, Source: r.Source, RequestedBy: r.RequestedBy,
		GroupID: r.GroupID, Status: r.Status, HasWarnings: flags.HasWarnings() || r.AccountType == models.AccountTeen,
		Warnings: flags, DecisionNote: r.DecisionNote,
		ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt, DecidedAt: r.DecidedAt,
	}
	if r.RequestedBy != nil {
		out.RequestedByName = nameIn(a.DB, *r.RequestedBy)
	}
	return out
}

// accountRequestHit counts one public-form submission from ip and
// reports the running count in the last hour (login_throttles, own key).
func (a *App) accountRequestHit(ctx context.Context, ip string) (int, error) {
	var failures int
	windowArg := fmt.Sprintf("%d seconds", int(time.Hour.Seconds()))
	err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Raw(`
INSERT INTO login_throttles (key, failures, window_start) VALUES (?, 1, now())
ON CONFLICT (key) DO UPDATE SET
  failures = CASE WHEN login_throttles.window_start < now() - ?::interval
                  THEN 1 ELSE login_throttles.failures + 1 END,
  window_start = CASE WHEN login_throttles.window_start < now() - ?::interval
                      THEN now() ELSE login_throttles.window_start END
RETURNING failures`, "acctreq:ip:"+ip, windowArg, windowArg).Scan(&failures).Error
	})
	return failures, err
}

type accountRequestInput struct {
	Name      string `json:"name"`
	Email     string `json:"email"`
	Phone     string `json:"phone"`
	AgeBand   string `json:"ageBand"`
	BirthDate string `json:"birthDate"`
	Message   string `json:"message"`
	Website   string `json:"website"` // honeypot: people never fill this in
}

// validated is the cleaned input plus the age gate's verdict.
type validatedRequest struct {
	name, email, phone, message string
	accountType                 string
	birthDate                   *time.Time
	honeypot                    bool
}

// validateAccountRequest cleans one door's input and runs the age gate
// (DEC-8). Under 13 answers 400 with the household explanation and the
// caller stores nothing.
func validateAccountRequest(c *gin.Context, body accountRequestInput) (validatedRequest, bool) {
	name := strings.TrimSpace(body.Name)
	if name == "" || len(name) > 100 {
		jsonError(c, http.StatusBadRequest, "name is required (at most 100 characters)")
		return validatedRequest{}, false
	}
	email := normalizeEmail(body.Email)
	if !validEmail(email) {
		jsonError(c, http.StatusBadRequest, "enter a valid email address")
		return validatedRequest{}, false
	}
	phone := strings.TrimSpace(body.Phone)
	if len(phone) > 30 {
		jsonError(c, http.StatusBadRequest, "phone must be at most 30 characters")
		return validatedRequest{}, false
	}
	message := strings.TrimSpace(body.Message)
	if len(message) > maxRequestMessageLen {
		jsonError(c, http.StatusBadRequest, "message must be at most 1000 characters")
		return validatedRequest{}, false
	}
	birth, err := accounts.ParseBirthDate(strings.TrimSpace(body.BirthDate))
	if err != nil {
		jsonError(c, http.StatusBadRequest, "birth date must be a real date (YYYY-MM-DD)")
		return validatedRequest{}, false
	}
	accountType, err := accounts.Classify(strings.TrimSpace(body.AgeBand), birth, time.Now().UTC())
	if errors.Is(err, accounts.ErrUnder13) {
		jsonError(c, http.StatusBadRequest,
			"Fold accounts start at 13. A parent or guardian can add a younger child to their household and sign them up for events.")
		return validatedRequest{}, false
	}
	if err != nil {
		jsonError(c, http.StatusBadRequest,
			"pick 18 or older, or 13 to 17 with a birth date that matches")
		return validatedRequest{}, false
	}
	if accountType == models.AccountAdult {
		birth = nil // adults only confirm 18+; no date is kept (5.3)
	}
	return validatedRequest{
		name: name, email: email, phone: phone, message: message,
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
	hits, err := a.accountRequestHit(ctx, c.ClientIP())
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save your request")
		return false
	}
	if hits > accountRequestIPLimit {
		jsonError(c, http.StatusTooManyRequests, "too many requests from this connection; try again in an hour")
		return false
	}
	var existing models.User
	hasAccount := a.DB.Where("email = ?", v.email).Limit(1).Find(&existing).RowsAffected == 1
	if hasAccount {
		if source == models.RequestMemberInvite {
			// An authenticated member learns what an inviter learns
			// (M1.2 invitations answer 409 too); no email is sent.
			jsonError(c, http.StatusConflict, "that email already has an account")
			return false
		}
		// Same answer and about the same cost as a stored request; the
		// hint travels by email instead (10.1).
		(&models.User{}).CheckPassword("not a real password")
		if err := a.sendAlreadyHaveAccountEmail(v.email); err != nil {
			log.Printf("account-exists email to %s: %v", v.email, err)
		}
		c.JSON(http.StatusOK, gin.H{"message": signupThanks})
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
		Name: v.name, Email: v.email, Phone: v.phone,
		AccountType: v.accountType, BirthDate: v.birthDate, Message: v.message,
		Source: source, RequestedBy: requestedBy, Status: models.RequestPending,
		Flags: string(flagsJSON), IP: c.ClientIP(),
		ExpiresAt: time.Now().UTC().Add(requestLifetime),
	}
	var emailed []uint
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
		ids, err := notifyAccountRequested(tx, &req, flags)
		emailed = ids
		return err
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save your request")
		return false
	}
	a.deliverNotificationEmails(emailed)
	if source == models.RequestSelfSignup {
		if err := a.sendRequestReceivedEmail(v.email); err != nil {
			log.Printf("request-received email to %s: %v", v.email, err)
		}
		c.JSON(http.StatusOK, gin.H{"message": signupThanks})
	} else {
		c.JSON(http.StatusOK, gin.H{"message": "Thanks — an admin will review it, and your friend will hear from us then."})
	}
	return true
}

// apiCreateAccountRequest is the public self-signup door (DEC-4): it
// stores a pending request and always answers the same way.
func (a *App) apiCreateAccountRequest(c *gin.Context) {
	if a.selfSignupMode() == selfSignupOff {
		jsonError(c, http.StatusForbidden, "self-signup is turned off; ask your church office for an account")
		return
	}
	var body accountRequestInput
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	v, ok := validateAccountRequest(c, body)
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
	v, ok := validateAccountRequest(c, body)
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
	out := make([]accountRequestJSON, 0, len(rows))
	for i := range rows {
		out = append(out, a.toAccountRequestJSON(&rows[i]))
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
		Note             string `json:"note"`
		FromNotification *uint  `json:"fromNotification"`
	}
	_ = c.ShouldBindJSON(&body)
	if !a.fromNotification(c, body.FromNotification, notify.ActionApproveAccount, req.ID) {
		return
	}
	if body.FromNotification != nil &&
		!policy.InlineApproveAccountOK(req.AccountType, parseRequestFlags(req.Flags).HasWarnings()) {
		jsonError(c, http.StatusForbidden, "that request needs review in the admin queue")
		return
	}
	a.decideAccountRequest(c, req, true, strings.TrimSpace(body.Note))
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
	a.decideAccountRequest(c, req, false, strings.TrimSpace(body.Note))
}

var errRequestHandled = errors.New("account request already decided")

func (a *App) decideAccountRequest(c *gin.Context, req *models.AccountRequest, approve bool, note string) {
	if len(note) > 500 {
		jsonError(c, http.StatusBadRequest, "note must be at most 500 characters")
		return
	}
	me := apiUser(c)
	if req.Status == models.RequestPending && !req.ExpiresAt.After(time.Now().UTC()) {
		if _, err := a.expirePendingRequests(); err != nil {
			log.Printf("account requests: expire: %v", err)
		}
		jsonError(c, http.StatusConflict, "that request has expired")
		return
	}
	outcome, auditAction := "rejected", audit.AccountRejected
	if approve {
		outcome, auditAction = "approved", audit.AccountApproved
	}
	var userID uint
	var created bool
	var raw string
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		status := models.RequestRejected
		if approve {
			status = models.RequestApproved
		}
		res := tx.Model(&models.AccountRequest{}).
			Where("id = ? AND status = ?", req.ID, models.RequestPending).
			Updates(map[string]any{
				"status": status, "decided_by": me.ID, "decided_at": now, "decision_note": note,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errRequestHandled
		}
		meta := map[string]any{"accountType": req.AccountType}
		if approve {
			var existing models.User
			if tx.Where("email = ?", req.Email).Limit(1).Find(&existing).RowsAffected == 1 {
				userID = existing.ID
			} else {
				status := models.UserInvited
				if req.AccountType == models.AccountTeen {
					// Consent finishes in M1.10; until then the teen
					// cannot sign in (CanSignIn needs active).
					status = models.UserPendingConsent
				}
				u := models.User{
					Email: req.Email, Name: req.Name, Phone: req.Phone,
					Role: models.RoleMember, Status: status,
					AccountType: req.AccountType, BirthDate: req.BirthDate,
				}
				if err := tx.Create(&u).Error; err != nil {
					return err
				}
				userID, created = u.ID, true
				var err error
				raw, err = createAccountToken(tx, models.TokenAccountSetup, u.ID, nil, tokenTTL)
				if err != nil {
					return err
				}
			}
			meta["userId"] = userID
			meta["created"] = created
		}
		if _, err := notify.Resolve(tx, notify.AccountRequested, req.ID, &me.ID, outcome); err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: auditAction,
			TargetType: "account_request", TargetID: req.ID, Meta: meta, IP: c.ClientIP()})
	})
	if errors.Is(err, errRequestHandled) {
		a.alreadyHandled(c, notify.AccountRequested, req.ID)
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not decide that request")
		return
	}
	// Decision emails go out after the decision is committed; the
	// person is emailed either way (M1.3 acceptance).
	if approve {
		if created {
			if err := a.sendAccountApprovedEmail(req, raw); err != nil {
				log.Printf("account approved email to %s: %v", req.Email, err)
			}
		} else if err := a.sendAlreadyHaveAccountEmail(req.Email); err != nil {
			log.Printf("account-exists email to %s: %v", req.Email, err)
		}
	} else if err := a.sendAccountRejectedEmail(req, note); err != nil {
		log.Printf("account rejected email to %s: %v", req.Email, err)
	}
	var fresh models.AccountRequest
	a.DB.First(&fresh, req.ID)
	c.JSON(http.StatusOK, gin.H{"request": a.toAccountRequestJSON(&fresh), "userId": userID})
}

// apiConfirmAccountSetup redeems the emailed set-password link. An
// adult signs in at once; a teen keeps pending_consent and is not
// signed in until consent is granted (M1.10).
func (a *App) apiConfirmAccountSetup(c *gin.Context) {
	var body struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Token == "" || body.Password == "" {
		jsonError(c, http.StatusBadRequest, "token and password are required")
		return
	}
	if len(body.Password) < minPasswordLength {
		jsonError(c, http.StatusBadRequest, "password must be at least 10 characters")
		return
	}
	var userID uint
	var pendingConsent bool
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		tok, err := consumeAccountToken(tx, models.TokenAccountSetup, body.Token)
		if err != nil {
			return err
		}
		var u models.User
		if err := tx.First(&u, tok.UserID).Error; err != nil || u.Status == models.UserDisabled {
			return errTokenInvalid
		}
		if err := u.SetPassword(body.Password); err != nil {
			return err
		}
		userID = u.ID
		pendingConsent = u.Status == models.UserPendingConsent
		updates := map[string]any{
			"password_hash":   *u.PasswordHash,
			"session_version": gorm.Expr("session_version + 1"),
		}
		if u.Status == models.UserInvited {
			updates["status"] = models.UserActive
		}
		if err := tx.Model(&u).Updates(updates).Error; err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &u.ID, Action: audit.AccountSetupCompleted,
			TargetType: "user", TargetID: u.ID, IP: c.ClientIP()})
	})
	if errors.Is(err, errTokenInvalid) {
		jsonError(c, http.StatusBadRequest, "that link is invalid or has expired")
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not set your password")
		return
	}
	var fresh models.User
	if err := a.DB.First(&fresh, userID).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not set your password")
		return
	}
	if pendingConsent {
		c.JSON(http.StatusOK, gin.H{"user": a.toUserJSON(&fresh), "pendingConsent": true})
		return
	}
	if err := a.signInSession(c, &fresh); err != nil {
		jsonError(c, http.StatusInternalServerError, "session error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": a.toUserJSON(&fresh)})
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
		if err := tx.Where(models.Setting{Key: settingSelfSignup}).
			Assign(models.Setting{Value: body.Mode}).
			FirstOrCreate(&models.Setting{}).Error; err != nil {
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
// lazily from the admin queue and daily from the ticker (5.5).
func (a *App) expirePendingRequests() (int64, error) {
	var ids []uint
	a.DB.Model(&models.AccountRequest{}).
		Where("status = ? AND expires_at <= now()", models.RequestPending).Pluck("id", &ids)
	var done int64
	for _, id := range ids {
		err := a.DB.Transaction(func(tx *gorm.DB) error {
			res := tx.Model(&models.AccountRequest{}).
				Where("id = ? AND status = ?", id, models.RequestPending).
				Update("status", models.RequestExpired)
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

// startAccountRequestExpiry expires overdue requests at startup and
// then daily (5.5: the daily ticker until the jobs table, M3.1).
func (a *App) startAccountRequestExpiry(ctx context.Context) {
	go func() {
		run := func() {
			if n, err := a.expirePendingRequests(); err != nil {
				log.Printf("account requests: expire: %v", err)
			} else if n > 0 {
				log.Printf("account requests: expired %d", n)
			}
		}
		run()
		tick := time.NewTicker(24 * time.Hour)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				run()
			}
		}
	}()
}

// ---------------------------------------------------------------------------
// Decision and hint emails (C6: plain text through the minimal sender)
// ---------------------------------------------------------------------------

func (a *App) sendRequestReceivedEmail(to string) error {
	return a.Mailer.Send(context.Background(), foldmail.Message{
		To:      to,
		Subject: fmt.Sprintf("We got your %s account request", a.siteName()),
		Text: fmt.Sprintf(`Thanks for asking for an account at %s.

An admin will review your request. We'll email you again when there's a decision — if you're approved, that email has the link to set your password.

If that wasn't you, you can ignore this email.`,
			a.siteName()),
	})
}

func (a *App) sendAlreadyHaveAccountEmail(to string) error {
	return a.Mailer.Send(context.Background(), foldmail.Message{
		To:      to,
		Subject: fmt.Sprintf("You already have a %s account", a.siteName()),
		Text: fmt.Sprintf(`Someone asked for a %s account with this email address, but you already have one.

Sign in here:
%s/login

Forgot your password? Reset it here:
%s/forgot-password`,
			a.siteName(), a.emailBaseURL(), a.emailBaseURL()),
	})
}

func (a *App) sendAccountApprovedEmail(req *models.AccountRequest, raw string) error {
	link := a.emailBaseURL() + "/set-password?token=" + raw
	extra := ""
	if req.AccountType == models.AccountTeen {
		extra = "\n\nBecause you're 13 to 17, your account also needs a parent or guardian's consent before you can sign in. Setting your password now is still the right first step."
	}
	return a.Mailer.Send(context.Background(), foldmail.Message{
		To:      req.Email,
		Subject: fmt.Sprintf("Your %s account is approved", a.siteName()),
		Text: fmt.Sprintf(`Good news — your account at %s is approved.

Set your password here (the link works once and expires in 24 hours):
%s%s

After that you can sign in with this email address.`, a.siteName(), link, extra),
	})
}

func (a *App) sendAccountRejectedEmail(req *models.AccountRequest, note string) error {
	notePart := ""
	if note != "" {
		notePart = "\n\nNote from the church office: " + note
	}
	return a.Mailer.Send(context.Background(), foldmail.Message{
		To:      req.Email,
		Subject: fmt.Sprintf("About your %s account request", a.siteName()),
		Text: fmt.Sprintf(`Thanks for asking for an account at %s.

We're not able to approve this request.%s

If you think this is a mistake, contact your church office and they'll help.`, a.siteName(), notePart),
	})
}
