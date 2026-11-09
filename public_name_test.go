package main

import (
	"testing"

	"fold/models"
)

// The SQL expression and User.PublicName must agree for every combination of
// display name and name.
func TestPublicNameSQLMatchesPublicName(t *testing.T) {
	app := testApp(t)
	for i, c := range []struct{ name, display string }{
		{"Pat Doe", "Pat"}, {"Pat Doe", ""}, {"", "Pat"}, {"", ""},
	} {
		u := newTestUser(t, app, "pn"+string(rune('a'+i)), "password1234", models.RoleMember)
		if err := app.DB.Model(u).Updates(map[string]any{"name": c.name, "display_name": c.display}).Error; err != nil {
			t.Fatal(err)
		}
		var got string
		if err := app.DB.Raw("SELECT "+models.PublicNameSQL("u")+" FROM users u WHERE u.id = ?", u.ID).Scan(&got).Error; err != nil {
			t.Fatal(err)
		}
		var unq string
		if err := app.DB.Raw("SELECT "+models.PublicNameSQL("")+" FROM users WHERE id = ?", u.ID).Scan(&unq).Error; err != nil {
			t.Fatal(err)
		}
		u.Name, u.DisplayName = c.name, c.display
		if want := u.PublicName(); got != want || unq != want {
			t.Fatalf("name=%q display=%q: SQL %q / %q, Go %q", c.name, c.display, got, unq, want)
		}
	}
}
