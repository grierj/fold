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
	actor             *models.User
	now               time.Time
	zone              *time.Location
	mailed            []uint
	refused           bool
	refusalMessage    string
	refusalStatus     int
	completionProblem string
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
			if strings.HasPrefix(c.FullPath(), "/api/v1/add-requests") {
				return refuseAdd(tx, &op, models.ContactRequest{}, "stale_session", c.ClientIP())
			}
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
	if r.Purpose != policy.RequestContact {
		return contactRequestJSON{}, errors.New("not a contact request")
	}
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
		r := models.ContactRequest{Purpose: policy.RequestContact, RequesterID: op.actor.ID, SubjectUserID: body.SubjectUserID, ContextType: body.ContextType, ContextID: body.ContextID, Fields: body.Fields, Reason: strings.TrimSpace(body.Reason)}
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
				op.refusalMessage = "you already have 10 open requests; withdraw one or wait for a decision"
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
		q := tx.Where("purpose = ?", policy.RequestContact).Order("id DESC").Limit(100)
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
		var err error
		out, err = a.presentContactRequests(tx, op, rs)
		return err
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
		err := tx.Where("purpose = ?", policy.RequestContact).First(&r, contactID(c)).Error
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
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&r, r.ID).Error; err != nil {
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
		setRequestDecision(&r, op.actor.ID, op.now, body.Party, body.Decision)
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
		err := tx.Where("purpose = ?", policy.RequestContact).First(&r, contactID(c)).Error
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
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&r, r.ID).Error; err != nil {
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
		q := tx.Where("request_id IN (SELECT id FROM contact_requests WHERE purpose = ?)", policy.RequestContact).Order("id DESC").Limit(100)
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
		var err error
		out, err = a.presentContactGrants(tx, op, gs)
		return err
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
		err := tx.Where("request_id IN (SELECT id FROM contact_requests WHERE purpose = ?)", policy.RequestContact).First(&discovered, contactID(c)).Error
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
		if err = tx.Where("purpose = ?", policy.RequestContact).Clauses(clause.Locking{Strength: "UPDATE"}).First(&r, discovered.RequestID).Error; err != nil {
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
// rejection sites. Only these explicit contact/add writes are covered. It never
// reads the request body, query, or claimed URL IDs; a trusted session, if any,
// supplies the actor. Abort sites call it before writing their error response.
func (a *App) auditContactRejection(c *gin.Context, reason string) error {
	if c.Request.Method != http.MethodPost && !(c.Request.Method == http.MethodPut && c.FullPath() == "/api/v1/events/:id/dates/:date/registration") {
		return nil
	}
	switch c.FullPath() {
	case "/api/v1/events/:id/registrations/add", "/api/v1/events/:id/dates/:date/decline", "/api/v1/events/:id/dates/:date/block", "/api/v1/events/:id/dates/:date/registration", "/api/v1/add-requests", "/api/v1/add-requests/:id/decision", "/api/v1/add-requests/:id/withdraw", "/api/v1/add-requests/:id/complete", "/api/v1/contact-requests", "/api/v1/contact-requests/:id/decision",
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
		action, target := audit.ContactRefused, "contact_endpoint"
		if strings.HasPrefix(c.FullPath(), "/api/v1/add-requests") {
			action, target = audit.AddRefused, "add_endpoint"
		}
		if strings.HasPrefix(c.FullPath(), "/api/v1/events/:id/dates/") || c.FullPath() == "/api/v1/events/:id/registrations/add" {
			action, target = audit.OccurrenceRefused, "occurrence_endpoint"
		}
		return audit.Write(tx, audit.Entry{Actor: actor, Action: action, TargetType: target, IP: c.ClientIP(), Meta: map[string]any{"reason": reason, "method": c.Request.Method, "route": c.FullPath()}})
	})
}

// addRequestJSON is a safe request preview, not a group/member/contact DTO.
// Context and names are omitted unless the viewer can already see the group.
type addRequestJSON struct {
	ID                uint       `json:"id"`
	EventTitle        string     `json:"eventTitle,omitempty"`
	Date              string     `json:"date,omitempty"`
	StartsAt          *time.Time `json:"startsAt,omitempty"`
	EndsAt            *time.Time `json:"endsAt,omitempty"`
	TZID              string     `json:"tzid,omitempty"`
	CompletionProblem string     `json:"completionProblem,omitempty"`
	Purpose           string     `json:"purpose"`
	RequesterName     string     `json:"requesterName,omitempty"`
	SubjectName       string     `json:"subjectName,omitempty"`
	ContextID         *uint      `json:"contextId,omitempty"`
	GroupName         string     `json:"groupName,omitempty"`
	Reason            string     `json:"reason"`
	OverLimit         bool       `json:"overLimit"`
	NeedsAdmin        bool       `json:"needsAdmin"`
	SubjectDecision   string     `json:"subjectDecision"`
	AdminDecision     string     `json:"adminDecision"`
	Status            string     `json:"status"`
	CreatedAt         time.Time  `json:"createdAt"`
	ExpiresAt         time.Time  `json:"expiresAt"`
	CompletedAt       *time.Time `json:"completedAt,omitempty"`
	EndReason         string     `json:"endReason"`
	CanSubjectDecide  bool       `json:"canSubjectDecide"`
	CanAdminDecide    bool       `json:"canAdminDecide"`
	CanWithdraw       bool       `json:"canWithdraw"`
	CanComplete       bool       `json:"canComplete"`
}

func addAuditAction(action string) string {
	switch action {
	case audit.ContactRequested:
		return audit.AddRequested
	case audit.ContactDecision:
		return audit.AddDecision
	case audit.ContactWithdrawn:
		return audit.AddWithdrawn
	case audit.ContactExpired:
		return audit.AddExpired
	case audit.ContactGranted:
		return audit.AddCompleted
	case audit.ContactEnded:
		return audit.AddEnded
	default:
		return audit.AddRefused
	}
}
func addNoticeKind(kind string) string {
	switch kind {
	case notify.ContactSubjectReview:
		return notify.AddSubjectReview
	case notify.ContactAdminReview:
		return notify.AddAdminReview
	default:
		return notify.AddUpdated
	}
}
func setRequestDecision(r *models.ContactRequest, actor uint, now time.Time, party, decision string) {
	if party == policy.ContactSubject {
		r.SubjectDecision, r.SubjectDecidedBy, r.SubjectDecidedAt = decision, &actor, &now
	} else {
		r.AdminDecision, r.AdminDecidedBy, r.AdminDecidedAt = decision, &actor, &now
	}
}

func (a *App) presentAddRequest(tx *gorm.DB, op *contactOperation, r models.ContactRequest) (addRequestJSON, error) {
	f, err := a.loadAddFacts(tx, r, op.now)
	if err != nil {
		return addRequestJSON{}, err
	}
	p, actor := addRequestPolicy(r), contactAccount(op.actor, op.now, op.zone)
	out := addRequestJSON{ID: r.ID, Purpose: r.Purpose, Reason: r.Reason, OverLimit: r.AddOverLimit, NeedsAdmin: r.NeedsAdmin, SubjectDecision: r.SubjectDecision, AdminDecision: r.AdminDecision, Status: r.Status, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, CompletedAt: r.CompletedAt, EndReason: r.EndReason,
		CanSubjectDecide: policy.AddRequestStatusAfterDecision(actor, policy.ContactSubject, policy.ContactApproved, p, f) != "", CanAdminDecide: policy.AddRequestStatusAfterDecision(actor, policy.ContactAdmin, policy.ContactApproved, p, f) != "", CanWithdraw: policy.CanWithdrawAddRequest(actor, p, op.now), CanComplete: policy.CanViewAddRequest(actor, p) && policy.CanFinalizeAddRequest(p, f)}
	if r.Purpose == policy.RequestOccurrenceAdd {
		out.Reason = ""
		out.CompletionProblem = op.completionProblem
		var ev models.Event
		var occ models.EventOccurrence
		if err := tx.Where("id = ?", r.ContextID).Limit(1).Find(&ev).Error; err != nil {
			return out, err
		}
		if err := tx.Where("id = ? AND event_id = ?", r.OccurrenceID, r.ContextID).Limit(1).Find(&occ).Error; err != nil {
			return out, err
		}
		if ev.ID != 0 && occ.ID != 0 {
			out.EventTitle = ev.Title
			if occ.TitleOverride != nil {
				out.EventTitle = *occ.TitleOverride
			}
			out.Date, out.StartsAt, out.EndsAt, out.TZID = occ.OriginalDate, &occ.StartsAt, &occ.EndsAt, ev.TZID
		}
		return out, nil
	}
	out.ContextID, err = a.contactContextVisible(tx, op.actor, r)
	if err != nil {
		return out, err
	}
	if out.ContextID != nil {
		var g models.Group
		if err = tx.First(&g, *out.ContextID).Error; err != nil {
			return out, err
		}
		shown := a.groupShownTo(op.actor, &g, relIn(tx, op.actor.ID, g.ID))
		if shown != nil {
			out.GroupName = shown.Name
		} // group-ok: groupShownTo applies current viewer visibility
		out.RequesterName, out.SubjectName = nameIn(tx, r.RequesterID), nameIn(tx, r.SubjectUserID)
	}
	return out, nil
}
func refuseAdd(tx *gorm.DB, op *contactOperation, r models.ContactRequest, operation, ip string) error {
	if r.Purpose != policy.RequestOccurrenceAdd {
		r.Purpose = policy.RequestGroupAdd
	}
	op.refusalMessage, op.refusalStatus = "this ask to add is unavailable; refresh the request", http.StatusForbidden
	return refuseContact(tx, op, r, operation, ip)
}
func (a *App) apiCreateAddRequest(c *gin.Context) {
	var body struct {
		SelectionID       string  `json:"selectionId"`
		ContextType       string  `json:"contextType"`
		ContextID         uint    `json:"contextId"`
		Reason            string  `json:"reason"`
		OverLimit         bool    `json:"overLimit"`
		OccurrenceID      uint    `json:"occurrenceId"`
		OverCapacity      bool    `json:"overCapacity"`
		AttendanceMode    *string `json:"attendanceMode"`
		GuestCount        *int    `json:"guestCount"`
		ChildcareCount    *int    `json:"childcareCount"`
		ChildcareAgesNote *string `json:"childcareAgesNote"`
	}
	valid := c.ShouldBindJSON(&body) == nil && utf8.RuneCountInString(body.Reason) <= policy.ContactReasonLimit
	var out addRequestJSON
	a.contactOperation(c, func(tx *gorm.DB, op *contactOperation) error {
		r := models.ContactRequest{Purpose: policy.RequestGroupAdd, RequesterID: op.actor.ID, ContextType: body.ContextType, ContextID: body.ContextID, Reason: strings.TrimSpace(body.Reason), AddOverLimit: body.OverLimit}
		if body.ContextType == policy.ContactEvent {
			r.Purpose = policy.RequestOccurrenceAdd
		}
		if !valid || (body.ContextType != policy.ContactGroup && body.ContextType != policy.ContactEvent) {
			if utf8.RuneCountInString(body.Reason) > policy.ContactReasonLimit {
				op.refused, op.refusalMessage, op.refusalStatus = true, "add reason must be at most 1000 characters", http.StatusBadRequest
				return contactAudit(tx, &op.actor.ID, audit.ContactRefused, r, "invalid_request", c.ClientIP())
			}
			return refuseAdd(tx, op, r, "invalid_request", c.ClientIP())
		}
		id, err := a.parsePeopleSelection(body.SelectionID, op.actor.ID, op.now)
		if err != nil {
			return refuseAdd(tx, op, r, "selection", c.ClientIP())
		}
		r.SubjectUserID = id
		if r.Purpose == policy.RequestOccurrenceAdd {
			if body.OverLimit || body.OccurrenceID == 0 {
				return refuseAdd(tx, op, r, "options", c.ClientIP())
			}
			r.OccurrenceID, r.AddOverCapacity = &body.OccurrenceID, body.OverCapacity
			var old models.Registration
			if err := tx.Where("user_id = ? AND occurrence_id = ?", id, body.OccurrenceID).Limit(1).Find(&old).Error; err != nil {
				return err
			}
			r.AddAttendanceMode, r.AddGuestCount = attendanceModeOf(old.AttendanceMode), old.GuestCount
			r.AddChildcareCount, r.AddChildcareAgesNote = old.ChildcareCount, old.ChildcareAgesNote
			if body.AttendanceMode != nil {
				r.AddAttendanceMode = *body.AttendanceMode
			}
			if body.GuestCount != nil {
				r.AddGuestCount = *body.GuestCount
			}
			if body.ChildcareCount != nil {
				r.AddChildcareCount = *body.ChildcareCount
			}
			if body.ChildcareAgesNote != nil {
				r.AddChildcareAgesNote = strings.TrimSpace(*body.ChildcareAgesNote)
			}
			if !policy.ValidAddOptions(r.Purpose, addRequestPolicy(r).Options) {
				return refuseAdd(tx, op, r, "options", c.ClientIP())
			}
			allowed, err := managerDateAddAllowedIn(tx, op.actor.ID)
			if err != nil {
				return err
			}
			if !allowed {
				return refuseManagerDateLimit(tx, op, r, c.ClientIP())
			}
		} else if body.OccurrenceID != 0 || body.OverCapacity || body.AttendanceMode != nil || body.GuestCount != nil || body.ChildcareCount != nil || body.ChildcareAgesNote != nil {
			return refuseAdd(tx, op, r, "options", c.ClientIP())
		}
		ids, err := a.reconcileContacts(tx, op.now, contactChange{UserID: r.RequesterID})
		if err != nil {
			return err
		}
		op.mailed = append(op.mailed, ids...)
		f, err := a.loadAddFacts(tx, r, op.now)
		if err != nil {
			return err
		}
		var count int64
		if err = tx.Model(&models.ContactRequest{}).Where("requester_id = ? AND status = ?", r.RequesterID, policy.ContactPending).Count(&count).Error; err != nil {
			return err
		}
		if !policy.CanCreateAddRequest(addRequestPolicy(r).AddScope, f, int(count)) {
			if count >= policy.ContactOpenLimit && policy.CanCreateAddRequest(addRequestPolicy(r).AddScope, f, 0) {
				op.refused = true
				op.refusalMessage = "you already have 10 open requests; withdraw one or wait for a decision"
				op.refusalStatus = http.StatusConflict
				return contactAudit(tx, &op.actor.ID, audit.ContactRefused, r, "open_limit", c.ClientIP())
			}
			return refuseAdd(tx, op, r, "create", c.ClientIP())
		}
		r.NeedsAdmin = f.Subject.Person.Teen
		r.SubjectDecision = policy.ContactPending
		r.AdminDecision = policy.ContactNotNeeded
		if r.NeedsAdmin {
			r.AdminDecision = policy.ContactPending
		}
		r.Status, r.CreatedAt, r.ExpiresAt = policy.ContactPending, op.now, op.now.Add(policy.ContactRequestLifetime)
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
		out, err = a.presentAddRequest(tx, op, r)
		return err
	})
	if !c.Writer.Written() {
		c.JSON(http.StatusCreated, gin.H{"request": out})
	}
}
func (a *App) apiListAddRequests(c *gin.Context) { a.readAddRequests(c, false) }
func (a *App) apiGetAddRequest(c *gin.Context)   { a.readAddRequests(c, true) }
func (a *App) readAddRequests(c *gin.Context, single bool) {
	out := []addRequestJSON{}
	a.contactOperation(c, func(tx *gorm.DB, op *contactOperation) error {
		actor := contactAccount(op.actor, op.now, op.zone)
		q := tx.Where("purpose IN ?", []string{policy.RequestGroupAdd, policy.RequestOccurrenceAdd}).Order("id DESC").Limit(100)
		if single {
			q = q.Where("id = ?", contactID(c))
		} else if before, _ := strconv.ParseUint(c.Query("before"), 10, 64); before > 0 {
			q = q.Where("id < ?", before)
		}
		if !policy.IsAdmin(actor.Person) {
			q = q.Where("requester_id = ? OR subject_user_id = ?", op.actor.ID, op.actor.ID)
		}
		var rs []models.ContactRequest
		if err := q.Find(&rs).Error; err != nil {
			return err
		}
		visible := make([]models.ContactRequest, 0, len(rs))
		for _, r := range rs {
			if policy.CanViewAddRequest(actor, addRequestPolicy(r)) {
				visible = append(visible, r)
			}
		}
		var err error
		out, err = a.presentAddRequests(tx, op, visible)
		if err != nil {
			return err
		}
		return nil
	})
	if c.Writer.Written() {
		return
	}
	if single {
		if len(out) == 0 {
			jsonError(c, 404, "not found")
			return
		}
		c.JSON(200, gin.H{"request": out[0]})
		return
	}
	c.JSON(200, gin.H{"requests": out})
}

// completeGroupAsk changes membership and records success in the SAME outer
// transaction. Capacity is a domain refusal, not an invalid party decision:
// approved decisions and their audits/tasks remain committed and retryable.
func (a *App) completeGroupAsk(tx *gorm.DB, op *contactOperation, r *models.ContactRequest, ip string) error {
	f, err := a.loadGroupAddFacts(tx, *r, op.now)
	if err != nil {
		return err
	}
	if !policy.CanFinalizeAddRequest(addRequestPolicy(*r), f) {
		return nil
	}
	g := models.Group{ID: r.ContextID}
	requester, err := contactUser(tx, r.RequesterID)
	if err != nil {
		return err
	}
	err = a.changeRoleIn(tx, requester, ip, &g, r.SubjectUserID, models.MembershipMember, "", r.AddOverLimit, &memberAddOptions{KeepExistingRole: true, approved: &verifiedGroupAdd{r.ID, op.now}}, func(me *models.User, rel, _ policy.Rel, _ int) bool { return policy.CanManageGroup(asPerson(me), rel) }, &op.mailed)
	if errors.Is(err, errGroupFull) {
		return contactAudit(tx, &op.actor.ID, audit.ContactRefused, *r, "group_full", ip)
	}
	if err != nil {
		return err
	}
	if g.DeletionRequested && memberCount(tx, g.ID) > 1 {
		if err := clearDeletionRequestIn(tx, g.ID, nil, "withdrawn"); err != nil {
			return err
		}
	}
	var m models.Membership
	if err = tx.Where("group_id = ? AND user_id = ? AND status = ?", r.ContextID, r.SubjectUserID, "active").First(&m).Error; err != nil {
		return err
	}
	r.Status, r.CompletedAt, r.CompletedTargetID = policy.ContactGranted, &op.now, &m.ID
	if err = tx.Save(r).Error; err != nil {
		return err
	}
	if err = contactAudit(tx, &op.actor.ID, audit.ContactGranted, *r, "", ip); err != nil {
		return err
	}
	if err = resolveContactTasks(tx, *r, &op.actor.ID, r.Status); err != nil {
		return err
	}
	recipients := []uint{r.RequesterID, r.SubjectUserID}
	if r.NeedsAdmin {
		guardians, err := contactGuardians(tx, r.SubjectUserID)
		if err != nil {
			return err
		}
		recipients = append(recipients, guardians...)
	}
	ids, err := contactNotice(tx, *r, notify.ContactUpdated, recipients, "completed")
	op.mailed = append(op.mailed, ids...)
	return err
}
func (a *App) apiDecideAddRequest(c *gin.Context)   { a.mutateAddRequest(c, "decision") }
func (a *App) apiWithdrawAddRequest(c *gin.Context) { a.mutateAddRequest(c, "withdraw") }
func (a *App) apiCompleteAddRequest(c *gin.Context) { a.mutateAddRequest(c, "complete") }
func (a *App) mutateAddRequest(c *gin.Context, operation string) {
	var body struct {
		Party    string `json:"party"`
		Decision string `json:"decision"`
	}
	valid := operation != "decision" || c.ShouldBindJSON(&body) == nil
	var out addRequestJSON
	a.contactOperation(c, func(tx *gorm.DB, op *contactOperation) error {
		// Gate serializes discovery/reconciliation. Reconcile locks request rows
		// in ID order before the selected request is updated.
		var r models.ContactRequest
		err := tx.Where("purpose IN ?", []string{policy.RequestGroupAdd, policy.RequestOccurrenceAdd}).First(&r, contactID(c)).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return refuseAdd(tx, op, models.ContactRequest{ID: contactID(c)}, operation, c.ClientIP())
		}
		if err != nil {
			return err
		}
		ids, err := a.reconcileContacts(tx, op.now, contactChange{UserID: r.RequesterID})
		if err != nil {
			return err
		}
		op.mailed = append(op.mailed, ids...)
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&r, r.ID).Error; err != nil {
			return err
		}
		p, actor := addRequestPolicy(r), contactAccount(op.actor, op.now, op.zone)
		f, err := a.loadAddFacts(tx, r, op.now)
		if err != nil {
			return err
		}
		if !policy.CanViewAddRequest(actor, p) {
			return refuseAdd(tx, op, r, operation, c.ClientIP())
		}
		switch operation {
		case "decision":
			status := policy.AddRequestStatusAfterDecision(actor, body.Party, body.Decision, p, f)
			if !valid || status == "" {
				return refuseAdd(tx, op, r, operation, c.ClientIP())
			}
			setRequestDecision(&r, op.actor.ID, op.now, body.Party, body.Decision)
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
			kind := notify.AddSubjectReview
			if body.Party == policy.ContactAdmin {
				kind = notify.AddAdminReview
			}
			if _, err = notify.Resolve(tx, kind, r.ID, &op.actor.ID, body.Decision); err != nil {
				return err
			}
			if r.Status != policy.ContactPending {
				if err = resolveContactTasks(tx, r, &op.actor.ID, r.Status); err != nil {
					return err
				}
			}
			ids, err = contactNotice(tx, r, notify.ContactUpdated, []uint{r.RequesterID, r.SubjectUserID}, "decision:"+body.Party)
			if err != nil {
				return err
			}
			op.mailed = append(op.mailed, ids...)
			if err = a.completeAddAsk(tx, op, &r, c.ClientIP()); err != nil {
				return err
			}
		case "withdraw":
			if !policy.CanWithdrawAddRequest(actor, p, op.now) {
				return refuseAdd(tx, op, r, operation, c.ClientIP())
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
			if err != nil {
				return err
			}
			op.mailed = append(op.mailed, ids...)
		case "complete":
			if !policy.CanFinalizeAddRequest(p, f) {
				return refuseAdd(tx, op, r, operation, c.ClientIP())
			}
			if err = a.completeAddAsk(tx, op, &r, c.ClientIP()); err != nil {
				return err
			}
		}
		out, err = a.presentAddRequest(tx, op, r)
		return err
	})
	if !c.Writer.Written() {
		c.JSON(200, gin.H{"request": out})
	}
}
