package policy

// Events and their dates. Adapted from Fold (internal/policy/policy.go at the
// pinned commit in docs/CARRYOVER.md: CanSeeEvent, CanManageEvent,
// GroupCanSchedule) with its table tests. Changes from Fold:
//   - A deleted event, or an event of a deleted group, is seen by nobody,
//     admins included (R29).
//   - Running an event needs its group to be active, as scheduling one does
//     (DEC-88, DEC-147). An archived group's events stay as they are until
//     it is active again.
//   - Church-wide events and their hosts (DEC-7), and opening an event wider
//     than its group (DEC-6, DEC-17, DEC-28), arrive in milestone 1b. Until
//     then an event may be set to its group's visibility or anything more
//     private.

const (
	EventActive  = "active"
	EventDeleted = "deleted"
)

// Event is what policy needs to know about an event.
type Event struct {
	Status     string
	Visibility string // the event's own, copied from its group when made (DEC-48)
}

// CanSeeEvent is the one visibility rule for an event and each of its dates.
// A waiting or archived group's events are its leaders' and admins' only;
// otherwise the event's own visibility decides, whatever its group's says
// now (DEC-48). Seeing an event does not reveal its group: pages ask
// CanSeeGroup before naming it. events.visibleSQL is this rule for lists,
// and a test holds the two together.
func CanSeeEvent(p *Person, e Event, g Group, r Rel) bool {
	if e.Status != EventActive || g.Status == GroupDeleted {
		return false
	}
	if IsAdmin(p) {
		return true
	}
	if g.Status != GroupActive {
		return p != nil && r.Leader
	}
	return visibleTo(e.Visibility, p, r.Member)
}

// GroupCanSchedule: only an active group has new events or changes to them;
// an admin approves or reactivates the group first (DEC-147).
func GroupCanSchedule(status string) bool { return status == GroupActive }

// CanScheduleEvent: a group's leaders and admins add events to an active
// group.
func CanScheduleEvent(p *Person, g Group, r Rel) bool {
	return CanManageGroup(p, g, r) && GroupCanSchedule(g.Status)
}

// CanManageEvent covers editing and deleting an event: the people who may
// schedule one in its group, while it is not deleted.
func CanManageEvent(p *Person, e Event, g Group, r Rel) bool {
	return e.Status == EventActive && CanScheduleEvent(p, g, r)
}

// CanSetEventVisibility: an event may become more private than it is, or
// take its group's visibility or anything more private than that. Making it
// more private removes nobody (R3). Opening it wider than its group is
// milestone 1b.
func CanSetEventVisibility(from, to, groupVisibility string) bool {
	return ValidVisibility(to) && (openness(to) <= openness(from) || openness(to) <= openness(groupVisibility))
}
