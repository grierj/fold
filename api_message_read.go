package main

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"fold/internal/content"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Reading an announcement in Fold (M3.13d2): the page an announcement's
// push opens. A recipient may read what they were sent (policy
// CanReadMessage), and nothing else about it: no audience, counts,
// history or other recipients. Its author and admins may read it too.

// messageReadJSON is GET /messages/:id/read: the message as its reader's
// email said it, merge fields filled with their own values. The event fields
// carry today's values only while, and only as far as, the reader may still
// see the event or date (readerEventValues); otherwise they read empty and a
// link reads as its text.
type messageReadJSON struct {
	ID      uint   `json:"id"`
	Subject string `json:"subject"`
	// From is "Pat Lee via First Church", or the church's name when the
	// author's account is gone (PLAN 7.8).
	From string `json:"from"`
	// SentAt is when it went out (when it started, while it is sending).
	SentAt   *time.Time `json:"sentAt"`
	BodyHTML string     `json:"bodyHtml"`
}

// isMessageRecipient: the person has a direct row (not a guardian copy)
// in any channel for an announcement that is sending or sent.
func (a *App) isMessageRecipient(m *models.Message, userID uint) (bool, error) {
	if m.Status != models.MessageSending && m.Status != models.MessageSent {
		return false, nil
	}
	var n int64
	err := a.DB.Model(&models.MessageRecipient{}).
		Where("message_id = ? AND user_id = ? AND copy_of_user_id IS NULL", m.ID, userID).Limit(1).Count(&n).Error
	return n > 0, err
}

// apiReadMessage answers GET /messages/:id/read. Who may read is
// policy.CanReadMessage; what the event fields hold is readerEventValues.
func (a *App) apiReadMessage(c *gin.Context) {
	me := apiUser(c)
	id, err := strconv.ParseUint(c.Param("id"), 10, 63)
	var m models.Message
	if err != nil || a.DB.First(&m, id).Error != nil {
		jsonError(c, http.StatusNotFound, "message not found")
		return
	}
	p := asPerson(me)
	isAuthor := m.AuthorID != nil && *m.AuthorID == me.ID
	reminder := m.Category == models.MessageCategoryReminder
	manages, recipient := false, false
	if reminder && !p.Admin && m.OccurrenceID != nil {
		if manages, err = a.managesOccurrenceEvent(me, *m.OccurrenceID); err != nil {
			jsonError(c, http.StatusInternalServerError, "could not load the message")
			return
		}
	}
	if !reminder && !p.Admin && !isAuthor {
		if recipient, err = a.isMessageRecipient(&m, me.ID); err != nil {
			jsonError(c, http.StatusInternalServerError, "could not load the message")
			return
		}
	}
	// Anyone else hears what a message that isn't there answers.
	if !policy.CanReadMessage(p, isAuthor, reminder, manages, recipient) {
		jsonError(c, http.StatusNotFound, "message not found")
		return
	}
	var au audienceJSON
	_ = json.Unmarshal([]byte(m.Audience), &au)
	// The event fields are filled per field, by what this reader may see of
	// the event or date today. Nobody is exempt: the author, an admin and a
	// reminder's manager get them only as far as they can see the event.
	vals, err := a.readerEventValues(me, au, time.Now().UTC())
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the message")
		return
	}
	from, err := messageFromName(a.DB, &m, a.siteName())
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the message")
		return
	}
	sentAt := m.SentAt
	if sentAt == nil {
		sentAt = m.ScheduledAt
	}
	// The stored body is sanitized; it is sanitized again once the
	// reader's values are in, so the page gets only the composer's
	// formatting (no script, style, image or frame).
	c.JSON(http.StatusOK, messageReadResponseJSON{Message: messageReadJSON{
		ID:       m.ID,
		Subject:  strings.TrimSpace(content.Merge(m.Subject, vals, false)),
		From:     from,
		SentAt:   sentAt,
		BodyHTML: content.SanitizeHTML(content.Merge(unlinkMissingFields(m.BodyHTML, vals), vals, true)),
	}})
}

// readerEventValues is the reader's whole merge map for the read page. It
// applies the event and date pages' own rules to the reader and never
// authorizes the message (CanReadMessage does that). Every read goes through
// a.DB, like loadOccurrenceDate, so no transaction or snapshot is claimed: a
// change committed while one response loads may or may not affect it, and the
// next load rechecks. A failed read is returned, never treated as "not
// visible".
//
// Access is decided per field:
//   - the series is visible: today's values, as a send gives them;
//   - one date seen on its own (an exact-date invitation, or an override that
//     opens a date of a hidden series): that date's title, its time when the
//     reader's registration is in the audience's statuses, and a link to the
//     date page, never the series link;
//   - a series audience the reader cannot see as a series: the series title
//     alone, through a scheduled date with no title of its own that they can
//     see;
//   - a cancelled date: the series title and link for series viewers only.
//
// Otherwise the event fields stay unset and a link reads as its text.
func (a *App) readerEventValues(reader *models.User, au audienceJSON, now time.Time) (map[string]string, error) {
	vals := map[string]string{content.FieldFirstName: firstName(reader.PublicName())}
	switch au.Type {
	case policy.AudienceOccurrence:
		if au.OccurrenceID == 0 {
			return vals, nil
		}
		return a.readerDateValues(reader, au, now, vals)
	case policy.AudienceEventSeries:
		if au.EventID == 0 {
			return vals, nil
		}
		return a.readerSeriesValues(reader, au, now, vals)
	}
	return vals, nil
}

func (a *App) readerDateValues(reader *models.User, au audienceJSON, now time.Time, vals map[string]string) (map[string]string, error) {
	var occ models.EventOccurrence
	if err := a.DB.First(&occ, au.OccurrenceID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return vals, nil // the date is gone: the fields stay unset
	} else if err != nil {
		return nil, err
	}
	vs, err := a.dateCandidates(reader, a.DB.Where("id = ?", occ.EventID))
	if err != nil {
		return nil, err
	}
	ev := vs.ByID[occ.EventID] // nil when the event or its group is deleted
	if occ.Status != models.OccurrenceScheduled {
		// Neither the event page's dates nor the date page show a cancelled
		// date: series viewers get the series alone, never the date's title.
		if ev != nil && vs.canSeeSeries(reader, ev) {
			maps.Copy(vals, a.eventFields(messageEvent{ID: ev.ID, Title: ev.Title, TZID: ev.TZID}, nil))
		}
		return vals, nil
	}
	scoped, err := a.scopedOccurrenceReads(reader, a.DB.Where("id = ? AND status = ?", occ.ID, models.OccurrenceScheduled), now)
	if err != nil {
		return nil, err
	}
	p, ok := a.projectDate(reader, vs, &occ, scoped[occ.ID])
	if !ok {
		return vals, nil
	}
	regs, err := audienceRegisteredDates(a.DB, occ.EventID, au, []uint{reader.ID}, now)
	if err != nil {
		return nil, err
	}
	d, registered := regs[reader.ID]
	if p.ShowsSeries() {
		f := newMessageEvent(*p.Event, au, occ.TitleOverride)
		maps.Copy(vals, a.eventFields(f, nil))
		if registered {
			vals[content.FieldEventTime] = f.timeText(d.StartsAt)
			vals[content.FieldManageLink] = a.emailLink(occurrenceDatePath(p.Event.ID, models.FormatCalendarDate(d.OriginalDate)))
		}
		return vals, nil
	}
	// The date alone: its own title and page, never the series link.
	vals[content.FieldEventTitle] = effectiveOccurrenceTitle(p.Event, &occ)
	vals[content.FieldManageLink] = a.emailLink(occurrenceDatePath(p.Event.ID, p.DateString()))
	if registered {
		vals[content.FieldEventTime] = newMessageEvent(*p.Event, au, nil).timeText(d.StartsAt)
	}
	return vals, nil
}

func (a *App) readerSeriesValues(reader *models.User, au audienceJSON, now time.Time, vals map[string]string) (map[string]string, error) {
	vs, err := a.dateCandidates(reader, a.DB.Where("id = ?", au.EventID))
	if err != nil {
		return nil, err
	}
	ev := vs.ByID[au.EventID]
	if ev == nil {
		return vals, nil // deleted event or group: no candidate and no live invitation
	}
	if vs.canSeeSeries(reader, ev) {
		f := newMessageEvent(*ev, au, nil)
		maps.Copy(vals, a.eventFields(f, nil))
		regs, err := audienceRegisteredDates(a.DB, ev.ID, au, []uint{reader.ID}, now)
		if err != nil {
			return nil, err
		}
		d, registered := regs[reader.ID]
		if !registered {
			return vals, nil
		}
		// The next date supplies a time and a page only if the reader may see
		// that date, ordinarily or by invitation.
		seen := vs.canSeeDate(reader, ev, &models.EventOccurrence{EventID: ev.ID, VisibilityOverride: d.VisibilityOverride})
		if !seen {
			scoped, err := a.scopedOccurrenceReads(reader, a.DB.Where("id = ? AND status = ?", d.OccurrenceID, models.OccurrenceScheduled), now)
			if err != nil {
				return nil, err
			}
			seen = scoped[d.OccurrenceID] != nil
		}
		if seen {
			vals[content.FieldEventTime] = f.timeText(d.StartsAt)
			vals[content.FieldManageLink] = a.emailLink(occurrenceDatePath(ev.ID, models.FormatCalendarDate(d.OriginalDate)))
		}
		return vals, nil
	}
	// A series the reader cannot see: the series title alone, through a
	// scheduled date with no title of its own that they can see. canSeeSeries
	// is canSeeDate with no override, so a hidden series' date is ordinarily
	// visible only through its override: the overrides that would open it for
	// this reader are known without a query, and one LIMIT 1 probe finds a
	// date that has one. Otherwise the invitation fallback pages the untitled
	// dates and validates every page. A date with any title_override, empty
	// included, never supplies the series title.
	open := []string{}
	for _, v := range []string{policy.Public, policy.Private, policy.InviteOnly} {
		if vs.canSeeDate(reader, ev, &models.EventOccurrence{EventID: ev.ID, VisibilityOverride: &v}) {
			open = append(open, v)
		}
	}
	if len(open) > 0 {
		var hit []models.EventOccurrence
		if err := a.DB.Select("id").
			Where("event_id = ? AND status = ? AND title_override IS NULL AND visibility_override IN ?", ev.ID, models.OccurrenceScheduled, open).
			Limit(1).Find(&hit).Error; err != nil {
			return nil, err
		}
		if len(hit) > 0 {
			vals[content.FieldEventTitle] = ev.Title
			return vals, nil
		}
	}
	// The existence probe pages this reader's live grant candidates for
	// untitled scheduled dates and validates them with the date page's grant
	// check, so an invalid earlier grant never hides a valid later one.
	hit, err := a.hasScopedUntitledRead(reader, ev.ID, now)
	if err != nil {
		return nil, err
	}
	if hit {
		vals[content.FieldEventTitle] = ev.Title
	}
	return vals, nil
}

// linkFieldAnchorRe is a link the editor made to {{event_link}} or
// {{manage_link}}, as the sanitizer stores it.
var linkFieldAnchorRe = regexp.MustCompile(`(?is)<a\b[^>]*\bhref="\{\{\s*(event_link|manage_link)\s*\}\}"[^>]*>(.*?)</a>`)

// unlinkMissingFields keeps the text of a link to a link field that has no
// value for this reader (its event is gone, say), never an empty href.
func unlinkMissingFields(html string, vals map[string]string) string {
	return linkFieldAnchorRe.ReplaceAllStringFunc(html, func(a string) string {
		m := linkFieldAnchorRe.FindStringSubmatch(a)
		if vals[strings.ToLower(m[1])] != "" {
			return a
		}
		return m[2]
	})
}
