package main

import (
	"fmt"
	"testing"
	"time"

	"fold/internal/mail"
	"fold/internal/notify"
	"fold/models"
)

// suppressionFixtureUsers makes one account per name whose email is
// name@example.com.
func suppressionFixtureUsers(t *testing.T, app *App, names ...string) map[string]*models.User {
	t.Helper()
	out := map[string]*models.User{}
	for _, name := range names {
		out[name] = newTestUser(t, app, name, "password1234", models.RoleMember)
	}
	return out
}

// Preview and snapshot skip labels (recipientFacts) are the same for a hashed
// row and a legacy NULL-hash row, a cleared row blocks nothing, and any open
// complaint gives "marked our email as spam" whichever reason was written
// first.
func TestRecipientFactsReadHashedAndLegacySuppressions(t *testing.T) {
	app := testApp(t)
	users := suppressionFixtureUsers(t, app, "hashed", "legacy", "cleared", "complaintfirst", "bouncefirst",
		"legacycomplaintfirst", "legacybouncefirst", "complaintonly", "clean")
	// An account stored with capitals and padding still matches.
	app.DB.Model(users["hashed"]).Update("email", " Hashed@Example.COM ")
	suppressHashed(t, app, "hashed@example.com", models.SuppressHardBounce)
	suppress(t, app, "legacy@example.com", models.SuppressHardBounce)
	h := mail.StoredAddressHash("cleared@example.com")
	now := time.Now()
	if err := app.DB.Create(&models.Suppression{Channel: "email", Address: "cleared@example.com", AddressHash: &h,
		Reason: models.SuppressHardBounce, ClearedAt: &now}).Error; err != nil {
		t.Fatal(err)
	}
	suppressHashed(t, app, "complaintfirst@example.com", models.SuppressComplaint)
	suppressHashed(t, app, "complaintfirst@example.com", models.SuppressHardBounce)
	suppressHashed(t, app, "bouncefirst@example.com", models.SuppressHardBounce)
	suppressHashed(t, app, "bouncefirst@example.com", models.SuppressComplaint)
	suppress(t, app, "legacycomplaintfirst@example.com", models.SuppressComplaint)
	suppress(t, app, "legacycomplaintfirst@example.com", models.SuppressHardBounce)
	suppress(t, app, "legacybouncefirst@example.com", models.SuppressHardBounce)
	suppress(t, app, "legacybouncefirst@example.com", models.SuppressComplaint)
	suppressHashed(t, app, "complaintonly@example.com", models.SuppressComplaint)

	var ids []uint
	for _, u := range users {
		ids = append(ids, u.ID)
	}
	facts, err := recipientFacts(app.DB, ids, notify.CatGroups)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range facts {
		got[f.User.Name] = f.Skip
	}
	want := map[string]string{
		"hashed": skipNotWorking, "legacy": skipNotWorking, "cleared": "", "clean": "",
		"complaintfirst": skipComplained, "bouncefirst": skipComplained,
		"legacycomplaintfirst": skipComplained, "legacybouncefirst": skipComplained,
		"complaintonly": skipComplained,
	}
	for name, skip := range want {
		if g, ok := got[name]; !ok || g != skip {
			t.Errorf("%s: skip = %q (present %v), want %q", name, g, ok, skip)
		}
	}
}

// suppressionDump renders every column of every row with the pointer fields
// (AddressHash, ClearedAt, ClearedBy) shown by value. Printing the structs with
// %+v shows pointer addresses, which differ between two reads of equal rows.
func suppressionDump(rows []models.Suppression) string {
	str := func(p *string) string {
		if p == nil {
			return "<nil>"
		}
		return "&" + *p
	}
	var out string
	for _, r := range rows {
		cleared, by := "<nil>", "<nil>"
		if r.ClearedAt != nil {
			cleared = r.ClearedAt.UTC().Format(time.RFC3339Nano)
		}
		if r.ClearedBy != nil {
			by = fmt.Sprint(*r.ClearedBy)
		}
		out += fmt.Sprintf("{ID:%d Channel:%q Address:%q AddressHash:%s Reason:%q Provider:%q Detail:%q CreatedAt:%s ClearedAt:%s ClearedBy:%s}\n",
			r.ID, r.Channel, r.Address, str(r.AddressHash), r.Reason, r.Provider, r.Detail,
			r.CreatedAt.UTC().Format(time.RFC3339Nano), cleared, by)
	}
	return out
}

// A suppression stays on the address: a later account with the same address
// in another case or padding is blocked, and erasing an account leaves every
// suppression row byte-identical.
func TestSuppressionsSurviveAddressReuseAndErasure(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	admin := loginAPI(t, app, "admin", "password1234")
	old := newTestUser(t, app, "pat", "password1234", models.RoleMember)
	suppressHashed(t, app, "pat@example.com", models.SuppressHardBounce)
	suppress(t, app, "sam@example.com", models.SuppressHardBounce)

	var before []models.Suppression
	app.DB.Order("id").Find(&before)
	if w := admin.do("DELETE", "/api/v1/admin/users/"+itoa(old.ID), nil, true); w.Code != 200 && w.Code != 204 {
		t.Fatalf("delete user: %d %s", w.Code, w.Body.String())
	}
	var after []models.Suppression
	app.DB.Order("id").Find(&after)
	if b, a := suppressionDump(before), suppressionDump(after); b != a || len(after) != 2 {
		t.Fatalf("erasure changed suppressions:\nbefore %s\nafter  %s", b, a)
	}
	// Someone signs up later with the same address in another case and padding.
	next := &models.User{Email: " PAT@Example.com ", Role: models.RoleMember, Name: "pat again", Status: models.UserActive}
	if err := app.DB.Create(next).Error; err != nil {
		t.Fatal(err)
	}
	rows, _, err := readEmailRecipients(app.DB, []uint{next.ID}, true)
	if err != nil || len(rows) != 1 || rows[0].EmailSuppressions != models.SuppressHardBounce {
		t.Fatalf("reused address: %+v, %v", rows, err)
	}
	facts, err := recipientFacts(app.DB, []uint{next.ID}, notify.CatGroups)
	if err != nil || len(facts) != 1 || facts[0].Skip != skipNotWorking {
		t.Fatalf("reused address facts: %+v, %v", facts, err)
	}
}

// snapshotSkip is the label preview and the send snapshot give someone with
// these rows (recipientFacts): any open complaint gives "marked our email as
// spam", any other open row "email not working", cleared rows nothing.
func (c suppressionCase) snapshotSkip() string {
	skip := ""
	for _, sp := range c.specs {
		switch {
		case sp.cleared:
		case sp.reason == models.SuppressComplaint:
			return skipComplained
		default:
			skip = skipNotWorking
		}
	}
	return skip
}

// sendTimeSkip is the label announcement send gives when the rows appear after
// the snapshot: the reason optional mail is refused with, which follows the
// older row, so a complaint written after a hard bounce says "not working".
func (c suppressionCase) sendTimeSkip() string {
	switch c.optional {
	case "":
		return ""
	case models.SuppressComplaint:
		return skipComplained
	}
	return skipNotWorking
}

// addSuppressionMembers adds one active member to the fixture's group per
// case, named sup00, sup01 and so on, and returns them by case name. It
// writes no suppression rows.
func (f composeFixture) addSuppressionMembers(t *testing.T, cases []suppressionCase) map[string]*models.User {
	t.Helper()
	out := map[string]*models.User{}
	for i, tc := range cases {
		u := newTestUser(t, f.app, fmt.Sprintf("sup%02d", i), "password1234", models.RoleMember)
		addMember(t, f.app, u, f.group, models.MembershipMember)
		out[tc.name] = u
	}
	return out
}
