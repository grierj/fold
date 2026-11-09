package main

import (
	"gorm.io/gorm"

	"fold/internal/policy"
	"fold/models"
)

// The policy glue: every handler loads facts here and then asks
// internal/policy. Group, membership and host facts each have one loader,
// batched by id; a single lookup is a batch of one. All of them return their
// errors, so a failed read is never taken for "not visible" or "no member".

// asPerson describes the signed-in user to policy; nil means signed out.
func asPerson(u *models.User) *policy.Person {
	if u == nil {
		return nil
	}
	// A disabled admin has no admin powers (and can't sign in anyway).
	return &policy.Person{
		ID: u.ID, Admin: u.Role == models.RoleAdmin && u.Status == models.UserActive,
		Teen: u.AccountType == models.AccountTeen, Inactive: u.Status != models.UserActive,
	}
}

// asGroup describes a group to policy. A nil group (a church-wide event, or a
// group that is gone) is the zero value, which no rule treats as active.
func asGroup(g *models.Group) policy.Group {
	if g == nil {
		return policy.Group{}
	}
	return policy.Group{Status: g.Status, Visibility: g.Visibility}
}

// membershipRel turns a membership row (any status) into policy facts.
func membershipRel(m models.Membership) policy.Rel {
	return relOfStatus(m.ID != 0, m.Status, m.Role)
}

// relOfStatus is the one place a membership's id, status and role become
// policy facts.
func relOfStatus(exists bool, status, role string) policy.Rel {
	active := exists && status == models.MembershipActive
	return policy.Rel{
		Member:  active,
		Leader:  active && role == models.MembershipLeader,
		Removed: exists && status == models.MembershipRemoved,
	}
}

func idList(ids map[uint]struct{}) []uint {
	list := make([]uint, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	return list
}

// groupsByID loads groups by id. A missing group is absent from the map, and
// so is a soft-deleted one unless unscoped (the admin "show deleted events"
// switch, DEC-68).
func groupsByID(db *gorm.DB, ids []uint, unscoped bool) (map[uint]*models.Group, error) {
	out := map[uint]*models.Group{}
	if len(ids) == 0 {
		return out, nil
	}
	if unscoped {
		db = db.Unscoped()
	}
	var gs []models.Group
	if err := db.Where("id IN ?", ids).Find(&gs).Error; err != nil {
		return nil, err
	}
	for i := range gs {
		out[gs[i].ID] = &gs[i]
	}
	return out, nil
}

// groupRels loads one user's membership in each of the groups (any status).
// A group with no membership row is absent from the map, which reads as the
// zero Rel.
func groupRels(db *gorm.DB, userID uint, groupIDs []uint) (map[uint]policy.Rel, error) {
	out := map[uint]policy.Rel{}
	if userID == 0 || len(groupIDs) == 0 {
		return out, nil
	}
	var ms []models.Membership
	if err := db.Where("user_id = ? AND group_id IN ?", userID, groupIDs).Find(&ms).Error; err != nil {
		return nil, err
	}
	for _, m := range ms {
		out[m.GroupID] = membershipRel(m)
	}
	return out, nil
}

// groupRel is groupRels for one group.
func groupRel(db *gorm.DB, userID, groupID uint) (policy.Rel, error) {
	rels, err := groupRels(db, userID, []uint{groupID})
	return rels[groupID], err
}

// hostedEvents is the church-wide events (of eventIDs) the user hosts. A host
// row on a group event grants nothing (DEC-7), so such events never appear.
func hostedEvents(db *gorm.DB, userID uint, eventIDs []uint) (map[uint]bool, error) {
	out := map[uint]bool{}
	if userID == 0 || len(eventIDs) == 0 {
		return out, nil
	}
	var ids []uint
	err := db.Model(&models.EventHost{}).
		Joins("JOIN events ON events.id = event_hosts.event_id AND events.group_id IS NULL").
		Where("event_hosts.user_id = ? AND event_hosts.event_id IN ?", userID, eventIDs).
		Pluck("event_hosts.event_id", &ids).Error
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// factSet is the group, membership and host facts of one viewer over a batch
// of events.
type factSet struct {
	Groups map[uint]*models.Group
	Rels   map[uint]policy.Rel
	Hosted map[uint]bool
}

// loadFactSet reads the facts for evs in three queries. withDeleted keeps
// soft-deleted groups, for the admin calendar switch only.
func loadFactSet(db *gorm.DB, viewer *models.User, evs []models.Event, withDeleted bool) (factSet, error) {
	s := factSet{Groups: map[uint]*models.Group{}, Rels: map[uint]policy.Rel{}, Hosted: map[uint]bool{}}
	gset := map[uint]struct{}{}
	var churchWide []uint
	for i := range evs {
		if evs[i].GroupID != nil {
			gset[*evs[i].GroupID] = struct{}{}
		} else {
			churchWide = append(churchWide, evs[i].ID)
		}
	}
	groupIDs := idList(gset)
	var err error
	if s.Groups, err = groupsByID(db, groupIDs, withDeleted); err != nil {
		return s, err
	}
	var viewerID uint
	if viewer != nil {
		viewerID = viewer.ID
	}
	if s.Rels, err = groupRels(db, viewerID, groupIDs); err != nil {
		return s, err
	}
	s.Hosted, err = hostedEvents(db, viewerID, churchWide)
	return s, err
}

// of is the facts for one event of the batch.
func (s factSet) of(ev *models.Event) eventFacts {
	f := eventFacts{view: viewOf(ev, s.Hosted[ev.ID])}
	if ev.GroupID != nil {
		f.group = s.Groups[*ev.GroupID]
		f.rel = s.Rels[*ev.GroupID]
	}
	return f
}

// eventFacts is the policy input for one event and one viewer.
type eventFacts struct {
	group *models.Group
	rel   policy.Rel
	view  policy.EventView
}

func viewOf(ev *models.Event, host bool) policy.EventView {
	if ev.GroupID != nil {
		host = false
	}
	return policy.EventView{
		ChurchWide: ev.GroupID == nil,
		Visibility: ev.Visibility,
		Host:       host,
	}
}

// eventFactsIn is the facts for one event: a batch of one.
func eventFactsIn(db *gorm.DB, u *models.User, ev *models.Event) (eventFacts, error) {
	s, err := loadFactSet(db, u, []models.Event{*ev}, false)
	if err != nil {
		return eventFacts{}, err
	}
	return s.of(ev), nil
}

// canSee, canManage and canRegister are the event rules with this viewer's
// facts filled in.
func (f eventFacts) canSee(me *models.User) bool {
	return policy.CanSeeEvent(asPerson(me), f.view, asGroup(f.group), f.rel)
}

func (f eventFacts) canManage(me *models.User) bool {
	return policy.CanManageEvent(asPerson(me), f.view, f.rel)
}

// groupAndRel is the group and viewer's membership for an event of the batch
// (nil and the zero Rel for a church-wide event).
func groupAndRel(ev *models.Event, groups map[uint]*models.Group, rels map[uint]policy.Rel) (*models.Group, policy.Rel) {
	if ev.GroupID == nil {
		return nil, policy.Rel{}
	}
	return groups[*ev.GroupID], rels[*ev.GroupID]
}

// hostsChurchEvent reports whether the user hosts any church-wide event. Host
// rows on group events grant nothing (DEC-7).
func hostsChurchEvent(db *gorm.DB, userID uint) (bool, error) {
	var n int64
	err := db.Model(&models.EventHost{}).
		Joins("JOIN events ON events.id = event_hosts.event_id AND events.group_id IS NULL AND events.deleted_at IS NULL").
		Where("event_hosts.user_id = ?", userID).Count(&n).Error
	return n > 0, err
}
