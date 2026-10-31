package main

import (
	"net/http"
	"strings"
	"testing"

	"fold/internal/audit"
	"fold/models"
)

// Guardian copies (DEC-19): off by default, admins only, each change
// audited.
func TestGuardianCopiesSetting(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "root", "password1234", models.RoleAdmin)
	newTestUser(t, app, "sam", "password1234", models.RoleMember)
	root, sam := loginAPI(t, app, "root", "password1234"), loginAPI(t, app, "sam", "password1234")
	for _, method := range []string{"GET", "PUT"} {
		if w := sam.do(method, "/api/v1/admin/settings/guardian-copies", map[string]any{"on": true}, true); w.Code != http.StatusForbidden {
			t.Fatalf("member %s: %d", method, w.Code)
		}
	}
	if w := root.do("GET", "/api/v1/admin/settings/guardian-copies", nil, false); !strings.Contains(w.Body.String(), `"on":false`) {
		t.Fatalf("default: %s", w.Body.String())
	}
	if w := root.do("PUT", "/api/v1/admin/settings/guardian-copies", map[string]any{}, true); w.Code != http.StatusBadRequest {
		t.Fatalf("no value: %d", w.Code)
	}
	for _, on := range []bool{true, false} {
		w := root.do("PUT", "/api/v1/admin/settings/guardian-copies", map[string]any{"on": on}, true)
		if w.Code != http.StatusOK {
			t.Fatalf("save %v: %d %s", on, w.Code, w.Body.String())
		}
		if got := app.settingValue(settingGuardianCopies) == "true"; got != on {
			t.Fatalf("stored %v, want %v", got, on)
		}
	}
	if auditCount(t, app, audit.GuardianCopiesChanged) != 2 {
		t.Fatal("each change is audited")
	}
}

// Where replies go (DEC-130): one plain address or empty, admins only,
// audited; the pipeline reads it.
func TestReplyToSetting(t *testing.T) {
	app := testApp(t)
	newTestUser(t, app, "root", "password1234", models.RoleAdmin)
	newTestUser(t, app, "sam", "password1234", models.RoleMember)
	root, sam := loginAPI(t, app, "root", "password1234"), loginAPI(t, app, "sam", "password1234")
	if w := sam.do("PUT", "/api/v1/admin/settings/reply-to", map[string]any{"address": "office@example.org"}, true); w.Code != http.StatusForbidden {
		t.Fatalf("member: %d", w.Code)
	}
	for _, bad := range []string{"office", "a@b.org, c@d.org", "Office <office@example.org>", "office@example.org\r\nBcc: x@y.org"} {
		if w := root.do("PUT", "/api/v1/admin/settings/reply-to", map[string]any{"address": bad}, true); w.Code != http.StatusBadRequest ||
			!strings.Contains(w.Body.String(), "reply_to_invalid") {
			t.Fatalf("%q: %d %s", bad, w.Code, w.Body.String())
		}
	}
	w := root.do("PUT", "/api/v1/admin/settings/reply-to", map[string]any{"address": " office@example.org "}, true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"address":"office@example.org"`) || app.replyToAddress() != "office@example.org" {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(root.do("GET", "/api/v1/admin/settings/reply-to", nil, false).Body.String(), "office@example.org") {
		t.Fatal("read back")
	}
	if w := root.do("PUT", "/api/v1/admin/settings/reply-to", map[string]any{"address": ""}, true); w.Code != http.StatusOK || app.replyToAddress() != "" {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}
	if auditCount(t, app, audit.ReplyToChanged) != 2 {
		t.Fatal("each change is audited")
	}
}
