package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"fold/internal/audit"
	"fold/internal/content"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// The composer's API (M3.4, PLAN 7.1–7.3). Leaders, hosts and admins write
// announcements to the people they may reach (policy.CanMessageAudience),
// save drafts, and schedule them for now or later; M3.5 sends them. Email
// only for now: texting stays hidden until an SMS provider exists (DEC-117).

type messageJSON struct {
	ID          uint         `json:"id"`
	Channel     string       `json:"channel"`
	Status      string       `json:"status"`
	Subject     string       `json:"subject"`
	BodyHTML    string       `json:"bodyHtml"`
	Audience    audienceJSON `json:"audience"`
	ScheduledAt *time.Time   `json:"scheduledAt"`
	SentAt      *time.Time   `json:"sentAt"`
	AuthorName  string       `json:"authorName"`
	// Editable: still a draft or scheduled, and the viewer may change it.
	Editable  bool      `json:"editable"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Composer limits, matching the CHECKs in migration 00054.
const (
	maxMessageSubject = 200
	maxMessageHTML    = 100000
	// A message can be scheduled up to a year ahead.
	maxScheduleAhead = 366 * 24 * time.Hour
)

const (
	errMessageSubject  = "add a subject of at most 200 characters"
	errMessageBody     = "write a message (it can be up to about 100,000 characters)"
	errMessageNobody   = "no one would get this message; choose people who can get email"
	errMessageLocked   = "this message is already sending or sent, so it can't change"
	errMessageWhen     = "pick a time from now to a year ahead"
	errMessageChannel  = "texting isn't set up yet; send this as an email"
	errMessageNotYours = "you can't send to those people"
)

func (a *App) messageAuthorName(id *uint) string {
	if id == nil {
		return ""
	}
	var u models.User
	if a.DB.Select("id, name, display_name").First(&u, *id).Error != nil {
		return ""
	}
	return u.PublicName()
}

func (a *App) toMessageJSON(m *models.Message, me *models.User) messageJSON {
	var au audienceJSON
	_ = json.Unmarshal([]byte(m.Audience), &au)
	isAuthor := m.AuthorID != nil && *m.AuthorID == me.ID
	return messageJSON{ID: m.ID, Channel: m.Channel, Status: m.Status, Subject: m.Subject, BodyHTML: m.BodyHTML,
		Audience: au, ScheduledAt: m.ScheduledAt, SentAt: m.SentAt, AuthorName: a.messageAuthorName(m.AuthorID),
		Editable:  (m.Status == models.MessageDraft || m.Status == models.MessageScheduled) && policy.CanEditMessage(asPerson(me), isAuthor),
		UpdatedAt: m.UpdatedAt}
}

// canCompose: the composer is for admins and anyone who leads an approved
// group or hosts a church-wide event.
func (a *App) canCompose(me *models.User) (bool, error) {
	leads, err := leadsApprovedGroup(a.DB, me.ID)
	if err != nil {
		return false, err
	}
	var hosts int64
	if err := a.DB.Model(&models.EventHost{}).Joins("JOIN events e ON e.id = event_hosts.event_id AND e.deleted_at IS NULL").
		Where("event_hosts.user_id = ?", me.ID).Count(&hosts).Error; err != nil {
		return false, err
	}
	return policy.CanComposeMessages(asPerson(me), leads, hosts > 0), nil
}

// checkAudience normalizes au and answers whether me may send to it, and
// who it reaches; false means the error response was sent.
func (a *App) checkAudience(c *gin.Context, me *models.User, au *audienceJSON) ([]uint, bool) {
	if err := au.normalize(); err != nil {
		jsonError(c, http.StatusBadRequest, errAudienceInvalid.Error())
		return nil, false
	}
	targets, err := a.audienceTargets(a.DB, me, *au)
	if errors.Is(err, errAudienceGone) {
		jsonError(c, http.StatusNotFound, errAudienceGone.Error())
		return nil, false
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not check who gets this message")
		return nil, false
	}
	if !policy.CanMessageAudience(asPerson(me), au.Type, targets) {
		jsonError(c, http.StatusForbidden, errMessageNotYours)
		return nil, false
	}
	ids, err := a.audienceUserIDs(a.DB, *au, time.Now())
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not check who gets this message")
		return nil, false
	}
	return ids, true
}

// composeGroupJSON / composeEventJSON are what the picker offers.
type composeGroupJSON struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
}

type composeDateJSON struct {
	OccurrenceID uint      `json:"occurrenceId"`
	Date         string    `json:"date"`
	StartsAt     time.Time `json:"startsAt"`
}

type composeEventJSON struct {
	ID         uint              `json:"id"`
	Title      string            `json:"title"`
	GroupName  string            `json:"groupName,omitempty"`
	ChurchWide bool              `json:"churchWide"`
	Dates      []composeDateJSON `json:"dates"`
}

// How much the picker lists.
const (
	maxComposeEvents = 200
	maxComposeDates  = 10
)

// apiComposeOptions lists only what the author may message (M3.4
// acceptance): admins every active group and event with upcoming dates,
// leaders their own active groups' groups and events, hosts the church-wide
// events they host.
func (a *App) apiComposeOptions(c *gin.Context) {
	me := apiUser(c)
	p := asPerson(me)
	ok, err := a.canCompose(me)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the composer")
		return
	}
	out := gin.H{"canCompose": ok, "canMessageEveryone": policy.IsAdmin(p), "groups": []composeGroupJSON{}, "events": []composeEventJSON{}}
	if !ok {
		c.JSON(http.StatusOK, out)
		return
	}
	groups := []models.Group{}
	gq := a.DB.Where("status = ?", models.GroupActive).Order("lower(name)")
	if !p.Admin {
		gq = gq.Where("id IN (SELECT group_id FROM memberships WHERE user_id = ? AND role = ? AND status = 'active')", me.ID, models.MembershipLeader)
	}
	if err := gq.Find(&groups).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the composer")
		return
	}
	names := map[uint]string{}
	gjson := []composeGroupJSON{}
	gids := []uint{}
	for _, g := range groups {
		gjson = append(gjson, composeGroupJSON{ID: g.ID, Name: g.Name}) // group-ok: groups the author leads, or an admin's
		names[g.ID] = g.Name                                            // group-ok: same
		gids = append(gids, g.ID)
	}
	now := time.Now()
	eq := a.DB.Where("id IN (SELECT event_id FROM event_occurrences WHERE status = ? AND starts_at >= ?)", models.OccurrenceScheduled, now)
	if !p.Admin {
		eq = eq.Where("(group_id IN ?) OR (group_id IS NULL AND id IN (SELECT event_id FROM event_hosts WHERE user_id = ?))", append(gids, 0), me.ID)
	} else {
		eq = eq.Where("group_id IS NULL OR group_id IN ?", append(gids, 0))
	}
	var evs []models.Event
	if err := eq.Order("lower(title), id").Limit(maxComposeEvents).Find(&evs).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the composer")
		return
	}
	// Each event's next few dates, in one read.
	eids := make([]uint, 0, len(evs))
	for _, ev := range evs {
		eids = append(eids, ev.ID)
	}
	var dates []struct {
		EventID      uint
		ID           uint
		OriginalDate string
		StartsAt     time.Time
	}
	if err := a.DB.Raw(`SELECT event_id, id, original_date, starts_at FROM (
		SELECT event_id, id, original_date, starts_at, row_number() OVER (PARTITION BY event_id ORDER BY starts_at, id) AS n
		FROM event_occurrences WHERE event_id IN ? AND status = ? AND starts_at >= ?) d WHERE n <= ? ORDER BY starts_at, id`,
		append(eids, 0), models.OccurrenceScheduled, now, maxComposeDates).Scan(&dates).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the composer")
		return
	}
	byEvent := map[uint][]composeDateJSON{}
	for _, d := range dates {
		byEvent[d.EventID] = append(byEvent[d.EventID], composeDateJSON{OccurrenceID: d.ID, Date: d.OriginalDate, StartsAt: d.StartsAt})
	}
	ejson := []composeEventJSON{}
	for _, ev := range evs {
		e := composeEventJSON{ID: ev.ID, Title: ev.Title, ChurchWide: ev.GroupID == nil, Dates: []composeDateJSON{}}
		if ev.GroupID != nil {
			e.GroupName = names[*ev.GroupID]
		}
		if d := byEvent[ev.ID]; d != nil {
			e.Dates = d
		}
		ejson = append(ejson, e)
	}
	out["groups"], out["events"] = gjson, ejson
	c.JSON(http.StatusOK, out)
}

// apiAudiencePreview counts who an audience reaches and who will be
// skipped, with Teen badges and a teen count (DEC-20).
func (a *App) apiAudiencePreview(c *gin.Context) {
	var body struct {
		Audience audienceJSON `json:"audience"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, errAudienceInvalid.Error())
		return
	}
	ids, ok := a.checkAudience(c, apiUser(c), &body.Audience)
	if !ok {
		return
	}
	s, err := a.summarizeAudience(a.DB, body.Audience, ids)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not count who gets this message")
		return
	}
	c.JSON(http.StatusOK, gin.H{"summary": s})
}

type messageBody struct {
	Channel  string       `json:"channel"`
	Subject  string       `json:"subject"`
	BodyHTML string       `json:"bodyHtml"`
	Audience audienceJSON `json:"audience"`
}

// apply validates and copies a draft's fields onto m; false means the
// error response was sent. The HTML is sanitized before it is stored.
func (a *App) applyMessageBody(c *gin.Context, me *models.User, m *models.Message) bool {
	var body messageBody
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return false
	}
	if body.Channel == "" {
		body.Channel = "email"
	}
	if body.Channel != "email" {
		jsonError(c, http.StatusBadRequest, errMessageChannel)
		return false
	}
	subject := strings.TrimSpace(body.Subject)
	if utf8.RuneCountInString(subject) > maxMessageSubject || strings.ContainsAny(subject, "\r\n") {
		jsonError(c, http.StatusBadRequest, errMessageSubject)
		return false
	}
	if len(body.BodyHTML) > maxMessageHTML {
		jsonError(c, http.StatusBadRequest, errMessageBody)
		return false
	}
	if _, ok := a.checkAudience(c, me, &body.Audience); !ok {
		return false
	}
	clean := content.SanitizeHTML(body.BodyHTML)
	aud, _ := json.Marshal(body.Audience)
	m.Channel, m.Subject, m.BodyHTML, m.BodyText, m.Audience = body.Channel, subject, clean, content.PlainText(clean), string(aud)
	return true
}

func (a *App) apiCreateMessage(c *gin.Context) {
	me := apiUser(c)
	if ok, err := a.canCompose(me); err != nil || !ok {
		jsonError(c, http.StatusForbidden, errMessageNotYours)
		return
	}
	m := models.Message{AuthorID: &me.ID, Status: models.MessageDraft, Category: "announcement"}
	if !a.applyMessageBody(c, me, &m) {
		return
	}
	if err := a.DB.Create(&m).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save the message")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"message": a.toMessageJSON(&m, me)})
}

// loadMessage finds a message the viewer may see: its author's, or any for
// an admin. Anyone else gets 404.
func (a *App) loadMessage(c *gin.Context) (*models.Message, bool) {
	me := apiUser(c)
	id, err := strconv.ParseUint(c.Param("id"), 10, 63)
	var m models.Message
	if err != nil || a.DB.First(&m, id).Error != nil || !policy.CanEditMessage(asPerson(me), m.AuthorID != nil && *m.AuthorID == me.ID) {
		jsonError(c, http.StatusNotFound, "message not found")
		return nil, false
	}
	return &m, true
}

func (a *App) apiGetMessage(c *gin.Context) {
	if m, ok := a.loadMessage(c); ok {
		c.JSON(http.StatusOK, gin.H{"message": a.toMessageJSON(m, apiUser(c))})
	}
}

// apiListMessages lists the viewer's messages (an admin's list has
// everyone's), newest first.
func (a *App) apiListMessages(c *gin.Context) {
	me := apiUser(c)
	q := a.DB.Order("updated_at DESC, id DESC").Limit(200)
	if !policy.IsAdmin(asPerson(me)) {
		q = q.Where("author_id = ?", me.ID)
	}
	var rows []models.Message
	if err := q.Find(&rows).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list messages")
		return
	}
	out := make([]messageJSON, 0, len(rows))
	for i := range rows {
		out = append(out, a.toMessageJSON(&rows[i], me))
	}
	c.JSON(http.StatusOK, gin.H{"messages": out})
}

// editable reports whether m can still change; false means 409 was sent.
func editable(c *gin.Context, m *models.Message) bool {
	if m.Status != models.MessageDraft && m.Status != models.MessageScheduled {
		jsonError(c, http.StatusConflict, errMessageLocked)
		return false
	}
	return true
}

// apiUpdateMessage edits a draft or a scheduled message; the audience is
// checked again, since roles change.
func (a *App) apiUpdateMessage(c *gin.Context) {
	m, ok := a.loadMessage(c)
	if !ok || !editable(c, m) {
		return
	}
	me := apiUser(c)
	if !a.applyMessageBody(c, me, m) {
		return
	}
	res := a.DB.Model(&models.Message{}).Where("id = ? AND status IN ?", m.ID, []string{models.MessageDraft, models.MessageScheduled}).
		Updates(map[string]any{"channel": m.Channel, "subject": m.Subject, "body_html": m.BodyHTML, "body_text": m.BodyText,
			"audience": m.Audience, "updated_at": time.Now()})
	if res.Error != nil {
		jsonError(c, http.StatusInternalServerError, "could not save the message")
		return
	}
	if res.RowsAffected == 0 {
		jsonError(c, http.StatusConflict, errMessageLocked)
		return
	}
	a.DB.First(m, m.ID)
	c.JSON(http.StatusOK, gin.H{"message": a.toMessageJSON(m, me)})
}

// apiScheduleMessage sets a draft (or a scheduled message) to go out at a
// time, or now. It needs a subject, a body and at least one person who can
// get it; M3.5 sends it.
func (a *App) apiScheduleMessage(c *gin.Context) {
	m, ok := a.loadMessage(c)
	if !ok || !editable(c, m) {
		return
	}
	var body struct {
		ScheduledAt *time.Time `json:"scheduledAt"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request")
		return
	}
	now := time.Now()
	when := now
	if body.ScheduledAt != nil {
		when = body.ScheduledAt.UTC()
		if when.Before(now.Add(-time.Minute)) || when.After(now.Add(maxScheduleAhead)) {
			jsonError(c, http.StatusBadRequest, errMessageWhen)
			return
		}
		if when.Before(now) {
			when = now
		}
	}
	if m.Subject == "" {
		jsonError(c, http.StatusBadRequest, errMessageSubject)
		return
	}
	if strings.TrimSpace(m.BodyText) == "" {
		jsonError(c, http.StatusBadRequest, errMessageBody)
		return
	}
	me := apiUser(c)
	var au audienceJSON
	_ = json.Unmarshal([]byte(m.Audience), &au)
	ids, ok := a.checkAudience(c, me, &au)
	if !ok {
		return
	}
	s, err := a.summarizeAudience(a.DB, au, ids)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not count who gets this message")
		return
	}
	if s.Eligible == 0 {
		jsonError(c, http.StatusBadRequest, errMessageNobody)
		return
	}
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.Message{}).Where("id = ? AND status IN ?", m.ID, []string{models.MessageDraft, models.MessageScheduled}).
			Updates(map[string]any{"status": models.MessageScheduled, "scheduled_at": when, "recipient_count": s.Eligible, "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errLocked
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.MessageScheduled, TargetType: "message", TargetID: m.ID,
			Meta: map[string]any{"audience": au.Type, "recipients": s.Eligible, "at": when}, IP: c.ClientIP()})
	})
	if errors.Is(err, errLocked) {
		jsonError(c, http.StatusConflict, errMessageLocked)
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not schedule the message")
		return
	}
	a.DB.First(m, m.ID)
	c.JSON(http.StatusOK, gin.H{"message": a.toMessageJSON(m, me), "summary": s})
}

var errLocked = errors.New("message locked")

// apiCancelMessage stops a draft or a scheduled message for good.
func (a *App) apiCancelMessage(c *gin.Context) {
	m, ok := a.loadMessage(c)
	if !ok || !editable(c, m) {
		return
	}
	me := apiUser(c)
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.Message{}).Where("id = ? AND status IN ?", m.ID, []string{models.MessageDraft, models.MessageScheduled}).
			Updates(map[string]any{"status": models.MessageCancelled, "updated_at": time.Now()})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errLocked
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.MessageCancelled, TargetType: "message", TargetID: m.ID, IP: c.ClientIP()})
	})
	if errors.Is(err, errLocked) {
		jsonError(c, http.StatusConflict, errMessageLocked)
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not cancel the message")
		return
	}
	a.DB.First(m, m.ID)
	c.JSON(http.StatusOK, gin.H{"message": a.toMessageJSON(m, me)})
}
