package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ---------------------------------------------------------------------------
// Group endpoints
// ---------------------------------------------------------------------------

func (a *App) apiListGroups(c *gin.Context) {
	search := c.Query("search")

	var groups []models.Group
	query := a.directoryQuery(apiUser(c))
	if search != "" {
		like := likeContains(strings.ToLower(search))
		query = query.Where(
			"LOWER(name) LIKE ? ESCAPE '\\' OR LOWER(description) LIKE ? ESCAPE '\\'",
			like, like,
		)
	}
	// Repeated tag params are AND-ed. Blank values are ignored.
	for _, tag := range c.QueryArray("tag") {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		query = query.Where("tags LIKE ? ESCAPE '\\'", likeContains(tag))
	}
	query.Order("name").Find(&groups)

	out := make([]groupSummaryJSON, 0, len(groups))
	for i := range groups {
		out = append(out, a.groupSummary(&groups[i]))
	}
	c.JSON(http.StatusOK, gin.H{"groups": out})
}

func (a *App) apiCreateGroup(c *gin.Context) {
	u := apiUser(c)
	if !policy.CanCreateGroup(asPerson(u)) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return
	}
	var body struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
		ImageURL    string   `json:"imageUrl"`
		Location    string   `json:"location"`
		Visibility  string   `json:"visibility"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		jsonError(c, http.StatusBadRequest, "name is required")
		return
	}
	name := strings.TrimSpace(body.Name)
	description := strings.TrimSpace(body.Description)
	location := strings.TrimSpace(body.Location)
	tags := joinTags(body.Tags)
	if msg := validateGroupText(name, description, location, tags); msg != "" {
		jsonError(c, http.StatusBadRequest, msg)
		return
	}
	visibility, msg := normalizeVisibility(body.Visibility)
	if msg != "" {
		jsonError(c, http.StatusBadRequest, msg)
		return
	}
	g := models.Group{
		Name:        name,
		Description: description,
		Tags:        tags,
		ImageURL:    strings.TrimSpace(body.ImageURL),
		Location:    location,
		LeaderID:    u.ID, // displayed primary contact
		Status:      policy.NewGroupStatus(asPerson(u)),
		Visibility:  visibility,
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&g).Error; err != nil {
			return err
		}
		// The creator is the group's first leader.
		return tx.Create(&models.Membership{
			UserID: u.ID, GroupID: g.ID,
			Role: models.MembershipLeader, Status: "active", JoinedAt: time.Now().UTC(),
		}).Error
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not create group")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"group": a.groupSummary(&g)})
}

func (a *App) apiGetGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	g, rel, ok := a.loadGroupFor(c, uint(id))
	if !ok {
		return
	}
	me := apiUser(c)
	p := asPerson(me)

	memberList := []memberJSON{}
	if policy.CanSeeRoster(p, asGroup(g), rel) {
		memberList = a.roster(g.ID)
	}

	var meetings []models.Meeting
	a.DB.Where("group_id = ?", g.ID).Find(&meetings)
	now := time.Now().UTC()
	upcoming := a.buildInstances(meetings, now, now.AddDate(0, 0, 90), me)

	c.JSON(http.StatusOK, gin.H{
		"group":            a.groupSummary(g),
		"members":          memberList,
		"upcomingMeetings": upcoming,
		"canManage":        policy.CanManageGroup(p, rel),
		"isMember":         rel.Member,
	})
}

// roster lists a group's active members whose accounts still exist.
func (a *App) roster(groupID uint) []memberJSON {
	var members []models.Membership
	a.DB.Preload("User").
		Joins("JOIN users ON users.id = memberships.user_id AND users.deleted_at IS NULL").
		Where("memberships.group_id = ? AND memberships.status = ?", groupID, "active").
		Order("memberships.joined_at").Find(&members)
	out := make([]memberJSON, 0, len(members))
	for i := range members {
		out = append(out, a.toMemberJSON(&members[i]))
	}
	return out
}

func (a *App) apiUpdateGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	g, rel, ok := a.loadManagedGroupID(c, uint(id))
	if !ok {
		return
	}
	u := apiUser(c)
	var body struct {
		Name        *string  `json:"name"`
		Description *string  `json:"description"`
		Tags        []string `json:"tags"`
		ImageURL    *string  `json:"imageUrl"`
		Location    *string  `json:"location"`
		Status      *string  `json:"status"`
		CoverFocal  *struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		} `json:"coverFocal"`
		Visibility *string `json:"visibility"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		if name == "" {
			jsonError(c, http.StatusBadRequest, "name is required")
			return
		}
		if len(name) > maxGroupNameLen {
			jsonError(c, http.StatusBadRequest, "name is too long")
			return
		}
		g.Name = name
	}
	if body.Description != nil {
		description := strings.TrimSpace(*body.Description)
		if len(description) > maxDescriptionLen {
			jsonError(c, http.StatusBadRequest, "description is too long")
			return
		}
		g.Description = description
	}
	if body.Tags != nil {
		tags := joinTags(body.Tags)
		if len(tags) > maxTagsLen {
			jsonError(c, http.StatusBadRequest, "tags are too long")
			return
		}
		g.Tags = tags
	}
	if body.ImageURL != nil {
		g.ImageURL = strings.TrimSpace(*body.ImageURL)
	}
	if body.CoverFocal != nil {
		g.CoverFocalX = clampFocal(body.CoverFocal.X)
		g.CoverFocalY = clampFocal(body.CoverFocal.Y)
	}
	if body.Location != nil {
		location := strings.TrimSpace(*body.Location)
		if len(location) > maxLocationLen {
			jsonError(c, http.StatusBadRequest, "location is too long")
			return
		}
		g.Location = location
	}
	if body.Status != nil {
		if !validGroupStatus(*body.Status) {
			jsonError(c, http.StatusBadRequest, "status must be pending, active, or archived")
			return
		}
		// Leaders may archive their own group; only admins may (re)activate.
		if !policy.CanSetGroupStatus(asPerson(u), rel, *body.Status) {
			jsonError(c, http.StatusForbidden, "only admins can change status to "+*body.Status)
			return
		}
		g.Status = *body.Status
	}
	if body.Visibility != nil {
		visibility, msg := normalizeVisibility(*body.Visibility)
		if msg != "" {
			jsonError(c, http.StatusBadRequest, msg)
			return
		}
		g.Visibility = visibility
	}
	if err := a.DB.Save(g).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not update group")
		return
	}
	c.JSON(http.StatusOK, gin.H{"group": a.groupSummary(g)})
}

// ---------------------------------------------------------------------------
// Membership endpoints
// ---------------------------------------------------------------------------

func (a *App) apiJoinGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	g, rel, ok := a.loadGroupFor(c, uint(id))
	if !ok {
		return
	}
	u := apiUser(c)
	if g.Status != models.GroupActive {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	if !policy.CanJoinGroup(asPerson(u), asGroup(g), rel) {
		if rel.Removed {
			jsonError(c, http.StatusForbidden, "A leader removed you from this group")
			return
		}
		jsonError(c, http.StatusForbidden, "this group is invite only")
		return
	}
	m := models.Membership{UserID: u.ID, GroupID: g.ID}
	res := a.DB.Where("user_id = ? AND group_id = ?", u.ID, g.ID).
		Attrs(models.Membership{Role: models.MembershipMember, Status: "active", JoinedAt: time.Now().UTC()}).
		FirstOrCreate(&m)
	if res.Error != nil {
		jsonError(c, http.StatusInternalServerError, "could not join group")
		return
	}
	if m.Status != "active" { // an admin rejoining after a removal
		a.DB.Model(&m).Updates(map[string]any{"status": "active", "role": models.MembershipMember})
		m.Status, m.Role = "active", models.MembershipMember
	}
	// A new member means the leader is no longer alone, so a pending
	// deletion request no longer matches the rule that created it.
	if g.DeletionRequested && res.RowsAffected > 0 {
		a.DB.Model(&models.Group{}).Where("id = ?", g.ID).Update("deletion_requested", false)
	}
	c.JSON(http.StatusOK, gin.H{"membership": gin.H{
		"id": m.ID, "userId": m.UserID, "groupId": m.GroupID,
		"role": m.Role, "status": m.Status,
	}})
}

func (a *App) apiLeaveGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	u := apiUser(c)
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	if err := a.changeRole(c, &g, u.ID, "", "", func(_, target policy.Rel, leaders int) bool {
		return !target.Member || policy.CanLeaveGroup(target, leaders)
	}); err != nil {
		if err == errRoleRefused {
			jsonError(c, http.StatusBadRequest,
				"you are the only leader — make someone else a leader first")
			return
		}
		jsonError(c, http.StatusInternalServerError, "could not leave group")
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

func (a *App) apiGroupMembers(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	g, rel, ok := a.loadGroupFor(c, uint(id))
	if !ok {
		return
	}
	if !policy.CanSeeRoster(asPerson(apiUser(c)), asGroup(g), rel) {
		jsonError(c, http.StatusForbidden, "only members can see who is in this group")
		return
	}
	c.JSON(http.StatusOK, gin.H{"members": a.roster(g.ID)})
}

func validGroupStatus(s string) bool {
	switch s {
	case models.GroupPending, models.GroupActive, models.GroupArchived:
		return true
	}
	return false
}

func validateGroupText(name, description, location, tags string) string {
	if len(name) > maxGroupNameLen {
		return "name is too long"
	}
	if len(description) > maxDescriptionLen {
		return "description is too long"
	}
	if len(location) > maxLocationLen {
		return "location is too long"
	}
	if len(tags) > maxTagsLen {
		return "tags are too long"
	}
	return ""
}

// likeContains builds a LIKE pattern that matches the text as a substring.
// % and _ in the input are escaped so they are not wildcards. Callers must
// pass ESCAPE '\' so the escape character is explicit.
func likeContains(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return "%" + s + "%"
}

// directoryQuery is the home list: active groups a visitor may browse. It
// is policy.CanSeeGroup as SQL (TestDirectoryMatchesPolicy keeps the two
// in step): signed-out visitors get public groups; signed-in people also
// get private groups and the invite-only groups they belong to; admins get
// everything.
func (a *App) directoryQuery(u *models.User) *gorm.DB {
	q := a.DB.Model(&models.Group{}).Where("status = ?", models.GroupActive)
	p := asPerson(u)
	switch {
	case policy.IsAdmin(p):
		return q
	case p == nil:
		return q.Where("visibility = ?", models.VisibilityPublic)
	}
	return q.Where("visibility IN ? OR id IN (?)",
		[]string{models.VisibilityPublic, models.VisibilityPrivate},
		a.DB.Model(&models.Membership{}).Select("group_id").
			Where("user_id = ? AND status = ?", u.ID, "active"))
}

// normalizeVisibility accepts public, private, or invite_only; empty means
// public.
func normalizeVisibility(visibility string) (string, string) {
	visibility = strings.TrimSpace(visibility)
	if visibility == "" {
		visibility = models.VisibilityPublic
	}
	if !policy.ValidVisibility(visibility) {
		return "", "visibility must be public, private, or invite_only"
	}
	return visibility, ""
}

func (a *App) loadManagedGroup(c *gin.Context) (*models.Group, *models.User, bool) {
	id, _ := strconv.Atoi(c.Param("id"))
	g, _, ok := a.loadManagedGroupID(c, uint(id))
	return g, apiUser(c), ok
}

// apiSetMemberRole makes an active member a leader, or a leader a member
// again. Leaders may do this in their own groups and admins anywhere; there
// is no admin-approval gate (DEC-31), but every change is audited and shown
// to admins as an in-app notice.
func (a *App) apiSetMemberRole(c *gin.Context) {
	g, me, ok := a.loadManagedGroup(c)
	if !ok {
		return
	}
	uid, _ := strconv.Atoi(c.Param("userId"))
	var body struct {
		Role string `json:"role"`
	}
	if err := c.ShouldBindJSON(&body); err != nil ||
		(body.Role != models.MembershipLeader && body.Role != models.MembershipMember) {
		jsonError(c, http.StatusBadRequest, "role must be leader or member")
		return
	}
	var target models.User
	if err := a.DB.First(&target, uid).Error; err != nil {
		jsonError(c, http.StatusNotFound, "user not found")
		return
	}
	makeLeader := body.Role == models.MembershipLeader
	err := a.changeRole(c, g, target.ID, body.Role, "", func(actor, targetRel policy.Rel, leaders int) bool {
		return policy.CanChangeRole(asPerson(me), actor, asPerson(&target), targetRel, makeLeader, leaders)
	})
	switch err {
	case nil:
	case errRoleRefused:
		jsonError(c, http.StatusBadRequest,
			"only adult members can be made leaders, and a group must keep at least one leader")
		return
	default:
		jsonError(c, http.StatusInternalServerError, "could not change role")
		return
	}
	c.JSON(http.StatusOK, gin.H{"members": a.roster(g.ID)})
}

// errRoleRefused means policy said no once the group row was locked.
var errRoleRefused = errors.New("role change refused")

// changeRole sets userID's role in g to newRole: "leader" or "member",
// "" to leave the group, or memberRemoved when a leader or admin removes
// them (note is kept in the audit row only). It locks the group row so
// concurrent changes can't remove the last leader, asks allow with fresh
// facts, keeps the displayed primary contact pointing at a leader, and
// writes a membership.role_changed audit row when leadership changes, or a
// membership.removed row for every removal.
func (a *App) changeRole(c *gin.Context, g *models.Group, userID uint, newRole, note string,
	allow func(actor, target policy.Rel, leaders int) bool) error {
	actor := apiUser(c)
	return a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(g, g.ID).Error; err != nil {
			return err
		}
		var m models.Membership // any status, so a removed row is reused
		if err := tx.Where("user_id = ? AND group_id = ?", userID, g.ID).Limit(1).Find(&m).Error; err != nil {
			return err
		}
		targetRel := membershipRel(m)
		oldRole := ""
		if targetRel.Member {
			oldRole = m.Role
		}
		actorRel := targetRel
		if actor.ID != userID {
			actorRel = relIn(tx, actor.ID, g.ID)
		}
		if !allow(actorRel, targetRel, leaderCount(tx, g.ID)) {
			return errRoleRefused
		}
		if oldRole == newRole || (newRole == memberRemoved && !targetRel.Member) {
			return nil
		}
		switch {
		case newRole == "":
			if err := tx.Delete(&m).Error; err != nil {
				return err
			}
		case newRole == memberRemoved:
			if err := tx.Model(&m).Updates(map[string]any{"status": memberRemoved, "role": models.MembershipMember}).Error; err != nil {
				return err
			}
		case m.ID == 0:
			if err := tx.Create(&models.Membership{UserID: userID, GroupID: g.ID, Role: newRole,
				Status: "active", JoinedAt: time.Now().UTC()}).Error; err != nil {
				return err
			}
		default: // a role change, or adding back someone who left or was removed
			if err := tx.Model(&m).Updates(map[string]any{"role": newRole, "status": "active"}).Error; err != nil {
				return err
			}
		}
		if oldRole == models.MembershipLeader || newRole == models.MembershipLeader {
			if err := fixPrimaryContact(tx, g); err != nil {
				return err
			}
		}
		if newRole == memberRemoved {
			meta := map[string]any{"userId": userID, "oldRole": oldRole}
			if note != "" {
				meta["note"] = note
			}
			return audit.Write(tx, audit.Entry{
				Actor: &actor.ID, Action: audit.MemberRemoved,
				TargetType: "group", TargetID: g.ID, Meta: meta, IP: c.ClientIP(),
			})
		}
		if oldRole != models.MembershipLeader && newRole != models.MembershipLeader {
			return nil
		}
		return audit.Write(tx, audit.Entry{
			Actor: &actor.ID, Action: audit.RoleChanged,
			TargetType: "group", TargetID: g.ID,
			Meta: map[string]any{"userId": userID, "oldRole": roleOrNone(oldRole), "newRole": roleOrNone(newRole)},
			IP:   c.ClientIP(),
		})
	})
}

// fixPrimaryContact keeps Group.LeaderID (display only) on a current
// leader: the earliest-joined one when the old contact stopped leading.
func fixPrimaryContact(tx *gorm.DB, g *models.Group) error {
	if relIn(tx, g.LeaderID, g.ID).Leader {
		return nil
	}
	var next models.Membership
	if err := tx.Where("group_id = ? AND role = ? AND status = ?", g.ID, models.MembershipLeader, "active").
		Order("joined_at, id").First(&next).Error; err != nil {
		return nil // no leader left (an admin removed the last one is refused earlier)
	}
	g.LeaderID = next.UserID
	return tx.Model(&models.Group{}).Where("id = ?", g.ID).Update("leader_id", next.UserID).Error
}

// relIn is relOf on a given handle (a transaction).
func relIn(tx *gorm.DB, userID, groupID uint) policy.Rel {
	var m models.Membership
	tx.Where("user_id = ? AND group_id = ?", userID, groupID).Limit(1).Find(&m)
	return membershipRel(m)
}

func roleOrNone(r string) string {
	if r == "" {
		return "none"
	}
	return r
}

// apiAddGroupMember lets a leader add someone by email. Invite-only
// groups have no public join button, so this is how they grow.
func (a *App) apiAddGroupMember(c *gin.Context) {
	g, _, ok := a.loadManagedGroup(c)
	if !ok {
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Email) == "" {
		jsonError(c, http.StatusBadRequest, "email is required")
		return
	}
	var target models.User
	if err := a.DB.Where("email = ?", normalizeEmail(body.Email)).First(&target).Error; err != nil {
		jsonError(c, http.StatusNotFound, "user not found")
		return
	}
	m := models.Membership{UserID: target.ID, GroupID: g.ID}
	res := a.DB.Where("user_id = ? AND group_id = ?", target.ID, g.ID).
		Attrs(models.Membership{Role: models.MembershipMember, Status: "active", JoinedAt: time.Now().UTC()}).
		FirstOrCreate(&m)
	if res.Error != nil {
		jsonError(c, http.StatusInternalServerError, "could not add member")
		return
	}
	if m.Status != "active" {
		a.DB.Model(&m).Updates(map[string]any{"status": "active"})
		m.Status = "active"
	}
	if g.DeletionRequested && res.RowsAffected > 0 {
		a.DB.Model(&models.Group{}).Where("id = ?", g.ID).Update("deletion_requested", false)
	}
	c.JSON(http.StatusOK, gin.H{"membership": gin.H{
		"id": m.ID, "userId": m.UserID, "groupId": m.GroupID,
		"role": m.Role, "status": m.Status,
	}})
}

// apiRequestDeletion asks an admin to delete the group. Only a leader can
// ask, and only when nobody else is still a member.
func (a *App) apiRequestDeletion(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	g, rel, ok := a.loadGroupFor(c, uint(id))
	if !ok {
		return
	}
	u := apiUser(c)
	var others int64
	a.DB.Model(&models.Membership{}).
		Where("group_id = ? AND status = ? AND user_id <> ?", g.ID, "active", u.ID).
		Count(&others)
	if !rel.Leader {
		jsonError(c, http.StatusForbidden, "only a group leader can request deletion")
		return
	}
	if !policy.CanRequestDeletion(asPerson(u), rel, others) {
		jsonError(c, http.StatusBadRequest, "the group still has other members")
		return
	}
	if err := a.DB.Model(g).Update("deletion_requested", true).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not request deletion")
		return
	}
	g.DeletionRequested = true
	c.JSON(http.StatusOK, gin.H{"group": a.groupSummary(g)})
}

// apiCancelDeletion withdraws a leader's deletion request.
func (a *App) apiCancelDeletion(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	g, rel, ok := a.loadGroupFor(c, uint(id))
	if !ok {
		return
	}
	if !policy.CanCancelDeletion(asPerson(apiUser(c)), rel) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return
	}
	if err := a.DB.Model(g).Update("deletion_requested", false).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not cancel deletion")
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

// deleteGroup removes the group and the rows that belong to it. Meetings
// and the group itself are soft-deleted. RSVPs, exceptions, and memberships
// have no deleted-at column, so they are removed outright.
func (a *App) deleteGroup(ctx context.Context, g *models.Group) error {
	imageURL := g.ImageURL
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var meetingIDs []uint
		if err := tx.Unscoped().Model(&models.Meeting{}).Where("group_id = ?", g.ID).Pluck("id", &meetingIDs).Error; err != nil {
			return err
		}
		if len(meetingIDs) > 0 {
			if err := tx.Where("meeting_id IN ?", meetingIDs).Delete(&models.RSVP{}).Error; err != nil {
				return err
			}
			if err := tx.Where("meeting_id IN ?", meetingIDs).Delete(&models.MeetingException{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("group_id = ?", g.ID).Delete(&models.Meeting{}).Error; err != nil {
			return err
		}
		if err := tx.Where("group_id = ?", g.ID).Delete(&models.Membership{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.Group{}, g.ID).Error
	})
	if err != nil {
		return err
	}
	if key, ok := uploadKeyFromURL(imageURL); ok {
		_ = a.Uploads.Delete(ctx, key)
	}
	return nil
}

func memberCount(db *gorm.DB, groupID uint) int64 {
	var n int64
	db.Model(&models.Membership{}).Where("group_id = ? AND status = ?", groupID, "active").Count(&n)
	return n
}

// apiRemoveGroupMember lets a group's leaders (and admins) remove someone
// from the roster (DEC-35). 404 for a group the caller can't see, 403 for
// one they don't lead.
func (a *App) apiRemoveGroupMember(c *gin.Context) {
	g, _, ok := a.loadManagedGroup(c)
	if !ok {
		return
	}
	a.removeMember(c, g)
}

// removeMember removes :userId from g if policy allows, marks the
// membership removed, and audits it. Body: optional {note}.
func (a *App) removeMember(c *gin.Context, g *models.Group) {
	uid, _ := strconv.Atoi(c.Param("userId"))
	var body struct {
		Note string `json:"note"`
	}
	_ = c.ShouldBindJSON(&body) // the body is optional
	note := strings.TrimSpace(body.Note)
	if len(note) > 500 {
		jsonError(c, http.StatusBadRequest, "note must be at most 500 characters")
		return
	}
	me := apiUser(c)
	if uint(uid) == me.ID {
		jsonError(c, http.StatusBadRequest, "to leave the group, use leave")
		return
	}
	var target models.User
	if err := a.DB.First(&target, uid).Error; err != nil {
		jsonError(c, http.StatusNotFound, "user not found")
		return
	}
	err := a.changeRole(c, g, target.ID, memberRemoved, note, func(actor, t policy.Rel, leaders int) bool {
		return policy.CanRemoveMember(asPerson(me), actor, asPerson(&target), t, leaders)
	})
	switch err {
	case nil:
	case errRoleRefused:
		jsonError(c, http.StatusBadRequest,
			"leaders can't remove an admin, and nobody can remove the group's only leader (make someone else a leader first)")
		return
	default:
		jsonError(c, http.StatusInternalServerError, "could not remove member")
		return
	}
	c.JSON(http.StatusOK, gin.H{"members": a.roster(g.ID)})
}
