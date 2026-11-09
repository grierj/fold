package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/jobs"
	"fold/internal/mail"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (a *App) apiAdminListUsers(c *gin.Context) {
	var users []models.User
	if err := a.DB.Order("lower(name), email").Find(&users).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list users")
		return
	}
	var historyIDs []uint
	if err := a.DB.Model(&models.TeenConsent{}).Distinct().Pluck("teen_user_id", &historyIDs).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list users")
		return
	}
	history := make(map[uint]bool, len(historyIDs))
	for _, id := range historyIDs {
		history[id] = true
	}
	suppressed, err := openSuppressions(a.DB)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list users")
		return
	}
	out := make([]userJSON, 0, len(users))
	for i := range users {
		row := a.toUserJSON(&users[i])
		row.HasTeenConsentHistory = history[users[i].ID]
		row.EmailSuppression = suppressed[mail.AddressHash(users[i].Email)]
		out = append(out, row)
	}
	c.JSON(http.StatusOK, gin.H{"users": out})
}

// apiAdminCreateUser adds an account. Without a password it starts
// invited and can't sign in until someone sets one (M1.2 emails the link).
func (a *App) apiAdminCreateUser(c *gin.Context) {
	var body struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		Role        string `json:"role"`
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
		Phone       string `json:"phone"`
		AgeBand     string `json:"ageBand"`
		BirthDate   string `json:"birthDate"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	kind, birth, ok := a.classifyAccountInput(c, body.AgeBand, body.BirthDate)
	if !ok {
		return
	}
	email := mail.NormalizeAddress(body.Email)
	if !validEmail(email) {
		fail(c, errValidEmailRequired)
		return
	}
	name, err := requiredName(body.Name)
	if err != nil {
		fail(c, err)
		return
	}
	if body.Password != "" && !checkNewPassword(c, body.Password) {
		return
	}
	role := body.Role
	if role == "" {
		role = models.RoleMember
	}
	if role != models.RoleAdmin && role != models.RoleMember {
		jsonError(c, http.StatusBadRequest, "role must be admin or member")
		return
	}
	if kind == models.AccountTeen && role == models.RoleAdmin {
		jsonError(c, http.StatusBadRequest, "a teen account can't be an admin")
		return
	}
	u := models.User{
		Email: email, Role: role, AccountType: kind, BirthDate: birth,
		Name: name, DisplayName: strings.TrimSpace(body.DisplayName), Phone: strings.TrimSpace(body.Phone),
	}
	if body.Password != "" {
		if err := u.SetPassword(body.Password); err != nil {
			jsonError(c, http.StatusInternalServerError, "could not create user")
			return
		}
	}
	u.Status = newAccountStatus(kind, body.Password != "")
	me := apiUser(c)
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockContacts(tx); err != nil {
			return err
		}
		actor, err := lockAdminIn(tx, me, actorNoKeyUpdate)
		if err != nil {
			return err
		}
		if err := tx.Create(&u).Error; err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &actor.ID, Action: audit.UserCreated,
			TargetType: "user", TargetID: u.ID, Meta: map[string]any{"role": u.Role}, IP: c.ClientIP()})
	})
	if answerStaleSession(c, err) {
		return
	}
	if err != nil {
		if emailTaken(err) {
			fail(c, errEmailTaken)
			return
		}
		jsonError(c, http.StatusInternalServerError, "could not create user")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"user": a.toUserJSON(&u)})
}

// emailTaken reports a violation of the unique login email on users, and no
// other unique index.
func emailTaken(err error) bool {
	return pgConstraint(err, pgUniqueViolation, "uni_users_email")
}

// loadTargetUser reads the account the :id route parameter names; nil when it
// is absent (or cannot be read). It also returns the parsed id.
func (a *App) loadTargetUser(c *gin.Context) (*models.User, int) {
	id, _ := strconv.Atoi(c.Param("id"))
	var u models.User
	if err := a.DB.First(&u, id).Error; err != nil {
		return nil, id
	}
	return &u, id
}

func (a *App) adminTargetUser(c *gin.Context) (*models.User, bool) {
	u, _ := a.loadTargetUser(c)
	if u == nil {
		jsonError(c, http.StatusNotFound, "user not found")
		return nil, false
	}
	return u, true
}

// activeAdmins scopes tx to the active admin accounts: the set the last-admin
// rule counts and the admins who are told about account changes.
func activeAdmins(tx *gorm.DB) *gorm.DB {
	return tx.Model(&models.User{}).Where("role = ? AND status = ?", models.RoleAdmin, models.UserActive)
}

// adminCount is adminCountIn for the early, ungated check. The gated decision
// (keepsAnAdminIn) is the authority; a failed read here counts as none, which
// only ever refuses.
func (a *App) adminCount() int {
	n, _ := adminCountIn(a.DB)
	return n
}

// adminCountIn counts the active admins on the connection that holds the
// locks, so the in-gate decision sees the same rows.
func adminCountIn(tx *gorm.DB) (int, error) {
	var n int64
	if err := activeAdmins(tx).Count(&n).Error; err != nil {
		return 0, err
	}
	return int(n), nil
}

// The deletion's answers that name an outcome (R4-CTX; classifyContactErr
// decides which). The first two are real copy on a 5xx on purpose.
var (
	errDeleteOutcomeUnknown = apiErr(http.StatusInternalServerError, "delete_outcome_unknown", "could not confirm whether the account was deleted")
	errDeleteTimedOut       = apiErr(http.StatusServiceUnavailable, "delete_timed_out", "deleting took too long, so nothing was deleted")
	errDeleteHouseholdBusy  = apiErr(http.StatusConflict, "delete_household_busy", "someone was changing this person's household, so nothing was deleted")
)

// adminChange is what an admin is about to do to an account that the
// last-admin and self rules (policy.CanDemoteOrDelete) can refuse.
type adminChange int

const (
	adminDemote adminChange = iota
	adminDisable
	adminDelete
)

// adminRuleRefusal is the answer when policy.CanDemoteOrDelete refuses change:
// the self rule when the admin acts on their own account, else the last-admin
// rule. The pre-gate checks and the locked recheck give the same answer.
func adminRuleRefusal(change adminChange, self bool) *apiError {
	if self {
		return adminRuleRefusals[change][1]
	}
	return adminRuleRefusals[change][0]
}

var adminRuleRefusals = [...][2]*apiError{
	adminDemote:  {errCannotDemoteLastAdmin, errCannotDemoteSelf},
	adminDisable: {errCannotDisableLastAdmin, errCannotDisableSelf},
	adminDelete:  {errCannotDeleteLastAdmin, errCannotDeleteSelf},
}

func isAdminRuleRefusal(err error) bool {
	for _, pair := range adminRuleRefusals {
		if errors.Is(err, pair[0]) || errors.Is(err, pair[1]) {
			return true
		}
	}
	return false
}

// keepsAnAdminIn decides the last-admin rule on the locked target, the actor
// the callback re-read and an in-gate count. It returns nil when the locked
// target is not an active admin, so acting on a non-active admin is allowed
// at any count (policy.CanDemoteOrDelete), and the rule's refusal
// (adminRuleRefusal) when it refuses.
//
// Under the gate, with the target locked and the actor re-read as an active
// admin distinct from the target, the count is at least 2, so this refuses
// nothing today's API can build (R4-H's actor re-read already closes two
// admins deleting each other). It is defense in depth against a future writer
// outside the gate, and it lets PUT judge its effective change.
func keepsAnAdminIn(tx *gorm.DB, actor, target *models.User, change adminChange) error {
	if !asPerson(target).Admin {
		return nil
	}
	n, err := adminCountIn(tx)
	if err != nil {
		return err
	}
	if !policy.CanDemoteOrDelete(asPerson(actor), asPerson(target), n) {
		return adminRuleRefusal(change, actor.ID == target.ID)
	}
	return nil
}

var errTeenActivation = apiErr(http.StatusBadRequest, "", "a teen account waiting for consent becomes active only when consent is granted")

// errAdminTargetGone: the person an admin edit names was erased while the edit
// waited for the contact gate.
var errAdminTargetGone = apiErr(http.StatusNotFound, "", "user not found")

func (a *App) apiAdminUpdateUser(c *gin.Context) {
	u, ok := a.adminTargetUser(c)
	if !ok {
		return
	}
	me := apiUser(c)
	var body struct {
		Name        *string `json:"name"`
		DisplayName *string `json:"displayName"`
		Email       *string `json:"email"`
		Phone       *string `json:"phone"`
		Role        *string `json:"role"`
		Status      *string `json:"status"`
		// Successors: required when disabling someone who leads a group alone
		// (DEC-36), as for deletion.
		Successors map[string]uint `json:"successors"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	updates := map[string]interface{}{}
	bump := false
	if body.Name != nil {
		name, err := optionalName(*body.Name)
		if err != nil {
			fail(c, err)
			return
		}
		updates["name"] = name
	}
	if body.DisplayName != nil {
		dn, err := displayName(*body.DisplayName)
		if err != nil {
			fail(c, err)
			return
		}
		updates["display_name"] = dn
	}
	if body.Email != nil {
		email := mail.NormalizeAddress(*body.Email)
		if !validEmail(email) {
			fail(c, errValidEmailRequired)
			return
		}
		if email != mail.NormalizeAddress(u.Email) {
			updates["email"] = email
			bump = true
		}
	}
	if body.Phone != nil {
		phone, err := phoneNumber(*body.Phone)
		if err != nil {
			fail(c, err)
			return
		}
		updates["phone"] = phone
	}
	if body.Role != nil {
		role := strings.TrimSpace(*body.Role)
		if role != models.RoleAdmin && role != models.RoleMember {
			jsonError(c, http.StatusBadRequest, "role must be admin or member")
			return
		}
		// A teen is never an admin (#94, DEC-8): admins grant teen consent.
		if role == models.RoleAdmin && u.AccountType == models.AccountTeen {
			jsonError(c, http.StatusBadRequest, "a teen account can't be an admin")
			return
		}
		if role != models.RoleAdmin && !policy.CanDemoteOrDelete(asPerson(me), asPerson(u), a.adminCount()) {
			fail(c, adminRuleRefusal(adminDemote, u.ID == me.ID))
			return
		}
		if role != u.Role {
			updates["role"] = role
			bump = true
		}
	}
	if body.Status != nil {
		status := strings.TrimSpace(*body.Status)
		if status != models.UserActive && status != models.UserDisabled {
			jsonError(c, http.StatusBadRequest, "status must be active or disabled")
			return
		}
		if status == models.UserDisabled && !policy.CanDemoteOrDelete(asPerson(me), asPerson(u), a.adminCount()) {
			fail(c, adminRuleRefusal(adminDisable, u.ID == me.ID))
			return
		}
	}
	if len(updates) > 0 || body.Status != nil {
		err := a.contactMutation(contactChange{UserID: u.ID}, func(tx *gorm.DB) error {
			actor, target, err := lockConsentUsers(tx, me.ID, u.ID)
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					// Erased while this request waited for the contact gate: the lookup
					// above found them, the locked read no longer does.
					return errAdminTargetGone
				}
				return err
			}
			if !adminCurrent(actor, me) {
				return errConsentForbidden
			}
			u = target
			if body.Role != nil && *body.Role == models.RoleAdmin && u.AccountType == models.AccountTeen {
				return errConsentTeen
			}
			if body.Status != nil {
				status := strings.TrimSpace(*body.Status)
				if status == models.UserActive && u.Status == models.UserPendingConsent {
					return errTeenActivation
				}
				adminDisabled := status == models.UserDisabled
				updates["administratively_disabled"] = adminDisabled
				if status == models.UserActive && u.AccountType == models.AccountTeen {
					granted, err := latestTeenConsentIn(tx, u.ID, models.TeenConsentAccount)
					if err != nil {
						return err
					}
					if !granted {
						status = models.UserPendingConsent
					}
				} else if status == models.UserActive && u.PasswordHash == nil {
					status = models.UserInvited
				}
				if status != u.Status || adminDisabled != u.AdministrativelyDisabled {
					updates["status"] = status
					bump = true
				}
			}
			// The locked target leaves the active-admin set when the effective
			// update demotes it or makes it anything but active (disabled,
			// invited, pending consent). Nothing has been written yet.
			newRole, roleSet := updates["role"].(string)
			newStatus, statusSet := updates["status"].(string)
			demotes := roleSet && newRole != models.RoleAdmin
			deactivates := statusSet && newStatus != models.UserActive
			if demotes || deactivates {
				change := adminDisable
				if demotes {
					change = adminDemote
				}
				if err := keepsAnAdminIn(tx, actor, u, change); err != nil {
					return err
				}
			}
			if newRole, ok := updates["role"].(string); ok && newRole != u.Role {
				if err := audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.UserRoleChanged, TargetType: "user", TargetID: u.ID,
					Meta: map[string]any{"oldRole": u.Role, "newRole": newRole}, IP: c.ClientIP()}); err != nil {
					return err
				}
			}
			if newEmail, ok := updates["email"].(string); ok {
				if err := audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.UserEmailChanged, TargetType: "user", TargetID: u.ID,
					Meta: map[string]any{"oldEmail": u.Email, "newEmail": newEmail}, IP: c.ClientIP()}); err != nil {
					return err
				}
			}
			if _, changed := updates["email"]; changed {
				if err := revokeAccountTokens(tx, u.ID); err != nil {
					return err
				}
			}
			if bump {
				updates["session_version"] = sessionVersionBump()
			}
			status, statusSet := updates["status"].(string)
			if !statusSet {
				if err := tx.Model(u).Updates(updates).Error; err != nil {
					return err
				}
			} else {
				wasDisabled := u.Status == models.UserDisabled
				if status == models.UserDisabled && !wasDisabled {
					// The handoff must see the account listed (leaderCount
					// counts listed users), so it runs before the status flips.
					if err := handOffLeadership(c, tx, u.ID, body.Successors, "account_disabled"); err != nil {
						return err
					}
				}
				if err := a.setAccountStatusIn(tx, u, updates, me.ID, c.ClientIP()); err != nil {
					return err
				}
				switch {
				case status == models.UserDisabled:
					return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.UserDisabled,
						TargetType: "user", TargetID: u.ID, IP: c.ClientIP()})
				case wasDisabled:
					if err := audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.UserEnabled,
						TargetType: "user", TargetID: u.ID, IP: c.ClientIP()}); err != nil {
						return err
					}
				}
			}
			today, err := churchClockIn(tx, time.Now().UTC())
			if err != nil {
				return err
			}
			return a.completeTeenConversionIfReady(tx, u, today, c.ClientIP())
		})
		if err != nil {
			if emailTaken(err) {
				fail(c, errEmailTaken)
				return
			}
			fail(c, err)
			return
		}
	}
	var updated models.User
	a.DB.First(&updated, u.ID)
	c.JSON(http.StatusOK, gin.H{"user": a.toUserJSON(&updated)})
}

type deleteUserBody struct {
	// AttemptID is the id minted for this press (POST
	// /admin/operation-attempts). Old clients send none and keep today's
	// behavior apart from the deadline and the outcome codes (T07B-1b).
	AttemptID string `json:"attemptId"`
	// Successors maps group id to the new leader, for every group this
	// person leads alone (DEC-36; see GET /admin/users/:id/sole-leader-groups).
	Successors map[string]uint `json:"successors"`
	// HostSuccessors maps church-wide event id to the new host, for every
	// event this person hosts alone (M7.4 owner decision 3; see GET
	// /admin/users/:id/sole-host-events).
	HostSuccessors map[string]uint `json:"hostSuccessors"`
	// ReasonReview is the admin's review of every contact-request reason
	// they were shown: the set-v1 envelope (format, count, digest, choices)
	// of account_erasure_reasons_envelope.go. It is kept raw and read by the
	// strict token scan after the submission check; it matters only when
	// the account has reasons (T07B-2b).
	ReasonReview json.RawMessage `json:"reasonReview"`
}

// readDeleteUserBody reads the body once and decodes it the way gin's binder
// does (the first JSON value; anything after it is ignored), so the strict key
// scan sees the same object. An empty body is fine (old clients, and people
// who lead and host nothing alone); ok is false for a body that does not
// parse and for an attemptId named twice or in another case, which leaves no
// single id to claim.
func readDeleteUserBody(c *gin.Context) (body deleteUserBody, keys topKeys, ok bool) {
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return body, keys, false
	}
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&body); err != nil {
		return deleteUserBody{}, keys, errors.Is(err, io.EOF)
	}
	keys = scanTopKeys(raw)
	if keys.attemptAmbiguous {
		return deleteUserBody{}, keys, false
	}
	return body, keys, true
}

func (a *App) apiAdminDeleteUser(c *gin.Context) {
	me := apiUser(c)
	body, keys, bodyOK := readDeleteUserBody(c)
	u, ok := a.adminDeletionTarget(c, me, body.AttemptID)
	if !ok {
		return
	}
	// A body that does not parse, or names attemptId ambiguously, is refused
	// recording nothing.
	if !bodyOK {
		jsonError(c, http.StatusBadRequest, "invalid body")
		return
	}
	// The submission check is the first step after the body is decoded
	// (R4-C08), so every refusal below ends the id rolled back: a client may
	// treat each as definitive. Two refusals come before it and record
	// nothing: the 404 for a missing target and the body that did not parse.
	var claim *attemptClaim
	if body.AttemptID != "" {
		var r submitResult
		if claim, r = a.submitAttempt(body.AttemptID, attemptKindAccountDelete, me.ID, u.ID); claim == nil {
			answerSubmit(c, r)
			return
		}
		defer claim.guard()
	}
	if a.restoreDetected() {
		claim.end(attemptRolledBack)
		answerRestoreDetected(c)
		return
	}
	if !policy.CanDemoteOrDelete(asPerson(me), asPerson(u), a.adminCount()) {
		claim.end(attemptRolledBack)
		fail(c, adminRuleRefusal(adminDelete, u.ID == me.ID))
		return
	}
	hostPicks, ok := parseHostSuccessors(body.HostSuccessors)
	if !ok {
		claim.end(attemptRolledBack)
		jsonError(c, http.StatusBadRequest, "invalid host successors")
		return
	}
	// The review's shape is checked after the submission check, so a bad one
	// ends the id rolled back like every other refusal (R4-C08). What a well-formed
	// one means (ignored, outdated, changed) only the transaction decides.
	reasonEnv, ok := parseReasonEnvelope(body.ReasonReview, keys.reviewAmbiguous)
	if !ok {
		claim.end(attemptRolledBack)
		jsonError(c, http.StatusBadRequest, "invalid reason review")
		return
	}
	// The account is erased by eraseAccountIn (account_erasure.go), after the
	// receipt, the host plan, the last-admin rule and the reason review below.
	//
	// The deletion has its own deadline, not the request's: a browser that
	// disconnects must not abort a commit. 40 seconds is the 60 second write
	// timeout minus 20 (as syncMailTimeout), and contactMutationCtx bounds the
	// server to it too (R4-CTX).
	ctx, cancel := context.WithTimeout(context.Background(), adminDeleteBudget)
	defer cancel()
	// The whole call retries when a household row is busy (R4-3b-1, inventory
	// 9.6 requirement 5): each attempt starts from the target's id alone and
	// reads the account, the actor and every set again under its own locks, so
	// nothing a rolled-back attempt filled in carries over. body and hostPicks
	// are only read.
	targetID := u.ID
	retry := newDeleteRetry(ctx)
	var err error
	// What the household and audit steps did, for the response. The callback
	// replaces it on every successful attempt; rolled-back values are unused.
	var counts erasureCounts
	// What the reason review decided, for the response, the audit rows and the
	// seal. Reset at the start of every attempt.
	var plan reasonPlan
	for {
		plan = reasonPlan{}
		// A fresh restore ticket per attempt (T07B-1b), taken by the wrapper
		// before the callback runs.
		err = a.fencedContactMutation(ctx, contactChange{UserID: targetID}, func(tx *gorm.DB) (*contactSeal, error) {
			u := &models.User{ID: targetID}
			// The receipt: the first write after the ticket, committing only
			// with the deletion, so a lost reply can be answered "applied".
			if claim != nil {
				if err := insertAttemptReceipt(tx, claim.nonce); err != nil {
					return nil, err
				}
			}
			// Hosted events FOR KEY SHARE, then the account FOR UPDATE, then the
			// admin (plain read) and the facts again; see api_host_handoff.go.
			picks, lockedHosted, actor, err := deletionHostPlan(tx, u, me, hostPicks)
			if err != nil {
				return nil, err
			}
			// The last-admin rule again, on the locked target, the re-read actor
			// and an in-gate count (defense in depth; the pre-gate check above
			// decides the usual answers).
			if err := keepsAnAdminIn(tx, actor, u, adminDelete); err != nil {
				return nil, err
			}
			// The contact-request reasons: locked and hashed here, applied by
			// eraseAccountIn. An account without any ignores the review.
			if plan, err = a.planReasonReviewIn(tx, actor, u, reasonEnv, c.ClientIP()); err != nil {
				return nil, err
			}
			// The subject is read from the row deletionHostPlan just locked, so
			// the address is the one the account holds now (memory only).
			counts, err = a.eraseAccountIn(tx, captureErasureSubject(u), erasureOptions{Actor: &me.ID, IP: c.ClientIP(), Live: &liveErasure{
				User: u, C: c, Successors: body.Successors, HostPicks: picks, LockedHosted: lockedHosted, Reasons: plan,
			}})
			if err != nil {
				return nil, err
			}
			// The actor is decided again on their locked row as the last
			// locking statement before COMMIT (R5-C01), with or without an
			// attempt id; when the account had reasons, that row must also be
			// allowed to review them.
			return a.deletionSeal(me, plan.mayReview), nil
		})
		var again bool
		if again, err = retry.after(err); !again {
			break
		}
		if a.hooks.deleteBeforeRetry != nil {
			a.hooks.deleteBeforeRetry()
		}
	}
	// The id's final state is recorded before any response is sent: applied
	// only from an acknowledged COMMIT, unknown from a commit that may have
	// happened, rolled back otherwise (every refusal after the claim).
	claim.end(outcomeAttemptState(err))
	if errors.Is(err, errNeedsSuccessor) || errors.Is(err, errNeedsHostSuccessor) {
		fail(c, err)
		return
	}
	if errors.Is(err, errAdminTargetGone) {
		fail(c, err)
		return
	}
	if answerStaleSession(c, err) {
		return
	}
	// The reason review (T07B-2b). Each is a refusal before COMMIT: the id above
	// already ended rolled back and nothing was deleted.
	if errors.Is(err, errNeedsReasonReview) {
		fail(c, errNeedsReasonReview)
		return
	}
	if errors.Is(err, errReasonReviewChanged) {
		fail(c, errReasonReviewChanged)
		return
	}
	if errors.Is(err, errReasonReviewOutdated) {
		fail(c, errReasonReviewOutdated)
		return
	}
	if errors.Is(err, errReasonReviewForbidden) || (errors.Is(err, errActorForbidden) && plan.mayReview != nil) {
		fail(c, errReasonReviewForbidden)
		return
	}
	if errors.Is(err, errActorForbidden) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return
	}
	if isAdminRuleRefusal(err) {
		fail(c, err)
		return
	}
	if err != nil {
		// Only classifyContactErr decides what a failure means. The log line
		// never names the account or an address (jobs.CleanError).
		switch classifyContactErr(err) {
		case outcomeUnknown:
			// COMMIT may have reached the server: never say nothing changed.
			log.Printf("could not confirm user deletion: %s", jobs.CleanError(err))
			fail(c, errDeleteOutcomeUnknown)
		case outcomeTimedOut:
			log.Printf("user deletion timed out (rollback confirmed: %v): %s", rollbackConfirmed(err), jobs.CleanError(err))
			fail(c, errDeleteTimedOut)
		default:
			if errors.Is(err, errRestoreDetected) {
				log.Printf("user deletion refused: the database was restored while Fold was running")
				answerRestoreDetected(c)
				return
			}
			if errors.Is(err, errActorBusy) {
				// The admin's own row was held at the seal on every attempt:
				// nothing was deleted, COMMIT was never sent.
				log.Printf("user deletion found the admin's own account busy (rollback confirmed: %v): %s", rollbackConfirmed(err), jobs.CleanError(err))
				fail(c, errActorBusy)
				return
			}
			if errors.Is(err, errReasonCounterpartBusy) {
				// Blanking a reason needed a deleted party's users row and every
				// attempt found it held (or the rollback after one was not
				// acknowledged): nothing was deleted, COMMIT was never sent.
				// Named apart from a busy household, which it is not.
				log.Printf("user deletion found a reviewed request's deleted party busy (rollback confirmed: %v): %s", rollbackConfirmed(err), jobs.CleanError(err))
				fail(c, errReasonCounterpartBusy)
				return
			}
			if isHouseholdBusy(err) {
				// Every attempt found a household row held by someone else (or
				// the rollback after one was not acknowledged, so no further
				// attempt began): nothing was deleted, COMMIT was never sent.
				log.Printf("user deletion found a household busy (rollback confirmed: %v): %s", rollbackConfirmed(err), jobs.CleanError(err))
				fail(c, errDeleteHouseholdBusy)
				return
			}
			log.Printf("could not delete user (rollback confirmed: %v): %s", rollbackConfirmed(err), jobs.CleanError(err))
			jsonError(c, http.StatusInternalServerError, "could not delete user")
		}
		return
	}
	report := newErasureReport(counts.Households, counts.Audit)
	report.ReasonsKept, report.ReasonsRemoved = plan.counts()
	c.JSON(http.StatusOK, gin.H{"erasure": report})
}

func (a *App) apiAdminResetPassword(c *gin.Context) {
	u, ok := a.adminTargetUser(c)
	if !ok {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Password == "" {
		fail(c, errPasswordRequired)
		return
	}
	if !checkNewPassword(c, body.Password) {
		return
	}
	var prepared models.User
	if err := prepared.SetPassword(body.Password); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not reset password")
		return
	}
	me := apiUser(c)
	err := a.contactMutation(contactChange{UserID: u.ID}, func(tx *gorm.DB) error {
		actor, target, err := lockConsentUsers(tx, me.ID, u.ID)
		if err != nil {
			return err
		}
		if !adminCurrent(actor, me) {
			return errSecuritySession
		}
		if err := replacePasswordIn(tx, target, *prepared.PasswordHash); err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &actor.ID, Action: audit.PasswordResetCompleted,
			TargetType: "user", TargetID: target.ID, IP: c.ClientIP(), Meta: map[string]any{"byAdmin": true}})
	})
	if answerStaleSession(c, err) {
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not reset password")
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}
