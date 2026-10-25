package main

import (
	"errors"
	"fold/internal/audit"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// contactPrivacyChanged records a person's own preference change, without
// copying their contact details into audit metadata.
const contactPrivacyChanged = "contact.privacy_changed"

// contactDetails never returns hidden values, even as empty JSON keys. Context
// facts come from the collection being rendered, not the viewer's other roles.
func contactDetails(viewer, subject *models.User, context policy.Contact) (email, phone *string) {
	if subject == nil {
		return nil, nil
	}
	context.Teen = subject.AccountType == models.AccountTeen
	context.HideFromLeaders = subject.HideContactFromLeaders
	if !policy.CanSeeContact(asPerson(viewer), viewer != nil && viewer.ID == subject.ID, context) {
		return nil, nil
	}
	return &subject.Email, &subject.Phone
}

// confirmedContactGuardians loads only confirmed links for this viewer and
// these teens. Query failure yields no guardian authority. Household relation
// alone and pending/rejected/revoked links never authorize contact disclosure.
func (a *App) confirmedContactGuardians(viewer *models.User, users map[uint]*models.User) map[uint]bool {
	out := map[uint]bool{}
	if viewer == nil {
		return out
	}
	var ids []uint
	for id, u := range users {
		if u.AccountType == models.AccountTeen {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return out
	}
	var links []models.GuardianLink
	if err := a.DB.Where("guardian_user_id = ? AND teen_user_id IN ? AND status = ?", viewer.ID, ids, models.GuardianConfirmed).Find(&links).Error; err != nil {
		return out
	}
	for _, link := range links {
		if link.TeenUserID != nil {
			out[*link.TeenUserID] = true
		}
	}
	return out
}

// presentAttendeeContacts uses the already loaded subjects. A household person
// has no contact details of their own: never substitute the account holder's.
func presentAttendeeContacts(rows []attendeeJSON, viewer *models.User, users map[uint]*models.User, guardians map[uint]bool, manager bool) {
	for i := range rows {
		r := &rows[i]
		if r.UserID == nil || r.HouseholdPerson != nil {
			continue
		}
		r.Email, r.Phone = contactDetails(viewer, users[*r.UserID], policy.Contact{
			EventManager:      manager,
			EventRegistrant:   r.Status != models.RegRemoved && r.Status != models.RegCancelled,
			ConfirmedGuardian: guardians[*r.UserID],
		})
	}
}

// Contact request APIs deliberately use dedicated DTOs: neither a list nor a
// review includes email/phone values. Only POST reveal can return those keys.
type contactRequestJSON struct {
	ID               uint       `json:"id"`
	RequesterID      uint       `json:"requesterId"`
	RequesterName    string     `json:"requesterName"`
	SubjectUserID    uint       `json:"subjectUserId"`
	SubjectName      string     `json:"subjectName"`
	ContextType      string     `json:"contextType"`
	ContextID        *uint      `json:"contextId,omitempty"`
	Fields           string     `json:"fields"`
	Reason           string     `json:"reason"`
	NeedsAdmin       bool       `json:"needsAdmin"`
	SubjectDecision  string     `json:"subjectDecision"`
	SubjectDecidedBy *uint      `json:"subjectDecidedBy,omitempty"`
	SubjectDecidedAt *time.Time `json:"subjectDecidedAt,omitempty"`
	AdminDecision    string     `json:"adminDecision"`
	AdminDecidedBy   *uint      `json:"adminDecidedBy,omitempty"`
	AdminDecidedAt   *time.Time `json:"adminDecidedAt,omitempty"`
	Status           string     `json:"status"`
	CreatedAt        time.Time  `json:"createdAt"`
	ExpiresAt        time.Time  `json:"expiresAt"`
	CompletedAt      *time.Time `json:"completedAt,omitempty"`
	EndReason        string     `json:"endReason"`
	CanSubjectDecide bool       `json:"canSubjectDecide"`
	CanAdminDecide   bool       `json:"canAdminDecide"`
	CanWithdraw      bool       `json:"canWithdraw"`
}

type contactGrantJSON struct {
	ID            uint       `json:"id"`
	RequestID     uint       `json:"requestId"`
	RequesterID   uint       `json:"requesterId"`
	RequesterName string     `json:"requesterName"`
	SubjectUserID uint       `json:"subjectUserId"`
	SubjectName   string     `json:"subjectName"`
	ContextType   string     `json:"contextType"`
	ContextID     *uint      `json:"contextId,omitempty"`
	Fields        string     `json:"fields"`
	NeedsAdmin    bool       `json:"needsAdmin"`
	GrantedAt     time.Time  `json:"grantedAt"`
	EndedAt       *time.Time `json:"endedAt,omitempty"`
	EndReason     string     `json:"endReason"`
	EndedBy       *uint      `json:"endedBy,omitempty"`
	CanReveal     bool       `json:"canReveal"`
	CanRevoke     bool       `json:"canRevoke"`
}

type contactRevealJSON struct {
	Email *string `json:"email,omitempty"`
	Phone *string `json:"phone,omitempty"`
}

type contactOperation struct {
	actor          *models.User
	now            time.Time
	zone           *time.Location
	mailed         []uint
	refused        bool
	refusalMessage string
	refusalStatus  int
}

// Rejected domain operations COMMIT their refusal audit, rather than returning
// an error from the transaction and accidentally rolling that audit back.
func (a *App) contactOperation(c *gin.Context, fn func(*gorm.DB, *contactOperation) error) {
	op := contactOperation{}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockContacts(tx); err != nil {
			return err
		}
		op.now = time.Now().UTC().Truncate(time.Microsecond)
		var err error
		op.zone, err = contactZone(tx)
		if err != nil {
			return err
		}
		op.actor, err = contactUser(tx, apiUser(c).ID)
		if err != nil {
			return err
		}
		if op.actor == nil || !op.actor.CanSignIn() || op.actor.SessionVersion != apiUser(c).SessionVersion {
			op.actor = &models.User{ID: apiUser(c).ID}
			return refuseContact(tx, &op, models.ContactRequest{}, "stale_session", c.ClientIP())
		}
		return fn(tx, &op)
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not complete contact operation")
		return
	}
	a.deliverNotificationEmails(op.mailed)
	if op.refused {
		if op.refusalMessage == "" {
			op.refusalMessage = "direct-contact access is unavailable; refresh the request"
			op.refusalStatus = http.StatusForbidden
		}
		jsonError(c, op.refusalStatus, op.refusalMessage)
		return
	}
}
func refuseContact(tx *gorm.DB, op *contactOperation, r models.ContactRequest, operation, ip string) error {
	op.refused = true
	return contactAudit(tx, &op.actor.ID, audit.ContactRefused, r, operation, ip)
}
func contactID(c *gin.Context) uint {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	return uint(id)
}
func (a *App) contactContextVisible(tx *gorm.DB, u *models.User, r models.ContactRequest) (*uint, error) {
	var g models.Group
	groupID := r.ContextID
	if r.ContextType == policy.ContactEvent {
		var ev models.Event
		err := tx.First(&ev, r.ContextID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		f := a.factsForIn(tx, u, &ev)
		group := policy.Group{}
		if f.group != nil {
			group = asGroup(f.group)
		}
		if !policy.CanSeeEvent(asPerson(u), f.view, group, f.rel) {
			return nil, nil
		}
		id := r.ContextID
		return &id, nil
	}
	err := tx.First(&g, groupID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if a.groupShownTo(u, &g, relIn(tx, u.ID, g.ID)) == nil {
		return nil, nil
	}
	return &groupID, nil
}
func (a *App) presentContactRequest(tx *gorm.DB, op *contactOperation, r models.ContactRequest) (contactRequestJSON, error) {
	f, _, err := a.loadContactFacts(tx, contactScope(r), op.now)
	if err != nil {
		return contactRequestJSON{}, err
	}
	actor := contactAccount(op.actor, op.now, op.zone)
	p := contactRequestPolicy(r)
	contextID, err := a.contactContextVisible(tx, op.actor, r)
	if err != nil {
		return contactRequestJSON{}, err
	}
	return contactRequestJSON{ID: r.ID, RequesterID: r.RequesterID, RequesterName: nameIn(tx, r.RequesterID), SubjectUserID: r.SubjectUserID, SubjectName: nameIn(tx, r.SubjectUserID), ContextType: r.ContextType, ContextID: contextID, Fields: r.Fields, Reason: r.Reason, NeedsAdmin: r.NeedsAdmin, SubjectDecision: r.SubjectDecision, SubjectDecidedBy: r.SubjectDecidedBy, SubjectDecidedAt: r.SubjectDecidedAt, AdminDecision: r.AdminDecision, AdminDecidedBy: r.AdminDecidedBy, AdminDecidedAt: r.AdminDecidedAt, Status: r.Status, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, CompletedAt: r.CompletedAt, EndReason: r.EndReason,
		CanSubjectDecide: policy.ContactRequestStatusAfterDecision(actor, policy.ContactSubject, policy.ContactApproved, p, f) != "", CanAdminDecide: policy.ContactRequestStatusAfterDecision(actor, policy.ContactAdmin, policy.ContactApproved, p, f) != "", CanWithdraw: policy.CanWithdrawContactRequest(actor, p, op.now)}, nil
}
func (a *App) presentContactGrant(tx *gorm.DB, op *contactOperation, g models.ContactGrant, r models.ContactRequest) (contactGrantJSON, error) {
	f, _, err := a.loadContactFacts(tx, contactScope(r), op.now)
	if err != nil {
		return contactGrantJSON{}, err
	}
	guardian, err := contactConfirmedGuardian(tx, op.actor.ID, r.SubjectUserID)
	if err != nil {
		return contactGrantJSON{}, err
	}
	actor := contactAccount(op.actor, op.now, op.zone)
	p := contactGrantPolicy(g, r)
	contextID, err := a.contactContextVisible(tx, op.actor, r)
	if err != nil {
		return contactGrantJSON{}, err
	}
	return contactGrantJSON{ID: g.ID, RequestID: r.ID, RequesterID: r.RequesterID, RequesterName: nameIn(tx, r.RequesterID), SubjectUserID: r.SubjectUserID, SubjectName: nameIn(tx, r.SubjectUserID), ContextType: r.ContextType, ContextID: contextID, Fields: r.Fields, NeedsAdmin: r.NeedsAdmin, GrantedAt: g.GrantedAt, EndedAt: g.EndedAt, EndReason: g.EndReason, EndedBy: g.EndedBy, CanReveal: policy.CanRevealContactGrant(actor, contactScope(r), p, f), CanRevoke: policy.ContactGrantRevocationReason(actor, guardian, p) != ""}, nil
}
func (a *App) apiCreateContactRequest(c *gin.Context) {
	var body struct {
		SubjectUserID uint   `json:"subjectUserId"`
		ContextType   string `json:"contextType"`
		ContextID     uint   `json:"contextId"`
		Fields        string `json:"fields"`
		Reason        string `json:"reason"`
	}
	// Even malformed direct-contact attempts are audited, without retaining the
	// untrusted reason (which may itself contain private contact information).
	valid := c.ShouldBindJSON(&body) == nil && utf8.RuneCountInString(body.Reason) <= policy.ContactReasonLimit
	var out contactRequestJSON
	a.contactOperation(c, func(tx *gorm.DB, op *contactOperation) error {
		r := models.ContactRequest{RequesterID: op.actor.ID, SubjectUserID: body.SubjectUserID, ContextType: body.ContextType, ContextID: body.ContextID, Fields: body.Fields, Reason: strings.TrimSpace(body.Reason)}
		if !valid {
			if utf8.RuneCountInString(body.Reason) > policy.ContactReasonLimit {
				op.refusalMessage = "contact reason must be at most 1000 characters"
				op.refusalStatus = http.StatusBadRequest
			}
			return refuseContact(tx, op, r, "invalid_request", c.ClientIP())
		}
		ids, err := a.reconcileContacts(tx, op.now, contactChange{UserID: op.actor.ID})
		if err != nil {
			return err
		}
		op.mailed = append(op.mailed, ids...)
		f, _, err := a.loadContactFacts(tx, contactScope(r), op.now)
		if err != nil {
			return err
		}
		var count int64
		if err = tx.Model(&models.ContactRequest{}).Where("requester_id = ? AND status = ?", op.actor.ID, policy.ContactPending).Count(&count).Error; err != nil {
			return err
		}
		if !policy.CanCreateContactRequest(contactScope(r), f, int(count)) {
			if count >= policy.ContactOpenLimit && policy.CanCreateContactRequest(contactScope(r), f, 0) {
				op.refusalMessage = "you already have 10 open contact requests; withdraw one or wait for a decision"
				op.refusalStatus = http.StatusConflict
			}
			return refuseContact(tx, op, r, "create", c.ClientIP())
		}
		r.NeedsAdmin = f.Subject.Person.Teen
		r.AdminDecision = policy.ContactNotNeeded
		if r.NeedsAdmin {
			r.AdminDecision = policy.ContactPending
		}
		r.SubjectDecision, r.Status, r.CreatedAt, r.ExpiresAt = policy.ContactPending, policy.ContactPending, op.now, op.now.Add(policy.ContactRequestLifetime)
		if err = tx.Create(&r).Error; err != nil {
			return err
		}
		if err = contactAudit(tx, &op.actor.ID, audit.ContactRequested, r, "", c.ClientIP()); err != nil {
			return err
		}
		ids, err = contactNotice(tx, r, notify.ContactSubjectReview, []uint{r.SubjectUserID}, "subject-review")
		if err != nil {
			return err
		}
		op.mailed = append(op.mailed, ids...)
		if r.NeedsAdmin {
			var admins []uint
			if err = tx.Model(&models.User{}).Where("role = ? AND status = ?", models.RoleAdmin, models.UserActive).Order("id").Pluck("id", &admins).Error; err != nil {
				return err
			}
			ids, err = contactNotice(tx, r, notify.ContactAdminReview, admins, "admin-review")
			if err != nil {
				return err
			}
			op.mailed = append(op.mailed, ids...)
		}
		out, err = a.presentContactRequest(tx, op, r)
		return err
	})
	if !c.Writer.Written() {
		c.JSON(http.StatusCreated, gin.H{"request": out})
	}
}

func (a *App) apiListContactRequests(c *gin.Context) { a.readContactRequests(c, false) }
func (a *App) apiGetContactRequest(c *gin.Context)   { a.readContactRequests(c, true) }
func (a *App) readContactRequests(c *gin.Context, single bool) {
	out := []contactRequestJSON{}
	a.contactOperation(c, func(tx *gorm.DB, op *contactOperation) error {
		actor := contactAccount(op.actor, op.now, op.zone)
		q := tx.Order("id DESC").Limit(100)
		if single {
			q = q.Where("id = ?", contactID(c))
		} else {
			before, _ := strconv.ParseUint(c.Query("before"), 10, 64)
			if before > 0 {
				q = q.Where("id < ?", before)
			}
		}
		if !policy.IsAdmin(actor.Person) {
			q = q.Where("requester_id = ? OR subject_user_id = ?", op.actor.ID, op.actor.ID)
		}
		var rs []models.ContactRequest
		if err := q.Find(&rs).Error; err != nil {
			return err
		}
		for _, r := range rs {
			if policy.CanViewContactRequest(actor, contactRequestPolicy(r)) {
				j, err := a.presentContactRequest(tx, op, r)
				if err != nil {
					return err
				}
				out = append(out, j)
			}
		}
		return nil
	})
	if c.Writer.Written() {
		return
	}
	if single {
		if len(out) == 0 {
			jsonError(c, http.StatusNotFound, "not found")
			return
		}
		c.JSON(http.StatusOK, gin.H{"request": out[0]})
		return
	}
	c.JSON(http.StatusOK, gin.H{"requests": out})
}
func (a *App) apiDecideContactRequest(c *gin.Context) {
	var body struct {
		Party    string `json:"party"`
		Decision string `json:"decision"`
	}
	valid := c.ShouldBindJSON(&body) == nil
	var out contactRequestJSON
	a.contactOperation(c, func(tx *gorm.DB, op *contactOperation) error {
		var r models.ContactRequest
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&r, contactID(c)).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return refuseContact(tx, op, models.ContactRequest{ID: contactID(c)}, "decision", c.ClientIP())
		}
		if err != nil {
			return err
		}
		ids, err := a.reconcileContacts(tx, op.now, contactChange{UserID: r.RequesterID})
		if err != nil {
			return err
		}
		op.mailed = append(op.mailed, ids...)
		if err = tx.First(&r, r.ID).Error; err != nil {
			return err
		}
		f, _, err := a.loadContactFacts(tx, contactScope(r), op.now)
		if err != nil {
			return err
		}
		status := policy.ContactRequestStatusAfterDecision(contactAccount(op.actor, op.now, op.zone), body.Party, body.Decision, contactRequestPolicy(r), f)
		if !valid || status == "" {
			return refuseContact(tx, op, r, "decision", c.ClientIP())
		}
		if body.Party == policy.ContactSubject {
			r.SubjectDecision, r.SubjectDecidedBy, r.SubjectDecidedAt = body.Decision, &op.actor.ID, &op.now
		} else {
			r.AdminDecision, r.AdminDecidedBy, r.AdminDecidedAt = body.Decision, &op.actor.ID, &op.now
		}
		r.Status = status
		if status != policy.ContactPending {
			r.CompletedAt = &op.now
		}
		if err = tx.Save(&r).Error; err != nil {
			return err
		}
		if err = contactAudit(tx, &op.actor.ID, audit.ContactDecision, r, body.Party+":"+body.Decision, c.ClientIP()); err != nil {
			return err
		}
		kind := notify.ContactSubjectReview
		if body.Party == policy.ContactAdmin {
			kind = notify.ContactAdminReview
		}
		if _, err = notify.Resolve(tx, kind, r.ID, &op.actor.ID, body.Decision); err != nil {
			return err
		}
		if status != policy.ContactPending {
			if err = resolveContactTasks(tx, r, &op.actor.ID, status); err != nil {
				return err
			}
		}
		if status == policy.ContactGranted {
			g := models.ContactGrant{RequestID: r.ID, LeaderID: r.RequesterID, SubjectUserID: r.SubjectUserID, ContextType: r.ContextType, ContextID: r.ContextID, Fields: r.Fields, GrantedAt: op.now}
			if err = tx.Create(&g).Error; err != nil {
				return err
			}
			if err = contactAudit(tx, &op.actor.ID, audit.ContactGranted, r, "", c.ClientIP()); err != nil {
				return err
			}
		}
		recipients := []uint{r.RequesterID, r.SubjectUserID}
		if status == policy.ContactGranted && r.NeedsAdmin {
			guardians, err := contactGuardians(tx, r.SubjectUserID)
			if err != nil {
				return err
			}
			recipients = append(recipients, guardians...)
		}
		ids, err = contactNotice(tx, r, notify.ContactUpdated, recipients, "decision:"+body.Party)
		if err != nil {
			return err
		}
		op.mailed = append(op.mailed, ids...)
		out, err = a.presentContactRequest(tx, op, r)
		return err
	})
	if !c.Writer.Written() {
		c.JSON(http.StatusOK, gin.H{"request": out})
	}
}
func (a *App) apiWithdrawContactRequest(c *gin.Context) {
	a.contactOperation(c, func(tx *gorm.DB, op *contactOperation) error {
		var r models.ContactRequest
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&r, contactID(c)).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return refuseContact(tx, op, models.ContactRequest{ID: contactID(c)}, "withdraw", c.ClientIP())
		}
		if err != nil {
			return err
		}
		ids, err := a.reconcileContacts(tx, op.now, contactChange{UserID: r.RequesterID})
		if err != nil {
			return err
		}
		op.mailed = append(op.mailed, ids...)
		if err = tx.First(&r, r.ID).Error; err != nil {
			return err
		}
		if !policy.CanWithdrawContactRequest(contactAccount(op.actor, op.now, op.zone), contactRequestPolicy(r), op.now) {
			return refuseContact(tx, op, r, "withdraw", c.ClientIP())
		}
		r.Status, r.CompletedAt, r.WithdrawnByID = policy.ContactWithdrawn, &op.now, &op.actor.ID
		if err = tx.Save(&r).Error; err != nil {
			return err
		}
		if err = contactAudit(tx, &op.actor.ID, audit.ContactWithdrawn, r, "", c.ClientIP()); err != nil {
			return err
		}
		if err = resolveContactTasks(tx, r, &op.actor.ID, r.Status); err != nil {
			return err
		}
		ids, err = contactNotice(tx, r, notify.ContactUpdated, []uint{r.SubjectUserID}, "withdrawn")
		op.mailed = append(op.mailed, ids...)
		return err
	})
	if !c.Writer.Written() {
		c.JSON(http.StatusOK, gin.H{})
	}
}

func (a *App) apiListContactGrants(c *gin.Context) {
	out := []contactGrantJSON{}
	a.contactOperation(c, func(tx *gorm.DB, op *contactOperation) error {
		actor := contactAccount(op.actor, op.now, op.zone)
		var gs []models.ContactGrant
		q := tx.Order("id DESC").Limit(100)
		before, _ := strconv.ParseUint(c.Query("before"), 10, 64)
		if before > 0 {
			q = q.Where("id < ?", before)
		}
		if !policy.IsAdmin(actor.Person) {
			q = q.Where("leader_id = ? OR subject_user_id = ? OR (ended_at IS NULL AND subject_user_id IN (SELECT teen_user_id FROM guardian_links WHERE guardian_user_id = ? AND status = ?))", op.actor.ID, op.actor.ID, op.actor.ID, models.GuardianConfirmed)
		}
		if err := q.Find(&gs).Error; err != nil {
			return err
		}
		for _, g := range gs {
			var r models.ContactRequest
			if err := tx.First(&r, g.RequestID).Error; err != nil {
				return err
			}
			guardian, err := contactConfirmedGuardian(tx, op.actor.ID, r.SubjectUserID)
			if err != nil {
				return err
			}
			f, _, err := a.loadContactFacts(tx, contactScope(r), op.now)
			if err != nil {
				return err
			}
			teen := f.Subject.Person != nil && f.Subject.Person.Teen && f.Subject.AgeYears != nil && *f.Subject.AgeYears >= 13 && *f.Subject.AgeYears < 18
			if !policy.CanViewContactGrant(actor, guardian, teen, contactGrantPolicy(g, r)) {
				continue
			}
			j, err := a.presentContactGrant(tx, op, g, r)
			if err != nil {
				return err
			}
			out = append(out, j)
		}
		return nil
	})
	if !c.Writer.Written() {
		c.JSON(http.StatusOK, gin.H{"grants": out})
	}
}
func (a *App) apiRevokeContactGrant(c *gin.Context) { a.useContactGrant(c, false) }
func (a *App) apiRevealContactGrant(c *gin.Context) { a.useContactGrant(c, true) }
func (a *App) useContactGrant(c *gin.Context, reveal bool) {
	var body struct {
		SubjectUserID uint   `json:"subjectUserId"`
		ContextType   string `json:"contextType"`
		ContextID     uint   `json:"contextId"`
		Fields        string `json:"fields"`
	}
	valid := true
	if reveal {
		valid = c.ShouldBindJSON(&body) == nil
	}
	out := contactRevealJSON{}
	a.contactOperation(c, func(tx *gorm.DB, op *contactOperation) error {
		var discovered models.ContactGrant
		err := tx.First(&discovered, contactID(c)).Error
		operation := "revoke"
		if reveal {
			operation = "reveal"
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return refuseContact(tx, op, models.ContactRequest{RequesterID: op.actor.ID, SubjectUserID: body.SubjectUserID, ContextType: body.ContextType, ContextID: body.ContextID, Fields: body.Fields}, operation, c.ClientIP())
		}
		if err != nil {
			return err
		}
		ids, err := a.reconcileContacts(tx, op.now, contactChange{UserID: discovered.LeaderID})
		if err != nil {
			return err
		}
		op.mailed = append(op.mailed, ids...)
		var r models.ContactRequest
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&r, discovered.RequestID).Error; err != nil {
			return err
		}
		var g models.ContactGrant
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&g, discovered.ID).Error; err != nil {
			return err
		}
		actor := contactAccount(op.actor, op.now, op.zone)
		p := contactGrantPolicy(g, r)
		f, subject, err := a.loadContactFacts(tx, contactScope(r), op.now)
		if err != nil {
			return err
		}
		if reveal {
			scope := policy.ContactScope{RequesterID: op.actor.ID, SubjectUserID: body.SubjectUserID, ContextType: body.ContextType, ContextID: body.ContextID, Fields: body.Fields}
			if !valid || !policy.CanRevealContactGrant(actor, scope, p, f) {
				return refuseContact(tx, op, r, "reveal", c.ClientIP())
			}
			if err = contactAudit(tx, &op.actor.ID, audit.ContactReveal, r, body.Fields, c.ClientIP()); err != nil {
				return err
			}
			if body.Fields == policy.ContactEmail || body.Fields == policy.ContactBoth {
				out.Email = &subject.Email
			}
			if body.Fields == policy.ContactPhone || body.Fields == policy.ContactBoth {
				out.Phone = &subject.Phone
			}
			return nil
		}
		guardian, err := contactConfirmedGuardian(tx, op.actor.ID, r.SubjectUserID)
		if err != nil {
			return err
		}
		reason := policy.ContactGrantRevocationReason(actor, guardian, p)
		if reason == "" {
			return refuseContact(tx, op, r, "revoke", c.ClientIP())
		}
		ids, err = endContactGrant(tx, &g, r, reason, &op.actor.ID, op.now, c.ClientIP())
		op.mailed = append(op.mailed, ids...)
		return err
	})
	// The transaction (including contact.reveal audit) has COMMITTED here. No
	// response writes or contact values may escape from inside its callback.
	if !c.Writer.Written() {
		if reveal {
			c.JSON(http.StatusOK, out)
		} else {
			c.JSON(http.StatusOK, gin.H{})
		}
	}
}

// auditContactRejection is the narrow pre-domain hook used by CSRF/session
// rejection sites. Only these explicit contact writes are covered. It never
// reads the request body, query, or claimed URL IDs; a trusted session, if any,
// supplies the actor. Abort sites call it before writing their error response.
func (a *App) auditContactRejection(c *gin.Context, reason string) error {
	if c.Request.Method != http.MethodPost {
		return nil
	}
	switch c.FullPath() {
	case "/api/v1/contact-requests", "/api/v1/contact-requests/:id/decision",
		"/api/v1/contact-requests/:id/withdraw", "/api/v1/contact-grants/:id/revoke",
		"/api/v1/contact-grants/:id/reveal":
	default:
		return nil
	}
	c.Header("Cache-Control", "no-store")
	var actor *uint
	if user, ok := a.authenticatedSessionUser(c); ok {
		actor = &user.ID
	}
	return a.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockContacts(tx); err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: actor, Action: audit.ContactRefused, TargetType: "contact_endpoint", IP: c.ClientIP(), Meta: map[string]any{"reason": reason, "method": c.Request.Method, "route": c.FullPath()}})
	})
}
