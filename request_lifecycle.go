package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The steps contact requests and add requests share. Each runs inside a
// contactOperation, under the contact gate; the handlers keep what differs
// (what makes a request eligible, what a decision leads to).

// lockRequest finds the request with this id among the kind's purposes,
// reconciles the requester's requests, then locks the row. found is false when
// there is no such request; the refusal is then already audited and err is the
// audit's error.
func (a *App) lockRequest(tx *gorm.DB, op *contactOperation, k *requestKind, id uint, operation, ip string) (r models.ContactRequest, found bool, err error) {
	err = tx.Where("purpose IN ?", k.purposes).First(&r, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return r, false, k.refuse(tx, op, models.ContactRequest{ID: id}, operation, ip)
	}
	if err != nil {
		return r, false, err
	}
	if err = a.reconcileContacts(tx, op.now, contactChange{UserID: r.RequesterID}); err != nil {
		return r, false, err
	}
	if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&r, r.ID).Error; err != nil {
		return r, false, err
	}
	return r, true, nil
}

// openRequest stores a request that passed its eligibility check and tells the
// people who must act on it: the subject, and the admins when the subject is a
// teen. The caller has built r's scope and reason and checked the open limit.
func openRequest(tx *gorm.DB, op *contactOperation, r *models.ContactRequest, needsAdmin bool, ip string) error {
	r.NeedsAdmin = needsAdmin
	r.SubjectDecision, r.AdminDecision = policy.ContactPending, policy.ContactNotNeeded
	if needsAdmin {
		r.AdminDecision = policy.ContactPending
	}
	r.Status, r.CreatedAt, r.ExpiresAt = policy.ContactPending, op.now, op.now.Add(policy.ContactRequestLifetime)
	if err := tx.Create(r).Error; err != nil {
		return err
	}
	if err := contactAudit(tx, &op.actor.ID, audit.ContactRequested, *r, "", ip); err != nil {
		return err
	}
	if err := contactNotice(tx, *r, roleSubjectReview, []uint{r.SubjectUserID}, "subject-review"); err != nil {
		return err
	}
	if !needsAdmin {
		return nil
	}
	admins, err := activeAdminIDs(tx)
	if err != nil {
		return err
	}
	return contactNotice(tx, *r, roleAdminReview, admins, "admin-review")
}

// decisionReason is the audit reason a party's decision is recorded under.
// contactHistoryQuery and presentContactHistory read exactly these back.
func decisionReason(party, decision string) string { return party + ":" + decision }

// decisionReasons are the four reasons a decision can be recorded under.
var decisionReasons = []string{
	decisionReason(policy.ContactSubject, policy.ContactApproved), decisionReason(policy.ContactSubject, policy.ContactDenied),
	decisionReason(policy.ContactAdmin, policy.ContactApproved), decisionReason(policy.ContactAdmin, policy.ContactDenied),
}

// recordDecision stores one party's decision and the status it leads to on a
// locked request, audits it, and closes the review task the decision answers
// (and every task of the request once it is no longer pending).
func recordDecision(tx *gorm.DB, op *contactOperation, k *requestKind, r *models.ContactRequest, party, decision, status, ip string) error {
	setRequestDecision(r, op.actor.ID, op.now, party, decision)
	r.Status = status
	if status != policy.ContactPending {
		r.CompletedAt = &op.now
	}
	if err := tx.Save(r).Error; err != nil {
		return err
	}
	if err := contactAudit(tx, &op.actor.ID, audit.ContactDecision, *r, decisionReason(party, decision), ip); err != nil {
		return err
	}
	if _, err := notify.Resolve(tx, k.notice(reviewRole(party)), r.ID, &op.actor.ID, decision); err != nil {
		return err
	}
	if r.Status != policy.ContactPending {
		return resolveContactTasks(tx, *r, &op.actor.ID, r.Status)
	}
	return nil
}

// withdrawRequest ends a locked request at its requester's wish and tells its
// subject. The caller has checked the policy.
func withdrawRequest(tx *gorm.DB, op *contactOperation, r *models.ContactRequest, ip string) error {
	r.Status, r.CompletedAt, r.WithdrawnByID = policy.ContactWithdrawn, &op.now, &op.actor.ID
	if err := tx.Save(r).Error; err != nil {
		return err
	}
	if err := contactAudit(tx, &op.actor.ID, audit.ContactWithdrawn, *r, "", ip); err != nil {
		return err
	}
	if err := resolveContactTasks(tx, *r, &op.actor.ID, r.Status); err != nil {
		return err
	}
	return contactNotice(tx, *r, roleUpdated, []uint{r.SubjectUserID}, "withdrawn")
}

// readRequests answers the list (and the single-request read) of one kind: the
// newest 100 the viewer is a party to, or all for an admin, filtered by the
// kind's own view rule and presented by present.
func readRequests[T any](a *App, c *gin.Context, k *requestKind, single bool, present func(*gorm.DB, *contactOperation, []models.ContactRequest) ([]T, error)) {
	out := []T{}
	a.contactRead(c, k, func(tx *gorm.DB, op *contactOperation) error {
		actor := contactAccount(op.actor, op.now, op.zone)
		q := tx.Where("purpose IN ?", k.purposes).Order("id DESC").Limit(100)
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
			if k.canView(actor, r) {
				visible = append(visible, r)
			}
		}
		var err error
		out, err = present(tx, op, visible)
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

// decisionReasonsSQL is decisionReasons as a SQL list. The values are the four
// constants above, never input.
var decisionReasonsSQL = "'" + strings.Join(decisionReasons, "','") + "'"

// parseDecisionReason reads a recorded decision reason back; anything but the
// four reasons decisionReason makes is not a decision.
func parseDecisionReason(reason string) (party, decision string, ok bool) {
	for _, d := range decisionReasons {
		if reason == d {
			party, decision, _ = strings.Cut(reason, ":")
			return party, decision, true
		}
	}
	return "", "", false
}
