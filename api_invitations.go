package main

import (
	"errors"
	"fmt"
	"net/http"
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

// errGroupNotActive: the invitation's group is no longer active.
var errGroupNotActive = errors.New("group not active")
var errInvitationLimit = apiErr(http.StatusTooManyRequests, "invite_limit", fmt.Sprintf("invitation limit reached (%d per day)", leaderInviteDailyLimit))

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

// leadsApprovedGroup reports whether userID actively leads at least one
// group an admin has approved (policy.LeadsApprovedGroup). Leading only a
// pending or rejected group grants no power to invite people into Fold.
func leadsApprovedGroup(db *gorm.DB, userID uint) (bool, error) {
	var statuses []string
	err := db.Model(&models.Membership{}).
		Joins("JOIN groups ON groups.id = memberships.group_id AND groups.deleted_at IS NULL").
		Where("memberships.user_id = ? AND memberships.role = ? AND memberships.status = ?", userID, models.MembershipLeader, models.MembershipActive).
		Pluck("groups.status", &statuses).Error
	return policy.LeadsApprovedGroup(statuses), err
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
	email := mail.NormalizeAddress(body.Email)
	if !validEmail(email) {
		fail(c, errEnterValidEmail)
		return
	}
	name, err := optionalName(body.Name)
	if err != nil {
		fail(c, err)
		return
	}
	phone, err := phoneNumber(body.Phone)
	if err != nil {
		fail(c, err)
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
	if !asPerson(me).Admin {
		var err error
		if leadsAnywhere, err = leadsApprovedGroup(a.DB, me.ID); err != nil {
			jsonError(c, http.StatusInternalServerError, "could not create invitation")
			return
		}
	}
	if !policy.CanInvite(asPerson(me), body.GroupID != nil, leadsTarget, leadsAnywhere) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return
	}
	// An invitation into a group that isn't active could never be accepted.
	if g != nil && !policy.CanInviteIntoGroup(g.Status) {
		fail(c, errGroupNotApprovedToInvite)
		return
	}

	var refusal error
	overLimit := body.OverLimit && g != nil
	inv := models.Invitation{
		Email: email, Phone: phone, Name: name, InvitedBy: me.ID,
		Role: models.MembershipMember, AccountType: "adult",
		GroupID: body.GroupID, OverLimit: overLimit,
		ExpiresAt: time.Now().UTC().Add(tokenTTL),
	}
	err = a.DB.Transaction(func(tx *gorm.DB) error {
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
			allowed, err := inviteSendAllowed(tx, me.ID, email)
			if err != nil {
				return err
			}
			if !allowed {
				return refuse(errInvitationLimit, "daily_limit")
			}
		}
		hold, err := emailHeldBy(tx, email, 0, 0, true)
		if err != nil {
			return err
		}
		switch hold {
		case holdAccount:
			return refuse(errEmailTaken, hold)
		case holdPendingConversion, holdPendingInvitation:
			return refuse(errConversionEmail, hold)
		}
		_, hash, err := newTokenAndHash()
		if err != nil {
			return err
		}
		inv.TokenHash = hash
		if err := tx.Create(&inv).Error; err != nil {
			return err
		}
		meta := map[string]any{"email": email, "overLimit": overLimit}
		if g != nil {
			meta["groupId"] = g.ID
		}
		if err := audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.InvitationCreated,
			TargetType: "invitation", TargetID: inv.ID, Meta: meta, IP: c.ClientIP()}); err != nil {
			return err
		}
		_, err = invitationMailType.Enqueue(tx, invitationMailJob{InvitationID: inv.ID}, time.Now(), "")
		return err
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not create invitation")
		return
	}
	switch {
	case errors.Is(refusal, errInvitationLimit):
		fail(c, errInvitationLimit)
		return
	case errors.Is(refusal, errEmailTaken):
		fail(c, errEmailTaken)
		return
	case errors.Is(refusal, errConversionEmail):
		fail(c, errAlreadyInvited)
		return
	case refusal != nil:
		jsonError(c, http.StatusInternalServerError, "could not create invitation")
		return
	}
	if g != nil {
		inv.Group = *g
	}
	c.JSON(http.StatusCreated, gin.H{"invitation": a.toInvitationJSON(&inv)})
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
	if !checkNewPassword(c, body.Password) {
		return
	}
	initial, err := liveInvitation(a.DB, body.Token)
	if err != nil {
		fail(c, errInvitationExpired)
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
	name, err := optionalName(body.Name)
	if err != nil {
		fail(c, err)
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
			var err error
			if leadsAnywhere, err = leadsApprovedGroup(tx, inviter.ID); err != nil {
				return err
			}
		}
		if !policy.CanInvite(asPerson(&inviter), group != nil, leadsTarget, leadsAnywhere) {
			return errTokenInvalid
		}
		u := models.User{Email: inv.Email, Role: models.RoleMember}
		if guestRow {
			u = existing
		}
		u.Name, u.AccountType, u.BirthDate = name, kind, birth
		u.Status = newAccountStatus(kind, true)
		if err := u.SetPassword(body.Password); err != nil {
			return err
		}
		if guestRow {
			err = upgradeGuestIn(tx, &u, "status", "name", "account_type", "birth_date", "password_hash")
		} else {
			err = tx.Create(&u).Error
		}
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		// Take the invitation's row lock last, in its own statement, so the
		// claim below reads the wall clock after any wait. A delivery worker
		// holds this row FOR UPDATE without changing it; an UPDATE that
		// waited on such a lock would resume without re-evaluating its
		// deadline test. The mail job locks only this row, so taking it after
		// the contact, email, group and inviter locks cannot deadlock with it.
		var held models.Invitation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").
			Where("id = ? AND token_hash = ? AND accepted_at IS NULL AND revoked_at IS NULL", inv.ID, hashToken(body.Token)).
			Limit(1).Find(&held).Error; err != nil {
			return err
		}
		if held.ID == 0 {
			return errTokenInvalid // rotated, revoked or claimed while we waited
		}
		// Claim the invitation atomically: a concurrent accept of the same
		// token must lose here, not rely on the users.email unique index.
		if err := claimInvitation(tx, inv.ID, body.Token, now); err != nil {
			return err
		}
		meta := map[string]any{"userId": u.ID}
		if group != nil {
			// The invitation is the authority (checked above under the inviter's
			// lock); changeRoleIn still holds the capacity rule (DEC-39) and writes
			// the over-limit audit row, naming this invitation. errGroupFull rolls
			// the whole accept back, so the token stays live and can be retried.
			err := a.changeRoleIn(tx, &inviter, c.ClientIP(), group, u.ID, inv.Role, inv.OverLimit,
				&memberAddOptions{invitationID: inv.ID},
				func(_, _ *models.User, _, _ policy.Rel, _ int) bool { return true })
			if err != nil {
				return err
			}
			meta["groupId"] = group.ID
		}
		userID = u.ID
		return audit.Write(tx, audit.Entry{Actor: &inv.InvitedBy, Action: audit.InvitationAccepted,
			TargetType: "invitation", TargetID: inv.ID, Meta: meta, IP: c.ClientIP()})
	})
	switch {
	case errors.Is(err, errTokenInvalid):
		fail(c, errInvitationExpired)
		return
	case errors.Is(err, errNeedName):
		fail(c, errNameRequired)
		return
	case errors.Is(err, errEmailTaken), emailTaken(err):
		fail(c, errEmailTaken)
		return
	case errors.Is(err, errGroupNotActive):
		fail(c, errGroupInactive)
		return
	case errors.Is(err, errGroupFull):
		fail(c, errGroupFull)
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
	user := a.toUserJSON(&fresh)
	c.JSON(http.StatusOK, invitationAcceptanceJSON{User: &user, PendingConsent: fresh.Status == models.UserPendingConsent})
}

// errNeedName: the accept form needs a name and the invitation had none.
var errNeedName = errors.New("name required")
