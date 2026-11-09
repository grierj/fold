package main

import (
	"time"

	"fold/internal/events"
	"fold/internal/policy"
	"fold/models"
)

// The date projector (E1). Every surface that shows one date of an event (the
// calendar, an event page's dates, a group's list, the date page, its .ics
// download and the three calendar feeds) gets how the date looks to the viewer
// from projectDate, which decides everything that depends on the viewer, once,
// from the facts the caller already loaded. Each surface renders the value it
// gets back:
//
//	calendar row          calendarRow
//	event / group list    occurrenceJSON (the caller adds counts and seats)
//	date page             dateJSON, rendered again as an .ics entry by
//	                      dateFeedSingle
//	public/group/personal feedSingle
//
// A renderer makes no visibility decision of its own. What differs between
// surfaces is a rendering option (a church-wide event's "hosted by" line on a
// calendar row, STATUS in an .ics file), never a second rule. The viewer's own
// answer and a date's counts are loaded in batches by each surface and handed to
// its renderer; they are facts about the viewer, not rules about the date.

type dateProjection struct {
	Event *models.Event
	Occ   *models.EventOccurrence
	// Ordinary is true when the viewer reaches the date through ordinary
	// visibility; otherwise they hold only an exact-date invitation (Invited)
	// and the date carries no group, series or host data.
	Ordinary bool
	Invited  *occurrenceRead
	// Group is the event's group when this viewer may see it (groupShownTo),
	// else nil. Rel is the viewer's standing in it; Facts the policy input.
	Group *models.Group
	Rel   policy.Rel
	Facts eventFacts
	// Manager is CanManageEvent; Attendees is CanSeeAttendeeNames; SeriesVisible
	// is whether the viewer may see the event itself and not only this date.
	Manager       bool
	Attendees     bool
	SeriesVisible bool

	eff *occurrenceJSON
}

// projectDate decides how occ looks to viewer. invited is the viewer's exact-date
// grant (nil for none). It reports false when the viewer may see the date neither
// way. vs holds the viewer's facts for the date's event; an invited-only date
// needs none of them.
func (a *App) projectDate(viewer *models.User, vs visibleSet, occ *models.EventOccurrence, invited *occurrenceRead) (*dateProjection, bool) {
	ev := vs.ByID[occ.EventID]
	ordinary := vs.canSeeDate(viewer, ev, occ)
	if !ordinary {
		if invited == nil {
			return nil, false
		}
		// The date alone: no group, series or host data. The viewer's standing
		// in the group still feeds the calendar's scope choice.
		p := &dateProjection{Event: &invited.Event, Occ: occ, Invited: invited}
		if ev != nil {
			p.Rel = vs.of(ev).rel
		}
		return p, true
	}
	f := vs.of(ev)
	p := &dateProjection{Event: ev, Occ: occ, Ordinary: true, Invited: invited, Rel: f.rel, Facts: f}
	p.Group = a.groupShownTo(viewer, f.group, f.rel)
	p.Manager = f.canManage(viewer)
	p.Attendees = policy.CanSeeAttendeeNames(asPerson(viewer), f.view, asGroup(f.group), f.rel)
	p.SeriesVisible = vs.canSeeSeries(viewer, ev)
	return p, true
}

func (p *dateProjection) DateString() string { return models.FormatCalendarDate(p.Occ.OriginalDate) }

func (p *dateProjection) DateURL() string { return occurrenceDatePath(p.Event.ID, p.DateString()) }

// ShowsSeries is whether the date carries the event's own details (description,
// childcare, recurrence): not for a date opened on its own.
func (p *dateProjection) ShowsSeries() bool { return p.Ordinary && p.SeriesVisible }

// Eff is the date with its overrides resolved. The group's place stands in as the
// default location only for a viewer who may see the group.
func (p *dateProjection) Eff() *occurrenceJSON {
	if p.eff == nil {
		groupLoc := ""
		if p.Group != nil {
			groupLoc = p.Group.Location
		}
		e := occurrenceToJSON(p.Event, p.Occ, groupLoc)
		p.eff = &e
	}
	return p.eff
}

func (p *dateProjection) RemoteOffered() bool {
	e := p.Eff()
	return events.RemoteOffered(e.RemoteAvailable, e.RemoteCapacity)
}

// groupLabel is how a date names the event's host: the group when the viewer may
// see it, else the church (DEC-17), never the group.
type groupLabel struct {
	ID         *uint
	Name       string
	Visibility string
	Deleted    bool
	HostedBy   string
}

type siteNameOnce struct {
	a    *App
	name string
	read bool
}

func (s *siteNameOnce) get() string {
	if !s.read {
		s.name, s.read = s.a.siteName(), true
	}
	return s.name
}

// label names the host. A church-wide event, or one whose group the viewer may
// not see, is hosted by the church; churchWideHostedBy says whether the surface
// spells that out for a church-wide event (event and date pages do; a calendar
// row has a churchWide flag instead).
func (p *dateProjection) label(sn *siteNameOnce, churchWideHostedBy bool) groupLabel {
	return labelGroup(p.Group, p.Event.GroupID != nil || churchWideHostedBy, sn)
}

// labelGroup is the one place a shown group (from groupShownTo, or nil) becomes
// the group or "hosted by" fields of a surface. wantHostedBy is false for a
// surface that names no host when there is no group.
func labelGroup(shown *models.Group, wantHostedBy bool, sn *siteNameOnce) groupLabel {
	if shown != nil {
		id := shown.ID
		return groupLabel{ID: &id, Name: shown.Name, Visibility: shown.Visibility, Deleted: shown.DeletedAt.Valid}
	}
	if wantHostedBy {
		return groupLabel{HostedBy: sn.get()}
	}
	return groupLabel{}
}

// occurrenceJSON is the date as the event pages and group lists show it, before
// the surface adds counts, seats, the viewer's own answer and attendees.
func (p *dateProjection) occurrenceJSON(sn *siteNameOnce) occurrenceJSON {
	inst := *p.Eff()
	inst.Visibility = policy.EffectiveVisibility(p.Event.Visibility, p.Occ.VisibilityOverride)
	inst.LeaderOpened = p.Event.LeaderOpened
	inst.ChurchWide = p.Event.GroupID == nil
	l := p.label(sn, true)
	inst.GroupID, inst.GroupName, inst.GroupVisibility, inst.HostedBy = l.ID, l.Name, l.Visibility, l.HostedBy
	if !p.SeriesVisible {
		// Like the date page: a hidden parent contributes no series fields,
		// description or childcare details (PLAN 8.2).
		inst.hideSeriesDetails(p.DateURL())
	}
	return inst
}

// listVideo is the video link on the event pages and lists: "always" links, and
// registered_only ones for a viewer going on this date (themselves or through
// their household) or running the event (policy.CanSeeVideoLink).
func (p *dateProjection) listVideo(inst *occurrenceJSON, going bool) {
	applyVideoReveal(inst, p.Event, going, p.Manager)
}

// calendarRow is the slim row the calendar shows. myStatus is the viewer's own
// answer ("" for none); it never carries a household answer.
func (p *dateProjection) calendarRow(sn *siteNameOnce, myStatus string, ownScope bool) calendarOccurrenceJSON {
	e := p.Eff()
	row := calendarOccurrenceJSON{
		EventID: p.Event.ID, Date: p.DateString(), StartsAt: e.StartsAt, EndsAt: e.EndsAt,
		Title: e.Title, Location: e.Location, ChurchWide: p.Event.GroupID == nil,
		RemoteAvailable: p.RemoteOffered(), ChildcareAvailable: p.Event.ChildcareAvailable, OwnScope: ownScope,
	}
	if !p.ShowsSeries() {
		row.DateURL = p.DateURL()
	}
	if p.Ordinary {
		l := p.label(sn, false)
		row.GroupID, row.GroupName, row.GroupDeleted, row.HostedBy = l.ID, l.Name, l.Deleted, l.HostedBy
	}
	if myStatus != "" {
		row.MyStatus = &myStatus
	}
	return row
}

// pageVideoRight is whether the viewer may see the link on this date's page: by
// ordinary access, or through an exact-date invitation (reg is their own row).
func (p *dateProjection) pageVideoRight(reg models.Registration, now time.Time) bool {
	var actor policy.ContactAccount
	var access policy.OccurrenceAccess
	var facts policy.OccurrenceFacts
	if p.Invited != nil {
		actor, access, facts = p.Invited.Account, p.Invited.Access, p.Invited.Facts
	}
	return policy.CanSeeOccurrenceVideo(actor, p.Ordinary, p.Manager, access, facts, now, videoRevealOf(p.Event), reg.Status)
}

// dateJSON is the date page. It never carries host people, series, attendee,
// contact or management data (the handler adds the room, reminders and whether
// the viewer can register). reg is the viewer's own registration, if any.
func (p *dateProjection) dateJSON(sn *siteNameOnce, reg models.Registration, now time.Time) dateOccurrenceJSON {
	e := p.Eff()
	var actor policy.ContactAccount
	var access policy.OccurrenceAccess
	if p.Invited != nil {
		actor, access = p.Invited.Account, p.Invited.Access
	}
	canVideo := p.pageVideoRight(reg, now)
	out := dateOccurrenceJSON{ID: p.Occ.ID, EventID: p.Event.ID, Date: p.DateString(), DateURL: p.DateURL(),
		Title: e.Title, Location: e.Location, StartsAt: e.StartsAt, EndsAt: e.EndsAt,
		StartsLocal: e.StartsLocal, EndsLocal: e.EndsLocal, TZID: e.TZID, DurationMinutes: e.DurationMinutes,
		RemoteAvailable: p.RemoteOffered(), GuestsAllowed: e.GuestsAllowed,
		VideoProvider: videoProviderOf(p.Event), CanSeeVideo: canVideo,
		CanSeeAttendees: policy.CanSeeOccurrenceAttendees(p.Ordinary, p.Attendees),
		CanDecline:      p.Invited != nil && policy.CanEndOccurrenceAccess(actor, false, access),
		CanBlock:        p.Invited != nil && policy.CanBlockManagerAdds(actor, access)}
	if reg.ID > 0 {
		out.MyRegistration = &reg.Status
		mode := attendanceModeOf(reg.AttendanceMode)
		out.MyAttendanceMode = &mode
		out.MyGuestCount = &reg.GuestCount
	}
	out.VideoURL, out.VideoHidden = videoWhen(p.Event, canVideo)
	if p.Ordinary {
		out.Visibility = policy.EffectiveVisibility(p.Event.Visibility, p.Occ.VisibilityOverride)
		l := p.label(sn, true)
		out.GroupID, out.GroupName, out.HostedBy = l.ID, l.Name, l.HostedBy
	}
	return out
}

// Calendar statuses (DEC-71, DEC-93).
const statusConfirmed = "CONFIRMED"

// calendarStatus is the iCalendar STATUS of a date for a person's own answer
// (DEC-71, DEC-93): Going is CONFIRMED, Maybe and Waitlisted are TENTATIVE.
// In the personal feed every other date it carries is TENTATIVE too (Not
// answered); a one-date download omits STATUS for a date the person has not
// answered, as its page shows no answer. This is a rendering option of the .ics
// file, not a rule about who sees the date.
func calendarStatus(answer string, personalFeed bool) string {
	switch {
	case answer == models.RegGoing:
		return statusConfirmed
	case personalFeed || answer == models.RegMaybe || answer == models.RegWaitlisted:
		return "TENTATIVE"
	}
	return ""
}

type feedAudience struct {
	// Personal is a signed-in person's own feed; the public and group feeds
	// are for anyone with the link and see what a signed-out visitor sees.
	Personal bool
	// Status is the person's own answer on this date, or "going" for a date a
	// household member is going to ("" for none): what the personal feed's
	// video rule asks.
	Status string
}

// feedVideoURL is the video link an entry may carry: none for a date opened on
// its own, then the personal feed's rule (DEC-70) or the signed-out rule.
func (p *dateProjection) feedVideoURL(aud feedAudience) string {
	if !p.ShowsSeries() {
		return ""
	}
	link := videoLinkOf(p.Event)
	if link == "" {
		return ""
	}
	if aud.Personal {
		if policy.CanSeeVideoLinkInFeed(videoRevealOf(p.Event), aud.Status, p.Rel.Member || p.Facts.view.Host, p.Manager) {
			return link
		}
		return ""
	}
	if policy.CanSeeVideoLink(videoRevealOf(p.Event), false, false) {
		return link
	}
	return ""
}

type feedEnv struct {
	baseURL string
	rooms   map[uint]*occurrenceRoomJSON
}

// feedSingle is a calendar entry for a date the viewer may see by ordinary
// access. An opened date carries its date fields, never its hidden parent's
// description or childcare details.
func (p *dateProjection) feedSingle(env feedEnv, aud feedAudience, status string) events.FeedSingle {
	e := p.Eff()
	single := events.FeedSingle{
		EventID: p.Event.ID, OriginalDate: p.DateString(), Title: e.Title,
		Location: placeLine(env.rooms[p.Occ.ID], e.Location), TZID: p.Event.TZID,
		StartsAt: e.StartsAt, EndsAt: e.EndsAt, VideoURL: p.feedVideoURL(aud), RemoteAvailable: p.RemoteOffered(),
		URL: env.baseURL + p.DateURL(), UpdatedAt: laterTime(p.Event.UpdatedAt, p.Occ.UpdatedAt), Status: status,
	}
	if p.ShowsSeries() {
		single.Description = p.Event.Description
		single.ChildcareAvailable, single.ChildcareAges = p.Event.ChildcareAvailable, p.Event.ChildcareAges
	}
	return single
}

// dateFeedSingle is a calendar entry rendered from the date page's value: the
// one-date download and an exact-date invitation in the personal feed. It shows
// exactly what the page may show, with no description, hosts or attendees.
func dateFeedSingle(d dateOccurrenceJSON, baseURL string, updatedAt time.Time, status string) events.FeedSingle {
	return events.FeedSingle{
		EventID: d.EventID, OriginalDate: d.Date, Title: d.Title,
		Location: placeLine(d.Room, d.Location), TZID: d.TZID, StartsAt: d.StartsAt, EndsAt: d.EndsAt,
		VideoURL: d.VideoURL, RemoteAvailable: d.RemoteAvailable,
		URL: baseURL + d.DateURL, UpdatedAt: updatedAt, Status: status,
	}
}
