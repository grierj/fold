package main

import (
	"net/http"

	"gorm.io/gorm"

	"fold/internal/policy"
	"fold/models"
)

// DEC-147: only an active group may have new events created or rooms booked,
// admins included. Existing events and bookings are kept, and releasing a
// room is never refused.
var (
	errGroupEventsNotApproved = apiErr(http.StatusConflict, "group_events_not_approved", "events can be scheduled once an admin approves this group")
	errGroupRoomsNotApproved  = apiErr(http.StatusConflict, "group_rooms_not_approved", "rooms can be booked once an admin approves this group")
)

// schedulingGroupSQL is the SQL form of groupSchedulesIn for an `events e`
// alias: church-wide, or a live active group. TestSchedulingGroupSQLMatchesPolicy
// keeps it equal to policy.GroupCanSchedule.
const schedulingGroupSQL = "(e.group_id IS NULL OR EXISTS (SELECT 1 FROM groups sg WHERE sg.id = e.group_id AND sg.deleted_at IS NULL AND sg.status = 'active'))"

// groupSchedulesIn reports whether new events or room bookings may be made
// for groupID (nil is a church-wide event, which has no group). A group that
// is deleted or missing cannot schedule. A failed read is an error, never an
// answer.
//
// This is a plain read in the writer's transaction, deliberately not FOR
// SHARE. A series write locks the group before the event, but booking paths
// already hold the event, and group tightening holds the group FOR UPDATE and
// then locks its events (lockGroupDates), so taking the group lock after the
// event could deadlock. It is also not needed: a status change never reads
// events or bookings, so a writer that read "active" serializes before a
// concurrent archive, the same as booking a moment earlier, and DEC-147 keeps
// those bookings.
func groupSchedulesIn(tx *gorm.DB, groupID *uint) (bool, error) {
	if groupID == nil {
		return true, nil
	}
	var statuses []string
	if err := tx.Model(&models.Group{}).Where("id = ?", *groupID).Limit(1).Pluck("status", &statuses).Error; err != nil {
		return false, err
	}
	if len(statuses) == 0 {
		return false, nil
	}
	return policy.GroupCanSchedule(statuses[0]), nil
}
