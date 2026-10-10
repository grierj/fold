package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"fold/internal/audit"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Group member limits (DEC-39). A group may cap its active memberships
// (a class in a room); nil means unlimited. Self-join is refused when full,
// and there is no waitlist. Leaders and admins go over only by explicit
// choice (overLimit), which is audited. A re-enabled account whose group is
// full waits as pending_return until a leader or admin approves or declines.

// parseMemberLimit reads memberLimit from a body: null clears it, a
// positive whole number sets it. raw is nil when the field is absent.
func parseMemberLimit(raw json.RawMessage) (limit *int, set bool, msg string) {
	if raw == nil {
		return nil, false, ""
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, true, ""
	}
	var n int
	if json.Unmarshal(raw, &n) != nil || n < 1 || n > 100000 {
		return nil, false, "member limit must be a positive whole number, or empty for no limit"
	}
	return &n, true, ""
}

// apiDecideReturn approves or declines a pending_return membership. Leaders
// of the group and admins decide. Approving is an explicit choice, so it
// may go over the limit; declining sets the membership to removed.
func (a *App) apiDecideReturn(c *gin.Context) {
	g, me, ok := a.loadManagedGroup(c)
	if !ok {
		return
	}
	uid, _ := strconv.Atoi(c.Param("userId"))
	var body struct {
		Decision string `json:"decision"` // "approve" | "decline"
	}
	if err := c.ShouldBindJSON(&body); err != nil || (body.Decision != "approve" && body.Decision != "decline") {
		jsonError(c, http.StatusBadRequest, "decision must be approve or decline")
		return
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(g, g.ID).Error; err != nil {
			return err
		}
		var m models.Membership
		if err := tx.Joins(listedUsers("memberships")).
			Where("memberships.user_id = ? AND memberships.group_id = ? AND memberships.status = ?",
				uid, g.ID, memberPendingReturn).First(&m).Error; err != nil {
			return err
		}
		count := int(memberCount(tx, g.ID))
		meta := map[string]any{"userId": uint(uid), "count": count}
		if g.MemberLimit != nil {
			meta["limit"] = *g.MemberLimit
		}
		status, action := "active", audit.ReturnApproved
		if body.Decision == "decline" {
			status, action = memberRemoved, audit.ReturnDeclined
		}
		if err := tx.Model(&m).Updates(map[string]any{"status": status, "role": models.MembershipMember}).Error; err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: action,
			TargetType: "group", TargetID: g.ID, Meta: meta, IP: c.ClientIP()})
	})
	switch {
	case err == gorm.ErrRecordNotFound:
		jsonError(c, http.StatusNotFound, "nobody is waiting to return")
	case err != nil:
		jsonError(c, http.StatusInternalServerError, "could not save the decision")
	default:
		c.JSON(http.StatusOK, gin.H{"group": a.groupSummary(g), "members": a.roster(g.ID),
			"pendingReturns": a.membersWith(g.ID, memberPendingReturn)})
	}
}

type pendingReturnJSON struct {
	Group  groupSummaryJSON `json:"group"`
	Member memberJSON       `json:"member"`
}

// pendingReturns lists pending_return memberships of listed accounts, in
// groups u leads, or everywhere for an admin.
func (a *App) pendingReturns(u *models.User) []pendingReturnJSON {
	q := a.DB.Preload("User").Joins(listedUsers("memberships")).
		Where("memberships.status = ?", memberPendingReturn)
	if !policy.IsAdmin(asPerson(u)) {
		q = q.Where("memberships.group_id IN (?)", a.DB.Model(&models.Membership{}).Select("group_id").
			Where("user_id = ? AND role = ? AND status = ?", u.ID, models.MembershipLeader, "active"))
	}
	var rows []models.Membership
	q.Order("memberships.group_id, memberships.id").Find(&rows)
	out := []pendingReturnJSON{}
	groups := map[uint]*models.Group{}
	for i := range rows {
		g, ok := groups[rows[i].GroupID]
		if !ok {
			g = &models.Group{}
			if a.DB.First(g, rows[i].GroupID).Error != nil {
				continue
			}
			groups[g.ID] = g
		}
		out = append(out, pendingReturnJSON{Group: a.groupSummary(g), Member: a.toMemberJSON(&rows[i])})
	}
	return out
}

// pendingReturnNotices: one in-app notice per pending return, for the
// group's leaders and for admins.
func (a *App) pendingReturnNotices(u *models.User) []notificationJSON {
	out := []notificationJSON{}
	for _, p := range a.pendingReturns(u) {
		out = append(out, notificationJSON{
			Type:  "approval",
			Title: fmt.Sprintf("%s is waiting to return to %s", p.Member.Name, p.Group.Name),
			Body:  "Their account was turned back on while the group was full. Approve or decline on the roster.",
			Link:  fmt.Sprintf("/groups/%d#waiting", p.Group.ID),
		})
	}
	return out
}

func (a *App) apiAdminPendingReturns(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"returns": a.pendingReturns(apiUser(c))})
}
