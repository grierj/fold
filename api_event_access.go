package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/events"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

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

func (a *App) groupsByID(ids map[uint]struct{}) map[uint]*models.Group {
	out := map[uint]*models.Group{}
	if len(ids) == 0 {
		return out
	}
	list := make([]uint, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	var gs []models.Group
	a.DB.Where("id IN ?", list).Find(&gs)
	for i := range gs {
		out[gs[i].ID] = &gs[i]
	}
	return out
}

func (a *App) relsFor(u *models.User, ids map[uint]struct{}) map[uint]policy.Rel {
	out := map[uint]policy.Rel{}
	if u == nil || len(ids) == 0 {
		return out
	}
	list := make([]uint, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	var ms []models.Membership
	a.DB.Where("user_id = ? AND group_id IN ?", u.ID, list).Find(&ms)
	for _, m := range ms {
		out[m.GroupID] = membershipRel(m)
	}
	return out
}

func (a *App) hostedEventIDs(u *models.User, eventIDs []uint) map[uint]bool {
	out := map[uint]bool{}
	if u == nil || len(eventIDs) == 0 {
		return out
	}
	var rows []models.EventHost
	a.DB.Where("user_id = ? AND event_id IN ?", u.ID, eventIDs).Find(&rows)
	for _, h := range rows {
		out[h.EventID] = true
	}
	return out
}

func groupAndRel(ev *models.Event, groups map[uint]*models.Group, rels map[uint]policy.Rel) (*models.Group, policy.Rel) {
	if ev.GroupID == nil {
		return nil, policy.Rel{}
	}
	return groups[*ev.GroupID], rels[*ev.GroupID]
}

func (a *App) factsFor(u *models.User, ev *models.Event) eventFacts {
	return a.factsForIn(a.DB, u, ev)
}

func (a *App) factsForIn(db *gorm.DB, u *models.User, ev *models.Event) eventFacts {
	f := eventFacts{}
	if ev.GroupID != nil {
		var g models.Group
		if err := db.First(&g, *ev.GroupID).Error; err == nil {
			f.group = &g
			if u != nil {
				var m models.Membership
				if db.Where("user_id = ? AND group_id = ?", u.ID, g.ID).First(&m).Error == nil {
					f.rel = membershipRel(m)
				}
			}
		}
	}
	host := false
	if ev.GroupID == nil && u != nil {
		var n int64
		db.Model(&models.EventHost{}).Where("event_id = ? AND user_id = ?", ev.ID, u.ID).Count(&n)
		host = n > 0
	}
	f.view = viewOf(ev, host)
	return f
}

// groupShownTo returns g when the viewer may see the group, else nil.
// Read a group's name, place or id for event and notice output only through
// its result (TestGroupDetailsGoThroughGroupShownTo enforces this).
func (a *App) groupShownTo(me *models.User, g *models.Group, rel policy.Rel) *models.Group {
	if g == nil || !policy.CanSeeGroup(asPerson(me), asGroup(g), rel) {
		return nil
	}
	return g
}

// presentGroup hides a group the viewer cannot see. An opened invite-only
// event then says it is hosted by the church and names nobody (DEC-17).
func (a *App) presentGroup(inst *occurrenceJSON, ev *models.Event, g *models.Group, me *models.User, rel policy.Rel) {
	if ev.GroupID == nil {
		inst.HostedBy = a.siteName()
		return
	}
	if shown := a.groupShownTo(me, g, rel); shown != nil {
		gid := shown.ID
		inst.GroupID = &gid
		inst.GroupName = shown.Name
		return
	}
	inst.HostedBy = a.siteName()
}

func (a *App) presentEvent(ev *models.Event, me *models.User, f eventFacts) eventJSON {
	j := toEventJSON(ev)
	if policy.CanManageEvent(asPerson(me), f.view, f.rel) {
		revealVideoToManager(&j, ev)
	}
	if shown := a.groupShownTo(me, f.group, f.rel); ev.GroupID == nil || shown == nil {
		j.GroupID = nil
		j.GroupName = ""
		j.HostedBy = a.siteName()
	} else {
		j.GroupName = shown.Name
		j.GroupVisibility = shown.Visibility
	}
	if policy.IsAdmin(asPerson(me)) && ev.GroupID == nil {
		j.Hosts = a.eventHosts(ev.ID)
	}
	return j
}

func (a *App) eventHosts(eventID uint) []hostJSON {
	var rows []models.EventHost
	a.DB.Preload("User").Where("event_id = ?", eventID).Order("id").Find(&rows)
	out := make([]hostJSON, 0, len(rows))
	for _, h := range rows {
		if h.User.ID == 0 {
			continue
		}
		out = append(out, hostJSON{UserID: h.UserID, Name: h.User.PublicName(), Email: h.User.Email})
	}
	return out
}

func (a *App) eventPermissions(me *models.User, f eventFacts) (manage, setVis bool) {
	p := asPerson(me)
	manage = policy.CanManageEvent(p, f.view, f.rel)
	setVis = policy.CanSetEventVisibility(p, f.view, f.rel, models.VisibilityPublic) ||
		policy.CanSetEventVisibility(p, f.view, f.rel, models.VisibilityPrivate)
	return
}

func upcomingWindow(now time.Time) (from, to string, start time.Time) {
	return now.Add(-24 * time.Hour).Format("2006-01-02"), now.AddDate(0, 0, 90).Format("2006-01-02"), now
}

// loadVisibleEvent is 404 when the event is missing or the caller cannot
// see it. Managing it is a separate 403.
func (a *App) loadVisibleEvent(c *gin.Context) (*models.Event, eventFacts, bool) {
	id, _ := strconv.Atoi(c.Param("id"))
	var ev models.Event
	if err := a.DB.First(&ev, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "event not found")
		return nil, eventFacts{}, false
	}
	f := a.factsFor(apiUser(c), &ev)
	gp := policy.Group{}
	if f.group != nil {
		gp = asGroup(f.group)
	}
	if !policy.CanSeeEvent(asPerson(apiUser(c)), f.view, gp, f.rel) {
		jsonError(c, http.StatusNotFound, "event not found")
		return nil, eventFacts{}, false
	}
	return &ev, f, true
}

// openEventIDs is church-wide events the viewer can see, plus group events
// they can see whose group they cannot. Admins already see every group, so
// an opened group event stays on that group's page for them.
func (a *App) openEventIDs(u *models.User) []uint {
	now := time.Now().UTC()
	from, to, _ := upcomingWindow(now)
	// Only events with a date in the listed window can add anything (#75).
	vs, err := a.visibleEvents(u, a.DB.Where(
		"id IN (SELECT event_id FROM event_occurrences WHERE original_date >= ? AND original_date <= ?)", from, to))
	if err != nil {
		return nil
	}
	p := asPerson(u)
	var out []uint
	for i := range vs.Events {
		ev := &vs.Events[i]
		g, rel := groupAndRel(ev, vs.Groups, vs.Rels)
		if ev.GroupID == nil || !policy.CanSeeGroup(p, asGroup(g), rel) {
			out = append(out, ev.ID)
		}
	}
	return out
}

func (a *App) apiListOpenEvents(c *gin.Context) {
	me := apiUser(c)
	now := time.Now().UTC()
	from, to, start := upcomingWindow(now)
	c.JSON(http.StatusOK, gin.H{"events": a.occurrencesForEvents(a.openEventIDs(me), from, to, &start, me)})
}

func (a *App) apiGetEvent(c *gin.Context) {
	ev, f, ok := a.loadVisibleEvent(c)
	if !ok {
		return
	}
	now := time.Now().UTC()
	from, to, start := upcomingWindow(now)
	manage, setVis := a.eventPermissions(apiUser(c), f)
	c.JSON(http.StatusOK, gin.H{
		"event":            a.presentEvent(ev, apiUser(c), f),
		"occurrences":      a.occurrencesForEvents([]uint{ev.ID}, from, to, &start, apiUser(c)),
		"canManage":        manage,
		"canSetVisibility": setVis,
	})
}

func (a *App) hostCandidate(actor *models.User, email string) (*models.User, int, string) {
	email = normalizeEmail(email)
	if email == "" {
		return nil, http.StatusBadRequest, "email is required"
	}
	var u models.User
	if err := a.DB.Where("email = ?", email).First(&u).Error; err != nil {
		return nil, http.StatusNotFound, "no account with that email"
	}
	if u.Status != models.UserActive {
		return nil, http.StatusBadRequest, "that account can't host an event"
	}
	if !policy.CanAssignHost(asPerson(actor), asPerson(&u)) {
		return nil, http.StatusBadRequest, "teens can't host an event"
	}
	return &u, 0, ""
}

func (a *App) apiCreateChurchEvent(c *gin.Context) {
	var body struct {
		eventBody
		Visibility *string  `json:"visibility"`
		Hosts      []string `json:"hosts"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "startsLocal and durationMinutes are required")
		return
	}
	if body.Title == nil || strings.TrimSpace(*body.Title) == "" {
		jsonError(c, http.StatusBadRequest, "title is required")
		return
	}
	vis := models.VisibilityPublic
	if body.Visibility != nil && strings.TrimSpace(*body.Visibility) != "" {
		vis = strings.TrimSpace(*body.Visibility)
	}
	if vis == models.VisibilityInviteOnly {
		jsonError(c, http.StatusBadRequest, "a church-wide event can't be invite-only")
		return
	}
	if vis != models.VisibilityPublic && vis != models.VisibilityPrivate {
		jsonError(c, http.StatusBadRequest, "visibility must be public or private")
		return
	}
	ed, ok := parseCapacityEdits(c, body.eventBody)
	if !ok {
		return
	}
	vid, ok := parseVideoEdits(c, body.eventBody, nil)
	if !ok {
		return
	}
	spec, ok := a.specFrom(c, body.eventBody, nil, true)
	if !ok {
		return
	}
	creator := apiUser(c)
	hostIDs := []uint{creator.ID}
	seen := map[uint]bool{creator.ID: true}
	for _, email := range body.Hosts {
		if strings.TrimSpace(email) == "" {
			continue
		}
		u, code, msg := a.hostCandidate(creator, email)
		if msg != "" {
			jsonError(c, code, msg)
			return
		}
		if seen[u.ID] {
			continue
		}
		seen[u.ID] = true
		hostIDs = append(hostIDs, u.ID)
	}
	ev := models.Event{
		Visibility: vis, CreatedBy: creator.ID,
	}
	applySpec(&ev, spec, body.eventBody, ed, vid)
	if !textOK(c, ev.Title, ev.Description, ev.Location) {
		return
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ev).Error; err != nil {
			return err
		}
		for _, id := range hostIDs {
			if err := tx.Create(&models.EventHost{EventID: ev.ID, UserID: id, AddedBy: creator.ID}).Error; err != nil {
				return err
			}
		}
		return events.Ensure(tx, &ev, time.Now().UTC())
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not create event")
		return
	}
	f := a.factsFor(creator, &ev)
	c.JSON(http.StatusCreated, gin.H{"event": a.presentEvent(&ev, creator, f)})
}

// apiSetEventVisibility sets an event's own visibility (DEC-48). On a group
// event, a value wider than the group marks it leader-opened and is audited
// as an override with a notice to the other leaders and admins (DEC-17,
// DEC-28); anything else clears the marker and is audited as a change.
func (a *App) apiSetEventVisibility(c *gin.Context) {
	ev, f, ok := a.loadVisibleEvent(c)
	if !ok {
		return
	}
	var body struct {
		Visibility string `json:"visibility"`
		Reason     string `json:"reason"`
		// Confirm acknowledges that registrants will be cancelled (the preview).
		Confirm bool `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid body")
		return
	}
	vis := strings.TrimSpace(body.Visibility)
	reason := strings.TrimSpace(body.Reason)
	if len(reason) > 500 {
		jsonError(c, http.StatusBadRequest, "reason is too long")
		return
	}
	me := apiUser(c)
	if !policy.CanManageEvent(asPerson(me), f.view, f.rel) {
		jsonError(c, http.StatusForbidden, "you can't change this event")
		return
	}
	if !policy.CanSetEventVisibility(asPerson(me), f.view, f.rel, vis) {
		jsonError(c, http.StatusBadRequest, "that visibility isn't allowed on this event")
		return
	}
	opened := f.group != nil && policy.MoreOpen(vis, f.group.Visibility)
	if ev.Visibility == vis && ev.LeaderOpened == opened {
		c.JSON(http.StatusOK, gin.H{"event": a.presentEvent(ev, me, f)})
		return
	}
	action := audit.VisibilityChanged
	if opened {
		action = audit.VisibilityOverride
	}
	oldVis, oldOpened := ev.Visibility, ev.LeaderOpened
	var impact visibilityImpactJSON
	var emailed []uint
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var locked models.Event
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, ev.ID).Error; err != nil {
			return err
		}
		// More private: the same cancel-and-notify as a group tightening (DEC-48).
		if policy.Openness(vis) < policy.Openness(locked.Visibility) {
			if impact = eventImpactOf(tx, &locked, vis); !body.Confirm && len(impact.Tightened) == 1 && impact.Tightened[0].Registrants > 0 {
				return errNeedDecision
			}
			ids, err := cancelLosers(c, tx, &locked, vis, time.Now().UTC())
			emailed = append(emailed, ids...)
			if err != nil {
				return err
			}
		}
		if err := tx.Model(&locked).Updates(map[string]any{"visibility": vis, "leader_opened": opened}).Error; err != nil {
			return err
		}
		locked.Visibility, locked.LeaderOpened = vis, opened
		*ev = locked
		meta := map[string]any{
			"oldVisibility":   oldVis,
			"newVisibility":   vis,
			"oldLeaderOpened": oldOpened,
			"leaderOpened":    opened,
		}
		if f.group != nil {
			meta["groupVisibility"] = f.group.Visibility
		}
		if reason != "" {
			meta["reason"] = reason
		}
		if err := audit.Write(tx, audit.Entry{
			Actor: &me.ID, Action: action, TargetType: "event", TargetID: ev.ID,
			Meta: meta, IP: c.ClientIP(),
		}); err != nil {
			return err
		}
		if action == audit.VisibilityOverride && f.group != nil {
			ids, err := notifyVisibilityOverride(tx, me.ID, ev, f.group, vis, reason)
			emailed = append(emailed, ids...)
			return err
		}
		return nil
	})
	if errors.Is(err, errNeedDecision) {
		c.JSON(http.StatusConflict, gin.H{
			"error":         "Confirm cancelling the registrations of people who can no longer see this event",
			"needsDecision": true,
			"impact":        impact,
		})
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not update visibility")
		return
	}
	a.deliverNotificationEmails(emailed)
	f.view.Visibility = vis
	c.JSON(http.StatusOK, gin.H{"event": a.presentEvent(ev, me, f)})
}

func (a *App) loadChurchEvent(c *gin.Context) (*models.Event, bool) {
	id, _ := strconv.Atoi(c.Param("id"))
	var ev models.Event
	if err := a.DB.First(&ev, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "event not found")
		return nil, false
	}
	if ev.GroupID != nil {
		jsonError(c, http.StatusBadRequest, "hosts are only assigned on church-wide events")
		return nil, false
	}
	return &ev, true
}

func (a *App) apiAddEventHost(c *gin.Context) {
	ev, ok := a.loadChurchEvent(c)
	if !ok {
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Email) == "" {
		jsonError(c, http.StatusBadRequest, "email is required")
		return
	}
	target, code, msg := a.hostCandidate(apiUser(c), body.Email)
	if msg != "" {
		jsonError(c, code, msg)
		return
	}
	row := models.EventHost{EventID: ev.ID, UserID: target.ID, AddedBy: apiUser(c).ID}
	if err := a.DB.Where("event_id = ? AND user_id = ?", ev.ID, target.ID).FirstOrCreate(&row).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not add host")
		return
	}
	c.JSON(http.StatusOK, gin.H{"hosts": a.eventHosts(ev.ID)})
}

func (a *App) apiRemoveEventHost(c *gin.Context) {
	ev, ok := a.loadChurchEvent(c)
	if !ok {
		return
	}
	uid, _ := strconv.Atoi(c.Param("userId"))
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var locked models.Event
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, ev.ID).Error; err != nil {
			return err
		}
		var rows []models.EventHost
		if err := tx.Where("event_id = ?", ev.ID).Find(&rows).Error; err != nil {
			return err
		}
		found := false
		for _, h := range rows {
			if h.UserID == uint(uid) {
				found = true
			}
		}
		if !found {
			jsonError(c, http.StatusNotFound, "that person isn't a host")
			return errHostGone
		}
		if len(rows) <= 1 {
			jsonError(c, http.StatusBadRequest, "a church-wide event needs at least one host")
			return errLastHost
		}
		return tx.Where("event_id = ? AND user_id = ?", ev.ID, uid).Delete(&models.EventHost{}).Error
	})
	if err == errHostGone || err == errLastHost {
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not remove host")
		return
	}
	c.JSON(http.StatusOK, gin.H{"hosts": a.eventHosts(ev.ID)})
}

// errHostGone and errLastHost stop the remove transaction after the handler
// has already written the response status.
var (
	errHostGone = errSent("host gone")
	errLastHost = errSent("last host")
)

type errSent string

func (e errSent) Error() string { return string(e) }

// visibleSet is the events one viewer may see out of a query, with the
// lookups callers need afterwards. Events keeps the query's order.
type visibleSet struct {
	Events []models.Event
	IDs    []uint
	ByID   map[uint]*models.Event
	Groups map[uint]*models.Group
	Rels   map[uint]policy.Rel
	Hosted map[uint]bool
}

// visibleEvents is the one list filter for event visibility (#75): it loads
// the events q selects and keeps those policy.CanSeeEvent allows for the
// viewer (nil is a signed-out visitor). An event whose group row is gone
// (soft-deleted) is never visible, not even to an admin. Pass a query
// already narrowed to a window where possible; TestOneEventVisibilityFilter
// keeps a second copy of this loop out of the code.
func (a *App) visibleEvents(viewer *models.User, q *gorm.DB) (visibleSet, error) {
	return a.visibleEventsWith(viewer, q, false)
}

// visibleEventsWith is visibleEvents with the admin-only "show deleted
// events" switch (DEC-68): when withDeleted is set and the viewer is an
// admin, events of soft-deleted groups are listed too. For anyone else
// the switch does nothing.
func (a *App) visibleEventsWith(viewer *models.User, q *gorm.DB, withDeleted bool) (visibleSet, error) {
	withDeleted = withDeleted && asPerson(viewer) != nil && asPerson(viewer).Admin
	vs := visibleSet{ByID: map[uint]*models.Event{}, Groups: map[uint]*models.Group{}, Rels: map[uint]policy.Rel{}, Hosted: map[uint]bool{}}
	var evs []models.Event
	if err := q.Find(&evs).Error; err != nil || len(evs) == 0 {
		return vs, err
	}
	gset := map[uint]struct{}{}
	ids := make([]uint, 0, len(evs))
	for i := range evs {
		ids = append(ids, evs[i].ID)
		if evs[i].GroupID != nil {
			gset[*evs[i].GroupID] = struct{}{}
		}
	}
	if withDeleted {
		vs.Groups = a.groupsByIDUnscoped(gset)
	} else {
		vs.Groups = a.groupsByID(gset)
	}
	vs.Rels = a.relsFor(viewer, gset)
	vs.Hosted = a.hostedEventIDs(viewer, ids)
	p := asPerson(viewer)
	for i := range evs {
		ev := evs[i]
		grp, rel := groupAndRel(&ev, vs.Groups, vs.Rels)
		if ev.GroupID != nil && grp == nil {
			continue
		}
		gp := policy.Group{}
		if grp != nil {
			gp = asGroup(grp)
		}
		if policy.CanSeeEvent(p, viewOf(&ev, vs.Hosted[ev.ID]), gp, rel) {
			vs.Events = append(vs.Events, ev)
		}
	}
	for i := range vs.Events {
		vs.ByID[vs.Events[i].ID] = &vs.Events[i]
		vs.IDs = append(vs.IDs, vs.Events[i].ID)
	}
	return vs, nil
}

// groupsByIDUnscoped is groupsByID including soft-deleted groups, for the
// admin "show deleted events" switch only (DEC-68).
func (a *App) groupsByIDUnscoped(ids map[uint]struct{}) map[uint]*models.Group {
	out := map[uint]*models.Group{}
	if len(ids) == 0 {
		return out
	}
	list := make([]uint, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	var gs []models.Group
	a.DB.Unscoped().Where("id IN ?", list).Find(&gs)
	for i := range gs {
		out[gs[i].ID] = &gs[i]
	}
	return out
}
