package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"fold/internal/audit"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// Notifications (derived, not stored)
// ---------------------------------------------------------------------------

type notificationJSON struct {
	// ID names this exact notice. It changes when the notice is about
	// something new (another pending group, a new audit row), so a notice
	// dismissed in the SPA never hides a later one with the same wording.
	ID    string `json:"id"`
	Type  string `json:"type"` // "approval" | "event" | "leadership"
	Title string `json:"title"`
	Body  string `json:"body"`
	Link  string `json:"link"`
}

// apiNotifications returns timely items for the current user: group
// approvals waiting (admins) and upcoming events in their groups.
// Nothing is persisted; the client tracks dismissed items locally.
func (a *App) apiNotifications(c *gin.Context) {
	u := apiUser(c)
	out := []notificationJSON{}

	if policy.IsAdmin(asPerson(u)) {
		if n, newest := groupQueue(a, "status = ?", models.GroupPending); n > 0 {
			title := fmt.Sprintf("%d groups need approval", n)
			if n == 1 {
				title = "1 group needs approval"
			}
			out = append(out, notificationJSON{
				ID:    fmt.Sprintf("approval:groups:%d:%d", newest, n),
				Type:  "approval",
				Title: title,
				Body:  "Review them in the admin panel.",
				Link:  "/admin?tab=approvals",
			})
		}
		if deletions, newest := groupQueue(a, "deletion_requested = ?", true); deletions > 0 {
			title := fmt.Sprintf("%d groups are waiting to be deleted", deletions)
			if deletions == 1 {
				title = "1 group is waiting to be deleted"
			}
			out = append(out, notificationJSON{
				ID:    fmt.Sprintf("approval:deletions:%d:%d", newest, deletions),
				Type:  "approval",
				Title: title,
				Body:  "A leader asked for the group to be removed.",
				Link:  "/admin?tab=approvals",
			})
		}
		out = append(out, a.leadershipNotices(u)...)
	}
	// Someone re-enabled into a full group hears they were left out (DEC-47).
	out = append(out, a.leftOutNotices(u)...)

	out = append(out, a.eventNotices(u)...)

	c.JSON(http.StatusOK, gin.H{"notifications": out})
}

// eventNotices lists upcoming occurrences in the member's groups, tells
// people who registered going or maybe when a date they were on is cancelled,
// and tells other leaders and admins when an event was opened past its group.
func (a *App) eventNotices(u *models.User) []notificationJSON {
	now := time.Now().UTC()
	out := a.cancelledNotices(u, now)
	var memberships []models.Membership
	a.DB.Where("user_id = ? AND status = ?", u.ID, "active").Find(&memberships)
	if len(memberships) > 0 {
		groupIDs := make([]uint, 0, len(memberships))
		for i := range memberships {
			groupIDs = append(groupIDs, memberships[i].GroupID)
		}
		out = append(out, a.upcomingNotices(groupIDs, now)...)
	}
	out = append(out, a.registrationCancelledNotices(u)...)
	return append(out, a.visibilityNotices(u)...)
}

type noticeRow struct {
	OccID         uint
	EventID       uint
	GroupID       *uint
	OriginalDate  string
	StartsAt      time.Time
	TZID          string
	Title         string
	Location      string
	GroupName     string
	GroupLocation string
}

func (a *App) cancelledNotices(u *models.User, now time.Time) []notificationJSON {
	userID := u.ID
	var rows []noticeRow
	a.DB.Table("registrations").
		Select(`event_occurrences.id as occ_id, events.id as event_id, events.group_id,
			event_occurrences.original_date, event_occurrences.starts_at, events.tzid,
			events.title, events.location, groups.name as group_name, groups.location as group_location`).
		Joins("JOIN event_occurrences ON event_occurrences.id = registrations.occurrence_id").
		Joins("JOIN events ON events.id = event_occurrences.event_id AND events.deleted_at IS NULL").
		Joins("LEFT JOIN groups ON groups.id = events.group_id AND groups.deleted_at IS NULL").
		Where("registrations.user_id = ? AND registrations.status IN ?", userID, []string{models.RegGoing, models.RegMaybe}).
		Where("event_occurrences.status = ? AND event_occurrences.starts_at > ?", models.OccurrenceCancelled, now.Add(-14*24*time.Hour)).
		Where("events.group_id IS NULL OR groups.id IS NOT NULL").
		Order("event_occurrences.starts_at").Limit(10).Scan(&rows)
	out := make([]notificationJSON, 0, len(rows))
	for _, r := range rows {
		// A registrant who can't see the group (an opened invite-only event,
		// or someone since removed) gets no group name, place, or link.
		if r.GroupID != nil {
			var g models.Group
			if err := a.DB.First(&g, *r.GroupID).Error; err != nil || !a.canSeeGroup(u, &g) {
				r.GroupID, r.GroupName, r.GroupLocation = nil, "", "" // group-ok: clearing, not reading
			}
		}
		out = append(out, notificationJSON{
			ID:    fmt.Sprintf("event-cancelled:%d", r.OccID),
			Type:  "event",
			Title: "Cancelled: " + noticeTitle(r),
			Body:  noticeWhen(r),
			Link:  noticeLink(r),
		})
	}
	return out
}

func (a *App) upcomingNotices(groupIDs []uint, now time.Time) []notificationJSON {
	var rows []noticeRow
	a.DB.Table("event_occurrences").
		Select(`event_occurrences.id as occ_id, events.id as event_id, events.group_id,
			event_occurrences.original_date, event_occurrences.starts_at, events.tzid,
			events.title, events.location, groups.name as group_name, groups.location as group_location`).
		Joins("JOIN events ON events.id = event_occurrences.event_id AND events.deleted_at IS NULL").
		Joins("JOIN groups ON groups.id = events.group_id AND groups.deleted_at IS NULL").
		Where("events.group_id IN ? AND event_occurrences.status = ?", groupIDs, models.OccurrenceScheduled).
		Where("event_occurrences.starts_at >= ? AND event_occurrences.starts_at < ?", now, now.AddDate(0, 0, 7)).
		Order("event_occurrences.starts_at").Limit(10).Scan(&rows)
	out := make([]notificationJSON, 0, len(rows))
	for _, r := range rows {
		title := r.GroupName // group-ok: upcomingNotices lists only the viewer's own groups
		if r.Title != "" {
			title = r.Title
		}
		out = append(out, notificationJSON{
			ID:    fmt.Sprintf("event:%d:%s", r.EventID, r.OriginalDate),
			Type:  "event",
			Title: title,
			Body:  noticeWhen(r),
			Link:  noticeLink(r),
		})
	}
	return out
}

// visibilityNotices is the derived copy of an event.visibility_override
// audit row (M1.1c stores it). Other leaders of that group, and admins,
// see it. The person who made the change does not. The id includes the
// audit row so dismissing one change does not hide the next.
func (a *App) visibilityNotices(me *models.User) []notificationJSON {
	var rows []models.AuditLog
	a.DB.Where("action = ? AND at > ? AND (actor_id IS NULL OR actor_id <> ?)",
		audit.VisibilityOverride, time.Now().Add(-14*24*time.Hour), me.ID).
		Order("at DESC").Limit(40).Find(&rows)
	out := []notificationJSON{}
	for _, r := range rows {
		if len(out) >= 20 {
			break
		}
		var ev models.Event
		if err := a.DB.First(&ev, r.TargetID).Error; err != nil || ev.GroupID == nil {
			continue
		}
		var g models.Group
		if err := a.DB.First(&g, *ev.GroupID).Error; err != nil {
			continue
		}
		if !policy.IsAdmin(asPerson(me)) && !a.relOf(me, g.ID).Leader {
			continue
		}
		var meta struct {
			NewVisibility   string `json:"newVisibility"`
			GroupVisibility string `json:"groupVisibility"`
			Reason          string `json:"reason"`
		}
		if json.Unmarshal([]byte(r.MetaJSON), &meta) != nil {
			continue
		}
		name := ev.Title
		if name == "" {
			name = g.Name // group-ok: visibility notices go only to the group's leaders and admins
		}
		body := a.actorName(r.ActorID) + " opened it up. Now " + visWord(meta.NewVisibility) + ". The group is " + visWord(meta.GroupVisibility) + "."
		if meta.Reason != "" {
			body += " " + meta.Reason
		}
		out = append(out, notificationJSON{
			ID:    fmt.Sprintf("visibility:%d", r.ID),
			Type:  "event",
			Title: "Event opened up: " + name,
			Body:  body,
			Link:  fmt.Sprintf("/groups/%d", g.ID),
		})
	}
	return out
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

func noticeLink(r noticeRow) string {
	if r.GroupID != nil && *r.GroupID != 0 {
		return fmt.Sprintf("/groups/%d", *r.GroupID)
	}
	return fmt.Sprintf("/events/%d", r.EventID)
}

func noticeTitle(r noticeRow) string {
	if r.Title != "" {
		return r.Title
	}
	if r.GroupName != "" { // group-ok: see below
		return r.GroupName // group-ok: cancelledNotices clears it when the group is hidden
	}
	return "Church event"
}

func noticeWhen(r noticeRow) string {
	loc := time.UTC
	if l, err := time.LoadLocation(r.TZID); err == nil {
		loc = l
	}
	body := r.StartsAt.In(loc).Format("Mon, Jan 2 · 3:04 PM")
	place := r.Location
	if place == "" {
		place = r.GroupLocation // group-ok: as above
	}
	if place != "" {
		body += " · " + place
	}
	return body
}

// leadershipNotices tells admins who was made or removed as a group leader
// in the last 14 days, by someone else (DEC-31: no admin gate, so admins
// hear about it and can undo a bad promotion). Read from the audit log.
func (a *App) leadershipNotices(me *models.User) []notificationJSON {
	var rows []models.AuditLog
	a.DB.Where("action IN ? AND at > ? AND (actor_id IS NULL OR actor_id <> ?)",
		[]string{audit.RoleChanged, audit.MemberRemoved}, time.Now().Add(-14*24*time.Hour), me.ID).
		Order("at DESC").Limit(40).Find(&rows)
	out := []notificationJSON{}
	for _, r := range rows {
		var meta struct {
			UserID  uint   `json:"userId"`
			OldRole string `json:"oldRole"`
			NewRole string `json:"newRole"`
		}
		if json.Unmarshal([]byte(r.MetaJSON), &meta) != nil {
			continue
		}
		if r.Action == audit.MemberRemoved && meta.OldRole != models.MembershipLeader {
			continue // removing a plain member isn't a leadership change
		}
		person := a.nameOf(meta.UserID)
		var title string
		switch {
		case r.Action == audit.MemberRemoved:
			title = fmt.Sprintf("%s removed %s (a leader) from the group", a.actorName(r.ActorID), person)
		case meta.NewRole == models.MembershipLeader:
			title = fmt.Sprintf("%s made %s a leader", a.actorName(r.ActorID), person)
		case r.ActorID != nil && *r.ActorID == meta.UserID:
			title = fmt.Sprintf("%s stepped down as a leader", person)
		default:
			title = fmt.Sprintf("%s removed %s as a leader", a.actorName(r.ActorID), person)
		}
		var g models.Group
		a.DB.Unscoped().First(&g, r.TargetID)
		out = append(out, notificationJSON{
			ID:    fmt.Sprintf("leadership:%d", r.ID),
			Type:  "leadership",
			Title: title,
			Body:  g.Name + " · " + r.At.Format("Jan 2"), // group-ok: leadership notices go to leaders and admins
			Link:  fmt.Sprintf("/groups/%d#role-change-%d", r.TargetID, r.ID),
		})
	}
	return out
}

func (a *App) nameOf(id uint) string {
	var u models.User
	if err := a.DB.Unscoped().First(&u, id).Error; err != nil {
		return "Someone"
	}
	return u.PublicName()
}

func (a *App) actorName(id *uint) string {
	if id == nil {
		return "Fold"
	}
	return a.nameOf(*id)
}

// groupQueue counts the groups matching where and returns the newest id,
// which makes a queue notice's ID change whenever a new group joins it.
func groupQueue(a *App, where string, arg any) (n int64, newest uint) {
	var row struct {
		N      int64
		Newest uint
	}
	a.DB.Model(&models.Group{}).Select("count(*) AS n, coalesce(max(id), 0) AS newest").
		Where(where, arg).Scan(&row)
	return row.N, row.Newest
}
