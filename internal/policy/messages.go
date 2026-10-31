package policy

// Messaging (PLAN 6, 7.2, M3.4). Admins may message anyone; a group's
// leaders may message their own active group and its events' registrants; a
// church-wide event's hosts may message only that event's registrants (DEC-7).
// Members ask instead (7.6). The check runs when a message is saved and again
// when it is sent (M3.5), since roles change.

// Audience kinds (PLAN 7.2).
const (
	AudienceOccurrence  = "occurrence"
	AudienceEventSeries = "event_series"
	AudienceEvents      = "events"
	AudienceGroup       = "group"
	AudienceAllMembers  = "all_members"
	AudienceUsers       = "users"
)

// MessageTarget is one group or event an audience reaches, as the author
// relates to it. A group target uses Group; an event target uses Event and,
// for a group's event, Group (the event's group).
type MessageTarget struct {
	IsEvent bool
	Event   EventView
	Group   Group
	Rel     Rel
}

// CanMessageTarget: may p message the people of one target?
func CanMessageTarget(p *Person, t MessageTarget) bool {
	if p == nil || p.Teen {
		return false
	}
	if p.Admin {
		return true
	}
	if t.IsEvent && t.Event.ChurchWide {
		return t.Event.Host
	}
	// A pending or archived group's leaders can't reach anyone (DEC-88).
	return t.Rel.Leader && t.Group.Status == GroupActive
}

// CanMessageAudience: may p send to an audience of this kind over these
// targets? all_members and users are admins' alone; every other kind needs
// at least one target, each of which p may message.
func CanMessageAudience(p *Person, kind string, targets []MessageTarget) bool {
	if p == nil || p.Teen {
		return false
	}
	switch kind {
	case AudienceAllMembers, AudienceUsers:
		return p.Admin
	case AudienceOccurrence, AudienceEventSeries, AudienceEvents, AudienceGroup:
	default:
		return false
	}
	if len(targets) == 0 {
		return false
	}
	for _, t := range targets {
		if t.IsEvent != (kind != AudienceGroup) || !CanMessageTarget(p, t) {
			return false
		}
	}
	return true
}

// CanComposeMessages: who sees the composer at all — admins, and anyone who
// leads an approved group or hosts a church-wide event.
func CanComposeMessages(p *Person, leadsApprovedGroup, hostsEvent bool) bool {
	return p != nil && !p.Teen && (p.Admin || leadsApprovedGroup || hostsEvent)
}

// CanEditMessage: the author, or an admin, while it hasn't started sending.
func CanEditMessage(p *Person, isAuthor bool) bool {
	return p != nil && (p.Admin || isAuthor)
}
