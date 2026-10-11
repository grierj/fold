package main

import (
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
	source := ev.VisibilitySource
	if source == "" {
		source = policy.SourceGroup
	}
	if ev.GroupID != nil {
		host = false
	}
	return policy.EventView{
		ChurchWide: ev.GroupID == nil,
		Visibility: ev.Visibility,
		Source:     source,
		Host:       host,
	}
}

// shownVisibility is what the API reports. Source group follows the group
// even when the stored column is a step behind.
func shownVisibility(ev *models.Event, g *models.Group) string {
	if g != nil && ev.VisibilitySource != models.VisibilitySourceCustom {
		return g.Visibility
	}
	return ev.Visibility
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
	f := eventFacts{}
	if ev.GroupID != nil {
		var g models.Group
		if err := a.DB.First(&g, *ev.GroupID).Error; err == nil {
			f.group = &g
			f.rel = a.relOf(u, g.ID)
		}
	}
	host := false
	if ev.GroupID == nil && u != nil {
		var n int64
		a.DB.Model(&models.EventHost{}).Where("event_id = ? AND user_id = ?", ev.ID, u.ID).Count(&n)
		host = n > 0
	}
	f.view = viewOf(ev, host)
	return f
}

// presentGroup hides a group the viewer cannot see. An opened invite-only
// event then says it is hosted by the church and names nobody (DEC-17).
func (a *App) presentGroup(inst *occurrenceJSON, ev *models.Event, g *models.Group, me *models.User, rel policy.Rel) {
	if ev.GroupID == nil {
		inst.HostedBy = a.siteName()
		return
	}
	if g != nil && policy.CanSeeGroup(asPerson(me), asGroup(g), rel) {
		gid := *ev.GroupID
		inst.GroupID = &gid
		inst.GroupName = g.Name
		return
	}
	inst.HostedBy = a.siteName()
}

func (a *App) presentEvent(ev *models.Event, me *models.User, f eventFacts) eventJSON {
	j := toEventJSON(ev)
	j.Visibility = shownVisibility(ev, f.group)
	if ev.GroupID == nil || f.group == nil || !policy.CanSeeGroup(asPerson(me), asGroup(f.group), f.rel) {
		j.GroupID = nil
		j.GroupName = ""
		j.HostedBy = a.siteName()
	} else {
		j.GroupName = f.group.Name
		j.GroupVisibility = f.group.Visibility
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
	gp := policy.Group{}
	if f.group != nil {
		gp = asGroup(f.group)
	}
	if f.view.ChurchWide {
		setVis = policy.CanSetEventVisibility(p, f.view, gp, f.rel, models.VisibilityPublic, policy.SourceCustom) ||
			policy.CanSetEventVisibility(p, f.view, gp, f.rel, models.VisibilityPrivate, policy.SourceCustom)
		return
	}
	if f.group != nil {
		setVis = policy.CanSetEventVisibility(p, f.view, gp, f.rel, f.group.Visibility, policy.SourceGroup)
	}
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
	var evs []models.Event
	if err := a.DB.Find(&evs).Error; err != nil || len(evs) == 0 {
		return nil
	}
	gset := map[uint]struct{}{}
	ids := make([]uint, 0, len(evs))
	for i := range evs {
		ids = append(ids, evs[i].ID)
		if evs[i].GroupID != nil {
			gset[*evs[i].GroupID] = struct{}{}
		}
	}
	groups := a.groupsByID(gset)
	rels := a.relsFor(u, gset)
	hosted := a.hostedEventIDs(u, ids)
	p := asPerson(u)
	var out []uint
	for i := range evs {
		ev := &evs[i]
		g, rel := groupAndRel(ev, groups, rels)
		if ev.GroupID != nil && g == nil {
			continue
		}
		gp := policy.Group{}
		if g != nil {
			gp = asGroup(g)
		}
		if !policy.CanSeeEvent(p, viewOf(ev, hosted[ev.ID]), gp, rel) {
			continue
		}
		if ev.GroupID == nil || !policy.CanSeeGroup(p, gp, rel) {
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
		return nil, http.StatusNotFound, "user not found"
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
		Visibility: vis, VisibilitySource: models.VisibilitySourceCustom, CreatedBy: creator.ID,
	}
	applySpec(&ev, spec, body.eventBody)
	if !textOK(c, ev.Title, ev.Location) {
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

func (a *App) apiSetEventVisibility(c *gin.Context) {
	ev, f, ok := a.loadVisibleEvent(c)
	if !ok {
		return
	}
	var body struct {
		Visibility string `json:"visibility"`
		Source     string `json:"source"`
		Reason     string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid body")
		return
	}
	source := strings.TrimSpace(body.Source)
	vis := strings.TrimSpace(body.Visibility)
	if f.view.ChurchWide && source == "" {
		source = policy.SourceCustom
	}
	if f.group != nil && source == policy.SourceGroup {
		vis = f.group.Visibility
	}
	reason := strings.TrimSpace(body.Reason)
	if len(reason) > 500 {
		jsonError(c, http.StatusBadRequest, "reason is too long")
		return
	}
	me := apiUser(c)
	gp := policy.Group{}
	if f.group != nil {
		gp = asGroup(f.group)
	}
	if !policy.CanManageEvent(asPerson(me), f.view, f.rel) {
		jsonError(c, http.StatusForbidden, "you can't change this event")
		return
	}
	if !policy.CanSetEventVisibility(asPerson(me), f.view, gp, f.rel, vis, source) {
		jsonError(c, http.StatusBadRequest, "that visibility isn't allowed on this event")
		return
	}
	if ev.Visibility == vis && ev.VisibilitySource == source {
		c.JSON(http.StatusOK, gin.H{"event": a.presentEvent(ev, me, f)})
		return
	}
	action := audit.VisibilityChanged
	if f.group != nil && source == policy.SourceCustom && policy.MoreOpen(vis, f.group.Visibility) {
		action = audit.VisibilityOverride
	}
	oldVis, oldSource := ev.Visibility, ev.VisibilitySource
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var locked models.Event
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, ev.ID).Error; err != nil {
			return err
		}
		locked.Visibility = vis
		locked.VisibilitySource = source
		if err := tx.Save(&locked).Error; err != nil {
			return err
		}
		*ev = locked
		meta := map[string]any{
			"oldVisibility": oldVis,
			"newVisibility": vis,
			"oldSource":     oldSource,
			"newSource":     source,
		}
		if f.group != nil {
			meta["groupVisibility"] = f.group.Visibility
		}
		if reason != "" {
			meta["reason"] = reason
		}
		return audit.Write(tx, audit.Entry{
			Actor: &me.ID, Action: action, TargetType: "event", TargetID: ev.ID,
			Meta: meta, IP: c.ClientIP(),
		})
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not update visibility")
		return
	}
	f.view.Visibility = vis
	f.view.Source = source
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
