package app

import (
	"errors"
	"net/http"
	"slices"
	"strconv"

	"fold/internal/accounts"
	"fold/internal/write"
)

// The profile: a person's own name, and whether leaders of their groups see
// their email (P5).

func (a *App) profileData(r *http.Request, f pageFacts, name string, version int64, nameErr string, conflict bool) any {
	return a.profileDataWith(f, name, version, nameErr, conflict, map[string]string{"saved": "Saved."}[r.URL.Query().Get("done")])
}

func (a *App) profileDataWith(f pageFacts, name string, version int64, nameErr string, conflict bool, status string) any {
	return a.profileView(f, name, version, nameErr, conflict, status, "", "")
}

// zoneSelect is the profile's time zone: the person's, else the church's,
// from the same full list setup offers, plus the person's own if a browser
// gave a name that list leaves out.
func zoneSelect(f pageFacts, zoneErr string) Select {
	current := viewerZone(f).String()
	s := Select{ID: "zone", Name: "zone", Label: "Your time zone", Hint: "Event times and notices are shown in it.", Error: zoneErr, Trigger: "/profile/zone"}
	if !slices.Contains(zoneNames, current) {
		s.Options = append(s.Options, Option{Value: current, Label: current, Selected: true})
	}
	for _, z := range zoneNames {
		s.Options = append(s.Options, Option{Value: z, Label: z, Selected: z == current})
	}
	return s
}

type profilePage struct {
	Name       Field
	Version    int64
	Email      string
	Contact    Chip
	ContactErr string
	Status     string
	Conflict   bool
	Zone       Select
	ZoneStatus string
}

func (a *App) profileView(f pageFacts, name string, version int64, nameErr string, conflict bool, status, zoneStatus, zoneErr string) profilePage {
	p := f.session.Person
	nameF := Field{ID: "name", Name: "name", Label: "Your name", Hint: "As people at church know you. It's shown on rosters and notices.",
		Value: name, MaxLength: maxName, Required: true, Error: nameErr, Autocomplete: "name"}
	return profilePage{Name: nameF, Version: version, Email: p.Email,
		// Pressing the switch posts the state it changes to.
		Contact: Chip{Label: "Show my email to leaders of my groups", Name: "hide", Value: map[bool]string{true: "0", false: "1"}[p.ContactHidden], Type: "submit", Pressed: !p.ContactHidden},
		Status:  status, Conflict: conflict, Zone: zoneSelect(f, zoneErr), ZoneStatus: zoneStatus}
}

// tooMany is what a person sees when their own changes outpace the limit.
const tooMany = "That's a lot of changes in a short time, so this one could not be saved. Try again in a little while."

func (a *App) getProfile(w http.ResponseWriter, r *http.Request) {
	f, ok := a.signedIn(w, r, nil)
	if !ok {
		return
	}
	f.Data = a.profileData(r, f, f.session.Person.Name, f.session.Person.Version, "", false)
	a.render(w, r, http.StatusOK, "profile", "", f.page)
}

func (a *App) postProfileName(w http.ResponseWriter, r *http.Request) {
	f, ok := a.signedIn(w, r, nil)
	if !ok {
		return
	}
	name, okName := text(r, "name", maxName)
	version, _ := strconv.ParseInt(r.PostFormValue("version"), 10, 64)
	if !okName || name == "" {
		f.Data = a.profileData(r, f, name, version, "Enter your name.", false)
		a.render(w, r, http.StatusUnprocessableEntity, "profile", "", f.page)
		return
	}
	if !a.limits.profile.Allow(f.session.Person.UID) {
		f.Data = a.profileData(r, f, name, version, tooMany, false)
		a.render(w, r, http.StatusUnprocessableEntity, "profile", "", f.page)
		return
	}
	err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), accounts.EditName{Name: name, Version: version})
	switch {
	case errors.Is(err, write.ErrConflict):
		f.Data = a.profileData(r, f, f.session.Person.Name, f.session.Person.Version, "", true)
		a.render(w, r, http.StatusConflict, "profile", "", f.page)
		return
	case errors.Is(err, write.ErrSignedOut):
		http.Redirect(w, r, "/signin", http.StatusSeeOther)
		return
	case err != nil:
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/profile?done=saved", http.StatusSeeOther)
}

// postProfileContact flips the one switch. With htmx it answers with the
// switch and "Saved"; without, it redirects back to the profile.
func (a *App) postProfileContact(w http.ResponseWriter, r *http.Request) {
	f, ok := a.signedIn(w, r, nil)
	if !ok {
		return
	}
	hidden := r.PostFormValue("hide") == "1"
	if !a.limits.profile.Allow(f.session.Person.UID) {
		d := a.profileView(f, f.session.Person.Name, f.session.Person.Version, "", false, "", "", "")
		d.ContactErr = tooMany
		f.Data = d
		if r.Header.Get("HX-Request") == "true" {
			a.render(w, r, http.StatusOK, "profile", "contact", f.page) // htmx swaps only a 200
			return
		}
		a.render(w, r, http.StatusUnprocessableEntity, "profile", "", f.page)
		return
	}
	err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), accounts.SetContactHidden{Hidden: hidden})
	if errors.Is(err, write.ErrSignedOut) {
		http.Redirect(w, r, "/signin", http.StatusSeeOther)
		return
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if r.Header.Get("HX-Request") != "true" {
		http.Redirect(w, r, "/profile?done=saved", http.StatusSeeOther)
		return
	}
	f.session.Person.ContactHidden = hidden
	f.Data = a.profileDataWith(f, f.session.Person.Name, f.session.Person.Version, "", false, "Saved.")
	a.render(w, r, http.StatusOK, "profile", "contact", f.page)
}

// postProfileZone saves the person's time zone. From the profile's select
// it answers with the select and "Saved" (htmx) or back to the profile. The
// browser's first-visit guess (first=1, static/zone.js) answers 204 whatever
// happened: it saves only while the person has no zone.
func (a *App) postProfileZone(w http.ResponseWriter, r *http.Request) {
	f, ok := a.signedIn(w, r, nil)
	if !ok {
		return
	}
	first := r.PostFormValue("first") == "1"
	zone := r.PostFormValue("zone")
	if first && f.session.Person.Zone != "" {
		w.WriteHeader(http.StatusNoContent) // a guess never replaces a zone, so skip the write
		return
	}
	if !a.limits.profile.Allow(f.session.Person.UID) {
		if first {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		f.Data = a.profileView(f, f.session.Person.Name, f.session.Person.Version, "", false, "", "", tooMany)
		if r.Header.Get("HX-Request") == "true" {
			a.render(w, r, http.StatusOK, "profile", "zone", f.page) // htmx swaps only a 200
			return
		}
		a.render(w, r, http.StatusUnprocessableEntity, "profile", "", f.page)
		return
	}
	err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), accounts.SetZone{Zone: zone, First: first})
	if first {
		if err != nil && !errors.Is(err, write.ErrNotAllowed) && !errors.Is(err, write.ErrSignedOut) {
			a.log.Warn("browser zone", "err", err)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	htmx := r.Header.Get("HX-Request") == "true"
	switch {
	case errors.Is(err, write.ErrSignedOut):
		http.Redirect(w, r, "/signin", http.StatusSeeOther)
		return
	case errors.Is(err, write.ErrNotAllowed):
		f.Data = a.profileView(f, f.session.Person.Name, f.session.Person.Version, "", false, "", "", "Choose a time zone from the list.")
		block := ""
		if htmx {
			block = "zone"
		}
		a.render(w, r, http.StatusUnprocessableEntity, "profile", block, f.page)
		return
	case err != nil:
		a.serverError(w, r, err)
		return
	}
	if !htmx {
		http.Redirect(w, r, "/profile?done=saved", http.StatusSeeOther)
		return
	}
	f.session.Person.Zone = zone
	f.Data = a.profileView(f, f.session.Person.Name, f.session.Person.Version, "", false, "", "Saved.", "")
	a.render(w, r, http.StatusOK, "profile", "zone", f.page)
}
