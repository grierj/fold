package main

import (
	"testing"
	"time"

	"fold/internal/policy"
	"fold/models"
)

func TestCalendarScopeDecisionUsesEffectiveDateVisibility(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	opened := models.VisibilityPublic
	ev := &models.Event{Visibility: models.VisibilityInviteOnly, GroupID: uintp(1)}
	occ := &models.EventOccurrence{VisibilityOverride: &opened, StartsAt: now.AddDate(0, 0, 31)}
	if got := calendarScopeDecision(policy.CalendarScopeAll, true, ev, occ, policy.Rel{}, false, false, false, "", now); got.Include {
		t.Fatalf("an effectively public unanswered date outside the window was included: %+v", got)
	}
	if got := calendarScopeDecision(policy.CalendarScopeMine, true, ev, occ, policy.Rel{Member: true}, false, false, false, "", now); !got.Include || !got.OwnScope {
		t.Fatalf("membership did not exempt an own-group date: %+v", got)
	}
}

func TestLoadCalendarScopeBatchUsesConstantReads(t *testing.T) {
	app := testApp(t)
	viewer := newTestUser(t, app, "calendar batch viewer", "password1234", models.RoleMember)
	ids := make([]uint, 100)
	for i := range ids {
		ids[i] = uint(i + 1)
	}
	reads := countDBReads(t, app.DB)
	if _, err := app.loadCalendarScopeBatch(viewer, ids); err != nil {
		t.Fatal(err)
	}
	reads.Stop()
	if got := reads.Reads(); got != 2 {
		t.Fatalf("calendar scope batch used %d reads for 100 dates, want 2", got)
	}
}

func uintp(v uint) *uint { return &v }
