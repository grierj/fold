package main

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (a *App) apiPendingGroups(c *gin.Context) {
	var groups []models.Group
	if err := a.DB.Where("status = ?", models.GroupPending).Order("created_at").Find(&groups).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list groups")
		return
	}
	a.respondGroups(c, groups)
}

func (a *App) respondGroups(c *gin.Context, groups []models.Group) {
	out, err := a.groupSummaries(groups)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list groups")
		return
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
	if !bindOptionalJSON(c, &body) {
		return
	}
	// Tags are the admin's to change only when they sent some; otherwise the
	// leader's own edits stay as they are.
	var tags *string
	if body.Tags != nil {
		joined := joinTags(body.Tags)
		if err := tagsField.check(joined); err != nil {
			fail(c, err)
			return
		}
		tags = &joined
	}
	a.decideGroup(c, notify.ActionApproveGroup, body.FromNotification, models.GroupActive, tags, "")
}

const maxRejectMessage = 500

// Notification text never carries contact details (5.12), so free text that
// becomes one is checked for an email address or a phone-shaped number: a run
// of ten or more digits (with the usual separators), or a local number like
// 555-0143. Dates such as 2026-11-05 or a 2026-2027 season are not phones.
var (
	digitRun    = regexp.MustCompile(`\+?\d[\d\s().-]{6,}\d`)
	localNumber = regexp.MustCompile(`\b\d{3}[-.\s]\d{4}\b`)
)

func hasContactDetails(s string) bool {
	if strings.Contains(s, "@") || localNumber.MatchString(s) {
		return true
	}
	for _, run := range digitRun.FindAllString(s, -1) {
		digits := 0
		for _, r := range run {
			if r >= '0' && r <= '9' {
				digits++
			}
		}
		if digits >= 10 {
			return true
		}
	}
	return false
}

func (a *App) apiRejectGroup(c *gin.Context) {
	var body struct {
		FromNotification *uint  `json:"fromNotification"`
		Message          string `json:"message"`
	}
	if !bindOptionalJSON(c, &body) {
		return
	}
	// An optional message to the leaders, carried on their notice (DEC-96).
	message := strings.TrimSpace(body.Message)
	if utf8.RuneCountInString(message) > maxRejectMessage {
		fail(c, errMessageTooLong500)
		return
	}
	if hasContactDetails(message) {
		fail(c, errMessageHasContact)
		return
	}
	a.decideGroup(c, "reject", body.FromNotification, models.GroupArchived, nil, message)
}

func (a *App) decideGroup(c *gin.Context, action string, fromNotification *uint, status string, tags *string, note string) {
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
	err := a.contactMutation(contactChange{GroupID: g.ID}, func(tx *gorm.DB) error {
		set := map[string]any{"status": status}
		if tags != nil {
			set["tags"] = *tags
		}
		if err := decidePendingIn(tx, &models.Group{}, g.ID, models.GroupPending, set); err != nil {
			return err
		}
		// Answer with the row as stored, not the unlocked read from above.
		if err := tx.First(&g, g.ID).Error; err != nil {
			return err
		}
		if _, err := notify.Resolve(tx, notify.GroupPending, g.ID, &me.ID, outcome); err != nil {
			return err
		}
		if err := notifyGroupDecided(tx, &g, me.ID, outcome == "approved", note); err != nil {
			return err
		}
		var meta map[string]any
		if note != "" {
			meta = map[string]any{"message": note}
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: auditAction, TargetType: "group", TargetID: g.ID, Meta: meta, IP: c.ClientIP()})
	})
	if a.answerAlreadyHandled(c, err, notify.GroupPending, g.ID) {
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not "+action+" group")
		return
	}
	if action == notify.ActionApproveGroup {
		summary, err := a.groupSummary(&g)
		if err != nil {
			jsonError(c, http.StatusInternalServerError, "could not "+action+" group")
			return
		}
		c.JSON(http.StatusOK, gin.H{"group": summary})
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

func (a *App) apiAdminListGroups(c *gin.Context) {
	var groups []models.Group
	if err := a.DB.Order("name").Find(&groups).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list groups")
		return
	}
	a.respondGroups(c, groups)
}

func (a *App) apiAdminAddMember(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	var body struct {
		UserID      uint   `json:"userId"`
		SelectionID string `json:"selectionId"`
		Role        string `json:"role"`
		// OverLimit: add even though the group is full (DEC-39); audited.
		OverLimit bool `json:"overLimit"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || (body.UserID == 0) == (body.SelectionID == "") {
		jsonError(c, 400, "choose one person by selection or userId")
		return
	}
	var u models.User
	if body.SelectionID != "" {
		selected, ok := a.resolvePeopleSelection(c, body.SelectionID)
		if !ok {
			return
		}
		u = *selected
	} else if err := a.DB.First(&u, body.UserID).Error; err != nil {
		jsonError(c, 404, "user not found")
		return
	}
	if policy.TeenAddNeedsApproval(asPerson(&u)) {
		fail(c, errTeenAddApproval)
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
	err := a.changeRole(c, &g, u.ID, role, body.OverLimit, &memberAddOptions{}, func(me, target *models.User, actor, t policy.Rel, leaders int) bool {
		// Admins may add someone straight in with either role.
		return policy.IsAdmin(asPerson(me)) && policy.CanChangeRole(asPerson(me), actor, asPerson(target),
			policy.Rel{Member: true, Leader: t.Leader}, role == models.MembershipLeader, leaders)
	})
	switch err {
	case nil:
	case errRoleRefused: // here the refusal is the leader rule
		fail(c, errLeaderRuleAdults)
		return
	default:
		fail(c, err)
		return
	}
	a.membershipAdded(c, &g, u.ID)
}

func (a *App) apiDeletionRequests(c *gin.Context) {
	var groups []models.Group
	if err := a.DB.Where("deletion_requested = ?", true).Order("updated_at").Find(&groups).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list groups")
		return
	}
	a.respondGroups(c, groups)
}

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
