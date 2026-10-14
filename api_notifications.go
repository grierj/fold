package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/mail"
	"fold/internal/notify"
	"fold/internal/policy"
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

func (a *App) toNotificationJSON(n *models.Notification, me uint) notificationJSON {
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
		out.Resolution = &resolutionJSON{Outcome: n.Resolution, By: a.actorName(n.ResolvedByID),
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
	page, ok := notify.Feed(a.DB, me, c.Query("filter"), c.Query("before"), limit)
	if !ok {
		jsonError(c, http.StatusBadRequest, "unknown filter or cursor")
		return
	}
	conv := func(rows []models.Notification) []notificationJSON {
		out := make([]notificationJSON, 0, len(rows))
		for i := range rows {
			out = append(out, a.toNotificationJSON(&rows[i], me))
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
func (a *App) notificationToggle(set func(db *gorm.DB, userID, id uint, on bool) bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
		me := apiUser(c).ID
		if !set(a.DB, me, uint(id), c.Request.Method == http.MethodPost) {
			jsonError(c, http.StatusNotFound, "notification not found")
			return
		}
		n, _ := notify.Get(a.DB, me, uint(id))
		c.JSON(http.StatusOK, gin.H{"notification": a.toNotificationJSON(&n, me)})
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
	var ids []uint
	tx.Model(&models.User{}).Where("role = ? AND status = ?", models.RoleAdmin, models.UserActive).Pluck("id", &ids)
	return ids
}

func leaderIDs(tx *gorm.DB, groupID uint) []uint {
	var ids []uint
	tx.Model(&models.Membership{}).Where("group_id = ? AND role = ? AND status = 'active'", groupID, models.MembershipLeader).
		Pluck("user_id", &ids)
	return ids
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

// notifyAccountRequested asks every admin to decide an account
// request (a task, DEC-4). Only a clean adult request carries the
// inline Approve (DEC-44); teens and flagged requests are Review-only.
// The row names the requester for admins but never their contact
// details, and the email variant names nobody at all.
func notifyAccountRequested(tx *gorm.DB, req *models.AccountRequest, flags models.RequestFlags) ([]uint, error) {
	source := "Self-signup"
	var actor *uint
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
func notifyNotRestored(tx *gorm.DB, actor uint, g *models.Group, userID uint) ([]uint, error) {
	vars, link := notify.Vars{}, "/"
	if canSeeGroupIn(tx, userIn(tx, userID), g) {
		vars.Group, link = g.Name, fmt.Sprintf("/groups/%d", g.ID) // group-ok: only when canSeeGroupIn says the person may see it
	}
	return notify.Send(tx, notify.Msg{
		Kind: notify.MemberNotRestored, To: []uint{userID}, Actor: &actor,
		SubjectType: "group", SubjectID: g.ID, Link: link, Vars: vars,
	})
}

// notifyOccurrencesCancelled tells everyone going or maybe on these dates.
// A registrant who can't see the group gets no group name, place, or link
// (PR #41). Each item archives itself when the date would have ended.
func notifyOccurrencesCancelled(tx *gorm.DB, actor uint, occIDs []uint) ([]uint, error) {
	if len(occIDs) == 0 {
		return nil, nil
	}
	var rows []struct {
		UserID uint
		OccID  uint
	}
	if err := tx.Table("registrations").Select("user_id, occurrence_id AS occ_id").
		Where("occurrence_id IN ? AND status IN ?", occIDs, []string{models.RegGoing, models.RegMaybe}).
		Order("occurrence_id, user_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	var ids []uint
	for _, r := range rows {
		var o models.EventOccurrence
		var ev models.Event
		if tx.First(&o, r.OccID).Error != nil || tx.First(&ev, o.EventID).Error != nil {
			continue
		}
		var g *models.Group
		if ev.GroupID != nil {
			var row models.Group
			if tx.First(&row, *ev.GroupID).Error == nil && canSeeGroupIn(tx, userIn(tx, r.UserID), &row) {
				g = &row
			}
		}
		vars := notify.Vars{Event: ev.Title, When: localWhen(o.StartsAt, ev.TZID), Place: ev.Location}
		link := fmt.Sprintf("/events/%d", ev.ID)
		if g != nil {
			vars.Event = firstNonEmpty(ev.Title, g.Name)        // group-ok: g is set only when canSeeGroupIn is true
			vars.Place = firstNonEmpty(ev.Location, g.Location) // group-ok: g is set only when canSeeGroupIn is true
			link = fmt.Sprintf("/groups/%d", g.ID)
		}
		end := o.EndsAt
		if end.IsZero() {
			end = o.StartsAt
		}
		sent, err := notify.Send(tx, notify.Msg{
			Kind: notify.OccurrenceCancelled, To: []uint{r.UserID}, Actor: &actor,
			SubjectType: "occurrence", SubjectID: o.ID, Link: link, Vars: vars,
			Data:      map[string]any{"eventId": ev.ID, "date": o.OriginalDate},
			DedupeKey: fmt.Sprintf("event.cancelled:%d", o.ID), ExpiresAt: &end,
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

func localWhen(t time.Time, tzid string) string {
	loc := time.UTC
	if l, err := time.LoadLocation(tzid); err == nil {
		loc = l
	}
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

// startNotificationPruning runs the DEC-45 retention cleanup at startup and
// then daily.
func (a *App) startNotificationPruning(ctx context.Context) {
	go func() {
		run := func() {
			if n, err := notify.Prune(a.DB, time.Now().UTC()); err != nil {
				log.Printf("notifications: prune: %v", err)
			} else if n > 0 {
				log.Printf("notifications: pruned %d", n)
			}
		}
		run()
		tick := time.NewTicker(24 * time.Hour)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				run()
			}
		}
	}()
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
	for _, id := range ids {
		a.deliverNotificationEmail(ctx, id)
	}
}

func (a *App) deliverNotificationEmail(ctx context.Context, id uint) {
	var n models.Notification
	if err := a.DB.First(&n, id).Error; err != nil {
		return // rolled back or pruned after commit; nothing to send
	}
	k, ok := notify.Lookup(n.Kind)
	if !ok || !k.AllowEmail {
		return
	}
	var u models.User
	if err := a.DB.Where("id = ?", n.UserID).First(&u).Error; err != nil || u.Status != models.UserActive {
		a.recordDelivery(n.ID, n.UserID, models.DeliverySuppressed, "recipient not active")
		return
	}
	text := n.EmailTitle
	if n.EmailBody != "" {
		text += "\n\n" + n.EmailBody
	}
	text += "\n\n" + a.emailLink(n.Link)
	if err := a.Mailer.Send(ctx, mail.Message{To: u.Email, Subject: n.EmailTitle, Text: text}); err != nil {
		log.Printf("notification email for notification %d: %v", n.ID, err)
		a.recordDelivery(n.ID, u.ID, models.DeliveryFailed, err.Error())
		return
	}
	a.recordDelivery(n.ID, u.ID, models.DeliverySent, "")
}

// recordDelivery writes one notification_deliveries row. The unique
// (notification_id, channel) keeps a second delivery of the same row from
// duplicating the record.
func (a *App) recordDelivery(notificationID, userID uint, status, reason string) {
	var sentAt *time.Time
	if status == models.DeliverySent {
		now := time.Now().UTC()
		sentAt = &now
	}
	_ = a.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "notification_id"}, {Name: "channel"}},
		DoNothing: true,
	}).Create(&models.NotificationDelivery{
		NotificationID: notificationID, Channel: models.DeliveryEmail,
		UserID: userID, Status: status, Reason: reason, SentAt: sentAt,
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
