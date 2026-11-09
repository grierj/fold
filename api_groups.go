package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/grouptags"
	"fold/internal/mail"
	"fold/internal/notify"
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
	// Repeated tag params are OR-ed: a group matches if it has any of them.
	// Matching is exact per tag and case-insensitive ("men" never matches
	// "women"); blank values are ignored and a comma in a query value is a
	// separator too (internal/grouptags is the one rule).
	if tags := grouptags.Parse(strings.Join(c.QueryArray("tag"), ",")); len(tags) > 0 {
		query = query.Where(grouptags.MatchAnySQL, grouptags.Array(tags))
	}
	if err := query.Order("name").Find(&groups).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list groups")
		return
	}

	roles, err := a.viewerRoles(apiUser(c), groups)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list groups")
		return
	}
	list, err := a.viewerGroupList(groups, roles)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list groups")
		return
	}
	c.JSON(http.StatusOK, groupListResponseJSON{Groups: list})
}

// viewerRoles maps each listed group the viewer actively belongs to onto
// their role there; nil for a signed-out viewer.
func (a *App) viewerRoles(me *models.User, groups []models.Group) (map[uint]string, error) {
	if me == nil || len(groups) == 0 {
		return nil, nil
	}
	ids := make([]uint, len(groups))
	for i := range groups {
		ids[i] = groups[i].ID
	}
	rels, err := groupRels(a.DB, me.ID, ids)
	if err != nil {
		return nil, err
	}
	roles := make(map[uint]string, len(rels))
	for id, rel := range rels {
		switch {
		case rel.Leader:
			roles[id] = models.MembershipLeader
		case rel.Member:
			roles[id] = models.MembershipMember
		}
	}
	return roles, nil
}

// viewerGroupList is a group list as its viewer browses it (DEC-94): each
// summary carries the viewer's own role from roles, and the list runs by
// next meeting, soonest first, then by name, with groups that have no
// upcoming date last.
func (a *App) viewerGroupList(groups []models.Group, roles map[uint]string) ([]groupSummaryJSON, error) {
	out, err := a.groupSummaries(groups)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].MyRole = roles[out[i].ID]
	}
	sort.SliceStable(out, func(i, j int) bool {
		x, y := out[i].NextEventAt, out[j].NextEventAt
		if (x == nil) != (y == nil) {
			return x != nil
		}
		if x != nil && !x.Equal(*y) {
			return x.Before(*y)
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// respondGroup answers with one group's summary, or a 500 when it cannot be
// built.
func (a *App) respondGroup(c *gin.Context, status int, g *models.Group) {
	summary, err := a.groupSummary(g)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load group")
		return
	}
	c.JSON(status, groupResponseJSON{Group: summary})
}

func (a *App) apiCreateGroup(c *gin.Context) {
	u := apiUser(c)
	if !policy.CanCreateGroup(asPerson(u)) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return
	}
	var body struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Tags        []string        `json:"tags"`
		Location    string          `json:"location"`
		Visibility  string          `json:"visibility"`
		MemberLimit json.RawMessage `json:"memberLimit"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		fail(c, errNameRequired)
		return
	}
	name := strings.TrimSpace(body.Name)
	description := strings.TrimSpace(body.Description)
	location := strings.TrimSpace(body.Location)
	tags := joinTags(body.Tags)
	if err := validateGroupText(name, description, location, tags); err != nil {
		fail(c, err)
		return
	}
	visibility, msg := normalizeVisibility(body.Visibility)
	if msg != "" {
		jsonError(c, http.StatusBadRequest, msg)
		return
	}
	limit, _, msg := parseMemberLimit(body.MemberLimit)
	if msg != "" {
		jsonError(c, http.StatusBadRequest, msg)
		return
	}
	g := models.Group{
		Name:        name,
		Description: description,
		Tags:        tags,
		Location:    location,
		LeaderID:    u.ID, // displayed primary contact
		Status:      policy.NewGroupStatus(asPerson(u)),
		Visibility:  visibility,
		MemberLimit: limit,
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&g).Error; err != nil {
			return err
		}
		// The creator is the group's first leader.
		if err := tx.Create(&models.Membership{
			UserID: u.ID, GroupID: g.ID,
			Role: models.MembershipLeader, Status: models.MembershipActive, JoinedAt: time.Now().UTC(),
		}).Error; err != nil {
			return err
		}
		if g.Status == models.GroupPending {
			err := notifyGroupPending(tx, &g, u.ID)
			return err
		}
		return nil
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not create group")
		return
	}
	a.respondGroup(c, http.StatusCreated, &g)
}

const maxGroupUpcomingOccurrences = 25

// Cursors are positions, never authority: each page rechecks date visibility.
// StartsAt and ID match the occurrence list's deterministic sort order.
type groupUpcomingCursor struct {
	GroupID  uint      `json:"groupId"`
	StartsAt time.Time `json:"startsAt"`
	ID       uint      `json:"id"`
}

type groupDetailJSON struct {
	Group          groupSummaryJSON `json:"group"`
	Members        []memberJSON     `json:"members"`
	UpcomingEvents []occurrenceJSON `json:"upcomingEvents"`
	Truncated      bool             `json:"truncated"`
	HasMore        bool             `json:"hasMore"`
	NextCursor     string           `json:"nextCursor"`
	CanManage      bool             `json:"canManage"`
	IsMember       bool             `json:"isMember"`
}

func parseGroupUpcomingPage(c *gin.Context, groupID uint) (int, *groupUpcomingCursor, bool) {
	limit := maxGroupUpcomingOccurrences
	if raw, present := c.GetQuery("limit"); present {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			fail(c, errInvalidUpcomingPage)
			return 0, nil, false
		}
		limit = n
	}
	var cursor *groupUpcomingCursor
	if raw, present := c.GetQuery("cursor"); present {
		cursor = &groupUpcomingCursor{}
		data, err := base64.RawURLEncoding.DecodeString(raw)
		if len(raw) > 512 || err != nil || json.Unmarshal(data, cursor) != nil || cursor.GroupID != groupID || cursor.ID == 0 || cursor.StartsAt.IsZero() {
			fail(c, errInvalidUpcomingPage)
			return 0, nil, false
		}
	}
	return limit, cursor, true
}

func nextGroupUpcomingCursor(groupID uint, dates []occurrenceJSON, hasMore bool) string {
	if !hasMore {
		return ""
	}
	last := dates[len(dates)-1]
	data, _ := json.Marshal(groupUpcomingCursor{GroupID: groupID, StartsAt: last.StartsAt, ID: last.ID})
	return base64.RawURLEncoding.EncodeToString(data)
}

func (a *App) apiGetGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	g, rel, ok := a.loadGroupFor(c, uint(id))
	if !ok {
		return
	}
	limit, cursor, ok := parseGroupUpcomingPage(c, g.ID)
	if !ok {
		return
	}
	me := apiUser(c)
	p := asPerson(me)

	memberList := []memberJSON{}
	if policy.CanSeeRoster(p, asGroup(g), rel) {
		var err error
		if memberList, err = a.rosterFor(g.ID, me, policy.CanManageGroup(p, rel)); err != nil {
			jsonError(c, http.StatusInternalServerError, "could not load group")
			return
		}
	}

	now := time.Now().UTC()
	var eventIDs []uint
	if err := a.DB.Model(&models.Event{}).Where("group_id = ?", g.ID).Pluck("id", &eventIDs).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load group")
		return
	}
	from, to := upcomingWindow(now)
	upcoming, hasMore, err := a.occurrencesForEventsAfterErr(eventIDs, from, to, &now, me, limit, cursor)
	if err != nil {
		log.Printf("group %d dates: %v", g.ID, err)
		jsonError(c, http.StatusInternalServerError, "could not load group")
		return
	}
	summary, err := a.groupSummary(g)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load group")
		return
	}
	c.JSON(http.StatusOK, groupDetailJSON{
		Group: summary, Members: memberList, UpcomingEvents: upcoming,
		Truncated: hasMore, HasMore: hasMore, NextCursor: nextGroupUpcomingCursor(g.ID, upcoming, hasMore),
		CanManage: policy.CanManageGroup(p, rel), IsMember: rel.Member,
	})
}

// respondRoster answers with the group's roster as its viewer may see it.
func (a *App) respondRoster(c *gin.Context, groupID uint, manager bool) {
	members, err := a.rosterFor(groupID, apiUser(c), manager)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load members")
		return
	}
	c.JSON(http.StatusOK, membersResponseJSON{Members: members})
}

// rosterFor exposes minor identities and badges only to the group's managers.
func (a *App) rosterFor(groupID uint, viewer *models.User, manager bool) ([]memberJSON, error) {
	var members []models.Membership
	if err := a.DB.Preload("User").Joins(listedUsers("memberships")).
		Where("memberships.group_id = ? AND memberships.status = ?", groupID, models.MembershipActive).
		Order("memberships.joined_at").Find(&members).Error; err != nil {
		return nil, err
	}
	users := make(map[uint]*models.User, len(members))
	for i := range members {
		users[members[i].User.ID] = &members[i].User
	}
	guardians := a.confirmedContactGuardians(viewer, users)
	out := make([]memberJSON, 0, len(members))
	for i := range members {
		if members[i].User.AccountType == models.AccountTeen && !policy.CanSeeMinorRosterIdentity(asPerson(viewer), manager) {
			continue
		}
		item := a.toMemberJSON(&members[i])
		item.Email, item.Phone = contactDetails(viewer, &members[i].User, policy.Contact{
			GroupLeader: manager, GroupMember: true,
			ConfirmedGuardian: guardians[members[i].User.ID],
		})
		if item.AccountType != models.AccountTeen {
			item.AccountType = ""
		}
		out = append(out, item)
	}
	return out, nil
}

func (a *App) apiUpdateGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	g, _, ok := a.loadManagedGroupID(c, uint(id))
	if !ok {
		return
	}
	u := apiUser(c)
	var body struct {
		Name        *string  `json:"name"`
		Description *string  `json:"description"`
		Tags        []string `json:"tags"`
		Location    *string  `json:"location"`
		Status      *string  `json:"status"`
		CoverFocal  *struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		} `json:"coverFocal"`
		Visibility *string `json:"visibility"`
		// OpenedEvents: when the group becomes more private, a "keep" or
		// "tighten" decision for each leader-opened event that is wider than
		// the new value, keyed by event id (DEC-48). See visibility-impact.
		OpenedEvents map[string]string `json:"openedEvents"`
		// MemberLimit: a number sets it, null clears it (DEC-39). Lowering it
		// below the current count is fine; nobody is removed.
		MemberLimit json.RawMessage `json:"memberLimit"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid body")
		return
	}
	patch := models.Group{}
	columns := []string{}
	if body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		if name == "" {
			fail(c, errNameRequired)
			return
		}
		if err := groupNameField.check(name); err != nil {
			fail(c, err)
			return
		}
		patch.Name = name
		columns = append(columns, "name")
	}
	if body.Description != nil {
		description := strings.TrimSpace(*body.Description)
		if err := descriptionField.check(description); err != nil {
			fail(c, err)
			return
		}
		patch.Description = description
		columns = append(columns, "description")
	}
	if body.Tags != nil {
		tags := joinTags(body.Tags)
		if err := tagsField.check(tags); err != nil {
			fail(c, err)
			return
		}
		patch.Tags = tags
		columns = append(columns, "tags")
	}
	if body.CoverFocal != nil {
		patch.CoverFocalX = clampFocal(body.CoverFocal.X)
		patch.CoverFocalY = clampFocal(body.CoverFocal.Y)
		columns = append(columns, "cover_focal_x", "cover_focal_y")
	}
	if body.Location != nil {
		location := strings.TrimSpace(*body.Location)
		if err := locationField.check(location); err != nil {
			fail(c, err)
			return
		}
		patch.Location = location
		columns = append(columns, "location")
	}
	if body.Status != nil {
		if !validGroupStatus(*body.Status) {
			jsonError(c, http.StatusBadRequest, "status must be pending, active, or archived")
			return
		}
		patch.Status = *body.Status
		columns = append(columns, "status")
	}
	if body.Visibility != nil {
		visibility, msg := normalizeVisibility(*body.Visibility)
		if msg != "" {
			jsonError(c, http.StatusBadRequest, msg)
			return
		}
		patch.Visibility = visibility
		columns = append(columns, "visibility")
	}
	if limit, set, msg := parseMemberLimit(body.MemberLimit); msg != "" {
		jsonError(c, http.StatusBadRequest, msg)
		return
	} else if set {
		patch.MemberLimit = limit
		columns = append(columns, "member_limit")
	}
	errHidden := errors.New("group not found")
	errForbidden := errors.New("group edit forbidden")
	var impact visibilityImpactJSON
	err := a.contactMutation(contactChange{GroupID: g.ID}, func(tx *gorm.DB) error {
		// Contact gate -> actor -> group -> membership, before domain writes.
		actor, err := lockActorIn(tx, u, actorNoKeyUpdate)
		if err != nil {
			return err
		}
		var locked models.Group
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, g.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errHidden
			}
			return err
		}
		var membership models.Membership
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("group_id = ? AND user_id = ?", g.ID, actor.ID).Limit(1).Find(&membership).Error; err != nil {
			return err
		}
		rel := membershipRel(membership)
		if !policy.CanSeeGroup(asPerson(actor), asGroup(&locked), rel) {
			return errHidden
		}
		if !policy.CanManageGroup(asPerson(actor), rel) ||
			(body.Status != nil && !policy.CanSetGroupStatus(asPerson(actor), rel, *body.Status)) {
			return errForbidden
		}
		// Downstream visibility/audit helpers use the same fresh actor.
		*u = *actor
		// Start with current state; omitted columns are never written.
		*g = locked
		if body.Name != nil {
			g.Name = patch.Name
		}
		if body.Description != nil {
			g.Description = patch.Description
		}
		if body.Tags != nil {
			g.Tags = patch.Tags
		}
		if body.Location != nil {
			g.Location = patch.Location
		}
		if body.Status != nil {
			g.Status = patch.Status
		}
		if body.Visibility != nil {
			g.Visibility = patch.Visibility
		}
		if body.CoverFocal != nil {
			g.CoverFocalX, g.CoverFocalY = patch.CoverFocalX, patch.CoverFocalY
		}
		if len(body.MemberLimit) != 0 {
			g.MemberLimit = patch.MemberLimit
		}
		if body.Visibility != nil {
			var err error
			if impact, err = a.applyGroupTightening(tx, u, c.ClientIP(), g, locked.Visibility, body.OpenedEvents); err != nil {
				return err
			}
		}
		// An admin activating or archiving a pending group by editing it
		// decides the approval task too.
		if locked.Status == models.GroupPending && g.Status != models.GroupPending {
			outcome, action := "approved", audit.GroupApproved
			if g.Status != models.GroupActive {
				outcome, action = "rejected", audit.GroupRejected
			}
			if _, err := notify.Resolve(tx, notify.GroupPending, g.ID, &u.ID, outcome); err != nil {
				return err
			}
			// Only an admin's decision is news to the leaders; a leader
			// archiving their own pending group is not "an admin decided".
			if policy.IsAdmin(asPerson(u)) {
				if err := notifyGroupDecided(tx, g, u.ID, g.Status == models.GroupActive, ""); err != nil {
					return err
				}
			}
			if err := audit.Write(tx, audit.Entry{Actor: &u.ID, Action: action, TargetType: "group", TargetID: g.ID, IP: c.ClientIP()}); err != nil {
				return err
			}
		}
		if len(columns) == 0 {
			return nil
		}
		return tx.Model(g).Select(columns).Updates(g).Error
	})
	if answerStaleSession(c, err) {
		return
	}
	if errors.Is(err, errHidden) {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	if errors.Is(err, errForbidden) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return
	}
	if errors.Is(err, errNeedDecision) {
		c.JSON(http.StatusConflict, gin.H{
			"error":         "Choose whether to keep each event a leader opened up, or make it match the group",
			"needsDecision": true,
			"impact":        impact,
		})
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not update group")
		return
	}
	a.respondGroup(c, http.StatusOK, g)
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
	// A pending or archived group is not there to join: 404 rather than the
	// invite-only refusal CanJoinGroup would give.
	if g.Status != models.GroupActive {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	if !policy.CanJoinGroup(asPerson(u), asGroup(g), rel) {
		switch {
		case rel.Removed:
			fail(c, errRemovedFromGroup)
		default:
			fail(c, errInviteOnly)
		}
		return
	}
	if !rel.Member {
		// Self-join never goes over the member limit (DEC-39).
		err := a.changeRole(c, g, u.ID, models.MembershipMember, false, nil, func(me, _ *models.User, _, t policy.Rel, _ int) bool {
			return policy.CanJoinGroup(asPerson(me), asGroup(g), t)
		})
		switch err {
		case nil:
		case errGroupFull:
			fail(c, errGroupFull)
			return
		case errSecuritySession:
			fail(c, errSecuritySession)
			return
		case errRoleRefused:
			fail(c, errCannotJoin)
			return
		default:
			jsonError(c, http.StatusInternalServerError, "could not join group")
			return
		}
	}
	a.membershipAdded(c, g, u.ID)
}

// membershipAdded clears a lone leader's deletion request once someone else
// is in the group, and answers with the membership.
func (a *App) membershipAdded(c *gin.Context, g *models.Group, userID uint) {
	if g.DeletionRequested {
		members, err := memberCount(a.DB, g.ID)
		if err == nil && members > 1 {
			err = a.clearDeletionRequest(g.ID, nil, "withdrawn")
		}
		if err != nil {
			fail(c, err)
			return
		}
	}
	var m models.Membership
	if err := a.DB.Where("user_id = ? AND group_id = ?", userID, g.ID).First(&m).Error; err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"membership": gin.H{
		"id": m.ID, "userId": m.UserID, "groupId": m.GroupID,
		"role": m.Role, "status": m.Status,
	}})
}

func (a *App) apiGroupMembers(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	g, rel, ok := a.loadGroupFor(c, uint(id))
	if !ok {
		return
	}
	if !policy.CanSeeRoster(asPerson(apiUser(c)), asGroup(g), rel) {
		fail(c, errMembersOnly)
		return
	}
	a.respondRoster(c, g.ID, policy.CanManageGroup(asPerson(apiUser(c)), rel))
}

func validGroupStatus(s string) bool {
	switch s {
	case models.GroupPending, models.GroupActive, models.GroupArchived:
		return true
	}
	return false
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
			Where("user_id = ? AND status = ?", u.ID, models.MembershipActive))
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

func (a *App) loadManagedGroup(c *gin.Context) (*models.Group, bool) {
	id, _ := strconv.Atoi(c.Param("id"))
	g, _, ok := a.loadManagedGroupID(c, uint(id))
	return g, ok
}

// apiSetMemberRole makes an active member a leader, or a leader a member
// again. Leaders may do this in their own groups and admins anywhere; there
// is no admin-approval gate (DEC-31), but every change is audited and shown
// to admins as an in-app notice.
func (a *App) apiSetMemberRole(c *gin.Context) {
	g, ok := a.loadManagedGroup(c)
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
	err := a.changeRole(c, g, target.ID, body.Role, false, nil, func(me, target *models.User, actor, targetRel policy.Rel, leaders int) bool {
		return policy.CanChangeRole(asPerson(me), actor, asPerson(target), targetRel, makeLeader, leaders)
	})
	switch err {
	case nil:
	case errSecuritySession:
		fail(c, errSecuritySession)
		return
	case errRoleRefused:
		fail(c, errLeaderRuleMembers)
		return
	default:
		jsonError(c, http.StatusInternalServerError, "could not change role")
		return
	}
	a.respondRoster(c, g.ID, true)
}

// errRoleRefused means policy said no once the group row was locked.
var errRoleRefused = apiErr(http.StatusForbidden, "", "forbidden")

// errGroupNotApprovedForAdds: only admins add people to a pending or
// archived group (policy.CanAddToGroup).
var errGroupNotApprovedForAdds = apiErr(http.StatusConflict, "group_add_not_approved", "this group isn't approved yet; you can add people once an admin approves it")

// memberAddOptions distinguishes direct manager adds from self-join and role
// changes. Regular adds preserve an active member's role; admin adds may change it.
type memberAddOptions struct {
	KeepExistingRole bool
	approved         *verifiedGroupAdd
	// invitationID names the invitation being accepted: the new member joins
	// on its authority (checked by the acceptor under the inviter's lock), so
	// the people-add rules do not apply, a teen still waiting for consent may
	// be added, and the over-limit audit row names the invitation.
	invitationID uint
}

// changeRole sets userID's role in g to newRole, "leader" or "member". It locks the group row so
// concurrent changes can't remove the last leader, asks allow with fresh
// facts, keeps the displayed primary contact pointing at a leader, and
// writes a membership.role_changed audit row when leadership changes. Direct manager additions require
// adult actors/targets and write membership.added.
func (a *App) changeRole(c *gin.Context, g *models.Group, userID uint, newRole string, overLimit bool, add *memberAddOptions,
	allow func(me, target *models.User, actorRel, targetRel policy.Rel, leaders int) bool) error {
	actor := apiUser(c)
	return a.contactMutation(contactChange{UserID: userID}, func(tx *gorm.DB) error {
		return a.changeRoleIn(tx, actor, c.ClientIP(), g, userID, newRole, overLimit, add, allow)
	})
}

// changeRoleIn requires the contact gate and caller-owned transaction. It locks
// users before the group/membership capacity protocol and never commits or sends emails.
func (a *App) changeRoleIn(tx *gorm.DB, actor *models.User, ip string, g *models.Group, userID uint, newRole string, overLimit bool, add *memberAddOptions, allow func(*models.User, *models.User, policy.Rel, policy.Rel, int) bool) error {
	var sessionErr error = errSecuritySession
	if add != nil {
		sessionErr = errPeopleAddForbidden
	}
	if actor == nil {
		return sessionErr
	}
	fresh, err := lockActorIn(tx, actor, actorNoKeyUpdate)
	if errors.Is(err, errSecuritySession) {
		return sessionErr
	}
	if err != nil {
		return err
	}
	actor = fresh
	target := actor
	if userID != actor.ID {
		target = &models.User{}
		if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(target, userID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errRoleRefused
			}
			return err
		}
	}
	viaInvitation := add != nil && add.invitationID != 0
	if target.Status != models.UserActive && !(viaInvitation && target.Status == models.UserPendingConsent) {
		return errRoleRefused
	}
	if add != nil && !viaInvitation {
		fresh, err := peopleAddActor(tx, actor)
		if err != nil {
			return err
		}
		actor = fresh
	}
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
		var err error
		if actorRel, err = groupRel(tx, actor.ID, g.ID); err != nil {
			return err
		}
	}
	leaders, err := leaderCount(tx, g.ID)
	if err != nil {
		return err
	}
	if !allow(actor, target, actorRel, targetRel, leaders) {
		return errRoleRefused
	}
	if (add != nil && add.KeepExistingRole && targetRel.Member) || oldRole == newRole {
		return nil
	}
	// Adding someone (or bringing them back) needs room under the member
	// limit, or an explicit over-limit choice, which is audited (DEC-39).
	adding := !targetRel.Member
	if adding && add != nil && !viaInvitation {
		if !policy.CanAddToGroup(asPerson(actor), g.Status) {
			return errGroupNotApprovedForAdds
		}
		if policy.TeenAddNeedsApproval(asPerson(target)) && (add.approved == nil || !a.verifiedGroupAddIn(tx, add.approved, actor.ID, userID, g.ID)) {
			return errTeenAddApproval
		}
	}
	count := 0
	if adding {
		if count, err = memberCount(tx, g.ID); err != nil {
			return err
		}
		if !policy.GroupHasRoom(g.MemberLimit, count) && !overLimit {
			return errGroupFull
		}
	}
	switch {
	case m.ID == 0:
		if err := tx.Create(&models.Membership{UserID: userID, GroupID: g.ID, Role: newRole,
			Status: models.MembershipActive, JoinedAt: time.Now().UTC()}).Error; err != nil {
			return err
		}
	default: // a role change, or adding back someone who left or was removed
		if err := tx.Model(&m).Updates(map[string]any{"role": newRole, "status": models.MembershipActive}).Error; err != nil {
			return err
		}
	}
	if oldRole == models.MembershipLeader || newRole == models.MembershipLeader {
		if err := fixPrimaryContact(tx, g); err != nil {
			return err
		}
	}
	if adding && !policy.GroupHasRoom(g.MemberLimit, count) {
		meta := map[string]any{"userId": userID, "count": count, "limit": *g.MemberLimit, "role": newRole}
		if viaInvitation {
			meta["invitationId"] = add.invitationID
		}
		if err := audit.Write(tx, audit.Entry{
			Actor: &actor.ID, Action: audit.AddedOverLimit, TargetType: "group", TargetID: g.ID,
			Meta: meta,
			IP:   ip,
		}); err != nil {
			return err
		}
	}
	if oldRole != models.MembershipLeader && newRole != models.MembershipLeader {
		if adding && add != nil && !viaInvitation {
			return audit.Write(tx, audit.Entry{Actor: &actor.ID, Action: audit.MemberAdded, TargetType: "group", TargetID: g.ID, Meta: map[string]any{"userId": userID, "role": newRole}, IP: ip})
		}
		return nil
	}
	err = auditAndNotifyLeadership(tx, audit.Entry{
		Actor: &actor.ID, Action: audit.RoleChanged,
		TargetType: "group", TargetID: g.ID,
		Meta: map[string]any{"userId": userID, "oldRole": roleOrNone(oldRole), "newRole": roleOrNone(newRole)},
		IP:   ip,
	})
	return err
}

// fixPrimaryContact keeps Group.LeaderID (display only) on a current
// leader: the earliest-joined one when the old contact stopped leading.
func fixPrimaryContact(tx *gorm.DB, g *models.Group) error {
	rel, err := groupRel(tx, g.LeaderID, g.ID)
	if err != nil || rel.Leader {
		return err
	}
	var next models.Membership
	if err := tx.Where("group_id = ? AND role = ? AND status = ?", g.ID, models.MembershipLeader, models.MembershipActive).
		Order("joined_at, id").First(&next).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // no leader left (an admin removed the last one is refused earlier)
		}
		return err
	}
	g.LeaderID = next.UserID
	return tx.Model(&models.Group{}).Where("id = ?", g.ID).Update("leader_id", next.UserID).Error
}

// apiAddGroupMember lets a leader add someone by selection or email. Invite-only
// groups have no public join button, so this is how they grow.
func (a *App) apiAddGroupMember(c *gin.Context) {
	g, ok := a.loadManagedGroup(c)
	if !ok {
		return
	}
	var body struct {
		Email       string `json:"email"`
		SelectionID string `json:"selectionId"`
		// OverLimit: add even though the group is full (DEC-39); audited.
		OverLimit bool `json:"overLimit"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || (strings.TrimSpace(body.Email) == "") == (body.SelectionID == "") {
		jsonError(c, http.StatusBadRequest, "choose one person by selection or email")
		return
	}
	var target models.User
	if body.SelectionID != "" {
		selected, ok := a.resolvePeopleSelection(c, body.SelectionID)
		if !ok {
			return
		}
		target = *selected
	} else if err := a.DB.Where("email = ?", mail.NormalizeAddress(body.Email)).First(&target).Error; err != nil {
		jsonError(c, http.StatusNotFound, "user not found")
		return
	}
	if policy.TeenAddNeedsApproval(asPerson(&target)) {
		fail(c, errTeenAddApproval)
		return
	}
	err := a.changeRole(c, g, target.ID, models.MembershipMember, body.OverLimit, &memberAddOptions{KeepExistingRole: true}, func(me, _ *models.User, actor, _ policy.Rel, _ int) bool {
		return policy.CanManageGroup(asPerson(me), actor)
	})
	if err != nil {
		fail(c, err)
		return
	}
	a.membershipAdded(c, g, target.ID)
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
	if err := a.DB.Model(&models.Membership{}).Joins(listedUsers("memberships")).
		Where("memberships.group_id = ? AND memberships.status = ? AND memberships.user_id <> ?", g.ID, models.MembershipActive, u.ID).
		Count(&others).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not request deletion")
		return
	}
	// A non-leader's answer is a 403 of its own, not the "group has members" 409.
	if !rel.Leader {
		jsonError(c, http.StatusForbidden, "only a group leader can request deletion")
		return
	}
	if !policy.CanRequestDeletion(asPerson(u), rel, others) {
		fail(c, errGroupHasMembers)
		return
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.Group{}).Where("id = ? AND NOT deletion_requested", g.ID).Update("deletion_requested", true)
		if res.Error != nil || res.RowsAffected == 0 {
			return res.Error // already requested: the admins already have the task
		}
		err := notifyDeletionRequested(tx, g, u.ID)
		return err
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not request deletion")
		return
	}
	g.DeletionRequested = true
	a.respondGroup(c, http.StatusOK, g)
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
	if err := a.clearDeletionRequest(g.ID, &apiUser(c).ID, "withdrawn"); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not cancel deletion")
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

// clearDeletionRequest turns a deletion request off and resolves the
// admins' task with outcome (kept, withdrawn).
func (a *App) clearDeletionRequest(groupID uint, by *uint, outcome string) error {
	return a.DB.Transaction(func(tx *gorm.DB) error {
		return clearDeletionRequestIn(tx, groupID, by, outcome)
	})
}

func clearDeletionRequestIn(tx *gorm.DB, groupID uint, by *uint, outcome string) error {
	if err := tx.Model(&models.Group{}).Where("id = ?", groupID).Update("deletion_requested", false).Error; err != nil {
		return err
	}
	_, err := notify.Resolve(tx, notify.GroupDeletion, groupID, by, outcome)
	return err
}

// auditAndNotifyLeadership writes a RoleChanged or MemberRemoved row and
// tells the other admins about a leadership change in the same transaction.
func auditAndNotifyLeadership(tx *gorm.DB, e audit.Entry) error {
	if err := audit.Write(tx, e); err != nil {
		return err
	}
	return notifyLeadership(tx, e)
}

// deleteGroup removes the group and the rows that belong to it. Events and
// the group itself are soft-deleted. Registrations, occurrences, and
// memberships have no deleted-at column, so they are removed outright.
func (a *App) deleteGroup(ctx context.Context, g *models.Group, by *uint) error {
	imageURL := g.ImageURL
	err := a.contactMutation(contactChange{GroupID: g.ID}, func(tx *gorm.DB) error {
		var eventIDs []uint
		if err := tx.Unscoped().Model(&models.Event{}).Where("group_id = ?", g.ID).Pluck("id", &eventIDs).Error; err != nil {
			return err
		}
		if err := deleteEventRows(tx, eventIDs); err != nil {
			return err
		}
		if err := tx.Where("group_id = ?", g.ID).Delete(&models.Membership{}).Error; err != nil {
			return err
		}
		// Any task still open about the group is done.
		for _, kind := range []string{notify.GroupPending, notify.GroupDeletion} {
			if _, err := notify.Resolve(tx, kind, g.ID, by, "deleted"); err != nil {
				return err
			}
		}
		if err := tx.Delete(&models.Group{}, g.ID).Error; err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: by, Action: audit.GroupDeleted, TargetType: "group", TargetID: g.ID, Meta: map[string]any{"name": g.Name}})
	})
	if err != nil {
		return err
	}
	// The group is gone; a cover that will not delete is only an orphaned file.
	if key, ok := uploadKeyFromURL(imageURL); ok {
		if err := a.Uploads.Delete(ctx, key); err != nil {
			log.Printf("delete cover of group %d: %v", g.ID, err)
		}
	}
	return nil
}

type groupSummaryJSON struct {
	ID                uint       `json:"id"`
	Name              string     `json:"name"`
	Description       string     `json:"description"`
	Tags              []string   `json:"tags"`
	ImageURL          string     `json:"imageUrl"`
	CoverFocalX       float64    `json:"coverFocalX"`
	CoverFocalY       float64    `json:"coverFocalY"`
	Location          string     `json:"location"`
	LeaderName        string     `json:"leaderName"`
	MemberCount       int64      `json:"memberCount"`
	NextEventAt       *time.Time `json:"nextEventAt"`
	Status            string     `json:"status"`
	LeaderID          uint       `json:"leaderId"`
	Visibility        string     `json:"visibility"` // public | private | invite_only
	DeletionRequested bool       `json:"deletionRequested"`
	// MemberLimit caps active members (DEC-39); null means unlimited.
	MemberLimit *int `json:"memberLimit"`
	// MyRole is the viewer's own active role, "leader" or "member", on the
	// directory and My groups lists only (DEC-94); absent otherwise.
	MyRole string `json:"myRole,omitempty"`
}

type memberJSON struct {
	Email        *string `json:"email,omitempty"`
	Phone        *string `json:"phone,omitempty"`
	AccountType  string  `json:"accountType,omitempty"`
	ID           uint    `json:"id"`
	Name         string  `json:"name"`
	AvatarURL    string  `json:"avatarUrl"`
	AvatarFocalX float64 `json:"avatarFocalX"`
	AvatarFocalY float64 `json:"avatarFocalY"`
	Role         string  `json:"role"`
	JoinedAt     string  `json:"joinedAt"`
	// Admin marks church admins so leaders see the label and no Remove button
	// (DEC-38); the server refuses such removals anyway (DEC-35).
	Admin bool `json:"admin"`
}

// toMemberJSON renders a membership's user for the roster.
func (a *App) toMemberJSON(m *models.Membership) memberJSON {
	avatarURL := ""
	if m.User.AvatarKey != "" && m.User.AccountType != models.AccountTeen {
		avatarURL = a.Uploads.URL(m.User.AvatarKey)
	}
	return memberJSON{
		ID: m.User.ID, Name: m.User.PublicName(), AvatarURL: avatarURL,
		AvatarFocalX: m.User.AvatarFocalX, AvatarFocalY: m.User.AvatarFocalY,
		Role: m.Role, JoinedAt: m.JoinedAt.Format(time.RFC3339),
		Admin: asPerson(&m.User).Admin, AccountType: m.User.AccountType,
	}
}

func splitTags(s string) []string {
	out := []string{}
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func joinTags(tags []string) string {
	var clean []string
	for _, t := range tags {
		if t = strings.TrimSpace(t); t != "" {
			clean = append(clean, t)
		}
	}
	return strings.Join(clean, ",")
}

// relOf loads u's membership in one group; the zero Rel for a signed-out viewer.
func (a *App) relOf(u *models.User, groupID uint) (policy.Rel, error) {
	if u == nil {
		return policy.Rel{}, nil
	}
	return groupRel(a.DB, u.ID, groupID)
}

// loadGroupFor loads group id for u: 404 when it doesn't exist or u can't
// see it (invite-only groups never answer 403 to outsiders).
func (a *App) loadGroupFor(c *gin.Context, id uint) (*models.Group, policy.Rel, bool) {
	u := apiUser(c)
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			jsonError(c, http.StatusNotFound, "group not found")
		} else {
			fail(c, err)
		}
		return nil, policy.Rel{}, false
	}
	rel, err := a.relOf(u, g.ID)
	if err != nil {
		fail(c, err)
		return nil, policy.Rel{}, false
	}
	if !policy.CanSeeGroup(asPerson(u), asGroup(&g), rel) {
		jsonError(c, http.StatusNotFound, "group not found")
		return nil, policy.Rel{}, false
	}
	return &g, rel, true
}

// loadManagedGroupID is loadGroupFor plus CanManageGroup (403 when the
// group is visible but u doesn't lead it).
func (a *App) loadManagedGroupID(c *gin.Context, id uint) (*models.Group, policy.Rel, bool) {
	g, rel, ok := a.loadGroupFor(c, id)
	if !ok {
		return nil, rel, false
	}
	if !policy.CanManageGroup(asPerson(apiUser(c)), rel) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return nil, rel, false
	}
	return g, rel, true
}
