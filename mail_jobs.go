package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"time"

	"fold/internal/events"
	"fold/internal/jobs"
	"fold/internal/mail"
	"fold/internal/policy"
	"fold/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	kindPasswordResetMail   = "account.password_reset_mail"
	kindAccountRequestMail  = "account.request_mail"
	kindAccountExistsMail   = "account.exists_mail"
	kindInvitationMail      = "invitation.mail"
	kindEmailVerifyMail     = "account.email_verify_mail"
	kindGuardianConsentMail = "guardian.consent_mail"
	kindGuestVerifyMail     = "guest.verify_mail"
)

// directMailAttempts is the attempts of every direct-mail job: eight on
// jobs.Backoff span about an hour.
const directMailAttempts = 8

func directMail(kind string) jobs.Type { return jobs.Type{Name: kind, MaxAttempts: directMailAttempts} }

// The direct-mail job types, queued with <type>.Enqueue(tx, <payload>, now, "").
var (
	passwordResetMailType   = directMail(kindPasswordResetMail)
	accountRequestMailType  = directMail(kindAccountRequestMail)
	accountExistsMailType   = directMail(kindAccountExistsMail)
	invitationMailType      = directMail(kindInvitationMail)
	emailVerifyMailType     = directMail(kindEmailVerifyMail)
	guardianConsentMailType = directMail(kindGuardianConsentMail)
	guestVerifyMailType     = directMail(kindGuestVerifyMail)
)

// Account-request mail cases. The case is the only non-ID payload value: it
// names which stored decision the job reports, never any rendered content.
const (
	requestMailReceived           = "received"
	requestMailApproved           = "approved"
	requestMailExists             = "exists"
	requestMailConversionApproved = "conversion_approved"
	requestMailRejected           = "rejected"
)

// Direct-mail jobs carry IDs only. Capabilities are minted in preparation,
// committed, and kept only in memory until the provider send finishes.
type userMailJob struct {
	UserID uint `json:"userId"`
}

type requestMailJob struct {
	RequestID uint   `json:"requestId"`
	Case      string `json:"case"`
}

type invitationMailJob struct {
	InvitationID uint `json:"invitationId"`
}

// accountTokenMailJob names one stored request, not a person: it identifies
// the verification row so a retry can rotate its hash and nothing else.
type accountTokenMailJob struct {
	AccountTokenID uint `json:"accountTokenId"`
}

type guardianConsentMailJob struct {
	GuardianLinkID uint `json:"guardianLinkId"`
}

// guestVerifyMailJob names the stored signup, never the visitor: the address
// and name stay in guest_signups until delivery.
type guestVerifyMailJob struct {
	GuestSignupID uint `json:"guestSignupId"`
}

// idMailKind registers one direct-mail kind whose payload P names a stored
// row by id. prep renders from the row as it is now, or reports false when
// nothing should be sent.
func idMailKind[P any](a *App, t jobs.Type, label string, id func(P) uint, prep func(tx *gorm.DB, p P) (mail.Message, bool, error)) jobs.Kind {
	return jobs.Kind{Type: t, Label: label,
		Run: func(ctx context.Context, payload json.RawMessage) error {
			var p P
			if err := json.Unmarshal(payload, &p); err != nil {
				return err
			}
			return a.prepareAndSend(ctx, t.Name, id(p), func(tx *gorm.DB) (mail.Message, bool, error) { return prep(tx, p) })
		}}
}

func (a *App) mailKinds() []jobs.Kind {
	return []jobs.Kind{
		idMailKind(a, passwordResetMailType, "Sending a password reset link",
			func(p userMailJob) uint { return p.UserID },
			func(tx *gorm.DB, p userMailJob) (mail.Message, bool, error) {
				return a.preparePasswordReset(tx, p.UserID)
			}),
		idMailKind(a, accountRequestMailType, "Sending an account request email",
			func(p requestMailJob) uint { return p.RequestID },
			func(tx *gorm.DB, p requestMailJob) (mail.Message, bool, error) {
				return a.prepareAccountRequestMail(tx, p.RequestID, p.Case)
			}),
		idMailKind(a, accountExistsMailType, "Sending an already-have-an-account email",
			func(p userMailJob) uint { return p.UserID },
			func(tx *gorm.DB, p userMailJob) (mail.Message, bool, error) {
				return a.prepareExistingAccountHint(tx, p.UserID)
			}),
		idMailKind(a, invitationMailType, "Sending an account invitation",
			func(p invitationMailJob) uint { return p.InvitationID },
			func(tx *gorm.DB, p invitationMailJob) (mail.Message, bool, error) {
				return a.prepareInvitationMail(tx, p.InvitationID)
			}),
		idMailKind(a, emailVerifyMailType, "Sending an email change verification link",
			func(p accountTokenMailJob) uint { return p.AccountTokenID },
			func(tx *gorm.DB, p accountTokenMailJob) (mail.Message, bool, error) {
				return a.prepareEmailVerification(tx, p.AccountTokenID)
			}),
		idMailKind(a, guardianConsentMailType, "Sending a guardian consent link",
			func(p guardianConsentMailJob) uint { return p.GuardianLinkID },
			func(tx *gorm.DB, p guardianConsentMailJob) (mail.Message, bool, error) {
				return a.prepareGuardianConsentMail(tx, p.GuardianLinkID)
			}),
		idMailKind(a, guestVerifyMailType, "Sending a guest registration confirmation link",
			func(p guestVerifyMailJob) uint { return p.GuestSignupID },
			func(tx *gorm.DB, p guestVerifyMailJob) (mail.Message, bool, error) {
				return a.prepareGuestVerification(tx, p.GuestSignupID)
			}),
	}
}

// prepareGuestVerification rotates the hash of one pending guest signup and
// renders its link from the event as it is now. Redemption reads the signup
// without a lock, locks the occurrence, and only then writes the signup row, so
// this takes the signup row alone and never an occurrence, event or group lock:
// no reverse order exists. Anything that closed the door (confirmed, expired,
// date cancelled, event or group tightened or gone) leaves the row untouched
// and finishes quietly, using redemption's own guest rule. The deadline never
// moves, so a retry replaces the link without extending the window.
func (a *App) prepareGuestVerification(tx *gorm.DB, signupID uint) (mail.Message, bool, error) {
	none := mail.Message{}
	var signup models.GuestSignup
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("confirmed_at IS NULL AND "+sqlStillLive).First(&signup, signupID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return none, false, nil
	}
	if err != nil {
		return none, false, err
	}
	var occ models.EventOccurrence
	err = tx.First(&occ, signup.OccurrenceID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return none, false, nil
	}
	if err != nil {
		return none, false, err
	}
	if occ.Status != models.OccurrenceScheduled {
		return none, false, nil
	}
	var ev models.Event
	err = tx.First(&ev, occ.EventID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return none, false, nil
	}
	if err != nil {
		return none, false, err
	}
	var grp *models.Group
	gp := policy.Group{}
	if ev.GroupID != nil {
		var g models.Group
		err := tx.First(&g, *ev.GroupID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return none, false, nil
		}
		if err != nil {
			return none, false, err
		}
		grp, gp = &g, asGroup(&g)
	}
	// The same rule redemption runs, on the date's effective visibility.
	if !policy.CanRegisterGuest(viewOf(dateEvent(&ev, &occ), false), gp) {
		return none, false, nil
	}
	site, err := siteNameIn(tx)
	if err != nil {
		return none, false, err
	}
	loc, err := events.LoadZone(signup.Timezone)
	if err != nil {
		// The guest's own zone is unusable: read the church's. A failed read
		// retries the job.
		if loc, err = churchZoneLenient(tx); err != nil {
			return none, false, err
		}
	}
	raw, err := rotateGuestSignupToken(tx, signup.ID)
	if errors.Is(err, errTokenInvalid) {
		return none, false, nil
	}
	if err != nil {
		return none, false, err
	}
	d := a.guestDisplayWith(func() string { return site }, &models.Registration{Status: models.RegGoing}, &occ, &ev, grp)
	return a.guestVerificationMessage(&signup, raw, site, d, loc), true, nil
}

func (a *App) guestVerificationMessage(signup *models.GuestSignup, raw, site string, d guestRegistrationJSON, loc *time.Location) mail.Message {
	link := a.emailBaseURL() + "/guest/confirm?token=" + raw
	return mail.Message{
		To:      signup.Email,
		Subject: fmt.Sprintf("Confirm your registration for %s", d.Title),
		Text: fmt.Sprintf(`Hi %s,

Someone used this email address to register as a guest for %s%s at %s, on %s.

Confirm your registration here (the link works once and expires within 24 hours):
%s

If that wasn't you, ignore this email and nothing will be registered.`,
			signup.Name, d.Title, guestWithLine(d), site, formatWhen(d.StartsAt, loc), link),
	}
}

// Preparation shares redemption's order: contact gate, users in ID order
// (and any conversion household), link, then token. Discover the relationship
// without locking it first; lockedTeenGuardian rechecks its current authority.
func (a *App) prepareGuardianConsentMail(tx *gorm.DB, linkID uint) (mail.Message, bool, error) {
	none := mail.Message{}
	if err := lockContacts(tx); err != nil {
		return none, false, err
	}
	var link models.GuardianLink
	err := tx.First(&link, linkID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return none, false, nil
	}
	if err != nil {
		return none, false, err
	}
	if link.Status != models.GuardianConfirmed || link.TeenUserID == nil {
		return none, false, nil
	}
	guardian, teen, _, err := lockedTeenGuardian(tx, link.GuardianUserID, *link.TeenUserID, link.ID)
	if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, errGuardianUnavailable) || errors.Is(err, errConsentForbidden) {
		return none, false, nil
	}
	if err != nil {
		return none, false, err
	}
	raw, err := a.createGuardianEmailToken(tx, guardian, teen, link.ID)
	if err != nil {
		return none, false, err
	}
	// Safeguarding mail names nobody. GET only previews the signed action;
	// the existing explicit POST is still the only way to give consent.
	return mail.Message{To: guardian.Email, Subject: "Review teen account consent in Fold",
		Text: "An admin confirmed your guardian link. Review the teen account and choose whether to give account consent:\n\n" + a.emailBaseURL() + "/guardian-consent?token=" + url.QueryEscape(raw) + "\n\nThis link expires in 24 hours and can be used once. Opening it does not give consent. You can also manage consent from your Fold profile."}, true, nil
}

// prepareEmailVerification rotates the hash of one stored verification token
// and renders the link for the address in its payload. The lock order matches
// redemption and cancellation: contact gate, token read, user lock, then the
// conditional write. Anything that closed the request (cancelled, verified,
// superseded, expired, account disabled or deleted) leaves the row unchanged
// and finishes quietly. The deadline never moves: a retry replaces the link
// but cannot outlive the cancellation window set at request time.
func (a *App) prepareEmailVerification(tx *gorm.DB, tokenID uint) (mail.Message, bool, error) {
	none := mail.Message{}
	if err := lockContacts(tx); err != nil {
		return none, false, err
	}
	var tok models.AccountToken
	err := tx.Where("kind = ? AND used_at IS NULL AND "+sqlStillLive, models.TokenEmailVerify).First(&tok, tokenID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return none, false, nil
	}
	if err != nil {
		return none, false, err
	}
	var user models.User
	err = tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&user, tok.UserID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return none, false, nil
	}
	if err != nil {
		return none, false, err
	}
	if !user.CanSignIn() {
		return none, false, nil
	}
	var payload struct {
		NewEmail string `json:"new_email"`
	}
	if err := json.Unmarshal([]byte(tok.Payload), &payload); err != nil || !validEmail(payload.NewEmail) {
		return none, false, nil
	}
	raw, changed, err := rotateAccountToken(tx, tok.ID, models.TokenEmailVerify, user.ID)
	if err != nil || !changed {
		return none, false, err
	}
	site, err := siteNameIn(tx)
	if err != nil {
		return none, false, err
	}
	return a.emailVerificationMessage(payload.NewEmail, raw, site), true, nil
}

func (a *App) emailVerificationMessage(to, raw, site string) mail.Message {
	link := a.emailBaseURL() + "/verify-email?token=" + raw
	return mail.Message{
		To:      to,
		Subject: "Verify your new Fold email",
		Text: fmt.Sprintf(`Someone asked to change the sign-in email for an account at %s to this address.

Verify it here (the link works once and expires within 24 hours):
%s

The change takes effect only after you use the link. If that wasn't you, you can ignore this email.`,
			site, link),
	}
}

// prepareInvitationMail locks only the invitation. Acceptance takes earlier
// user, household, group and email locks, so taking any of those here would
// reverse its lock order. Authority reads are checked but remain read-only;
// acceptance checks them again before granting anything.
func (a *App) prepareInvitationMail(tx *gorm.DB, id uint) (mail.Message, bool, error) {
	none := mail.Message{}
	var inv models.Invitation
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("accepted_at IS NULL AND revoked_at IS NULL AND "+sqlStillLive).First(&inv, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return none, false, nil
	}
	if err != nil {
		return none, false, err
	}
	var inviter models.User
	err = tx.Where("status = ?", models.UserActive).First(&inviter, inv.InvitedBy).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return none, false, nil
	}
	if err != nil {
		return none, false, err
	}
	if inv.HouseholdPersonID != nil {
		var person models.HouseholdPerson
		err := tx.First(&person, *inv.HouseholdPersonID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return none, false, nil
		}
		if err != nil {
			return none, false, err
		}
		allowed, err := a.conversionAllowed(tx, &inviter, &person)
		if errors.Is(err, errConversionHolder) {
			return none, false, nil
		}
		if err != nil || !allowed {
			return none, false, err
		}
		disabled, err := conversionInviterDisabled(tx, &inv)
		if err != nil || disabled {
			return none, false, err
		}
		inv.Name, inv.AccountType = person.FullName(), person.Kind
	} else {
		leadsTarget, leadsAnywhere := false, false
		if inv.GroupID != nil {
			err := tx.First(&inv.Group, *inv.GroupID).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return none, false, nil
			}
			if err != nil {
				return none, false, err
			}
			if !policy.CanInviteIntoGroup(inv.Group.Status) {
				return none, false, nil
			}
			var membership models.Membership
			err = tx.Where("user_id = ? AND group_id = ?", inviter.ID, inv.Group.ID).First(&membership).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return none, false, err
			}
			leadsTarget = err == nil && membershipRel(membership).Leader
		} else {
			var err error
			if leadsAnywhere, err = leadsApprovedGroup(tx, inviter.ID); err != nil {
				return none, false, err
			}
		}
		if !policy.CanInvite(asPerson(&inviter), inv.GroupID != nil, leadsTarget, leadsAnywhere) {
			return none, false, nil
		}
		// Capacity is checked at acceptance. A temporarily full group does
		// not make an invitation permanently ineligible for delivery.
	}
	raw, err := rotateInvitationToken(tx, inv.ID)
	if errors.Is(err, errTokenInvalid) {
		return none, false, nil
	}
	if err != nil {
		return none, false, err
	}
	site, err := siteNameIn(tx)
	if err != nil {
		return none, false, err
	}
	return a.invitationMessage(&inv, &inviter, raw, site), true, nil
}

func (a *App) invitationMessage(inv *models.Invitation, inviter *models.User, raw, site string) mail.Message {
	link := a.emailBaseURL() + "/invite?token=" + raw
	if inv.HouseholdPersonID != nil {
		timing := "Your past and upcoming event sign-ups will move to your account after an admin approves. Until then, your household keeps managing them."
		if inv.AccountType == models.AccountTeen {
			timing = "Your account needs admin approval, consent, and a fresh check of your guardian link. Your household keeps managing your event sign-ups until all three are complete."
		}
		return mail.Message{To: inv.Email, Subject: "Create your own account at " + site,
			Text: fmt.Sprintf("%s invited you to create an account for %s. %s\n\nChoose your password: %s\n\nThis link expires within 24 hours. If this invitation is not for you, ignore it.", inviter.PublicName(), inv.Name, timing, link)}
	}
	groupPart := ""
	if inv.Group.ID != 0 {
		groupPart = fmt.Sprintf(" at %s", inv.Group.Name)
	}
	return mail.Message{To: inv.Email, Subject: fmt.Sprintf("You've been invited to %s", site),
		Text: fmt.Sprintf(`%s invited you to join %s%s.

Set your password here (the link works once and expires within 24 hours):
%s

If you weren't expecting this, you can ignore it.`, inviter.PublicName(), site, groupPart, link)}
}

// prepareAndSend commits the capability before contacting the provider. No
// database lock spans provider I/O. A retry prepares a fresh capability,
// invalidating the previous attempt's link even if that mail was accepted.
// Preparation reads, including siteNameIn, must use the supplied transaction;
// message constructors take those values without querying the database.
func (a *App) prepareAndSend(ctx context.Context, kind string, subjectID uint, prep func(*gorm.DB) (mail.Message, bool, error)) error {
	var msg mail.Message
	var send bool
	err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		msg, send, err = prep(tx)
		return err
	})
	if err != nil {
		log.Printf("mail: %s #%d preparation: %s", kind, subjectID, jobs.CleanError(err))
		return err
	}
	if !send {
		return nil
	}
	return a.sendDirect(ctx, kind, subjectID, msg)
}

// sendDirect uses the configured suppression-aware sender for required account
// mail. Permanent outcomes finish quietly; the runner retries transient errors
// and cancellation, and alerts admins if all attempts fail.
func (a *App) sendDirect(ctx context.Context, kind string, subjectID uint, msg mail.Message) error {
	if a.Mailer == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, deliverSendTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	msg.Optional = false
	err := a.Mailer.Send(ctx, msg)
	if err == nil {
		return nil
	}
	log.Printf("mail: %s #%d: %s", kind, subjectID, jobs.CleanError(err))
	if mail.NotConfigured(err) || mail.IsSuppressed(err) || mail.IsPermanent(err) {
		return nil
	}
	return err
}

func (a *App) preparePasswordReset(tx *gorm.DB, userID uint) (mail.Message, bool, error) {
	if err := lockContacts(tx); err != nil {
		return mail.Message{}, false, err
	}
	var user models.User
	if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&user, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return mail.Message{}, false, nil
		}
		return mail.Message{}, false, err
	}
	if user.Status != models.UserActive {
		return mail.Message{}, false, nil
	}
	raw, _, err := issueAccountToken(tx, models.TokenPasswordReset, user.ID, nil, tokenTTL)
	if err != nil {
		return mail.Message{}, false, err
	}
	site, err := siteNameIn(tx)
	if err != nil {
		return mail.Message{}, false, err
	}
	return a.passwordResetMessage(user.Email, raw, site), true, nil
}

func (a *App) passwordResetMessage(to, raw, site string) mail.Message {
	link := a.emailBaseURL() + "/reset-password?token=" + raw
	return mail.Message{
		To:      to,
		Subject: "Reset your Fold password",
		Text: fmt.Sprintf(`Someone asked to reset the password for this email address at %s.

Reset it here (the link works once and expires in 24 hours):
%s

If that wasn't you, you can ignore this email — your password won't change.`,
			site, link),
	}
}

// existingNonGuestUser loads a user who is not deleted and not a guest.
// Disabled accounts stay eligible: the hint is informational and never
// grants anything, matching both public doors.
func existingNonGuestUser(tx *gorm.DB, userID uint) (*models.User, error) {
	var user models.User
	// A missing or soft-deleted user has nothing to send; any other database
	// error is returned so the job retries.
	err := tx.First(&user, userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if user.Status == models.UserGuest {
		return nil, nil
	}
	return &user, nil
}

// prepareExistingAccountHint addresses the hint to the user's current stored
// email, so an address change before delivery never mails the old one.
func (a *App) prepareExistingAccountHint(tx *gorm.DB, userID uint) (mail.Message, bool, error) {
	user, err := existingNonGuestUser(tx, userID)
	if err != nil || user == nil {
		return mail.Message{}, false, err
	}
	site, err := siteNameIn(tx)
	if err != nil {
		return mail.Message{}, false, err
	}
	return a.existingAccountMessage(user.Email, site), true, nil
}

// prepareAccountRequestMail rebuilds one account-request email from stored
// facts. Only the approved setup mail takes locks and mints a capability.
func (a *App) prepareAccountRequestMail(tx *gorm.DB, requestID uint, kind string) (mail.Message, bool, error) {
	none := mail.Message{}
	if kind == requestMailApproved {
		// Same order as setup redemption: contact gate, user lock, token.
		if err := lockContacts(tx); err != nil {
			return none, false, err
		}
	}
	var req models.AccountRequest
	err := tx.First(&req, requestID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return none, false, nil
	}
	if err != nil {
		return none, false, err
	}
	// An erased person's request carries a placeholder address: nothing goes
	// to it (rejected and conversion_approved would otherwise try).
	if isErasedAddress(req.Email) {
		return none, false, nil
	}
	conversion := req.Source == models.RequestHouseholdConversion
	switch kind {
	case requestMailReceived:
		if req.Status != models.RequestPending {
			return none, false, nil
		}
	case requestMailRejected:
		if req.Status != models.RequestRejected {
			return none, false, nil
		}
	case requestMailConversionApproved:
		if req.Status != models.RequestApproved || !conversion {
			return none, false, nil
		}
	case requestMailExists, requestMailApproved:
		if req.Status != models.RequestApproved || req.ApprovedUserID == nil || (kind == requestMailApproved && conversion) {
			return none, false, nil
		}
	default:
		return none, false, fmt.Errorf("unknown account request mail case %q", kind)
	}
	to, raw, needsConsent := req.Email, "", false
	switch kind {
	case requestMailExists:
		user, err := existingNonGuestUser(tx, *req.ApprovedUserID)
		if err != nil || user == nil {
			return none, false, err
		}
		to = user.Email
	case requestMailApproved:
		var user models.User
		err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&user, *req.ApprovedUserID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return none, false, nil
		}
		if err != nil {
			return none, false, err
		}
		switch user.Status {
		case models.UserInvited, models.UserPendingConsent, models.UserActive:
		default:
			return none, false, nil
		}
		if user.PasswordHash != nil {
			return none, false, nil
		}
		if raw, _, err = issueAccountToken(tx, models.TokenAccountSetup, user.ID, nil, tokenTTL); err != nil {
			return none, false, err
		}
		to = user.Email
		needsConsent = user.AccountType == models.AccountTeen && user.Status == models.UserPendingConsent
	}
	site, err := siteNameIn(tx)
	if err != nil {
		return none, false, err
	}
	switch kind {
	case requestMailReceived:
		return a.requestReceivedMessage(to, site), true, nil
	case requestMailRejected:
		return a.accountRejectedMessage(to, site, req.DecisionNote), true, nil
	case requestMailConversionApproved:
		return a.conversionApprovedMessage(to, site, req.AccountType == models.AccountTeen), true, nil
	case requestMailExists:
		return a.existingAccountMessage(to, site), true, nil
	}
	return a.accountApprovedMessage(to, raw, site, needsConsent), true, nil
}
