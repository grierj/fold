package main

import (
	"errors"
	"sort"
	"strings"
	"time"

	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"

	"gorm.io/gorm"
)

// Message audiences (M3.4, PLAN 7.2). An audience names whose registrants
// or members a message reaches; this file checks the author may target it
// and works out who it reaches, with the counts the composer shows. M3.5's
// sender resolves it again at send time.

// audienceJSON is one PLAN 7.2 audience.
type audienceJSON struct {
	Type         string   `json:"type"`
	OccurrenceID uint     `json:"occurrenceId,omitempty"`
	EventID      uint     `json:"eventId,omitempty"`
	EventIDs     []uint   `json:"eventIds,omitempty"`
	GroupID      uint     `json:"groupId,omitempty"`
	UserIDs      []uint   `json:"userIds,omitempty"`
	Statuses     []string `json:"statuses,omitempty"`
	Roles        []string `json:"roles,omitempty"`
}

// Bounds on what one audience may name.
const (
	maxAudienceEvents = 50
	maxAudienceUsers  = 500
)

var (
	errAudienceInvalid = errors.New("choose who gets this message")
	errAudienceGone    = errors.New("that group or event isn't there anymore")
)

// Registrants a message can reach by default: Going and on the waitlist.
var defaultRegStatuses = []string{models.RegGoing, models.RegWaitlisted}

// normalize checks the shape and fills defaults; it reports errAudienceInvalid.
func (au *audienceJSON) normalize() error {
	switch au.Type {
	case policy.AudienceOccurrence:
		if au.OccurrenceID == 0 {
			return errAudienceInvalid
		}
	case policy.AudienceEventSeries:
		if au.EventID == 0 {
			return errAudienceInvalid
		}
	case policy.AudienceEvents:
		if len(au.EventIDs) == 0 || len(au.EventIDs) > maxAudienceEvents {
			return errAudienceInvalid
		}
	case policy.AudienceGroup:
		if au.GroupID == 0 {
			return errAudienceInvalid
		}
		roles := []string{}
		for _, r := range au.Roles {
			if r == models.MembershipLeader || r == models.MembershipMember {
				roles = append(roles, r)
			}
		}
		if len(roles) == 0 {
			roles = []string{models.MembershipMember, models.MembershipLeader}
		}
		au.Roles = roles
	case policy.AudienceAllMembers:
	case policy.AudienceUsers:
		if len(au.UserIDs) == 0 || len(au.UserIDs) > maxAudienceUsers {
			return errAudienceInvalid
		}
	default:
		return errAudienceInvalid
	}
	if au.Type == policy.AudienceOccurrence || au.Type == policy.AudienceEventSeries || au.Type == policy.AudienceEvents {
		statuses := []string{}
		for _, s := range au.Statuses {
			if s == models.RegGoing || s == models.RegMaybe || s == models.RegWaitlisted {
				statuses = append(statuses, s)
			}
		}
		if len(statuses) == 0 {
			statuses = defaultRegStatuses
		}
		au.Statuses = statuses
	}
	au.canonical()
	return nil
}

// canonical clears the fields au.Type doesn't use, so the stored audience
// says only what it means.
func (au *audienceJSON) canonical() {
	c := audienceJSON{Type: au.Type}
	switch au.Type {
	case policy.AudienceOccurrence:
		c.OccurrenceID, c.Statuses = au.OccurrenceID, au.Statuses
	case policy.AudienceEventSeries:
		c.EventID, c.Statuses = au.EventID, au.Statuses
	case policy.AudienceEvents:
		c.EventIDs, c.Statuses = dedupeIDs(au.EventIDs), au.Statuses
	case policy.AudienceGroup:
		c.GroupID, c.Roles = au.GroupID, au.Roles
	case policy.AudienceUsers:
		c.UserIDs = dedupeIDs(au.UserIDs)
	}
	*au = c
}

// eventIDs are the events an event audience names, after loading the
// occurrence's event for an occurrence audience.
func (a *App) audienceEventIDs(db *gorm.DB, au audienceJSON) ([]uint, error) {
	switch au.Type {
	case policy.AudienceOccurrence:
		var occ models.EventOccurrence
		if err := db.Select("id, event_id").First(&occ, au.OccurrenceID).Error; err != nil {
			return nil, errAudienceGone
		}
		return []uint{occ.EventID}, nil
	case policy.AudienceEventSeries:
		return []uint{au.EventID}, nil
	case policy.AudienceEvents:
		return au.EventIDs, nil
	}
	return nil, nil
}

// audienceTargets are the groups and events an audience reaches, as the
// author relates to them, for policy.CanMessageAudience.
func (a *App) audienceTargets(db *gorm.DB, me *models.User, au audienceJSON) ([]policy.MessageTarget, error) {
	if au.Type == policy.AudienceGroup {
		var g models.Group
		if err := db.First(&g, au.GroupID).Error; err != nil {
			return nil, errAudienceGone
		}
		return []policy.MessageTarget{{Group: asGroup(&g), Rel: relIn(db, me.ID, g.ID)}}, nil
	}
	ids, err := a.audienceEventIDs(db, au)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	var evs []models.Event
	if err := db.Where("id IN ?", ids).Find(&evs).Error; err != nil {
		return nil, err
	}
	if len(evs) != len(dedupeIDs(ids)) {
		return nil, errAudienceGone
	}
	gset := map[uint]struct{}{}
	for _, ev := range evs {
		if ev.GroupID != nil {
			gset[*ev.GroupID] = struct{}{}
		}
	}
	groups, err := a.groupsByID(gset)
	if err != nil {
		return nil, err
	}
	rels, err := a.relsFor(me, gset)
	if err != nil {
		return nil, err
	}
	hosted, err := a.hostedEventIDs(me, ids)
	if err != nil {
		return nil, err
	}
	out := make([]policy.MessageTarget, 0, len(evs))
	for _, ev := range evs {
		t := policy.MessageTarget{IsEvent: true, Event: policy.EventView{ChurchWide: ev.GroupID == nil, Host: hosted[ev.ID]}}
		if ev.GroupID != nil {
			g := groups[*ev.GroupID]
			if g == nil {
				return nil, errAudienceGone
			}
			t.Group, t.Rel = asGroup(g), rels[g.ID]
		}
		out = append(out, t)
	}
	return out, nil
}

func dedupeIDs(ids []uint) []uint {
	seen := map[uint]bool{}
	out := []uint{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// audienceUserIDs are the people an audience reaches before any filtering,
// each once. A household person's registration reaches the account holder
// who registered them (DEC-8). Event audiences reach upcoming dates only.
func (a *App) audienceUserIDs(db *gorm.DB, au audienceJSON, now time.Time) ([]uint, error) {
	var ids []uint
	regs := func(q *gorm.DB) error {
		return q.Model(&models.Registration{}).
			Where("registrations.status IN ?", au.Statuses).
			Distinct().Pluck("COALESCE(registrations.user_id, registrations.registered_by)", &ids).Error
	}
	var err error
	switch au.Type {
	case policy.AudienceOccurrence:
		err = regs(db.Where("registrations.occurrence_id = ?", au.OccurrenceID))
	case policy.AudienceEventSeries, policy.AudienceEvents:
		events, _ := a.audienceEventIDs(db, au)
		err = regs(db.Joins("JOIN event_occurrences o ON o.id = registrations.occurrence_id").
			Where("o.event_id IN ? AND o.status = ? AND o.starts_at >= ?", events, models.OccurrenceScheduled, now))
	case policy.AudienceGroup:
		err = db.Model(&models.Membership{}).Where("group_id = ? AND status = 'active' AND role IN ?", au.GroupID, au.Roles).
			Distinct().Pluck("user_id", &ids).Error
	case policy.AudienceAllMembers:
		err = db.Model(&models.User{}).Where("status = ?", models.UserActive).Pluck("id", &ids).Error
	case policy.AudienceUsers:
		ids = dedupeIDs(au.UserIDs)
	}
	out := []uint{}
	for _, id := range ids {
		if id != 0 {
			out = append(out, id)
		}
	}
	return dedupeIDs(out), err
}

// Why someone an audience names won't get the email. Admins see the reason
// for each person; leaders and hosts see only the counts and that a person
// won't get it, since one person's consent or complaint is theirs.
const (
	skipNotActive  = "not active"
	skipGuest      = "guest, hasn't asked for updates"
	skipNoEmail    = "no email address"
	skipNotWorking = "email not working"
	skipComplained = "marked our email as spam"
	skipOptedOut   = "turned off message notices"
	skipHidden     = "won't get this"
)

type audienceRecipientJSON struct {
	Name string `json:"name"`
	Teen bool   `json:"teen"`
	// Skipped says why this person won't get it ("won't get this" unless
	// the viewer is an admin); "" means they will.
	Skipped string `json:"skipped,omitempty"`
}

// audienceSummaryJSON is what the composer shows about an audience: counts
// and names only, never addresses (DEC-31).
type audienceSummaryJSON struct {
	People   int `json:"people"`
	Eligible int `json:"eligible"`
	// Teens counts the teens among those who will get it (DEC-20).
	Teens   int            `json:"teens"`
	Skipped map[string]int `json:"skipped"`
	// Recipients are the first 100 by name.
	Recipients []audienceRecipientJSON `json:"recipients"`
	More       bool                    `json:"more"`
}

const maxAudiencePreview = 100

// summarizeAudience filters the people for email: active, with an address,
// not suppressed, and not opted out of message notices (category:messages,
// which "Turn off all optional email" and the one-click link write, DEC-115).
// Guests are skipped: they get only transactional mail and reminders until
// they ask for updates (PLAN 7.4). Each read goes in chunks, so an
// all-members audience stays within Postgres's parameter limit.
// reasons: show each person's reason (admins).
func (a *App) summarizeAudience(db *gorm.DB, ids []uint, reasons bool) (audienceSummaryJSON, error) {
	out := audienceSummaryJSON{Skipped: map[string]int{}, Recipients: []audienceRecipientJSON{}}
	var users []models.User
	suppressed := map[string]string{}
	optedOut := map[uint]map[string]bool{}
	err := eachIDChunk(ids, func(chunk []uint) error {
		var part []models.User
		if err := db.Select("id, name, display_name, email, status, account_type").Where("id IN ?", chunk).Find(&part).Error; err != nil {
			return err
		}
		users = append(users, part...)
		emails := make([]string, 0, len(part))
		for _, u := range part {
			if e := normalizeEmail(u.Email); e != "" {
				emails = append(emails, e)
			}
		}
		if len(emails) > 0 {
			var sups []models.Suppression
			if err := db.Where("channel = 'email' AND cleared_at IS NULL AND address IN ?", emails).Find(&sups).Error; err != nil {
				return err
			}
			for _, s := range sups {
				if suppressed[s.Address] != models.SuppressComplaint {
					suppressed[s.Address] = s.Reason
				}
			}
		}
		opts, err := emailOptOuts(db, chunk)
		for id, scopes := range opts {
			optedOut[id] = scopes
		}
		return err
	})
	if err != nil {
		return out, err
	}
	messages := categoryScope(notify.CatMessages)
	for i := range users {
		u := &users[i]
		r := audienceRecipientJSON{Name: u.PublicName(), Teen: u.AccountType == models.AccountTeen}
		switch {
		case u.Status == models.UserGuest:
			r.Skipped = skipGuest
		case u.Status != models.UserActive:
			r.Skipped = skipNotActive
		case strings.TrimSpace(u.Email) == "":
			r.Skipped = skipNoEmail
		case suppressed[normalizeEmail(u.Email)] == models.SuppressComplaint:
			r.Skipped = skipComplained
		case suppressed[normalizeEmail(u.Email)] != "":
			r.Skipped = skipNotWorking
		case optedOut[u.ID][messages]:
			r.Skipped = skipOptedOut
		}
		out.People++
		if r.Skipped == "" {
			out.Eligible++
			if r.Teen {
				out.Teens++
			}
		} else {
			out.Skipped[r.Skipped]++
			if !reasons {
				r.Skipped = skipHidden
			}
		}
		out.Recipients = append(out.Recipients, r)
	}
	sort.SliceStable(out.Recipients, func(i, j int) bool {
		return strings.ToLower(out.Recipients[i].Name) < strings.ToLower(out.Recipients[j].Name)
	})
	if len(out.Recipients) > maxAudiencePreview {
		out.Recipients, out.More = out.Recipients[:maxAudiencePreview], true
	}
	return out, nil
}
