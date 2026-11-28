package app

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"fold/internal/accounts"
	"fold/internal/events"
	"fold/internal/httpx"
	"fold/internal/people"
	"fold/internal/policy"
	"fold/internal/write"
)

// A person's calendar feed (DEC-71, PLAN slice 2 milestone 4b): their Just
// mine, read by a calendar app with the link alone, since it cannot sign
// in. The link is a standing secret (AGENTS.md, "Links are secrets").

const (
	feedBack    = 30 * 24 * time.Hour
	feedAhead   = 183 * 24 * time.Hour
	feedRows    = 1000
	feedRefresh = time.Hour
)

// feedGone is the one answer for every link that does not work: never
// made, replaced, turned off, or its account no longer active.
var feedGone = []byte("There is no calendar at this address.\n")

func feedPath(token string) string { return "/feed/" + token + "/calendar.ics" }

func (a *App) getFeed(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	w.Header().Set("Cache-Control", "private, max-age=900")
	// Limits are spent before the lookup, so a guess costs the same as a
	// match; the token is counted by its hash.
	client := httpx.ClientKey(a.cfg.Proxies.ClientIP(r))
	if !a.limits.feedClient.Allow(client) || !a.limits.feedToken.Allow(hex.EncodeToString(accounts.Hash(token))) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("Too many requests for this calendar. It will work again in a while.\n"))
		return
	}
	now := a.clock.Now()
	var body []byte
	err := a.reader.Read(r.Context(), func(q *sql.Tx) error {
		p, err := accounts.FeedOwner(r.Context(), q, token)
		if errors.Is(err, people.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		f, err := a.load(r.Context(), r, q)
		if err != nil {
			return err
		}
		s := events.Scope{Viewer: events.Viewer{ID: p.ID, Admin: policy.IsAdmin(p.Policy())}, Answerer: p.ID, JustMine: true, Cancelled: true, Details: true}
		list, err := events.Calendar(r.Context(), q, s, now.Add(-feedBack), now.Add(feedAhead), time.Unix(0, 0), now, feedRows)
		if err != nil {
			return err
		}
		out := make([]events.ICSEvent, 0, len(list))
		for _, c := range list {
			out = append(out, a.dateEvent(c.UID, c.Title, c.Place, c.Zone, c.StartsAt, c.EndsAt, c.About, c.Video, c.Answer, c.Cancelled))
		}
		body = events.ICS(f.Church, feedRefresh, out)
		return nil
	})
	if err != nil {
		a.log.Error("feed", "err", err)
		http.Error(w, "Something went wrong on our side. Please try again in a moment.", http.StatusInternalServerError)
		return
	}
	if body == nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		w.Write(feedGone)
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Write(body)
}

// feedView is the profile's feed section. URL is set only on the page that
// made the link: the server keeps its hash alone and cannot show it again.
type feedView struct {
	On           bool
	URL          Field
	Status       string
	Replace, Off Dialog
}

func (a *App) feedSection(r *http.Request, on bool, token, status string) feedView {
	v := feedView{On: on || token != "", Status: status,
		Replace: Dialog{ID: "feed-new", OpenLabel: "Make a new link", OpenKind: "secondary", Title: "Make a new link?",
			Text:        "The link you have now stops working at once. A calendar using it stops updating until you add the new one.",
			CancelLabel: "Keep this link", ConfirmLabel: "Make a new link", ConfirmKind: "primary", Action: "/profile/feed/new", CSRF: a.csrfToken(r)},
		Off: Dialog{ID: "feed-off", OpenLabel: "Turn off", OpenKind: "danger-outline", Title: "Turn off your calendar feed?",
			Text:        "Its link stops working at once, and calendars using it stop updating. You can turn it on again with a new link.",
			CancelLabel: "Keep it on", ConfirmLabel: "Turn off", ConfirmKind: "danger", Action: "/profile/feed/off", CSRF: a.csrfToken(r)}}
	if token != "" {
		v.URL = Field{ID: "feed-url", Name: "feed", Label: "Your calendar's address", Value: a.origin + feedPath(token), ReadOnly: true}
	}
	return v
}

// postFeed turns the feed on or makes a new link, and shows the link on the
// page it answers with, never in an address.
func (a *App) postNewFeed(w http.ResponseWriter, r *http.Request) {
	f, ok := a.signedIn(w, r, nil)
	if !ok {
		return
	}
	if !a.limits.profile.Allow(f.session.Person.UID) {
		a.showProfileFeed(w, r, f, a.feedSection(r, true, "", tooMany))
		return
	}
	c := &accounts.NewFeed{}
	if !a.runFeed(w, r, c) {
		return
	}
	a.showProfileFeed(w, r, f, a.feedSection(r, true, c.Token, "Your calendar feed is on."))
}

func (a *App) postEndFeed(w http.ResponseWriter, r *http.Request) {
	f, ok := a.signedIn(w, r, nil)
	if !ok {
		return
	}
	if !a.limits.profile.Allow(f.session.Person.UID) {
		a.showProfileFeed(w, r, f, a.feedSection(r, true, "", tooMany))
		return
	}
	if a.runFeed(w, r, accounts.EndFeed{}) {
		http.Redirect(w, r, "/profile?done=feed-off#feed", http.StatusSeeOther)
	}
}

func (a *App) runFeed(w http.ResponseWriter, r *http.Request, c write.Command) bool {
	err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), c)
	switch {
	case errors.Is(err, write.ErrSignedOut):
		http.Redirect(w, r, "/signin", http.StatusSeeOther)
	case errors.Is(err, write.ErrNotAllowed):
		a.notFound(w, r)
	case err != nil:
		a.serverError(w, r, err)
	default:
		return true
	}
	return false
}

func (a *App) showProfileFeed(w http.ResponseWriter, r *http.Request, f pageFacts, feed feedView) {
	d := a.profileView(r, f, f.session.Person.Name, f.session.Person.Version, "", false, "", "", "")
	d.Feed = feed
	f.Data = d
	a.render(w, r, http.StatusOK, "profile", "", f.page)
}
