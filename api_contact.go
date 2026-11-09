package main

import (
	"errors"
	"fold/internal/audit"
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
	refused           bool
	refusal           *apiError // nil answers errContactUnavailable
	completionProblem string
}

// Rejected domain operations COMMIT their refusal audit, rather than returning
// an error from the transaction and accidentally rolling that audit back.
func (a *App) contactOperation(c *gin.Context, k *requestKind, fn func(*gorm.DB, *contactOperation) error) {
	a.runContactOperation(c, k, lockContacts, fn)
}

// contactRead is contactOperation for GET handlers that only read. It takes the
// shared gate, so readers do not queue behind each other.
func (a *App) contactRead(c *gin.Context, k *requestKind, fn func(*gorm.DB, *contactOperation) error) {
	a.runContactOperation(c, k, lockContactsShared, fn)
}

func (a *App) runContactOperation(c *gin.Context, k *requestKind, lock func(*gorm.DB) error, fn func(*gorm.DB, *contactOperation) error) {
	op := contactOperation{}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := lock(tx); err != nil {
			return err
		}
		op.now = time.Now().UTC().Truncate(time.Microsecond)
		var err error
		op.zone, err = churchZoneIn(tx)
		if err != nil {
			return err
		}
		op.actor, err = contactUser(tx, apiUser(c).ID)
		if err != nil {
			return err
		}
		if !sessionCurrent(op.actor, apiUser(c)) {
			op.actor = &models.User{ID: apiUser(c).ID}
			return k.refuse(tx, &op, models.ContactRequest{}, "stale_session", c.ClientIP())
		}
		return fn(tx, &op)
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not complete contact operation")
		return
	}
	if op.refused {
		if op.refusal == nil {
			op.refusal = errContactUnavailable
		}
		fail(c, op.refusal)
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
	a.contactOperation(c, contactKind, func(tx *gorm.DB, op *contactOperation) error {
		r := models.ContactRequest{Purpose: policy.RequestContact, RequesterID: op.actor.ID, SubjectUserID: body.SubjectUserID, ContextType: body.ContextType, ContextID: body.ContextID, Fields: body.Fields, Reason: strings.TrimSpace(body.Reason)}
		if !valid {
			if utf8.RuneCountInString(body.Reason) > policy.ContactReasonLimit {
				op.refusal = errContactReasonLong
			}
			return refuseContact(tx, op, r, "invalid_request", c.ClientIP())
		}
		err := a.reconcileContacts(tx, op.now, contactChange{UserID: op.actor.ID})
		if err != nil {
			return err
		}
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
				op.refusal = errRequestOpenLimit
			}
			return refuseContact(tx, op, r, "create", c.ClientIP())
		}
		if err = openRequest(tx, op, &r, f.Subject.Person.Teen, c.ClientIP()); err != nil {
			return err
		}
		out, err = a.presentContactRequest(tx, op, r)
		return err
	})
	if !c.Writer.Written() {
		c.JSON(http.StatusCreated, gin.H{"request": out})
	}
}

func (a *App) apiListContactRequests(c *gin.Context) {
	readRequests(a, c, contactKind, false, a.presentContactRequests)
}
func (a *App) apiGetContactRequest(c *gin.Context) {
	readRequests(a, c, contactKind, true, a.presentContactRequests)
}
func (a *App) apiDecideContactRequest(c *gin.Context) {
	var body struct {
		Party    string `json:"party"`
		Decision string `json:"decision"`
	}
	valid := c.ShouldBindJSON(&body) == nil
	var out contactRequestJSON
	a.contactOperation(c, contactKind, func(tx *gorm.DB, op *contactOperation) error {
		r, found, err := a.lockRequest(tx, op, contactKind, contactID(c), "decision", c.ClientIP())
		if err != nil || !found {
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
		if err = recordDecision(tx, op, contactKind, &r, body.Party, body.Decision, status, c.ClientIP()); err != nil {
			return err
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
		err = contactNotice(tx, r, roleUpdated, recipients, "decision:"+body.Party)
		if err != nil {
			return err
		}
		out, err = a.presentContactRequest(tx, op, r)
		return err
	})
	if !c.Writer.Written() {
		c.JSON(http.StatusOK, gin.H{"request": out})
	}
}
func (a *App) apiWithdrawContactRequest(c *gin.Context) {
	a.contactOperation(c, contactKind, func(tx *gorm.DB, op *contactOperation) error {
		r, found, err := a.lockRequest(tx, op, contactKind, contactID(c), "withdraw", c.ClientIP())
		if err != nil || !found {
			return err
		}
		if !policy.CanWithdrawContactRequest(contactAccount(op.actor, op.now, op.zone), contactRequestPolicy(r), op.now) {
			return refuseContact(tx, op, r, "withdraw", c.ClientIP())
		}
		return withdrawRequest(tx, op, &r, c.ClientIP())
	})
	if !c.Writer.Written() {
		c.JSON(http.StatusOK, gin.H{})
	}
}

func (a *App) apiListContactGrants(c *gin.Context) {
	out := []contactGrantJSON{}
	a.contactRead(c, contactKind, func(tx *gorm.DB, op *contactOperation) error {
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
	a.contactOperation(c, contactKind, func(tx *gorm.DB, op *contactOperation) error {
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
		err = a.reconcileContacts(tx, op.now, contactChange{UserID: discovered.LeaderID})
		if err != nil {
			return err
		}
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
		err = endContactGrant(tx, &g, r, reason, &op.actor.ID, op.now, c.ClientIP())
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

// refusalKind is the audit row a refused write to one family of routes leaves.
type refusalKind struct{ action, target string }

var (
	refusedContact    = refusalKind{audit.ContactRefused, "contact_endpoint"}
	refusedAdd        = refusalKind{audit.AddRefused, "add_endpoint"}
	refusedOccurrence = refusalKind{audit.OccurrenceRefused, "occurrence_endpoint"}
)

// auditRefusals returns a registrar for routes on group whose CSRF and
// authentication refusals are written to the audit log as kind. The route table
// is the only list of those routes: the registrar records "METHOD /full/path",
// and auditRefusal looks the matched route up. (The CSRF check is a global
// middleware and runs before any route's own, so a route-level middleware could
// not see its refusal; the registration records it instead.)
func (a *App) auditRefusals(group *gin.RouterGroup, kind refusalKind) func(method, path string, handlers ...gin.HandlerFunc) {
	return func(method, path string, handlers ...gin.HandlerFunc) {
		a.refusalRoutes.Store(method+" "+group.BasePath()+path, kind)
		group.Handle(method, path, handlers...)
	}
}

// auditRefusal is the narrow pre-domain hook used by CSRF/session rejection
// sites. Only the routes registered through auditRefusals are covered. It never
// reads the request body, query, or claimed URL IDs; a trusted session, if any,
// supplies the actor. Abort sites call it before writing their error response.
func (a *App) auditRefusal(c *gin.Context, reason string) error {
	v, ok := a.refusalRoutes.Load(c.Request.Method + " " + c.FullPath())
	if !ok {
		return nil
	}
	kind := v.(refusalKind)
	c.Header("Cache-Control", "no-store")
	var actor *uint
	if user, ok := a.authenticatedSessionUser(c); ok {
		actor = &user.ID
	}
	return a.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockContacts(tx); err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: actor, Action: kind.action, TargetType: kind.target, IP: c.ClientIP(), Meta: map[string]any{"reason": reason, "method": c.Request.Method, "route": c.FullPath()}})
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

func setRequestDecision(r *models.ContactRequest, actor uint, now time.Time, party, decision string) {
	if party == policy.ContactSubject {
		r.SubjectDecision, r.SubjectDecidedBy, r.SubjectDecidedAt = decision, &actor, &now
	} else {
		r.AdminDecision, r.AdminDecidedBy, r.AdminDecidedAt = decision, &actor, &now
	}
}

// refuseAdd refuses an operation on an add request. A message the caller
// already chose (a long reason, the open limit) is kept.
func refuseAdd(tx *gorm.DB, op *contactOperation, r models.ContactRequest, operation, ip string) error {
	if r.Purpose != policy.RequestOccurrenceAdd {
		r.Purpose = policy.RequestGroupAdd
	}
	if op.refusal == nil {
		op.refusal = errAddRequestUnavailable
	}
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
	a.contactOperation(c, addKind, func(tx *gorm.DB, op *contactOperation) error {
		r := models.ContactRequest{Purpose: policy.RequestGroupAdd, RequesterID: op.actor.ID, ContextType: body.ContextType, ContextID: body.ContextID, Reason: strings.TrimSpace(body.Reason), AddOverLimit: body.OverLimit}
		if body.ContextType == policy.ContactEvent {
			r.Purpose = policy.RequestOccurrenceAdd
		}
		if !valid || (body.ContextType != policy.ContactGroup && body.ContextType != policy.ContactEvent) {
			if utf8.RuneCountInString(body.Reason) > policy.ContactReasonLimit {
				op.refusal = errAddReasonLong
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
		err = a.reconcileContacts(tx, op.now, contactChange{UserID: r.RequesterID})
		if err != nil {
			return err
		}
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
				op.refusal = errRequestOpenLimit
				return refuseAdd(tx, op, r, "open_limit", c.ClientIP())
			}
			return refuseAdd(tx, op, r, "create", c.ClientIP())
		}
		if err = openRequest(tx, op, &r, f.Subject.Person.Teen, c.ClientIP()); err != nil {
			return err
		}
		out, err = a.presentAddRequest(tx, op, r)
		return err
	})
	if !c.Writer.Written() {
		c.JSON(http.StatusCreated, gin.H{"request": out})
	}
}
func (a *App) apiListAddRequests(c *gin.Context) {
	readRequests(a, c, addKind, false, a.presentAddRequests)
}
func (a *App) apiGetAddRequest(c *gin.Context) {
	readRequests(a, c, addKind, true, a.presentAddRequests)
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
	err = a.changeRoleIn(tx, requester, ip, &g, r.SubjectUserID, models.MembershipMember, r.AddOverLimit, &memberAddOptions{KeepExistingRole: true, approved: &verifiedGroupAdd{r.ID, op.now}}, func(me, _ *models.User, rel, _ policy.Rel, _ int) bool {
		return policy.CanManageGroup(asPerson(me), rel)
	})
	if errors.Is(err, errGroupFull) {
		return contactAudit(tx, &op.actor.ID, audit.ContactRefused, *r, "group_full", ip)
	}
	if errors.Is(err, errGroupNotApprovedForAdds) {
		return contactAudit(tx, &op.actor.ID, audit.ContactRefused, *r, "group_not_approved", ip)
	}
	if err != nil {
		return err
	}
	if g.DeletionRequested {
		members, err := memberCount(tx, g.ID)
		if err != nil {
			return err
		}
		if members > 1 {
			if err := clearDeletionRequestIn(tx, g.ID, nil, "withdrawn"); err != nil {
				return err
			}
		}
	}
	var m models.Membership
	if err = tx.Where("group_id = ? AND user_id = ? AND status = ?", r.ContextID, r.SubjectUserID, models.MembershipActive).First(&m).Error; err != nil {
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
	err = contactNotice(tx, *r, roleUpdated, recipients, "completed")
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
	a.contactOperation(c, addKind, func(tx *gorm.DB, op *contactOperation) error {
		// Gate serializes discovery/reconciliation. Reconcile locks request rows
		// in ID order before the selected request is updated.
		r, found, err := a.lockRequest(tx, op, addKind, contactID(c), operation, c.ClientIP())
		if err != nil || !found {
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
			if err = recordDecision(tx, op, addKind, &r, body.Party, body.Decision, status, c.ClientIP()); err != nil {
				return err
			}
			err = contactNotice(tx, r, roleUpdated, []uint{r.RequesterID, r.SubjectUserID}, "decision:"+body.Party)
			if err != nil {
				return err
			}
			if err = a.completeAddAsk(tx, op, &r, c.ClientIP()); err != nil {
				return err
			}
		case "withdraw":
			if !policy.CanWithdrawAddRequest(actor, p, op.now) {
				return refuseAdd(tx, op, r, operation, c.ClientIP())
			}
			if err = withdrawRequest(tx, op, &r, c.ClientIP()); err != nil {
				return err
			}
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
