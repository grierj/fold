package main

import (
	"fold/models"
	"net/http"
	"net/http/httptest"
	"testing"
)

// M1.2: /dev/outbox is unreachable outside dev.
func TestDevOutboxUnreachableOutsideDev(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "alice", "password1234", models.RoleMember)

	r := app.setupRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/dev/outbox", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("dev: %d", w.Code)
	}

	prod := testApp(t)
	newTestUser(t, prod, "alice", "password1234", models.RoleMember)
	prod.Config.Env = "prod"
	rp := prod.setupRouter()
	w = httptest.NewRecorder()
	rp.ServeHTTP(w, httptest.NewRequest("GET", "/dev/outbox", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("prod: %d, want 404", w.Code)
	}
}
