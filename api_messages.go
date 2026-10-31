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
	// Error says why it failed, or why some people didn't get it (M3.5):
	// the pipeline's words or the provider's error, never an address.
	Error string `json:"error"`
	// FailedCount is how many people a send failed for: "Partly sent".
	FailedCount int `json:"failedCount"`
}

// Composer limits, matching the CHECKs in migration 00054.
const (
	maxMessageSubject = 200
	maxMessageHTML    = 100000
	maxMessageText    = 50000
	// A message can be scheduled up to a year ahead.
	maxScheduleAhead = 366 * 24 * time.Hour
)

const (
	errMessageSubject  = "add a subject on one line, up to 200 characters"
	errMessageBody     = "write a message of under about 50,000 characters"
	errMessageNobody   = "no one would get this message; choose people who can get email"
	errMessageLocked   = "this message is already sending or sent, so it can't change"
	errMessageWhen     = "pick a time from now to a year ahead"
	errMessageChannel  = "texting isn't set up yet; send this as an email"
	errMessageNotYours = "you can't send to those people"
)

// messageFacts is what a list of messages shows beyond their own rows:
// each author's name, and how many people each send failed for.
type messageFacts struct {
	authors map[uint]string
	failed  map[uint]int
}

// loadMessageFacts reads them for msgs in two queries, whatever the count.
func (a *App) loadMessageFacts(msgs []models.Message) (messageFacts, error) {
	out := messageFacts{authors: map[uint]string{}, failed: map[uint]int{}}
	ids, settled := []uint{}, []uint{}
	for _, m := range msgs {
		if m.AuthorID != nil {
			ids = append(ids, *m.AuthorID)
		}
		if m.Status == models.MessageSent || m.Status == models.MessageFailed {
			settled = append(settled, m.ID)
		}
	}
	if len(ids) > 0 {
		var users []models.User
		if err := a.DB.Select("id, name, display_name").Where("id IN ?", dedupeIDs(ids)).Find(&users).Error; err != nil {
			return out, err
		}
		for _, u := range users {
			out.authors[u.ID] = u.PublicName()
		}
	}
	if len(settled) > 0 {
		var counts []struct {
			MessageID uint
			N         int
		}
		if err := a.DB.Model(&models.MessageRecipient{}).Select("message_id, count(*) AS n").
			Where("message_id IN ? AND status = ?", settled, models.RecipientFailed).Group("message_id").Scan(&counts).Error; err != nil {
			return out, err
		}
		for _, c := range counts {
			out.failed[c.MessageID] = c.N
		}
	}
	return out, nil
}

func toMessageJSON(m *models.Message, me *models.User, facts messageFacts) messageJSON {
	var au audienceJSON
	_ = json.Unmarshal([]byte(m.Audience), &au)
	isAuthor := m.AuthorID != nil && *m.AuthorID == me.ID
	name := ""
	if m.AuthorID != nil {
		name = facts.authors[*m.AuthorID]
	}
	return messageJSON{ID: m.ID, Channel: m.Channel, Status: m.Status, Subject: m.Subject, BodyHTML: m.BodyHTML,
		Audience: au, ScheduledAt: m.ScheduledAt, SentAt: m.SentAt, AuthorName: name,
		Editable:  (m.Status == models.MessageDraft || m.Status == models.MessageScheduled) && policy.CanEditMessage(asPerson(me), isAuthor),
		UpdatedAt: m.UpdatedAt, Error: messageErrorFor(m.Error, policy.IsAdmin(asPerson(me))), FailedCount: facts.failed[m.ID]}
}

// messageErrorFor is a stored error as the viewer reads it: the
// pipeline's own plain texts (ownMessageErrors) as they are, anything else
// (a provider's error) in plain words unless the viewer is an admin.
func messageErrorFor(stored string, admin bool) string {
	if stored == "" || ownMessageErrors[stored] || admin {
		return stored
	}
	return whyProvider
}

// respondMessage re-reads m and answers with it.
func (a *App) respondMessage(c *gin.Context, status int, m *models.Message, extra gin.H) {
	if err := a.DB.First(m, m.ID).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the message")
		return
	}
	facts, err := a.loadMessageFacts([]models.Message{*m})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the message")
		return
	}
	out := gin.H{"message": toMessageJSON(m, apiUser(c), facts)}
	for k, v := range extra {
		out[k] = v
	}
	c.JSON(status, out)
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
	ids, err := a.resolveAudience(me, au, time.Now())
	switch {
	case errors.Is(err, errAudienceInvalid):
		jsonError(c, http.StatusBadRequest, errAudienceInvalid.Error())
	case errors.Is(err, errAudienceGone) && policy.IsAdmin(asPerson(me)):
		jsonError(c, http.StatusNotFound, errAudienceGone.Error())
	case errors.Is(err, errAudienceGone), errors.Is(err, errAudienceRefused):
		// Only an admin learns a target is gone; anyone else hears what a
		// target that isn't theirs answers, so ids can't be probed.
		jsonError(c, http.StatusForbidden, errMessageNotYours)
	case err != nil:
		jsonError(c, http.StatusInternalServerError, "could not check who gets this message")
	default:
		return ids, true
	}
	return nil, false
}

// errAudienceRefused: me may not message that audience (policy).
var errAudienceRefused = errors.New("not allowed to message that audience")

// resolveAudience normalizes au, checks that me may message it
// (policy.CanMessageAudience) and returns who it reaches. The handlers and
// message.send (at send time, PLAN 7.1 step 1) share it. Errors are
// errAudienceInvalid, errAudienceGone, errAudienceRefused or a database
// error.
func (a *App) resolveAudience(me *models.User, au *audienceJSON, now time.Time) ([]uint, error) {
	if err := au.normalize(); err != nil {
		return nil, errAudienceInvalid
	}
	targets, err := a.audienceTargets(a.DB, me, *au)
	if err != nil {
		return nil, err
	}
	if !policy.CanMessageAudience(asPerson(me), au.Type, targets) {
		return nil, errAudienceRefused
	}
	return a.audienceUserIDs(a.DB, *au, now)
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

// composeOptionsJSON is GET /messages/compose: who the author may write to.
type composeOptionsJSON struct {
	CanCompose         bool `json:"canCompose"`
	CanMessageEveryone bool `json:"canMessageEveryone"`
	// SmsEnabled shows the composer's SMS tab. Always false until an SMS
	// provider can be set up (M3.7, DEC-117).
	SmsEnabled bool               `json:"smsEnabled"`
	Groups     []composeGroupJSON `json:"groups"`
	Events     []composeEventJSON `json:"events"`
}

// How much the picker lists.
const (
	maxComposeEvents = 200
	maxComposeDates  = 10
)

// apiComposeOptions lists only what the author may message (M3.4
// acceptance): admins every active group and event with upcoming dates,
// leaders their own active groups' groups and events, hosts the church-wide
// events they host. ?event=<id> narrows it to that one event, for a page
// that only asks whether the viewer may write to it (the date page): the
// same rules, no groups, and no events when they may not.
func (a *App) apiComposeOptions(c *gin.Context) {
	me := apiUser(c)
	p := asPerson(me)
	var onlyEvent uint64
	if raw := c.Query("event"); raw != "" {
		n, err := strconv.ParseUint(raw, 10, 63)
		if err != nil || n == 0 {
			jsonError(c, http.StatusBadRequest, "invalid request")
			return
		}
		onlyEvent = n
	}
	ok, err := a.canCompose(me)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the composer")
		return
	}
	out := composeOptionsJSON{CanCompose: ok, CanMessageEveryone: policy.IsAdmin(p), Groups: []composeGroupJSON{}, Events: []composeEventJSON{}}
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
	if onlyEvent > 0 {
		eq = eq.Where("id = ?", onlyEvent)
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
	out.Groups, out.Events = gjson, ejson
	if onlyEvent > 0 {
		out.Groups = []composeGroupJSON{}
	}
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
	s, err := a.summarizeAudience(a.DB, ids, policy.IsAdmin(asPerson(apiUser(c))))
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
	// Bound the input before sanitizing it, then what is stored: escaping
	// and link addresses can make either longer (the CHECKs count characters).
	if len(body.BodyHTML) > 4*maxMessageHTML {
		jsonError(c, http.StatusBadRequest, errMessageBody)
		return false
	}
	clean := content.SanitizeHTML(body.BodyHTML)
	text := content.PlainText(clean)
	if utf8.RuneCountInString(clean) > maxMessageHTML || utf8.RuneCountInString(text) > maxMessageText {
		jsonError(c, http.StatusBadRequest, errMessageBody)
		return false
	}
	if _, ok := a.checkAudience(c, me, &body.Audience); !ok {
		return false
	}
	if msg := mergeFieldError(subject+"\n"+clean, body.Audience); msg != "" {
		jsonError(c, http.StatusBadRequest, msg)
		return false
	}
	aud, _ := json.Marshal(body.Audience)
	m.Channel, m.Subject, m.BodyHTML, m.BodyText, m.Audience = body.Channel, subject, clean, text, string(aud)
	return true
}

// sendable checks that m could go out now: a subject, a body and at least
// one person who can get it. It answers who it reaches; false means the
// error response was sent.
func (a *App) sendable(c *gin.Context, me *models.User, m *models.Message) (audienceJSON, audienceSummaryJSON, bool) {
	var au audienceJSON
	if m.Subject == "" {
		jsonError(c, http.StatusBadRequest, errMessageSubject)
		return au, audienceSummaryJSON{}, false
	}
	if strings.TrimSpace(m.BodyText) == "" {
		jsonError(c, http.StatusBadRequest, errMessageBody)
		return au, audienceSummaryJSON{}, false
	}
	_ = json.Unmarshal([]byte(m.Audience), &au)
	ids, ok := a.checkAudience(c, me, &au)
	if !ok {
		return au, audienceSummaryJSON{}, false
	}
	s, err := a.summarizeAudience(a.DB, ids, policy.IsAdmin(asPerson(me)))
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not count who gets this message")
		return au, s, false
	}
	if s.Eligible == 0 {
		jsonError(c, http.StatusBadRequest, errMessageNobody)
		return au, s, false
	}
	return au, s, true
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
	a.respondMessage(c, http.StatusCreated, &m, nil)
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
		a.respondMessage(c, http.StatusOK, m, nil)
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
	facts, err := a.loadMessageFacts(rows)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list messages")
		return
	}
	out := make([]messageJSON, 0, len(rows))
	for i := range rows {
		out = append(out, toMessageJSON(&rows[i], me, facts))
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
	fields := map[string]any{"channel": m.Channel, "subject": m.Subject, "body_html": m.BodyHTML, "body_text": m.BodyText,
		"audience": m.Audience, "updated_at": time.Now()}
	// A scheduled message stays scheduled only while it could still go out,
	// and its count follows the new audience.
	scheduled := m.Status == models.MessageScheduled
	if scheduled {
		au, s, ok := a.sendable(c, me, m)
		if !ok {
			return
		}
		// A new audience counts against its own group's daily limit.
		if full, err := a.dailyLimitReached(me, m.ID, au, *m.ScheduledAt); err != nil {
			jsonError(c, http.StatusInternalServerError, "could not save the message")
			return
		} else if full {
			jsonError(c, http.StatusBadRequest, errMessageDailyLimit)
			return
		}
		fields["recipient_count"] = s.Eligible
	}
	isAuthor := m.AuthorID != nil && *m.AuthorID == me.ID
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.Message{}).Where("id = ? AND status = ?", m.ID, m.Status).Updates(fields)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errLocked
		}
		// Changing what is already scheduled, or someone else's message, is
		// worth a record; a draft's own edits are not.
		if !scheduled && isAuthor {
			return nil
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.MessageEdited, TargetType: "message", TargetID: m.ID,
			Meta: map[string]any{"status": m.Status, "byOther": !isAuthor}, IP: c.ClientIP()})
	})
	if errors.Is(err, errLocked) {
		jsonError(c, http.StatusConflict, errMessageLocked)
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save the message")
		return
	}
	a.respondMessage(c, http.StatusOK, m, nil)
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
	me := apiUser(c)
	au, s, ok := a.sendable(c, me, m)
	if !ok {
		return
	}
	if full, err := a.dailyLimitReached(me, m.ID, au, when); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not schedule the message")
		return
	} else if full {
		jsonError(c, http.StatusBadRequest, errMessageDailyLimit)
		return
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.Message{}).Where("id = ? AND status IN ?", m.ID, []string{models.MessageDraft, models.MessageScheduled}).
			Updates(map[string]any{"status": models.MessageScheduled, "scheduled_at": when, "recipient_count": s.Eligible, "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errLocked
		}
		if err := enqueueMessageSend(tx, m.ID, when); err != nil {
			return err
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
	a.respondMessage(c, http.StatusOK, m, gin.H{"summary": s})
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
	a.respondMessage(c, http.StatusOK, m, nil)
}
