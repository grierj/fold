package policy

// Events and their dates. Adapted from Fold (internal/policy/policy.go at the
// pinned commit in docs/CARRYOVER.md: CanSeeEvent, CanManageEvent,
// CanSetEventVisibility, MoreOpen, GroupCanSchedule) with its table tests.
// Changes from Fold:
//   - A deleted event, or an event of a deleted group, is seen by nobody,
//     admins included (R29).
//   - Running a group's event needs its group to be active, as scheduling one
//     does (DEC-88, DEC-147). An archived group's events stay as they are
//     until it is active again.
//   - One date may be opened wider than its series, never narrower (P7);
//     Fold allowed both.

const (
	EventActive  = "active"
	EventDeleted = "deleted"
)

// Event is what policy needs to know about an event.
type Event struct {
	Status     string
	Visibility string // the event's own, copied from its group when made (DEC-48)
	ChurchWide bool   // no group; admins made it (DEC-7)
	// Host: the person hosts this church-wide event. A host row on a group's
	// event grants nothing.
	Host bool
}

// Widest is the more open of two visibilities; "" counts as nothing.
func Widest(a, b string) string {
	if b == "" || (a != "" && openness(a) >= openness(b)) {
		return a
	}
	return b
}

// CanSeeEvent is the one visibility rule for an event. A waiting or archived
// group's events are its leaders' and admins' only; otherwise the event's
// own visibility decides, whatever its group's says now (DEC-48). A
// church-wide event has no members: its hosts count as members. Seeing an
// event does not reveal its group: pages ask CanSeeGroup before naming it.
// events.visibleSQL is this rule for lists, and a test holds them together.
func CanSeeEvent(p *Person, e Event, g Group, r Rel) bool {
	if e.Status != EventActive {
		return false
	}
	if e.ChurchWide {
		return IsAdmin(p) || visibleTo(e.Visibility, p, p != nil && !p.Inactive && e.Host)
	}
	if g.Status == GroupDeleted {
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

// CanSeeDate is CanSeeEvent for one date, which may be open wider than its
// series (P7). Someone who sees only an opened date does not see the event.
func CanSeeDate(p *Person, e Event, dateVisibility string, g Group, r Rel) bool {
	e.Visibility = Widest(e.Visibility, dateVisibility)
	return CanSeeEvent(p, e, g, r)
}

// GroupCanSchedule: only an active group has new events or changes to them;
// an admin approves or reactivates the group first (DEC-147).
func GroupCanSchedule(status string) bool { return status == GroupActive }

// CanScheduleEvent: a group's leaders and admins add events to an active
// group.
func CanScheduleEvent(p *Person, g Group, r Rel) bool {
	return CanManageGroup(p, g, r) && GroupCanSchedule(g.Status)
}

// CanCreateChurchEvent: only admins make church-wide events (DEC-7).
func CanCreateChurchEvent(p *Person) bool { return IsAdmin(p) }

// CanManageEvent covers editing and deleting an event, cancelling and
// restoring its dates, and opening a date wider: for a group's event, the
// people who may schedule one in its group; for a church-wide event, admins
// and its hosts, who are never teens (DEC-18).
func CanManageEvent(p *Person, e Event, g Group, r Rel) bool {
	if e.Status != EventActive {
		return false
	}
	if e.ChurchWide {
		return IsAdmin(p) || (p != nil && !p.Inactive && !p.Teen && e.Host)
	}
	return CanScheduleEvent(p, g, r)
}

// CanSetEventVisibility: a group's event may take any visibility; one wider
// than its group is told (OpensWider). A church-wide event is public or
// church only, since it has no members to invite.
func CanSetEventVisibility(to string, churchWide bool) bool {
	if churchWide {
		return to == Public || to == Church
	}
	return ValidVisibility(to)
}

// MoreOpen reports whether one visibility lets more people in than another.
func MoreOpen(a, b string) bool { return openness(a) > openness(b) }

// OpensWider reports whether a change makes an event or a date more open
// than it was and wider than its group. It is audited, and the group's other
// leaders and the admins are told (DEC-6, DEC-28). A church-wide event has
// no group to be wider than.
func OpensWider(from, to, groupVisibility string, churchWide bool) bool {
	return !churchWide && openness(to) > openness(from) && openness(to) > openness(groupVisibility)
}

// CanSetDateVisibility: one date may be opened wider than its series, never
// narrower (P7). "" puts it back to the series'.
func CanSetDateVisibility(eventVisibility, to string, churchWide bool) bool {
	if to == "" {
		return true
	}
	return CanSetEventVisibility(to, churchWide) && openness(to) > openness(eventVisibility)
}

// DateState is what the date-by-date rules need to know about one date.
type DateState struct {
	Started   bool // it has begun: history, never changed
	Cancelled bool
	BySeries  bool // a series edit cancelled it, and only a series edit restores it (DEC-91)
}

// CanCancelDate: those who run the event cancel one date that has not begun.
func CanCancelDate(manages bool, d DateState) bool { return manages && !d.Started && !d.Cancelled }

// CanRestoreDate: those who run the event bring back a date cancelled by
// hand, before it begins, while the series still makes that day (DEC-91).
func CanRestoreDate(manages bool, d DateState, seriesMakesIt bool) bool {
	return manages && !d.Started && d.Cancelled && !d.BySeries && seriesMakesIt
}

// CanOpenDate: those who run the event open a date that is still to come.
func CanOpenDate(manages bool, d DateState) bool { return manages && !d.Started && !d.Cancelled }

// CanNameHost: those who run a church-wide event name its hosts, who are
// active adults (DEC-7, DEC-18). Admins and hosts find them by name; results
// show names only (DEC-78).
func CanNameHost(p *Person, e Event, target *Person) bool {
	return e.ChurchWide && CanManageEvent(p, e, Group{}, Rel{}) && target != nil && !target.Inactive && !target.Teen
}

// CanFindHosts: the people who may name hosts may search people by name to
// do it.
func CanFindHosts(p *Person, e Event) bool {
	return e.ChurchWide && CanManageEvent(p, e, Group{}, Rel{})
}

// CanRemoveHost: those who run a church-wide event remove any of its hosts,
// themselves and the last included; admins still run it.
func CanRemoveHost(p *Person, e Event) bool { return CanFindHosts(p, e) }
