package main

import (
	"crypto/rand"
	"net/http"
	"net/url"
	"strings"

	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
)

// Video links on events (5.8, M1.8): a pasted link or a generated Jitsi
// room, always a plain link out — there is no embedded video anywhere
// (DEC-16). Who sees the link is policy.CanSeeVideoLink; this file holds
// the input validation, the reveal glue for the JSON shapes, and the
// Jitsi link generator.

const (
	defaultJitsiDomain = "meet.jit.si"
	maxVideoURLLen     = 2048
)

// videoEdits is the effective video state from an event body: the body's
// fields where present, the existing event's otherwise (defaults on
// create). parseVideoEdits validates the result, so applySpec can assign
// it whole.
type videoEdits struct {
	provider string
	url      string
	reveal   string
}

func videoProviderOf(ev *models.Event) string {
	if ev == nil || ev.VideoProvider == "" {
		return models.VideoProviderNone
	}
	return ev.VideoProvider
}

func videoRevealOf(ev *models.Event) string {
	if ev == nil || ev.VideoReveal == "" {
		return models.VideoRevealRegisteredOnly
	}
	return ev.VideoReveal
}

// videoLinkOf is the event's link, or "" when it has none.
func videoLinkOf(ev *models.Event) string {
	if ev == nil || videoProviderOf(ev) == models.VideoProviderNone {
		return ""
	}
	return ev.VideoURL
}

// validVideoURL: an absolute https:// URL with a host, no spaces, and no
// userinfo (https://meet.jit.si@evil.example goes to evil.example). A
// Jitsi link is checked the same way as a pasted one: a leader may edit
// it, so its host is not pinned to jitsi_domain.
func validVideoURL(s string) bool {
	if s == "" || strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

// parseVideoEdits reads videoProvider, videoUrl and videoReveal from a
// body over base (nil on create) and validates the effective values; the error is the 400 to answer with. none clears the URL; any other provider
// needs a full https:// address of at most 2048 characters.
func parseVideoEdits(body eventBody, base *models.Event) (videoEdits, error) {
	ed := videoEdits{
		provider: models.VideoProviderNone,
		reveal:   models.VideoRevealRegisteredOnly,
	}
	if base != nil {
		ed.provider = videoProviderOf(base)
		ed.url = base.VideoURL
		ed.reveal = videoRevealOf(base)
	}
	if body.VideoProvider != nil && strings.TrimSpace(*body.VideoProvider) != "" {
		ed.provider = strings.TrimSpace(*body.VideoProvider)
	}
	if body.VideoURL != nil {
		ed.url = strings.TrimSpace(*body.VideoURL)
	}
	if body.VideoReveal != nil && strings.TrimSpace(*body.VideoReveal) != "" {
		ed.reveal = strings.TrimSpace(*body.VideoReveal)
	}
	switch ed.provider {
	case models.VideoProviderNone, models.VideoProviderLink, models.VideoProviderJitsi:
	default:
		return ed, errInvalidVideoProvider
	}
	switch ed.reveal {
	case models.VideoRevealAlways, models.VideoRevealRegisteredOnly:
	default:
		return ed, errInvalidVideoReveal
	}
	if len(ed.url) > maxVideoURLLen {
		return ed, errVideoLinkTooLong
	}
	if ed.provider == models.VideoProviderNone {
		// A supplied address is still validated, then cleared: none
		// means the event has no video link.
		if ed.url != "" && !validVideoURL(ed.url) {
			return ed, errInvalidVideoUrl
		}
		ed.url = ""
		return ed, nil
	}
	if !validVideoURL(ed.url) {
		return ed, errInvalidVideoUrl
	}
	return ed, nil
}

// videoShown is the one reveal rule for a viewer: the event's link when
// policy.CanSeeVideoLink lets them see it, else hidden when a link exists at
// all (so the UI can say "Register to get the link"). Every JSON shape that
// carries a video link sets its fields from this.
func videoShown(ev *models.Event, isGoing, isManager bool) (url string, hidden bool) {
	return videoWhen(ev, policy.CanSeeVideoLink(videoRevealOf(ev), isGoing, isManager))
}

// videoWhen is videoShown once the viewer's right to the link is decided (the
// date page also decides it for people holding only an exact-date invitation).
func videoWhen(ev *models.Event, canSee bool) (url string, hidden bool) {
	link := videoLinkOf(ev)
	switch {
	case link == "":
		return "", false
	case canSee:
		return link, false
	}
	return "", true
}

// applyVideoReveal fills one occurrence's video fields for a viewer.
// occurrenceToJSON calls it with (false, false), so a registered_only
// link stays hidden until a caller that knows the viewer's facts —
// occurrencesForEvents, the per-date editor — calls it again.
func applyVideoReveal(inst *occurrenceJSON, ev *models.Event, isGoing, isManager bool) {
	inst.VideoProvider = videoProviderOf(ev)
	inst.VideoReveal = videoRevealOf(ev)
	inst.VideoURL, inst.VideoHidden = videoShown(ev, isGoing, isManager)
}

// applyEventVideoReveal is applyVideoReveal for the series JSON: toEventJSON
// starts fail-closed and presentEvent reveals the link to the event's managers.
func applyEventVideoReveal(j *eventJSON, ev *models.Event, isGoing, isManager bool) {
	j.VideoURL, j.VideoHidden = videoShown(ev, isGoing, isManager)
}

// ---------------------------------------------------------------------------
// POST /api/v1/events/video/jitsi: a new random Jitsi room URL, made
// locally (no API call). Leaders, hosts and admins only.
// ---------------------------------------------------------------------------

// churchSlug is the room-name prefix from the church's name: lowercase,
// a-z0-9 and hyphens, "fold" when nothing usable is left.
func churchSlug(name string) string {
	var b strings.Builder
	lastHyphen := true // also trims a leading hyphen
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
		case !lastHyphen:
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "fold"
	}
	return slug
}

const (
	jitsiRoomAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	jitsiRoomLen      = 12
)

// jitsiRoom returns 12 random room characters from crypto/rand. Bytes at
// or above 252 (7*36) are rejected so every character is equally likely.
func jitsiRoom() (string, error) {
	const limit = 256 - 256%len(jitsiRoomAlphabet)
	out := make([]byte, 0, jitsiRoomLen)
	buf := make([]byte, 32)
	for len(out) < jitsiRoomLen {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) < limit && len(out) < jitsiRoomLen {
				out = append(out, jitsiRoomAlphabet[int(b)%len(jitsiRoomAlphabet)])
			}
		}
	}
	return string(out), nil
}

func (a *App) apiGenerateJitsiLink(c *gin.Context) {
	me := apiUser(c)
	leads, err := leadsApprovedGroup(a.DB, me.ID)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not generate a video link")
		return
	}
	hosts, err := hostsChurchEvent(a.DB, me.ID)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not generate a video link")
		return
	}
	if !policy.CanGenerateVideoLink(asPerson(me), leads, hosts) {
		jsonError(c, http.StatusForbidden, "only leaders, hosts, and admins can generate a video link")
		return
	}
	room, err := jitsiRoom()
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not generate a video link")
		return
	}
	c.JSON(http.StatusOK, jitsiLinkResponseJSON{URL: "https://" + a.jitsiDomain() + "/" + churchSlug(a.siteName()) + "-" + room})
}

// jitsiDomain reads the jitsi_domain setting (PLAN 5.8), defaulting to
// meet.jit.si. A value that isn't a plain host name also falls back to the
// default, so a typo in settings can't mint links onto a malformed host.
func (a *App) jitsiDomain() string {
	v, found, err := settingIn(a.DB, settingJitsiDomain)
	if err != nil || !found {
		return defaultJitsiDomain
	}
	d := strings.TrimSpace(v)
	d = strings.TrimPrefix(d, "https://")
	d = strings.TrimPrefix(d, "http://")
	if i := strings.Index(d, "/"); i >= 0 {
		d = d[:i]
	}
	d = strings.ToLower(d)
	if d == "" {
		return defaultJitsiDomain
	}
	for _, r := range d {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == ':':
		default:
			return defaultJitsiDomain
		}
	}
	return d
}
