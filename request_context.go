package main

import (
	"fold/internal/policy"
	"fold/models"

	"gorm.io/gorm"
)

// userContext keys a fact about one person in one group or event.
type userContext struct{ user, context uint }

// contextRef names the group or event one request was made in.
type contextRef struct {
	typ string
	id  uint
}

// requestContexts holds the groups and events a page of requests was made in,
// with what the listed people's memberships and hosting say about them. It is
// loaded once per page; shown is the one rule for whether a viewer may see a
// request's context (the contact and add request lists and the account-deletion
// reason review all ask it).
type requestContexts struct {
	groups map[uint]*models.Group
	events map[uint]models.Event
	rels   map[userContext]policy.Rel
	hosts  map[userContext]bool
}

// loadRequestContexts loads the contexts in refs, and the membership and
// hosting rows of userIDs in them. An event's own group is loaded too.
func loadRequestContexts(tx *gorm.DB, refs []contextRef, userIDs []uint) (*requestContexts, error) {
	c := &requestContexts{
		groups: map[uint]*models.Group{}, events: map[uint]models.Event{},
		rels: map[userContext]policy.Rel{}, hosts: map[userContext]bool{},
	}
	groupIDs, eventIDs := []uint{}, []uint{}
	for _, ref := range refs {
		switch ref.typ {
		case policy.ContactEvent:
			eventIDs = append(eventIDs, ref.id)
		case policy.ContactGroup:
			groupIDs = append(groupIDs, ref.id)
		}
	}
	if len(eventIDs) > 0 {
		var rows []models.Event
		if err := tx.Where("id IN ?", eventIDs).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, ev := range rows {
			c.events[ev.ID] = ev
			if ev.GroupID != nil {
				groupIDs = append(groupIDs, *ev.GroupID)
			}
		}
	}
	if len(groupIDs) > 0 {
		var groups []models.Group
		if err := tx.Where("id IN ?", groupIDs).Find(&groups).Error; err != nil {
			return nil, err
		}
		for i := range groups {
			c.groups[groups[i].ID] = &groups[i]
		}
		var memberships []models.Membership
		if err := tx.Where("group_id IN ? AND user_id IN ?", groupIDs, userIDs).Find(&memberships).Error; err != nil {
			return nil, err
		}
		for _, m := range memberships {
			c.rels[userContext{m.UserID, m.GroupID}] = membershipRel(m)
		}
	}
	if len(eventIDs) > 0 {
		var hosts []models.EventHost
		if err := tx.Where("event_id IN ? AND user_id IN ?", eventIDs, userIDs).Find(&hosts).Error; err != nil {
			return nil, err
		}
		for _, h := range hosts {
			c.hosts[userContext{h.UserID, h.EventID}] = true
		}
	}
	return c, nil
}

// shown is the one answer to "may this viewer see the group or event this
// request was made in", with the name to show for it (the group's name, the
// event's title). A group goes through groupShownTo. An event goes through
// CanSeeEvent on its own visibility, never through its group's: date
// invitations and overrides do not authorize the series, and a group the viewer
// cannot see does not hide an event that is open to them. An event whose group
// no longer exists is not shown.
func (c *requestContexts) shown(a *App, viewer *models.User, ref contextRef) (string, bool) {
	switch ref.typ {
	case policy.ContactGroup:
		// group-ok: groupShownTo applies the viewer's visibility
		if g := a.groupShownTo(viewer, c.groups[ref.id], c.rels[userContext{viewer.ID, ref.id}]); g != nil {
			return g.Name, true
		}
	case policy.ContactEvent:
		ev, ok := c.events[ref.id]
		if !ok {
			return "", false
		}
		group, rel := policy.Group{}, policy.Rel{}
		if ev.GroupID != nil {
			g := c.groups[*ev.GroupID]
			if g == nil {
				return "", false
			}
			group, rel = asGroup(g), c.rels[userContext{viewer.ID, g.ID}]
		}
		if policy.CanSeeEvent(asPerson(viewer), viewOf(&ev, c.hosts[userContext{viewer.ID, ev.ID}]), group, rel) {
			return ev.Title, true
		}
	}
	return "", false
}
