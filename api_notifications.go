package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
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
		var n int64
		a.DB.Model(&models.Group{}).Where("status = ?", models.GroupPending).Count(&n)
		if n > 0 {
			word := "groups"
			if n == 1 {
				word = "group"
			}
			out = append(out, notificationJSON{
				Type:  "approval",
				Title: fmt.Sprintf("%d %s need approval", n, word),
				Body:  "Review them in the admin panel.",
				Link:  "/admin?tab=approvals",
			})
		}
		var deletions int64
		a.DB.Model(&models.Group{}).Where("deletion_requested = ?", true).Count(&deletions)
		if deletions > 0 {
			title := fmt.Sprintf("%d groups are waiting to be deleted", deletions)
			if deletions == 1 {
				title = "1 group is waiting to be deleted"
			}
			out = append(out, notificationJSON{
				Type:  "approval",
				Title: title,
				Body:  "A leader asked for the group to be removed.",
				Link:  "/admin?tab=approvals",
			})
		}
		out = append(out, a.leadershipNotices(u)...)
	}

	var memberships []models.Membership
	a.DB.Where("user_id = ? AND status = ?", u.ID, "active").Find(&memberships)
	if len(memberships) > 0 {
		groupIDs := make([]uint, 0, len(memberships))
		for i := range memberships {
			groupIDs = append(groupIDs, memberships[i].GroupID)
		}
		var groups []models.Group
		a.DB.Where("id IN ?", groupIDs).Find(&groups)
		names := map[uint]string{}
		for i := range groups {
			names[groups[i].ID] = groups[i].Name
		}
		var meetings []models.Meeting
		a.DB.Where("group_id IN ?", groupIDs).Find(&meetings)
		now := time.Now().UTC()
		horizon := now.AddDate(0, 0, 7)
		type evt struct {
			at time.Time
			n  notificationJSON
		}
		var evts []evt
		for i := range meetings {
			m := &meetings[i]
			cancelled := a.cancelledDates(m.ID)
			for _, occ := range expandMeeting(m, now, horizon) {
				date := occ.UTC().Format("2006-01-02")
				if cancelled[date] {
					continue
				}
				body := occ.Format("Mon, Jan 2 · 3:04 PM")
				if m.Location != "" {
					body += " · " + m.Location
				}
				evts = append(evts, evt{at: occ, n: notificationJSON{
					Type:  "event",
					Title: names[m.GroupID],
					Body:  body,
					Link:  fmt.Sprintf("/groups/%d", m.GroupID),
				}})
			}
		}
		sort.Slice(evts, func(i, j int) bool { return evts[i].at.Before(evts[j].at) })
		for i := 0; i < len(evts) && i < 10; i++ {
			out = append(out, evts[i].n)
		}
	}

	c.JSON(http.StatusOK, gin.H{"notifications": out})
}

// leadershipNotices tells admins who was made or removed as a group leader
// in the last 14 days, by someone else (DEC-31: no admin gate, so admins
// hear about it and can undo a bad promotion). Read from the audit log.
func (a *App) leadershipNotices(me *models.User) []notificationJSON {
	var rows []models.AuditLog
	a.DB.Where("action = ? AND at > ? AND (actor_id IS NULL OR actor_id <> ?)",
		audit.RoleChanged, time.Now().Add(-14*24*time.Hour), me.ID).
		Order("at DESC").Limit(20).Find(&rows)
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
		person := a.nameOf(meta.UserID)
		var title string
		switch {
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
			Type:  "leadership",
			Title: title,
			Body:  g.Name + " · " + r.At.Format("Jan 2"),
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
