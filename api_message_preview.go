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

var (
	errMergeUnknown        = apiErr(http.StatusBadRequest, "message_merge_unknown", "use only the merge fields the composer offers")
	errMergeEvent          = apiErr(http.StatusBadRequest, "message_merge_event", "event merge fields only work in a message to one event's people")
	errMessageTestLimit    = apiErr(http.StatusBadRequest, "message_test_limit", "you've sent 10 tests in the last hour")
	errMessageTestEmail    = apiErr(http.StatusBadRequest, "message_test_email", "your own email address can't get mail right now")
	errMessageTestOptedOut = apiErr(http.StatusBadRequest, "message_test_opted_out", "this kind of email is turned off in your notification settings")
	errMessageTestFail     = apiErr(http.StatusBadGateway, "message_test_failed", "we couldn't send the test")
)

const testSendsPerHour = 10

// mergeFieldError checks the fields s uses: each must be known, and event
// fields need a message to one event's people (one date, or one event's
// dates). nil means they are fine.
func mergeFieldError(s string, au audienceJSON) error {
	oneEvent := au.Type == policy.AudienceOccurrence || au.Type == policy.AudienceEventSeries
	for _, f := range content.FieldsIn(s) {
		if !content.IsMergeField(f) {
			return errMergeUnknown
		}
		if content.IsEventField(f) && !oneEvent {
			return errMergeEvent
		}
	}
	return nil
}

type renderedMessage struct {
	Subject string `json:"subject"`
	Text    string `json:"text"`
	HTML    string `json:"-"`
	// Unsubscribe is the one-click link for the List-Unsubscribe header.
	Unsubscribe string `json:"-"`
}

// sampleValues are the merge-field values for previewing m as me: what
// recipientValues would give me, with the date the audience's next (or, with
// none upcoming, latest) one. An event that is gone leaves its fields out.
func (a *App) sampleValues(m *models.Message, me *models.User) (map[string]string, error) {
	vals := map[string]string{content.FieldFirstName: firstName(me.PublicName())}
	var au audienceJSON
	_ = json.Unmarshal([]byte(m.Audience), &au)
	if au.Type != policy.AudienceOccurrence && au.Type != policy.AudienceEventSeries {
		return vals, nil
	}
	ids, err := a.audienceEventIDs(a.DB, au)
	if errors.Is(err, errAudienceGone) || (err == nil && len(ids) != 1) {
		return vals, nil
	} else if err != nil {
		return nil, err
	}
	var ev models.Event
	if err := a.DB.Select("id, title, tzid").First(&ev, ids[0]).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return vals, nil
	} else if err != nil {
		return nil, err
	}
	var occ models.EventOccurrence
	q := a.DB.Select("id, starts_at, title_override").Where("event_id = ?", ev.ID)
	if au.Type == policy.AudienceOccurrence {
		q = q.Where("id = ?", au.OccurrenceID)
	} else {
		q = q.Where("status = ? AND starts_at >= ?", models.OccurrenceScheduled, time.Now()).Order("starts_at")
	}
	found := true
	if err := q.First(&occ).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		found = false
	} else if err != nil {
		return nil, err
	}
	if !found && au.Type == policy.AudienceEventSeries {
		// Nothing upcoming: show the latest date, so the preview isn't blank.
		err := a.DB.Select("id, starts_at").Where("event_id = ?", ev.ID).Order("starts_at DESC").First(&occ).Error
		if found = err == nil; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	title := ev.Title
	if au.Type == policy.AudienceOccurrence && occ.TitleOverride != nil && *occ.TitleOverride != "" {
		title = *occ.TitleOverride
	}
	a.setEventFields(vals, ev.ID, title)
	if found {
		vals[content.FieldEventTime] = eventTimeText(occ.StartsAt, ev.TZID)
	}
	return vals, nil
}

// setEventFields sets the fields every message about an event can use: its
// title, and its page as both the event link and (until a reader's own date
// is known) the manage link.
func (a *App) setEventFields(vals map[string]string, eventID uint, title string) {
	link := a.emailLink("/events/" + idString(eventID))
	vals[content.FieldEventTitle], vals[content.FieldEventLink], vals[content.FieldManageLink] = title, link, link
}

// eventTimeText is a date's start on its event's clock, as a merge field and
// a reminder's subject show it.
func eventTimeText(startsAt time.Time, tzid string) string {
	return formatWhen(startsAt, eventZone(tzid))
}

func firstName(name string) string {
	if f := strings.Fields(name); len(f) > 0 {
		return f[0]
	}
	return name
}

func (a *App) renderMessage(m *models.Message, u *models.User, vals map[string]string) (renderedMessage, error) {
	footer, err := footerFactsIn(a.DB)
	if err != nil {
		return renderedMessage{}, err
	}
	return a.renderMessageWith(m, u, u.Email, vals, footer)
}

// renderMessageWith renders with the church's name and address already
// read, so a batch reads them once, not once per email.
func (a *App) renderMessageWith(m *models.Message, u *models.User, to string, vals map[string]string, f footerFacts) (renderedMessage, error) {
	site := f.site
	cat := messageCategory(m)
	token := a.unsubscribeToken(u.ID, categoryScope(cat), to)
	html, err := content.RenderEmail(content.EmailPage{
		Church: site, Subject: content.Merge(m.Subject, vals, false), Body: content.Merge(m.BodyHTML, vals, true),
		Footer:      notify.YoureGetting(cat, site) + ".",
		SettingsURL: a.emailSettingsURL(token), Address: f.addressLines(),
	})
	if err != nil {
		return renderedMessage{}, err
	}
	return renderedMessage{
		Subject:     strings.TrimSpace(content.Merge(m.Subject, vals, false)),
		Text:        content.Merge(m.BodyText, vals, false) + a.optionalEmailFooter(f, cat, token),
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
	vals, err := a.sampleValues(m, me)
	if err == nil {
		var r renderedMessage
		if r, err = a.renderMessage(m, me, vals); err == nil {
			return m, r, true
		}
	}
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not show the preview")
	}
	return nil, renderedMessage{}, false
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

// emailPreviewCSP is the policy for a page that shows an email's HTML (the
// composer's preview and the dev outbox): email needs inline styles, and
// nothing in it may run, load anything but images, or reach the app.
const emailPreviewCSP = "default-src 'none'; style-src 'unsafe-inline'; img-src https: data:; base-uri 'none'; form-action 'none'; " +
	"frame-ancestors 'self'; sandbox allow-popups allow-popups-to-escape-sandbox"

func serveEmailHTML(c *gin.Context, html string) {
	c.Header("Content-Security-Policy", emailPreviewCSP)
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
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
		fail(c, errMessageSubject)
		return
	}
	if strings.TrimSpace(m.BodyText) == "" {
		fail(c, errMessageBody)
		return
	}
	if strings.TrimSpace(me.Email) == "" {
		fail(c, errMessageTestEmail)
		return
	}
	// D12: the locked session check rejects earlier security changes. The
	// effective choice is read at its statement's start under the lock.
	// D11: a choice committed before that statement starts is honored; a later
	// change may not stop this one in-flight send. The user lock is released at
	// commit, before the provider call. An opt-out, erasure or address change
	// committed after commit and before or during that call may not stop this
	// test send. Every later request rechecks; no user lock spans provider I/O.
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var fresh models.User
		if err := tx.Raw("SELECT id, email, status, password_hash, session_version, logout_version, deleted_at FROM users WHERE id = ? FOR UPDATE", me.ID).Scan(&fresh).Error; err != nil {
			return err
		}
		if !sessionCurrent(&fresh, me) || mail.NormalizeAddress(fresh.Email) != mail.NormalizeAddress(me.Email) {
			return errSecuritySession
		}
		off, err := readEmailOff(tx, []emailDest{{UserID: me.ID, Address: fresh.Email}})
		if err != nil {
			return err
		}
		if off.Off(me.ID, fresh.Email, categoryScope(messageCategory(m))) {
			return errMessageTestOptedOut
		}
		var tries int64
		if err := tx.Model(&models.AuditLog{}).Where("actor_id = ? AND action = ? AND at > ?", me.ID, audit.MessageTestSent, time.Now().Add(-time.Hour)).
			Count(&tries).Error; err != nil {
			return err
		}
		if tries >= testSendsPerHour {
			return errMessageTestLimit
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.MessageTestSent, TargetType: "message", TargetID: m.ID, IP: c.ClientIP()})
	})
	if answerStaleSession(c, err) {
		return
	}
	if err != nil {
		fail(c, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	from, err := messageFromName(a.DB, m, a.siteName())
	if err == nil {
		var replyTo string
		if replyTo, err = replyToAddressIn(a.DB); err == nil {
			// Synchronous by design: the person at the composer waits for this one
			// test message, so it is not a notice and does not go through notify.Send.
			res := sendTracked(ctx, a.mailSender(), mail.Message{To: me.Email, Subject: "[Test] " + r.Subject, Text: r.Text, HTML: r.HTML,
				Stream: providers.StreamBroadcast, Optional: true, ListUnsubscribe: r.Unsubscribe, FromName: from, ReplyTo: replyTo}, nil)
			answerTestSend(c, res)
			return
		}
	}
	fail(c, errMessageTestFail)
}

func answerTestSend(c *gin.Context, res sendResult) {
	switch res.outcome {
	case sendSent:
	case sendSuppressed:
		fail(c, errMessageTestEmail)
		return
	default:
		fail(c, errMessageTestFail)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
