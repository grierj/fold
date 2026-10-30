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
	foldmail "fold/internal/mail"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// Guest registration (M1.4, C5, 5.10, 10.1): a signed-out adult registers
// for one date of a public event with a name and an email. The email is
// verified by a link before any User or Registration exists, so an
// unverified address never lands on an attendee list. Confirming creates
// (or reuses) the guest User row and an ordinary registration against it,
// plus a manage link that cancels without an account. Public answers
// never reveal whether an email already has an account.
// ---------------------------------------------------------------------------

const (
	// Verification emails to one address stop after 3 in an hour or 10
	// in a day, whatever the IP (10.1).
	guestVerifyPerHour = 3
	guestVerifyPerDay  = 10
	// The per-IP ceiling matches the account-request form (M1.3).
	guestIPLimit = 10
)

var (
	errGuestEventGone  = errors.New("event date no longer open to guests")
	errGuestHasAccount = errors.New("email already has an account")
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

// guestDisplay builds the signed-out view of one registration. Group
// details come only through groupShownTo for a nil viewer, so an opened
// invite-only event keeps its group hidden.
func (a *App) guestDisplay(reg *models.Registration, occ *models.EventOccurrence,
	ev *models.Event, grp *models.Group) guestRegistrationJSON {
	shown := a.groupShownTo(nil, grp, policy.Rel{})
	loc := ""
	if shown != nil {
		loc = shown.Location
	}
	inst := occurrenceToJSON(ev, occ, loc)
	out := guestRegistrationJSON{
		EventID: ev.ID, OccurrenceID: occ.ID, Date: occ.OriginalDate,
		Title: inst.Title, Status: reg.Status, StartsAt: inst.StartsAt,
		Location: inst.Location,
	}
	if ev.GroupID != nil && shown != nil {
		out.GroupName = shown.Name
	} else {
		out.HostedBy = a.siteName()
	}
	if out.Title == "" {
		out.Title = "Church event"
		if out.GroupName != "" {
			out.Title = out.GroupName
		}
	}
	return out
}

// loadGuestEvent loads the event in :id for a signed-out visitor. It
// answers 404 unless a guest may register for it, like every other
// hidden thing in the API.
func (a *App) loadGuestEvent(c *gin.Context, date string) (*models.Event, *models.EventOccurrence, eventFacts, bool) {
	id, _ := strconv.Atoi(c.Param("id"))
	var ev models.Event
	if err := a.DB.First(&ev, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "event not found")
		return nil, nil, eventFacts{}, false
	}
	var occ models.EventOccurrence
	if a.DB.Where("event_id = ? AND original_date = ? AND status = ?", ev.ID, date, models.OccurrenceScheduled).First(&occ).Error != nil {
		jsonError(c, 404, "event not found")
		return nil, nil, eventFacts{}, false
	}
	f := a.factsFor(nil, dateEvent(&ev, &occ))
	gp := policy.Group{}
	if f.group != nil {
		gp = asGroup(f.group)
	}
	if !policy.CanRegisterGuest(f.view, gp) {
		jsonError(c, http.StatusNotFound, "event not found")
		return nil, nil, eventFacts{}, false
	}
	return &ev, &occ, f, true
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
	ev, occ, f, ok := a.loadGuestEvent(c, strings.TrimSpace(body.Date))
	if !ok {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || len(name) > 100 {
		jsonError(c, http.StatusBadRequest, "name is required (at most 100 characters)")
		return
	}
	email := normalizeEmail(body.Email)
	if !validEmail(email) {
		jsonError(c, http.StatusBadRequest, "enter a valid email address")
		return
	}
	if !body.Over18 {
		// The age gate on the guest door (5.10): nothing is stored.
		jsonError(c, http.StatusBadRequest,
			"Ask a parent or guardian to register you. Guests registering on their own must be 18 or older.")
		return
	}
	hits, err := a.throttleHit(c.Request.Context(), "guestreg:ip:"+rateLimitIP(c.ClientIP()), time.Hour)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save your registration")
		return
	}
	if hits > guestIPLimit {
		// No 429 here (DEC-52): refusing out loud would confirm the
		// address is being mailed. The emails simply stop.
		log.Printf("guest registration rate limited: ip=%s", c.ClientIP())
		c.JSON(http.StatusOK, gin.H{"message": signupThanks})
		return
	}
	if ok, err := a.throttleAllow(c.Request.Context(), "guestreg:site", publicSignupSiteLimit, time.Hour); err != nil || !ok {
		log.Printf("guest registration site limit: err=%v", err)
		c.JSON(http.StatusOK, gin.H{"message": signupThanks})
		return
	}
	if strings.TrimSpace(body.Website) != "" {
		c.JSON(http.StatusOK, gin.H{"message": signupThanks})
		return
	}
	// Every in-budget submission pays the same bcrypt cost before account
	// lookup or either address limit, even when that address is exhausted.
	// IP/site limits and the honeypot above shed CPU without inspecting it.
	(&models.User{}).CheckPassword("not a real password")
	var existing models.User
	if a.DB.Where("email = ?", email).Limit(1).Find(&existing).RowsAffected == 1 &&
		existing.Status != models.UserGuest {
		// The hint is an email to that address too, so it shares the
		// per-address limit (3 an hour, 10 a day) whatever the IP (10.1).
		if ok, err := a.targetAllow(c.Request.Context(), "guestreg:hint:"+email); err == nil && ok {
			a.runInBackground(func() {
				if err := a.sendAlreadyHaveAccountEmail(email); err != nil {
					log.Printf("account-exists email to %s: %v", email, err)
				}
			})
		}
		c.JSON(http.StatusOK, gin.H{"message": signupThanks})
		return
	}
	if a.guestVerifyLimited(email, time.Now().UTC()) {
		log.Printf("guest verification emails to one address limited (DEC-52)")
		c.JSON(http.StatusOK, gin.H{"message": signupThanks})
		return
	}
	raw, hash, err := createInvitationToken()
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
	if err := a.DB.Create(&signup).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save your registration")
		return
	}
	a.runInBackground(func() {
		if err := a.sendGuestVerificationEmail(&signup, raw, ev, occ, f); err != nil {
			log.Printf("guest verification email to %s: %v", email, err)
		}
	})
	c.JSON(http.StatusOK, gin.H{"message": signupThanks})
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
	var emailed []uint
	var waitlistPos *int
	var fullNoWaitlist bool
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var signup models.GuestSignup
		if err := tx.Where("token_hash = ? AND confirmed_at IS NULL AND expires_at > now()",
			hashToken(body.Token)).First(&signup).Error; err != nil {
			return errTokenInvalid
		}
		occ, err := lockOccurrence(tx, signup.OccurrenceID)
		if err != nil {
			return errGuestEventGone
		}
		var ev models.Event
		if err := tx.First(&ev, occ.EventID).Error; err != nil {
			return errGuestEventGone
		}
		var grp *models.Group
		gp := policy.Group{}
		if ev.GroupID != nil {
			var g models.Group
			if err := tx.First(&g, *ev.GroupID).Error; err != nil {
				return errGuestEventGone
			}
			grp = &g
			gp = asGroup(&g)
		}
		// The door may have closed since the form was sent: the event
		// tightened, or the date was cancelled.
		if occ.Status != models.OccurrenceScheduled ||
			!policy.CanRegisterGuest(viewOf(dateEvent(&ev, occ), false), gp) {
			return errGuestEventGone
		}
		var user models.User
		found := tx.Where("email = ?", signup.Email).Limit(1).Find(&user).RowsAffected == 1
		if found && user.Status != models.UserGuest {
			// An account arrived for this address after the form. The
			// person proved the mailbox is theirs by opening the link,
			// so saying so reveals nothing to a stranger.
			return errGuestHasAccount
		}
		// A guest who confirms again already holds their row; like
		// registerLocked, their own seats do not count against them.
		var prior models.Registration
		havePrior := found &&
			tx.Where("user_id = ? AND occurrence_id = ?", user.ID, occ.ID).Limit(1).Find(&prior).RowsAffected == 1
		// Seat check under the lock, before anything is created.
		cap := events.EffectiveCapacity(ev.Capacity, occ.CapacityOverride)
		used, err := seatsUsed(tx, occ.ID)
		if err != nil {
			return err
		}
		if havePrior && prior.Status == models.RegGoing && prior.AttendanceMode == models.AttendanceInPerson {
			used -= 1 + prior.GuestCount
		}
		if cap != nil && used+1 > *cap && !ev.WaitlistEnabled {
			// No seat and no waitlist: the link dies here (the update
			// commits), nothing is created, and the page says the
			// event is full.
			if err := tx.Model(&models.GuestSignup{}).Where("id = ?", signup.ID).
				Update("confirmed_at", time.Now().UTC()).Error; err != nil {
				return err
			}
			fullNoWaitlist = true
			return nil
		}
		full := cap != nil && used+1 > *cap
		// Claim the link atomically: a second redeem of the same token
		// loses here instead of registering twice.
		now := time.Now().UTC()
		res := tx.Model(&models.GuestSignup{}).
			Where("id = ? AND confirmed_at IS NULL", signup.ID).
			Update("confirmed_at", now)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errTokenInvalid
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
		raw, hash, err := createInvitationToken()
		if err != nil {
			return err
		}
		manageRaw = raw
		status := models.RegGoing
		var wlAt *time.Time
		if full {
			status = models.RegWaitlisted
			wlAt = &now
			if havePrior && prior.Status == models.RegWaitlisted && prior.WaitlistedAt != nil {
				wlAt = prior.WaitlistedAt // keep their place in line
			}
		}
		reg := models.Registration{UserID: &user.ID, OccurrenceID: occ.ID}
		if havePrior && prior.Status == models.RegRemoved {
			// A pruned guest can't re-enter through the public form;
			// a leader or host adds them back (mirrors registerLocked).
			return errRemovedBlocked
		}
		err = tx.Where("user_id = ? AND occurrence_id = ?", user.ID, occ.ID).
			Attrs(models.Registration{Status: status, Source: models.RegSourceGuestForm, AttendanceMode: models.AttendanceInPerson}).
			FirstOrCreate(&reg).Error
		if err != nil {
			return err
		}
		reg.Status = status
		reg.Source = models.RegSourceGuestForm
		// Guests register in person; the seat check above counted that pool.
		reg.AttendanceMode = models.AttendanceInPerson
		reg.ManageTokenHash = hash
		reg.WaitlistedAt = wlAt
		if err := tx.Save(&reg).Error; err != nil {
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
			ids, err := createGuestUpgradeRequest(tx, &signup)
			if err != nil {
				return err
			}
			emailed = ids
		}
		display = a.guestDisplay(&reg, occ, &ev, grp)
		display.WaitlistPosition = waitlistPos
		guestEmail = signup.Email
		guestZone = recipientZone(tx, user.ID)
		return nil
	})
	switch {
	case errors.Is(err, errTokenInvalid):
		jsonError(c, http.StatusBadRequest, "that link is invalid or has expired")
		return
	case errors.Is(err, errGuestEventGone):
		jsonError(c, http.StatusConflict, "that event date is no longer open for guest registration")
		return
	case errors.Is(err, errGuestHasAccount):
		jsonError(c, http.StatusConflict, "that email already has an account; sign in to register")
		return
	case errors.Is(err, errRemovedBlocked):
		jsonError(c, http.StatusConflict, errRemovedBlocked.Error())
		return
	case err != nil:
		jsonError(c, http.StatusInternalServerError, "could not confirm your registration")
		return
	}
	if fullNoWaitlist {
		body := errorBody(http.StatusConflict, "This event is full")
		body["full"] = true
		c.JSON(http.StatusConflict, body)
		return
	}
	a.deliverNotificationEmails(emailed)
	if err := a.sendGuestConfirmationEmail(guestEmail, manageRaw, display, guestZone, askedAccount); err != nil {
		log.Printf("guest confirmation email to %s: %v", guestEmail, err)
	}
	c.JSON(http.StatusOK, gin.H{"registration": display, "manageToken": manageRaw})
}

// createGuestUpgradeRequest opens the guest_upgrade account request a
// guest asked for on the form (5.10). A still-pending request for the
// same address is left alone: the admins already have the task.
func createGuestUpgradeRequest(tx *gorm.DB, signup *models.GuestSignup) ([]uint, error) {
	var pending int64
	tx.Model(&models.AccountRequest{}).
		Where("email = ? AND status = ?", signup.Email, models.RequestPending).Count(&pending)
	if pending > 0 {
		return nil, nil
	}
	req := models.AccountRequest{
		Name: signup.Name, Email: signup.Email,
		AccountType: models.AccountAdult, Source: models.RequestGuestUpgrade,
		Status: models.RequestPending, Flags: "{}", IP: signup.IP,
		ExpiresAt: time.Now().UTC().Add(requestLifetime),
	}
	if err := tx.Create(&req).Error; err != nil {
		return nil, err
	}
	if err := audit.Write(tx, audit.Entry{Action: audit.AccountRequested,
		TargetType: "account_request", TargetID: req.ID,
		Meta: map[string]any{"source": models.RequestGuestUpgrade,
			"accountType": models.AccountAdult, "hasWarnings": false},
		IP: signup.IP}); err != nil {
		return nil, err
	}
	return notifyAccountRequested(tx, &req, models.RequestFlags{})
}

// liveTokenManage finds the registration a manage token controls. The
// token identifies a credential: authorizedTokenManage also checks current
// subject access for guests, promoted members and household rows. It stops
// working once the date has ended: there is nothing left to manage, and
// a bearer link should not outlive its purpose (DEC-55).
func liveTokenManage(db *gorm.DB, raw string) (*models.Registration, *models.EventOccurrence, *models.Event, *models.Group, error) {
	if raw == "" {
		return nil, nil, nil, nil, errTokenInvalid
	}
	var reg models.Registration
	if err := db.Preload("Occurrence.Event").Joins(listedRegistrations()).
		Where("registrations.manage_token_hash = ? AND registrations.manage_token_hash <> ?", hashToken(raw), "").
		Where("registrations.status <> ? AND (registrations.household_person_id IS NULL OR registrations.status <> ?)", models.RegRemoved, models.RegCancelled).
		First(&reg).Error; err != nil {
		return nil, nil, nil, nil, errTokenInvalid
	}
	occ := reg.Occurrence
	if occ.ID == 0 || time.Now().UTC().After(occ.EndsAt) || occ.Status != models.OccurrenceScheduled {
		return nil, nil, nil, nil, errTokenInvalid
	}
	ev := occ.Event
	var grp *models.Group
	if ev.GroupID != nil {
		var g models.Group
		if err := db.First(&g, *ev.GroupID).Error; err != nil {
			return nil, nil, nil, nil, err
		}
		grp = &g
	}
	return &reg, &occ, &ev, grp, nil
}

// authorizedTokenManage runs under the contact gate. The holder-first lock
// prefix matches registration writes; the occurrence lock protects both reads
// and idempotent cancellations against a concurrent visibility change.
func (a *App) authorizedTokenManage(tx *gorm.DB, raw string) (*models.Registration, *models.EventOccurrence, *models.Event, *models.Group, error) {
	reg, occ, _, _, err := liveTokenManage(tx, raw)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if err := lockRegistrationHolder(tx, reg.ID, occ.ID); err != nil {
		return nil, nil, nil, nil, err
	}
	if _, err := lockOccurrence(tx, occ.ID); err != nil {
		return nil, nil, nil, nil, err
	}
	reg, occ, ev, grp, err := liveTokenManage(tx, raw)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if err := a.authorizeRegistrationLink(tx, reg, occ, ev, grp); err != nil {
		return nil, nil, nil, nil, err
	}
	return reg, occ, ev, grp, nil
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
		var accesses []models.OccurrenceAccess
		if err := tx.Where("registration_id = ? AND subject_user_id = ? AND occurrence_id = ? AND status = ?", reg.ID, holder.ID, occ.ID, policy.OccurrenceAccessActive).Find(&accesses).Error; err != nil {
			return err
		}
		reads, err := occurrenceInvitationReadsIn(tx, accesses, time.Now().UTC())
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
		var person models.HouseholdPerson
		if err := tx.First(&person, *reg.HouseholdPersonID).Error; err != nil {
			return err
		}
		var account models.HouseholdAccount
		if err := tx.Where("user_id = ? AND household_id = ?", holder.ID, person.HouseholdID).First(&account).Error; err != nil {
			return err
		}
		var links []models.GuardianLink
		if err := tx.Where("person_id = ?", person.ID).Find(&links).Error; err != nil {
			return err
		}
		zone, err := contactZone(tx)
		if err != nil {
			return err
		}
		hr := householdRelOf(account, true)
		if policy.CanRegisterHouseholdPerson(asPerson(&holder), asPerson(&holder), hr, hr, householdFacts(&person, links, time.Now().In(zone)), holder.CanSignIn(), allowed, allowed) != "" {
			return errTokenInvalid
		}
	}
	return nil
}

func (a *App) apiGetGuestRegistration(c *gin.Context) {
	// Reject dead credentials without queuing a public request on the global
	// contact gate. This is only a preflight; current authority and the token
	// are checked again under the gate before any registration is displayed.
	if _, _, _, _, err := liveTokenManage(a.DB, c.Param("token")); err != nil {
		jsonError(c, http.StatusNotFound, "registration not found")
		return
	}
	var display guestRegistrationJSON
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockContacts(tx); err != nil {
			return err
		}
		reg, occ, ev, grp, err := a.authorizedTokenManage(tx, c.Param("token"))
		if err != nil {
			return err
		}
		display = a.guestDisplay(reg, occ, ev, grp)
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
	c.JSON(http.StatusOK, gin.H{"registration": display})
}

func (a *App) apiCancelGuestRegistration(c *gin.Context) {
	// Discover only the reconciliation scope here. The token is authenticated
	// again under the contact gate, before any registration locks or writes.
	_, _, scope, _, err := liveTokenManage(a.DB, c.Param("token"))
	if err != nil {
		jsonError(c, http.StatusNotFound, "registration not found")
		return
	}
	var display guestRegistrationJSON
	var emailed []uint
	err = a.contactMutation(contactChange{EventID: scope.ID}, func(tx *gorm.DB) error {
		reg, occ, ev, grp, err := a.authorizedTokenManage(tx, c.Param("token"))
		if err != nil {
			return err
		}
		if ev.ID != scope.ID {
			return errTokenInvalid
		}
		before := reg.Status
		cancellable := before == models.RegGoing || before == models.RegMaybe ||
			before == models.RegWaitlisted
		if !cancellable {
			display = a.guestDisplay(reg, occ, ev, grp)
			return nil
		}
		// authorizedTokenManage keeps the occurrence locked through the
		// cancel and promotion check, so a freed seat cannot be taken twice.
		res := tx.Model(&models.Registration{}).
			Where("id = ? AND status IN ?", reg.ID,
				[]string{models.RegGoing, models.RegMaybe, models.RegWaitlisted}).
			Updates(map[string]any{"status": models.RegCancelled, "waitlisted_at": nil, "promoted_at": nil})
			// The manage token stays live: cancel is idempotent (a second
			// POST with the same link is a 200 no-op), and the token can
			// then only ever show or re-cancel this dead registration.
			// Current date authority is still required on every replay.
			// It dies at the date end, and a new manage email rotates it.
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errTokenInvalid
		}
		if reg.HouseholdPersonID != nil {
			if err := tx.Model(reg).Update("manage_token_hash", "").Error; err != nil {
				return err
			}
		}
		reg.Status = models.RegCancelled
		if err := audit.Write(tx, audit.Entry{Action: audit.RegistrationCancelled,
			TargetType: "event", TargetID: ev.ID,
			Meta: registrationMeta(*reg),
			IP:   c.ClientIP()}); err != nil {
			return err
		}
		if before == models.RegGoing {
			ids, err := a.promoteLocked(tx, ev, occ, time.Now().UTC(), nil)
			if err != nil {
				return err
			}
			emailed = ids
		}
		display = a.guestDisplay(reg, occ, ev, grp)
		return nil
	})
	if err != nil {
		jsonError(c, http.StatusNotFound, "registration not found")
		return
	}
	a.deliverNotificationEmails(emailed)
	c.JSON(http.StatusOK, gin.H{"registration": display})
}

// ---------------------------------------------------------------------------
// Guest emails (C6: plain text through the minimal sender). Both name
// the event as a signed-out visitor may see it; a hidden group is
// never named.
// ---------------------------------------------------------------------------

func guestWithLine(d guestRegistrationJSON) string {
	if d.GroupName != "" {
		return " with " + d.GroupName
	}
	if d.HostedBy != "" {
		return " hosted by " + d.HostedBy
	}
	return ""
}

func (a *App) sendGuestVerificationEmail(signup *models.GuestSignup, raw string,
	ev *models.Event, occ *models.EventOccurrence, f eventFacts) error {
	pending := models.Registration{Status: models.RegGoing}
	d := a.guestDisplay(&pending, occ, ev, f.group)
	link := a.emailBaseURL() + "/guest/confirm?token=" + raw
	return a.Mailer.Send(context.Background(), foldmail.Message{
		To:      signup.Email,
		Subject: fmt.Sprintf("Confirm your registration for %s", d.Title),
		Text: fmt.Sprintf(`Hi %s,

Someone used this email address to register as a guest for %s%s at %s, on %s.

Confirm your registration here (the link works once and expires in 24 hours):
%s

If that wasn't you, ignore this email and nothing will be registered.`,
			signup.Name, d.Title, guestWithLine(d), a.siteName(), formatWhen(d.StartsAt, zoneOrChurch(a.DB, signup.Timezone)), link),
	})
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
	return a.Mailer.Send(context.Background(), foldmail.Message{
		To:      to,
		Subject: headline,
		Text: intro + fmt.Sprintf(`

Can't come after all? Cancel your registration here:
%s`, link),
	})
}
