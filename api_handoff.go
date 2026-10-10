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

// handOffLeadership runs inside the deletion transaction. For each group
// leaving solely leads it makes successors[groupID] a leader (adding them
// to the group if needed, as an admin may not be a member), points the
// primary contact at them, and audits it with reason. Then it ends all of
// leaving's memberships.
func handOffLeadership(c *gin.Context, tx *gorm.DB, leaving uint, successors map[string]uint, reason string) error {
	actor := apiUser(c)
	groups, err := soleLedGroups(tx, leaving)
	if err != nil {
		return err
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
		return errNeedSuccessors{missing}
	}
	for _, g := range groups {
		g := g
		sid := picks[g.ID]
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&g, g.ID).Error; err != nil {
			return err
		}
		var m models.Membership
		if err := tx.Where("user_id = ? AND group_id = ?", sid, g.ID).Limit(1).Find(&m).Error; err != nil {
			return err
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
			return err
		}
		if err := tx.Model(&models.Group{}).Where("id = ?", g.ID).Update("leader_id", sid).Error; err != nil {
			return err
		}
		if err := audit.Write(tx, audit.Entry{
			Actor: &actor.ID, Action: audit.RoleChanged, TargetType: "group", TargetID: g.ID,
			Meta: map[string]any{"userId": sid, "oldRole": oldRole, "newRole": models.MembershipLeader,
				"reason": reason, "departingUserId": leaving},
			IP: c.ClientIP(),
		}); err != nil {
			return err
		}
	}
	if err := tx.Where("user_id = ?", leaving).Delete(&models.Membership{}).Error; err != nil {
		return err
	}
	// Groups they co-led still show them as the primary contact; move it.
	var contactOf []models.Group
	if err := tx.Where("leader_id = ?", leaving).Find(&contactOf).Error; err != nil {
		return err
	}
	for i := range contactOf {
		if err := fixPrimaryContact(tx, &contactOf[i]); err != nil {
			return err
		}
	}
	return nil
}

// needSuccessors answers 400 with the groups still missing a successor.
func needSuccessors(c *gin.Context, err error) bool {
	var need errNeedSuccessors
	if !errors.As(err, &need) {
		return false
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": need.Error(), "groups": need.groups})
	return true
}
