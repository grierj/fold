package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/jobs"
	"fold/internal/mail"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/internal/testdb"
	"fold/internal/txguard"
	"fold/models"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// Account erasure core (M7.4 R4-1-2): the capture step, the credential
// deletes, the owned throttle keys and the tombstone scrub, all through the
// real DELETE /admin/users/:id.

const erasurePW = "password1234"

// coreErasureScene is an acting admin and a second admin, so the target can be an
// admin too.
type coreErasureScene struct {
	app    *App
	admin  *models.User
	second *models.User
	ac     *apiClient
}

func newCoreErasureScene(t *testing.T) coreErasureScene {
	t.Helper()
	app := testApp(t)
	s := coreErasureScene{app: app}
	s.admin = newTestUser(t, app, "erase-admin", erasurePW, models.RoleAdmin)
	s.second = newTestUser(t, app, "erase-second", erasurePW, models.RoleAdmin)
	s.ac = loginAPI(t, app, "erase-admin", erasurePW)
	return s
}

// erase deletes id as the acting admin and fails the test on anything but 200.
func (s coreErasureScene) erase(t *testing.T, id uint) {
	t.Helper()
	if r := deleteAccount(s.ac, id, nil); r.code != http.StatusOK {
		t.Fatalf("delete %d: %d %s", id, r.code, r.body)
	}
}

// dumpRows is the rows of table matching where, every column, as one JSON
// string ordered by order, so "byte-identical" is one string comparison.
func dumpRows(t *testing.T, db *gorm.DB, table, order, where string, args ...any) string {
	t.Helper()
	var out string
	q := fmt.Sprintf(`SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY r.%s)::text, '[]') FROM %s r WHERE %s`, order, table, where)
	if err := db.Raw(q, args...).Scan(&out).Error; err != nil {
		t.Fatal(err)
	}
	return out
}

// credentialDump is every credential row a person holds.
func credentialDump(t *testing.T, db *gorm.DB, uid uint) string {
	t.Helper()
	return dumpRows(t, db, "account_tokens", "id", "user_id = ?", uid) +
		dumpRows(t, db, "calendar_tokens", "id", "user_id = ?", uid) +
		dumpRows(t, db, "push_subscriptions", "id", "user_id = ?", uid)
}

var erasureTokenKinds = []string{
	models.TokenEmailVerify, models.TokenEmailChangeCancel, models.TokenPasswordReset,
	models.TokenAccountSetup, models.TokenTeenConsent,
}

// seedErasureTokens gives uid a used and an unused token of every kind. The
// email_verify rows name newEmail as the pending new address.
func seedErasureTokens(t *testing.T, db *gorm.DB, uid uint, tag, newEmail string) {
	t.Helper()
	used := time.Now().Add(-time.Hour)
	for _, kind := range erasureTokenKinds {
		for _, isUsed := range []bool{false, true} {
			tok := models.AccountToken{
				Kind: kind, UserID: uid, Payload: "{}",
				TokenHash: hashToken(fmt.Sprintf("%s-%s-%t", tag, kind, isUsed)),
				ExpiresAt: time.Now().Add(time.Hour),
			}
			if kind == models.TokenEmailVerify {
				tok.Payload = fmt.Sprintf(`{"new_email":%q}`, newEmail)
			}
			if isUsed {
				tok.UsedAt = &used
			}
			if err := db.Create(&tok).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
}

func seedErasureCalendar(t *testing.T, db *gorm.DB, uid uint, raw string) {
	t.Helper()
	if err := db.Create(&models.CalendarToken{UserID: uid, TokenHash: hashToken(raw), Scope: models.CalendarFeedMine}).Error; err != nil {
		t.Fatal(err)
	}
}

func seedThrottleRows(t *testing.T, db *gorm.DB, keys []string) {
	t.Helper()
	for i, k := range keys {
		row := models.LoginThrottle{Key: k, Failures: i + 1, WindowStart: time.Now().Add(-time.Duration(i+1) * time.Minute)}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed throttle %q: %v", k, err)
		}
	}
}

func throttleCount(t *testing.T, db *gorm.DB, keys []string) int64 {
	t.Helper()
	return countRows(t, db, "login_throttles", "key IN ?", keys)
}

func targetedStrings(k throttleKey) []string {
	both := k.targeted()
	return throttleKeyStrings(both[:])
}

// Every personal column is scrubbed to its exact value, the identity columns
// stay, the session ends and the admin count drops only for an admin.
func TestAccountErasureScrubsTheTombstone(t *testing.T) {
	adultBirth := time.Date(1985, 4, 2, 0, 0, 0, 0, time.UTC)
	teenBirth := time.Now().UTC().AddDate(-15, 0, 0).Truncate(24 * time.Hour)
	cases := []struct {
		name   string
		build  func(u *models.User)
		login  bool
		isAdmn bool
	}{
		{"active adult", func(u *models.User) {}, true, false},
		{"admin target", func(u *models.User) { u.Role = models.RoleAdmin }, true, true},
		{"guest", func(u *models.User) { u.Status = models.UserGuest; u.PasswordHash = nil }, false, false},
		{"invited", func(u *models.User) { u.Status = models.UserInvited; u.PasswordHash = nil }, false, false},
		{"pending consent teen", func(u *models.User) {
			u.Status = models.UserPendingConsent
			u.AccountType = models.AccountTeen
			u.BirthDate = &teenBirth
			u.PasswordHash = nil
		}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newCoreErasureScene(t)
			u := models.User{
				Email: "scrub-target@example.com", Role: models.RoleMember, Status: models.UserActive,
				Name: "Pat Personal", DisplayName: "Patty", Phone: "555-0100", Timezone: "America/Boise",
				BirthDate: &adultBirth, HideContactFromLeaders: true, AvatarFocalX: 20, AvatarFocalY: 80,
				AvatarKey: "avatars/scrub/photo.png",
			}
			if err := u.SetPassword(erasurePW); err != nil {
				t.Fatal(err)
			}
			tc.build(&u)
			if err := s.app.DB.Create(&u).Error; err != nil {
				t.Fatal(err)
			}
			var old *apiClient
			if tc.login {
				old = loginAPI(t, s.app, "scrub-target", erasurePW)
			}
			var before models.User
			if err := s.app.DB.Unscoped().First(&before, u.ID).Error; err != nil {
				t.Fatal(err)
			}
			adminsBefore := s.app.adminCount()

			s.erase(t, u.ID)

			var got models.User
			if err := s.app.DB.Unscoped().First(&got, u.ID).Error; err != nil {
				t.Fatal(err)
			}
			if got.Email != fmt.Sprintf("deleted-%d@deleted.invalid", u.ID) {
				t.Errorf("email = %q", got.Email)
			}
			if got.Name != "" || got.DisplayName != "" || got.Phone != "" || got.Timezone != "" || got.AvatarKey != "" {
				t.Errorf("text columns not cleared: name=%q display=%q phone=%q tz=%q avatar=%q",
					got.Name, got.DisplayName, got.Phone, got.Timezone, got.AvatarKey)
			}
			if got.PasswordHash != nil || got.BirthDate != nil {
				t.Errorf("password hash or birth date survived: %v %v", got.PasswordHash, got.BirthDate)
			}
			if got.AvatarFocalX != 50 || got.AvatarFocalY != 50 || got.HideContactFromLeaders {
				t.Errorf("focal %v/%v hide %v", got.AvatarFocalX, got.AvatarFocalY, got.HideContactFromLeaders)
			}
			if got.Status != models.UserDisabled || !got.AdministrativelyDisabled {
				t.Errorf("status %q administratively_disabled %v", got.Status, got.AdministrativelyDisabled)
			}
			if !got.DeletedAt.Valid {
				t.Error("deleted_at is not set")
			}
			if got.SessionVersion != before.SessionVersion+1 {
				t.Errorf("session_version %d, want %d", got.SessionVersion, before.SessionVersion+1)
			}
			if got.ID != before.ID || got.Role != before.Role || got.AccountType != before.AccountType ||
				!got.CreatedAt.Equal(before.CreatedAt) || got.LogoutVersion != before.LogoutVersion {
				t.Errorf("an identity column changed: before %+v after %+v", before, got)
			}
			if tc.login {
				if w := old.do("GET", "/api/v1/auth/me", nil, false); w.Code != http.StatusUnauthorized {
					t.Errorf("the old session answers %d, want 401", w.Code)
				}
			}
			want := adminsBefore
			if tc.isAdmn {
				want--
			}
			if got := s.app.adminCount(); got != want {
				t.Errorf("adminCount = %d, want %d", got, want)
			}
		})
	}
}

// Everything that lets someone act as the person goes; another person's rows
// and a living guardian's token naming the person stay.
func TestAccountErasureDeletesEveryCredential(t *testing.T) {
	s := newCoreErasureScene(t)
	db := s.app.DB
	u := newTestUser(t, s.app, "cred-u", erasurePW, models.RoleMember)
	v := newTestUser(t, s.app, "cred-v", erasurePW, models.RoleMember)
	g := newTestUser(t, s.app, "cred-guardian", erasurePW, models.RoleMember)
	seedErasureTokens(t, db, u.ID, "u", "pending-u@example.com")
	seedErasureTokens(t, db, v.ID, "v", "pending-v@example.com")
	seedErasureCalendar(t, db, u.ID, "calendar-secret-u")
	seedErasureCalendar(t, db, v.ID, "calendar-secret-v")
	newPushDevice(t, s.app, u, "u-phone")
	newPushDevice(t, s.app, u, "u-tablet")
	newPushDevice(t, s.app, v, "v-phone")
	newPushDevice(t, s.app, v, "v-tablet")
	guardianTok := models.AccountToken{
		Kind: models.TokenTeenConsent, UserID: g.ID, TokenHash: hashToken("guardian-token"),
		Payload: fmt.Sprintf(`{"teenId":%d}`, u.ID), ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := db.Create(&guardianTok).Error; err != nil {
		t.Fatal(err)
	}
	anon := anonClient(t, s.app, "10.20.0.1")
	if w := fetchFeed(t, anon, "/cal/u/calendar-secret-u.ics"); w.Code != http.StatusOK {
		t.Fatalf("the calendar link before erasure answers %d", w.Code)
	}
	vBefore := credentialDump(t, db, v.ID)
	gBefore := credentialDump(t, db, g.ID)

	s.erase(t, u.ID)

	if got := credentialDump(t, db, u.ID); got != "[][][]" {
		t.Errorf("the erased person still has credentials: %s", got)
	}
	if got := credentialDump(t, db, v.ID); got != vBefore {
		t.Errorf("another person's credentials changed:\n%s\n%s", vBefore, got)
	}
	if got := credentialDump(t, db, g.ID); got != gBefore {
		t.Errorf("the living guardian's token changed:\n%s\n%s", gBefore, got)
	}
	if w := fetchFeed(t, anon, "/cal/u/calendar-secret-u.ics"); w.Code != http.StatusNotFound {
		t.Errorf("the old calendar link answers %d, want 404", w.Code)
	}
	if n := countRows(t, db, "jobs", "kind = ?", kindPushDevicesOff); n != 0 {
		t.Errorf("%d push.devices_off jobs were queued", n)
	}
}

// Exactly the keys ownedThrottleKeys lists go; shared keys, other people's
// keys and the O-21 residue stay untouched.
func TestAccountErasureDeletesOnlyOwnedThrottleKeys(t *testing.T) {
	s := newCoreErasureScene(t)
	db := s.app.DB
	const addr = "erase.me@x.test"
	const pastAddr = "erase.old@x.test"
	const pendingAddr = "erase.pending@x.test"
	u := newTestUser(t, s.app, addr, erasurePW, models.RoleMember)
	v := newTestUser(t, s.app, "throttle-v", erasurePW, models.RoleMember)
	seedErasureTokens(t, db, u.ID, "u", pendingAddr)

	owned := ownedThrottleKeys(u.ID, addr)
	lockedUntil := time.Now().Add(time.Hour)
	seedThrottleRows(t, db, owned)
	if err := db.Model(&models.LoginThrottle{}).Where("key = ?", string(loginEmailKey(addr))).
		Updates(map[string]any{"locked_until": lockedUntil, "failures": 0}).Error; err != nil {
		t.Fatal(err)
	}

	survivors := []string{
		// shared
		string(loginIPKey("10.0.0.9")), string(pwresetIPKey("10.0.0.9")), string(accountRequestIPKey("10.0.0.9")),
		string(guestIPKey("10.0.0.9")), string(guardianClaimIPKey("10.0.0.9")),
		string(accountRequestSiteKey()), string(guestSiteKey()),
		// other people
		string(loginEmailKey(addr + ".example")), string(loginUserKey(u.ID * 10)),
		string(managerDateAddKey(v.ID)), string(peopleSearchUserKey(v.ID)),
		// residue (O-21): the pending new address and a past address
		string(loginEmailKey(pastAddr)), string(pwresetAddrKey(pastAddr) + ":h"),
	}
	survivors = append(survivors, targetedStrings(guardianClaimAddrKey("other.b2@x.test"))...)
	survivors = append(survivors, targetedStrings(pwresetAddrKey(addr+".example"))...)
	survivors = append(survivors, targetedStrings(emailChangeUserKey(v.ID))...)
	survivors = append(survivors, targetedStrings(emailChangeAddrKey(pendingAddr))...)
	seedThrottleRows(t, db, survivors)
	before := dumpRows(t, db, "login_throttles", "key", "key IN ?", survivors)

	s.erase(t, u.ID)

	if n := throttleCount(t, db, owned); n != 0 {
		t.Errorf("%d owned throttle rows survived", n)
	}
	if after := dumpRows(t, db, "login_throttles", "key", "key IN ?", survivors); after != before {
		t.Errorf("rows that must survive changed:\n%s\n%s", before, after)
	}
	// The lockout on the address no longer blocks a sign-in attempt there.
	if code, body := tryLogin(t, s.app, "10.9.9.9", addr, "wrong password"); code != http.StatusUnauthorized {
		t.Errorf("sign-in at the freed address answers %d %s, want 401", code, body)
	}
}

// The address is the one the account holds once the gate and the row lock are
// taken, never one it held before.
func TestAccountErasureCapturesTheAddressAfterTheLock(t *testing.T) {
	s := newCoreErasureScene(t)
	db := s.app.DB
	const a0, a1 = "capture.old@x.test", "capture.new@x.test"
	u := newTestUser(t, s.app, a0, erasurePW, models.RoleMember)
	for _, a := range []string{a0, a1} {
		seedThrottleRows(t, db, append([]string{string(loginEmailKey(a))}, targetedStrings(pwresetAddrKey(a))...))
	}
	release, gPID := holdAdvisory(t, db, contactLockID)
	del := bgDo(s.ac, "DELETE", "/api/v1/admin/users/"+itoa(u.ID), nil)
	waiterOn(t, db, gPID)
	if err := db.Exec("UPDATE users SET email = ? WHERE id = ?", a1, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	release()
	if r := awaitResult(t, del); r.code != http.StatusOK {
		t.Fatalf("delete: %d %s", r.code, r.body)
	}
	a1Keys := append([]string{string(loginEmailKey(a1))}, targetedStrings(pwresetAddrKey(a1))...)
	a0Keys := append([]string{string(loginEmailKey(a0))}, targetedStrings(pwresetAddrKey(a0))...)
	if n := throttleCount(t, db, a1Keys); n != 0 {
		t.Errorf("%d keys for the held address survived", n)
	}
	if n := throttleCount(t, db, a0Keys); n != int64(len(a0Keys)) {
		t.Errorf("keys for a past address: %d left, want %d", n, len(a0Keys))
	}
}

// T35: erasure never creates, clears or changes a suppression.
func TestAccountErasureLeavesSuppressionsAlone(t *testing.T) {
	s := newCoreErasureScene(t)
	db := s.app.DB
	const addr = "suppressed.person@x.test"
	u := newTestUser(t, s.app, addr, erasurePW, models.RoleMember)
	hash := mail.StoredAddressHash(addr)
	cleared := time.Now().Add(-time.Hour)
	rows := []models.Suppression{
		{Channel: "email", Address: addr, AddressHash: &hash, Reason: models.SuppressHardBounce},
		{Channel: "email", Address: addr, AddressHash: &hash, Reason: models.SuppressComplaint, ClearedAt: &cleared, ClearedBy: &s.admin.ID},
		{Channel: "email", Address: addr, Reason: models.SuppressManual},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	before := dumpRows(t, db, "suppressions", "id", "true")

	s.erase(t, u.ID)

	if after := dumpRows(t, db, "suppressions", "id", "true"); after != before {
		t.Errorf("suppressions changed:\n%s\n%s", before, after)
	}
}

// F-8: every membership of the person goes, whatever its status; other
// people's rows stay.
func TestAccountErasureHardDeletesMemberships(t *testing.T) {
	s := newCoreErasureScene(t)
	db := s.app.DB
	u := newTestUser(t, s.app, "member-u", erasurePW, models.RoleMember)
	l1 := newTestUser(t, s.app, "lead-one", erasurePW, models.RoleMember)
	l2 := newTestUser(t, s.app, "lead-two", erasurePW, models.RoleMember)
	g1 := newTestGroup(t, s.app, l1, "Plain", models.GroupActive)
	g2 := newTestGroup(t, s.app, l2, "Co-led", models.GroupActive)
	g3 := newTestGroup(t, s.app, l1, "Removed", models.GroupActive)
	g4 := newTestGroup(t, s.app, l1, "Left", models.GroupActive)
	addMember(t, s.app, u, g1, models.MembershipMember)
	addMember(t, s.app, u, g2, models.MembershipLeader)
	for gid, status := range map[uint]string{g3.ID: "removed", g4.ID: "left"} {
		row := models.Membership{UserID: u.ID, GroupID: gid, Role: models.MembershipMember, Status: status, JoinedAt: time.Now().UTC()}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if n := countRows(t, db, "memberships", "user_id = ?", u.ID); n != 4 {
		t.Fatalf("seeded %d memberships, want 4", n)
	}
	// Another person's live departure grant names that person's own
	// membership (the registration trigger requires it), in a group the
	// erased person also belongs to. The acting admin is the grant's actor.
	o := newTestUser(t, s.app, "member-o", erasurePW, models.RoleMember)
	addMember(t, s.app, o, g1, models.MembershipMember)
	var oMembership models.Membership
	if err := db.Where("user_id = ? AND group_id = ?", o.ID, g1.ID).First(&oMembership).Error; err != nil {
		t.Fatal(err)
	}
	_, occ := seedEvent(t, s.app, g1, l1.ID, time.Now().UTC().Add(48*time.Hour))
	oid, actor, granted := o.ID, s.second.ID, time.Now().UTC().Add(-time.Minute)
	grant := models.Registration{
		OccurrenceID: occ.ID, UserID: &oid, Status: models.RegGoing,
		AttendanceMode: models.AttendanceInPerson, Source: models.RegSourceWeb,
		AccessOrigin: policy.OccurrenceOriginDepartureRemoval, AccessDepartureMembershipID: &oMembership.ID,
		AccessDepartureActorID: &actor, AccessGrantedAt: &granted,
	}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, "registrations", "id = ? AND access_departure_membership_id = ?", grant.ID, oMembership.ID); n != 1 {
		t.Fatalf("seeded departure grant not stored with membership %d", oMembership.ID)
	}
	var uMembership models.Membership
	if err := db.Where("user_id = ? AND group_id = ?", u.ID, g1.ID).First(&uMembership).Error; err != nil {
		t.Fatal(err)
	}
	uid := u.ID
	uGrant := models.Registration{
		OccurrenceID: occ.ID, UserID: &uid, Status: models.RegGoing,
		AttendanceMode: models.AttendanceInPerson, Source: models.RegSourceWeb,
		AccessOrigin: policy.OccurrenceOriginDepartureRemoval, AccessDepartureMembershipID: &uMembership.ID,
		AccessDepartureActorID: &actor, AccessGrantedAt: &granted,
	}
	if err := db.Create(&uGrant).Error; err != nil {
		t.Fatal(err)
	}
	others := dumpRows(t, db, "memberships", "id", "user_id <> ?", u.ID)
	registrations := dumpRows(t, db, "registrations", "id", "id = ?", grant.ID)

	s.erase(t, u.ID)

	if n := countRows(t, db, "memberships", "user_id = ?", u.ID); n != 0 {
		t.Errorf("%d memberships survived", n)
	}
	if got := dumpRows(t, db, "memberships", "id", "user_id <> ?", u.ID); got != others {
		t.Errorf("other people's memberships changed:\n%s\n%s", others, got)
	}
	// Includes registrations.access_departure_membership_id, seeded above.
	if got := dumpRows(t, db, "registrations", "id", "id = ?", grant.ID); got != registrations {
		t.Errorf("registrations changed:\n%s\n%s", registrations, got)
	}
	if n := countRows(t, db, "registrations", "id = ? AND access_departure_membership_id = ?", grant.ID, oMembership.ID); n != 1 {
		t.Errorf("departure grant lost its membership id %d", oMembership.ID)
	}
	if n := countRows(t, db, "memberships", "id = ?", oMembership.ID); n != 1 {
		t.Errorf("the membership a departure grant names is gone")
	}
	if n := countRows(t, db, "registrations", "id = ? AND access_origin = ? AND access_departure_membership_id = ? AND access_departure_actor_id = ? AND access_granted_at = ?", uGrant.ID, policy.OccurrenceOriginDepartureRemoval, uMembership.ID, actor, granted); n != 1 {
		t.Error("erased person's departure grant lost its provenance")
	}
}

// erasureSnapshot is everything the erasure writes, as one string.
func erasureSnapshot(t *testing.T, db *gorm.DB, uid uint, throttleKeys []string) string {
	t.Helper()
	return dumpRows(t, db, "users", "id", "id = ?", uid) +
		credentialDump(t, db, uid) +
		dumpRows(t, db, "login_throttles", "key", "key IN ?", throttleKeys) +
		dumpRows(t, db, "memberships", "id", "user_id = ?", uid) +
		dumpRows(t, db, "notifications", "id", "user_id = ?", uid) +
		dumpRows(t, db, "audit_logs", "id", "true") +
		dumpRows(t, db, "jobs", "id", "kind = ?", kindAvatarCleanup)
}

func TestAccountErasureRollsBackWhole(t *testing.T) {
	s := newCoreErasureScene(t)
	db := s.app.DB
	const addr = "rollback.me@x.test"
	u := newTestUser(t, s.app, addr, erasurePW, models.RoleMember)
	l := newTestUser(t, s.app, "rollback-lead", erasurePW, models.RoleMember)
	g := newTestGroup(t, s.app, l, "Rollback", models.GroupActive)
	addMember(t, s.app, u, g, models.MembershipMember)
	seedErasureTokens(t, db, u.ID, "u", "rollback.pending@x.test")
	seedErasureCalendar(t, db, u.ID, "rollback-calendar")
	newPushDevice(t, s.app, u, "rollback-phone")
	newPushDevice(t, s.app, u, "rollback-tablet")
	owned := ownedThrottleKeys(u.ID, addr)
	seedThrottleRows(t, db, owned)
	before := erasureSnapshot(t, db, u.ID, owned)

	failJobEnqueue(t, db, kindAvatarCleanup)
	r := deleteAccount(s.ac, u.ID, nil)
	if r.code != http.StatusInternalServerError {
		t.Fatalf("delete answered %d %s, want 500", r.code, r.body)
	}
	if after := erasureSnapshot(t, db, u.ID, owned); after != before {
		t.Errorf("a failed erasure left a change:\n%s\n%s", before, after)
	}
}

// Every Unscoped reader of a person's name shows "Member" after erasure
// (inventory 3, rule 5). The seeded contact_requests rows are only read here.
func TestErasedPersonRendersAsMember(t *testing.T) {
	f := newOccurrenceReadFixture(t)
	db := f.app.DB
	birth := time.Now().UTC().AddDate(-15, 0, 0).Truncate(24 * time.Hour)
	u := models.User{
		Email: "erased.teen@example.com", Name: "Erased Teen Name", DisplayName: "Zyx Display",
		Role: models.RoleMember, Status: models.UserActive, AccountType: models.AccountTeen, BirthDate: &birth,
	}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	contact := models.ContactRequest{
		Purpose: policy.RequestContact, RequesterID: u.ID, SubjectUserID: f.other.ID,
		ContextType: policy.ContactGroup, ContextID: f.group, Fields: policy.ContactBoth, Reason: "reason",
		NeedsAdmin: true, SubjectDecision: policy.ContactPending, AdminDecision: policy.ContactPending,
		Status: policy.ContactPending, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}
	add := models.ContactRequest{
		Purpose: policy.RequestGroupAdd, RequesterID: u.ID, SubjectUserID: f.other.ID,
		ContextType: policy.ContactGroup, ContextID: f.group,
		SubjectDecision: policy.ContactPending, AdminDecision: policy.ContactNotNeeded,
		Status: policy.ContactPending, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}
	for _, r := range []*models.ContactRequest{&contact, &add} {
		if err := db.Create(r).Error; err != nil {
			t.Fatal(err)
		}
	}
	link := models.GuardianLink{GuardianUserID: f.other.ID, TeenUserID: &u.ID, Status: models.GuardianPending}
	if err := db.Create(&link).Error; err != nil {
		t.Fatal(err)
	}

	if r := deleteAccount(f.admin, u.ID, nil); r.code != http.StatusOK {
		t.Fatalf("delete: %d %s", r.code, r.body)
	}

	if got := publicNamesIn(db, []uint{u.ID}).name(u.ID); got != "Member" {
		t.Errorf("publicNamesIn = %q", got)
	}
	if got := nameIn(db, u.ID); got != "Member" {
		t.Errorf("nameIn = %q", got)
	}
	op := &contactOperation{actor: f.issuer, now: now, zone: time.UTC}
	var rc, ra models.ContactRequest
	if err := db.First(&rc, contact.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&ra, add.ID).Error; err != nil {
		t.Fatal(err)
	}
	reads, err := loadContactPageReads(db, op, []models.ContactRequest{rc}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := reads.name(u.ID); got != "Member" {
		t.Errorf("loadContactPageReads name = %q", got)
	}
	rows, err := f.app.presentAddRequests(db, op, []models.ContactRequest{ra})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "Erased Teen Name") || strings.Contains(string(b), "Zyx Display") {
		t.Errorf("an add request still shows the old name: %s", b)
	}
	w := f.admin.do("GET", "/api/v1/admin/guardian-links?status=all", nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("guardian links: %d %s", w.Code, w.Body.String())
	}
	var list struct {
		GuardianLinks []guardianLinkJSON `json:"guardianLinks"`
	}
	decodeJSON(t, w, &list)
	found := false
	for _, l := range list.GuardianLinks {
		if l.TeenUserID != nil && *l.TeenUserID == u.ID {
			found = true
			if l.PersonName != "Member" || l.BirthDate != "" {
				t.Errorf("guardian link shows personName %q birthDate %q", l.PersonName, l.BirthDate)
			}
		}
	}
	if !found {
		t.Error("the guardian link is missing from the list")
	}
}

// Inventory 9.4, scoped by timing: this proves only preparations that start
// after the erasure commits. A preparation that committed before the erasure
// (or, for the existing-account hint and `exists`, read the user before it
// committed) may still send to the captured address, or to the pending new
// address for email verification, with its capability's token deleted; the
// hint and `exists` mail carry no capability. It does not cover the classes
// that stay live until their owners land: invitation, guest verification and
// account-request received, rejected and conversion_approved (R4-2a), and
// guardian-consent mail (R4-3).
func TestQueuedAccountMailForErasedAccountSendsNothing(t *testing.T) {
	s := newCoreErasureScene(t)
	db := s.app.DB
	// u is an active account: password reset, the existing-account hint,
	// email verification and the `exists` case are deliverable only for it.
	u := newTestUser(t, s.app, "queued-u", erasurePW, models.RoleMember)
	seedErasureTokens(t, db, u.ID, "u", "queued.pending@example.com")
	var verify models.AccountToken
	if err := db.Where("user_id = ? AND kind = ? AND used_at IS NULL", u.ID, models.TokenEmailVerify).First(&verify).Error; err != nil {
		t.Fatal(err)
	}
	existsReq := models.AccountRequest{
		Name: "Queued Requester", Email: "queued-request@example.com", Source: models.RequestSelfSignup,
		Status: models.RequestApproved, ApprovedUserID: &u.ID, ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	if err := db.Create(&existsReq).Error; err != nil {
		t.Fatal(err)
	}
	// The approved setup mail needs a passwordless invited account, which
	// u is not (preparation already sends nothing for a user with a password).
	w := newTestUser(t, s.app, "queued-w", erasurePW, models.RoleMember)
	if err := db.Model(&models.User{}).Where("id = ?", w.ID).Updates(map[string]any{"password_hash": nil, "status": models.UserInvited}).Error; err != nil {
		t.Fatal(err)
	}
	approvedReq := models.AccountRequest{
		Name: "Queued Invitee", Email: "queued-invitee@example.com", Source: models.RequestSelfSignup,
		Status: models.RequestApproved, ApprovedUserID: &w.ID, ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	if err := db.Create(&approvedReq).Error; err != nil {
		t.Fatal(err)
	}

	type preparation struct {
		name string
		run  func(tx *gorm.DB) (mail.Message, bool, error)
	}
	preparations := []preparation{
		{"password reset", func(tx *gorm.DB) (mail.Message, bool, error) { return s.app.preparePasswordReset(tx, u.ID) }},
		{"existing-account hint", func(tx *gorm.DB) (mail.Message, bool, error) { return s.app.prepareExistingAccountHint(tx, u.ID) }},
		{"email verification", func(tx *gorm.DB) (mail.Message, bool, error) { return s.app.prepareEmailVerification(tx, verify.ID) }},
		{"account request exists", func(tx *gorm.DB) (mail.Message, bool, error) {
			return s.app.prepareAccountRequestMail(tx, existsReq.ID, requestMailExists)
		}},
		{"account request approved", func(tx *gorm.DB) (mail.Message, bool, error) {
			return s.app.prepareAccountRequestMail(tx, approvedReq.ID, requestMailApproved)
		}},
	}
	// Each mail is deliverable before the erasure; the transaction is rolled
	// back so nothing is minted or rotated.
	for _, p := range preparations {
		tx := db.Begin()
		_, eligible, err := p.run(tx)
		tx.Rollback()
		if err != nil || !eligible {
			t.Fatalf("%s must be eligible before erasure: eligible=%v err=%v", p.name, eligible, err)
		}
	}
	// The ID-only jobs are queued before the DELETE and nothing drains them
	// until both erasures have committed. The API client drains notice and
	// mail jobs after every request unless told to hold them; without the
	// hold, the first DELETE ran all five jobs while the passwordless account
	// w still existed, and its approved setup mail was rightly sent.
	s.ac.holdNotices = true
	queued := []struct {
		kind    string
		payload any
	}{
		{kindPasswordResetMail, userMailJob{UserID: u.ID}},
		{kindAccountExistsMail, userMailJob{UserID: u.ID}},
		{kindEmailVerifyMail, accountTokenMailJob{AccountTokenID: verify.ID}},
		{kindAccountRequestMail, requestMailJob{RequestID: existsReq.ID, Case: requestMailExists}},
		{kindAccountRequestMail, requestMailJob{RequestID: approvedReq.ID, Case: requestMailApproved}},
	}
	for _, job := range queued {
		if _, err := jobs.Enqueue(db, job.kind, job.payload, time.Now(), "", directMailAttempts); err != nil {
			t.Fatal(err)
		}
	}
	// The sender sees every message the drain sends, so the drain must hold
	// exactly these five jobs: any other notice job would make the count
	// below mean something else.
	var drainable []string
	for _, k := range append(s.app.notifyKinds(), s.app.mailKinds()...) {
		drainable = append(drainable, k.Name)
	}
	var sent []string
	s.app.Mailer = directMailSenderFunc(func(_ context.Context, m mail.Message) error { sent = append(sent, m.To); return nil })
	tokensBefore := totalRows(t, db, "account_tokens")

	s.erase(t, u.ID)
	s.erase(t, w.ID)
	if n := countRows(t, db, "jobs", "kind IN ?", drainable); n != int64(len(queued)) {
		t.Fatalf("%d notice or mail jobs queued before the drain, want the %d queued here", n, len(queued))
	}
	if n := countRows(t, db, "jobs", "kind IN ? AND status = ?", drainable, models.JobQueued); n != int64(len(queued)) {
		t.Fatalf("%d of the queued jobs ran before the drain, want none", len(queued)-int(n))
	}
	drainNotices(t, s.app)
	// Every queued job ran to completion without an error, and none sent.
	if n := countRows(t, db, "jobs", "kind IN ? AND status = ? AND last_error = ''", drainable, models.JobDone); n != int64(len(queued)) {
		t.Errorf("%d of %d queued account mail jobs finished cleanly", n, len(queued))
	}
	if len(sent) != 0 {
		t.Errorf("queued account mail sent %d messages after erasure, to %v", len(sent), sent)
	}

	for _, p := range preparations {
		var ok bool
		err := db.Transaction(func(tx *gorm.DB) error {
			var err error
			_, ok, err = p.run(tx)
			return err
		})
		if err != nil || ok {
			t.Errorf("%s: ok=%v err=%v, want nothing to send and no error", p.name, ok, err)
		}
	}
	if n := countRows(t, db, "account_tokens", "user_id IN ?", []uint{u.ID, w.ID}); n != 0 {
		t.Errorf("a preparation minted %d tokens for the erased accounts", n)
	}
	if total := totalRows(t, db, "account_tokens"); total >= tokensBefore {
		t.Errorf("account_tokens rows: %d before erasure, %d after", tokensBefore, total)
	}
}

// F-4, classification only. An erased guest's address is rewritten, so the
// Unscoped email lookup in invitation acceptance (api_invitations.go, the
// guest-upgrade branch) no longer finds the tombstone: accepting an invitation
// to the old address creates a new account and leaves the tombstone row
// byte-identical. This does not fix the whole-row Save in the guest-upgrade
// path, which R19.2 still owns; it only shows that erasure takes the tombstone
// out of that path's reach.
func TestErasedGuestIsNotRevivedByInvitationAcceptance(t *testing.T) {
	s := newCoreErasureScene(t)
	db := s.app.DB
	const address = "former-guest@example.com"
	guest := models.User{Email: address, Name: "Former Guest", Role: models.RoleMember, Status: models.UserGuest}
	if err := db.Create(&guest).Error; err != nil {
		t.Fatal(err)
	}
	s.erase(t, guest.ID)
	tombstone := dumpRows(t, db, "users", "id", "id = ?", guest.ID)

	const raw = "invitation-to-an-erased-guest"
	inv := models.Invitation{Email: address, Name: "Fresh Start", InvitedBy: s.admin.ID,
		TokenHash: hashToken(raw), ExpiresAt: time.Now().UTC().Add(time.Hour)}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}
	anon := anonClient(t, s.app, "10.0.0.77")
	w := anon.do("POST", "/api/v1/invitations/accept", map[string]string{
		"token": raw, "name": "Fresh Start", "password": erasurePW, "ageBand": "adult",
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}

	var accounts []models.User
	if err := db.Unscoped().Where("email = ?", address).Find(&accounts).Error; err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].ID == guest.ID || accounts[0].Status != models.UserActive || accounts[0].DeletedAt.Valid {
		t.Fatalf("accepting the invitation did not create one new active account for %q: %+v", address, accounts)
	}
	if after := dumpRows(t, db, "users", "id", "id = ?", guest.ID); after != tombstone {
		t.Fatalf("the tombstone changed when its old address was invited:\nbefore %s\nafter  %s", tombstone, after)
	}
}

// erasureCaptured runs captureErasureSetsIn for u inside a transaction that is
// always rolled back, so a capture is shown to be reads only.
func erasureCaptured(t *testing.T, db *gorm.DB, u *models.User) erasureSubject {
	t.Helper()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	s := captureErasureSubject(u)
	if err := captureErasureSetsIn(tx, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func erasureMustCreate(t *testing.T, db *gorm.DB, row any) {
	t.Helper()
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("seed %T: %v", row, err)
	}
}

var erasureInvitationSeq int

func erasureInvitation(t *testing.T, db *gorm.DB, by uint, email string, accepted bool, personID *uint) uint {
	t.Helper()
	erasureInvitationSeq++
	inv := models.Invitation{Email: email, InvitedBy: by, Role: "member", AccountType: "adult",
		TokenHash: fmt.Sprintf("capture-hash-%d", erasureInvitationSeq), ExpiresAt: time.Now().Add(time.Hour),
		HouseholdPersonID: personID}
	if accepted {
		now := time.Now()
		inv.AcceptedAt = &now
	}
	erasureMustCreate(t, db, &inv)
	return inv.ID
}

func erasureAcceptedAudit(t *testing.T, db *gorm.DB, invID uint, meta string) {
	t.Helper()
	erasureMustCreate(t, db, &models.AuditLog{Action: "invitation.accepted", TargetType: "invitation", TargetID: invID, MetaJSON: meta})
}

func erasureRequest(t *testing.T, db *gorm.DB, email, source, status string, approved *uint, personID, invID *uint) uint {
	t.Helper()
	r := models.AccountRequest{Name: "Someone", Email: email, Source: source, Status: status,
		ApprovedUserID: approved, HouseholdPersonID: personID, InvitationID: invID, ExpiresAt: time.Now().Add(time.Hour)}
	erasureMustCreate(t, db, &r)
	return r.ID
}

// R4-2a-1: REQ, PERS and INV (inventory 3.1), the T17 cases (1)-(6) and the
// request cases.
func TestErasureCapturesRequestInvitationAndPersonSets(t *testing.T) {
	app := testApp(t)
	db := app.DB
	const a0, a = "capset.old@x.test", "capset.now@x.test"
	u := newTestUser(t, app, a, erasurePW, models.RoleMember)
	other := newTestUser(t, app, a0, erasurePW, models.RoleMember)
	inviter := newTestUser(t, app, "capset-inviter@x.test", erasurePW, models.RoleAdmin)
	uid, oid := u.ID, other.ID

	// (1) accepted at A0, the address then changed to A: found through (a).
	i1 := erasureInvitation(t, db, inviter.ID, a0, true, nil)
	erasureAcceptedAudit(t, db, i1, fmt.Sprintf(`{"userId":%d}`, uid))
	// (2) another account later took A0 and accepted its own invitation there.
	i2 := erasureInvitation(t, db, inviter.ID, a0, true, nil)
	erasureAcceptedAudit(t, db, i2, fmt.Sprintf(`{"userId":%d}`, oid))
	// (3) accepted with no userId in its audit row, an accepted row at A with no
	// audit at all, and an unaccepted row to the past address: none match.
	i3 := erasureInvitation(t, db, inviter.ID, a, true, nil)
	erasureAcceptedAudit(t, db, i3, `{}`)
	erasureInvitation(t, db, inviter.ID, a, true, nil)
	oldOpen := erasureInvitation(t, db, inviter.ID, a0, false, nil)
	// (4) an open invitation to the current address: found through (d).
	i4 := erasureInvitation(t, db, inviter.ID, a, false, nil)
	// (6) one the person sent, and one to another address: excluded.
	erasureInvitation(t, db, uid, "capset.third@x.test", false, nil)
	erasureInvitation(t, db, inviter.ID, "capset.third@x.test", false, nil)

	// REQ: approved for U at another address, unlinked at A; excluded: approved
	// for another account carrying A, unlinked at another address.
	rLinked := erasureRequest(t, db, "capset.elsewhere@x.test", "self_signup", "approved", &uid, nil, nil)
	rUnlinked := erasureRequest(t, db, a, "self_signup", "pending", nil, nil, nil)
	erasureRequest(t, db, a, "self_signup", "approved", &oid, nil, nil)
	erasureRequest(t, db, "capset.unrelated@x.test", "self_signup", "pending", nil, nil, nil)

	s := erasureCaptured(t, db, u)
	if !sameIDs(s.Requests, rLinked, rUnlinked) {
		t.Errorf("REQ = %v, want [%d %d]", s.Requests, rLinked, rUnlinked)
	}
	if !sameIDs(s.Invitations, i1, i4) {
		t.Errorf("INV = %v, want [%d %d]", s.Invitations, i1, i4)
	}
	if len(s.People) != 0 {
		t.Errorf("PERS = %v, want none", s.People)
	}
	if o := erasureCaptured(t, db, other); !sameIDs(o.Invitations, i2, oldOpen) {
		t.Errorf("INV of the later holder = %v, want [%d %d] (excluding %d)", o.Invitations, i2, oldOpen, i1)
	}

	// (5a) a completed conversion: the person points at the user, the request
	// carries the invitation, a second invitation names the person.
	conv := newTestUser(t, app, "capset-converted@x.test", erasurePW, models.RoleMember)
	hh := models.Household{Name: "Capture family", CreatedBy: inviter.ID}
	erasureMustCreate(t, db, &hh)
	p1 := models.HouseholdPerson{HouseholdID: hh.ID, FirstName: "Done", Kind: models.HouseholdAdult, CreatedBy: inviter.ID, ConvertedUserID: &conv.ID}
	erasureMustCreate(t, db, &p1)
	iB := erasureInvitation(t, db, inviter.ID, "capset.conv1@x.test", true, nil)
	iC := erasureInvitation(t, db, inviter.ID, "capset.conv2@x.test", true, &p1.ID)
	rConv := erasureRequest(t, db, "capset.conv1@x.test", "household_conversion", "approved", &conv.ID, &p1.ID, &iB)
	cs := erasureCaptured(t, db, conv)
	if !sameIDs(cs.People, p1.ID) || !sameIDs(cs.Invitations, iB, iC) || !sameIDs(cs.Requests, rConv) {
		t.Errorf("completed conversion: PERS %v INV %v REQ %v, want [%d] [%d %d] [%d]", cs.People, cs.Invitations, cs.Requests, p1.ID, iB, iC, rConv)
	}

	// (5b) a staged teen conversion: converted_user_id still NULL, the person
	// soft-deleted; only the approved request links the two.
	teen := newTestUser(t, app, "capset-teen@x.test", erasurePW, models.RoleMember)
	p2 := models.HouseholdPerson{HouseholdID: hh.ID, FirstName: "Staged", Kind: models.HouseholdTeen, CreatedBy: inviter.ID}
	erasureMustCreate(t, db, &p2)
	if err := db.Delete(&p2).Error; err != nil {
		t.Fatal(err)
	}
	iD := erasureInvitation(t, db, inviter.ID, "capset.teen@x.test", true, nil)
	erasureRequest(t, db, "capset.teen@x.test", "household_conversion", "approved", &teen.ID, &p2.ID, &iD)
	ts := erasureCaptured(t, db, teen)
	if !sameIDs(ts.People, p2.ID) || !sameIDs(ts.Invitations, iD) {
		t.Errorf("staged conversion: PERS %v INV %v, want [%d] [%d]", ts.People, ts.Invitations, p2.ID, iD)
	}
	// A teen with a pending request for a person is not that person.
	pend := newTestUser(t, app, "capset-pending@x.test", erasurePW, models.RoleMember)
	p3 := models.HouseholdPerson{HouseholdID: hh.ID, FirstName: "Pending", Kind: models.HouseholdTeen, CreatedBy: inviter.ID}
	erasureMustCreate(t, db, &p3)
	erasureRequest(t, db, "capset.pending@x.test", "household_conversion", "pending", nil, &p3.ID, nil)
	if ps := erasureCaptured(t, db, pend); len(ps.People) != 0 {
		t.Errorf("pending conversion: PERS = %v, want none", ps.People)
	}

	// The capture reads only: nothing above was changed.
	var n int64
	db.Model(&models.AccountRequest{}).Count(&n)
	if n != 7 {
		t.Errorf("account_requests rows = %d, want 7", n)
	}
}

func TestErasureHelpers(t *testing.T) {
	if got := erasedRowAddressSQL("guest_signups"); got != "'deleted-guest_signups-' || id || '@deleted.invalid'" {
		t.Errorf("erasedRowAddressSQL = %q", got)
	}
	if got := erasedTokenMarkerSQL("invitations"); got != "'erased-invitations-' || id" {
		t.Errorf("erasedTokenMarkerSQL = %q", got)
	}
	for addr, want := range map[string]bool{
		"deleted-7@deleted.invalid":                 true,
		" Deleted-guest_signups-3@Deleted.Invalid ": true,
		"deleted-7@deleted.invalid.example.com":     false,
		"someone@example.com":                       false,
		"":                                          false,
	} {
		if got := isErasedAddress(addr); got != want {
			t.Errorf("isErasedAddress(%q) = %v, want %v", addr, got, want)
		}
	}
	if erasureRecipientWait != 20*time.Second {
		t.Errorf("erasureRecipientWait = %v, want 20s", erasureRecipientWait)
	}
}

// withStatementTimeout contract, each case on one reserved connection whose
// session value is 7s.
func TestErasureStatementTimeoutContract(t *testing.T) {
	app := testApp(t)
	err := app.DB.Connection(func(conn *gorm.DB) error {
		if err := conn.Exec("SET statement_timeout = '7s'").Error; err != nil {
			return err
		}
		defer conn.Exec("RESET statement_timeout")
		show := func(t *testing.T, db *gorm.DB) string {
			t.Helper()
			var v string
			if err := db.Raw("SHOW statement_timeout").Scan(&v).Error; err != nil {
				t.Fatal(err)
			}
			return v
		}
		pgCode := func(err error) string {
			var pe *pgconn.PgError
			if errors.As(err, &pe) {
				return pe.Code
			}
			return ""
		}

		t.Run("failure keeps the error and issues no restore", func(t *testing.T) {
			tx := conn.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			var fnErr error
			err := withStatementTimeout(tx, 50*time.Millisecond, func() error {
				fnErr = tx.Exec("SELECT pg_sleep(0.2)").Error
				return fnErr
			})
			if fnErr == nil || pgCode(err) != "57014" || !errors.Is(err, fnErr) {
				t.Fatalf("err = %v (fn returned %v), want the same 57014 error", err, fnErr)
			}
			if code := pgCode(tx.Exec("SELECT 1").Error); code != "25P02" {
				t.Errorf("statement after the failure: code %q, want 25P02", code)
			}
			if err := tx.Rollback().Error; err != nil {
				t.Fatal(err)
			}
			if got := show(t, conn); got != "7s" {
				t.Errorf("after rollback statement_timeout = %q, want 7s", got)
			}
		})

		t.Run("success restores the prior value", func(t *testing.T) {
			tx := conn.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			var inside string
			if err := withStatementTimeout(tx, 50*time.Millisecond, func() error {
				inside = show(t, tx)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if inside != "50ms" {
				t.Errorf("inside = %q, want 50ms", inside)
			}
			if got := show(t, tx); got != "7s" {
				t.Errorf("after the call, same transaction = %q, want 7s", got)
			}
			if err := tx.Commit().Error; err != nil {
				t.Fatal(err)
			}
			if got := show(t, conn); got != "7s" {
				t.Errorf("after commit = %q, want 7s", got)
			}
		})

		t.Run("a restore failure is returned", func(t *testing.T) {
			tx := conn.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			err := withStatementTimeout(tx, 50*time.Millisecond, func() error {
				_ = tx.Exec("SELECT 1/0").Error // aborts the transaction; the error is ignored
				return nil
			})
			if pgCode(err) != "25P02" {
				t.Fatalf("err = %v, want code 25P02", err)
			}
			if err := tx.Rollback().Error; err != nil {
				t.Fatal(err)
			}
			if got := show(t, conn); got != "7s" {
				t.Errorf("after rollback statement_timeout = %q, want 7s", got)
			}
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// R4-2a-2 test helpers: erasureTestPause (a barrier matched on facts GORM has
// set before the step, rule F6) and erasureTestSQLStates (SQLSTATE evidence,
// rule F4).
// ---------------------------------------------------------------------------

// erasureTestWhere is a WHERE condition to match: a SQL fragment and the
// values that must sit at the given positions of its variables.
type erasureTestWhere struct {
	SQL  string
	Vars map[int]any
}

// erasureTestPoint names one GORM callback position and, optionally, the
// statement to stop at. Every filter is optional and all given filters must
// match. Table, Where, Assign, Locking and Operation read facts GORM has set
// before the main step of the query, create, update and delete processors;
// SQLContains and VarAt read Statement.SQL and Statement.Vars, which those
// processors build inside the main step, so they are allowed only on row and
// raw or at an After position.
type erasureTestPoint struct {
	Processor string // query, row, raw, create, update or delete
	After     bool
	Step      string // gorm:query, gorm:row, gorm:raw, gorm:create, gorm:update or gorm:delete

	Table       string
	Where       *erasureTestWhere
	Assign      map[string]any // matched against Statement.Dest when it is a map[string]any
	Locking     bool           // a FOR clause is present
	Operation   string         // testDBOperationKey in the statement context
	SQLContains string
	VarAt       map[int]any

	// PID, when set, receives pg_backend_pid() of the participant's own
	// connection before reached closes.
	PID *atomic.Int64
}

func (p erasureTestPoint) validate() error {
	switch p.Processor {
	case "query", "create", "update", "delete":
		if (p.SQLContains != "" || len(p.VarAt) > 0) && !p.After {
			return fmt.Errorf("SQLContains and VarAt cannot match before gorm:%s, which builds Statement.SQL and Statement.Vars itself", p.Processor)
		}
	case "row", "raw":
	default:
		return fmt.Errorf("unknown processor %q", p.Processor)
	}
	if p.Step == "" {
		return errors.New("a point needs its gorm step")
	}
	return nil
}

// erasureTestNormalize turns every integer kind into int64, so a uint id
// matches an int64 value.
func erasureTestNormalize(v any) any {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(rv.Uint())
	}
	return v
}

func erasureTestEqual(a, b any) bool {
	return reflect.DeepEqual(erasureTestNormalize(a), erasureTestNormalize(b))
}

// erasureTestFlatten lists the leaf expressions of a WHERE, descending into
// AndConditions.
func erasureTestFlatten(exprs []clause.Expression) []clause.Expression {
	var out []clause.Expression
	for _, e := range exprs {
		if and, ok := e.(clause.AndConditions); ok {
			out = append(out, erasureTestFlatten(and.Exprs)...)
			continue
		}
		out = append(out, e)
	}
	return out
}

func erasureTestColumnName(c any) string {
	switch v := c.(type) {
	case clause.Column:
		return v.Name
	case string:
		return v
	}
	return ""
}

func (w erasureTestWhere) matches(stmt *gorm.Statement) bool {
	c, ok := stmt.Clauses["WHERE"]
	if !ok {
		return false
	}
	where, ok := c.Expression.(clause.Where)
	if !ok {
		return false
	}
	for _, e := range erasureTestFlatten(where.Exprs) {
		switch v := e.(type) {
		case clause.Expr:
			if !strings.Contains(v.SQL, w.SQL) {
				continue
			}
			all := true
			for i, want := range w.Vars {
				if i >= len(v.Vars) || !erasureTestEqual(v.Vars[i], want) {
					all = false
				}
			}
			if all {
				return true
			}
		case clause.Eq:
			col := strings.TrimSuffix(strings.TrimSpace(w.SQL), " = ?")
			if erasureTestColumnName(v.Column) == col && (len(w.Vars) == 0 || erasureTestEqual(v.Value, w.Vars[0])) {
				return true
			}
		case clause.IN:
			col := strings.TrimSuffix(strings.TrimSpace(w.SQL), " IN ?")
			if erasureTestColumnName(v.Column) != col {
				continue
			}
			all := true
			for _, want := range w.Vars {
				found := false
				for _, got := range v.Values {
					if erasureTestEqual(got, want) {
						found = true
					}
				}
				all = all && found
			}
			if all {
				return true
			}
		}
	}
	return false
}

func (p erasureTestPoint) matches(stmt *gorm.Statement) bool {
	if p.Table != "" && stmt.Table != p.Table {
		return false
	}
	if p.Where != nil && !p.Where.matches(stmt) {
		return false
	}
	if len(p.Assign) > 0 {
		dest, ok := stmt.Dest.(map[string]any)
		if !ok {
			return false
		}
		for col, want := range p.Assign {
			got, ok := dest[col]
			if !ok || !erasureTestEqual(got, want) {
				return false
			}
		}
	}
	if p.Locking {
		if _, ok := stmt.Clauses["FOR"]; !ok {
			return false
		}
	}
	if p.Operation != "" && stmt.Context.Value(testDBOperationKey{}) != p.Operation {
		return false
	}
	if p.SQLContains != "" && !strings.Contains(stmt.SQL.String(), p.SQLContains) {
		return false
	}
	for i, want := range p.VarAt {
		if i >= len(stmt.Vars) || !erasureTestEqual(stmt.Vars[i], want) {
			return false
		}
	}
	return true
}

// erasureTestPause stops the first statement that matches p, before or after
// the named GORM step, on the handle db. reached closes when it has stopped
// (after p.PID is stored), and the statement continues at release. Release
// runs on cleanup before the callback is removed, so a failed assertion never
// strands a participant (F3).
func erasureTestPause(t *testing.T, db *gorm.DB, p erasureTestPoint) (reached <-chan struct{}, release func()) {
	t.Helper()
	if err := p.validate(); err != nil {
		t.Fatalf("erasureTestPause: %v", err)
	}
	signal, resume := make(chan struct{}), make(chan struct{})
	var fired atomic.Bool
	name := fmt.Sprintf("test:erasure_pause:%d", queryCounterSeq.Add(1))
	fn := func(tx *gorm.DB) {
		if tx.Error != nil || !p.matches(tx.Statement) || fired.Swap(true) {
			return
		}
		ctx := tx.Statement.Context
		if p.PID != nil {
			var pid int64
			if err := tx.Statement.ConnPool.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
				tx.AddError(err)
				return
			}
			p.PID.Store(pid)
		}
		close(signal)
		select {
		case <-resume:
		case <-ctx.Done():
			tx.AddError(ctx.Err())
		}
	}
	var err error
	switch p.Processor {
	case "query":
		err = erasureTestRegisterQuery(db, p, name, fn)
	case "row":
		err = erasureTestRegisterRow(db, p, name, fn)
	case "raw":
		err = erasureTestRegisterRaw(db, p, name, fn)
	case "create":
		err = erasureTestRegisterCreate(db, p, name, fn)
	case "update":
		err = erasureTestRegisterUpdate(db, p, name, fn)
	case "delete":
		err = erasureTestRegisterDelete(db, p, name, fn)
	}
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release = func() { once.Do(func() { close(resume) }) }
	t.Cleanup(func() {
		release()
		switch p.Processor {
		case "query":
			_ = db.Callback().Query().Remove(name)
		case "row":
			_ = db.Callback().Row().Remove(name)
		case "raw":
			_ = db.Callback().Raw().Remove(name)
		case "create":
			_ = db.Callback().Create().Remove(name)
		case "update":
			_ = db.Callback().Update().Remove(name)
		case "delete":
			_ = db.Callback().Delete().Remove(name)
		}
	})
	return signal, release
}

func erasureTestRegisterQuery(db *gorm.DB, p erasureTestPoint, name string, fn func(*gorm.DB)) error {
	if p.After {
		return db.Callback().Query().After(p.Step).Register(name, fn)
	}
	return db.Callback().Query().Before(p.Step).Register(name, fn)
}

func erasureTestRegisterRow(db *gorm.DB, p erasureTestPoint, name string, fn func(*gorm.DB)) error {
	if p.After {
		return db.Callback().Row().After(p.Step).Register(name, fn)
	}
	return db.Callback().Row().Before(p.Step).Register(name, fn)
}

func erasureTestRegisterRaw(db *gorm.DB, p erasureTestPoint, name string, fn func(*gorm.DB)) error {
	if p.After {
		return db.Callback().Raw().After(p.Step).Register(name, fn)
	}
	return db.Callback().Raw().Before(p.Step).Register(name, fn)
}

func erasureTestRegisterCreate(db *gorm.DB, p erasureTestPoint, name string, fn func(*gorm.DB)) error {
	if p.After {
		return db.Callback().Create().After(p.Step).Register(name, fn)
	}
	return db.Callback().Create().Before(p.Step).Register(name, fn)
}

func erasureTestRegisterUpdate(db *gorm.DB, p erasureTestPoint, name string, fn func(*gorm.DB)) error {
	if p.After {
		return db.Callback().Update().After(p.Step).Register(name, fn)
	}
	return db.Callback().Update().Before(p.Step).Register(name, fn)
}

func erasureTestRegisterDelete(db *gorm.DB, p erasureTestPoint, name string, fn func(*gorm.DB)) error {
	if p.After {
		return db.Callback().Delete().After(p.Step).Register(name, fn)
	}
	return db.Callback().Delete().Before(p.Step).Register(name, fn)
}

// erasureTestBeforeNotificationsDelete is the erasure's own
// tx.Where("user_id = ?", id).Delete(&models.Notification{}), stopped before
// it executes. Delete adds the condition as a clause.Expr carrying the id
// before gorm:delete runs, and Notification has no DeletedAt, so the
// statement stays a delete. Every account_requests statement of the erasure
// has run by then.
func erasureTestBeforeNotificationsDelete(uid uint) erasureTestPoint {
	return erasureTestPoint{Processor: "delete", Step: "gorm:delete", Table: "notifications",
		Where: &erasureTestWhere{SQL: "user_id = ?", Vars: map[int]any{0: uid}}}
}

// erasureTestBeforeResolve is notify.Resolve(tx, notify.AccountRequested,
// subjectID, _, resolution), stopped before its UPDATE executes: Resolve
// chains Model(...).Where(...) and passes its map to UpdateColumns, which
// stores it in Statement.Dest before gorm:update.
func erasureTestBeforeResolve(subjectID uint, resolution string) erasureTestPoint {
	return erasureTestPoint{Processor: "update", Step: "gorm:update", Table: "notifications",
		Where:  &erasureTestWhere{SQL: "kind = ? AND subject_id = ?", Vars: map[int]any{0: notify.AccountRequested, 1: subjectID}},
		Assign: map[string]any{"resolution": resolution}}
}

// erasureTestStateEntry is one failed statement: its SQLSTATE and the start
// of its SQL.
type erasureTestStateEntry struct {
	Code string
	SQL  string
}

// erasureTestStateLog records the SQLSTATE of every failed statement on the
// handle erasureTestSQLStates installed (F4). A generic HTTP 500 proves
// nothing about a deadlock; this does.
type erasureTestStateLog struct {
	t       *testing.T
	mu      sync.Mutex
	entries []erasureTestStateEntry
}

// Count is how many failed statements carried SQLSTATE code.
func (l *erasureTestStateLog) Count(code string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.entries {
		if e.Code == code {
			n++
		}
	}
	return n
}

// Only returns the single failed statement with SQLSTATE code and fails the
// test when there is not exactly one.
func (l *erasureTestStateLog) Only(code string) erasureTestStateEntry {
	l.t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	var found []erasureTestStateEntry
	for _, e := range l.entries {
		if e.Code == code {
			found = append(found, e)
		}
	}
	if len(found) != 1 {
		l.t.Fatalf("SQLSTATE %s recorded %d times, want exactly 1 (all: %v)", code, len(found), l.entries)
	}
	return found[0]
}

type erasureTestSQLKey struct{}

type erasureTestTracer struct{ log *erasureTestStateLog }

func (tr erasureTestTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, erasureTestSQLKey{}, d.SQL)
}

func (tr erasureTestTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryEndData) {
	if d.Err == nil {
		return
	}
	code := ""
	var pe *pgconn.PgError
	if errors.As(d.Err, &pe) {
		code = pe.Code
	}
	sqlText, _ := ctx.Value(erasureTestSQLKey{}).(string)
	if len(sqlText) > 120 {
		sqlText = sqlText[:120]
	}
	tr.log.mu.Lock()
	tr.log.entries = append(tr.log.entries, erasureTestStateEntry{Code: code, SQL: sqlText})
	tr.log.mu.Unlock()
}

// erasureTestSQLStates swaps app.DB for a handle on the same database whose
// pgx connections record the SQLSTATE of every failed statement, and puts the
// original back on cleanup. Register pauses and sessions after calling it, on
// the swapped app.DB.
func erasureTestSQLStates(t *testing.T, app *App) *erasureTestStateLog {
	t.Helper()
	var name string
	if err := app.DB.Raw("SELECT current_database()").Scan(&name).Error; err != nil {
		t.Fatal(err)
	}
	dsn, err := testdb.URL(name)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	log := &erasureTestStateLog{t: t}
	cfg.Tracer = erasureTestTracer{log: log}
	sqlDB := stdlib.OpenDB(*cfg)
	traced, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := traced.Use(txguard.Plugin{}); err != nil {
		t.Fatal(err)
	}
	original := app.DB
	app.DB = traced
	t.Cleanup(func() {
		app.DB = original
		_ = sqlDB.Close()
	})
	return log
}

// TestErasureTestPauseFiresBeforeExecution runs the helper against the exact
// production statement shapes.
func TestErasureTestPauseFiresBeforeExecution(t *testing.T) {
	app := testApp(t)
	db := app.DB
	admin := newTestUser(t, app, "pause-admin", erasurePW, models.RoleAdmin)
	u := newTestUser(t, app, "pause-u", erasurePW, models.RoleMember)
	v := newTestUser(t, app, "pause-v", erasurePW, models.RoleMember)
	for _, uid := range []uint{u.ID, v.ID} {
		for i := 0; i < 2; i++ {
			erasureMustCreate(t, db, &models.Notification{UserID: uid, Kind: "test.note", Category: "groups", Title: "note", Link: "/"})
		}
	}
	expires := time.Now().Add(time.Hour)
	r := erasureSeedRequest(t, db, models.AccountRequest{Name: "R", Email: "pause-r@x.test", Source: "self_signup", Status: models.RequestPending, ExpiresAt: expires})
	r2 := erasureSeedRequest(t, db, models.AccountRequest{Name: "R2", Email: "pause-r2@x.test", Source: "self_signup", Status: models.RequestPending, ExpiresAt: expires})
	erasureSeedTasks(t, db, &r)
	erasureSeedTasks(t, db, &r2)
	notRun := func(t *testing.T, reached <-chan struct{}, what string) {
		t.Helper()
		select {
		case <-reached:
			t.Fatalf("%s fired the barrier", what)
		default:
		}
	}
	count := func(q string, args ...any) int64 {
		t.Helper()
		var n int64
		if err := db.Raw(q, args...).Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("notifications delete", func(t *testing.T) {
		var pid atomic.Int64
		p := erasureTestBeforeNotificationsDelete(u.ID)
		p.PID = &pid
		reached, release := erasureTestPause(t, db, p)
		// V's delete has the same shape and another id: it neither fires nor waits.
		if err := db.Transaction(func(tx *gorm.DB) error {
			return tx.Where("user_id = ?", v.ID).Delete(&models.Notification{}).Error
		}); err != nil {
			t.Fatal(err)
		}
		notRun(t, reached, "V's delete")
		if n := count("SELECT count(*) FROM notifications WHERE user_id = ?", v.ID); n != 0 {
			t.Errorf("V has %d notifications left", n)
		}
		before := count("SELECT count(*) FROM notifications WHERE user_id = ?", u.ID)
		sessionPID, done := startDBSession(t, db, "pause-delete", func(conn *gorm.DB) error {
			return conn.Transaction(func(tx *gorm.DB) error {
				return tx.Where("user_id = ?", u.ID).Delete(&models.Notification{}).Error
			})
		})
		awaitDBSignal(t, reached)
		if got := pid.Load(); got != int64(sessionPID) {
			t.Errorf("recorded pid %d, the participant's session is %d", got, sessionPID)
		}
		var state string
		if err := db.Raw("SELECT state FROM pg_stat_activity WHERE pid = ?", pid.Load()).Scan(&state).Error; err != nil || state != "idle in transaction" {
			t.Errorf("recorded backend state = %q (%v), want idle in transaction", state, err)
		}
		if n := count("SELECT count(*) FROM notifications WHERE user_id = ?", u.ID); n != before {
			t.Errorf("while paused U has %d notifications, want %d: the DELETE ran", n, before)
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			return tx.Exec("SELECT 1 FROM notifications WHERE user_id = ? FOR UPDATE NOWAIT", u.ID).Error
		}); err != nil {
			t.Errorf("U's rows were locked while paused: %v", err)
		}
		release()
		if err := awaitDBResult(t, done); err != nil {
			t.Fatal(err)
		}
		if n := count("SELECT count(*) FROM notifications WHERE user_id = ?", u.ID); n != 0 {
			t.Errorf("U has %d notifications after release", n)
		}
	})

	t.Run("resolve expired", func(t *testing.T) {
		var pid atomic.Int64
		p := erasureTestBeforeResolve(r.ID, "expired")
		p.PID = &pid
		reached, release := erasureTestPause(t, db, p)
		for _, c := range []struct {
			subject    uint
			resolution string
		}{{r2.ID, "expired"}, {r.ID, "withdrawn"}} {
			tx := db.Begin()
			if _, err := notify.Resolve(tx, notify.AccountRequested, c.subject, nil, c.resolution); err != nil {
				t.Fatal(err)
			}
			tx.Rollback()
			notRun(t, reached, fmt.Sprintf("Resolve(%d, %s)", c.subject, c.resolution))
		}
		sessionPID, done := startDBSession(t, db, "pause-resolve", func(conn *gorm.DB) error {
			return conn.Transaction(func(tx *gorm.DB) error {
				_, err := notify.Resolve(tx, notify.AccountRequested, r.ID, nil, "expired")
				return err
			})
		})
		awaitDBSignal(t, reached)
		if got := pid.Load(); got != int64(sessionPID) {
			t.Errorf("recorded pid %d, the participant's session is %d", got, sessionPID)
		}
		open := "SELECT count(*) FROM notifications WHERE kind = ? AND subject_id = ? AND resolved_at IS NULL"
		if n := count(open, notify.AccountRequested, r.ID); n == 0 {
			t.Error("R's task was resolved while paused: the UPDATE ran")
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			return tx.Exec("SELECT 1 FROM notifications WHERE kind = ? AND subject_id = ? FOR UPDATE NOWAIT", notify.AccountRequested, r.ID).Error
		}); err != nil {
			t.Errorf("R's task was locked while paused: %v", err)
		}
		release()
		if err := awaitDBResult(t, done); err != nil {
			t.Fatal(err)
		}
		if n := count(open, notify.AccountRequested, r.ID); n != 0 {
			t.Errorf("%d of R's tasks still open after release", n)
		}
		if n := count("SELECT count(*) FROM notifications WHERE kind = ? AND subject_id = ? AND resolution = 'expired' AND user_id = ?", notify.AccountRequested, r.ID, admin.ID); n != 1 {
			t.Errorf("admin's task for R: %d resolved expired, want 1", n)
		}
	})

	t.Run("validate", func(t *testing.T) {
		bad := []erasureTestPoint{
			{Processor: "update", Step: "gorm:update", SQLContains: "UPDATE"},
			{Processor: "delete", Step: "gorm:delete", VarAt: map[int]any{0: 1}},
			{Processor: "query", Step: "gorm:query", SQLContains: "x"},
			{Processor: "create", Step: "gorm:create", VarAt: map[int]any{0: 1}},
		}
		for _, p := range bad {
			if p.validate() == nil {
				t.Errorf("Before gorm:%s with SQL filters was accepted", p.Processor)
			}
		}
		good := []erasureTestPoint{
			{Processor: "row", Step: "gorm:row", SQLContains: "FOR UPDATE"},
			{Processor: "raw", Step: "gorm:raw", VarAt: map[int]any{0: 1}},
			{Processor: "update", Step: "gorm:update", After: true, SQLContains: "UPDATE"},
			{Processor: "update", Step: "gorm:update", Table: "guest_signups"},
			erasureTestBeforeNotificationsDelete(1),
			erasureTestBeforeResolve(1, "expired"),
		}
		for _, p := range good {
			if err := p.validate(); err != nil {
				t.Errorf("%s %s was refused: %v", p.Processor, p.Step, err)
			}
		}
	})
}

func erasureSeedRequest(t *testing.T, db *gorm.DB, r models.AccountRequest) models.AccountRequest {
	t.Helper()
	erasureMustCreate(t, db, &r)
	return r
}

// erasureSeedTasks writes the waiting AccountRequested task for every active
// admin, the way a real request does.
func erasureSeedTasks(t *testing.T, db *gorm.DB, r *models.AccountRequest) {
	t.Helper()
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := notifyAccountRequested(tx, r, models.RequestFlags{})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func erasureRequestRow(t *testing.T, db *gorm.DB, id uint) models.AccountRequest {
	t.Helper()
	var r models.AccountRequest
	if err := db.First(&r, id).Error; err != nil {
		t.Fatal(err)
	}
	return r
}

func erasurePlaceholder(id uint) string {
	return fmt.Sprintf("deleted-account_requests-%d@deleted.invalid", id)
}

// TestAccountErasureAnonymizesAccountRequests goes through the real DELETE.
func TestAccountErasureAnonymizesAccountRequests(t *testing.T) {
	s := newCoreErasureScene(t)
	s.ac.holdNotices = true
	app, db := s.app, s.app.DB
	u := newTestUser(t, app, "erase-req-u", erasurePW, models.RoleMember)
	other := newTestUser(t, app, "erase-req-other", erasurePW, models.RoleMember)
	inviter := newTestUser(t, app, "erase-req-inviter", erasurePW, models.RoleMember)
	addr := mail.NormalizeAddress(u.Email)
	hash := "request-password-hash"
	birth := time.Date(2012, 5, 5, 0, 0, 0, 0, time.UTC)
	expires := time.Now().Add(48 * time.Hour).Truncate(time.Microsecond)

	r1 := erasureSeedRequest(t, db, models.AccountRequest{Name: "Pat Real", Email: addr, Phone: "555-0100", BirthDate: &birth,
		Message: "please let me in", GuardianEmail: "guardian@x.test", PasswordHash: &hash, DecisionNote: "n", IP: "9.9.9.1",
		Source: models.RequestSelfSignup, Status: models.RequestPending, ExpiresAt: expires})
	r2 := erasureSeedRequest(t, db, models.AccountRequest{Name: "Invited Pat", Email: addr, IP: "9.9.9.2", RequestedBy: &inviter.ID,
		Source: models.RequestMemberInvite, Status: models.RequestPending, ExpiresAt: expires})
	// The erased person invited someone: only the ip of that row goes (M39).
	r3 := erasureSeedRequest(t, db, models.AccountRequest{Name: "Invitee", Email: "invitee@x.test", IP: "9.9.9.3", RequestedBy: &u.ID,
		Source: models.RequestMemberInvite, Status: models.RequestPending, ExpiresAt: expires})
	// A conversion the erased person started for someone else keeps its ip.
	r4 := erasureSeedRequest(t, db, models.AccountRequest{Name: "Kid", Email: "kid@x.test", IP: "9.9.9.4", RequestedBy: &u.ID,
		Source: models.RequestHouseholdConversion, Status: models.RequestPending, ExpiresAt: expires})
	// Approved for another account that carries the same address.
	r5 := erasureSeedRequest(t, db, models.AccountRequest{Name: "Other Pat", Email: addr, IP: "9.9.9.5", ApprovedUserID: &other.ID,
		Source: models.RequestSelfSignup, Status: models.RequestApproved, ExpiresAt: expires})
	// Another person's request that named this address as the guardian's.
	r6 := erasureSeedRequest(t, db, models.AccountRequest{Name: "Teen", Email: "teen@x.test", GuardianEmail: addr, IP: "9.9.9.6",
		Source: models.RequestSelfSignup, Status: models.RequestPending, AccountType: models.AccountTeen, ExpiresAt: expires})
	// An approved staged conversion of the erased person keeps every id link.
	hh := models.Household{Name: "Req family", CreatedBy: inviter.ID}
	erasureMustCreate(t, db, &hh)
	person := models.HouseholdPerson{HouseholdID: hh.ID, FirstName: "Staged", Kind: models.HouseholdTeen, CreatedBy: inviter.ID}
	erasureMustCreate(t, db, &person)
	invID := erasureInvitation(t, db, inviter.ID, "conv@x.test", true, nil)
	decided := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	r7 := erasureSeedRequest(t, db, models.AccountRequest{Name: "Converted", Email: "conv@x.test", IP: "9.9.9.7", RequestedBy: &inviter.ID,
		ApprovedUserID: &u.ID, HouseholdPersonID: &person.ID, InvitationID: &invID, DecidedBy: &inviter.ID, DecidedAt: &decided,
		Source: models.RequestHouseholdConversion, Status: models.RequestApproved, ExpiresAt: expires})

	untouched := func() string {
		return dumpRows(t, db, "account_requests", "id", "id IN ?", []uint{r4.ID, r5.ID})
	}
	beforeUntouched := untouched()
	s.erase(t, u.ID)
	if got := untouched(); got != beforeUntouched {
		t.Errorf("rows of other people changed:\nbefore %s\nafter  %s", beforeUntouched, got)
	}

	scrubbed := func(r models.AccountRequest) {
		t.Helper()
		if r.Name != "" || r.Phone != "" || r.BirthDate != nil || r.Message != "" || r.GuardianEmail != "" ||
			r.PasswordHash != nil || r.DecisionNote != "" {
			t.Errorf("request %d keeps personal data: %+v", r.ID, r)
		}
		if r.Email != erasurePlaceholder(r.ID) {
			t.Errorf("request %d email = %q, want %q", r.ID, r.Email, erasurePlaceholder(r.ID))
		}
	}
	g1 := erasureRequestRow(t, db, r1.ID)
	scrubbed(g1)
	if g1.Status != models.RequestWithdrawn || g1.IP != "" || g1.Source != models.RequestSelfSignup || !g1.ExpiresAt.Equal(r1.ExpiresAt) {
		t.Errorf("pending self-signup: status %q ip %q source %q", g1.Status, g1.IP, g1.Source)
	}
	g2 := erasureRequestRow(t, db, r2.ID)
	scrubbed(g2)
	if g2.Status != models.RequestWithdrawn || g2.IP != "9.9.9.2" || g2.RequestedBy == nil || *g2.RequestedBy != inviter.ID {
		t.Errorf("member invite: status %q ip %q", g2.Status, g2.IP)
	}
	if g1.Email == g2.Email {
		t.Error("two scrubbed requests share one placeholder address")
	}
	g3 := erasureRequestRow(t, db, r3.ID)
	if g3.IP != "" || g3.Name != "Invitee" || g3.Email != "invitee@x.test" || g3.Status != models.RequestPending {
		t.Errorf("request the erased person made: %+v", g3)
	}
	g4 := erasureRequestRow(t, db, r4.ID)
	if g4.IP != "9.9.9.4" || g4.Name != "Kid" {
		t.Errorf("conversion the erased person started changed: %+v", g4)
	}
	g6 := erasureRequestRow(t, db, r6.ID)
	if g6.GuardianEmail != "" || g6.Name != "Teen" || g6.Email != "teen@x.test" || g6.IP != "9.9.9.6" || g6.Status != models.RequestPending {
		t.Errorf("another person's request: %+v", g6)
	}
	g7 := erasureRequestRow(t, db, r7.ID)
	scrubbed(g7)
	if g7.IP != "" || g7.Status != models.RequestApproved || g7.Source != models.RequestHouseholdConversion ||
		g7.ApprovedUserID == nil || *g7.ApprovedUserID != u.ID || g7.HouseholdPersonID == nil || *g7.HouseholdPersonID != person.ID ||
		g7.InvitationID == nil || *g7.InvitationID != invID || g7.DecidedBy == nil || *g7.DecidedBy != inviter.ID ||
		g7.DecidedAt == nil || !g7.DecidedAt.Equal(decided) || g7.RequestedBy == nil || *g7.RequestedBy != inviter.ID {
		t.Errorf("approved conversion lost a link or kept its ip: %+v", g7)
	}
	// The unique conversion index still holds the person and the user.
	dup := models.AccountRequest{Name: "Dup", Email: "dup@x.test", ApprovedUserID: &u.ID, HouseholdPersonID: &person.ID,
		Source: models.RequestHouseholdConversion, Status: models.RequestApproved, ExpiresAt: expires}
	if err := db.Create(&dup).Error; err == nil {
		t.Error("a second approved conversion for the same person was accepted")
	}
	_ = r5
}

// TestAccountErasureRequestAuditDisposition: only pending rows change status,
// decided rows and their audit rows stay, and the waiting tasks close.
func TestAccountErasureRequestAuditDisposition(t *testing.T) {
	s := newCoreErasureScene(t)
	s.ac.holdNotices = true
	app, db := s.app, s.app.DB
	u := newTestUser(t, app, "erase-aud-u", erasurePW, models.RoleMember)
	third := newTestUser(t, app, "erase-aud-third", erasurePW, models.RoleMember)
	addr := mail.NormalizeAddress(u.Email)
	expires := time.Now().Add(48 * time.Hour)
	decided := time.Now().Add(-time.Hour).UTC()

	pend1 := erasureSeedRequest(t, db, models.AccountRequest{Name: "Pending One", Email: addr, Source: models.RequestSelfSignup, Status: models.RequestPending, ExpiresAt: expires})
	pend2 := erasureSeedRequest(t, db, models.AccountRequest{Name: "Pending Two", Email: addr, Source: models.RequestMemberInvite, RequestedBy: &s.second.ID, Status: models.RequestPending, ExpiresAt: expires})
	approved := erasureSeedRequest(t, db, models.AccountRequest{Name: "Approved", Email: "elsewhere@x.test", ApprovedUserID: &u.ID, DecidedBy: &s.second.ID, DecidedAt: &decided, DecisionNote: "ok",
		Source: models.RequestSelfSignup, Status: models.RequestApproved, ExpiresAt: expires})
	rejected := erasureSeedRequest(t, db, models.AccountRequest{Name: "Rejected", Email: addr, DecidedBy: &s.second.ID, DecidedAt: &decided,
		Source: models.RequestSelfSignup, Status: models.RequestRejected, ExpiresAt: expires})
	expired := erasureSeedRequest(t, db, models.AccountRequest{Name: "Expired", Email: addr, Source: models.RequestSelfSignup, Status: models.RequestExpired, ExpiresAt: decided})
	unrelated := erasureSeedRequest(t, db, models.AccountRequest{Name: "Someone Else", Email: "else@x.test", Source: models.RequestSelfSignup, Status: models.RequestPending, ExpiresAt: expires})
	for _, r := range []*models.AccountRequest{&pend1, &pend2, &unrelated} {
		erasureSeedTasks(t, db, r)
	}
	// A waiting task of another kind about the same subject must stay open.
	erasureMustCreate(t, db, &models.Notification{UserID: s.second.ID, Kind: "test.other", Category: "admin", Waiting: true,
		SubjectType: "account_request", SubjectID: &pend1.ID, Title: "other", Link: "/"})
	for i, r := range []models.AccountRequest{approved, rejected, expired} {
		action := []string{audit.AccountApproved, audit.AccountRejected, audit.AccountExpired}[i]
		erasureMustCreate(t, db, &models.AuditLog{ActorID: &s.second.ID, Action: action, TargetType: "account_request", TargetID: r.ID,
			MetaJSON: `{"note":"kept"}`, IP: "9.9.9.9"})
	}
	_ = third

	decisions := func() string {
		var out string
		if err := db.Raw(`SELECT coalesce(jsonb_agg(jsonb_build_object('id', id, 'status', status, 'decidedBy', decided_by, 'decidedAt', decided_at) ORDER BY id)::text, '[]')
			FROM account_requests WHERE id IN ?`, []uint{approved.ID, rejected.ID, expired.ID}).Scan(&out).Error; err != nil {
			t.Fatal(err)
		}
		return out
	}
	decisionAudit := func() string {
		return dumpRows(t, db, "audit_logs", "id", "action IN ?", []string{audit.AccountApproved, audit.AccountRejected, audit.AccountExpired})
	}
	otherTasks := func() string {
		return dumpRows(t, db, "notifications", "id", "kind = 'test.other' OR (kind = ? AND subject_id = ?)", notify.AccountRequested, unrelated.ID)
	}
	mailJobs := func() int64 {
		var n int64
		if err := db.Raw("SELECT count(*) FROM jobs WHERE kind = ?", kindAccountRequestMail).Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	accountAudit := func() int64 {
		var n int64
		if err := db.Raw("SELECT count(*) FROM audit_logs WHERE action LIKE 'account.%'").Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	d0, a0, o0, j0, n0 := decisions(), decisionAudit(), otherTasks(), mailJobs(), accountAudit()
	s.erase(t, u.ID)

	for _, id := range []uint{pend1.ID, pend2.ID} {
		if g := erasureRequestRow(t, db, id); g.Status != models.RequestWithdrawn {
			t.Errorf("pending request %d is %q, want withdrawn", id, g.Status)
		}
	}
	if got := decisions(); got != d0 {
		t.Errorf("decided requests changed:\nbefore %s\nafter  %s", d0, got)
	}
	if got := decisionAudit(); got != a0 {
		t.Errorf("decision audit rows changed:\nbefore %s\nafter  %s", a0, got)
	}
	if got := otherTasks(); got != o0 {
		t.Errorf("unrelated tasks changed:\nbefore %s\nafter  %s", o0, got)
	}
	if got := mailJobs(); got != j0 {
		t.Errorf("account request mail jobs: %d, was %d", got, j0)
	}
	if got := accountAudit(); got != n0 {
		t.Errorf("account.* audit rows: %d, was %d", got, n0)
	}
	if n := auditCount(t, app, audit.UserDeleted); n != 1 {
		t.Errorf("user.deleted rows = %d, want 1", n)
	}
	var total, closed int64
	q := "SELECT count(*) FROM notifications WHERE kind = ? AND subject_id IN ? AND user_id <> ?"
	if err := db.Raw(q, notify.AccountRequested, []uint{pend1.ID, pend2.ID}, u.ID).Scan(&total).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(q+" AND resolved_at IS NOT NULL AND resolved_by_id = ? AND resolution = 'withdrawn'", notify.AccountRequested, []uint{pend1.ID, pend2.ID}, u.ID, s.admin.ID).Scan(&closed).Error; err != nil {
		t.Fatal(err)
	}
	if total == 0 || closed != total {
		t.Errorf("%d of %d waiting tasks closed as withdrawn by the admin", closed, total)
	}
}

// TestAccountRequestApprovalLosesToErasure: an approval that waits for the
// contact gate while the erasure is paused finds the request withdrawn.
func TestAccountRequestApprovalLosesToErasure(t *testing.T) {
	s := newCoreErasureScene(t)
	app := s.app
	u := newTestUser(t, app, "erase-appr-u", erasurePW, models.RoleMember)
	s.ac.holdNotices = true
	zc := loginAPI(t, app, "erase-second", erasurePW)
	zc.holdNotices = true
	req := erasureSeedRequest(t, app.DB, models.AccountRequest{Name: "Pending Pat", Email: mail.NormalizeAddress(u.Email),
		Source: models.RequestSelfSignup, Status: models.RequestPending, ExpiresAt: time.Now().Add(48 * time.Hour)})
	erasureSeedTasks(t, app.DB, &req)
	var users0 int64
	app.DB.Model(&models.User{}).Unscoped().Count(&users0)

	states := erasureTestSQLStates(t, app)
	var erasurePID atomic.Int64
	p := erasureTestBeforeNotificationsDelete(u.ID)
	p.PID = &erasurePID
	reached, release := erasureTestPause(t, app.DB, p)
	del := bgDo(s.ac, "DELETE", "/api/v1/admin/users/"+itoa(u.ID), nil)
	awaitDBSignal(t, reached)
	appr := bgDo(zc, "POST", "/api/v1/admin/account-requests/"+itoa(req.ID)+"/approve", map[string]any{})
	approvalPID := waitForLockedUsersQuery(t, app, "%pg_advisory_xact_lock%")
	waitDBBlocked(t, app.DB, approvalPID, int(erasurePID.Load()))
	release()
	if r := awaitResult(t, del); r.code != http.StatusOK {
		t.Fatalf("delete: %d %s", r.code, r.body)
	}
	r := awaitResult(t, appr)
	if r.code != http.StatusConflict || !strings.Contains(r.body, `"resolution":"withdrawn"`) {
		t.Fatalf("approval: %d %s, want 409 naming the withdrawn resolution", r.code, r.body)
	}
	if got := erasureRequestRow(t, app.DB, req.ID); got.Status != models.RequestWithdrawn {
		t.Errorf("request is %q, want withdrawn", got.Status)
	}
	var users1 int64
	app.DB.Model(&models.User{}).Unscoped().Count(&users1)
	if users1 != users0 {
		t.Errorf("users: %d, was %d; the approval created an account", users1, users0)
	}
	if n := auditCount(t, app, audit.AccountApproved); n != 0 {
		t.Errorf("%d account.approved rows", n)
	}
	if n := states.Count("40P01"); n != 0 {
		t.Errorf("%d deadlocks recorded", n)
	}
}

// TestAccountRequestExpiryRacingErasure: expirePendingRequests and the
// erasure take request rows before notification rows (L2), in both orders and
// for both ways a request reaches the erasure (O: the person's own pending
// request; G: another person's request that names the address as guardian).
func TestAccountRequestExpiryRacingErasure(t *testing.T) {
	for _, variant := range []string{"O", "G"} {
		for _, order := range []string{"expiry-first", "erasure-first"} {
			t.Run(variant+"/"+order, func(t *testing.T) { expiryRacesErasure(t, variant, order) })
		}
	}
}

func expiryRacesErasure(t *testing.T, variant, order string) {
	s := newCoreErasureScene(t)
	app := s.app
	u := s.second // the erased admin
	z := newTestUser(t, app, "erase-z", erasurePW, models.RoleAdmin)
	s.ac.holdNotices = true
	addr := mail.NormalizeAddress(u.Email)
	r := models.AccountRequest{Name: "Req Person", Email: addr, Source: models.RequestSelfSignup, Status: models.RequestPending,
		IP: "9.9.9.1", ExpiresAt: time.Now().Add(-time.Hour)}
	if variant == "G" {
		r.Email, r.GuardianEmail, r.AccountType = "child@x.test", addr, models.AccountTeen
	}
	r = erasureSeedRequest(t, app.DB, r)
	erasureSeedTasks(t, app.DB, &r)

	states := erasureTestSQLStates(t, app)
	db := app.DB
	type expiryResult struct {
		n   int64
		err error
	}
	expiry := make(chan expiryResult, 1)
	runExpiry := func() {
		go func() {
			n, err := app.expirePendingRequests()
			expiry <- expiryResult{n, err}
		}()
	}
	delPath := "/api/v1/admin/users/" + itoa(u.ID)
	var wantExpired int64
	switch order {
	case "expiry-first":
		var expiryPID atomic.Int64
		p := erasureTestBeforeResolve(r.ID, "expired")
		p.PID = &expiryPID
		reached, release := erasureTestPause(t, db, p)
		runExpiry()
		awaitDBSignal(t, reached) // expiry holds R and has not touched notifications
		del := bgDo(s.ac, "DELETE", delPath, nil)
		erasurePID := waitForLockedUsersQuery(t, app, "%account_requests%")
		waitDBBlocked(t, db, erasurePID, int(expiryPID.Load()))
		release()
		if res := awaitResult(t, del); res.code != http.StatusOK {
			t.Fatalf("delete: %d %s", res.code, res.body)
		}
		wantExpired = 1
	case "erasure-first":
		var erasurePID atomic.Int64
		p := erasureTestBeforeNotificationsDelete(u.ID)
		p.PID = &erasurePID
		reached, release := erasureTestPause(t, db, p)
		del := bgDo(s.ac, "DELETE", delPath, nil)
		awaitDBSignal(t, reached) // every account_requests statement has run; R is held
		runExpiry()
		expiryPID := waitForLockedUsersQuery(t, app, "%account_requests%")
		waitDBBlocked(t, db, expiryPID, int(erasurePID.Load()))
		release()
		if res := awaitResult(t, del); res.code != http.StatusOK {
			t.Fatalf("delete: %d %s", res.code, res.body)
		}
		if variant == "G" {
			wantExpired = 1
		}
	}
	var res expiryResult
	select {
	case res = <-expiry:
	case <-time.After(10 * time.Second):
		t.Fatal("expiry never finished")
	}
	if res.err != nil || res.n != wantExpired {
		t.Fatalf("expirePendingRequests = (%d, %v), want (%d, nil)", res.n, res.err, wantExpired)
	}

	got := erasureRequestRow(t, db, r.ID)
	var wantStatus, wantResolution string
	var wantBy *uint
	switch {
	case variant == "O" && order == "erasure-first":
		wantStatus, wantResolution, wantBy = models.RequestWithdrawn, "withdrawn", &s.admin.ID
	default:
		wantStatus, wantResolution = models.RequestExpired, "expired"
	}
	if got.Status != wantStatus {
		t.Errorf("request status = %q, want %q", got.Status, wantStatus)
	}
	if variant == "O" {
		if got.Email != erasurePlaceholder(r.ID) || got.Name != "" || got.IP != "" {
			t.Errorf("request of the erased person not scrubbed: %+v", got)
		}
	} else if got.GuardianEmail != "" || got.Name != "Req Person" || got.Email != "child@x.test" || got.IP != "9.9.9.1" {
		t.Errorf("another person's request: want only guardian_email cleared: %+v", got)
	}
	var task models.Notification
	if err := db.Where("kind = ? AND subject_id = ? AND user_id = ?", notify.AccountRequested, r.ID, z.ID).First(&task).Error; err != nil {
		t.Fatalf("Z's task: %v", err)
	}
	if task.ResolvedAt == nil || task.Resolution != wantResolution || (wantBy == nil) != (task.ResolvedByID == nil) ||
		(wantBy != nil && *task.ResolvedByID != *wantBy) {
		t.Errorf("Z's task: resolved %v by %v as %q, want %q by %v", task.ResolvedAt != nil, task.ResolvedByID, task.Resolution, wantResolution, wantBy)
	}
	var left int64
	db.Model(&models.Notification{}).Where("user_id = ?", u.ID).Count(&left)
	if left != 0 {
		t.Errorf("%d of the erased admin's notifications remain", left)
	}
	if n := auditCount(t, app, audit.AccountExpired); n != wantExpired {
		t.Errorf("account.expired rows = %d, want %d", n, wantExpired)
	}
	if n := states.Count("40P01"); n != 0 {
		t.Errorf("%d deadlocks recorded: %v", n, states.entries)
	}
}

// TestErasureTestSQLStatesRecordsFailures: the recorder keeps the SQLSTATE and
// the start of the SQL of a failed statement, and nothing for a good one.
func TestErasureTestSQLStatesRecordsFailures(t *testing.T) {
	app := testApp(t)
	states := erasureTestSQLStates(t, app)
	if err := app.DB.Exec("SELECT 1").Error; err != nil {
		t.Fatal(err)
	}
	if n := states.Count("22012"); n != 0 {
		t.Fatalf("%d division errors before one happened", n)
	}
	if err := app.DB.Exec("SELECT 1/0").Error; err == nil {
		t.Fatal("division by zero succeeded")
	}
	if e := states.Only("22012"); !strings.Contains(e.SQL, "1/0") {
		t.Errorf("recorded SQL %q", e.SQL)
	}
	if n := states.Count("40P01"); n != 0 {
		t.Errorf("%d deadlocks recorded", n)
	}
}
