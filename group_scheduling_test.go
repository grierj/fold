package main

import (
	"testing"
	"time"

	"fold/models"
)

// The scheduling rule is read in Go (groupSchedulesIn) by writers and in SQL
// (schedulingGroupSQL) by the admin location match. They must agree for every
// kind of group.
func TestSchedulingGroupSQLMatchesPolicy(t *testing.T) {
	app := testApp(t)
	leader := newTestUser(t, app, "leader", "password1234", models.RoleMember)
	start := time.Now().UTC().Add(48 * time.Hour)

	deleted := newTestGroup(t, app, leader, "Deleted", models.GroupActive)
	if err := app.DB.Delete(&models.Group{}, deleted.ID).Error; err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		group *models.Group
		want  bool
	}{
		{"active", newTestGroup(t, app, leader, "Active", models.GroupActive), true},
		{"pending", newTestGroup(t, app, leader, "Pending", models.GroupPending), false},
		{"archived", newTestGroup(t, app, leader, "Archived", models.GroupArchived), false},
		{"active but deleted", deleted, false},
		{"church-wide", nil, true},
	}
	for _, c := range cases {
		var ev models.Event
		if c.group != nil {
			ev, _ = seedEvent(t, app, c.group, leader.ID, start)
		} else {
			ev = models.Event{
				Visibility: models.VisibilityPublic, StartsLocal: start.Format("2006-01-02T15:04"),
				DurationMinutes: 60, TZID: "UTC", CreatedBy: leader.ID,
			}
			if err := app.DB.Create(&ev).Error; err != nil {
				t.Fatalf("%s: seed event: %v", c.name, err)
			}
		}
		goForm, err := groupSchedulesIn(app.DB, ev.GroupID)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var sqlForm bool
		if err := app.DB.Raw("SELECT EXISTS(SELECT 1 FROM events e WHERE e.id = ? AND "+schedulingGroupSQL+")", ev.ID).Scan(&sqlForm).Error; err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if goForm != c.want || sqlForm != c.want {
			t.Errorf("%s: go %v, sql %v, want %v", c.name, goForm, sqlForm, c.want)
		}
	}
}

func TestGroupSchedulesInMissingGroupIsRefused(t *testing.T) {
	app := testApp(t)
	missing := uint(999999)
	ok, err := groupSchedulesIn(app.DB, &missing)
	if err != nil || ok {
		t.Fatalf("got %v, %v; want false, nil", ok, err)
	}
}
