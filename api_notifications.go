package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/events"
	"fold/internal/mail"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/internal/providers"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ---------------------------------------------------------------------------
// Notifications API (5.12): the user's own stored rows only. Anyone else's
// row answers 404.
// ---------------------------------------------------------------------------

type notificationJSON struct {
	ID         uint            `json:"id"`
	Kind       string          `json:"kind"`
	SubjectID  *uint           `json:"subjectId,omitempty"` // the group an inline action is about
	Category   string          `json:"category"`
	Title      string          `json:"title"`
	Body       string          `json:"body"`
	Link       string          `json:"link"`
	Actions    []notify.Action `json:"actions"`
	BatchCount int             `json:"batchCount"`
	CreatedAt  time.Time       `json:"createdAt"`
	UpdatedAt  time.Time       `json:"updatedAt"`
	Read       bool            `json:"read"`
	Archived   bool            `json:"archived"`
	Waiting    bool            `json:"waiting"` // an open task, counted until resolved
	Resolution *resolutionJSON `json:"resolution,omitempty"`
}

type resolutionJSON struct {
	Outcome string    `json:"outcome"` // approved, rejected, deleted, kept, withdrawn
	By      string    `json:"by"`
	ByMe    bool      `json:"byMe"`
	At      time.Time `json:"at"`
}

func toNotificationJSON(n *models.Notification, me uint, names publicNames) notificationJSON {
	out := notificationJSON{
		ID: n.ID, Kind: n.Kind, SubjectID: n.SubjectID, Category: n.Category, Title: n.Title, Body: n.Body, Link: n.Link,
		Actions: []notify.Action{}, BatchCount: n.BatchCount, CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt,
		Read: n.ReadAt != nil, Archived: n.ArchivedAt != nil || (n.ExpiresAt != nil && !n.ExpiresAt.After(time.Now())),
		Waiting: n.Waiting && n.ResolvedAt == nil,
	}
	if out.Waiting {
		_ = json.Unmarshal([]byte(n.Actions), &out.Actions)
	}
	if n.ResolvedAt != nil {
		out.Resolution = &resolutionJSON{Outcome: n.Resolution, By: names.actorName(n.ResolvedByID),
			ByMe: n.ResolvedByID != nil && *n.ResolvedByID == me, At: *n.ResolvedAt}
	}
	return out
}

// GET /notifications/badge -> {unread, waiting}; 304 when unchanged.
func (a *App) apiNotificationBadge(c *gin.Context) {
	unread, waiting, err := notify.Badge(a.DB, apiUser(c).ID)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load notifications")
		return
	}
	etag := fmt.Sprintf(`"%d-%d"`, unread, waiting)
	c.Header("ETag", etag)
	if c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		return
	}
	c.JSON(http.StatusOK, gin.H{"unread": unread, "waiting": waiting})
}

// GET /notifications?filter=all|unread|waiting|archived&before=<cursor>&limit=20
func (a *App) apiNotifications(c *gin.Context) {
	me := apiUser(c).ID
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	page, ok, err := notify.Feed(a.DB, me, c.Query("filter"), c.Query("before"), limit)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load notifications")
		return
	}
	if !ok {
		jsonError(c, http.StatusBadRequest, "unknown filter or cursor")
		return
	}
	ids := []uint{}
	for _, rows := range [][]models.Notification{page.Waiting, page.Items} {
		for _, n := range rows {
			if n.ResolvedAt != nil && n.ResolvedByID != nil {
				ids = append(ids, *n.ResolvedByID)
			}
		}
	}
	names := publicNamesIn(a.DB, ids)
	conv := func(rows []models.Notification) []notificationJSON {
		out := make([]notificationJSON, 0, len(rows))
		for i := range rows {
			out = append(out, toNotificationJSON(&rows[i], me, names))
		}
		return out
	}
	c.JSON(http.StatusOK, gin.H{
		"waiting": conv(page.Waiting), "waitingTotal": page.WaitingTotal,
		"notifications": conv(page.Items), "next": page.Next,
	})
}

func (a *App) apiNotificationsSeen(c *gin.Context) {
	var body struct {
		IDs []uint `json:"ids"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || len(body.IDs) > 100 {
		jsonError(c, http.StatusBadRequest, "ids is a list of up to 100 notification ids")
		return
	}
	if err := notify.Seen(a.DB, apiUser(c).ID, body.IDs); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not update notifications")
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

// notificationToggle serves POST|DELETE /notifications/:id/read and /archive.
func (a *App) notificationToggle(set func(db *gorm.DB, userID, id uint, on bool) (bool, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
		me := apiUser(c).ID
		found, err := set(a.DB, me, uint(id), c.Request.Method == http.MethodPost)
		if err != nil {
			jsonError(c, http.StatusInternalServerError, "could not update notifications")
			return
		}
		if !found {
			jsonError(c, http.StatusNotFound, "notification not found")
			return
		}
		n, _ := notify.Get(a.DB, me, uint(id))
		ids := []uint{}
		if n.ResolvedAt != nil && n.ResolvedByID != nil {
			ids = append(ids, *n.ResolvedByID)
		}
		c.JSON(http.StatusOK, gin.H{"notification": toNotificationJSON(&n, me, publicNamesIn(a.DB, ids))})
	}
}

func (a *App) notificationBulk(do func(db *gorm.DB, userID uint) error) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := do(a.DB, apiUser(c).ID); err != nil {
			jsonError(c, http.StatusInternalServerError, "could not update notifications")
			return
		}
		c.JSON(http.StatusOK, gin.H{})
	}
}

func (a *App) registerNotificationRoutes(v1 *gin.RouterGroup) {
	n := v1.Group("/notifications", a.apiAuth())
	n.GET("", a.apiNotifications)
	n.GET("/badge", a.apiNotificationBadge)
	n.POST("/seen", a.apiNotificationsSeen)
	n.POST("/read-all", a.notificationBulk(notify.ReadAll))
	n.POST("/archive-read", a.notificationBulk(notify.ArchiveRead))
	n.POST("/:id/read", a.notificationToggle(notify.SetRead))
	n.DELETE("/:id/read", a.notificationToggle(notify.SetRead))
	n.POST("/:id/archive", a.notificationToggle(notify.SetArchived))
	n.DELETE("/:id/archive", a.notificationToggle(notify.SetArchived))
}

// fromNotification checks an inline action. A request that names one of
// the user's notifications must be for an allow-listed action on that
// kind, about this subject; anything else is refused (6.6, 6.13 #5).
// It reports false after answering.
func (a *App) fromNotification(c *gin.Context, id *uint, action string, subjectID uint) bool {
	if id == nil {
		return true
	}
	n, ok := notify.Get(a.DB, apiUser(c).ID, *id)
	if !ok {
		jsonError(c, http.StatusNotFound, "notification not found")
		return false
	}
	if !notify.Allows(n.Kind, action) || n.SubjectID == nil || *n.SubjectID != subjectID {
		jsonError(c, http.StatusForbidden, "that action can't be taken from a notification")
		return false
	}
	return true
}

// alreadyHandled answers 409 with who resolved the task, so the row can say
// "Already approved by Pat" instead of an error (6.4).
func (a *App) alreadyHandled(c *gin.Context, kind string, subjectID uint) {
	by, outcome, _ := notify.LastResolution(a.DB, kind, subjectID)
	c.JSON(http.StatusConflict, gin.H{
		"error": "This was already handled", "resolvedBy": a.actorName(by), "resolution": outcome,
	})
}

// ---------------------------------------------------------------------------
// Writers: called by domain code inside its transaction, next to the
// AuditLog row. Each decides what the recipient may see.
// ---------------------------------------------------------------------------

func adminIDs(tx *gorm.DB) []uint {
	ids, _ := activeAdminIDs(tx)
	return ids
}

// activeAdminIDs lists the active admins in id order, for callers that must
// fail when the read does.
func activeAdminIDs(tx *gorm.DB) ([]uint, error) {
	var ids []uint
	err := tx.Model(&models.User{}).Where("role = ? AND status = ?", models.RoleAdmin, models.UserActive).Order("id").Pluck("id", &ids).Error
	return ids, err
}

func leaderIDs(tx *gorm.DB, groupID uint) []uint {
	var ids []uint
	tx.Model(&models.Membership{}).Where("group_id = ? AND role = ? AND status = 'active'", groupID, models.MembershipLeader).
		Pluck("user_id", &ids)
	return ids
}

// publicNames keeps historical display names for one response. Like nameIn,
// it includes soft-deleted users and never falls back to an email address.
type publicNames map[uint]string

// Bound the IN expansion well below PostgreSQL's 65,535 bind parameters;
// account-request history can supply an unbounded number of distinct people.
const publicNamesBatchSize = 1000

func publicNamesIn(tx *gorm.DB, ids []uint) publicNames {
	names := publicNames{}
	unique := make([]uint, 0, len(ids))
	for _, id := range ids {
		if _, ok := names[id]; !ok {
			names[id] = "Someone"
			unique = append(unique, id)
		}
	}
	for start := 0; start < len(unique); start += publicNamesBatchSize {
		batch := unique[start:min(start+publicNamesBatchSize, len(unique))]
		var users []models.User
		if err := tx.Unscoped().Select("id", "name", "display_name").Where("id IN ?", batch).Find(&users).Error; err != nil {
			// Preserve the historical all-fallback behavior on read failure;
			// never publish names determined by which batch failed.
			for id := range names {
				names[id] = "Someone"
			}
			return names
		}
		for _, u := range users {
			names[u.ID] = u.PublicName()
		}
	}
	return names
}

func (names publicNames) name(id uint) string {
	if name, ok := names[id]; ok {
		return name
	}
	return "Someone"
}

func (names publicNames) actorName(id *uint) string {
	if id == nil {
		return "Fold"
	}
	return names.name(*id)
}

func nameIn(tx *gorm.DB, id uint) string {
	var u models.User
	if err := tx.Unscoped().First(&u, id).Error; err != nil {
		return "Someone"
	}
	return u.PublicName()
}

// userIn loads a recipient for a visibility check inside tx.
func userIn(tx *gorm.DB, id uint) *models.User {
	var u models.User
	if tx.First(&u, id).Error != nil {
		return nil
	}
	return &u
}

// canSeeGroupIn is canSeeGroup against tx, so it sees the transaction's
// own changes (a membership just removed, a visibility just changed).
func canSeeGroupIn(tx *gorm.DB, u *models.User, g *models.Group) bool {
	if u == nil || g == nil {
		return false
	}
	return policy.CanSeeGroup(asPerson(u), asGroup(g), relIn(tx, u.ID, g.ID))
}

// noticeFacts is everything eventNoticeVars needs for a batch of
// recipients, loaded once with IN queries (issue #76, the relsFor
// pattern in api_event_access.go) instead of re-read per person.
// viewers holds each recipient as a signed-in viewer: a
// guest, pending-consent or missing account is never a signed-in viewer,
// so a private group's name, place or link cannot be granted by its presence
// in the recipient list.
type noticeFacts struct {
	groups  map[uint]*models.Group
	viewers map[uint]*models.User
	rels    map[[2]uint]policy.Rel // by {userID, groupID}
	zones   map[uint]*time.Location
	church  *time.Location
}

// loadNoticeFacts loads the groups, recipients, their memberships in
// those groups and their zones for a batch of event notices, plus the
// church zone once when anyone needs it. Like groupsByID and relsFor,
// a failed load leaves the fact absent, the same answer the per-person
// First calls gave.
func loadNoticeFacts(tx *gorm.DB, groupIDs, userIDs []uint) *noticeFacts {
	f := &noticeFacts{
		groups:  map[uint]*models.Group{},
		viewers: map[uint]*models.User{},
		rels:    map[[2]uint]policy.Rel{},
		zones:   map[uint]*time.Location{},
		church:  time.UTC,
	}
	if len(groupIDs) > 0 {
		var gs []models.Group
		tx.Where("id IN ?", groupIDs).Find(&gs)
		for i := range gs {
			f.groups[gs[i].ID] = &gs[i]
		}
	}
	var users []models.User
	if len(userIDs) > 0 {
		tx.Where("id IN ?", userIDs).Find(&users)
	}
	loaded := make(map[uint]bool, len(users))
	for i := range users {
		loaded[users[i].ID] = true
	}
	needChurch := false
	for _, id := range userIDs {
		if !loaded[id] {
			needChurch = true
		}
	}
	for i := range users {
		if users[i].Status == models.UserActive {
			f.viewers[users[i].ID] = &users[i]
		}
		if loc, err := events.LoadZone(users[i].Timezone); err == nil {
			f.zones[users[i].ID] = loc
		} else {
			needChurch = true
		}
	}
	if needChurch {
		f.church = churchZone(tx)
		for i := range users {
			if _, ok := f.zones[users[i].ID]; !ok {
				f.zones[users[i].ID] = f.church
			}
		}
	}
	if len(groupIDs) > 0 && len(userIDs) > 0 {
		var ms []models.Membership
		tx.Where("user_id IN ? AND group_id IN ?", userIDs, groupIDs).Find(&ms)
		for _, m := range ms {
			f.rels[[2]uint{m.UserID, m.GroupID}] = membershipRel(m)
		}
	}
	return f
}

// canSeeGroup is canSeeGroupIn against the preloaded facts: the same
// policy call, with the membership relIn would have loaded.
func (f *noticeFacts) canSeeGroup(userID uint, g *models.Group) bool {
	u := f.viewers[userID]
	if u == nil || g == nil {
		return false
	}
	return policy.CanSeeGroup(asPerson(u), asGroup(g), f.rels[[2]uint{userID, g.ID}])
}

// zone is the preloaded recipientZone for userID: their own zone, else
// the church's, else UTC.
func (f *noticeFacts) zone(userID uint) *time.Location {
	if loc, ok := f.zones[userID]; ok && loc != nil {
		return loc
	}
	if f.church != nil {
		return f.church
	}
	return time.UTC
}

const approvalsLink = "/admin?tab=approvals"

// notifyGroupPending asks every admin to approve a new group (a task).
func notifyGroupPending(tx *gorm.DB, g *models.Group, actor uint) ([]uint, error) {
	return notify.Send(tx, notify.Msg{
		Kind: notify.GroupPending, To: adminIDs(tx), Actor: &actor,
		SubjectType: "group", SubjectID: g.ID, Link: approvalsLink,
		Vars:      notify.Vars{Actor: nameIn(tx, actor), Group: g.Name}, // group-ok: admin tasks go to admins
		Data:      map[string]any{"groupId": g.ID},
		DedupeKey: fmt.Sprintf("group.pending:%d", g.ID),
	})
}

// notifyGroupDecided tells a pending group's leaders how an admin decided
// it (DEC-96), in the decision's transaction. A leader may always see their
// own group, approved or not.
func notifyGroupDecided(tx *gorm.DB, g *models.Group, actor uint, approved bool, note string) ([]uint, error) {
	kind := notify.GroupRejected
	if approved {
		kind = notify.GroupApproved
	}
	return notify.Send(tx, notify.Msg{
		Kind: kind, To: leaderIDs(tx, g.ID), Actor: &actor,
		SubjectType: "group", SubjectID: g.ID, Link: "/groups/" + strconv.FormatUint(uint64(g.ID), 10),
		Vars: notify.Vars{Group: g.Name, Note: note}, // group-ok: its own leaders
		// No dedupe key: the pending → decided transition sends it once, and a
		// group set back to pending and decided again earns a new notice.
	})
}

// notifyAccountRequested asks every admin to decide an account
// request (a task, DEC-4). Only a clean adult request carries the
// inline Approve (DEC-44); teens and flagged requests are Review-only.
// The row names the requester for admins but never their contact
// details, and the email variant names nobody at all.
func notifyAccountRequested(tx *gorm.DB, req *models.AccountRequest, flags models.RequestFlags) ([]uint, error) {
	source := "Self-signup"
	if req.Source == models.RequestGuestUpgrade {
		source = "Guest asking for an account"
	}
	var actor *uint
	if req.Source == models.RequestHouseholdConversion && req.RequestedBy != nil {
		source = "Household account invited by " + nameIn(tx, *req.RequestedBy)
		actor = req.RequestedBy
	}
	if req.Source == models.RequestMemberInvite && req.RequestedBy != nil {
		source = "Invited by " + nameIn(tx, *req.RequestedBy)
		actor = req.RequestedBy
	}
	eligible := policy.InlineApproveAccountOK(req.AccountType, flags.HasWarnings())
	return notify.Send(tx, notify.Msg{
		Kind: notify.AccountRequested, To: adminIDs(tx), Actor: actor,
		SubjectType: "account_request", SubjectID: req.ID, Link: approvalsLink,
		Vars:            notify.Vars{Person: req.Name, Change: source, Teen: req.AccountType == models.AccountTeen},
		Data:            map[string]any{"requestId": req.ID},
		DedupeKey:       fmt.Sprintf("account.requested:%d", req.ID),
		SuppressActions: !eligible,
	})
}

// notifyGuardianLinkPending asks every admin to confirm a guardian link
// to a child or teen household person (a task, DEC-32, M1.9). The rows
// go to admins only, so they name the guardian and the person; the email
// variant names nobody. Resolved on confirm and reject (and withdrawn
// when the person is removed before a decision).
func notifyGuardianLinkPending(tx *gorm.DB, link *models.GuardianLink, person *models.HouseholdPerson) ([]uint, error) {
	vars := notify.Vars{Actor: nameIn(tx, link.GuardianUserID)}
	data := map[string]any{"guardianLinkId": link.ID}
	if person != nil {
		vars.Person, vars.Change, vars.Teen = person.FullName(), person.Kind, person.Kind == models.HouseholdTeen
		data["personId"] = person.ID
	} else if link.TeenUserID != nil {
		// M1.10 uses account-targeted guardian links for parent consent.
		vars.Person, vars.Change, vars.Teen = nameIn(tx, *link.TeenUserID), "teen_account", true
		data["teenUserId"] = *link.TeenUserID
	}
	return notify.Send(tx, notify.Msg{
		Kind: notify.HouseholdLinkPending, To: adminIDs(tx), Actor: &link.GuardianUserID,
		SubjectType: "guardian_link", SubjectID: link.ID, Link: approvalsLink,
		Vars:      vars,
		Data:      data,
		DedupeKey: fmt.Sprintf("household.link_pending:%d", link.ID),
	})
}

// notifyDeletionRequested asks every admin to delete or keep a group.
func notifyDeletionRequested(tx *gorm.DB, g *models.Group, actor uint) ([]uint, error) {
	return notify.Send(tx, notify.Msg{
		Kind: notify.GroupDeletion, To: adminIDs(tx), Actor: &actor,
		SubjectType: "group", SubjectID: g.ID, Link: approvalsLink,
		Vars: notify.Vars{Actor: nameIn(tx, actor), Group: g.Name}, // group-ok: admin tasks go to admins
		Data: map[string]any{"groupId": g.ID},
	})
}

// notifyLeadership tells the other admins about a leadership change, from
// the RoleChanged or MemberRemoved entry just written (DEC-31).
func notifyLeadership(tx *gorm.DB, e audit.Entry) ([]uint, error) {
	var userID uint
	switch v := e.Meta["userId"].(type) {
	case uint:
		userID = v
	case int:
		userID = uint(v)
	}
	oldRole, _ := e.Meta["oldRole"].(string)
	newRole, _ := e.Meta["newRole"].(string)
	var change string
	switch {
	case e.Action == audit.MemberRemoved:
		if oldRole != models.MembershipLeader {
			return nil, nil // removing a plain member isn't a leadership change
		}
		change = "removed_from_group"
	case newRole == models.MembershipLeader:
		change = "added"
	case e.Actor != nil && *e.Actor == userID:
		change = "stepped_down"
	default:
		change = "removed"
	}
	var g models.Group
	tx.Unscoped().First(&g, e.TargetID)
	actor := "Fold"
	if e.Actor != nil {
		actor = nameIn(tx, *e.Actor)
	}
	return notify.Send(tx, notify.Msg{
		Kind: notify.LeadershipChanged, To: adminIDs(tx), Actor: e.Actor,
		SubjectType: "group", SubjectID: g.ID, Link: fmt.Sprintf("/groups/%d", g.ID),
		Vars: notify.Vars{Actor: actor, Person: nameIn(tx, userID), Group: g.Name, Change: change}, // group-ok: leadership notices go to admins
		Data: map[string]any{"groupId": g.ID, "userId": userID},
	})
}

// notifyNotRestored tells a re-enabled person they were left out of a full
// group (DEC-47). Their membership is already gone in tx, so an
// invite-only group's name and link are left out.
func notifyNotRestored(tx *gorm.DB, actor *uint, g *models.Group, userID uint) ([]uint, error) {
	vars, link := notify.Vars{}, "/"
	if canSeeGroupIn(tx, userIn(tx, userID), g) {
		vars.Group, link = g.Name, fmt.Sprintf("/groups/%d", g.ID) // group-ok: only when canSeeGroupIn says the person may see it
	}
	return notify.Send(tx, notify.Msg{
		Kind: notify.MemberNotRestored, To: []uint{userID}, Actor: actor,
		SubjectType: "group", SubjectID: g.ID, Link: link, Vars: vars,
	})
}

// notifyOccurrencesCancelled tells everyone going or waitlisted on
// these dates (M1.5: maybe no longer gets one). A registrant who can't
// see the group gets no group name, place, or link (PR #41). Each item
// archives itself when the date would have ended.
func notifyOccurrencesCancelled(tx *gorm.DB, actor uint, occIDs []uint) ([]uint, error) {
	return notifyOccurrences(tx, actor, occIDs, notify.OccurrenceCancelled)
}

// notifyOccurrencesRestored tells the same people, by the same privacy
// rules, that dates a series edit had cancelled are back on. Their
// registrations were never touched by the cancellation, so the notice says
// the sign-up is still there rather than asking them to register again.
func notifyOccurrencesRestored(tx *gorm.DB, actor uint, occIDs []uint) ([]uint, error) {
	return notifyOccurrences(tx, actor, occIDs, notify.OccurrenceRestored)
}

// notifyOccurrences is the shared fan-out of the cancelled and restored
// notices: one stored item per person per date.
func notifyOccurrences(tx *gorm.DB, actor uint, occIDs []uint, kind string) ([]uint, error) {
	if len(occIDs) == 0 {
		return nil, nil
	}
	var rows []struct {
		UserID uint
		OccID  uint
	}
	if err := tx.Table("registrations").Joins(listedRegistrations()).Distinct("users.id AS user_id, registrations.occurrence_id AS occ_id").
		Where("registrations.occurrence_id IN ? AND registrations.status IN ?", occIDs,
			[]string{models.RegGoing, models.RegWaitlisted}).
		Order("occ_id, user_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	// Load the occurrences, their events and every recipient's facts
	// once (issue #76); the loop below only writes notices.
	var occRows []models.EventOccurrence
	tx.Where("id IN ?", occIDs).Find(&occRows)
	occByID := make(map[uint]*models.EventOccurrence, len(occRows))
	eventIDs := make([]uint, 0, len(occRows))
	for i := range occRows {
		occByID[occRows[i].ID] = &occRows[i]
		eventIDs = append(eventIDs, occRows[i].EventID)
	}
	evByID := map[uint]*models.Event{}
	var groupIDs []uint
	if len(eventIDs) > 0 {
		var evRows []models.Event
		tx.Where("id IN ?", eventIDs).Find(&evRows)
		for i := range evRows {
			evByID[evRows[i].ID] = &evRows[i]
			if evRows[i].GroupID != nil {
				groupIDs = append(groupIDs, *evRows[i].GroupID)
			}
		}
	}
	userIDs := make([]uint, 0, len(rows))
	for _, r := range rows {
		userIDs = append(userIDs, r.UserID)
	}
	facts := loadNoticeFacts(tx, groupIDs, userIDs)
	accesses, err := occurrenceNoticeAccesses(tx, occIDs)
	if err != nil {
		return nil, err
	}
	var ids []uint
	for _, r := range rows {
		o := occByID[r.OccID]
		if o == nil {
			continue
		}
		ev := evByID[o.EventID]
		if ev == nil {
			continue
		}
		if _, ok := accesses[o.ID][r.UserID]; ok {
			// Lifecycle reconciliation owns the single invitation-ended notice.
			// Ordinary registrants still receive the cancellation notice below.
			continue
		}
		vars, link := eventNoticeVars(ev, o, r.UserID, facts)
		end := o.EndsAt
		if end.IsZero() {
			end = o.StartsAt
		}
		sent, err := notify.Send(tx, notify.Msg{
			Kind: kind, To: []uint{r.UserID}, Actor: &actor,
			SubjectType: "occurrence", SubjectID: o.ID, Link: link, Vars: vars,
			Data: map[string]any{"eventId": ev.ID, "date": o.OriginalDate},
			// A date can be cancelled, restored and cancelled again, so the key
			// carries the moment of this change (the row was just updated).
			DedupeKey: fmt.Sprintf("%s:%d:%d", kind, o.ID, o.UpdatedAt.UnixNano()), ExpiresAt: &end,
		})
		if err != nil {
			return nil, err
		}
		ids = append(ids, sent...)
	}
	return ids, nil
}

// notifyRegistrationCancelled tells someone who can no longer see an event
// that their registrations were cancelled. It names neither the group nor
// the place and links nowhere private (PR #41).
func notifyRegistrationCancelled(tx *gorm.DB, actor uint, ev *models.Event, userID uint) ([]uint, error) {
	u := userIn(tx, userID)
	if u == nil || u.Status == models.UserDisabled {
		return nil, nil
	}
	return notify.Send(tx, notify.Msg{
		Kind: notify.RegistrationCancelled, To: []uint{userID}, Actor: &actor,
		SubjectType: "event", SubjectID: ev.ID, Link: "/", Vars: notify.Vars{Event: ev.Title},
	})
}

// notifyVisibilityOverride tells the group's other leaders and the admins
// that an event is more open than its group (DEC-17, DEC-28).
func notifyVisibilityOverride(tx *gorm.DB, actor uint, ev *models.Event, g *models.Group, newVis, note string) ([]uint, error) {
	return notify.Send(tx, notify.Msg{
		Kind: notify.VisibilityOverride, To: append(leaderIDs(tx, g.ID), adminIDs(tx)...), Actor: &actor,
		SubjectType: "event", SubjectID: ev.ID, Link: fmt.Sprintf("/groups/%d", g.ID),
		Vars: notify.Vars{Actor: nameIn(tx, actor), Event: firstNonEmpty(ev.Title, g.Name), // group-ok: leaders and admins only
			Visibility: visWord(newVis), GroupVis: visWord(g.Visibility), Note: note},
		Data: map[string]any{"eventId": ev.ID, "groupId": g.ID},
	})
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// recipientZone is the zone a person reads emailed and notified times in
// (DEC-58): their own, else the church's, else UTC.
func recipientZone(tx *gorm.DB, userID uint) *time.Location {
	var zones []string
	tx.Model(&models.User{}).Where("id = ?", userID).Pluck("timezone", &zones)
	if len(zones) == 1 {
		return zoneOrChurch(tx, zones[0])
	}
	return zoneOrChurch(tx, "")
}

// zoneOrChurch is tz when it names a zone, else the church's, else UTC.
func zoneOrChurch(tx *gorm.DB, tz string) *time.Location {
	if loc, err := events.LoadZone(tz); err == nil {
		return loc
	}
	return churchZone(tx)
}

// churchZone is the church's zone from its setting, else UTC.
// churchZoneErr is churchZone, reporting a failed read instead of guessing
// UTC. An unset or unknown zone is UTC, as there.
func churchZoneErr(tx *gorm.DB) (*time.Location, error) {
	var s models.Setting
	err := tx.Where("key = ?", settingTimezone).First(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return time.UTC, nil
	}
	if err != nil {
		return nil, err
	}
	if loc, err := events.LoadZone(s.Value); err == nil {
		return loc, nil
	}
	return time.UTC, nil
}

func churchZone(tx *gorm.DB) *time.Location {
	var s models.Setting
	if tx.Where("key = ?", settingTimezone).First(&s).Error == nil {
		if loc, err := events.LoadZone(s.Value); err == nil {
			return loc
		}
	}
	return time.UTC
}

// formatWhen is how emails and notices write an event time, in the
// reader's zone with its abbreviation: "Sat, Oct 17 · 8:00 PM MDT".
func formatWhen(t time.Time, loc *time.Location) string {
	return t.In(loc).Format("Mon, Jan 2 · 3:04 PM MST")
}

func visWord(v string) string {
	switch v {
	case models.VisibilityPublic:
		return "public"
	case models.VisibilityPrivate:
		return "private"
	case models.VisibilityInviteOnly:
		return "invite-only"
	default:
		return "closed"
	}
}

func (a *App) nameOf(id uint) string { return nameIn(a.DB, id) }

func (a *App) actorName(id *uint) string {
	if id == nil {
		return "Fold"
	}
	return a.nameOf(*id)
}

// ---------------------------------------------------------------------------
// Email delivery (5.12, M1.2). Kinds marked AllowEmail send a transactional
// email through the minimal sender right after the writing transaction
// commits, with no delay. Each send is recorded in
// notification_deliveries. Sensitive kinds use the link-only RenderEmailSafe
// variant, which names nobody. The 15-minute "if not seen" escalation waits
// for the jobs table (M3.1). Delivery failures are logged, never returned:
// the notification row is already committed.
// ---------------------------------------------------------------------------

// deliverNotificationEmails sends email for the AllowEmail kinds among ids.
// Call it after the writing transaction committed, never inside it.
func (a *App) deliverNotificationEmails(ids []uint) {
	if len(ids) == 0 || a.Mailer == nil {
		return
	}
	ctx := context.Background()
	// Two reads for the whole batch, not two per recipient (#76 follow-up):
	// the rows, then their recipients. A row missing here was rolled back
	// or pruned after commit; there is nothing to send for it.
	var rows []models.Notification
	if err := a.DB.Where("id IN ?", ids).Order("id").Find(&rows).Error; err != nil {
		log.Printf("notification email: loading %d notifications: %v", len(ids), err)
		return
	}
	userIDs := make([]uint, 0, len(rows))
	for _, n := range rows {
		if k, ok := notify.Lookup(n.Kind); ok && k.AllowEmail {
			userIDs = append(userIDs, n.UserID)
		}
	}
	if len(userIDs) == 0 {
		return
	}
	// Each recipient comes with the email consent scopes they opted out of
	// (DEC-97), in the same read, so a batch stays at two reads.
	var users []emailRecipient
	if err := a.DB.Model(&models.User{}).Select(`users.*, COALESCE((
		SELECT string_agg(latest.scope, ',') FROM (
			SELECT DISTINCT ON (c.scope) c.scope, c.status FROM consents c
			WHERE c.user_id = users.id AND c.channel = ? ORDER BY c.scope, c.at DESC, c.id DESC
		) latest WHERE latest.status = ?), '') AS email_opt_outs`, models.ConsentEmail, models.ConsentOptedOut).
		Where("users.id IN ?", userIDs).Find(&users).Error; err != nil {
		// Mark nothing suppressed: without a delivery record the rows stay
		// pending, so a later delivery pass can still send them.
		log.Printf("notification email: loading %d recipients: %v", len(userIDs), err)
		return
	}
	byID := make(map[uint]*models.User, len(users))
	optOuts := make(map[uint]map[string]bool, len(users))
	for i := range users {
		byID[users[i].ID] = &users[i].User
		for _, scope := range strings.Split(users[i].EmailOptOuts, ",") {
			if scope == "" {
				continue
			}
			if optOuts[users[i].ID] == nil {
				optOuts[users[i].ID] = map[string]bool{}
			}
			optOuts[users[i].ID][scope] = true
		}
	}
	for i := range rows {
		a.deliverNotificationEmail(ctx, &rows[i], byID[rows[i].UserID], optOuts[rows[i].UserID])
	}
}

// emailRecipient is a notification email recipient with the comma-joined
// email consent scopes whose latest record opts out.
type emailRecipient struct {
	models.User
	EmailOptOuts string
}

// emailOptOuts maps each user to the email consent scopes whose latest
// record opts out (5.12, DEC-97). Delivery reads the same thing alongside
// each recipient's account (deliverNotificationEmails).
func emailOptOuts(db *gorm.DB, userIDs []uint) (map[uint]map[string]bool, error) {
	var rows []models.Consent
	if err := db.Raw(`SELECT DISTINCT ON (user_id, scope) user_id, scope, status FROM consents
		WHERE user_id IN ? AND channel = ? ORDER BY user_id, scope, at DESC, id DESC`, userIDs, models.ConsentEmail).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[uint]map[string]bool)
	for _, r := range rows {
		if r.Status != models.ConsentOptedOut {
			continue
		}
		if out[r.UserID] == nil {
			out[r.UserID] = map[string]bool{}
		}
		out[r.UserID][r.Scope] = true
	}
	return out, nil
}

func categoryScope(category string) string { return "category:" + category }

// deliverNotificationEmail sends one row to its preloaded recipient (nil
// when the account is gone). optedOut holds the recipient's email consent
// scopes that opt out; only optional kinds consult it (DEC-97).
func (a *App) deliverNotificationEmail(ctx context.Context, n *models.Notification, u *models.User, optedOut map[string]bool) {
	k, ok := notify.Lookup(n.Kind)
	if !ok || !k.AllowEmail {
		return
	}
	if !k.Required && optedOut[categoryScope(k.Category)] {
		a.recordDelivery(n.ID, n.UserID, models.DeliverySuppressed, "optional email turned off", "")
		return
	}
	if u == nil ||
		(u.Status != models.UserActive && u.Status != models.UserGuest) {
		a.recordDelivery(n.ID, n.UserID, models.DeliverySuppressed, "recipient not active", "")
		return
	}
	text := n.EmailTitle
	if n.EmailBody != "" {
		text += "\n\n" + n.EmailBody
	}
	text += "\n\n" + a.emailLink(n.Link)
	// A spam complaint stops optional mail, not required notices (DEC-112).
	msg := mail.Message{To: u.Email, Subject: n.EmailTitle, Text: text, Optional: !k.Required}
	// The provider's id for the message lets its delivery and bounce reports
	// find this row (M3.2).
	var providerID string
	var err error
	if tracked, ok := a.Mailer.(providers.EmailSender); ok {
		providerID, err = tracked.SendEmail(ctx, msg)
	} else {
		err = a.Mailer.Send(ctx, msg)
	}
	if mail.IsSuppressed(err) {
		a.recordDelivery(n.ID, u.ID, models.DeliverySuppressed, strings.TrimPrefix(err.Error(), "mail: "), "")
		return
	}
	if err != nil {
		log.Printf("notification email for notification %d: %v", n.ID, err)
		a.recordDelivery(n.ID, u.ID, models.DeliveryFailed, err.Error(), "")
		return
	}
	a.recordDelivery(n.ID, u.ID, models.DeliverySent, "", providerID)
}

// recordDelivery writes one notification_deliveries row. The unique
// (notification_id, channel) keeps a second delivery of the same row from
// duplicating the record.
func (a *App) recordDelivery(notificationID, userID uint, status, reason, providerID string) {
	var sentAt *time.Time
	var pid *string
	if providerID != "" {
		pid = &providerID
	}
	if status == models.DeliverySent {
		now := time.Now().UTC()
		sentAt = &now
	}
	_ = a.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "notification_id"}, {Name: "channel"}},
		DoNothing: true,
	}).Create(&models.NotificationDelivery{
		NotificationID: notificationID, Channel: models.DeliveryEmail,
		UserID: userID, Status: status, Reason: reason, SentAt: sentAt, ProviderMessageID: pid,
	}).Error
}

// emailBaseURL is the absolute base for links in transactional email:
// SITE_URL in prod, localhost in dev.
func (a *App) emailBaseURL() string {
	if a.Config.SiteURL != "" {
		return strings.TrimSuffix(a.Config.SiteURL, "/")
	}
	return "http://localhost:" + a.Config.Port
}

// emailLink makes an absolute URL from a notification's internal path.
func (a *App) emailLink(path string) string {
	return a.emailBaseURL() + path
}

// notificationSettingsJSON is the Me > Notifications payload. Today it holds
// the single DEC-97 switch; M3.12 adds per-category Email and Text switches.
type notificationSettingsJSON struct {
	OptionalEmailOff bool `json:"optionalEmailOff"`
}

// optionalEmailOff reports whether every optional category's latest email
// consent opts out, which is what the switch writes.
func optionalEmailOff(db *gorm.DB, userID uint) (bool, error) {
	optOuts, err := emailOptOuts(db, []uint{userID})
	if err != nil {
		return false, err
	}
	for _, c := range notify.OptionalEmailCategories() {
		if !optOuts[userID][categoryScope(c)] {
			return false, nil
		}
	}
	return true, nil
}

func (a *App) apiGetNotificationSettings(c *gin.Context) {
	off, err := optionalEmailOff(a.DB, apiUser(c).ID)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load notification settings")
		return
	}
	c.JSON(http.StatusOK, gin.H{"settings": notificationSettingsJSON{OptionalEmailOff: off}})
}

// apiUpdateNotificationSettings turns all optional email off or back on by
// appending one consent row per optional category whose state changes
// (DEC-97). Required email (account, safety, requests waiting on you) is
// never affected. The append-only consents rows, with their IP and user
// agent, are the record of the change (5.3), so no AuditLog row is added.
func (a *App) apiUpdateNotificationSettings(c *gin.Context) {
	var body struct {
		OptionalEmailOff *bool `json:"optionalEmailOff"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.OptionalEmailOff == nil {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return
	}
	me := apiUser(c)
	status := models.ConsentOptedIn
	if *body.OptionalEmailOff {
		status = models.ConsentOptedOut
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		// One writer per person at a time, so two quick toggles can't
		// interleave their rows.
		if err := tx.Exec("SELECT 1 FROM users WHERE id = ? FOR NO KEY UPDATE", me.ID).Error; err != nil {
			return err
		}
		optOuts, err := emailOptOuts(tx, []uint{me.ID})
		if err != nil {
			return err
		}
		for _, cat := range notify.OptionalEmailCategories() {
			if optOuts[me.ID][categoryScope(cat)] == *body.OptionalEmailOff {
				continue
			}
			if err := tx.Create(&models.Consent{
				UserID: me.ID, Channel: models.ConsentEmail, Scope: categoryScope(cat), Status: status,
				Source: models.ConsentSourceWebForm, IP: c.ClientIP(), UserAgent: clipRunes(c.Request.UserAgent(), 500),
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save notification settings")
		return
	}
	c.JSON(http.StatusOK, gin.H{"settings": notificationSettingsJSON{OptionalEmailOff: *body.OptionalEmailOff}})
}

// clipRunes keeps at most n runes of s (a request header we store).
func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
