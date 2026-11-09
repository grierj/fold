package main

import (
	"errors"
	"testing"

	"gorm.io/gorm"

	"fold/models"
)

// sessionCurrent is the one decision every recheck makes: a row that can sign
// in and carries both of the session's epochs.
func TestSessionCurrentPredicate(t *testing.T) {
	hash := "x"
	live := func() *models.User {
		return &models.User{ID: 7, Status: models.UserActive, PasswordHash: &hash, SessionVersion: 2, LogoutVersion: 3}
	}
	session := &models.User{ID: 7, SessionVersion: 2, LogoutVersion: 3}
	cases := map[string]func(u *models.User){
		"current":        func(u *models.User) {},
		"session epoch":  func(u *models.User) { u.SessionVersion++ },
		"logout epoch":   func(u *models.User) { u.LogoutVersion++ },
		"disabled":       func(u *models.User) { u.Status = models.UserDisabled },
		"no password":    func(u *models.User) { u.PasswordHash = nil },
		"soft deleted":   func(u *models.User) { u.DeletedAt = gorm.DeletedAt{Valid: true} },
		"another person": func(u *models.User) { u.ID = 8 },
	}
	for name, mutate := range cases {
		u := live()
		mutate(u)
		if got, want := sessionCurrent(u, session), name == "current"; got != want {
			t.Errorf("%s: sessionCurrent = %v, want %v", name, got, want)
		}
	}
	if sessionCurrent(nil, session) || sessionCurrent(live(), nil) {
		t.Error("a nil row or session was current")
	}
}

// lockActorIn answers errSecuritySession for every way a session goes stale,
// whichever lock mode the site uses, and lockAdminIn also needs the role.
func TestLockActorInRefusesEveryStaleSession(t *testing.T) {
	app := testApp(t)
	member := newTestUser(t, app, "actor-member", "password1234", models.RoleMember)
	admin := newTestUser(t, app, "actor-admin", "password1234", models.RoleAdmin)
	for _, mode := range []actorLock{actorRead, actorShare, actorNoKeyUpdate} {
		for name, session := range map[string]*models.User{
			"current":       {ID: member.ID, SessionVersion: member.SessionVersion, LogoutVersion: member.LogoutVersion},
			"session epoch": {ID: member.ID, SessionVersion: member.SessionVersion + 1, LogoutVersion: member.LogoutVersion},
			"logout epoch":  {ID: member.ID, SessionVersion: member.SessionVersion, LogoutVersion: member.LogoutVersion + 1},
			"unknown":       {ID: 999999},
		} {
			err := app.DB.Transaction(func(tx *gorm.DB) error {
				got, err := lockActorIn(tx, session, mode)
				if err == nil && got.ID != member.ID {
					t.Errorf("mode %d %s: returned user %d", mode, name, got.ID)
				}
				return err
			})
			if stale := name != "current"; stale != errors.Is(err, errSecuritySession) || (!stale && err != nil) {
				t.Errorf("mode %d %s: err = %v", mode, name, err)
			}
		}
		err := app.DB.Transaction(func(tx *gorm.DB) error {
			_, err := lockAdminIn(tx, member, mode)
			return err
		})
		if !errors.Is(err, errSecuritySession) {
			t.Errorf("mode %d: a current non-admin was accepted: %v", mode, err)
		}
		err = app.DB.Transaction(func(tx *gorm.DB) error {
			_, err := lockAdminIn(tx, admin, mode)
			return err
		})
		if err != nil {
			t.Errorf("mode %d: a current admin was refused: %v", mode, err)
		}
	}
}

// endAllSessionsIn bumps the credential epoch, applies the caller's columns in
// the same update and revokes every outstanding capability.
func TestEndAllSessionsIn(t *testing.T) {
	app := testApp(t)
	u := newTestUser(t, app, "end-all", "password1234", models.RoleMember)
	token := models.AccountToken{Kind: models.TokenPasswordReset, UserID: u.ID, TokenHash: hashToken("end-all"), Payload: `{}`, ExpiresAt: u.CreatedAt.AddDate(1, 0, 0)}
	if err := app.DB.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Transaction(func(tx *gorm.DB) error {
		return endAllSessionsIn(tx, u, map[string]any{"name": "Renamed"})
	}); err != nil {
		t.Fatal(err)
	}
	var after models.User
	if err := app.DB.First(&after, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.SessionVersion != u.SessionVersion+1 || after.Name != "Renamed" || after.LogoutVersion != u.LogoutVersion {
		t.Errorf("epochs/columns after: session %d (was %d), logout %d (was %d), name %q", after.SessionVersion, u.SessionVersion, after.LogoutVersion, u.LogoutVersion, after.Name)
	}
	var left int64
	app.DB.Model(&models.AccountToken{}).Where("user_id = ? AND used_at IS NULL", u.ID).Count(&left)
	if left != 0 {
		t.Errorf("%d capabilities survived", left)
	}
}
