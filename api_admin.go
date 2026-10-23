package main

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ---------------------------------------------------------------------------
// Admin endpoints
// ---------------------------------------------------------------------------

func (a *App) apiPendingGroups(c *gin.Context) {
	var groups []models.Group
	a.DB.Where("status = ?", models.GroupPending).Order("created_at").Find(&groups)
	out := make([]groupSummaryJSON, 0, len(groups))
	for i := range groups {
		out = append(out, a.groupSummary(&groups[i]))
	}
	c.JSON(http.StatusOK, gin.H{"groups": out})
}

// apiApproveGroup and apiRejectGroup decide a pending group. Both are
// conditional on the group still being pending, and resolve every admin's
// task in the same transaction (5.12). A request from the bell names its
// notification (fromNotification); only Approve is allow-listed (6.6).
func (a *App) apiApproveGroup(c *gin.Context) {
	var body struct {
		Tags             []string `json:"tags"`
		FromNotification *uint    `json:"fromNotification"`
	}
	_ = c.ShouldBindJSON(&body)
	a.decideGroup(c, notify.ActionApproveGroup, body.FromNotification, func(g *models.Group) {
		if body.Tags != nil {
			g.Tags = joinTags(body.Tags)
		}
		g.Status = models.GroupActive
	})
}

func (a *App) apiRejectGroup(c *gin.Context) {
	var body struct {
		FromNotification *uint `json:"fromNotification"`
	}
	_ = c.ShouldBindJSON(&body)
	a.decideGroup(c, "reject", body.FromNotification, func(g *models.Group) { g.Status = models.GroupArchived })
}

func (a *App) decideGroup(c *gin.Context, action string, fromNotification *uint, apply func(*models.Group)) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	if !a.fromNotification(c, fromNotification, action, g.ID) {
		return
	}
	me := apiUser(c)
	outcome, auditAction := "approved", audit.GroupApproved
	if action != notify.ActionApproveGroup {
		outcome, auditAction = "rejected", audit.GroupRejected
	}
	apply(&g)
	errHandled := errors.New("already handled")
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.Group{}).Where("id = ? AND status = ?", g.ID, models.GroupPending).
			Updates(map[string]any{"status": g.Status, "tags": g.Tags})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errHandled
		}
		if _, err := notify.Resolve(tx, notify.GroupPending, g.ID, &me.ID, outcome); err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: auditAction, TargetType: "group", TargetID: g.ID, IP: c.ClientIP()})
	})
	if errors.Is(err, errHandled) {
		a.alreadyHandled(c, notify.GroupPending, g.ID)
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not "+action+" group")
		return
	}
	if action == notify.ActionApproveGroup {
		c.JSON(http.StatusOK, gin.H{"group": a.groupSummary(&g)})
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

// ---------------------------------------------------------------------------
// Admin: guardian links (M1.9 slice 1, DEC-32). A child or teen added to
// a household waits here; only an admin confirms it, with a note on how
// it was checked. There is no auto-confirm path.
// ---------------------------------------------------------------------------

type guardianLinkJSON struct {
	ID             uint       `json:"id"`
	GuardianUserID uint       `json:"guardianUserId"`
	GuardianName   string     `json:"guardianName"`
	TeenUserID     *uint      `json:"teenUserId,omitempty"`
	PersonID       *uint      `json:"personId,omitempty"`
	PersonName     string     `json:"personName"`
	PersonKind     string     `json:"personKind"` // child | teen
	BirthDate      string     `json:"birthDate,omitempty"`
	HouseholdID    uint       `json:"householdId,omitempty"`
	HouseholdName  string     `json:"householdName,omitempty"`
	Status         string     `json:"status"` // pending | confirmed | rejected | revoked
	CheckNote      string     `json:"checkNote"`
	ConfirmedBy    *uint      `json:"confirmedBy,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	DecidedAt      *time.Time `json:"decidedAt,omitempty"`
}

func (a *App) toGuardianLinkJSON(link *models.GuardianLink) guardianLinkJSON {
	out := guardianLinkJSON{
		ID: link.ID, GuardianUserID: link.GuardianUserID, GuardianName: nameIn(a.DB, link.GuardianUserID),
		TeenUserID: link.TeenUserID, PersonID: link.PersonID, Status: link.Status,
		CheckNote: link.CheckNote, ConfirmedBy: link.ConfirmedBy,
		CreatedAt: link.CreatedAt, DecidedAt: link.DecidedAt,
	}
	if link.PersonID != nil {
		var person models.HouseholdPerson
		if err := a.DB.Unscoped().First(&person, *link.PersonID).Error; err == nil {
			out.PersonName = person.FullName()
			out.PersonKind = person.Kind
			out.BirthDate = formatBirthDate(person.BirthDate)
			out.HouseholdID = person.HouseholdID
			var household models.Household
			if err := a.DB.First(&household, person.HouseholdID).Error; err == nil {
				out.HouseholdName = household.Name
			}
		}
	} else if link.TeenUserID != nil {
		out.PersonName = nameIn(a.DB, *link.TeenUserID)
		out.PersonKind = models.HouseholdTeen
		var teen models.User
		if err := a.DB.Unscoped().First(&teen, *link.TeenUserID).Error; err == nil {
			out.BirthDate = formatBirthDate(teen.BirthDate)
		}
	}
	return out
}

func (a *App) apiListGuardianLinks(c *gin.Context) {
	status := c.DefaultQuery("status", models.GuardianPending)
	q := a.DB.Order("created_at, id")
	switch status {
	case models.GuardianPending, models.GuardianConfirmed, models.GuardianRejected, models.GuardianRevoked:
		q = q.Where("status = ?", status)
	case "all":
	default:
		jsonError(c, http.StatusBadRequest, "unknown status")
		return
	}
	var rows []models.GuardianLink
	q.Find(&rows)
	out := make([]guardianLinkJSON, 0, len(rows))
	for i := range rows {
		out = append(out, a.toGuardianLinkJSON(&rows[i]))
	}
	nominations, err := unresolvedGuardianNominations(a.DB)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load guardian nominations")
		return
	}
	c.JSON(http.StatusOK, gin.H{"guardianLinks": out, "nominations": nominations})
}

func (a *App) apiConfirmGuardianLink(c *gin.Context) { a.decideGuardianLink(c, true) }
func (a *App) apiRejectGuardianLink(c *gin.Context)  { a.decideGuardianLink(c, false) }

// decideGuardianLink confirms or rejects a pending guardian link,
// conditional on it still being pending, and resolves every admin's task
// in the same transaction (5.12). Confirming requires a check note
// saying how the admin checked (DEC-32); "note" is accepted for the same
// field, like the other approval endpoints.
func (a *App) decideGuardianLink(c *gin.Context, confirm bool) {
	id, _ := strconv.Atoi(c.Param("id"))
	var link models.GuardianLink
	if err := a.DB.First(&link, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "guardian link not found")
		return
	}
	me := apiUser(c)
	if !policy.CanConfirmGuardianLink(asPerson(me)) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return
	}
	var body struct {
		CheckNote string `json:"checkNote"`
		Note      string `json:"note"`
	}
	_ = c.ShouldBindJSON(&body)
	note := strings.TrimSpace(body.CheckNote)
	if note == "" {
		note = strings.TrimSpace(body.Note)
	}
	if confirm && note == "" {
		jsonError(c, http.StatusBadRequest, "a check note is required to confirm this link")
		return
	}
	if len(note) > 500 {
		jsonError(c, http.StatusBadRequest, "check note must be at most 500 characters")
		return
	}
	// DEC-79: an admin cannot confirm their own guardian link while another
	// active admin exists; a sole admin may, and the audit marks it.
	selfConfirm := confirm && link.GuardianUserID == me.ID
	if selfConfirm {
		var otherAdmins int64
		if err := a.DB.Model(&models.User{}).Where("id <> ? AND role = ? AND status = ?", me.ID, models.RoleAdmin, models.UserActive).
			Count(&otherAdmins).Error; err != nil {
			jsonError(c, http.StatusInternalServerError, "could not check admins")
			return
		}
		if otherAdmins > 0 {
			jsonError(c, http.StatusForbidden, "another admin must confirm your own guardian link")
			return
		}
	}
	status, outcome, auditAction := models.GuardianRejected, "rejected", audit.GuardianLinkRejected
	if confirm {
		status, outcome, auditAction = models.GuardianConfirmed, "approved", audit.GuardianLinkConfirmed
	}
	errHandled := errors.New("already handled")
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if link.PersonID != nil {
			var person models.HouseholdPerson
			if err := tx.First(&person, *link.PersonID).Error; err != nil {
				return err
			}
			if err := lockHousehold(tx, person.HouseholdID); err != nil {
				return err
			}
			if err := tx.First(&person, *link.PersonID).Error; err != nil {
				return err
			}
		}
		now := time.Now().UTC()
		updates := map[string]any{"status": status, "decided_at": now, "check_note": note}
		if confirm {
			updates["confirmed_by"] = me.ID
		}
		res := tx.Model(&models.GuardianLink{}).Where("id = ? AND status = ?", link.ID, models.GuardianPending).
			Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errHandled
		}
		if _, err := notify.Resolve(tx, notify.HouseholdLinkPending, link.ID, &me.ID, outcome); err != nil {
			return err
		}
		meta := map[string]any{"guardianUserId": link.GuardianUserID, "checkNote": note}
		if selfConfirm {
			meta["selfConfirm"] = true
		}
		if link.PersonID != nil {
			meta["personId"] = *link.PersonID
		}
		if link.TeenUserID != nil {
			meta["teenUserId"] = *link.TeenUserID
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: auditAction,
			TargetType: "guardian_link", TargetID: link.ID, Meta: meta, IP: c.ClientIP()})
	})
	if errors.Is(err, errHandled) {
		a.alreadyHandled(c, notify.HouseholdLinkPending, link.ID)
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not decide that link")
		return
	}
	var fresh models.GuardianLink
	a.DB.First(&fresh, link.ID)
	c.JSON(http.StatusOK, gin.H{"guardianLink": a.toGuardianLinkJSON(&fresh)})
}

// ---------------------------------------------------------------------------
// Admin: user management
// ---------------------------------------------------------------------------

func (a *App) apiAdminListUsers(c *gin.Context) {
	var users []models.User
	a.DB.Order("lower(name), email").Find(&users)
	out := make([]userJSON, 0, len(users))
	for i := range users {
		out = append(out, a.toUserJSON(&users[i]))
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
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	email := normalizeEmail(body.Email)
	if !validEmail(email) {
		jsonError(c, http.StatusBadRequest, "a valid email is required")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || len(name) > 100 {
		jsonError(c, http.StatusBadRequest, "name is required (at most 100 characters)")
		return
	}
	if body.Password != "" && len(body.Password) < minPasswordLength {
		jsonError(c, http.StatusBadRequest,
			"password must be at least "+strconv.Itoa(minPasswordLength)+" characters")
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
	u := models.User{
		Email: email, Role: role, Status: models.UserInvited,
		Name: name, DisplayName: strings.TrimSpace(body.DisplayName), Phone: strings.TrimSpace(body.Phone),
	}
	if body.Password != "" {
		if err := u.SetPassword(body.Password); err != nil {
			jsonError(c, http.StatusInternalServerError, "could not create user")
			return
		}
		u.Status = models.UserActive
	}
	if err := a.DB.Create(&u).Error; err != nil {
		if emailTaken(err) {
			jsonError(c, http.StatusConflict, "an account with that email already exists")
			return
		}
		jsonError(c, http.StatusInternalServerError, "could not create user")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"user": a.toUserJSON(&u)})
}

// emailTaken reports a unique-email violation.
func emailTaken(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (a *App) adminTargetUser(c *gin.Context) (*models.User, bool) {
	id, _ := strconv.Atoi(c.Param("id"))
	var u models.User
	if err := a.DB.First(&u, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "user not found")
		return nil, false
	}
	return &u, true
}

// adminCount counts admin accounts.
func (a *App) adminCount() int {
	var n int64
	a.DB.Model(&models.User{}).Where("role = ? AND status = ?", models.RoleAdmin, models.UserActive).Count(&n)
	return int(n)
}

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
		if len(*body.Name) > 100 {
			jsonError(c, http.StatusBadRequest, "name must be at most 100 characters")
			return
		}
		updates["name"] = strings.TrimSpace(*body.Name)
	}
	if body.DisplayName != nil {
		if len(*body.DisplayName) > 100 {
			jsonError(c, http.StatusBadRequest, "display name must be at most 100 characters")
			return
		}
		updates["display_name"] = strings.TrimSpace(*body.DisplayName)
	}
	if body.Email != nil {
		email := normalizeEmail(*body.Email)
		if !validEmail(email) {
			jsonError(c, http.StatusBadRequest, "a valid email is required")
			return
		}
		if email != normalizeEmail(u.Email) {
			updates["email"] = email
			bump = true
		}
	}
	if body.Phone != nil {
		if len(*body.Phone) > 30 {
			jsonError(c, http.StatusBadRequest, "phone must be at most 30 characters")
			return
		}
		updates["phone"] = strings.TrimSpace(*body.Phone)
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
			msg := "cannot demote the last admin"
			if u.ID == me.ID {
				msg = "you cannot demote yourself"
			}
			jsonError(c, http.StatusBadRequest, msg)
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
			msg := "cannot disable the last admin"
			if u.ID == me.ID {
				msg = "you cannot disable your own account"
			}
			jsonError(c, http.StatusBadRequest, msg)
			return
		}
	}
	if len(updates) > 0 || body.Status != nil {
		var emailed []uint
		errTeenActivation := errors.New("a teen account waiting for consent becomes active only when consent is granted")
		err := a.DB.Transaction(func(tx *gorm.DB) error {
			actor, target, err := lockConsentUsers(tx, me.ID, u.ID)
			if err != nil {
				return err
			}
			if !policy.IsAdmin(asPerson(actor)) || actor.SessionVersion != me.SessionVersion {
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
			if bump {
				updates["session_version"] = gorm.Expr("session_version + 1")
			}
			if updates["status"] == models.UserDisabled {
				if u.Status == models.UserDisabled {
					if err := tx.Model(u).Updates(updates).Error; err != nil {
						return err
					}
					return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.UserDisabled, TargetType: "user", TargetID: u.ID, IP: c.ClientIP()})
				}
				ids, err := handOffLeadership(c, tx, u.ID, body.Successors, "account_disabled")
				emailed = append(emailed, ids...)
				if err != nil {
					return err
				}
				// Flip the status before the seat work: the rest of this
				// transaction (seat counts, the waitlist) must see the
				// account as disabled, since disabled accounts use no
				// seats (DEC-38). The handoff above must see it active:
				// leaderCount counts listed users.
				if err := tx.Model(u).Updates(updates).Error; err != nil {
					return err
				}
				// Disabling keeps registration rows but frees every seat
				// the account held on future dates, promoting into them.
				ids, err = a.promoteOnDisable(tx, u.ID)
				emailed = append(emailed, ids...)
				if err != nil {
					return err
				}
				return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.UserDisabled,
					TargetType: "user", TargetID: u.ID, IP: c.ClientIP()})
			}
			if s, ok := updates["status"].(string); ok && u.Status == models.UserDisabled && s != models.UserDisabled {
				ids, err := restoreMemberships(c, tx, u.ID)
				emailed = append(emailed, ids...)
				if err != nil {
					return err
				}
				// Registrations come back as they were when they fit;
				// a row that would overfill is dropped and the person
				// is told to ask a leader or host (DEC-38, DEC-47, M1.5).
				ids, err = a.restoreRegistrations(c, tx, u.ID, s)
				emailed = append(emailed, ids...)
				if err != nil {
					return err
				}
				if err := audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.UserEnabled,
					TargetType: "user", TargetID: u.ID, IP: c.ClientIP()}); err != nil {
					return err
				}
			}
			return tx.Model(u).Updates(updates).Error
		})
		if needSuccessors(c, err) {
			return
		}
		if err != nil {
			if errors.Is(err, errTeenActivation) {
				jsonError(c, http.StatusBadRequest, err.Error())
				return
			}
			if errors.Is(err, errConsentForbidden) || errors.Is(err, errConsentTeen) {
				consentError(c, err)
				return
			}
			if emailTaken(err) {
				jsonError(c, http.StatusConflict, "an account with that email already exists")
				return
			}
			jsonError(c, http.StatusInternalServerError, "could not update user")
			return
		}
		a.deliverNotificationEmails(emailed)
	}
	var updated models.User
	a.DB.First(&updated, u.ID)
	c.JSON(http.StatusOK, gin.H{"user": a.toUserJSON(&updated)})
}

func (a *App) apiAdminDeleteUser(c *gin.Context) {
	u, ok := a.adminTargetUser(c)
	if !ok {
		return
	}
	me := apiUser(c)
	if !policy.CanDemoteOrDelete(asPerson(me), asPerson(u), a.adminCount()) {
		msg := "cannot delete the last admin"
		if u.ID == me.ID {
			msg = "you cannot delete your own account"
		}
		jsonError(c, http.StatusBadRequest, msg)
		return
	}
	var body struct {
		// Successors maps group id to the new leader, for every group this
		// person leads alone (DEC-36; see GET /admin/users/:id/sole-leader-groups).
		Successors map[string]uint `json:"successors"`
	}
	_ = c.ShouldBindJSON(&body) // optional when they lead no group alone
	// Soft delete (DeletedAt), after handing off every group they lead alone
	// and ending their memberships. The email is replaced so the address is
	// free for a new account, and the session version is bumped so open
	// sessions end.
	var emailed []uint
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(u, u.ID).Error; err != nil {
			return err
		}
		ids, err := handOffLeadership(c, tx, u.ID, body.Successors, "account_deleted")
		emailed = append(emailed, ids...)
		if err != nil {
			return err
		}
		if err := tx.Model(u).Updates(map[string]any{
			"email":           fmt.Sprintf("deleted-%d@deleted.invalid", u.ID),
			"session_version": gorm.Expr("session_version + 1"),
		}).Error; err != nil {
			return err
		}
		if err := audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.UserDeleted,
			TargetType: "user", TargetID: u.ID, IP: c.ClientIP()}); err != nil {
			return err
		}
		// Notifications go with the person (5.3); the audit log stays.
		if err := tx.Where("user_id = ?", u.ID).Delete(&models.Notification{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(u).Error; err != nil {
			return err
		}
		// The deleted account no longer counts, so its seats and childcare
		// spots are free: promote waiters now (Whittler, PR 109).
		promoted, err := a.promoteOnDisable(tx, u.ID)
		if err != nil {
			return err
		}
		emailed = append(emailed, promoted...)
		return nil
	})
	if needSuccessors(c, err) {
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not delete user")
		return
	}
	a.deliverNotificationEmails(emailed)
	c.JSON(http.StatusOK, gin.H{})
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
		jsonError(c, http.StatusBadRequest, "password is required")
		return
	}
	if len(body.Password) < minPasswordLength {
		jsonError(c, http.StatusBadRequest,
			"password must be at least "+strconv.Itoa(minPasswordLength)+" characters")
		return
	}
	if err := a.setPassword(u, body.Password); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not reset password")
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

// ---------------------------------------------------------------------------
// Admin: group + membership management
// ---------------------------------------------------------------------------

func (a *App) apiAdminListGroups(c *gin.Context) {
	var groups []models.Group
	a.DB.Order("name").Find(&groups)
	out := make([]groupSummaryJSON, 0, len(groups))
	for i := range groups {
		out = append(out, a.groupSummary(&groups[i]))
	}
	c.JSON(http.StatusOK, gin.H{"groups": out})
}

func (a *App) apiAdminAddMember(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	var body struct {
		UserID uint   `json:"userId"`
		Role   string `json:"role"`
		// OverLimit: add even though the group is full (DEC-39); audited.
		OverLimit bool `json:"overLimit"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.UserID == 0 {
		jsonError(c, http.StatusBadRequest, "userId is required")
		return
	}
	var u models.User
	if err := a.DB.First(&u, body.UserID).Error; err != nil {
		jsonError(c, http.StatusNotFound, "user not found")
		return
	}
	role := body.Role
	if role == "" {
		role = models.MembershipMember
	}
	if role != models.MembershipLeader && role != models.MembershipMember {
		jsonError(c, http.StatusBadRequest, "role must be leader or member")
		return
	}
	me := apiUser(c)
	err := a.changeRole(c, &g, u.ID, role, "", body.OverLimit, func(actor, t policy.Rel, leaders int) bool {
		// Admins may add someone straight in with either role.
		return policy.CanChangeRole(asPerson(me), actor, asPerson(&u),
			policy.Rel{Member: true, Leader: t.Leader}, role == models.MembershipLeader, leaders)
	})
	switch err {
	case nil:
	case errGroupFull:
		groupFull(c)
		return
	case errRoleRefused:
		jsonError(c, http.StatusBadRequest,
			"only adults can be leaders, and a group must keep at least one leader")
		return
	default:
		jsonError(c, http.StatusInternalServerError, "could not add member")
		return
	}
	var m models.Membership
	a.DB.Where("user_id = ? AND group_id = ?", u.ID, g.ID).First(&m)
	if g.DeletionRequested && memberCount(a.DB, g.ID) > 1 {
		a.clearDeletionRequest(g.ID, nil, "withdrawn")
	}
	c.JSON(http.StatusOK, gin.H{"membership": gin.H{
		"id": m.ID, "userId": m.UserID, "groupId": m.GroupID, "role": role,
	}})
}

func (a *App) apiAdminRemoveMember(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	a.removeMember(c, &g)
}

func (a *App) apiDeletionRequests(c *gin.Context) {
	var groups []models.Group
	a.DB.Where("deletion_requested = ?", true).Order("updated_at").Find(&groups)
	out := make([]groupSummaryJSON, 0, len(groups))
	for i := range groups {
		out = append(out, a.groupSummary(&groups[i]))
	}
	c.JSON(http.StatusOK, gin.H{"groups": out})
}

// apiRejectDeletion keeps the group and clears the leader's request.
func (a *App) apiRejectDeletion(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	if err := a.clearDeletionRequest(g.ID, &apiUser(c).ID, "kept"); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not reject deletion")
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

// apiAdminDeleteGroup removes any group. Unlike a leader's request, this
// does not wait for a membership check.
func (a *App) apiAdminDeleteGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	if err := a.deleteGroup(c.Request.Context(), &g, &apiUser(c).ID); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not delete group")
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}
