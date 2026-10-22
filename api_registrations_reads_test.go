package main

import (
	"net/http"
	"testing"

	"gorm.io/gorm"
)

// Read budgets for the hot registration paths (Whittler, PR 110). Master
// before slice 2: solo PUT 8, GET /events/:id 14. Household facts add a
// bounded, per-request set; this keeps them from growing per list.
func TestRegistrationReadBudget(t *testing.T) {
	app := testApp(t)
	admin, gid := openGroup(t, app, "Reads")
	eventID, date := seedCappedEvent(t, app, admin, gid, intPtr(5), true)
	_, c, spouse, child := householdFamily(t, app, admin, "readholder")
	batchAnswer(t, c, eventID, date, map[string]any{"status": "going", "people": []map[string]any{{"personId": spouse.ID, "status": "going"}, {"personId": child.ID, "status": "going"}}}, http.StatusOK)
	n := 0
	cb := func(*gorm.DB) { n++ }
	_ = app.DB.Callback().Query().Before("gorm:query").Register("zz:q", cb)
	_ = app.DB.Callback().Row().Before("gorm:row").Register("zz:r", cb)
	_ = app.DB.Callback().Raw().Before("gorm:raw").Register("zz:raw", cb)
	n = 0
	apiDo(t, admin, "PUT", "/api/v1/events/"+itoa(eventID)+"/registrations", map[string]any{"date": date, "status": "maybe"}, http.StatusOK)
	if n > 11 {
		t.Fatalf("solo PUT read %d times, budget 11", n)
	}
	n = 0
	apiDo(t, c, "GET", "/api/v1/events/"+itoa(eventID), nil, http.StatusOK)
	if n > 22 {
		t.Fatalf("holder GET read %d times, budget 22", n)
	}
	_ = newTestUser(t, app, "solo", "password1234", "member")
	solo := loginAPI(t, app, "solo", "password1234")
	apiDo(t, solo, "PUT", "/api/v1/events/"+itoa(eventID)+"/registrations", map[string]any{"date": date, "status": "going"}, http.StatusOK)
	n = 0
	apiDo(t, solo, "GET", "/api/v1/events/"+itoa(eventID), nil, http.StatusOK)
	if n > 20 {
		t.Fatalf("member GET read %d times, budget 20", n)
	}
	n = 0
	apiDo(t, admin, "GET", "/api/v1/events/"+itoa(eventID), nil, http.StatusOK)
	if n > 21 {
		t.Fatalf("admin GET read %d times, budget 21", n)
	}
}
