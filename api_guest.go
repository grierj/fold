package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/events"
	"fold/internal/jobs"
	foldmail "fold/internal/mail"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Guest registration (M1.4, C5, 5.10, 10.1): a signed-out adult registers
// for one date of a public event with a name and an email. The email is
// verified by a link before any User or Registration exists, so an
// unverified address never lands on an attendee list. Confirming creates
// (or reuses) the guest User row and an ordinary registration against it,
// plus a manage link that cancels without an account. Public answers
// never reveal whether an email already has an account.

const (
	// Verification emails to one address stop after 3 in an hour or 10
	// in a day, whatever the IP (10.1).
	guestVerifyPerHour = 3
	guestVerifyPerDay  = 10
	// The per-IP ceiling matches the account-request form (M1.3).
	guestIPLimit = 10
)

// guestRegistrationJSON is what the confirm and manage pages show a
// signed-out visitor: the event as they may see it, and the status of
// their registration. It carries no email and no attendee names.
type guestRegistrationJSON struct {
	EventID      uint      `json:"eventId"`
	OccurrenceID uint      `json:"occurrenceId"`
	Date         string    `json:"date"`
	Title        string    `json:"title"`
	HostedBy     string    `json:"hostedBy,omitempty"`
	GroupName    string    `json:"groupName,omitempty"`
	Status       string    `json:"status"`
	StartsAt     time.Time `json:"startsAt"`
	Location     string    `json:"location"`
	// WaitlistPosition is the 1-based waitlist position, set only when
	// the registration is waitlisted (M1.5).
	WaitlistPosition *int `json:"waitlistPosition,omitempty"`
}

// guestDisplayWith builds the signed-out view of one registration. Group
// details come only through groupShownTo for a nil viewer, so an opened
// invite-only event keeps its group hidden. The church name comes from siteName,
// called only when the event is the church's own; mail preparation passes a
// value it already read in its transaction, so rendering opens no second
// connection.
func (a *App) guestDisplayWith(siteName func() string, reg *models.Registration, occ *models.EventOccurrence,
	ev *models.Event, grp *models.Group) guestRegistrationJSON {
	shown := a.groupShownTo(nil, grp, policy.Rel{})
	loc := ""
	if shown != nil {
		loc = shown.Location
	}
	inst := occurrenceToJSON(ev, occ, loc)
	out := guestRegistrationJSON{
		EventID: ev.ID, OccurrenceID: occ.ID, Date: models.FormatCalendarDate(occ.OriginalDate),
		Title: inst.Title, Status: reg.Status, StartsAt: inst.StartsAt,
		Location: inst.Location,
	}
	if ev.GroupID != nil && shown != nil {
		out.GroupName = shown.Name
	} else {
		out.HostedBy = siteName()
	}
	if out.Title == "" {
		out.Title = "Church event"
		if out.GroupName != "" {
			out.Title = out.GroupName
		}
	}
	return out
}

// loadGuestOccurrence loads the date in :id/date for a signed-out visitor. It
// answers 404 unless a guest may register for it, like every other hidden
// thing in the API. Its visibility rule is the one verification and
// redemption run again later (policy.CanRegisterGuest).
func (a *App) loadGuestOccurrence(c *gin.Context, date string) (*models.EventOccurrence, bool) {
	id, _ := strconv.Atoi(c.Param("id"))
	var ev models.Event
	if err := a.DB.First(&ev, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "event not found")
		return nil, false
	}
	var occ models.EventOccurrence
	if !isCalendarDate(date) || a.DB.Where("event_id = ? AND original_date = ? AND status = ?", ev.ID, date, models.OccurrenceScheduled).First(&occ).Error != nil {
		jsonError(c, 404, "event not found")
		return nil, false
	}
	f, err := eventFactsIn(a.DB, nil, dateEvent(&ev, &occ))
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load this date")
		return nil, false
	}
	if !policy.CanRegisterGuest(f.view, asGroup(f.group)) {
		jsonError(c, http.StatusNotFound, "event not found")
		return nil, false
	}
	return &occ, true
}

type guestRegistrationInput struct {
	Name          string `json:"name"`
	Email         string `json:"email"`
	Date          string `json:"date"`
	Over18        bool   `json:"over18"`
	CreateAccount bool   `json:"createAccount"`
	Website       string `json:"website"` // honeypot: people never fill this in
	// Timezone is the browser's zone, for this guest's emails (DEC-58).
	Timezone string `json:"timezone"`
}

// apiCreateGuestRegistration is the public guest form. Every outcome
// that could reveal whether an email is registered answers with the
// same thanks message and lets the email itself carry the difference.
func (a *App) apiCreateGuestRegistration(c *gin.Context) {
	var body guestRegistrationInput
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	occ, ok := a.loadGuestOccurrence(c, strings.TrimSpace(body.Date))
	if !ok {
		return
	}
	name, err := requiredName(body.Name)
	if err != nil {
		fail(c, err)
		return
	}
	email := foldmail.NormalizeAddress(body.Email)
	if !validEmail(email) {
		fail(c, errEnterValidEmail)
		return
	}
	if !body.Over18 {
		// The age gate on the guest door (5.10): nothing is stored.
		fail(c, errGuestUnder18)
		return
	}
	hits, err := a.throttleHit(c.Request.Context(), guestIPKey(c.ClientIP()), time.Hour)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save your registration")
		return
	}
	if hits > guestIPLimit {
		// No 429 here (DEC-52): refusing out loud would confirm the
		// address is being mailed. The emails simply stop.
		log.Printf("guest registration rate limited: ip=%s", c.ClientIP())
		c.JSON(http.StatusOK, noticeResponseJSON{Message: signupThanks})
		return
	}
	if ok, err := a.throttleAllow(c.Request.Context(), guestSiteKey(), publicSignupSiteLimit, time.Hour); err != nil || !ok {
		log.Printf("guest registration site limit: err=%v", err)
		c.JSON(http.StatusOK, noticeResponseJSON{Message: signupThanks})
		return
	}
	if strings.TrimSpace(body.Website) != "" {
		c.JSON(http.StatusOK, noticeResponseJSON{Message: signupThanks})
		return
	}
	// Every in-budget submission pays the same bcrypt cost before account
	// lookup or either address limit, even when that address is exhausted.
	// IP/site limits and the honeypot above shed CPU without inspecting it.
	(&models.User{}).CheckPassword("not a real password")
	var existing models.User
	lookup := a.DB.Where("email = ?", email).Limit(1).Find(&existing)
	if lookup.Error != nil {
		log.Printf("guest registration: %s", jobs.CleanError(lookup.Error))
		jsonError(c, http.StatusInternalServerError, "could not save your registration")
		return
	}
	if lookup.RowsAffected == 1 && existing.Status != models.UserGuest {
		// The hint is an email to that address too, so it shares the
		// per-address limit (3 an hour, 10 a day) whatever the IP (10.1).
		if ok, err := a.targetAllow(c.Request.Context(), guestHintKey(email)); err == nil && ok {
			// The hint is queued by account id; delivery mails the address
			// the account has then. A failed write is the same failure as a
			// failed stored registration. No request context: a disconnect
			// cannot abort the durable write, as in the stored path.
			if _, err := accountExistsMailType.Enqueue(a.DB, userMailJob{UserID: existing.ID}, time.Now(), ""); err != nil {
				log.Printf("account-exists hint: %s", jobs.CleanError(err))
				jsonError(c, http.StatusInternalServerError, "could not save your registration")
				return
			}
		}
		c.JSON(http.StatusOK, noticeResponseJSON{Message: signupThanks})
		return
	}
	if a.guestVerifyLimited(email, time.Now().UTC()) {
		log.Printf("guest verification emails to one address limited (DEC-52)")
		c.JSON(http.StatusOK, noticeResponseJSON{Message: signupThanks})
		return
	}
	// Only the hash is kept. Delivery rotates it and mints the link the guest
	// receives, so no capability exists until the mail is about to be sent.
	_, hash, err := newTokenAndHash()
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save your registration")
		return
	}
	signup := models.GuestSignup{
		Name: name, Email: email, OccurrenceID: occ.ID,
		CreateAccount: body.CreateAccount, TokenHash: hash,
		ExpiresAt: time.Now().UTC().Add(tokenTTL), IP: c.ClientIP(),
	}
	if _, err := events.LoadZone(body.Timezone); err == nil {
		signup.Timezone = strings.TrimSpace(body.Timezone)
	}
	// The signup and its mail job commit together, so an accepted submission
	// always has a delivery duty. No request context: a disconnect cannot abort
	// the durable write. The job carries the signup's ID and nothing else.
	if err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&signup).Error; err != nil {
			return err
		}
		_, err := guestVerifyMailType.Enqueue(tx, guestVerifyMailJob{GuestSignupID: signup.ID}, time.Now(), "")
		return err
	}); err != nil {
		log.Printf("guest registration: %s", jobs.CleanError(err))
		jsonError(c, http.StatusInternalServerError, "could not save your registration")
		return
	}
	c.JSON(http.StatusOK, noticeResponseJSON{Message: signupThanks})
}

// guestVerifyLimited reports whether this address has used up its
// verification emails: 3 in the last hour or 10 in the last day (10.1).
// Each stored signup is one email sent, so the rows are the counter.
// A failed count is treated as limited: no email goes out unmetered.
func (a *App) guestVerifyLimited(email string, now time.Time) bool {
	var hour, day int64
	if err := a.DB.Model(&models.GuestSignup{}).
		Where("email = ? AND created_at > ?", email, now.Add(-time.Hour)).Count(&hour).Error; err != nil {
		log.Printf("guest verification count: %v", err)
		return true
	}
	if hour >= guestVerifyPerHour {
		return true
	}
	if err := a.DB.Model(&models.GuestSignup{}).
		Where("email = ? AND created_at > ?", email, now.Add(-24*time.Hour)).Count(&day).Error; err != nil {
		log.Printf("guest verification count: %v", err)
		return true
	}
	return day >= guestVerifyPerDay
}

// apiConfirmGuestRegistration redeems a verification link. The SPA
// posts the token, so a mail scanner fetching the link confirms
// nothing. Everything the redemption creates lands in one transaction.
// The redemption runs the locked seat path (M1.5): a guest who fits
// becomes going; on a full event with the waitlist on they become
// waitlisted; with the waitlist off no user and no registration are
// created and the link is marked confirmed so it dies.
func (a *App) apiConfirmGuestRegistration(c *gin.Context) {
	var body struct {
		Token string `json:"token"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Token == "" {
		jsonError(c, http.StatusBadRequest, "token is required")
		return
	}
	var display guestRegistrationJSON
	var manageRaw, guestEmail string
	guestZone := time.UTC
	var askedAccount bool
	var waitlistPos *int
	var fullNoWaitlist bool
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var signup models.GuestSignup
		if err := tx.Where("token_hash = ? AND confirmed_at IS NULL AND "+sqlStillLive,
			hashToken(body.Token)).First(&signup).Error; err != nil {
			return errTokenInvalid
		}
		// The guest's own row first, then the occurrence (see registration_locks.go).
		user, found, err := lockRedeemingGuest(tx, signup.Email)
		if err != nil {
			return err
		}
		occ, err := lockOccurrence(tx, signup.OccurrenceID)
		if err != nil {
			return errDateClosed
		}
		var ev models.Event
		if err := tx.First(&ev, occ.EventID).Error; err != nil {
			return errDateClosed
		}
		var grp *models.Group
		gp := policy.Group{}
		if ev.GroupID != nil {
			var g models.Group
			if err := tx.First(&g, *ev.GroupID).Error; err != nil {
				return errDateClosed
			}
			grp = &g
			gp = asGroup(&g)
		}
		// The door may have closed since the form was sent: the event
		// tightened, or the date was cancelled.
		if occ.Status != models.OccurrenceScheduled ||
			!policy.CanRegisterGuest(viewOf(dateEvent(&ev, occ), false), gp) {
			return errDateClosed
		}
		if found && user.Status != models.UserGuest {
			// An account arrived for this address after the form. The
			// person proved the mailbox is theirs by opening the link,
			// so saying so reveals nothing to a stranger.
			return errEmailTakenSignIn
		}
		// A guest who confirms again already holds their row; like
		// registerLocked, their own seats do not count against them.
		var prior models.Registration
		havePrior := false
		if found {
			lookup := tx.Where("user_id = ? AND occurrence_id = ?", user.ID, occ.ID).Limit(1).Find(&prior)
			if lookup.Error != nil {
				return lookup.Error
			}
			havePrior = lookup.RowsAffected == 1
		}
		// Guests register in person. The seat check runs under the lock, before
		// anything is created, through the same ledger as every other writer.
		reg := prior
		reg.OccurrenceID, reg.Status, reg.Source = occ.ID, models.RegGoing, models.RegSourceGuestForm
		reg.AttendanceMode = models.AttendanceInPerson
		if err := seatChange(tx, &ev, occ, prior, &reg, time.Now().UTC()); err != nil {
			if !errors.Is(err, errEventFull) {
				return err
			}
			// No seat and no waitlist: the link dies here (the update
			// commits), nothing is created, and the page says the
			// event is full.
			// The same hash-checked claim as below: a verification mail that
			// rotated the link after this read must not be consumed here.
			if err := claimGuestSignup(tx, &signup, time.Now().UTC()); err != nil {
				return err
			}
			fullNoWaitlist = true
			return nil
		}
		full := reg.Status == models.RegWaitlisted
		// Claim the link atomically: a second redeem of the same token
		// loses here instead of registering twice.
		now := time.Now().UTC()
		if err := claimGuestSignup(tx, &signup, now); err != nil {
			return err
		}
		if found {
			if user.Name != signup.Name {
				user.Name = signup.Name
				if err := tx.Model(&user).Update("name", signup.Name).Error; err != nil {
					return err
				}
			}
			if signup.Timezone != "" && user.Timezone != signup.Timezone {
				user.Timezone = signup.Timezone
				if err := tx.Model(&user).Update("timezone", signup.Timezone).Error; err != nil {
					return err
				}
			}
		} else {
			user = models.User{
				Email: signup.Email, Name: signup.Name,
				Role: models.RoleMember, Status: models.UserGuest,
				AccountType: models.AccountAdult,
				Timezone:    signup.Timezone,
			}
			if err := tx.Create(&user).Error; err != nil {
				return err
			}
		}
		raw, hash, err := newTokenAndHash()
		if err != nil {
			return err
		}
		manageRaw = raw
		if havePrior && prior.Status == models.RegRemoved {
			// A pruned guest can't re-enter through the public form;
			// a leader or host adds them back (mirrors registerLocked).
			return errRemovedBlocked
		}
		reg.UserID, reg.ManageTokenHash = &user.ID, hash
		if err := saveRegistration(tx, &reg); err != nil {
			return err
		}
		if full {
			pos, err := waitlistPosition(tx, occ.ID, reg.ID)
			if err != nil {
				return err
			}
			waitlistPos = &pos
		}
		askedAccount = signup.CreateAccount
		if signup.CreateAccount {
			err := createGuestUpgradeRequest(tx, &signup)
			if err != nil {
				return err
			}
		}
		display = a.guestDisplayWith(a.siteName, &reg, occ, &ev, grp)
		display.WaitlistPosition = waitlistPos
		guestEmail = signup.Email
		var zoneErr error
		guestZone, zoneErr = recipientZone(tx, user.ID)
		return zoneErr
	})
	switch {
	case errors.Is(err, errTokenInvalid):
		fail(c, errLinkExpired)
		return
	case err != nil:
		fail(c, err)
		return
	}
	if fullNoWaitlist {
		fail(c, errEventFullNoWaitlist)
		return
	}
	if err := a.sendGuestConfirmationEmail(guestEmail, manageRaw, display, guestZone, askedAccount); err != nil {
		log.Printf("guest confirmation email: %s", jobs.CleanError(err))
	}
	c.JSON(http.StatusOK, guestConfirmationJSON{Registration: display, ManageToken: manageRaw})
}

// claimGuestSignup marks the link used. It locks the row, then matches the hash
// that was read and the live deadline, so a link rotated or expired since the read claims nothing.
// A lost claim returns errTokenInvalid and the caller's transaction rolls back
// everything it had created.
func claimGuestSignup(tx *gorm.DB, signup *models.GuestSignup, at time.Time) error {
	// Take the signup's row lock last, in its own statement, so the claim
	// below reads the wall clock after any wait. A delivery worker holds this
	// row FOR UPDATE and commits it unchanged when the date closed; an UPDATE
	// that waited on such a lock would resume without re-evaluating its
	// deadline test. Redemption has locked the occurrence already, and the
	// mail job locks only this row, so the order cannot deadlock.
	var held models.GuestSignup
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").
		Where("id = ? AND token_hash = ? AND confirmed_at IS NULL", signup.ID, signup.TokenHash).
		Limit(1).Find(&held).Error; err != nil {
		return err
	}
	if held.ID == 0 {
		return errTokenInvalid // rotated or claimed while we waited
	}
	res := tx.Model(&models.GuestSignup{}).
		Where("id = ? AND token_hash = ? AND confirmed_at IS NULL AND "+sqlStillLive, signup.ID, signup.TokenHash).
		Update("confirmed_at", at)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errTokenInvalid
	}
	return nil
}

// createGuestUpgradeRequest opens the guest_upgrade account request a
// guest asked for on the form (5.10). A still-pending request for the
// same address is left alone: the admins already have the task.
func createGuestUpgradeRequest(tx *gorm.DB, signup *models.GuestSignup) error {
	var pending int64
	if err := tx.Model(&models.AccountRequest{}).
		Where("email = ? AND status = ?", signup.Email, models.RequestPending).Count(&pending).Error; err != nil {
		return err
	}
	if pending > 0 {
		return nil
	}
	req := models.AccountRequest{
		Name: signup.Name, Email: signup.Email,
		AccountType: models.AccountAdult, Source: models.RequestGuestUpgrade,
		Status: models.RequestPending, Flags: "{}", IP: signup.IP,
		ExpiresAt: time.Now().UTC().Add(requestLifetime),
	}
	if err := tx.Create(&req).Error; err != nil {
		return err
	}
	if err := audit.Write(tx, audit.Entry{Action: audit.AccountRequested,
		TargetType: "account_request", TargetID: req.ID,
		Meta: map[string]any{"source": models.RequestGuestUpgrade,
			"accountType": models.AccountAdult, "hasWarnings": false},
		IP: signup.IP}); err != nil {
		return err
	}
	return notifyAccountRequested(tx, &req, models.RequestFlags{})
}

// tokenRegistration is what a manage token controls: its registration, the date,
// the event and, for a group event, the group.
type tokenRegistration struct {
	Reg *models.Registration
	Occ *models.EventOccurrence
	Ev  *models.Event
	Grp *models.Group
}

// liveTokenManage finds the registration a manage token controls. The
// token identifies a credential: authorizedTokenManage also checks current
// subject access for guests, promoted members and household rows. It stops
// working once the date has ended: there is nothing left to manage, and
// a bearer link should not outlive its purpose (DEC-55).
func liveTokenManage(db *gorm.DB, raw string) (tokenRegistration, error) {
	if raw == "" {
		return tokenRegistration{}, errTokenInvalid
	}
	var reg models.Registration
	if err := db.Preload("Occurrence.Event").Joins(listedRegistrations()).
		Where("registrations.manage_token_hash = ? AND registrations.manage_token_hash <> ?", hashToken(raw), "").
		Where("registrations.status <> ? AND (registrations.household_person_id IS NULL OR registrations.status <> ?)", models.RegRemoved, models.RegCancelled).
		First(&reg).Error; err != nil {
		return tokenRegistration{}, errTokenInvalid
	}
	occ := reg.Occurrence
	if occ.ID == 0 || time.Now().UTC().After(occ.EndsAt) || occ.Status != models.OccurrenceScheduled {
		return tokenRegistration{}, errTokenInvalid
	}
	ev := occ.Event
	var grp *models.Group
	if ev.GroupID != nil {
		var g models.Group
		if err := db.First(&g, *ev.GroupID).Error; err != nil {
			return tokenRegistration{}, err
		}
		grp = &g
	}
	return tokenRegistration{Reg: &reg, Occ: &occ, Ev: &ev, Grp: grp}, nil
}

// authorizedTokenManage runs under the contact gate. The holder-first lock
// prefix matches registration writes; the occurrence lock protects both reads
// and idempotent cancellations against a concurrent visibility change.
func (a *App) authorizedTokenManage(tx *gorm.DB, raw string) (tokenRegistration, error) {
	found, err := liveTokenManage(tx, raw)
	if err != nil {
		return tokenRegistration{}, err
	}
	if err := lockRegistrationHolder(tx, found.Reg.ID, found.Occ.ID); err != nil {
		return tokenRegistration{}, err
	}
	if _, err := lockOccurrence(tx, found.Occ.ID); err != nil {
		return tokenRegistration{}, err
	}
	m, err := liveTokenManage(tx, raw)
	if err != nil {
		return tokenRegistration{}, err
	}
	if err := a.authorizeRegistrationLink(tx, m.Reg, m.Occ, m.Ev, m.Grp); err != nil {
		return tokenRegistration{}, err
	}
	return m, nil
}

// authorizeRegistrationLink loads current authority, not authority at token
// issuance. Denial never clears the hash: reopening a date can restore access.
// Every fact lookup fails closed; a failed query is never an empty relationship.
func (a *App) authorizeRegistrationLink(tx *gorm.DB, reg *models.Registration, occ *models.EventOccurrence, ev *models.Event, grp *models.Group) error {
	var holder models.User
	if err := tx.First(&holder, registrationHolder(*reg)).Error; err != nil {
		return err
	}
	gp, rel := policy.Group{}, policy.Rel{}
	if grp != nil {
		gp = asGroup(grp)
		var membership models.Membership
		if err := tx.Where("user_id = ? AND group_id = ?", holder.ID, grp.ID).Limit(1).Find(&membership).Error; err != nil {
			return err
		}
		rel = membershipRel(membership)
	}
	guest := holder.Status == models.UserGuest
	var person *policy.Person
	if !guest {
		person = asPerson(&holder)
	}
	exact := false
	if !guest && reg.UserID != nil {
		var grants []models.Registration
		if err := tx.Where("registrations.id = ? AND registrations.user_id = ? AND registrations.occurrence_id = ? AND "+registrationAccessLiveSQL, reg.ID, holder.ID, occ.ID).Find(&grants).Error; err != nil {
			return err
		}
		reads, err := occurrenceInvitationReadsIn(tx, grants, time.Now().UTC())
		if err != nil {
			return err
		}
		exact = len(reads) > 0
	}
	allowed := policy.CanManageGuestRegistration(person, guest, holder.CanSignIn(), viewOf(dateEvent(ev, occ), false), gp, rel, exact)
	if !allowed {
		return errTokenInvalid
	}
	if reg.HouseholdPersonID != nil {
		facts, err := a.loadEligibilityFacts(tx, []models.Registration{*reg}, time.Now().UTC(), &holder)
		if err != nil {
			return err
		}
		person := facts.people[*reg.HouseholdPersonID]
		if person == nil || facts.householdProblem(&holder, person, allowed) != "" {
			return errTokenInvalid
		}
	}
	return nil
}

func (a *App) apiGetGuestRegistration(c *gin.Context) {
	// Reject dead credentials without queuing a public request on the global
	// contact gate. This is only a preflight; current authority and the token
	// are checked again under the gate before any registration is displayed.
	if _, err := liveTokenManage(a.DB, c.Param("token")); err != nil {
		jsonError(c, http.StatusNotFound, "registration not found")
		return
	}
	var display guestRegistrationJSON
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockContacts(tx); err != nil {
			return err
		}
		m, err := a.authorizedTokenManage(tx, c.Param("token"))
		if err != nil {
			return err
		}
		reg, occ := m.Reg, m.Occ
		display = a.guestDisplayWith(a.siteName, reg, occ, m.Ev, m.Grp)
		if reg.Status == models.RegWaitlisted {
			pos, err := waitlistPosition(tx, occ.ID, reg.ID)
			if err != nil {
				return err
			}
			display.WaitlistPosition = &pos
		}
		return nil
	})
	if err != nil {
		jsonError(c, http.StatusNotFound, "registration not found")
		return
	}
	c.JSON(http.StatusOK, guestRegistrationResponseJSON{Registration: display})
}

func (a *App) apiCancelGuestRegistration(c *gin.Context) {
	// Discover only the reconciliation scope here. The token is authenticated
	// again under the contact gate, before any registration locks or writes.
	preflight, err := liveTokenManage(a.DB, c.Param("token"))
	if err != nil {
		jsonError(c, http.StatusNotFound, "registration not found")
		return
	}
	scope := preflight.Ev
	var display guestRegistrationJSON
	err = a.contactMutation(contactChange{EventID: scope.ID}, func(tx *gorm.DB) error {
		m, err := a.authorizedTokenManage(tx, c.Param("token"))
		if err != nil {
			return err
		}
		reg, occ, ev, grp := m.Reg, m.Occ, m.Ev, m.Grp
		if ev.ID != scope.ID {
			return errTokenInvalid
		}
		before := reg.Status
		cancellable := before == models.RegGoing || before == models.RegMaybe ||
			before == models.RegWaitlisted
		if !cancellable {
			display = a.guestDisplayWith(a.siteName, reg, occ, ev, grp)
			return nil
		}
		// authorizedTokenManage keeps the occurrence locked through the
		// cancel and promotion check, so a freed seat cannot be taken twice.
		// The manage token of an account row stays live (see saveRegistration):
		// a second POST with the same link is a 200 no-op, and the token can
		// then only ever show or re-cancel this dead registration. Current
		// date authority is still required on every replay. It dies at the date
		// end, and a new manage email rotates it.
		now := time.Now().UTC()
		cancelled := *reg
		cancelled.Status = models.RegCancelled
		if err := saveRegistration(tx, &cancelled); err != nil {
			return err
		}
		// Like declining, cancelling ends a live exact-date grant on the row.
		if reg.AccessLive() {
			holder := registrationHolder(*reg)
			if err := endRegistrationAccess(tx, &cancelled, accessScopeOf(ev), policy.OccurrenceDeclined, &holder, now, c.ClientIP()); err != nil {
				return err
			}
		}
		*reg = cancelled
		if err := audit.Write(tx, audit.Entry{Action: audit.RegistrationCancelled,
			TargetType: "event", TargetID: ev.ID,
			Meta: registrationMeta(*reg),
			IP:   c.ClientIP()}); err != nil {
			return err
		}
		if before == models.RegGoing {
			err := a.promoteLocked(tx, ev, occ, now, nil)
			if err != nil {
				return err
			}
		}
		display = a.guestDisplayWith(a.siteName, reg, occ, ev, grp)
		return nil
	})
	if err != nil {
		jsonError(c, http.StatusNotFound, "registration not found")
		return
	}
	c.JSON(http.StatusOK, guestRegistrationResponseJSON{Registration: display})
}

// Guest emails (C6: plain text through the minimal sender). Verification is a
// stored job (mail_jobs.go); the confirmation below stays synchronous because
// its manage link is also returned to the browser. Both name
// the event as a signed-out visitor may see it; a hidden group is
// never named.

func guestWithLine(d guestRegistrationJSON) string {
	if d.GroupName != "" {
		return " with " + d.GroupName
	}
	if d.HostedBy != "" {
		return " hosted by " + d.HostedBy
	}
	return ""
}

func (a *App) sendGuestConfirmationEmail(to, manageRaw string,
	d guestRegistrationJSON, loc *time.Location, askedAccount bool) error {
	when := formatWhen(d.StartsAt, loc)
	link := a.emailBaseURL() + "/guest/manage/" + manageRaw
	extra := ""
	if askedAccount {
		extra = "\n\nYou also asked for an account. An admin will review that request, and we will email you the decision."
	}
	where := ""
	if d.Location != "" {
		where = "\nWhere: " + d.Location
	}
	headline := fmt.Sprintf("You're registered for %s", d.Title)
	intro := fmt.Sprintf(`You're registered for %s%s at %s, on %s.%s%s`,
		d.Title, guestWithLine(d), a.siteName(), when, where, extra)
	if d.Status == models.RegWaitlisted {
		headline = fmt.Sprintf("You're on the waitlist for %s", d.Title)
		intro = fmt.Sprintf(`You're on the waitlist for %s%s at %s, on %s.%s%s`,
			d.Title, guestWithLine(d), a.siteName(), when, where, extra)
		if d.WaitlistPosition != nil {
			intro += fmt.Sprintf("\n\nYou're number %d in line. We'll email you if a spot opens.", *d.WaitlistPosition)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), syncMailTimeout)
	defer cancel()
	return sendTracked(ctx, a.mailSender(), foldmail.Message{
		To:      to,
		Subject: headline,
		Text: intro + fmt.Sprintf(`

Can't come after all? Cancel your registration here:
%s`, link),
	}, nil).err
}
