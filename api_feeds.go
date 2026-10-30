package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/events"
	"fold/internal/policy"
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

func feedUnavailable(c *gin.Context, err error) {
	log.Printf("calendar feed unavailable: %v", err)
	c.Header("Cache-Control", "no-store")
	c.String(http.StatusInternalServerError, "calendar unavailable")
}

func (a *App) feedSiteName() (string, error) {
	var s models.Setting
	if err := a.DB.Where("key = ?", "site_name").First(&s).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}
	if s.Value == "" {
		return "Fold", nil
	}
	return s.Value, nil
}

func (a *App) feedPublic(c *gin.Context) {
	name, err := a.feedSiteName()
	if err != nil {
		feedUnavailable(c, err)
		return
	}
	feed, err := a.buildFeed(nil, feedOptions{name: name}, time.Now().UTC())
	if err != nil {
		feedUnavailable(c, err)
		return
	}
	serveFeed(c, events.RenderFeed(feed))
}

func (a *App) feedGroup(c *gin.Context) {
	id, err := strconv.Atoi(strings.TrimSuffix(c.Param("id"), ".ics"))
	if err != nil || !strings.HasSuffix(c.Param("id"), ".ics") {
		feedNotFound(c)
		return
	}
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		feedUnavailable(c, err)
		return
	}
	if g.ID == 0 || g.Status != models.GroupActive || g.Visibility != models.VisibilityPublic {
		feedNotFound(c)
		return
	}
	feed, err := a.buildFeed(nil, feedOptions{
		name:        g.Name, // group-ok: this feed only serves active public groups
		onlyGroupID: &g.ID,
	}, time.Now().UTC())
	if err != nil {
		feedUnavailable(c, err)
		return
	}
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
		if errors.Is(err, gorm.ErrRecordNotFound) {
			feedNotFound(c)
		} else {
			feedUnavailable(c, err)
		}
		return
	}
	// A disabled or deleted owner's link stops working, with the same
	// plain 404 as a token that never existed.
	var owner models.User
	if err := a.DB.First(&owner, tok.UserID).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		feedUnavailable(c, err)
		return
	}
	if owner.ID == 0 || owner.Status != models.UserActive {
		feedNotFound(c)
		return
	}
	name, err := a.feedSiteName()
	if err != nil {
		feedUnavailable(c, err)
		return
	}
	feed, err := a.buildFeed(&owner, feedOptions{
		name:     name,
		personal: true,
		scope:    tok.Scope,
	}, time.Now().UTC())
	if err != nil {
		feedUnavailable(c, err)
		return
	}
	serveFeed(c, events.RenderFeed(feed))
}

type feedOptions struct {
	name        string
	onlyGroupID *uint
	personal    bool
	scope       string
}

// buildFeed projects authorized scheduled dates inside the export window.
// Parent candidates never grant series access; group fallback locations and
// parent details are separately checked before reaching the renderer.
func (a *App) buildFeed(viewer *models.User, opts feedOptions, now time.Time) (events.Feed, error) {
	windowStart, windowEnd := feedWindow(now)
	feed := events.Feed{
		Name: opts.name, Host: a.feedHost(),
		WindowStart: windowStart, WindowEnd: windowEnd,
		Singles: []events.FeedSingle{},
	}

	// Candidate parents load facts only. Every exported occurrence still
	// needs its own effective-date authorization below.
	q := a.DB.Model(&models.Event{}).Where("id IN (SELECT event_id FROM event_occurrences WHERE starts_at >= ? AND starts_at < ?)", windowStart, windowEnd)
	if opts.onlyGroupID != nil {
		q = q.Where("group_id = ?", *opts.onlyGroupID)
	}
	vs, err := a.dateCandidates(viewer, q)
	if err != nil {
		return events.Feed{}, err
	}
	scoped := map[uint]*occurrenceRead{}
	if opts.personal && viewer != nil && opts.onlyGroupID == nil {
		scoped, err = a.scopedOccurrenceReads(viewer, a.DB.Where("starts_at >= ? AND starts_at < ?", windowStart, windowEnd), now)
		if err != nil {
			return events.Feed{}, err
		}
	}
	if len(vs.IDs) == 0 {
		return feed, nil
	}

	var occs []models.EventOccurrence
	// Keep the candidate restriction in SQL rather than expanding an
	// unbounded event-ID list into PostgreSQL bind parameters.
	if err := a.DB.Where("event_id IN (?) AND starts_at >= ? AND starts_at < ?", q.Select("id"), windowStart, windowEnd).
		Order("starts_at").Find(&occs).Error; err != nil {
		return events.Feed{}, err
	}
	if opts.personal && viewer != nil {
		return a.buildPersonalFeedSingles(feed, viewer, opts.scope, now, occs, scoped, vs)
	}
	for i := range occs {
		occ := &occs[i]
		ev := vs.ByID[occ.EventID]
		if occ.Status != models.OccurrenceScheduled || !vs.canSeeDate(viewer, ev, occ) {
			continue
		}
		grp, rel := groupAndRel(ev, vs.Groups, vs.Rels)
		groupLoc := ""
		if shown := a.groupShownTo(viewer, grp, rel); shown != nil {
			groupLoc = shown.Location
		}
		eff := occurrenceToJSON(ev, occ, groupLoc)
		videoURL := ""
		if policy.CanSeeVideoLink(videoRevealOf(ev), false, false) {
			videoURL = videoLinkOf(ev)
		}
		details := projectFeedSeriesDetails(ev, vs.canSeeSeries(viewer, ev), videoURL)
		feed.Singles = append(feed.Singles, events.FeedSingle{
			EventID: ev.ID, OriginalDate: occ.OriginalDate,
			Title: eff.Title, Description: details.description, Location: eff.Location,
			VideoURL: details.videoURL, RemoteAvailable: events.RemoteOffered(eff.RemoteAvailable, eff.RemoteCapacity),
			ChildcareAvailable: details.childcareAvailable, ChildcareAges: details.childcareAges,
			TZID: ev.TZID, StartsAt: eff.StartsAt, EndsAt: eff.EndsAt,
			URL:       a.emailBaseURL() + occurrenceDatePath(ev.ID, occ.OriginalDate),
			UpdatedAt: laterTime(ev.UpdatedAt, occ.UpdatedAt), Status: "CONFIRMED",
		})
	}
	return feed, nil
}

// buildPersonalFeedSingles expands personal feeds date by date. That makes
// every hidden, declined, out-of-window, or differently answered recurrence
// explicit, so an RRULE can never reintroduce it in a calendar client.
func (a *App) buildPersonalFeedSingles(feed events.Feed, viewer *models.User, rawScope string, now time.Time, occs []models.EventOccurrence, scoped map[uint]*occurrenceRead, vs visibleSet) (events.Feed, error) {
	ids := make([]uint, len(occs))
	for i := range occs {
		ids[i] = occs[i].ID
	}
	facts, err := a.loadCalendarScopeBatch(viewer, ids)
	if err != nil {
		return events.Feed{}, err
	}
	scope := policy.CalendarScopeMine
	if rawScope == models.CalendarFeedAll {
		scope = policy.CalendarScopeAll
	}
	householdGoing, err := householdGoingMap(a.DB, ids, viewer, false)
	if err != nil {
		return events.Feed{}, err
	}
	viewerAdmin := asPerson(viewer).Admin
	for i := range occs {
		occ := &occs[i]
		if occ.Status != models.OccurrenceScheduled {
			continue
		}
		ev := vs.ByID[occ.EventID]
		read := scoped[occ.ID]
		ordinary := vs.canSeeDate(viewer, ev, occ)
		if !ordinary && read == nil {
			continue
		}
		if ev == nil && read != nil {
			ev = &read.Event
		}
		grp, rel := groupAndRel(ev, vs.Groups, vs.Rels)
		ownStatus := facts.ownStatus[occ.ID]
		decision := calendarScopeDecision(scope, true, ev, occ, rel, vs.Hosted[ev.ID], facts.household[occ.ID], read != nil, ownStatus, now)
		// Personal ICS never carries Can't go, even in scope=all.
		if !decision.Include || ownStatus == models.RegDeclined {
			continue
		}

		status := "TENTATIVE"
		if ownStatus == models.RegGoing {
			status = "CONFIRMED"
		}
		if !ordinary {
			date := presentOccurrenceDate(&read.Event, occ, "", false, false, false, read.Registration, read, now)
			feed.Singles = append(feed.Singles, events.FeedSingle{
				EventID: date.EventID, OriginalDate: date.Date, Title: date.Title,
				Location: date.Location, TZID: date.TZID, StartsAt: date.StartsAt, EndsAt: date.EndsAt,
				VideoURL: date.VideoURL, RemoteAvailable: date.RemoteAvailable,
				URL: a.emailBaseURL() + date.DateURL, UpdatedAt: occ.UpdatedAt, Status: status,
			})
			continue
		}

		groupLoc := ""
		if shown := a.groupShownTo(viewer, grp, rel); shown != nil {
			groupLoc = shown.Location
		}
		eff := occurrenceToJSON(ev, occ, groupLoc)
		seriesVisible := vs.canSeeSeries(viewer, ev)
		videoStatus := ownStatus
		if videoStatus == "" && householdGoing[occ.ID] {
			videoStatus = models.RegGoing
		}
		videoURL := ""
		if seriesVisible {
			if link := videoLinkOf(ev); link != "" && policy.CanSeeVideoLinkInFeed(videoRevealOf(ev), videoStatus,
				rel.Member || vs.Hosted[ev.ID], rel.Leader || vs.Hosted[ev.ID] || viewerAdmin) {
				videoURL = link
			}
		}
		details := projectFeedSeriesDetails(ev, seriesVisible, videoURL)
		feed.Singles = append(feed.Singles, events.FeedSingle{
			EventID: ev.ID, OriginalDate: occ.OriginalDate, Title: eff.Title,
			Description: details.description, Location: eff.Location, TZID: ev.TZID,
			StartsAt: eff.StartsAt, EndsAt: eff.EndsAt, VideoURL: details.videoURL,
			RemoteAvailable: events.RemoteOffered(eff.RemoteAvailable, eff.RemoteCapacity), ChildcareAvailable: details.childcareAvailable,
			ChildcareAges: details.childcareAges, URL: a.emailBaseURL() + occurrenceDatePath(ev.ID, occ.OriginalDate),
			UpdatedAt: laterTime(ev.UpdatedAt, occ.UpdatedAt), Status: status,
		})
	}
	return feed, nil
}

type feedSeriesDetails struct {
	description        string
	videoURL           string
	childcareAvailable bool
	childcareAges      string
}

// projectFeedSeriesDetails is shared by public, group and personal date feeds.
// An opened date authorizes its date fields, never its hidden parent details.
func projectFeedSeriesDetails(ev *models.Event, seriesVisible bool, videoURL string) feedSeriesDetails {
	if !seriesVisible {
		return feedSeriesDetails{}
	}
	return feedSeriesDetails{description: ev.Description, videoURL: videoURL,
		childcareAvailable: ev.ChildcareAvailable, childcareAges: ev.ChildcareAges}
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
