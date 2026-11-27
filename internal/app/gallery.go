package app

import (
	"database/sql"
	"net/http"

	"fold/internal/policy"
)

// The component gallery shows every partial in every state, for the browser
// gates and for checking a real phone (docs/DECISIONS.md P18). Admins only.

type galleryData struct {
	Buttons      [][]Button
	LinkButtons  []LinkButton
	Fields       []Field
	Choices      []Choice
	Chips        []Chip
	Segment      Segment
	Menu         Menu
	Dialog       Dialog
	Empty        Empty
	Notices      []Notice
	Errors       []FieldError
	Toggle       Chip
	ToggleStatus string
}

func (a *App) galleryFacts(w http.ResponseWriter, r *http.Request) (pageFacts, bool) {
	var f pageFacts
	err := a.reader.Read(r.Context(), func(q *sql.Tx) error {
		var err error
		f, err = a.load(r.Context(), r, q)
		return err
	})
	if err != nil {
		a.serverError(w, r, err)
		return f, false
	}
	if !policyViewGallery(f) {
		a.notFound(w, r)
		return f, false
	}
	return f, true
}

func policyViewGallery(f pageFacts) bool { return policy.ViewGallery(viewerFacts(f.session)) }

func (a *App) gallery(w http.ResponseWriter, r *http.Request) {
	f, ok := a.galleryFacts(w, r)
	if !ok {
		return
	}
	var rows [][]Button
	for _, k := range []string{"primary", "secondary", "danger", "danger-outline", "ghost"} {
		rows = append(rows, []Button{
			{Kind: k, Label: "Save", Type: "button"},
			{Kind: k, Label: "Save", Type: "button", Disabled: true},
			{Kind: k, Label: "Saving", Type: "button", Busy: true},
		})
	}
	d := galleryData{
		Buttons:     rows,
		LinkButtons: []LinkButton{{Kind: "primary", Label: "Link as primary", Href: "#links"}, {Kind: "secondary", Label: "Link as secondary", Href: "#links"}},
		Fields: []Field{
			{ID: "g-empty", Name: "g-empty", Label: "Group name"},
			{ID: "g-hint", Name: "g-hint", Label: "Where you meet", Hint: "A room or an address.", Value: "Fellowship hall"},
			{ID: "g-error", Name: "g-error", Label: "Email", Type: "email", Value: "pat@", Error: "Enter an email address like name@example.org."},
			{ID: "g-disabled", Name: "g-disabled", Label: "Church", Value: "Cloverdale", Disabled: true},
			{ID: "g-readonly", Name: "g-readonly", Label: "Time zone", Value: "America/Boise", ReadOnly: true},
			{ID: "g-password", Name: "g-password", Label: "Token", Type: "password", Hint: "One is saved. Leave this blank to keep it."},
			{ID: "g-list", Name: "g-list", Label: "With suggestions", List: "g-list-options", Options: []string{"America/Boise", "America/Denver"}},
		},
		Choices: []Choice{
			{ID: "g-c1", Name: "g-c1", Type: "checkbox", Value: "1", Label: "Email me about new groups"},
			{ID: "g-c2", Name: "g-c2", Type: "checkbox", Value: "1", Label: "Checked", Checked: true},
			{ID: "g-c3", Name: "g-c3", Type: "checkbox", Value: "1", Label: "Disabled", Disabled: true},
			{ID: "g-c4", Name: "g-c4", Type: "checkbox", Value: "1", Label: "With an error", Error: "Confirm this to go on."},
			{ID: "g-r1", Name: "g-r", Type: "radio", Value: "a", Label: "Weekly", Hint: "Every week on the same day.", Checked: true},
			{ID: "g-r2", Name: "g-r", Type: "radio", Value: "b", Label: "Once"},
		},
		Chips:   []Chip{{Label: "Women"}, {Label: "Men", Pressed: true}, {Label: "Youth", Disabled: true}},
		Segment: Segment{Label: "Your answer", Name: "g-seg", Options: []SegmentOption{{Label: "Going", Value: "going", Pressed: true}, {Label: "Maybe", Value: "maybe"}, {Label: "Can't go", Value: "no"}}},
		Menu:    Menu{ID: "g-menu", Label: "More", Items: []MenuItem{{Label: "Edit", Href: "#menu"}, {Label: "Share", Href: "#menu"}}},
		Dialog: Dialog{ID: "g-dialog", OpenLabel: "Delete this", OpenKind: "danger-outline", Title: "Delete this group?",
			Text: "Its 12 members will be told. This cannot be undone.", CancelLabel: "Keep it", ConfirmLabel: "Delete",
			ConfirmKind: "danger", Action: "/admin/gallery/toggle", CSRF: a.csrfToken(r)},
		Empty: Empty{Image: "illustrations/all-caught-up.svg", Title: "No groups yet", Text: "Groups you join will appear here.",
			Action: &LinkButton{Kind: "primary", Label: "Find a group", Href: "#empty"}},
		Notices: []Notice{
			{Tone: "info", Title: "For your information", Text: "This is how a neutral message looks."},
			{Tone: "success", Title: "Saved", Text: "Your changes are saved."},
			{Tone: "warning", Title: "Heads up", Text: "Three people will no longer see this group."},
			{Tone: "danger", Title: "That could not be saved", Text: "Someone else changed this group. Here is what it says now."},
		},
		Errors: []FieldError{{ID: "g-error", Message: "Enter an email address like name@example.org."}},
		Toggle: Chip{Label: "Remind me", Name: "on", Value: "1", Type: "submit"},
	}
	f.Data = d
	a.render(w, r, http.StatusOK, "gallery", "", f.page)
}

// galleryToggle shows a fragment update working under the strict policy: a
// one-setting toggle that answers "Saved" (DEC-98). It stores nothing; the
// gallery has nothing to save.
func (a *App) galleryToggle(w http.ResponseWriter, r *http.Request) {
	f, ok := a.galleryFacts(w, r)
	if !ok {
		return
	}
	on := r.PostFormValue("on") == "1"
	next := "1"
	if on {
		next = "0"
	}
	f.Data = galleryData{Toggle: Chip{Label: "Remind me", Name: "on", Value: next, Type: "submit", Pressed: on}, ToggleStatus: "Saved"}
	if r.Header.Get("HX-Request") != "true" {
		http.Redirect(w, r, "/admin/gallery", http.StatusSeeOther)
		return
	}
	a.render(w, r, http.StatusOK, "gallery", "toggle", f.page)
}
