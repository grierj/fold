package main

import (
	"errors"
	"log"
	"time"

	"fold/internal/jobs"
	"fold/internal/policy"
	"fold/models"

	"gorm.io/gorm"
)

// activeAdminIDs lists the active admins in id order, for callers that must
// fail when the read does.
func activeAdminIDs(tx *gorm.DB) ([]uint, error) {
	var ids []uint
	err := tx.Model(&models.User{}).Where("role = ? AND status = ?", models.RoleAdmin, models.UserActive).Order("id").Pluck("id", &ids).Error
	return ids, err
}

// leaderIDs lists a group's active leaders. A failed read is an error: a
// notice must not silently skip the people it is for.
func leaderIDs(tx *gorm.DB, groupID uint) ([]uint, error) {
	var ids []uint
	err := tx.Model(&models.Membership{}).Where("group_id = ? AND role = ? AND status = ?", groupID, models.MembershipLeader, models.MembershipActive).
		Order("user_id").Pluck("user_id", &ids).Error
	return ids, err
}

// unknownPersonName stands for a person with no row at all. A nameless person
// shows as "Member" (User.PublicName); an erased one is shown by the surface
// that says so.
const unknownPersonName = "Someone"

// publicNames keeps historical display names for one response. Like nameIn,
// it includes soft-deleted users and never falls back to an email address.
type publicNames map[uint]string

// Bound the IN expansion well below PostgreSQL's 65,535 bind parameters;
// account-request history can supply an unbounded number of distinct people.
const publicNamesBatchSize = 1000

func publicNamesIn(tx *gorm.DB, ids []uint) publicNames {
	names := publicNames{}
	unique := make([]uint, 0, len(ids))
	for _, id := range ids {
		if _, ok := names[id]; !ok {
			names[id] = unknownPersonName
			unique = append(unique, id)
		}
	}
	err := eachChunk(unique, publicNamesBatchSize, func(batch []uint) error {
		var users []models.User
		if err := tx.Unscoped().Select("id", "name", "display_name").Where("id IN ?", batch).Find(&users).Error; err != nil {
			return err
		}
		for _, u := range users {
			names[u.ID] = u.PublicName()
		}
		return nil
	})
	if err != nil {
		// Preserve the historical all-fallback behavior on read failure;
		// never publish names determined by which batch failed.
		for id := range names {
			names[id] = unknownPersonName
		}
	}
	return names
}

func (names publicNames) name(id uint) string {
	if name, ok := names[id]; ok {
		return name
	}
	return unknownPersonName
}

func (names publicNames) actorName(id *uint) string {
	if id == nil {
		return "Fold"
	}
	return names.name(*id)
}

// nameOf is the name to show for a person, soft-deleted ones included; a
// person who never existed is "Someone". A failed read is an error, for
// writers whose notice must not go out with a made-up name.
func nameOf(tx *gorm.DB, id uint) (string, error) {
	var u models.User
	switch err := tx.Unscoped().First(&u, id).Error; {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return unknownPersonName, nil
	case err != nil:
		return "", err
	}
	return u.PublicName(), nil
}

// nameIn is nameOf for pages that render anyway: a failed read shows
// "Someone" and is logged.
func nameIn(tx *gorm.DB, id uint) string {
	name, err := nameOf(tx, id)
	if err != nil {
		log.Printf("names: could not read user %d: %s", id, jobs.CleanError(err))
		return unknownPersonName
	}
	return name
}

// userIn loads a recipient for a visibility check inside tx. A missing
// (or soft-deleted) account is nil with no error; a failed read is the error.
func userIn(tx *gorm.DB, id uint) (*models.User, error) {
	var u models.User
	switch err := tx.First(&u, id).Error; {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	case err != nil:
		return nil, err
	}
	return &u, nil
}

// userCanSeeGroupIn says whether a person may see a group, for a notice writer: it loads the
// person and their membership in tx and returns a failed read as an error,
// so a notice never leaves out (or includes) a group's name on a guess.
func userCanSeeGroupIn(tx *gorm.DB, userID uint, g *models.Group) (bool, error) {
	u, err := userIn(tx, userID)
	if err != nil || u == nil || g == nil {
		return false, err
	}
	var m models.Membership
	if err := tx.Where("user_id = ? AND group_id = ?", u.ID, g.ID).Limit(1).Find(&m).Error; err != nil {
		return false, err
	}
	return policy.CanSeeGroup(asPerson(u), asGroup(g), membershipRel(m)), nil
}

// noticeFacts is everything eventNoticeVars needs for a batch of
// recipients, loaded once with IN queries (issue #76, the relsFor
// pattern in api_event_access.go) instead of re-read per person.
// viewers holds each recipient as a signed-in viewer: a
// guest, pending-consent or missing account is never a signed-in viewer,
// so a private group's name, place or link cannot be granted by its presence
// in the recipient list.
type noticeFacts struct {
	groups  map[uint]*models.Group
	viewers map[uint]*models.User
	rels    map[[2]uint]policy.Rel // by {userID, groupID}
	zones   map[uint]*time.Location
	church  *time.Location
}

// loadNoticeFacts loads the groups, recipients, their memberships in
// those groups and their zones for a batch of event notices, plus the
// church zone once. A failed read is an error: the writer fails its
// transaction rather than send notices built from partial facts.
func loadNoticeFacts(tx *gorm.DB, groupIDs, userIDs []uint) (*noticeFacts, error) {
	f := &noticeFacts{
		groups:  map[uint]*models.Group{},
		viewers: map[uint]*models.User{},
		rels:    map[[2]uint]policy.Rel{},
		zones:   map[uint]*time.Location{},
		church:  time.UTC,
	}
	if len(groupIDs) > 0 {
		var gs []models.Group
		if err := tx.Where("id IN ?", groupIDs).Find(&gs).Error; err != nil {
			return nil, err
		}
		for i := range gs {
			f.groups[gs[i].ID] = &gs[i]
		}
	}
	var users []models.User
	if len(userIDs) > 0 {
		if err := tx.Where("id IN ?", userIDs).Find(&users).Error; err != nil {
			return nil, err
		}
		f.church = churchZoneOrUTC(tx)
	}
	for i := range users {
		if users[i].Status == models.UserActive {
			f.viewers[users[i].ID] = &users[i]
		}
		f.zones[users[i].ID] = userZone(users[i].Timezone, f.church)
	}
	if len(groupIDs) > 0 && len(userIDs) > 0 {
		var ms []models.Membership
		if err := tx.Where("user_id IN ? AND group_id IN ?", userIDs, groupIDs).Find(&ms).Error; err != nil {
			return nil, err
		}
		for _, m := range ms {
			f.rels[[2]uint{m.UserID, m.GroupID}] = membershipRel(m)
		}
	}
	return f, nil
}

// canSeeGroup is userCanSeeGroupIn against the preloaded facts: the same
// policy call, with the membership relIn would have loaded.
func (f *noticeFacts) canSeeGroup(userID uint, g *models.Group) bool {
	u := f.viewers[userID]
	if u == nil || g == nil {
		return false
	}
	return policy.CanSeeGroup(asPerson(u), asGroup(g), f.rels[[2]uint{userID, g.ID}])
}

// zone is the preloaded recipientZone for userID: their own zone, else
// the church's, else UTC.
func (f *noticeFacts) zone(userID uint) *time.Location {
	if loc, ok := f.zones[userID]; ok {
		return loc
	}
	return f.church
}

// recipientZone is the zone a person reads emailed and notified times in
// (DEC-58): their own, else the church's, else UTC.
func recipientZone(tx *gorm.DB, userID uint) (*time.Location, error) {
	var zones []string
	if err := tx.Model(&models.User{}).Where("id = ?", userID).Pluck("timezone", &zones).Error; err != nil {
		return nil, err
	}
	if len(zones) == 1 {
		return userZoneIn(tx, zones[0]), nil
	}
	return userZoneIn(tx, ""), nil
}
