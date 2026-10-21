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
	settingJitsiDomain = "jitsi_domain"
	settingChurchName  = "church_name"
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

// validVideoURL: an absolute https:// URL with a host and no spaces.
func validVideoURL(s string) bool {
	if s == "" || strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

// parseVideoEdits reads videoProvider, videoUrl and videoReveal from a
// body over base (nil on create) and validates the effective values,
// answering 400 on a bad one. none clears the URL; any other provider
// needs a full https:// address of at most 2048 characters.
func parseVideoEdits(c *gin.Context, body eventBody, base *models.Event) (videoEdits, bool) {
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
		jsonError(c, http.StatusBadRequest, "videoProvider must be none, link, or jitsi")
		return ed, false
	}
	switch ed.reveal {
	case models.VideoRevealAlways, models.VideoRevealRegisteredOnly:
	default:
		jsonError(c, http.StatusBadRequest, "videoReveal must be always or registered_only")
		return ed, false
	}
	if len(ed.url) > maxVideoURLLen {
		jsonError(c, http.StatusBadRequest, "video link is too long")
		return ed, false
	}
	if ed.provider == models.VideoProviderNone {
		// A supplied address is still validated, then cleared: none
		// means the event has no video link.
		if ed.url != "" && !validVideoURL(ed.url) {
			jsonError(c, http.StatusBadRequest, "video link must be a full https:// address")
			return ed, false
		}
		ed.url = ""
		return ed, true
	}
	if !validVideoURL(ed.url) {
		jsonError(c, http.StatusBadRequest, "video link must be a full https:// address")
		return ed, false
	}
	return ed, true
}

// applyVideoReveal fills one occurrence's video fields for a viewer.
// occurrenceToJSON calls it with (false, false), so a registered_only
// link stays hidden until a caller that knows the viewer's facts —
// occurrencesForEvents, the per-date editor — calls it again.
func applyVideoReveal(inst *occurrenceJSON, ev *models.Event, isGoing, isManager bool) {
	inst.VideoProvider = videoProviderOf(ev)
	inst.VideoReveal = videoRevealOf(ev)
	link := videoLinkOf(ev)
	inst.VideoURL = ""
	inst.VideoHidden = false
	if link == "" {
		return
	}
	if policy.CanSeeVideoLink(inst.VideoReveal, isGoing, isManager) {
		inst.VideoURL = link
	} else {
		inst.VideoHidden = true
	}
}

// revealVideoToManager undoes toEventJSON's fail-closed default for a
// viewer who manages the event (a leader, a host, or an admin).
func revealVideoToManager(j *eventJSON, ev *models.Event) {
	if link := videoLinkOf(ev); link != "" &&
		policy.CanSeeVideoLink(videoRevealOf(ev), false, true) {
		j.VideoURL = link
		j.VideoHidden = false
	}
}

// ---------------------------------------------------------------------------
// POST /api/v1/events/video/jitsi: a new random Jitsi room URL, made
// locally (no API call). Leaders, hosts and admins only.
// ---------------------------------------------------------------------------

// jitsiDomain reads the jitsi_domain setting, defaulting to meet.jit.si.
// A value that isn't a plain host name also falls back to the default, so
// a typo in settings can't mint links onto a malformed host.
func (a *App) jitsiDomain() string {
	var s models.Setting
	if err := a.DB.Where("key = ?", settingJitsiDomain).First(&s).Error; err != nil {
		return defaultJitsiDomain
	}
	d := strings.TrimSpace(s.Value)
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

func (a *App) churchSlug() string {
	var s models.Setting
	if err := a.DB.Where("key = ?", settingChurchName).First(&s).Error; err == nil &&
		strings.TrimSpace(s.Value) != "" {
		return churchSlug(s.Value)
	}
	return churchSlug(a.siteName())
}

const jitsiRoomAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// jitsiRoom returns 10 random room characters from crypto/rand.
func jitsiRoom() (string, error) {
	raw := make([]byte, 10)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	out := make([]byte, len(raw))
	for i, b := range raw {
		out[i] = jitsiRoomAlphabet[int(b)%len(jitsiRoomAlphabet)]
	}
	return string(out), nil
}

func (a *App) apiGenerateJitsiLink(c *gin.Context) {
	me := apiUser(c)
	allowed := policy.IsAdmin(asPerson(me))
	if !allowed {
		var leads, hosts int64
		a.DB.Model(&models.Membership{}).
			Where("user_id = ? AND role = ? AND status = ?", me.ID, models.MembershipLeader, "active").
			Count(&leads)
		a.DB.Model(&models.EventHost{}).Where("user_id = ?", me.ID).Count(&hosts)
		allowed = leads > 0 || hosts > 0
	}
	if !allowed {
		jsonError(c, http.StatusForbidden, "only leaders, hosts, and admins can generate a video link")
		return
	}
	room, err := jitsiRoom()
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not generate a video link")
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": "https://" + a.jitsiDomain() + "/" + a.churchSlug() + "-" + room})
}
