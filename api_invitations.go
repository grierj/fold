package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/mail"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ---------------------------------------------------------------------------
// Invitations (M1.2, 5.10, DEC-4): admin and leader invitations, with no
// approval step. The person sets a password on acceptance and gets an
// active account.
// ---------------------------------------------------------------------------

// leaderInviteDailyLimit: a leader may send 20 invitations per rolling day
// (10.1). The 21st gets 429 and is audited. Admins set the limit later;
// until then it is a constant.
const leaderInviteDailyLimit = 20

// writeInvitationRefusal spends only the actor allowance for a valid address
// check that sent no invitation. Ordinary and household invitations share it.
func writeInvitationRefusal(tx *gorm.DB, actor uint, reason, ip string) error {
	return audit.Write(tx, audit.Entry{Actor: &actor, Action: audit.InvitationRefused,
		TargetType: "user", TargetID: actor,
		Meta: map[string]any{"reason": reason, "limit": leaderInviteDailyLimit}, IP: ip})
}

// errEmailTaken: the address got an account after the invitation was sent.
var errEmailTaken = errors.New("email taken")

// errGroupNotActive: the invitation's group is no longer active.
var errGroupNotActive = errors.New("group not active")
var errInvitationLimit = errors.New("invitation limit reached (20 per day)")

type invitationJSON struct {
	ID          uint      `json:"id"`
	Email       string    `json:"email"`
	Name        string    `json:"name"`
	GroupID     *uint     `json:"groupId,omitempty"`
	GroupName   string    `json:"groupName,omitempty"`
	ExpiresAt   time.Time `json:"expiresAt"`
	Conversion  bool      `json:"conversion"`
	AccountType string    `json:"accountType"`
}

type invitationAcceptanceJSON struct {
	User           *userJSON `json:"user,omitempty"`
	PendingConsent bool      `json:"pendingConsent"`
	// PendingApproval: a converted adult waits for an admin (DEC-4).
	PendingApproval bool `json:"pendingApproval"`
}

type invitationContextJSON struct {
	Invitation  invitationJSON `json:"invitation"`
	InviterName string         `json:"inviterName"`
	SiteName    string         `json:"siteName"`
	Site        string         `json:"site"`
}

func (a *App) toInvitationJSON(inv *models.Invitation) invitationJSON {
	out := invitationJSON{
		ID: inv.ID, Email: inv.Email, Name: inv.Name,
		GroupID: inv.GroupID, ExpiresAt: inv.ExpiresAt,
		Conversion: inv.HouseholdPersonID != nil, AccountType: inv.AccountType,
	}
	if inv.Group.ID != 0 {
		out.GroupName = inv.Group.Name
	}
	return out
}

// apiCreateInvitation invites someone to Fold, optionally into a group.
// Admins may invite anyone, into any group or none; leaders only into a
// group they lead, or with no group (policy.CanInvite). A leader's 21st
// invitation in a day gets 429 and is audited.
func (a *App) apiCreateInvitation(c *gin.Context) {
	me := apiUser(c)
	var body struct {
		Email     string `json:"email"`
		Name      string `json:"name"`
		Phone     string `json:"phone"`
		GroupID   *uint  `json:"groupId"`
		OverLimit bool   `json:"overLimit"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	email := normalizeEmail(body.Email)
	if !validEmail(email) {
		jsonError(c, http.StatusBadRequest, "enter a valid email address")
		return
	}
	name := strings.TrimSpace(body.Name)
	if len(name) > 100 {
		jsonError(c, http.StatusBadRequest, "name must be at most 100 characters")
		return
	}
	phone := strings.TrimSpace(body.Phone)
	if len(phone) > 30 {
		jsonError(c, http.StatusBadRequest, "phone must be at most 30 characters")
		return
	}

	// The target group: 404 when it doesn't exist or the inviter can't see
	// it (invite-only groups never answer 403 to outsiders).
	var g *models.Group
	var rel policy.Rel
	if body.GroupID != nil {
		var gg *models.Group
		var ok bool
		if gg, rel, ok = a.loadGroupFor(c, *body.GroupID); !ok {
			return
		}
		g = gg
	}
	leadsTarget := rel.Leader
	leadsAnywhere := false
	if !leadsTarget && !asPerson(me).Admin {
		var n int64
		a.DB.Model(&models.Membership{}).
			Where("user_id = ? AND role = ? AND status = 'active'", me.ID, models.MembershipLeader).
			Count(&n)
		leadsAnywhere = n > 0
	}
	if !policy.CanInvite(asPerson(me), body.GroupID != nil, leadsTarget, leadsAnywhere) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return
	}

	var raw string
	var refusal error
	overLimit := body.OverLimit && g != nil
	inv := models.Invitation{
		Email: email, Phone: phone, Name: name, InvitedBy: me.ID,
		Role: models.MembershipMember, AccountType: "adult",
		GroupID: body.GroupID, OverLimit: overLimit,
		ExpiresAt: time.Now().UTC().Add(tokenTTL),
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockInvitationEmail(tx, email); err != nil {
			return err
		}
		// Refusals spend the actor's allowance without pretending mail was
		// sent. Commit this audit instead of rolling the counter back with
		// the public conflict response. Admin invitations stay exempt.
		refuse := func(cause error, reason string) error {
			refusal = cause
			if asPerson(me).Admin {
				return nil
			}
			if errors.Is(cause, errInvitationLimit) {
				return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.InvitationRateLimited,
					TargetType: "user", TargetID: me.ID,
					Meta: map[string]any{"reason": reason, "limit": leaderInviteDailyLimit}, IP: c.ClientIP()})
			}
			return writeInvitationRefusal(tx, me.ID, reason, c.ClientIP())
		}
		if !asPerson(me).Admin {
			allowed, err := conversionSendAllowed(tx, me.ID, email)
			if err != nil {
				return err
			}
			if !allowed {
				return refuse(errInvitationLimit, "daily_limit")
			}
		}
		var existing models.User
		if err := tx.Unscoped().Where("email = ?", email).Limit(1).Find(&existing).Error; err != nil {
			return err
		}
		if existing.ID != 0 && (existing.Status != models.UserGuest || existing.DeletedAt.Valid) {
			return refuse(errEmailTaken, "existing_account")
		}
		var pending int64
		if err := tx.Model(&models.AccountRequest{}).Where("email = ? AND source = ? AND status = ?", email, models.RequestHouseholdConversion, models.RequestPending).Count(&pending).Error; err != nil {
			return err
		}
		if pending != 0 {
			return refuse(errConversionEmail, "pending_conversion")
		}
		var n int64
		if err := tx.Model(&models.Invitation{}).Where("email = ? AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()", email).Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return refuse(errConversionEmail, "pending_invitation")
		}
		token, hash, err := createInvitationToken()
		if err != nil {
			return err
		}
		raw, inv.TokenHash = token, hash
		if err := tx.Create(&inv).Error; err != nil {
			return err
		}
		meta := map[string]any{"email": email, "overLimit": overLimit}
		if g != nil {
			meta["groupId"] = g.ID
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.InvitationCreated,
			TargetType: "invitation", TargetID: inv.ID, Meta: meta, IP: c.ClientIP()})
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not create invitation")
		return
	}
	switch {
	case errors.Is(refusal, errInvitationLimit):
		jsonError(c, http.StatusTooManyRequests, "invitation limit reached (20 per day)")
		return
	case errors.Is(refusal, errEmailTaken):
		jsonError(c, http.StatusConflict, "an account with that email already exists")
		return
	case errors.Is(refusal, errConversionEmail):
		jsonError(c, http.StatusConflict, "that email was already invited")
		return
	case refusal != nil:
		jsonError(c, http.StatusInternalServerError, "could not create invitation")
		return
	}
	if g != nil {
		inv.Group = *g
	}
	if err := a.sendInvitationEmail(&inv, me, raw); err != nil {
		log.Printf("invitation email to %s: %v", email, err)
	}
	c.JSON(http.StatusCreated, gin.H{"invitation": a.toInvitationJSON(&inv)})
}

// sendInvitationEmail sends the single-use accept link (C6: plain text).
func (a *App) sendInvitationEmail(inv *models.Invitation, inviter *models.User, raw string) error {
	link := a.emailBaseURL() + "/invite?token=" + raw
	groupPart := ""
	if inv.Group.ID != 0 {
		groupPart = fmt.Sprintf(" at %s", inv.Group.Name)
	}
	return a.Mailer.Send(context.Background(), mail.Message{
		To:      inv.Email,
		Subject: fmt.Sprintf("You've been invited to %s", a.siteName()),
		Text: fmt.Sprintf(`%s invited you to join %s%s.

Set your password here (the link works once and expires in 24 hours):
%s

If you weren't expecting this, you can ignore it.`,
			inviter.PublicName(), a.siteName(), groupPart, link),
	})
}

// apiGetInvitation shows what an invitation is for, so the public accept
// page can render it. The token is the credential.
func (a *App) apiGetInvitation(c *gin.Context) {
	inv, err := liveInvitation(a.DB, c.Param("token"))
	if err != nil {
		jsonError(c, http.StatusNotFound, "invitation not found")
		return
	}
	if inv.GroupID != nil {
		a.DB.First(&inv.Group, *inv.GroupID)
	}
	c.JSON(http.StatusOK, invitationContextJSON{
		Invitation:  a.toInvitationJSON(inv),
		InviterName: nameIn(a.DB, inv.InvitedBy),
		SiteName:    a.siteName(), Site: a.siteName(),
	})
}

// apiAcceptInvitation redeems an invitation: the person sets a password and
// gets an active account, with no approval step (DEC-4). When the
// invitation names a group, they join it as a member; into a full group
// that join happens only with the invitation's over-limit choice, and is
// audited (DEC-39). A full group without that choice refuses the accept
// (409) without consuming the token, so it can be retried.
func (a *App) apiAcceptInvitation(c *gin.Context) {
	var body struct {
		Token     string `json:"token"`
		Name      string `json:"name"`
		Password  string `json:"password"`
		AgeBand   string `json:"ageBand"`
		BirthDate string `json:"birthDate"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Token == "" || body.Password == "" {
		jsonError(c, http.StatusBadRequest, "token, name, and password are required")
		return
	}
	if len(body.Password) < minPasswordLength {
		jsonError(c, http.StatusBadRequest, "password must be at least 10 characters")
		return
	}
	initial, err := liveInvitation(a.DB, body.Token)
	if err != nil {
		jsonError(c, http.StatusBadRequest, "that invitation is invalid or has expired")
		return
	}
	if initial.HouseholdPersonID != nil {
		a.acceptHouseholdConversion(c, body.Token, body.Password, initial)
		return
	}
	kind, birth, ok := a.classifyAccountInput(c, body.AgeBand, body.BirthDate)
	if !ok {
		return
	}
	name := strings.TrimSpace(body.Name)
	if len(name) > 100 {
		jsonError(c, http.StatusBadRequest, "name must be at most 100 characters")
		return
	}
	var userID uint
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		// Share the lifecycle gate with demotion/removal/disable before taking
		// domain locks. Acceptance must not race an inviter losing authority.
		if err := lockContacts(tx); err != nil {
			return err
		}
		inv, err := liveInvitation(tx, body.Token)
		if err != nil {
			return err
		}
		if inv.HouseholdPersonID != nil {
			return errTokenInvalid
		}
		if err := lockInvitationEmail(tx, inv.Email); err != nil {
			return err
		}
		if name == "" {
			name = inv.Name
		}
		if name == "" {
			return errNeedName
		}
		// A guest row with this email (M1.4) is upgraded in place below
		// (C5), so their guest registrations stay theirs. Any other
		// existing account refuses the accept.
		var existing models.User
		guestRow := tx.Unscoped().Where("email = ?", inv.Email).Limit(1).Find(&existing).RowsAffected == 1
		if guestRow && (existing.Status != models.UserGuest || existing.DeletedAt.Valid) {
			return errEmailTaken
		}
		var group *models.Group
		var count int64
		if inv.GroupID != nil {
			var g models.Group
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&g, *inv.GroupID).Error; err != nil {
				return errTokenInvalid // the group is gone; the invite dies with it
			}
			if g.Status != models.GroupActive {
				return errGroupNotActive
			}
			group = &g
		}
		var inviter models.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("status = ?", models.UserActive).First(&inviter, inv.InvitedBy).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errTokenInvalid
			}
			return err
		}
		leadsTarget, leadsAnywhere := false, false
		if group != nil {
			leadsTarget = relIn(tx, inviter.ID, group.ID).Leader
		} else {
			var n int64
			if err := tx.Model(&models.Membership{}).Where("user_id = ? AND role = ? AND status = 'active'", inviter.ID, models.MembershipLeader).Count(&n).Error; err != nil {
				return err
			}
			leadsAnywhere = n > 0
		}
		if !policy.CanInvite(asPerson(&inviter), group != nil, leadsTarget, leadsAnywhere) {
			return errTokenInvalid
		}
		if group != nil {
			count = memberCount(tx, group.ID)
			if !policy.GroupHasRoom(group.MemberLimit, int(count)) && !inv.OverLimit {
				return errGroupFull // the token stays live: this can be retried
			}
		}
		u := models.User{Email: inv.Email, Name: name, Role: models.RoleMember, Status: models.UserActive}
		if guestRow {
			u = existing
			u.Name = name
			u.Status = models.UserActive
		}
		u.AccountType, u.BirthDate = kind, birth
		if kind == models.AccountTeen {
			u.Status = models.UserPendingConsent
		}
		if err := u.SetPassword(body.Password); err != nil {
			return err
		}
		if guestRow {
			if err := tx.Save(&u).Error; err != nil {
				return err
			}
			if err := revokeAccountTokens(tx, u.ID); err != nil {
				return err
			}
		} else if err := tx.Create(&u).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		// Claim the invitation atomically: a concurrent accept of the same
		// token must lose here, not rely on the users.email unique index.
		res := tx.Model(&models.Invitation{}).
			Where("id = ? AND accepted_at IS NULL", inv.ID).
			Update("accepted_at", now)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errTokenInvalid // another accept claimed it first
		}
		meta := map[string]any{"userId": u.ID}
		if group != nil {
			if err := tx.Create(&models.Membership{UserID: u.ID, GroupID: group.ID,
				Role: inv.Role, Status: "active", JoinedAt: now}).Error; err != nil {
				return err
			}
			meta["groupId"] = group.ID
			if !policy.GroupHasRoom(group.MemberLimit, int(count)) {
				// Over the limit by the inviting leader's or admin's
				// explicit choice (DEC-39).
				if err := audit.Write(tx, audit.Entry{
					Actor: &inv.InvitedBy, Action: audit.AddedOverLimit,
					TargetType: "group", TargetID: group.ID,
					Meta: map[string]any{"userId": u.ID, "count": count,
						"limit": *group.MemberLimit, "role": inv.Role, "invitationId": inv.ID},
					IP: c.ClientIP(),
				}); err != nil {
					return err
				}
			}
		}
		userID = u.ID
		return audit.Write(tx, audit.Entry{Actor: &inv.InvitedBy, Action: audit.InvitationAccepted,
			TargetType: "invitation", TargetID: inv.ID, Meta: meta, IP: c.ClientIP()})
	})
	switch {
	case errors.Is(err, errTokenInvalid):
		jsonError(c, http.StatusBadRequest, "that invitation is invalid or has expired")
		return
	case errors.Is(err, errNeedName):
		jsonError(c, http.StatusBadRequest, "name is required")
		return
	case errors.Is(err, errEmailTaken), emailTaken(err):
		jsonError(c, http.StatusConflict, "an account with that email already exists")
		return
	case errors.Is(err, errGroupNotActive):
		jsonError(c, http.StatusConflict, "that group is no longer active")
		return
	case errors.Is(err, errGroupFull):
		groupFull(c) // 409 "This group is full" with full:true, like a leader's add
		return
	case err != nil:
		jsonError(c, http.StatusInternalServerError, "could not accept invitation")
		return
	}
	var fresh models.User
	if err := a.DB.First(&fresh, userID).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not accept invitation")
		return
	}
	if err := a.signInSession(c, &fresh); err != nil {
		jsonError(c, http.StatusInternalServerError, "session error")
		return
	}
	c.JSON(http.StatusOK, invitationAcceptanceJSON{User: userJSONPtr(a.toUserJSON(&fresh)), PendingConsent: fresh.Status == models.UserPendingConsent})
}

// errNeedName: the accept form needs a name and the invitation had none.
var errNeedName = errors.New("name required")
