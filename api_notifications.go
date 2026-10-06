package main

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"church-groups/models"

	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// Notifications (derived, not stored)
// ---------------------------------------------------------------------------

type notificationJSON struct {
	Type  string `json:"type"` // "approval" | "event"
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

	if u.IsAdmin() {
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
