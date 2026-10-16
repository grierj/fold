package main

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"fold/internal/audit"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Sole-leader handoff (DEC-36): before an admin deletes the account of a
// group's only leader, they pick a new leader for each such group: an adult
// member of that group or any admin. The SPA preselects the deleting admin.

type successorJSON struct {
	ID    uint   `json:"id"`
	Name  string `json:"name"`
	Admin bool   `json:"admin"`
}

type soleLedGroupJSON struct {
	ID         uint            `json:"id"`
	Name       string          `json:"name"`
	Status     string          `json:"status"`
	Candidates []successorJSON `json:"candidates"`
}

// soleLedGroups lists the groups, in any status, where userID is the only
// active leader.
func soleLedGroups(db *gorm.DB, userID uint) ([]models.Group, error) {
	var groups []models.Group
	err := db.Where(`id IN (SELECT group_id FROM memberships
		WHERE user_id = ? AND role = ? AND status = 'active')`, userID, models.MembershipLeader).
		Order("name").Find(&groups).Error
	if err != nil {
		return nil, err
	}
	out := groups[:0]
	for _, g := range groups {
		if leaderCount(db, g.ID) == 1 {
			out = append(out, g)
		}
	}
	return out, nil
}

// successorCandidates: the group's other active members who may lead, then
// every admin who isn't already listed.
func successorCandidates(db *gorm.DB, groupID, leaving uint) []successorJSON {
	var users []models.User
	db.Where(`id <> ? AND status = 'active' AND (role = ? OR id IN (SELECT user_id FROM memberships
		WHERE group_id = ? AND status = 'active'))`, leaving, models.RoleAdmin, groupID).
		Order("name, id").Find(&users)
	out := []successorJSON{}
	for i := range users {
		p := asPerson(&users[i])
		if policy.CanBeSuccessor(p, relIn(db, users[i].ID, groupID)) {
			out = append(out, successorJSON{ID: users[i].ID, Name: users[i].PublicName(), Admin: p.Admin})
		}
	}
	return out
}

// apiAdminSoleLedGroups: GET /admin/users/:id/sole-leader-groups.
func (a *App) apiAdminSoleLedGroups(c *gin.Context) {
	u, ok := a.adminTargetUser(c)
	if !ok {
		return
	}
	groups, err := soleLedGroups(a.DB, u.ID)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list groups")
		return
	}
	out := []soleLedGroupJSON{}
	for _, g := range groups {
		out = append(out, soleLedGroupJSON{ID: g.ID, Name: g.Name, Status: g.Status,
			Candidates: successorCandidates(a.DB, g.ID, u.ID)})
	}
	c.JSON(http.StatusOK, gin.H{"groups": out})
}

// errNeedSuccessors means some sole-led groups have no valid successor.
type errNeedSuccessors struct{ groups []soleLedGroupJSON }

func (errNeedSuccessors) Error() string {
	return "every group this person leads alone needs a new leader"
}

// handOffLeadership runs inside the delete or disable transaction. For each
// group leaving solely leads it makes successors[groupID] a leader (adding
// them to the group if needed, as an admin may not be a member), points the
// primary contact at them, and audits it with reason. Then deletion ends all
// of leaving's memberships, while disabling keeps them as plain
// memberships (DEC-38).
func handOffLeadership(c *gin.Context, tx *gorm.DB, leaving uint, successors map[string]uint, reason string) ([]uint, error) {
	actor := apiUser(c)
	groups, err := soleLedGroups(tx, leaving)
	if err != nil {
		return nil, err
	}
	var missing []soleLedGroupJSON
	picks := map[uint]uint{}
	for _, g := range groups {
		sid := successors[strconv.FormatUint(uint64(g.ID), 10)]
		var s models.User
		if sid == 0 || sid == leaving || tx.First(&s, sid).Error != nil || s.Status != models.UserActive ||
			!policy.CanBeSuccessor(asPerson(&s), relIn(tx, sid, g.ID)) {
			missing = append(missing, soleLedGroupJSON{ID: g.ID, Name: g.Name, Status: g.Status,
				Candidates: successorCandidates(tx, g.ID, leaving)})
			continue
		}
		picks[g.ID] = sid
	}
	if len(missing) > 0 {
		return nil, errNeedSuccessors{missing}
	}
	var emailed []uint
	for _, g := range groups {
		g := g
		sid := picks[g.ID]
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&g, g.ID).Error; err != nil {
			return nil, err
		}
		var m models.Membership
		if err := tx.Where("user_id = ? AND group_id = ?", sid, g.ID).Limit(1).Find(&m).Error; err != nil {
			return nil, err
		}
		oldRole := "none"
		if membershipRel(m).Member {
			oldRole = m.Role
		}
		m.UserID, m.GroupID, m.Role, m.Status = sid, g.ID, models.MembershipLeader, "active"
		if m.JoinedAt.IsZero() {
			m.JoinedAt = time.Now().UTC()
		}
		if err := tx.Save(&m).Error; err != nil {
			return nil, err
		}
		if err := tx.Model(&models.Group{}).Where("id = ?", g.ID).Update("leader_id", sid).Error; err != nil {
			return nil, err
		}
		ids, err := auditAndNotifyLeadership(tx, audit.Entry{
			Actor: &actor.ID, Action: audit.RoleChanged, TargetType: "group", TargetID: g.ID,
			Meta: map[string]any{"userId": sid, "oldRole": oldRole, "newRole": models.MembershipLeader,
				"reason": reason, "departingUserId": leaving},
			IP: c.ClientIP(),
		})
		emailed = append(emailed, ids...)
		if err != nil {
			return nil, err
		}
	}
	if reason == "account_deleted" {
		if err := tx.Where("user_id = ?", leaving).Delete(&models.Membership{}).Error; err != nil {
			return nil, err
		}
	} else {
		ids, err := demoteEverywhere(c, tx, leaving, reason)
		emailed = append(emailed, ids...)
		if err != nil {
			// Disabling keeps memberships (DEC-38); they are hidden while the
			// account is disabled, and any leadership becomes plain membership.
			return nil, err
		}
	}
	// Groups they co-led still show them as the primary contact; move it.
	var contactOf []models.Group
	if err := tx.Where("leader_id = ?", leaving).Find(&contactOf).Error; err != nil {
		return nil, err
	}
	for i := range contactOf {
		if err := fixPrimaryContact(tx, &contactOf[i]); err != nil {
			return nil, err
		}
	}
	return emailed, nil
}

// needSuccessors answers 400 with the groups still missing a successor.
func needSuccessors(c *gin.Context, err error) bool {
	var need errNeedSuccessors
	if !errors.As(err, &need) {
		return false
	}
	body := errorBody(http.StatusBadRequest, need.Error())
	body["groups"] = need.groups
	c.JSON(http.StatusBadRequest, body)
	return true
}

// demoteEverywhere makes every leadership of userID a plain membership, with
// a membership.role_changed audit row per group (DEC-38: a disabled
// account keeps its memberships but never its leadership).
func demoteEverywhere(c *gin.Context, tx *gorm.DB, userID uint, reason string) ([]uint, error) {
	actor := apiUser(c)
	var led []models.Membership
	if err := tx.Where("user_id = ? AND role = ? AND status = 'active'", userID, models.MembershipLeader).
		Find(&led).Error; err != nil {
		return nil, err
	}
	var emailed []uint
	for _, m := range led {
		if err := tx.Model(&m).Update("role", models.MembershipMember).Error; err != nil {
			return nil, err
		}
		ids, err := auditAndNotifyLeadership(tx, audit.Entry{
			Actor: &actor.ID, Action: audit.RoleChanged, TargetType: "group", TargetID: m.GroupID,
			Meta: map[string]any{"userId": userID, "oldRole": models.MembershipLeader,
				"newRole": models.MembershipMember, "reason": reason},
			IP: c.ClientIP(),
		})
		emailed = append(emailed, ids...)
		if err != nil {
			return nil, err
		}
	}
	return emailed, nil
}

// restoreMemberships runs when a disabled account is enabled again. Its
// kept memberships come back as plain members (DEC-38), one
// membership.restored audit row each. A group at its member limit leaves
// them out instead: the membership is dropped, a membership.not_restored
// row is written, and they get a notice to ask a leader (DEC-47).
func restoreMemberships(c *gin.Context, tx *gorm.DB, userID uint) ([]uint, error) {
	actor := apiUser(c)
	var kept []models.Membership
	// Stable order (by group) so concurrent restores lock groups the same way.
	if err := tx.Where("user_id = ? AND status = 'active'", userID).Order("group_id").Find(&kept).Error; err != nil {
		return nil, err
	}
	var emailed []uint
	for _, m := range kept {
		// Lock the group so a concurrent join or restore can't race the
		// count. The account is still disabled here, so it isn't counted.
		var g models.Group
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&g, m.GroupID).Error; err != nil {
			return nil, err
		}
		if !policy.GroupHasRoom(g.MemberLimit, int(memberCount(tx, g.ID))) {
			// Full: left out. A leader can add them over the limit (DEC-47).
			if err := tx.Delete(&m).Error; err != nil {
				return nil, err
			}
			meta := map[string]any{"userId": userID, "count": memberCount(tx, g.ID), "limit": *g.MemberLimit}
			if err := audit.Write(tx, audit.Entry{
				Actor: &actor.ID, Action: audit.MemberNotRestored, TargetType: "group", TargetID: m.GroupID,
				Meta: meta, IP: c.ClientIP(),
			}); err != nil {
				return nil, err
			}
			ids, err := notifyNotRestored(tx, actor.ID, &g, userID)
			emailed = append(emailed, ids...)
			if err != nil {
				return nil, err
			}
			continue
		}
		if err := tx.Model(&m).Updates(map[string]any{"role": models.MembershipMember, "status": "active"}).Error; err != nil {
			return nil, err
		}
		if err := audit.Write(tx, audit.Entry{
			Actor: &actor.ID, Action: audit.MemberRestored, TargetType: "group", TargetID: m.GroupID,
			Meta: map[string]any{"userId": userID, "status": "active"}, IP: c.ClientIP(),
		}); err != nil {
			return nil, err
		}
	}
	return emailed, nil
}
