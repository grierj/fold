package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"fold/internal/jobs"
	"fold/internal/mail"
	"fold/internal/policy"
	"fold/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	kindPasswordResetMail  = "account.password_reset_mail"
	kindAccountRequestMail = "account.request_mail"
	kindAccountExistsMail  = "account.exists_mail"
	kindInvitationMail     = "invitation.mail"
	directMailAttempts     = 8
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

func (a *App) mailKinds() []jobs.Kind {
	return []jobs.Kind{{
		Name: kindPasswordResetMail, Label: "Sending a password reset link", MaxAttempts: directMailAttempts,
		Run: func(ctx context.Context, payload json.RawMessage) error {
			var p userMailJob
			if err := json.Unmarshal(payload, &p); err != nil {
				return err
			}
			return a.prepareAndSend(ctx, kindPasswordResetMail, p.UserID, func(tx *gorm.DB) (mail.Message, bool, error) {
				return a.preparePasswordReset(tx, p.UserID)
			})
		},
	}, {
		Name: kindAccountRequestMail, Label: "Sending an account request email", MaxAttempts: directMailAttempts,
		Run: func(ctx context.Context, payload json.RawMessage) error {
			var p requestMailJob
			if err := json.Unmarshal(payload, &p); err != nil {
				return err
			}
			return a.prepareAndSend(ctx, kindAccountRequestMail, p.RequestID, func(tx *gorm.DB) (mail.Message, bool, error) {
				return a.prepareAccountRequestMail(tx, p.RequestID, p.Case)
			})
		},
	}, {
		Name: kindAccountExistsMail, Label: "Sending an already-have-an-account email", MaxAttempts: directMailAttempts,
		Run: func(ctx context.Context, payload json.RawMessage) error {
			var p userMailJob
			if err := json.Unmarshal(payload, &p); err != nil {
				return err
			}
			return a.prepareAndSend(ctx, kindAccountExistsMail, p.UserID, func(tx *gorm.DB) (mail.Message, bool, error) {
				return a.prepareExistingAccountHint(tx, p.UserID)
			})
		},
	}, {
		Name: kindInvitationMail, Label: "Sending an account invitation", MaxAttempts: directMailAttempts,
		Run: func(ctx context.Context, payload json.RawMessage) error {
			var p invitationMailJob
			if err := json.Unmarshal(payload, &p); err != nil {
				return err
			}
			return a.prepareAndSend(ctx, kindInvitationMail, p.InvitationID, func(tx *gorm.DB) (mail.Message, bool, error) {
				return a.prepareInvitationMail(tx, p.InvitationID)
			})
		},
	}}
}

// prepareInvitationMail locks only the invitation. Acceptance takes earlier
// user, household, group and email locks, so taking any of those here would
// reverse its lock order. Authority reads are checked but remain read-only;
// acceptance checks them again before granting anything.
func (a *App) prepareInvitationMail(tx *gorm.DB, id uint) (mail.Message, bool, error) {
	none := mail.Message{}
	var inv models.Invitation
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()").First(&inv, id).Error
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
	raw, err := createAccountToken(tx, models.TokenPasswordReset, user.ID, nil, tokenTTL)
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
		if raw, err = createAccountToken(tx, models.TokenAccountSetup, user.ID, nil, tokenTTL); err != nil {
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
