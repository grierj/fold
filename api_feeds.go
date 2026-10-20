package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/events"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ICS calendar feeds (M1.7, 8.2): the public church feed, one feed per
// public group, and one secret-link feed per person. Feeds live outside
// /api/v1 because that group's no-store middleware would fight the feed
// caching calendar apps rely on. No feed ever carries attendee data.

func (a *App) registerCalendarFeeds(r *gin.Engine) {
	r.GET("/cal/public.ics", a.feedPublic)
	r.GET("/cal/groups/:id", a.feedGroup)
	r.GET("/cal/u/:token", a.feedPersonal)
}

// feedWindow covers 30 days back to 12 months ahead, by occurrence start.
func feedWindow(now time.Time) (time.Time, time.Time) {
	return now.AddDate(0, 0, -30), now.AddDate(1, 0, 0)
}

func (a *App) feedHost() string {
	if a.Config.SiteURL != "" {
		if u, err := url.Parse(a.Config.SiteURL); err == nil && u.Host != "" {
			return u.Host
		}
	}
	return "fold"
}

func (a *App) feedEventURL(id uint) string {
	return a.emailBaseURL() + "/events/" + strconv.FormatUint(uint64(id), 10)
}

func feedNotFound(c *gin.Context) {
	c.String(http.StatusNotFound, "not found")
}

func serveFeed(c *gin.Context, body []byte) {
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	c.Header("ETag", etag)
	c.Header("Cache-Control", "private, max-age=900")
	if m := c.GetHeader("If-None-Match"); m == etag || m == "*" {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, "text/calendar; charset=utf-8", body)
}

func (a *App) feedPublic(c *gin.Context) {
	feed := a.buildFeed(nil, feedOptions{name: a.siteName()}, time.Now().UTC())
	serveFeed(c, events.RenderFeed(feed))
}

func (a *App) feedGroup(c *gin.Context) {
	id, err := strconv.Atoi(strings.TrimSuffix(c.Param("id"), ".ics"))
	if err != nil || !strings.HasSuffix(c.Param("id"), ".ics") {
		feedNotFound(c)
		return
	}
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil ||
		g.Status != models.GroupActive || g.Visibility != models.VisibilityPublic {
		feedNotFound(c)
		return
	}
	feed := a.buildFeed(nil, feedOptions{
		name:        g.Name, // group-ok: this feed only serves active public groups
		onlyGroupID: &g.ID,
	}, time.Now().UTC())
	serveFeed(c, events.RenderFeed(feed))
}

func (a *App) feedPersonal(c *gin.Context) {
	param := c.Param("token")
	if !strings.HasSuffix(param, ".ics") {
		feedNotFound(c)
		return
	}
	raw := strings.TrimSuffix(param, ".ics")
	var tok models.CalendarToken
	if err := a.DB.Where("token_hash = ?", hashToken(raw)).First(&tok).Error; err != nil {
		feedNotFound(c)
		return
	}
	// A disabled or deleted owner's link stops working, with the same
	// plain 404 as a token that never existed.
	var owner models.User
	if err := a.DB.First(&owner, tok.UserID).Error; err != nil || owner.Status != models.UserActive {
		feedNotFound(c)
		return
	}
	feed := a.buildFeed(&owner, feedOptions{
		name:     a.siteName(),
		personal: true,
		scope:    tok.Scope,
	}, time.Now().UTC())
	serveFeed(c, events.RenderFeed(feed))
}

type feedOptions struct {
	name        string
	onlyGroupID *uint
	personal    bool
	scope       string
}

// buildFeed loads the events one feed carries. Every event passes
// policy.CanSeeEvent for the viewer (nil is a signed-out visitor) here,
// never on a caller's word, the occurrencesForEvents rule. Group names
// never reach the renderer; a group's location is the fallback only
// when the viewer may see the group.
func (a *App) buildFeed(viewer *models.User, opts feedOptions, now time.Time) events.Feed {
	windowStart, windowEnd := feedWindow(now)
	feed := events.Feed{
		Name:        opts.name,
		Host:        a.feedHost(),
		WindowStart: windowStart,
		WindowEnd:   windowEnd,
		Events:      []events.FeedEvent{},
		Singles:     []events.FeedSingle{},
	}

	// Only events with a date in the window can add anything (#75).
	q := a.DB.Where("id IN (SELECT event_id FROM event_occurrences WHERE starts_at >= ? AND starts_at < ?)",
		windowStart, windowEnd)
	if opts.onlyGroupID != nil {
		q = q.Where("group_id = ?", *opts.onlyGroupID)
	}
	vs, err := a.visibleEvents(viewer, q)
	if err != nil {
		return feed
	}
	byID, groups, rels, hosted, visibleIDs := vs.ByID, vs.Groups, vs.Rels, vs.Hosted, vs.IDs
	if len(visibleIDs) == 0 {
		return feed
	}

	var occs []models.EventOccurrence
	a.DB.Where("event_id IN ? AND starts_at >= ? AND starts_at < ?",
		visibleIDs, windowStart, windowEnd).
		Order("starts_at").Find(&occs)
	byEvent := map[uint][]*models.EventOccurrence{}
	for i := range occs {
		byEvent[occs[i].EventID] = append(byEvent[occs[i].EventID], &occs[i])
	}

	// Option A needs the viewer's own going and waitlisted dates on
	// events that don't arrive as a whole series.
	myStatus := map[uint]string{}
	if opts.personal && opts.scope == models.CalendarFeedMine && viewer != nil && len(occs) > 0 {
		occIDs := make([]uint, len(occs))
		for i := range occs {
			occIDs[i] = occs[i].ID
		}
		var rows []models.Registration
		a.DB.Where("user_id = ? AND occurrence_id IN ? AND status IN ?", viewer.ID, occIDs,
			[]string{models.RegGoing, models.RegWaitlisted}).Find(&rows)
		for _, r := range rows {
			myStatus[r.OccurrenceID] = r.Status
		}
	}

	for _, id := range visibleIDs {
		ev := byID[id]
		list := byEvent[id]
		if ev == nil || len(list) == 0 {
			continue
		}
		grp, rel := groupAndRel(ev, groups, rels)
		groupLoc := ""
		if shown := a.groupShownTo(viewer, grp, rel); shown != nil {
			groupLoc = shown.Location
		}
		full := !opts.personal || opts.scope == models.CalendarFeedAll || rel.Member || hosted[ev.ID]
		if !full {
			for _, occ := range list {
				status, mine := myStatus[occ.ID]
				if !mine || occ.Status == models.OccurrenceCancelled {
					continue
				}
				eff := occurrenceToJSON(ev, occ, groupLoc)
				s := "CONFIRMED"
				if status == models.RegWaitlisted {
					s = "TENTATIVE"
				}
				feed.Singles = append(feed.Singles, events.FeedSingle{
					EventID: ev.ID, OriginalDate: occ.OriginalDate,
					Title: eff.Title, Description: ev.Description, Location: eff.Location,
					TZID: ev.TZID, StartsAt: eff.StartsAt, EndsAt: eff.EndsAt,
					URL: a.feedEventURL(ev.ID), UpdatedAt: occ.UpdatedAt, Status: s,
				})
			}
			continue
		}
		seriesLoc := ev.Location
		if seriesLoc == "" {
			seriesLoc = groupLoc
		}
		until := ""
		if ev.RRuleUntil != nil {
			until = *ev.RRuleUntil
		}
		fe := events.FeedEvent{
			ID: ev.ID, Title: ev.Title, Description: ev.Description, Location: seriesLoc,
			TZID: ev.TZID, StartsLocal: ev.StartsLocal, DurationMinutes: ev.DurationMinutes,
			RRule: ev.RRule, RRuleUntil: until,
			URL: a.feedEventURL(ev.ID), UpdatedAt: ev.UpdatedAt,
		}
		for _, occ := range list {
			if occ.Status == models.OccurrenceCancelled {
				fe.CancelledDates = append(fe.CancelledDates, occ.OriginalDate)
				continue
			}
			eff := occurrenceToJSON(ev, occ, groupLoc)
			fo := events.FeedOccurrence{
				OriginalDate: occ.OriginalDate, StartsAt: eff.StartsAt, EndsAt: eff.EndsAt,
				Title: eff.Title, Location: eff.Location, UpdatedAt: occ.UpdatedAt,
			}
			fe.Occurrences = append(fe.Occurrences, fo)
			if occ.Customized() {
				fe.Overrides = append(fe.Overrides, fo)
			}
		}
		feed.Events = append(feed.Events, fe)
	}
	return feed
}

// ---------------------------------------------------------------------------
// Token management under /api/v1. The raw token reaches the client only
// in the POST response; everything else sees that a link exists and
// which scope it carries.
// ---------------------------------------------------------------------------

type calendarFeedJSON struct {
	HasToken bool   `json:"hasToken"`
	Scope    string `json:"scope"`
	URL      string `json:"url,omitempty"`
}

func (a *App) calendarTokenFor(userID uint) *models.CalendarToken {
	var tok models.CalendarToken
	if err := a.DB.Where("user_id = ?", userID).First(&tok).Error; err != nil {
		return nil
	}
	return &tok
}

func calendarFeedOf(tok *models.CalendarToken) calendarFeedJSON {
	if tok == nil {
		return calendarFeedJSON{HasToken: false, Scope: models.CalendarFeedMine}
	}
	return calendarFeedJSON{HasToken: true, Scope: tok.Scope}
}

func feedScopeOK(c *gin.Context, scope string) bool {
	if scope == models.CalendarFeedMine || scope == models.CalendarFeedAll {
		return true
	}
	jsonError(c, http.StatusBadRequest, "scope must be mine or all")
	return false
}

func (a *App) apiGetMyCalendarFeed(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"calendarFeed": calendarFeedOf(a.calendarTokenFor(apiUser(c).ID))})
}

func (a *App) apiCreateMyCalendarFeed(c *gin.Context) {
	me := apiUser(c)
	var body struct {
		Scope *string `json:"scope"`
	}
	if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	scope := models.CalendarFeedMine
	if existing := a.calendarTokenFor(me.ID); existing != nil {
		scope = existing.Scope
	}
	if body.Scope != nil {
		scope = strings.TrimSpace(*body.Scope)
	}
	if !feedScopeOK(c, scope) {
		return
	}
	raw, err := newRawToken()
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not create the calendar link")
		return
	}
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", me.ID).Delete(&models.CalendarToken{}).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.CalendarToken{
			UserID: me.ID, TokenHash: hashToken(raw), Scope: scope,
		}).Error; err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{
			Actor: &me.ID, Action: audit.CalendarTokenRegenerated,
			TargetType: "user", TargetID: me.ID,
			Meta: map[string]any{"scope": scope}, IP: c.ClientIP(),
		})
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not create the calendar link")
		return
	}
	c.JSON(http.StatusOK, gin.H{"calendarFeed": calendarFeedJSON{
		HasToken: true, Scope: scope,
		URL: a.emailBaseURL() + "/cal/u/" + raw + ".ics",
	}})
}

func (a *App) apiUpdateMyCalendarFeed(c *gin.Context) {
	me := apiUser(c)
	var body struct {
		Scope string `json:"scope"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	scope := strings.TrimSpace(body.Scope)
	if !feedScopeOK(c, scope) {
		return
	}
	tok := a.calendarTokenFor(me.ID)
	if tok == nil {
		jsonError(c, http.StatusNotFound, "calendar feed not found")
		return
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.CalendarToken{}).Where("user_id = ?", me.ID).
			Update("scope", scope).Error; err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{
			Actor: &me.ID, Action: audit.CalendarTokenScopeChanged,
			TargetType: "user", TargetID: me.ID,
			Meta: map[string]any{"oldScope": tok.Scope, "newScope": scope}, IP: c.ClientIP(),
		})
	})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save the feed choice")
		return
	}
	tok.Scope = scope
	c.JSON(http.StatusOK, gin.H{"calendarFeed": calendarFeedOf(tok)})
}

func (a *App) apiDeleteMyCalendarFeed(c *gin.Context) {
	me := apiUser(c)
	if _, err := a.revokeCalendarToken(c, me.ID, me.ID); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not turn off the calendar link")
		return
	}
	c.JSON(http.StatusOK, gin.H{"calendarFeed": calendarFeedOf(nil)})
}

func (a *App) apiAdminRevokeCalendarFeed(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		jsonError(c, http.StatusNotFound, "user not found")
		return
	}
	had, err := a.revokeCalendarToken(c, apiUser(c).ID, uint(id))
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not revoke the calendar link")
		return
	}
	// hadLink lets the admin page say whether a link was actually turned off.
	c.JSON(http.StatusOK, gin.H{"calendarFeed": calendarFeedOf(nil), "hadLink": had})
}

// revokeCalendarToken deletes userID's link when one exists. Revoking a
// link that is already off succeeds the same way, so the admin action
// can be unconditional.
func (a *App) revokeCalendarToken(c *gin.Context, actorID, userID uint) (bool, error) {
	had := false
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Where("user_id = ?", userID).Delete(&models.CalendarToken{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		had = true
		return audit.Write(tx, audit.Entry{
			Actor: &actorID, Action: audit.CalendarTokenRevoked,
			TargetType: "user", TargetID: userID, IP: c.ClientIP(),
		})
	})
	return had, err
}
