package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"fold/internal/policy"
	"fold/models"
)

func assertContactKeys(t *testing.T, row map[string]any, subject *models.User, visible bool) {
	t.Helper()
	for key, want := range map[string]string{"email": subject.Email, "phone": subject.Phone} {
		got, present := row[key]
		if present != visible || (visible && got != want) {
			t.Fatalf("%s: got %v (present=%v), visible=%v, want %q; row=%v", key, got, present, visible, want, row)
		}
	}
}

func TestContactDetailsJSONKeys(t *testing.T) {
	adult := &models.User{ID: 1, AccountType: models.AccountAdult, Email: "adult@example.com", Phone: "123"}
	hidden := *adult
	hidden.HideContactFromLeaders = true
	teen := *adult
	teen.AccountType = models.AccountTeen
	viewer := &models.User{ID: 2, AccountType: models.AccountAdult}
	admin := &models.User{ID: 3, Role: models.RoleAdmin, Status: models.UserActive}
	for _, tc := range []struct {
		name            string
		viewer, subject *models.User
		context         policy.Contact
		visible         bool
	}{
		{"signed out", nil, adult, policy.Contact{GroupLeader: true, GroupMember: true}, false},
		{"self", &hidden, &hidden, policy.Contact{}, true},
		{"teen self", &teen, &teen, policy.Contact{}, true},
		{"admin", admin, &teen, policy.Contact{}, true},
		{"own group", viewer, adult, policy.Contact{GroupLeader: true, GroupMember: true}, true},
		{"hidden", viewer, &hidden, policy.Contact{GroupLeader: true, GroupMember: true}, false},
		{"teen", viewer, &teen, policy.Contact{GroupLeader: true, GroupMember: true}, false},
		{"confirmed guardian", viewer, &teen, policy.Contact{ConfirmedGuardian: true}, true},
		{"pending guardian", viewer, &teen, policy.Contact{}, false},
		{"unrelated leader", viewer, adult, policy.Contact{GroupLeader: true}, false},
		{"own host", viewer, adult, policy.Contact{EventManager: true, EventRegistrant: true}, true},
		{"unrelated host", viewer, adult, policy.Contact{EventManager: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			email, phone := contactDetails(tc.viewer, tc.subject, tc.context)
			body, err := json.Marshal(memberJSON{Email: email, Phone: phone})
			if err != nil {
				t.Fatal(err)
			}
			var row map[string]any
			if err := json.Unmarshal(body, &row); err != nil {
				t.Fatal(err)
			}
			assertContactKeys(t, row, tc.subject, tc.visible)
		})
	}
}

func TestContactPrivacyRosterAndAttendees(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "leader", "password1234", models.RoleMember)
	admin := newTestUser(t, app, "admin", "password1234", models.RoleAdmin)
	adult := newTestUser(t, app, "adult", "password1234", models.RoleMember)
	hidden := newTestUser(t, app, "hidden", "password1234", models.RoleMember)
	teen := newTestUser(t, app, "teen", "password1234", models.RoleMember)
	outsider := newTestUser(t, app, "outsider", "password1234", models.RoleMember)
	for _, u := range []*models.User{adult, hidden, teen} {
		u.Phone = "555-" + itoa(u.ID)
		u.AvatarKey = "test-avatar.png"
		if u == hidden {
			u.HideContactFromLeaders = true
		}
		if u == teen {
			u.AccountType = models.AccountTeen
			birth := time.Now().UTC().AddDate(-15, 0, 0)
			u.BirthDate = &birth
		}
		if err := app.DB.Save(u).Error; err != nil {
			t.Fatal(err)
		}
	}
	g := newTestGroup(t, app, leader, "Group", models.GroupActive)
	// Even leadership of another group shared with this adult does not confer
	// contact rights in the group/event currently being presented.
	otherGroup := newTestGroup(t, app, outsider, "Other group", models.GroupActive)
	if err := app.DB.Create(&models.Membership{UserID: adult.ID, GroupID: otherGroup.ID, Status: "active", Role: models.MembershipMember}).Error; err != nil {
		t.Fatal(err)
	}
	for _, u := range []*models.User{adult, hidden, teen, outsider} {
		if err := app.DB.Create(&models.Membership{UserID: u.ID, GroupID: g.ID, Status: "active", Role: models.MembershipMember}).Error; err != nil {
			t.Fatal(err)
		}
	}
	ev, occ := seedEvent(t, app, g, leader.ID, time.Now().UTC().Add(24*time.Hour))
	for _, u := range []*models.User{adult, hidden, teen} {
		if err := app.DB.Create(&models.Registration{UserID: &u.ID, OccurrenceID: occ.ID, Status: models.RegGoing}).Error; err != nil {
			t.Fatal(err)
		}
	}
	lc := loginAPI(t, app, leader.Email, "password1234")
	ac := loginAPI(t, app, admin.Email, "password1234")
	oc := loginAPI(t, app, outsider.Email, "password1234")
	sc := newAPIClient(t, app)
	for _, tc := range []struct {
		name            string
		client          *apiClient
		manage, isAdmin bool
	}{
		{"own leader", lc, true, false}, {"admin", ac, true, true}, {"unrelated leader", oc, false, false}, {"signed out", sc, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.client.do("GET", "/api/v1/groups/"+itoa(g.ID), nil, false)
			if w.Code != http.StatusOK {
				t.Fatalf("group: %d %s", w.Code, w.Body.String())
			}
			var body struct {
				Members []map[string]any `json:"members"`
			}
			decodeJSON(t, w, &body)
			if tc.client == sc && len(body.Members) != 0 {
				t.Fatal("signed-out viewer received a roster")
			}
			if tc.client != sc && len(body.Members) < 3 {
				t.Fatalf("expected adult roster identities: %v", body.Members)
			}
			if tc.manage && len(body.Members) != 5 {
				t.Fatalf("manager must see every roster identity: %v", body.Members)
			}
			for _, row := range body.Members {
				for _, u := range []*models.User{adult, hidden, teen} {
					if row["name"] != u.PublicName() {
						continue
					}
					assertContactKeys(t, row, u, tc.isAdmin || (tc.manage && u == adult))
					if u == teen && row["avatarUrl"] != "" {
						t.Fatalf("teen avatar leaked: %v", row)
					}
				}
			}
			w = tc.client.do("GET", "/api/v1/events/"+itoa(ev.ID), nil, false)
			if w.Code != http.StatusOK {
				t.Fatalf("event: %d %s", w.Code, w.Body.String())
			}
			var page struct {
				Occurrences []struct {
					Attendees []map[string]any `json:"attendees"`
				} `json:"occurrences"`
			}
			decodeJSON(t, w, &page)
			if len(page.Occurrences) != 1 {
				t.Fatalf("occurrences: %+v", page)
			}
			wantAttendees := 2
			if tc.manage {
				wantAttendees = 3
			}
			if tc.client == sc {
				wantAttendees = 0
			}
			if len(page.Occurrences[0].Attendees) != wantAttendees {
				t.Fatalf("attendees: got %v, want %d identities", page.Occurrences[0].Attendees, wantAttendees)
			}
			for _, row := range page.Occurrences[0].Attendees {
				for _, u := range []*models.User{adult, hidden, teen} {
					if row["name"] == u.PublicName() {
						assertContactKeys(t, row, u, tc.isAdmin || (tc.manage && u == adult))
					}
				}
			}
			if !tc.isAdmin {
				for _, u := range []*models.User{hidden, teen} {
					if strings.Contains(w.Body.String(), u.Email) || strings.Contains(w.Body.String(), u.Phone) {
						t.Fatalf("hidden contact leaked: %s", w.Body.String())
					}
				}
			}
		})
	}
	// The same leader gets guardian rights only from a confirmed link.
	link := models.GuardianLink{GuardianUserID: leader.ID, TeenUserID: &teen.ID, Status: models.GuardianPending}
	if err := app.DB.Create(&link).Error; err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{models.GuardianPending, models.GuardianConfirmed, models.GuardianRevoked, models.GuardianRejected} {
		if err := app.DB.Model(&link).Update("status", status).Error; err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/api/v1/groups/" + itoa(g.ID), "/api/v1/events/" + itoa(ev.ID)} {
			w := lc.do("GET", path, nil, false)
			visible := status == models.GuardianConfirmed
			if w.Code != 200 || strings.Contains(w.Body.String(), teen.Email) != visible || strings.Contains(w.Body.String(), teen.Phone) != visible {
				t.Fatalf("guardian status=%s %s: %d %s", status, path, w.Code, w.Body.String())
			}
		}
	}
}

func TestContactPrivacyChurchEventHost(t *testing.T) {
	app := testApp(t)
	host := newTestUser(t, app, "host", "password1234", models.RoleMember)
	otherHost := newTestUser(t, app, "otherhost", "password1234", models.RoleMember)
	adult := newTestUser(t, app, "adult", "password1234", models.RoleMember)
	hidden := newTestUser(t, app, "hidden", "password1234", models.RoleMember)
	if err := app.DB.Model(hidden).Updates(map[string]any{"hide_contact_from_leaders": true, "phone": "hidden-phone"}).Error; err != nil {
		t.Fatal(err)
	}
	g := newTestGroup(t, app, host, "Seed group", models.GroupActive)
	ev, occ := seedEvent(t, app, g, host.ID, time.Now().UTC().Add(24*time.Hour))
	other, _ := seedEvent(t, app, g, otherHost.ID, time.Now().UTC().Add(24*time.Hour))
	if err := app.DB.Model(&models.Event{}).Where("id IN ?", []uint{ev.ID, other.ID}).Update("group_id", nil).Error; err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct{ event, user uint }{{ev.ID, host.ID}, {other.ID, otherHost.ID}} {
		if err := app.DB.Create(&models.EventHost{EventID: pair.event, UserID: pair.user, AddedBy: pair.user}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []*models.User{adult, hidden} {
		if err := app.DB.Create(&models.Registration{UserID: &u.ID, OccurrenceID: occ.ID, Status: models.RegGoing}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		user    *models.User
		visible bool
	}{{host, true}, {otherHost, false}} {
		client := loginAPI(t, app, tc.user.Email, "password1234")
		w := client.do("GET", "/api/v1/events/"+itoa(ev.ID), nil, false)
		if w.Code != 200 {
			t.Fatalf("event: %d %s", w.Code, w.Body.String())
		}
		var page struct {
			Event       map[string]any `json:"event"`
			Occurrences []struct {
				Attendees []map[string]any `json:"attendees"`
			} `json:"occurrences"`
		}
		decodeJSON(t, w, &page)
		if _, ok := page.Event["hosts"]; ok {
			t.Fatal("non-admin received host-management output")
		}
		if len(page.Occurrences) != 1 {
			t.Fatalf("occurrences: %+v", page)
		}
		for _, row := range page.Occurrences[0].Attendees {
			if row["name"] == adult.PublicName() {
				assertContactKeys(t, row, adult, tc.visible)
			}
			if row["name"] == hidden.PublicName() {
				assertContactKeys(t, row, hidden, false)
			}
		}
	}
	// Being a host is not itself a registration/contact relationship.
	rows := []attendeeJSON{{UserID: &adult.ID, Status: models.RegRemoved}}
	presentAttendeeContacts(rows, host, map[uint]*models.User{adult.ID: adult}, nil, true)
	if rows[0].Email != nil || rows[0].Phone != nil {
		t.Fatal("removed registration authorized contact")
	}
}

func TestContactPresentationDoesNotUseUnrestrictedUserJSON(t *testing.T) {
	for _, path := range []string{"api_groups.go", "api_events.go", "api_event_access.go", "api_contact.go", "registration_subjects.go"} {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), ".toUserJSON(") {
			t.Errorf("%s uses unrestricted user presentation", path)
		}
	}
}
