package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/content"
	"fold/internal/mail"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/internal/providers"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Preview and "Send a test to me" (M3.4b, PLAN 7.3). Both render a saved
// message as its email: merge fields filled with the author's own values
// (their first name; for one event's people, that event's next date and
// link), the mobile-safe layout, and the footer an announcement carries.
// M3.5's sender renders the same way with each reader's values.

const (
	errMergeUnknown        = "use only the merge fields the composer offers"
	errMergeEvent          = "event merge fields only work in a message to one event's people"
	errMessageTestLimit    = "you've sent 10 tests in the last hour"
	errMessageTestEmail    = "your own email address can't get mail right now"
	errMessageTestOptedOut = "this kind of email is turned off in your notification settings"
	errMessageTestFail     = "we couldn't send the test"
)

// testSendsPerHour bounds "Send a test to me" for one person.
const testSendsPerHour = 10

// mergeFieldError checks the fields s uses: each must be known, and event
// fields need a message to one event's people (one date, or one event's
// dates). "" means they are fine.
func mergeFieldError(s string, au audienceJSON) string {
	oneEvent := au.Type == policy.AudienceOccurrence || au.Type == policy.AudienceEventSeries
	for _, f := range content.FieldsIn(s) {
		if !content.IsMergeField(f) {
			return errMergeUnknown
		}
		if content.IsEventField(f) && !oneEvent {
			return errMergeEvent
		}
	}
	return ""
}

// renderedMessage is a message as one reader gets it.
type renderedMessage struct {
	Subject string `json:"subject"`
	Text    string `json:"text"`
	HTML    string `json:"-"`
	// Unsubscribe is the one-click link for the List-Unsubscribe header.
	Unsubscribe string `json:"-"`
}

// sampleValues are the merge-field values for previewing m as me.
func (a *App) sampleValues(m *models.Message, me *models.User) map[string]string {
	vals := map[string]string{content.FieldFirstName: firstName(me.PublicName())}
	var au audienceJSON
	_ = json.Unmarshal([]byte(m.Audience), &au)
	ids, _ := a.audienceEventIDs(a.DB, au)
	if len(ids) != 1 || (au.Type != policy.AudienceOccurrence && au.Type != policy.AudienceEventSeries) {
		return vals
	}
	var ev models.Event
	if a.DB.Select("id, title, tzid").First(&ev, ids[0]).Error != nil {
		return vals
	}
	var occ models.EventOccurrence
	q := a.DB.Select("id, starts_at").Where("event_id = ?", ev.ID)
	if au.Type == policy.AudienceOccurrence {
		q = q.Where("id = ?", au.OccurrenceID)
	} else {
		q = q.Where("status = ? AND starts_at >= ?", models.OccurrenceScheduled, time.Now()).Order("starts_at")
	}
	link := a.emailLink("/events/" + historyID(ev.ID))
	vals[content.FieldEventTitle], vals[content.FieldEventLink], vals[content.FieldManageLink] = ev.Title, link, link
	found := q.First(&occ).Error == nil
	if !found && au.Type == policy.AudienceEventSeries {
		// Nothing upcoming: show the latest date, so the preview isn't blank.
		found = a.DB.Select("id, starts_at").Where("event_id = ?", ev.ID).Order("starts_at DESC").First(&occ).Error == nil
	}
	if found {
		loc, err := time.LoadLocation(ev.TZID)
		if err != nil {
			loc = time.UTC
		}
		vals[content.FieldEventTime] = formatWhen(occ.StartsAt, loc)
	}
	return vals
}

// firstName is the first word of a name.
func firstName(name string) string {
	if f := strings.Fields(name); len(f) > 0 {
		return f[0]
	}
	return name
}

// renderMessage renders m for the reader u with the given merge values.
func (a *App) renderMessage(m *models.Message, u *models.User, vals map[string]string) (renderedMessage, error) {
	return a.renderMessageWith(m, u, vals, footerFacts{site: a.siteName(), address: a.churchAddress()})
}

// renderMessageWith renders with the church's name and address already
// read, so a batch reads them once, not once per email.
func (a *App) renderMessageWith(m *models.Message, u *models.User, vals map[string]string, f footerFacts) (renderedMessage, error) {
	site, address := f.site, f.address
	cat := messageCategory(m)
	token := a.unsubscribeToken(u.ID, categoryScope(cat))
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(address, "\r", ""), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	html, err := content.RenderEmail(content.EmailPage{
		Church: site, Subject: content.Merge(m.Subject, vals, false), Body: content.Merge(m.BodyHTML, vals, true),
		Footer:      notify.YoureGetting(cat, site) + ".",
		SettingsURL: a.emailBaseURL() + "/email-settings/" + token, Address: lines,
	})
	if err != nil {
		return renderedMessage{}, err
	}
	return renderedMessage{
		Subject:     strings.TrimSpace(content.Merge(m.Subject, vals, false)),
		Text:        content.Merge(m.BodyText, vals, false) + a.optionalEmailFooter(footerFacts{site: site, address: address}, cat, token),
		HTML:        html,
		Unsubscribe: a.oneClickURL(token),
	}, nil
}

// renderedForViewer loads a message the viewer may see and renders it as
// they would get it. The audience is checked again first, since roles and
// events change after a draft is saved: a preview shows an event's title
// and time only to someone who may still write to its people. false means
// the error response was sent.
func (a *App) renderedForViewer(c *gin.Context) (*models.Message, renderedMessage, bool) {
	m, ok := a.loadOwnMessage(c)
	if !ok {
		return nil, renderedMessage{}, false
	}
	me := apiUser(c)
	var au audienceJSON
	_ = json.Unmarshal([]byte(m.Audience), &au)
	if _, ok := a.checkAudience(c, me, &au); !ok {
		return nil, renderedMessage{}, false
	}
	r, err := a.renderMessage(m, me, a.sampleValues(m, me))
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not show the preview")
		return nil, renderedMessage{}, false
	}
	return m, r, true
}

// apiPreviewMessage answers the subject and plain-text part as the viewer
// would get them; apiPreviewMessageHTML is the HTML part, for an iframe.
func (a *App) apiPreviewMessage(c *gin.Context) {
	if _, r, ok := a.renderedForViewer(c); ok {
		c.JSON(http.StatusOK, gin.H{"preview": r})
	}
}

func (a *App) apiPreviewMessageHTML(c *gin.Context) {
	if _, r, ok := a.renderedForViewer(c); ok {
		serveEmailHTML(c, r.HTML)
	}
}

// apiTestMessage sends the message to the viewer's own address, as an
// announcement goes out (the broadcast stream, with its unsubscribe link),
// with "[Test]" before the subject. Ten tries an hour: the audit row is
// written, under a lock on the viewer's row, before the send, so parallel
// requests can't pass the count together.
func (a *App) apiTestMessage(c *gin.Context) {
	m, r, ok := a.renderedForViewer(c)
	if !ok {
		return
	}
	me := apiUser(c)
	if m.Subject == "" {
		jsonError(c, http.StatusBadRequest, errMessageSubject)
		return
	}
	if strings.TrimSpace(m.BodyText) == "" {
		jsonError(c, http.StatusBadRequest, errMessageBody)
		return
	}
	if strings.TrimSpace(me.Email) == "" {
		jsonError(c, http.StatusBadRequest, errMessageTestEmail)
		return
	}
	errLimit := errors.New("limit")
	errOptedOut := errors.New(errMessageTestOptedOut)
	// D12: the locked session check rejects earlier security changes. The
	// effective choice is read at its statement's start under the lock.
	// D11: a choice committed before that statement starts is honored; a later
	// change may not stop this one in-flight send. The user lock is released at
	// commit, before the provider call. An opt-out, erasure or address change
	// committed after commit and before or during that call may not stop this
	// test send. Every later request rechecks; no user lock spans provider I/O.
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var fresh models.User
		if err := tx.Raw("SELECT id, email, status, session_version, logout_version, deleted_at FROM users WHERE id = ? FOR UPDATE", me.ID).Scan(&fresh).Error; err != nil {
			return err
		}
		if fresh.ID == 0 || fresh.DeletedAt.Valid || fresh.Status != me.Status || fresh.SessionVersion != me.SessionVersion || fresh.LogoutVersion != me.LogoutVersion || mail.NormalizeAddress(fresh.Email) != mail.NormalizeAddress(me.Email) {
			return errSecuritySession
		}
		off, err := readEmailOff(tx, []emailDest{{UserID: me.ID, Address: fresh.Email}})
		if err != nil {
			return err
		}
		if off.Off(me.ID, fresh.Email, categoryScope(messageCategory(m))) {
			return errOptedOut
		}
		var tries int64
		if err := tx.Model(&models.AuditLog{}).Where("actor_id = ? AND action = ? AND at > ?", me.ID, audit.MessageTestSent, time.Now().Add(-time.Hour)).
			Count(&tries).Error; err != nil {
			return err
		}
		if tries >= testSendsPerHour {
			return errLimit
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.MessageTestSent, TargetType: "message", TargetID: m.ID, IP: c.ClientIP()})
	})
	if errors.Is(err, errSecuritySession) {
		jsonError(c, http.StatusUnauthorized, "unauthorized")
		return
	}
	if errors.Is(err, errOptedOut) {
		jsonError(c, http.StatusBadRequest, errMessageTestOptedOut)
		return
	}
	if errors.Is(err, errLimit) {
		jsonError(c, http.StatusBadRequest, errMessageTestLimit)
		return
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, errMessageTestFail)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	// Synchronous by design: the person at the composer waits for this one
	// test message, so it is not a notice and does not go through notify.Send.
	err = a.Mailer.Send(ctx, mail.Message{To: me.Email, Subject: "[Test] " + r.Subject, Text: r.Text, HTML: r.HTML,
		Stream: providers.StreamBroadcast, Optional: true, ListUnsubscribe: r.Unsubscribe, FromName: a.messageFromName(a.DB, m, a.siteName()), ReplyTo: a.replyToAddress()})
	var suppressed *mail.ErrSuppressed
	if errors.As(err, &suppressed) {
		jsonError(c, http.StatusBadRequest, errMessageTestEmail)
		return
	}
	if err != nil {
		jsonError(c, http.StatusBadGateway, errMessageTestFail)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
