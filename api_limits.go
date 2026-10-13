package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"fold/internal/audit"
	"fold/models"
)

// Group member limits (DEC-39). A group may cap its active memberships
// (a class in a room); nil means unlimited. Self-join is refused when full,
// and there is no waitlist. Leaders and admins go over only by explicit
// choice (overLimit), which is audited. A re-enabled account whose group is
// full is left out of it and told to ask a leader (DEC-47); a leader can
// then use the audited over-limit add.

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

// leftOutNotices tells a re-enabled person which full groups they were
// left out of (DEC-47). Derived from membership.not_restored audit rows of
// the last 30 days; the id includes the row so a later one is new.
func (a *App) leftOutNotices(me *models.User) []notificationJSON {
	var rows []models.AuditLog
	a.DB.Where("action = ? AND at > ? AND meta_json->>'userId' = ?",
		audit.MemberNotRestored, time.Now().Add(-30*24*time.Hour), fmt.Sprint(me.ID)).
		Order("at DESC").Limit(20).Find(&rows)
	out := []notificationJSON{}
	for _, r := range rows {
		var g models.Group
		if a.DB.First(&g, r.TargetID).Error != nil || !a.canSeeGroup(me, &g) {
			continue
		}
		out = append(out, notificationJSON{
			ID:    fmt.Sprintf("not-restored:%d", r.ID),
			Type:  "leadership",
			Title: "You're not back in " + g.Name + " yet",
			Body:  "The group was full when your account was turned back on. Ask a leader to add you.",
			Link:  fmt.Sprintf("/groups/%d", g.ID),
		})
	}
	return out
}
