package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
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

	stopAvatar := failJobEnqueue(t, db, kindAvatarCleanup)
	r := deleteAccount(s.ac, u.ID, nil)
	if r.code != http.StatusInternalServerError {
		t.Fatalf("delete answered %d %s, want 500", r.code, r.body)
	}
	if after := erasureSnapshot(t, db, u.ID, owned); after != before {
		t.Errorf("a failed erasure left a change:\n%s\n%s", before, after)
	}
	stopAvatar()

	// R4-2a-5: a failure in the last statement (the throttle delete) leaves
	// every table R4-2a touches identical. This is one transaction with no
	// competing writer, so equality with the pre-state is the right comparison.
	s.ac.holdNotices = true
	_, occ, _ := erasureEventFixture(t, s)
	expires := time.Now().Add(48 * time.Hour)
	req := erasureSeedRequest(t, db, models.AccountRequest{Name: "Rollback Requester", Email: addr, Source: models.RequestSelfSignup,
		Status: models.RequestPending, ExpiresAt: expires})
	erasureSeedTasks(t, db, &req)
	erasureInvitation(t, db, s.admin.ID, addr, false, nil)
	erasureMustCreate(t, db, &models.GuestSignup{Name: "Rollback Guest", Email: addr, OccurrenceID: occ.ID, TokenHash: "rollback-signup",
		ExpiresAt: expires, IP: "203.0.113.9", Timezone: "UTC"})
	erasureMustCreate(t, db, &models.MailOutbox{ToAddress: addr, Subject: "rollback", Body: "rollback"})
	at := time.Now().Add(-time.Minute)
	m := models.Message{AuthorID: &l.ID, Status: models.MessageScheduled, Subject: "Rollback", BodyHTML: "<p>x</p>", BodyText: "x", ScheduledAt: &at}
	erasureMustCreate(t, db, &m)
	mine := models.Message{AuthorID: &u.ID, Status: models.MessageScheduled, Subject: "Rollback mine", BodyHTML: "<p>x</p>", BodyText: "x", ScheduledAt: &at}
	erasureMustCreate(t, db, &mine)
	erasureMustCreate(t, db, &models.MessageRecipient{MessageID: m.ID, UserID: u.ID, Channel: models.DeliveryEmail, Address: addr, Status: models.RecipientQueued})
	erasureMustCreate(t, db, &models.Registration{UserID: &u.ID, OccurrenceID: occ.ID, Status: models.RegGoing, Source: models.RegSourceGuestForm,
		AttendanceMode: models.AttendanceInPerson, ManageTokenHash: "rollback-manage-hash", ChildcareAgesNote: "ages 3", RemovalNote: "note"})
	erasureSeedActivity(t, s, u, l)
	tables := []string{"account_requests", "notifications", "invitations", "guest_signups", "mail_outboxes", "messages", "message_recipients",
		"audit_logs", "series_undo_receipts", "manager_add_blocks", "registrations", "notification_deliveries", "users"}
	writeSet := func() string {
		var out string
		for _, table := range tables {
			out += table + dumpRows(t, db, table, "id", "true")
		}
		return out
	}
	whole := writeSet()
	stopThrottle := erasureTestFailThrottleDelete(t, db)
	if r := deleteAccount(s.ac, u.ID, nil); r.code != http.StatusInternalServerError {
		t.Fatalf("delete with a failing last statement answered %d %s, want 500", r.code, r.body)
	}
	stopThrottle()
	if got := writeSet(); got != whole {
		t.Errorf("a failed erasure left a change in the tables R4-2a touches:\n%s\n%s", whole, got)
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

// ---------------------------------------------------------------------------
// R4-2a-3: invitations, guest signups and the dev outbox.
// ---------------------------------------------------------------------------

// erasureEventFixture is a public group's event with one future occurrence,
// made by the scene's admin.
func erasureEventFixture(t *testing.T, s coreErasureScene) (eventID uint, occ models.EventOccurrence, groupID uint) {
	t.Helper()
	s.ac.holdNotices = true
	groupID = createGroupAs(t, s.ac, "Garden Club", models.VisibilityPublic)
	ev := scheduleGroupEvent(t, s.ac, groupID, "Picnic")
	if err := s.app.DB.Where("event_id = ?", ev.ID).First(&occ).Error; err != nil {
		t.Fatal(err)
	}
	return ev.ID, occ, groupID
}

func erasureInvitationRow(t *testing.T, db *gorm.DB, id uint) models.Invitation {
	t.Helper()
	var inv models.Invitation
	if err := db.First(&inv, id).Error; err != nil {
		t.Fatal(err)
	}
	return inv
}

// TestAccountErasureAnonymizesInvitations: T17 cases 1-6 through the real
// DELETE. Every INV row loses address, phone, name and token and keeps its
// inviter and acceptance; an open one is revoked. The excluded rows are
// byte-identical.
func TestAccountErasureAnonymizesInvitations(t *testing.T) {
	s := newCoreErasureScene(t)
	s.ac.holdNotices = true
	db := s.app.DB
	const a0, a = "invanon.old@x.test", "invanon.now@x.test"
	u := newTestUser(t, s.app, a, erasurePW, models.RoleMember)
	other := newTestUser(t, s.app, "invanon-other@x.test", erasurePW, models.RoleMember)
	inviter := s.admin

	// (1) accepted at A0, the address changed to A since.
	i1 := erasureInvitation(t, db, inviter.ID, a0, true, nil)
	erasureAcceptedAudit(t, db, i1, fmt.Sprintf(`{"userId":%d}`, u.ID))
	// (4) open at the current address.
	i4 := erasureInvitation(t, db, inviter.ID, a, false, nil)
	// (5) a completed conversion: the request carries one invitation, the
	// person another.
	hh := models.Household{Name: "Invitation family", CreatedBy: inviter.ID}
	erasureMustCreate(t, db, &hh)
	p1 := models.HouseholdPerson{HouseholdID: hh.ID, FirstName: "Done", Kind: models.HouseholdAdult, CreatedBy: inviter.ID, ConvertedUserID: &u.ID}
	erasureMustCreate(t, db, &p1)
	iB := erasureInvitation(t, db, inviter.ID, "invanon.conv1@x.test", true, nil)
	iC := erasureInvitation(t, db, inviter.ID, "invanon.conv2@x.test", true, &p1.ID)
	erasureRequest(t, db, "invanon.conv1@x.test", models.RequestHouseholdConversion, models.RequestApproved, &u.ID, &p1.ID, &iB)
	// Excluded: (2) another account took A0 and accepted its own; (3) accepted
	// with no userId, accepted at A with no audit row, open at the past
	// address; (6) one the person sent and one to a third address.
	i2 := erasureInvitation(t, db, inviter.ID, a0, true, nil)
	erasureAcceptedAudit(t, db, i2, fmt.Sprintf(`{"userId":%d}`, other.ID))
	i3 := erasureInvitation(t, db, inviter.ID, a, true, nil)
	erasureAcceptedAudit(t, db, i3, `{}`)
	i3b := erasureInvitation(t, db, inviter.ID, a, true, nil)
	i3c := erasureInvitation(t, db, inviter.ID, a0, false, nil)
	i5 := erasureInvitation(t, db, u.ID, "invanon.third@x.test", false, nil)
	i6 := erasureInvitation(t, db, inviter.ID, "invanon.third@x.test", false, nil)
	all := []uint{i1, i4, iB, iC, i2, i3, i3b, i3c, i5, i6}
	if err := db.Exec("UPDATE invitations SET phone = '555-0100', name = 'Real Name' WHERE id IN ?", all).Error; err != nil {
		t.Fatal(err)
	}
	excluded := []uint{i2, i3, i3b, i3c, i5, i6}
	before := dumpRows(t, db, "invitations", "id", "id IN ?", excluded)

	s.erase(t, u.ID)

	if got := dumpRows(t, db, "invitations", "id", "id IN ?", excluded); got != before {
		t.Errorf("excluded invitations changed:\nbefore %s\nafter  %s", before, got)
	}
	for _, c := range []struct {
		id       uint
		accepted bool
	}{{i1, true}, {i4, false}, {iB, true}, {iC, true}} {
		got := erasureInvitationRow(t, db, c.id)
		if got.Email != fmt.Sprintf("deleted-invitations-%d@deleted.invalid", c.id) || got.Phone != "" || got.Name != "" ||
			got.TokenHash != fmt.Sprintf("erased-invitations-%d", c.id) {
			t.Errorf("invitation %d keeps personal data: %+v", c.id, got)
		}
		if got.InvitedBy != inviter.ID || (got.AcceptedAt != nil) != c.accepted || (got.RevokedAt != nil) == c.accepted {
			t.Errorf("invitation %d: invited_by %d accepted %v revoked %v, want accepted=%v and revoked only when it was open",
				c.id, got.InvitedBy, got.AcceptedAt != nil, got.RevokedAt != nil, c.accepted)
		}
	}
}

func TestAccountErasureInvitationOwnershipAfterReuse(t *testing.T) {
	s := newCoreErasureScene(t)
	s.ac.holdNotices = true
	db := s.app.DB
	const oldAddress = "reuse.previous@x.test"
	first := newTestUser(t, s.app, "reuse.current@x.test", erasurePW, models.RoleMember)
	later := newTestUser(t, s.app, oldAddress, erasurePW, models.RoleMember)
	prior := erasureInvitation(t, db, s.admin.ID, oldAddress, true, nil)
	erasureAcceptedAudit(t, db, prior, fmt.Sprintf(`{"userId":%d}`, first.ID))
	current := erasureInvitation(t, db, s.admin.ID, oldAddress, true, nil)
	erasureAcceptedAudit(t, db, current, fmt.Sprintf(`{"userId":%d}`, later.ID))
	before := dumpRows(t, db, "invitations", "id", "id = ?", prior)
	s.erase(t, later.ID)
	if got := dumpRows(t, db, "invitations", "id", "id = ?", prior); got != before {
		t.Errorf("prior holder's invitation changed: before %s, after %s", before, got)
	}
	got := erasureInvitationRow(t, db, current)
	if got.Email != fmt.Sprintf("deleted-invitations-%d@deleted.invalid", current) || got.TokenHash != fmt.Sprintf("erased-invitations-%d", current) {
		t.Errorf("later holder's invitation was not anonymized: %+v", got)
	}
}

func TestAccountErasureStagedConversionInvitation(t *testing.T) {
	s := newCoreErasureScene(t)
	s.ac.holdNotices = true
	db := s.app.DB
	u := newTestUser(t, s.app, "staged.erase@x.test", erasurePW, models.RoleMember)
	hh := models.Household{Name: "Staged family", CreatedBy: s.admin.ID}
	erasureMustCreate(t, db, &hh)
	person := models.HouseholdPerson{HouseholdID: hh.ID, FirstName: "Staged", Kind: models.HouseholdTeen, CreatedBy: s.admin.ID}
	erasureMustCreate(t, db, &person)
	if err := db.Delete(&person).Error; err != nil {
		t.Fatal(err)
	}
	id := erasureInvitation(t, db, s.admin.ID, "staged.invite@x.test", true, &person.ID)
	req := erasureRequest(t, db, "staged.invite@x.test", models.RequestHouseholdConversion, models.RequestApproved, &u.ID, &person.ID, &id)
	before := erasureInvitationRow(t, db, id)
	s.erase(t, u.ID)
	after := erasureInvitationRow(t, db, id)
	if after.Email != fmt.Sprintf("deleted-invitations-%d@deleted.invalid", id) || after.TokenHash != fmt.Sprintf("erased-invitations-%d", id) || after.Name != "" || after.Phone != "" {
		t.Errorf("staged invitation was not anonymized: %+v", after)
	}
	if after.InvitedBy != before.InvitedBy || after.HouseholdPersonID == nil || *after.HouseholdPersonID != person.ID || after.AcceptedAt == nil || before.AcceptedAt == nil || !after.AcceptedAt.Equal(*before.AcceptedAt) {
		t.Errorf("staged invitation lost links or acceptance: before %+v, after %+v", before, after)
	}
	request := erasureRequestRow(t, db, req)
	if request.ApprovedUserID == nil || *request.ApprovedUserID != u.ID || request.HouseholdPersonID == nil || *request.HouseholdPersonID != person.ID || request.InvitationID == nil || *request.InvitationID != id {
		t.Errorf("staged request lost its links: %+v", request)
	}
}

// TestAccountErasureGuestSignupsAndOutbox: a pending signup at the address is
// deleted, a confirmed one is anonymized and keeps its occurrence and times,
// outbox rows to the address go in any case or spacing, and other addresses
// are untouched.
func TestAccountErasureGuestSignupsAndOutbox(t *testing.T) {
	s := newCoreErasureScene(t)
	db := s.app.DB
	_, occ, _ := erasureEventFixture(t, s)
	const a, b = "gsout.guest@x.test", "gsout.other@x.test"
	guest := models.User{Email: a, Name: "Guest Person", Role: models.RoleMember, Status: models.UserGuest}
	erasureMustCreate(t, db, &guest)
	confirmed := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	signup := func(email string, at *time.Time, hash string) models.GuestSignup {
		g := models.GuestSignup{Name: "Signup Name", Email: email, OccurrenceID: occ.ID, TokenHash: hash, ExpiresAt: time.Now().Add(time.Hour).Truncate(time.Microsecond),
			ConfirmedAt: at, IP: "203.0.113.5", Timezone: "America/Boise"}
		erasureMustCreate(t, db, &g)
		return g
	}
	pend := signup(a, nil, "gsout-pending-a")
	conf := signup(a, &confirmed, "gsout-confirmed-a")
	otherPend := signup(b, nil, "gsout-pending-b")
	otherConf := signup(b, &confirmed, "gsout-confirmed-b")
	for _, to := range []string{a, "  GSOUT.Guest@X.test ", b} {
		erasureMustCreate(t, db, &models.MailOutbox{ToAddress: to, Subject: "hello", Body: "body"})
	}
	untouched := func() string {
		return dumpRows(t, db, "guest_signups", "id", "id IN ?", []uint{otherPend.ID, otherConf.ID}) +
			dumpRows(t, db, "mail_outboxes", "id", "to_address = ?", b)
	}
	before := untouched()

	s.erase(t, guest.ID)

	if got := untouched(); got != before {
		t.Errorf("other addresses changed:\nbefore %s\nafter  %s", before, got)
	}
	if n := countRows(t, db, "guest_signups", "id = ?", pend.ID); n != 0 {
		t.Error("the pending signup survived")
	}
	var got models.GuestSignup
	if err := db.First(&got, conf.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Email != fmt.Sprintf("deleted-guest_signups-%d@deleted.invalid", conf.ID) || got.TokenHash != fmt.Sprintf("erased-guest_signups-%d", conf.ID) ||
		got.Name != "" || got.IP != "" || got.Timezone != "" {
		t.Errorf("confirmed signup keeps personal data: %+v", got)
	}
	if got.OccurrenceID != occ.ID || got.ConfirmedAt == nil || !got.ConfirmedAt.Equal(confirmed) || !got.ExpiresAt.Equal(conf.ExpiresAt) {
		t.Errorf("confirmed signup lost its occurrence or times: %+v", got)
	}
	if n := countRows(t, db, "mail_outboxes", "lower(btrim(to_address)) = ?", a); n != 0 {
		t.Errorf("%d outbox rows to the erased address remain", n)
	}
}

// TestQueuedDependentMailForErasedAccountSendsNothing follows
// TestQueuedAccountMailForErasedAccountSendsNothing for the classes R4-2a
// owns: invitation, guest verification and account-request received, rejected
// and conversion_approved. The same limit applies, scoped by timing: this
// proves only preparations that start after the erasure commits. A
// preparation that committed before the erasure may still have rotated a
// capability and sent to the captured address.
func TestQueuedDependentMailForErasedAccountSendsNothing(t *testing.T) {
	s := newCoreErasureScene(t)
	s.ac.holdNotices = true
	db := s.app.DB
	eventID, occ, _ := erasureEventFixture(t, s)
	_ = eventID
	u := newTestUser(t, s.app, "queued-dep-u", erasurePW, models.RoleMember)
	addr := mail.NormalizeAddress(u.Email)
	expires := time.Now().Add(24 * time.Hour)

	invID := erasureInvitation(t, db, s.admin.ID, addr, false, nil)
	signup := models.GuestSignup{Name: "Queued Guest", Email: addr, OccurrenceID: occ.ID, TokenHash: "queued-dep-signup", ExpiresAt: expires, Timezone: "UTC"}
	erasureMustCreate(t, db, &signup)
	received := erasureSeedRequest(t, db, models.AccountRequest{Name: "Received", Email: addr, Source: models.RequestSelfSignup, Status: models.RequestPending, ExpiresAt: expires})
	rejected := erasureSeedRequest(t, db, models.AccountRequest{Name: "Rejected", Email: addr, Source: models.RequestSelfSignup, Status: models.RequestRejected, DecisionNote: "no", ExpiresAt: expires})
	conversion := erasureSeedRequest(t, db, models.AccountRequest{Name: "Converted", Email: addr, Source: models.RequestHouseholdConversion, Status: models.RequestApproved, ExpiresAt: expires})

	type preparation struct {
		name string
		run  func(tx *gorm.DB) (mail.Message, bool, error)
	}
	preparations := []preparation{
		{"invitation", func(tx *gorm.DB) (mail.Message, bool, error) { return s.app.prepareInvitationMail(tx, invID) }},
		{"guest verification", func(tx *gorm.DB) (mail.Message, bool, error) { return s.app.prepareGuestVerification(tx, signup.ID) }},
		{"request received", func(tx *gorm.DB) (mail.Message, bool, error) {
			return s.app.prepareAccountRequestMail(tx, received.ID, requestMailReceived)
		}},
		{"request rejected", func(tx *gorm.DB) (mail.Message, bool, error) {
			return s.app.prepareAccountRequestMail(tx, rejected.ID, requestMailRejected)
		}},
		{"request conversion approved", func(tx *gorm.DB) (mail.Message, bool, error) {
			return s.app.prepareAccountRequestMail(tx, conversion.ID, requestMailConversionApproved)
		}},
	}
	// Each is deliverable before the erasure; the rollback keeps any minted
	// or rotated capability from being committed.
	for _, p := range preparations {
		tx := db.Begin()
		_, eligible, err := p.run(tx)
		tx.Rollback()
		if err != nil || !eligible {
			t.Fatalf("%s must be eligible before erasure: eligible=%v err=%v", p.name, eligible, err)
		}
	}
	// holdNotices is set above, before the jobs are queued; nothing drains
	// them, only the prepares below run.
	for _, job := range []struct {
		kind    string
		payload any
	}{
		{kindInvitationMail, invitationMailJob{InvitationID: invID}},
		{kindGuestVerifyMail, guestVerifyMailJob{GuestSignupID: signup.ID}},
		{kindAccountRequestMail, requestMailJob{RequestID: received.ID, Case: requestMailReceived}},
		{kindAccountRequestMail, requestMailJob{RequestID: rejected.ID, Case: requestMailRejected}},
		{kindAccountRequestMail, requestMailJob{RequestID: conversion.ID, Case: requestMailConversionApproved}},
	} {
		if _, err := jobs.Enqueue(db, job.kind, job.payload, time.Now(), "", directMailAttempts); err != nil {
			t.Fatal(err)
		}
	}

	s.erase(t, u.ID)

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
}

// TestInvitationAcceptanceRacingErasure: acceptance and the erasure serialize
// on the contact gate. Acceptance first, the erasure waits and then
// anonymizes the accepted invitation (found through its audit userId);
// erasure first, acceptance finds no live invitation and creates no account.
func TestInvitationAcceptanceRacingErasure(t *testing.T) {
	for _, order := range []string{"acceptance-first", "erasure-first"} {
		t.Run(order, func(t *testing.T) {
			s := newCoreErasureScene(t)
			s.ac.holdNotices = true
			app := s.app
			const address = "race.guest@x.test"
			guest := models.User{Email: address, Name: "Race Guest", Role: models.RoleMember, Status: models.UserGuest}
			erasureMustCreate(t, app.DB, &guest)
			const raw = "invitation-racing-an-erasure"
			inv := models.Invitation{Email: address, Name: "Race Guest", InvitedBy: s.admin.ID, TokenHash: hashToken(raw),
				Role: "member", AccountType: "adult", ExpiresAt: time.Now().UTC().Add(time.Hour)}
			erasureMustCreate(t, app.DB, &inv)
			var users0 int64
			app.DB.Unscoped().Model(&models.User{}).Count(&users0)

			states := erasureTestSQLStates(t, app)
			anon := anonClient(t, app, "10.0.0.88")
			anon.holdNotices = true
			accept := func() <-chan httpResult {
				return bgDo(anon, "POST", "/api/v1/invitations/accept", map[string]string{
					"token": raw, "name": "Race Guest", "password": erasurePW, "ageBand": "adult"})
			}
			delPath := "/api/v1/admin/users/" + itoa(guest.ID)
			gate := "%pg_advisory_xact_lock%"
			var acceptCode, deleteCode int
			var acceptBody string
			switch order {
			case "acceptance-first":
				var acceptPID atomic.Int64
				reached, release := erasureTestPause(t, app.DB, erasureTestPoint{Processor: "update", Step: "gorm:update", Table: "invitations", PID: &acceptPID})
				acc := accept()
				awaitDBSignal(t, reached) // acceptance holds the gate and has not claimed the invitation
				del := bgDo(s.ac, "DELETE", delPath, nil)
				erasurePID := waitForLockedUsersQuery(t, app, gate)
				waitDBBlocked(t, app.DB, erasurePID, int(acceptPID.Load()))
				release()
				a := awaitResult(t, acc)
				d := awaitResult(t, del)
				acceptCode, acceptBody, deleteCode = a.code, a.body, d.code
				if acceptCode != http.StatusOK {
					t.Fatalf("accept: %d %s", acceptCode, acceptBody)
				}
				got := erasureInvitationRow(t, app.DB, inv.ID)
				if got.Email != fmt.Sprintf("deleted-invitations-%d@deleted.invalid", inv.ID) || got.TokenHash != fmt.Sprintf("erased-invitations-%d", inv.ID) ||
					got.AcceptedAt == nil || got.RevokedAt != nil {
					t.Errorf("the accepted invitation was not anonymized in place: %+v", got)
				}
			case "erasure-first":
				var erasurePID atomic.Int64
				p := erasureTestBeforeNotificationsDelete(guest.ID)
				p.PID = &erasurePID
				reached, release := erasureTestPause(t, app.DB, p)
				del := bgDo(s.ac, "DELETE", delPath, nil)
				awaitDBSignal(t, reached)
				acc := accept()
				acceptPID := waitForLockedUsersQuery(t, app, gate)
				waitDBBlocked(t, app.DB, acceptPID, int(erasurePID.Load()))
				release()
				d := awaitResult(t, del)
				a := awaitResult(t, acc)
				acceptCode, acceptBody, deleteCode = a.code, a.body, d.code
				if acceptCode != http.StatusBadRequest || !strings.Contains(acceptBody, "that invitation is invalid or has expired") {
					t.Errorf("accept after the erasure: %d %s, want the invalid-invitation answer", acceptCode, acceptBody)
				}
				if got := erasureInvitationRow(t, app.DB, inv.ID); got.RevokedAt == nil || got.AcceptedAt != nil {
					t.Errorf("the open invitation was not revoked: %+v", got)
				}
			}
			if deleteCode != http.StatusOK {
				t.Fatalf("delete: %d", deleteCode)
			}
			var users1 int64
			app.DB.Unscoped().Model(&models.User{}).Count(&users1)
			if users1 != users0 {
				t.Errorf("users: %d, was %d; an account was created", users1, users0)
			}
			if n := countRows(t, app.DB, "users", "email = ?", address); n != 0 {
				t.Errorf("%d users still hold the erased address", n)
			}
			if n := states.Count("40P01"); n != 0 {
				t.Errorf("%d deadlocks recorded", n)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// R4-2a-4: messages and the bounded recipient scrub.
// ---------------------------------------------------------------------------

// erasureTestRegister and erasureTestUnregister attach a callback to the
// processor and position a point names.
func erasureTestRegister(db *gorm.DB, p erasureTestPoint, name string, fn func(*gorm.DB)) error {
	switch p.Processor {
	case "query":
		return erasureTestRegisterQuery(db, p, name, fn)
	case "row":
		return erasureTestRegisterRow(db, p, name, fn)
	case "raw":
		return erasureTestRegisterRaw(db, p, name, fn)
	case "create":
		return erasureTestRegisterCreate(db, p, name, fn)
	case "update":
		return erasureTestRegisterUpdate(db, p, name, fn)
	}
	return erasureTestRegisterDelete(db, p, name, fn)
}

func erasureTestUnregister(db *gorm.DB, p erasureTestPoint, name string) {
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
	default:
		_ = db.Callback().Delete().Remove(name)
	}
}

// erasureTestProbe stores pg_backend_pid() of the first statement that
// matches p, read through the statement's own connection, and never blocks it.
func erasureTestProbe(t *testing.T, db *gorm.DB, p erasureTestPoint) *atomic.Int64 {
	t.Helper()
	if err := p.validate(); err != nil {
		t.Fatalf("erasureTestProbe: %v", err)
	}
	var pid atomic.Int64
	var fired atomic.Bool
	name := fmt.Sprintf("test:erasure_probe:%d", queryCounterSeq.Add(1))
	fn := func(tx *gorm.DB) {
		if tx.Error != nil || !p.matches(tx.Statement) || fired.Swap(true) {
			return
		}
		var got int64
		if err := tx.Statement.ConnPool.QueryRowContext(tx.Statement.Context, "SELECT pg_backend_pid()").Scan(&got); err != nil {
			tx.AddError(err)
			return
		}
		pid.Store(got)
	}
	if err := erasureTestRegister(db, p, name, fn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { erasureTestUnregister(db, p, name) })
	return &pid
}

// erasureTestWorkerPID records the backend of the message worker that locks
// recipient recipientID. deliverMessageBatchOnce takes that lock with
// tx.Raw("SELECT id FROM message_recipients WHERE id = ? AND status = ? FOR
// UPDATE SKIP LOCKED", row.ID, ...).Scan(&held); Raw builds its SQL and
// variables before gorm:row, so the probe matches that worker's own row
// before the lock is taken (F6, F7).
func erasureTestWorkerPID(t *testing.T, db *gorm.DB, recipientID uint) *atomic.Int64 {
	t.Helper()
	return erasureTestProbe(t, db, erasureTestPoint{Processor: "row", Step: "gorm:row",
		SQLContains: "FOR UPDATE SKIP LOCKED", VarAt: map[int]any{0: recipientID}})
}

const erasureRecipientScrubPrefix = "UPDATE message_recipients SET address"

// erasureTestStatementWait waits for the erasure's recipient scrub to wait on
// a lock and returns its backend and the statement's pg_stat_activity
// query_start. Only that statement has this text.
func erasureTestStatementWait(t *testing.T, app *App) (pid int, queryStart time.Time) {
	t.Helper()
	pid = waitForLockedUsersQuery(t, app, erasureRecipientScrubPrefix+"%")
	if err := app.DB.Raw("SELECT query_start FROM pg_stat_activity WHERE pid = ?", pid).Scan(&queryStart).Error; err != nil {
		t.Fatal(err)
	}
	return pid, queryStart
}

// erasureTestWaitBlockedOnlyBy polls every millisecond until the waiter is in
// the same statement execution (same query_start), active, waiting on a lock
// and blocked by exactly blocker. It returns when that first held.
func erasureTestWaitBlockedOnlyBy(t *testing.T, app *App, waiter int, queryStart time.Time, blocker int) time.Time {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		var n int
		if err := app.DB.Raw(`SELECT count(*) FROM pg_stat_activity a
			WHERE a.pid = ? AND a.state = 'active' AND a.wait_event_type = 'Lock' AND a.query_start = ?
			AND a.query LIKE ? AND pg_blocking_pids(a.pid) = ARRAY[?]::int[]`,
			waiter, queryStart, erasureRecipientScrubPrefix+"%", blocker).Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n == 1 {
			return time.Now()
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatalf("session %d was never blocked only by session %d in the statement started at %v", waiter, blocker, queryStart)
		}
	}
}

// erasureTestSinceQueryStart is clock_timestamp() - queryStart read on the
// database clock, so no test-process clock is compared with query_start.
func erasureTestSinceQueryStart(t *testing.T, app *App, queryStart time.Time) time.Duration {
	t.Helper()
	var secs float64
	if err := app.DB.Raw("SELECT extract(epoch FROM clock_timestamp() - ?::timestamptz)", queryStart).Scan(&secs).Error; err != nil {
		t.Fatal(err)
	}
	return time.Duration(secs * float64(time.Second))
}

// erasureTestFailThrottleDelete makes the erasure's last statement fail, to
// show that everything before it rolls back.
func erasureTestFailThrottleDelete(t *testing.T, db *gorm.DB) (stop func()) {
	t.Helper()
	name := fmt.Sprintf("test:fail_throttle_delete:%d", queryCounterSeq.Add(1))
	if err := db.Callback().Raw().Before("gorm:raw").Register(name, func(tx *gorm.DB) {
		if strings.HasPrefix(tx.Statement.SQL.String(), "DELETE FROM login_throttles") {
			tx.AddError(errors.New("injected throttle failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	stop = func() { once.Do(func() { _ = db.Callback().Raw().Remove(name) }) }
	t.Cleanup(stop)
	return stop
}

const (
	erasureSeedSubject1 = "Erasure wait one"
	erasureSeedSubject2 = "Erasure wait two"
)

// erasureRecipientSeed is the fixture of the bounded-wait tests: the erased
// member U at one address, an author P, the deleting admin, and two sending
// messages by P, each with one queued email row for U (R1 inserted first),
// plus rows of U in the other tables the erasure writes.
type erasureRecipientSeed struct {
	s       coreErasureScene
	u, p    *models.User
	address string
	m1, m2  models.Message
	r1, r2  models.MessageRecipient
	labels  map[string]map[uint]string
}

func (e *erasureRecipientSeed) key1() string {
	return erasureTestSendKey(e.address, erasureSeedSubject1)
}
func (e *erasureRecipientSeed) key2() string {
	return erasureTestSendKey(e.address, erasureSeedSubject2)
}

func erasureTestRecipientSeed(t *testing.T, app *App) *erasureRecipientSeed {
	t.Helper()
	e := &erasureRecipientSeed{}
	e.s = coreErasureScene{app: app}
	e.s.admin = newTestUser(t, app, "erase-admin", erasurePW, models.RoleAdmin)
	e.s.second = newTestUser(t, app, "erase-second", erasurePW, models.RoleAdmin)
	e.s.ac = loginAPI(t, app, "erase-admin", erasurePW)
	e.s.ac.holdNotices = true
	db := app.DB
	e.u = newTestUser(t, app, "rs-u", erasurePW, models.RoleMember)
	e.p = newTestUser(t, app, "rs-author", erasurePW, models.RoleMember)
	e.address = mail.NormalizeAddress(e.u.Email)
	now := time.Now().Add(-time.Minute)
	message := func(subject string) models.Message {
		m := models.Message{AuthorID: &e.p.ID, Status: models.MessageSending, Subject: subject, BodyHTML: "<p>Hi {{first_name}}</p>",
			BodyText: "Hi", ScheduledAt: &now, RecipientCount: 1}
		erasureMustCreate(t, db, &m)
		return m
	}
	row := func(m models.Message) models.MessageRecipient {
		r := models.MessageRecipient{MessageID: m.ID, UserID: e.u.ID, Channel: models.DeliveryEmail, Address: e.address, Status: models.RecipientQueued}
		erasureMustCreate(t, db, &r)
		return r
	}
	e.m1, e.m2 = message(erasureSeedSubject1), message(erasureSeedSubject2)
	e.r1 = row(e.m1)
	e.r2 = row(e.m2)

	// U's rows in the other tables the erasure writes.
	_, occ, _ := erasureEventFixture(t, e.s)
	expires := time.Now().Add(48 * time.Hour)
	erasureMustCreate(t, db, &models.Notification{UserID: e.u.ID, Kind: "test.note", Category: "groups", Title: "note", Link: "/"})
	req := erasureSeedRequest(t, db, models.AccountRequest{Name: "Seed Requester", Email: e.address, Source: models.RequestSelfSignup,
		Status: models.RequestPending, ExpiresAt: expires})
	erasureSeedTasks(t, db, &req)
	invID := erasureInvitation(t, db, e.s.admin.ID, e.address, false, nil)
	signup := models.GuestSignup{Name: "Seed Guest", Email: e.address, OccurrenceID: occ.ID, TokenHash: "erasure-seed-signup",
		ExpiresAt: expires, IP: "203.0.113.7", Timezone: "UTC"}
	erasureMustCreate(t, db, &signup)
	erasureMustCreate(t, db, &models.MailOutbox{ToAddress: e.address, Subject: "seed", Body: "seed"})
	seedThrottleRows(t, db, ownedThrottleKeys(e.u.ID, e.address))
	seedErasureTokens(t, db, e.u.ID, "rs", "rs.pending@example.com")
	// Credentials and a registration of U, so the comparison covers every
	// kind of row an erasure deletes or scrubs.
	seedErasureCalendar(t, db, e.u.ID, "rs-calendar")
	newPushDevice(t, app, e.u, "rs-phone")
	erasureMustCreate(t, db, &models.Registration{UserID: &e.u.ID, OccurrenceID: occ.ID, Status: models.RegGoing,
		Source: models.RegSourceGuestForm, AttendanceMode: models.AttendanceInPerson, ManageTokenHash: "rs-manage-token-hash"})
	e.labels = map[string]map[uint]string{
		"user":       {e.u.ID: "U", e.p.ID: "P", e.s.admin.ID: "admin", e.s.second.ID: "second"},
		"message":    {e.m1.ID: "M1", e.m2.ID: "M2"},
		"recipient":  {e.r1.ID: "R1", e.r2.ID: "R2"},
		"request":    {req.ID: "REQ"},
		"invitation": {invID: "INV"},
		"signup":     {signup.ID: "SIGNUP"},
	}
	return e
}

func erasureTestIsTimeKey(k string) bool {
	return k == "at" || strings.HasSuffix(k, "_at") || strings.HasSuffix(k, "_until") ||
		strings.HasSuffix(k, "_start") || strings.HasSuffix(k, "_for")
}

// erasureTestIsHashKey: token hashes and the push device's key material,
// random per run, are compared by class only (none, empty, an erasure marker,
// or set).
func erasureTestIsHashKey(k string) bool {
	return strings.HasSuffix(k, "_hash") || k == "p256dh" || k == "auth"
}

func erasureTestHashClass(s string) string {
	switch {
	case s == "":
		return "empty"
	case strings.HasPrefix(s, "erased-"):
		return "marker"
	}
	return "set"
}

// erasureTestRefNamespace is the explicit mapping from a column or JSON key to
// the namespace of the generated ids it holds. Only these keys are relabelled;
// any other number or text is compared exactly.
var erasureTestRefNamespace = map[string]string{
	"user_id": "user", "author_id": "user", "actor_id": "user", "invited_by": "user", "decided_by": "user",
	"requested_by": "user", "copy_of_user_id": "user", "approved_user_id": "user", "registered_by": "user",
	"removed_by": "user", "userId": "user",
	"message_id": "message", "messageId": "message",
	"messageRecipientId": "recipient", "recipientIds": "recipient", "recipient_id": "recipient",
	"requestId": "request", "invitationId": "invitation", "guestSignupId": "signup",
}

// erasureTestTargetNamespace maps an audit row's target_type, and a
// notification's subject_type, to the namespace of its target_id or
// subject_id; other types keep their number.
var erasureTestTargetNamespace = map[string]string{
	"message": "message", "user": "user", "account_request": "request", "invitation": "invitation", "guest_signup": "signup",
}

func erasureTestLabelID(ns string, id float64, labels map[string]map[uint]string) any {
	if l, ok := labels[ns][uint(id)]; ok && ns != "" {
		return ns + ":" + l
	}
	return id
}

// erasureTestProjectField reduces one column or JSON value. ns is the
// namespace of a polymorphic id column (target_id, subject_id) already
// resolved from its row.
func erasureTestProjectField(key, ns string, v any, labels map[string]map[uint]string) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = erasureTestProjectField(k, "", val, labels)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = erasureTestProjectField(key, ns, x[i], labels)
		}
		return out
	case float64:
		if ns == "" {
			ns = erasureTestRefNamespace[key]
		}
		if ns != "" {
			return erasureTestLabelID(ns, x, labels)
		}
		return x
	case string:
		switch {
		case erasureTestIsTimeKey(key):
			return x != ""
		case erasureTestIsHashKey(key):
			return erasureTestHashClass(x)
		}
		return x
	case nil:
		switch {
		case erasureTestIsTimeKey(key):
			return false
		case erasureTestIsHashKey(key):
			return "none"
		}
	}
	return v
}

// erasureTestProjectRow drops the row's own sequence id (the row is told
// apart by its natural key) and reduces the rest.
func erasureTestProjectRow(row map[string]any, labels map[string]map[uint]string) map[string]any {
	targetType, _ := row["target_type"].(string)
	subjectType, _ := row["subject_type"].(string)
	out := make(map[string]any, len(row))
	for k, val := range row {
		switch k {
		case "id":
			continue
		case "target_id":
			out[k] = erasureTestProjectField(k, erasureTestTargetNamespace[targetType], val, labels)
		case "subject_id":
			out[k] = erasureTestProjectField(k, erasureTestTargetNamespace[subjectType], val, labels)
		default:
			out[k] = erasureTestProjectField(k, "", val, labels)
		}
	}
	return out
}

// erasureTestProjectionTables reads committed state: every row of the tables
// the erasure, the message workers and the seed touch, with sequence ids
// dropped, the ids the seed named replaced by their labels, timestamps reduced
// to set or null, hashes to their class, and everything else (ordinary text,
// addresses, ips, provider ids, job kinds, statuses, attempts, errors and
// labelled job payloads) exactly as stored. Each table's rows are sorted by
// their own projection, so two runs of the same seed compare equal whatever
// their sequence ids and clocks.
func erasureTestProjectionTables(t *testing.T, db *gorm.DB, labels map[string]map[uint]string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, table := range []string{"users", "memberships", "messages", "message_recipients", "audit_logs", "account_requests",
		"notifications", "notification_deliveries", "invitations", "guest_signups", "mail_outboxes", "registrations",
		"login_throttles", "account_tokens", "calendar_tokens", "push_subscriptions", "jobs"} {
		var raw []string
		if err := db.Raw("SELECT to_jsonb(r)::text FROM " + table + " r").Scan(&raw).Error; err != nil {
			t.Fatal(err)
		}
		rows := []string{}
		for _, text := range raw {
			var v map[string]any
			if err := json.Unmarshal([]byte(text), &v); err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(erasureTestProjectRow(v, labels))
			rows = append(rows, string(b))
		}
		sort.Strings(rows)
		out[table] = rows
	}
	return out
}

func erasureTestMessageProjection(t *testing.T, db *gorm.DB, labels map[string]map[uint]string) string {
	t.Helper()
	b, _ := json.Marshal(erasureTestProjectionTables(t, db, labels))
	return string(b)
}

// erasureWaitRun is one subtest's world: its own seed on a freshly cleared
// database, a blocking sender, SQLSTATE recording and a worker runner.
type erasureWaitRun struct {
	t      *testing.T
	app    *App
	seed   *erasureRecipientSeed
	sender *erasureTestBlockingSender
	states *erasureTestStateLog
	ctx    context.Context
	wg     sync.WaitGroup
}

func newErasureWaitRun(t *testing.T) *erasureWaitRun {
	t.Helper()
	app := testApp(t)
	r := &erasureWaitRun{t: t, app: app}
	r.seed = erasureTestRecipientSeed(t, app)
	r.states = erasureTestSQLStates(t, app)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.ctx = ctx
	t.Cleanup(func() {
		done := make(chan struct{})
		go func() { r.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("a message worker did not stop")
		}
	})
	r.sender = newErasureTestBlockingSender(t, r.seed.key1(), r.seed.key2())
	app.Mailer = r.sender
	return r
}

// worker runs deliverMessageBatchOnce for m's one row on its own goroutine.
func (r *erasureWaitRun) worker(m models.Message, rowID uint) <-chan error {
	ch := make(chan error, 1)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ch <- r.app.deliverMessageBatchOnce(r.ctx, messageBatchJob{MessageID: m.ID, RecipientIDs: []uint{rowID}})
	}()
	return ch
}

func (r *erasureWaitRun) erase() <-chan httpResult {
	return bgDo(r.seed.s.ac, "DELETE", "/api/v1/admin/users/"+itoa(r.seed.u.ID), nil)
}

func (r *erasureWaitRun) rowStatus(id uint) models.MessageRecipient {
	r.t.Helper()
	var row models.MessageRecipient
	if err := r.app.DB.First(&row, id).Error; err != nil {
		r.t.Fatal(err)
	}
	return row
}

// awaitStatus polls from a separate session until the row commits status.
func (r *erasureWaitRun) awaitStatus(id uint, status string) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r.rowStatus(id).Status == status {
			return
		}
		time.Sleep(time.Millisecond)
	}
	r.t.Fatalf("recipient %d never became %s", id, status)
}

func (r *erasureWaitRun) awaitWorker(ch <-chan error) {
	r.t.Helper()
	select {
	case err := <-ch:
		if err != nil {
			r.t.Fatalf("worker: %v", err)
		}
	case <-time.After(10 * time.Second):
		r.t.Fatal("worker never finished")
	}
}

func (r *erasureWaitRun) projection() string {
	r.t.Helper()
	return erasureTestMessageProjection(r.t, r.app.DB, r.seed.labels)
}

// assertNoErasureWrites checks that no erasure write survived (a failed or
// absent erasure): no user.deleted row, no avatar cleanup job, no message
// cancellation.
func (r *erasureWaitRun) assertNoErasureWrites() {
	r.t.Helper()
	if n := auditCount(r.t, r.app, audit.UserDeleted); n != 0 {
		r.t.Errorf("%d user.deleted rows", n)
	}
	if n := auditCount(r.t, r.app, audit.MessageCancelled); n != 0 {
		r.t.Errorf("%d message.cancelled rows", n)
	}
	if n := countRows(r.t, r.app.DB, "jobs", "kind = ?", kindAvatarCleanup); n != 0 {
		r.t.Errorf("%d avatar.cleanup jobs", n)
	}
}

func (r *erasureWaitRun) assertCleanRun() {
	r.t.Helper()
	if n := r.states.Count("40P01"); n != 0 {
		r.t.Errorf("%d deadlocks recorded", n)
	}
	if others := r.sender.Others(); len(others) != 0 {
		r.t.Errorf("%d messages that matched no key were sent", len(others))
	}
}

// erasureWaitSnapshot is everything an unfinished erasure must leave alone.
func erasureWaitSnapshot(t *testing.T, db *gorm.DB) string {
	t.Helper()
	return dumpRows(t, db, "users", "id", "true") + dumpRows(t, db, "audit_logs", "id", "true") +
		dumpRows(t, db, "account_requests", "id", "true") + dumpRows(t, db, "notifications", "id", "true") +
		dumpRows(t, db, "invitations", "id", "true") + dumpRows(t, db, "message_recipients", "id", "true")
}

func erasureTestAssertTimeout(t *testing.T, r *erasureWaitRun, res httpResult) {
	t.Helper()
	// DEC-52: a failure a person cannot act on reaches the client as the generic
	// server error. The handler's own "could not delete user" text is only
	// logged, and neither it nor the SQLSTATE or statement text reaches the body.
	if res.code != http.StatusInternalServerError || !strings.Contains(res.body, `"code":"server_error"`) {
		t.Fatalf("erasure: %d %s, want the generic 500 server_error", res.code, res.body)
	}
	for _, leak := range []string{"57014", "statement timeout", "message_recipients", "could not delete user"} {
		if strings.Contains(res.body, leak) {
			t.Errorf("the 500 body leaks %q: %s", leak, res.body)
		}
	}
	e := r.states.Only("57014")
	if !strings.HasPrefix(e.SQL, erasureRecipientScrubPrefix) {
		t.Errorf("the statement that timed out is %q, want the message_recipients UPDATE", e.SQL)
	}
	r.states.mu.Lock()
	failures := append([]erasureTestStateEntry(nil), r.states.entries...)
	r.states.mu.Unlock()
	if len(failures) != 1 {
		t.Errorf("unexpected failed statements after the scrub timeout: %v", failures)
	}
}

// TestAccountErasureRecipientWaitIsBounded: the erasure's recipient scrub runs
// under a statement_timeout for the whole statement. A worker holds a queued
// row FOR UPDATE SKIP LOCKED across its provider call; the scrub waits on it.
// Single and staggered waits are proved by backend PIDs, each against a
// worker-only control on an identical seed (F5, F7).
func TestAccountErasureRecipientWaitIsBounded(t *testing.T) {
	old := erasureRecipientWait
	erasureRecipientWait = time.Second
	t.Cleanup(func() { erasureRecipientWait = old })

	t.Run("one row released in time", func(t *testing.T) {
		r := newErasureWaitRun(t)
		w1 := r.worker(r.seed.m1, r.seed.r1.ID)
		awaitDBSignal(t, r.sender.Reached(r.seed.key1()))
		del := r.erase()
		erasureTestStatementWait(t, r.app)
		time.Sleep(200 * time.Millisecond)
		r.sender.Release(r.seed.key1())
		if res := awaitResult(t, del); res.code != http.StatusOK {
			t.Fatalf("erasure: %d %s", res.code, res.body)
		}
		r.awaitWorker(w1)
		got := r.rowStatus(r.seed.r1.ID)
		if got.Status != models.RecipientSent || got.Address != "" || got.Error != "" {
			t.Errorf("R1 after the erasure: %+v", got)
		}
		r.assertCleanRun()
	})

	var control2 string
	t.Run("one row, worker only (2w)", func(t *testing.T) {
		r := newErasureWaitRun(t)
		erasureTestWorkerPID(t, r.app.DB, r.seed.r1.ID)
		w1 := r.worker(r.seed.m1, r.seed.r1.ID)
		awaitDBSignal(t, r.sender.Reached(r.seed.key1()))
		r.sender.Release(r.seed.key1())
		r.awaitWorker(w1)
		control2 = r.projection()
	})

	t.Run("one row never released in time", func(t *testing.T) {
		r := newErasureWaitRun(t)
		w1pid := erasureTestWorkerPID(t, r.app.DB, r.seed.r1.ID)
		w1 := r.worker(r.seed.m1, r.seed.r1.ID)
		awaitDBSignal(t, r.sender.Reached(r.seed.key1()))
		before := erasureWaitSnapshot(t, r.app.DB)
		del := r.erase()
		epid, qs := erasureTestStatementWait(t, r.app)
		observed := erasureTestWaitBlockedOnlyBy(t, r.app, epid, qs, int(w1pid.Load()))
		res := awaitResult(t, del)
		answered := time.Since(observed)
		erasureTestAssertTimeout(t, r, res)
		// Upper bound from the first observation of the wait; the lower bound
		// is the statement's own age on the database clock, with a 250 ms
		// tolerance under the 1 s timeout.
		if answered > 4*time.Second {
			t.Errorf("the erasure answered %v after the wait was first seen, want under 4s", answered)
		}
		if age := erasureTestSinceQueryStart(t, r.app, qs); age < 750*time.Millisecond {
			t.Errorf("the scrub timed out %v after it started, want at least 750ms", age)
		}
		if after := erasureWaitSnapshot(t, r.app.DB); after != before {
			t.Errorf("a failed erasure left a change:\n%s\n%s", before, after)
		}
		r.sender.Release(r.seed.key1())
		r.awaitWorker(w1)
		if got := r.projection(); got != control2 {
			t.Errorf("worker writes differ from the worker-only control:\nrace    %s\ncontrol %s", got, control2)
		}
		r.assertNoErasureWrites()
		if res := awaitResult(t, r.erase()); res.code != http.StatusOK {
			t.Fatalf("retried erasure: %d %s", res.code, res.body)
		}
		if got := r.rowStatus(r.seed.r1.ID); got.Address != "" || got.Status != models.RecipientSent {
			t.Errorf("R1 after the retry: %+v", got)
		}
		r.assertCleanRun()
	})

	var control3 string
	t.Run("staggered rows, workers only (3w)", func(t *testing.T) {
		r := newErasureWaitRun(t)
		w1 := r.worker(r.seed.m1, r.seed.r1.ID)
		awaitDBSignal(t, r.sender.Reached(r.seed.key1()))
		w2 := r.worker(r.seed.m2, r.seed.r2.ID)
		awaitDBSignal(t, r.sender.Reached(r.seed.key2()))
		r.sender.Release(r.seed.key1())
		r.awaitStatus(r.seed.r1.ID, models.RecipientSent)
		r.sender.Release(r.seed.key2())
		r.awaitWorker(w1)
		r.awaitWorker(w2)
		control3 = r.projection()
	})

	t.Run("staggered rows outlast the whole statement", func(t *testing.T) {
		r := newErasureWaitRun(t)
		db := r.app.DB
		ctidOf := func(id uint) (page, off int) {
			var text string
			if err := db.Raw("SELECT ctid::text FROM message_recipients WHERE id = ?", id).Scan(&text).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := fmt.Sscanf(text, "(%d,%d)", &page, &off); err != nil {
				t.Fatalf("ctid %q: %v", text, err)
			}
			return page, off
		}
		p1, o1 := ctidOf(r.seed.r1.ID)
		p2, o2 := ctidOf(r.seed.r2.ID)
		if p1 > p2 || (p1 == p2 && o1 >= o2) {
			t.Fatalf("ctid(R1) (%d,%d) is not before ctid(R2) (%d,%d)", p1, o1, p2, o2)
		}
		// Every plan PostgreSQL may use here visits R1 before R2, whatever it
		// picks. A heap scan (sequential or bitmap) walks ctid order, and an
		// index scan on message_recipients_once (message_id, user_id, channel,
		// copy_of_user_id) or on the message_id index walks message_id order,
		// where M1 comes before M2. The test therefore asserts the premise
		// both ways (R1 is first by ctid and by message id) and that the plan
		// is one of those; it does not assume which one the planner picks. The
		// schedule below is also self-checking: if the statement had locked R2
		// before blocking on R1, W2's SKIP LOCKED read would skip R2 and its
		// key would never be reached, failing the test.
		if r.seed.m1.ID >= r.seed.m2.ID {
			t.Fatalf("M1 (%d) is not before M2 (%d)", r.seed.m1.ID, r.seed.m2.ID)
		}
		var plan []string
		explain := `EXPLAIN UPDATE message_recipients SET address = '', error = '', status = CASE WHEN status = 'queued' THEN 'suppressed' ELSE status END,
			reason = CASE WHEN status = 'queued' THEN 'not active' ELSE reason END, updated_at = now()
			WHERE user_id = ? AND (address <> '' OR error <> '' OR status = 'queued')`
		if err := db.Raw(explain, r.seed.u.ID).Scan(&plan).Error; err != nil {
			t.Fatal(err)
		}
		planText := strings.Join(plan, "\n")
		inOrder := false
		for _, ok := range []string{"Seq Scan on message_recipients", "Bitmap Heap Scan on message_recipients",
			"Index Scan using message_recipients_once on message_recipients", "Index Scan using idx_message_recipients_message on message_recipients"} {
			inOrder = inOrder || strings.Contains(planText, ok)
		}
		if !inOrder {
			t.Fatalf("the scrub uses a plan whose row order is not known to reach R1 first:\n%s", planText)
		}
		w1pid := erasureTestWorkerPID(t, db, r.seed.r1.ID)
		w2pid := erasureTestWorkerPID(t, db, r.seed.r2.ID)
		w1 := r.worker(r.seed.m1, r.seed.r1.ID)
		awaitDBSignal(t, r.sender.Reached(r.seed.key1()))
		del := r.erase()
		epid, qs := erasureTestStatementWait(t, r.app)
		t1 := erasureTestWaitBlockedOnlyBy(t, r.app, epid, qs, int(w1pid.Load()))
		w2 := r.worker(r.seed.m2, r.seed.r2.ID)
		awaitDBSignal(t, r.sender.Reached(r.seed.key2()))
		erasureTestWaitBlockedOnlyBy(t, r.app, epid, qs, int(w1pid.Load())) // still W1 alone
		time.Sleep(time.Until(t1.Add(600 * time.Millisecond)))
		r.sender.Release(r.seed.key1())
		r.awaitStatus(r.seed.r1.ID, models.RecipientSent)
		t2 := erasureTestWaitBlockedOnlyBy(t, r.app, epid, qs, int(w2pid.Load()))
		res := awaitResult(t, del)
		if since := time.Since(t2); since >= 900*time.Millisecond {
			t.Errorf("the erasure answered %v after the second wait, want under 900ms", since)
		}
		select {
		case <-w2:
			t.Fatal("W2 finished before the erasure timed out")
		default:
		}
		if got := r.rowStatus(r.seed.r2.ID); got.Status != models.RecipientQueued {
			t.Errorf("R2 is %q while W2 still holds it", got.Status)
		}
		erasureTestAssertTimeout(t, r, res)
		r.sender.Release(r.seed.key2())
		r.awaitWorker(w1)
		r.awaitWorker(w2)
		if got := r.projection(); got != control3 {
			t.Errorf("worker writes differ from the workers-only control:\nrace    %s\ncontrol %s", got, control3)
		}
		r.assertNoErasureWrites()
		r.assertCleanRun()
	})

	t.Run("staggered rows within the bound", func(t *testing.T) {
		r := newErasureWaitRun(t)
		w1pid := erasureTestWorkerPID(t, r.app.DB, r.seed.r1.ID)
		w2pid := erasureTestWorkerPID(t, r.app.DB, r.seed.r2.ID)
		w1 := r.worker(r.seed.m1, r.seed.r1.ID)
		awaitDBSignal(t, r.sender.Reached(r.seed.key1()))
		del := r.erase()
		epid, qs := erasureTestStatementWait(t, r.app)
		erasureTestWaitBlockedOnlyBy(t, r.app, epid, qs, int(w1pid.Load()))
		w2 := r.worker(r.seed.m2, r.seed.r2.ID)
		awaitDBSignal(t, r.sender.Reached(r.seed.key2()))
		time.Sleep(200 * time.Millisecond)
		r.sender.Release(r.seed.key1())
		r.awaitStatus(r.seed.r1.ID, models.RecipientSent)
		erasureTestWaitBlockedOnlyBy(t, r.app, epid, qs, int(w2pid.Load()))
		time.Sleep(200 * time.Millisecond)
		r.sender.Release(r.seed.key2())
		if res := awaitResult(t, del); res.code != http.StatusOK {
			t.Fatalf("erasure: %d %s", res.code, res.body)
		}
		r.awaitWorker(w1)
		r.awaitWorker(w2)
		for _, id := range []uint{r.seed.r1.ID, r.seed.r2.ID} {
			if got := r.rowStatus(id); got.Address != "" || got.Status != models.RecipientSent {
				t.Errorf("recipient %d after the erasure: %+v", id, got)
			}
		}
		r.assertCleanRun()
	})

	t.Run("erasure first", func(t *testing.T) {
		r := newErasureWaitRun(t)
		var erasurePID atomic.Int64
		reached, release := erasureTestPause(t, r.app.DB, erasureTestPoint{Processor: "raw", Step: "gorm:raw",
			SQLContains: "DELETE FROM login_throttles", PID: &erasurePID})
		del := r.erase()
		awaitDBSignal(t, reached) // the recipient scrub has run; the erasure holds R1 and R2
		r.awaitWorker(r.worker(r.seed.m1, r.seed.r1.ID))
		select {
		case <-r.sender.Reached(r.seed.key1()):
			t.Fatal("a worker reached the provider for a row the erasure held")
		default:
		}
		release()
		if res := awaitResult(t, del); res.code != http.StatusOK {
			t.Fatalf("erasure: %d %s", res.code, res.body)
		}
		got := r.rowStatus(r.seed.r1.ID)
		if got.Status != models.RecipientSuppressed || got.Reason != skipNotActive || got.Address != "" {
			t.Errorf("R1 after the erasure: %+v", got)
		}
		r.awaitWorker(r.worker(r.seed.m1, r.seed.r1.ID))
		select {
		case <-r.sender.Reached(r.seed.key1()):
			t.Fatal("a later worker sent to the erased address")
		default:
		}
		r.assertCleanRun()
	})
}

// TestAccountErasureCancelsScheduledMessages: the erased author's scheduled
// messages are cancelled, one message.cancelled row each by the admin, and
// nothing else about messages changes.
func TestAccountErasureCancelsScheduledMessages(t *testing.T) {
	s := newCoreErasureScene(t)
	s.ac.holdNotices = true
	app, db := s.app, s.app.DB
	author := newTestUser(t, app, "cancel-author", erasurePW, models.RoleMember)
	other := newTestUser(t, app, "cancel-other", erasurePW, models.RoleMember)
	at := time.Now().Add(time.Hour)
	mk := func(by *models.User, status, subject string) models.Message {
		m := models.Message{AuthorID: &by.ID, Status: status, Subject: subject, BodyHTML: "<p>x</p>", BodyText: "x"}
		if status == models.MessageScheduled || status == models.MessageSent {
			m.ScheduledAt = &at
		}
		erasureMustCreate(t, db, &m)
		return m
	}
	scheduled := mk(author, models.MessageScheduled, "Cancel me")
	draft := mk(author, models.MessageDraft, "A draft")
	sent := mk(author, models.MessageSent, "Sent")
	failed := mk(author, models.MessageFailed, "Failed")
	theirs := mk(other, models.MessageScheduled, "Someone else's")
	erasureMustCreate(t, db, &models.AuditLog{ActorID: &author.ID, Action: audit.MessageScheduled, TargetType: "message", TargetID: scheduled.ID, IP: "198.51.100.4"})
	scheduledAudit := dumpRows(t, db, "audit_logs", "id", "action = ?", audit.MessageScheduled)
	untouched := func() string {
		return dumpRows(t, db, "messages", "id", "id IN ?", []uint{draft.ID, sent.ID, failed.ID, theirs.ID})
	}
	before := untouched()

	// A forced failure in the last statement leaves messages and audit alone.
	snapshot := func() string {
		return dumpRows(t, db, "messages", "id", "true") + dumpRows(t, db, "audit_logs", "id", "true")
	}
	whole := snapshot()
	stopFailing := erasureTestFailThrottleDelete(t, db)
	if r := deleteAccount(s.ac, author.ID, nil); r.code != http.StatusInternalServerError {
		t.Fatalf("forced failure: %d %s, want 500", r.code, r.body)
	}
	stopFailing()
	if got := snapshot(); got != whole {
		t.Errorf("a failed erasure left a change:\n%s\n%s", whole, got)
	}

	s.erase(t, author.ID)

	var got models.Message
	db.First(&got, scheduled.ID)
	if got.Status != models.MessageCancelled {
		t.Errorf("the scheduled message is %q, want cancelled", got.Status)
	}
	if now := untouched(); now != before {
		t.Errorf("other messages changed:\nbefore %s\nafter  %s", before, now)
	}
	if now := dumpRows(t, db, "audit_logs", "id", "action = ?", audit.MessageScheduled); now != scheduledAudit {
		t.Errorf("the message.scheduled row changed")
	}
	var rows []models.AuditLog
	db.Where("action = ?", audit.MessageCancelled).Find(&rows)
	var deleted models.AuditLog
	db.Where("action = ?", audit.UserDeleted).First(&deleted)
	if len(rows) != 1 || rows[0].TargetID != scheduled.ID || rows[0].TargetType != "message" || rows[0].ActorID == nil ||
		*rows[0].ActorID != s.admin.ID || rows[0].IP != deleted.IP {
		t.Errorf("message.cancelled rows: %+v (user.deleted ip %q)", rows, deleted.IP)
	}
	// A later message.send for it sends nothing.
	sender := newErasureTestBlockingSender(t)
	app.Mailer = sender
	if err := app.prepareMessage(context.Background(), scheduled.ID); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, "message_recipients", "message_id = ?", scheduled.ID); n != 0 || len(sender.Others()) != 0 {
		t.Errorf("a cancelled message made %d rows and sent %d emails", n, len(sender.Others()))
	}
}

// TestAccountErasureScrubsMessageRecipients: the erased person's recipient
// rows lose their address and error text and queued rows become suppressed;
// a guardian's copy row about them stays; settling still counts email rows
// only.
func TestAccountErasureScrubsMessageRecipients(t *testing.T) {
	s := newCoreErasureScene(t)
	s.ac.holdNotices = true
	app, db := s.app, s.app.DB
	u := newTestUser(t, app, "scrub-u", erasurePW, models.RoleMember)
	guardian := newTestUser(t, app, "scrub-guardian", erasurePW, models.RoleMember)
	friend := newTestUser(t, app, "scrub-friend", erasurePW, models.RoleMember)
	author := newTestUser(t, app, "scrub-author", erasurePW, models.RoleMember)
	at := time.Now().Add(-time.Minute)
	m := models.Message{AuthorID: &author.ID, Status: models.MessageSending, Subject: "Scrub", BodyHTML: "<p>x</p>", BodyText: "x", ScheduledAt: &at, RecipientCount: 2}
	erasureMustCreate(t, db, &m)
	row := func(r models.MessageRecipient) models.MessageRecipient {
		r.MessageID = m.ID
		erasureMustCreate(t, db, &r)
		return r
	}
	sentAt := time.Now()
	queuedEmail := row(models.MessageRecipient{UserID: u.ID, Channel: models.DeliveryEmail, Address: u.Email, Status: models.RecipientQueued})
	queuedPush := row(models.MessageRecipient{UserID: u.ID, Channel: models.DeliveryPush, Status: models.RecipientQueued})
	// A second message gives U a sent row with an address and an error text.
	m2 := models.Message{AuthorID: &author.ID, Status: models.MessageSent, Subject: "Sent", BodyHTML: "<p>x</p>", BodyText: "x", ScheduledAt: &at, SentAt: &sentAt}
	erasureMustCreate(t, db, &m2)
	sentEmail := models.MessageRecipient{MessageID: m2.ID, UserID: u.ID, Channel: models.DeliveryEmail, Address: u.Email, Status: models.RecipientSent, Error: "provider said no", SentAt: &sentAt}
	erasureMustCreate(t, db, &sentEmail)
	// Rows that are not U's own: a guardian copy about U, and a friend's rows.
	copyRow := row(models.MessageRecipient{UserID: guardian.ID, CopyOfUserID: &u.ID, Channel: models.DeliveryEmail, Address: guardian.Email, Status: models.RecipientQueued})
	friendEmail := row(models.MessageRecipient{UserID: friend.ID, Channel: models.DeliveryEmail, Address: friend.Email, Status: models.RecipientSent, SentAt: &sentAt})
	friendPush := row(models.MessageRecipient{UserID: friend.ID, Channel: models.DeliveryPush, Status: models.RecipientQueued})
	others := func() string {
		return dumpRows(t, db, "message_recipients", "id", "id IN ?", []uint{copyRow.ID, friendEmail.ID, friendPush.ID})
	}
	before := others()

	s.erase(t, u.ID)

	if now := others(); now != before {
		t.Errorf("rows that are not the erased person's changed:\nbefore %s\nafter  %s", before, now)
	}
	for _, id := range []uint{queuedEmail.ID, queuedPush.ID} {
		var got models.MessageRecipient
		db.First(&got, id)
		if got.Status != models.RecipientSuppressed || got.Reason != skipNotActive || got.Address != "" {
			t.Errorf("queued row %d: %+v", id, got)
		}
	}
	var got models.MessageRecipient
	db.First(&got, sentEmail.ID)
	if got.Status != models.RecipientSent || got.Address != "" || got.Error != "" || got.SentAt == nil {
		t.Errorf("sent row: %+v", got)
	}
	// The friend's queued push row does not hold the message in sending: only
	// email rows count, and U's email row is no longer queued.
	if err := db.Model(&models.MessageRecipient{}).Where("id = ?", copyRow.ID).Update("status", models.RecipientSent).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return app.settleMessage(tx, m.ID) }); err != nil {
		t.Fatal(err)
	}
	var settled models.Message
	db.First(&settled, m.ID)
	if settled.Status != models.MessageSent {
		t.Errorf("the message is %q after its email rows finished, want sent", settled.Status)
	}
}

// TestErasureTestMessageProjectionIsDeterministic: the same seed on a freshly
// cleared database projects identically, which the worker-only comparisons
// rely on.
func TestErasureTestMessageProjectionIsDeterministic(t *testing.T) {
	first := testApp(t)
	one := erasureTestMessageProjection(t, first.DB, erasureTestRecipientSeed(t, first).labels)
	second := testApp(t)
	two := erasureTestMessageProjection(t, second.DB, erasureTestRecipientSeed(t, second).labels)
	if one != two {
		t.Errorf("two runs of one seed project differently:\n%s\n%s", one, two)
	}
}

// TestErasureTestMessageProjectionDetectsDifferences: the projection tells
// apart changed ordinary text (including digits), a retargeted job and the
// writes of an erasure that survived, and returns to equal when a change is
// put back.
func TestErasureTestMessageProjectionDetectsDifferences(t *testing.T) {
	app := testApp(t)
	seed := erasureTestRecipientSeed(t, app)
	db := app.DB
	if _, err := jobs.Enqueue(db, kindMessageSend, messageSendJob{MessageID: seed.m1.ID}, time.Now(), "", 0); err != nil {
		t.Fatal(err)
	}
	base := erasureTestMessageProjection(t, db, seed.labels)
	baseTables := erasureTestProjectionTables(t, db, seed.labels)
	if again := erasureTestMessageProjection(t, db, seed.labels); again != base {
		t.Fatal("one state projects two ways")
	}
	for _, table := range []string{"calendar_tokens", "push_subscriptions", "registrations", "account_tokens", "login_throttles",
		"invitations", "guest_signups", "mail_outboxes", "account_requests", "notifications", "message_recipients", "jobs"} {
		if len(baseTables[table]) == 0 {
			t.Errorf("the seed has no %s rows, so the projection cannot show them change", table)
		}
	}
	for _, c := range []struct {
		name, apply, undo string
		args, undoArgs    []any
	}{
		{"an ip that differs only in a digit", "UPDATE guest_signups SET ip = '203.0.113.8'", "UPDATE guest_signups SET ip = '203.0.113.7'", nil, nil},
		{"a provider id that differs only in a digit", "UPDATE message_recipients SET provider_message_id = 'pm-2' WHERE id = ?", "UPDATE message_recipients SET provider_message_id = '' WHERE id = ?",
			[]any{seed.r1.ID}, []any{seed.r1.ID}},
		{"ordinary text that differs only in case", "UPDATE mail_outboxes SET subject = 'seeD'", "UPDATE mail_outboxes SET subject = 'seed'", nil, nil},
		{"a job retargeted to another message", "UPDATE jobs SET payload = jsonb_set(payload, '{messageId}', to_jsonb(?::bigint)) WHERE kind = ?",
			"UPDATE jobs SET payload = jsonb_set(payload, '{messageId}', to_jsonb(?::bigint)) WHERE kind = ?",
			[]any{seed.m2.ID, kindMessageSend}, []any{seed.m1.ID, kindMessageSend}},
		{"a job in another state", "UPDATE jobs SET status = 'failed' WHERE kind = ?", "UPDATE jobs SET status = 'queued' WHERE kind = ?",
			[]any{kindMessageSend}, []any{kindMessageSend}},
	} {
		if err := db.Exec(c.apply, c.args...).Error; err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := erasureTestMessageProjection(t, db, seed.labels); got == base {
			t.Errorf("the projection did not notice %s", c.name)
		}
		if err := db.Exec(c.undo, c.undoArgs...).Error; err != nil {
			t.Fatalf("%s (undo): %v", c.name, err)
		}
		if got := erasureTestMessageProjection(t, db, seed.labels); got != base {
			t.Errorf("the projection did not return to equal after undoing %s", c.name)
		}
	}

	// A real erasure leaves writes in every table it scrubs or deletes from.
	seed.s.erase(t, seed.u.ID)
	after := erasureTestProjectionTables(t, db, seed.labels)
	for _, table := range []string{"users", "audit_logs", "account_requests", "notifications", "invitations", "guest_signups",
		"mail_outboxes", "login_throttles", "account_tokens", "calendar_tokens", "push_subscriptions", "message_recipients", "jobs"} {
		if strings.Join(after[table], "\n") == strings.Join(baseTables[table], "\n") {
			t.Errorf("a completed erasure left no difference in %s", table)
		}
	}
	if erasureTestMessageProjection(t, db, seed.labels) == base {
		t.Error("a completed erasure projects like the seed")
	}
}

// ---------------------------------------------------------------------------
// R4-2a-5: remaining activity rows, contact grants and the frozen
// contact_requests text.
// ---------------------------------------------------------------------------

// erasureActivity names the rows erasureSeedActivity made.
type erasureActivity struct {
	group                        *models.Group
	event                        models.Event
	uBlocks, otherBlocks         []models.ManagerAddBlock
	uReceipts, otherReceipts     []models.SeriesUndoReceipt
	uDeliveries, otherDeliveries []models.NotificationDelivery
}

// erasureSeedActivity gives u and other rows in series_undo_receipts,
// manager_add_blocks and notification_deliveries. One of u's deliveries sits on
// another person's notification, so only the explicit delete reaches it.
func erasureSeedActivity(t *testing.T, s coreErasureScene, u, other *models.User) erasureActivity {
	t.Helper()
	db := s.app.DB
	var a erasureActivity
	a.group = newTestGroup(t, s.app, other, "Activity group", models.GroupActive)
	a.event, _ = seedEvent(t, s.app, a.group, other.ID, time.Now().UTC().Add(72*time.Hour))
	block := func(uid uint, typ string, id uint) models.ManagerAddBlock {
		b := models.ManagerAddBlock{SubjectUserID: uid, OrganizerType: typ, OrganizerID: id}
		erasureMustCreate(t, db, &b)
		return b
	}
	a.uBlocks = []models.ManagerAddBlock{block(u.ID, "group", a.group.ID), block(u.ID, "event", a.event.ID)}
	a.otherBlocks = []models.ManagerAddBlock{block(other.ID, "group", a.group.ID)}
	receipt := func(uid uint, label string, used bool) models.SeriesUndoReceipt {
		r := models.SeriesUndoReceipt{TokenHash: hashToken("receipt-" + label), ActorID: uid, EventID: a.event.ID, Snapshot: "[]", CreatedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour)}
		if used {
			now := time.Now()
			r.UsedAt = &now
		}
		erasureMustCreate(t, db, &r)
		return r
	}
	a.uReceipts = []models.SeriesUndoReceipt{receipt(u.ID, "u1", false), receipt(u.ID, "u2", true)}
	a.otherReceipts = []models.SeriesUndoReceipt{receipt(other.ID, "o1", false)}
	note := func(uid uint, label string) models.Notification {
		n := models.Notification{UserID: uid, Kind: "test.note", Category: "groups", Title: label, Link: "/"}
		erasureMustCreate(t, db, &n)
		return n
	}
	delivery := func(n models.Notification, uid uint) models.NotificationDelivery {
		d := models.NotificationDelivery{NotificationID: n.ID, Channel: models.DeliveryEmail, UserID: uid, Status: "sent"}
		erasureMustCreate(t, db, &d)
		return d
	}
	nU, nOther, nOther2 := note(u.ID, "u's own"), note(other.ID, "other's one"), note(other.ID, "other's two")
	a.uDeliveries = []models.NotificationDelivery{delivery(nU, u.ID), delivery(nOther, u.ID)}
	a.otherDeliveries = []models.NotificationDelivery{delivery(nOther2, other.ID)}
	return a
}

func erasureIDs[T any](rows []T, id func(T) uint) []uint {
	out := make([]uint, 0, len(rows))
	for _, r := range rows {
		out = append(out, id(r))
	}
	return out
}

// TestAccountErasureDeletesBlocksReceiptsAndDeliveries: the erased person's
// rows go, other people's stay byte for byte.
func TestAccountErasureDeletesBlocksReceiptsAndDeliveries(t *testing.T) {
	s := newCoreErasureScene(t)
	s.ac.holdNotices = true
	db := s.app.DB
	u := newTestUser(t, s.app, "activity-u", erasurePW, models.RoleMember)
	other := newTestUser(t, s.app, "activity-other", erasurePW, models.RoleMember)
	a := erasureSeedActivity(t, s, u, other)
	others := func() string {
		return dumpRows(t, db, "manager_add_blocks", "id", "id IN ?", erasureIDs(a.otherBlocks, func(b models.ManagerAddBlock) uint { return b.ID })) +
			dumpRows(t, db, "series_undo_receipts", "id", "id IN ?", erasureIDs(a.otherReceipts, func(r models.SeriesUndoReceipt) uint { return r.ID })) +
			dumpRows(t, db, "notification_deliveries", "id", "id IN ?", erasureIDs(a.otherDeliveries, func(d models.NotificationDelivery) uint { return d.ID }))
	}
	before := others()

	s.erase(t, u.ID)

	if got := others(); got != before {
		t.Errorf("other people's rows changed:\n%s\n%s", before, got)
	}
	if n := countRows(t, db, "manager_add_blocks", "subject_user_id = ?", u.ID); n != 0 {
		t.Errorf("%d manager add blocks remain", n)
	}
	if n := countRows(t, db, "series_undo_receipts", "actor_id = ?", u.ID); n != 0 {
		t.Errorf("%d series undo receipts remain", n)
	}
	if n := countRows(t, db, "notification_deliveries", "user_id = ?", u.ID); n != 0 {
		t.Errorf("%d notification deliveries remain", n)
	}
}

// TestAccountErasureScrubsRegistrations: the three free-text and capability
// fields of the erased person's registrations are cleared and nothing else is
// written, so the access-provenance trigger takes its unchanged branch.
func TestAccountErasureScrubsRegistrations(t *testing.T) {
	s := newCoreErasureScene(t)
	s.ac.holdNotices = true
	db := s.app.DB
	u := newTestUser(t, s.app, "reg-u", erasurePW, models.RoleMember)
	o := newTestUser(t, s.app, "reg-other", erasurePW, models.RoleMember)
	leader := newTestUser(t, s.app, "reg-lead", erasurePW, models.RoleMember)
	g := newTestGroup(t, s.app, leader, "Registrations", models.GroupActive)
	addMember(t, s.app, u, g, models.MembershipMember)
	var membership models.Membership
	if err := db.Where("user_id = ? AND group_id = ?", u.ID, g.ID).First(&membership).Error; err != nil {
		t.Fatal(err)
	}
	occ := func(hours int) models.EventOccurrence {
		_, o := seedEvent(t, s.app, g, leader.ID, time.Now().UTC().Add(time.Duration(hours)*time.Hour))
		return o
	}
	reg := func(r models.Registration) models.Registration {
		erasureMustCreate(t, db, &r)
		return r
	}
	removed := leader.ID
	plain := reg(models.Registration{UserID: &u.ID, OccurrenceID: occ(50).ID, Status: models.RegGoing, Source: models.RegSourceGuestForm,
		AttendanceMode: models.AttendanceInPerson, ManageTokenHash: "reg-plain-hash", ChildcareCount: 2, ChildcareAgesNote: "ages 3 and 5", GuestCount: 1})
	gone := reg(models.Registration{UserID: &u.ID, OccurrenceID: occ(51).ID, Status: models.RegRemoved, Source: models.RegSourceWeb,
		AttendanceMode: models.AttendanceInPerson, RemovedBy: &removed, RemovalNote: "left early"})
	granted := time.Now().UTC().Add(-time.Hour)
	live := reg(models.Registration{UserID: &u.ID, OccurrenceID: occ(52).ID, Status: models.RegGoing, Source: models.RegSourceWeb,
		AttendanceMode: models.AttendanceInPerson, ManageTokenHash: "reg-live-hash", ChildcareAgesNote: "ages 7",
		AccessOrigin: policy.OccurrenceOriginDepartureSelf, AccessDepartureMembershipID: &membership.ID, AccessDepartureActorID: &u.ID, AccessGrantedAt: &granted})
	ended := reg(models.Registration{UserID: &u.ID, OccurrenceID: occ(53).ID, Status: models.RegGoing, Source: models.RegSourceWeb,
		AttendanceMode: models.AttendanceInPerson, ManageTokenHash: "reg-ended-hash", RemovalNote: "n",
		AccessOrigin: policy.OccurrenceOriginDepartureSelf, AccessDepartureMembershipID: &membership.ID, AccessDepartureActorID: &u.ID, AccessGrantedAt: &granted})
	if err := db.Model(&models.Registration{}).Where("id = ?", ended.ID).
		Updates(map[string]any{"access_ended_at": time.Now().UTC(), "access_end_reason": "occurrence_ended"}).Error; err != nil {
		t.Fatalf("end the grant: %v", err)
	}
	// Another person's registration that the erased leader removed keeps the
	// note the leader wrote; it is that person's record.
	theirs := reg(models.Registration{UserID: &o.ID, OccurrenceID: occ(54).ID, Status: models.RegRemoved, Source: models.RegSourceWeb,
		AttendanceMode: models.AttendanceInPerson, RemovedBy: &u.ID, RemovalNote: "written by the erased leader", ManageTokenHash: "reg-other-hash"})
	without := func(id uint, extra ...string) string {
		cols := append([]string{"manage_token_hash", "childcare_ages_note", "removal_note"}, extra...)
		expr := "to_jsonb(r)"
		for _, c := range cols {
			expr += " - '" + c + "'"
		}
		var out string
		if err := db.Raw("SELECT ("+expr+")::text FROM registrations r WHERE id = ?", id).Scan(&out).Error; err != nil {
			t.Fatal(err)
		}
		return out
	}
	liveCols := []string{"access_ended_at", "access_end_reason"} // reconcileContacts may end a live grant
	beforePlain, beforeGone, beforeLive, beforeEnded := without(plain.ID), without(gone.ID), without(live.ID, liveCols...), without(ended.ID)
	beforeTheirs := dumpRows(t, db, "registrations", "id", "id = ?", theirs.ID)

	started := time.Now().UTC().Add(-time.Second)
	s.erase(t, u.ID)
	finished := time.Now().UTC().Add(time.Second)
	var endedLive models.Registration
	if err := db.First(&endedLive, live.ID).Error; err != nil {
		t.Fatal(err)
	}
	if endedLive.AccessEndedAt == nil || endedLive.AccessEndedAt.Before(started) || endedLive.AccessEndedAt.After(finished) || endedLive.AccessEndReason != policy.ContactAccountDisabled {
		t.Errorf("live grant lifecycle ending: %+v", endedLive)
	}
	var snapshots []models.AuditLog
	if err := db.Where("action = ? AND target_type = ? AND target_id = ?", audit.OccurrenceAccessEnded, "registration", live.ID).Find(&snapshots).Error; err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("got %d ended snapshots, want 1", len(snapshots))
	}
	var meta struct {
		EndReason string `json:"endReason"`
	}
	if err := json.Unmarshal([]byte(snapshots[0].MetaJSON), &meta); err != nil || meta.EndReason != policy.ContactAccountDisabled {
		t.Errorf("ended snapshot %s: %v", snapshots[0].MetaJSON, err)
	}

	for _, c := range []struct {
		name   string
		id     uint
		before string
		extra  []string
	}{{"plain", plain.ID, beforePlain, nil}, {"removed", gone.ID, beforeGone, nil}, {"live grant", live.ID, beforeLive, liveCols}, {"ended grant", ended.ID, beforeEnded, nil}} {
		if got := without(c.id, c.extra...); got != c.before {
			t.Errorf("%s registration changed beyond the three fields:\n%s\n%s", c.name, c.before, got)
		}
		var row models.Registration
		if err := db.First(&row, c.id).Error; err != nil {
			t.Fatal(err)
		}
		if row.ManageTokenHash != "" || row.ChildcareAgesNote != "" || row.RemovalNote != "" {
			t.Errorf("%s registration keeps %q %q %q", c.name, row.ManageTokenHash, row.ChildcareAgesNote, row.RemovalNote)
		}
	}
	if got := dumpRows(t, db, "registrations", "id", "id = ?", theirs.ID); got != beforeTheirs {
		t.Errorf("another person's registration changed:\n%s\n%s", beforeTheirs, got)
	}
}

// grantFixtureRow makes a granted contact request and its live grant.
func grantFixtureRow(t *testing.T, f *contactAPIFixture, contextType string, contextID uint) models.ContactGrant {
	t.Helper()
	at := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Microsecond)
	r := models.ContactRequest{Purpose: policy.RequestContact, RequesterID: f.leader.ID, SubjectUserID: f.subject.ID, ContextType: contextType, ContextID: contextID,
		Fields: policy.ContactBoth, Reason: "grant reason", NeedsAdmin: false, SubjectDecision: policy.ContactApproved, AdminDecision: policy.ContactNotNeeded,
		Status: policy.ContactGranted, SubjectDecidedBy: &f.subject.ID,
		SubjectDecidedAt: &at, CompletedAt: &at, CreatedAt: at.Add(-time.Hour), ExpiresAt: at.Add(24 * time.Hour)}
	erasureMustCreate(t, f.app.DB, &r)
	g := models.ContactGrant{RequestID: r.ID, LeaderID: r.RequesterID, SubjectUserID: r.SubjectUserID, ContextType: r.ContextType, ContextID: r.ContextID, Fields: r.Fields, GrantedAt: at}
	erasureMustCreate(t, f.app.DB, &g)
	return g
}

// TestAccountErasureEndsContactGrantsOnce (T06): live grants where the erased
// person is the subject or the leader are ended by the lifecycle's own
// reconcile, once each. The erasure adds no statement of its own for them.
func TestAccountErasureEndsContactGrantsOnce(t *testing.T) {
	eligible := func(t *testing.T, f *contactAPIFixture, grants []models.ContactGrant) {
		t.Helper()
		for _, g := range grants {
			scope := policy.ContactScope{RequesterID: g.LeaderID, SubjectUserID: g.SubjectUserID, ContextType: g.ContextType, ContextID: g.ContextID, Fields: g.Fields}
			facts, _, err := f.app.loadContactFacts(f.app.DB, scope, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if reason := policy.ContactIneligibility(facts, false); reason != "" {
				t.Fatalf("grant %d initially ineligible: %s", g.ID, reason)
			}
		}
	}
	check := func(t *testing.T, f *contactAPIFixture, grants []models.ContactGrant) {
		t.Helper()
		for _, g := range grants {
			var got models.ContactGrant
			if err := f.app.DB.First(&got, g.ID).Error; err != nil {
				t.Fatal(err)
			}
			if got.EndedAt == nil || got.EndReason != policy.ContactAccountDisabled || got.EndedBy != nil {
				t.Errorf("grant %d was not ended by the lifecycle: %+v", g.ID, got)
			}
			if n := countRows(t, f.app.DB, "audit_logs", "action = ? AND target_type = ? AND target_id = ?", audit.ContactEnded, "contact_request", g.RequestID); n != 1 {
				t.Errorf("request %d has %d contact.ended snapshots", g.RequestID, n)
			}
		}
		if n := auditCount(t, f.app, audit.ContactEnded); n != int64(len(grants)) {
			t.Errorf("%d contact.ended rows for %d grants", n, len(grants))
		}
	}
	t.Run("erased subject", func(t *testing.T) {
		f := newContactAPIFixture(t, false)
		f.ac.holdNotices = true
		ev, occ := seedEvent(t, f.app, f.group, f.leader.ID, time.Now().UTC().Add(48*time.Hour))
		erasureMustCreate(t, f.app.DB, &models.Registration{UserID: &f.subject.ID, OccurrenceID: occ.ID, Status: models.RegGoing, Source: models.RegSourceWeb, AttendanceMode: models.AttendanceInPerson})
		grants := []models.ContactGrant{grantFixtureRow(t, f, policy.ContactGroup, f.group.ID), grantFixtureRow(t, f, policy.ContactEvent, ev.ID)}
		eligible(t, f, grants)
		if r := deleteAccount(f.ac, f.subject.ID, nil); r.code != http.StatusOK {
			t.Fatalf("delete: %d %s", r.code, r.body)
		}
		check(t, f, grants)
	})
	t.Run("erased leader", func(t *testing.T) {
		f := newContactAPIFixture(t, false)
		f.ac.holdNotices = true
		addMember(t, f.app, f.guardian, f.group, models.MembershipMember)
		grants := []models.ContactGrant{grantFixtureRow(t, f, policy.ContactGroup, f.group.ID)}
		body := map[string]any{"successors": map[string]uint{itoa(f.group.ID): f.guardian.ID}}
		eligible(t, f, grants)
		if r := deleteAccount(f.ac, f.leader.ID, body); r.code != http.StatusOK {
			t.Fatalf("delete: %d %s", r.code, r.body)
		}
		check(t, f, grants)
	})
}

// TestAccountErasureLeavesContactRequestTextForT07 guards the G-7 boundary:
// the erasure writes no contact_requests statement, so every row's reason and
// add_childcare_ages_note stay as written, for pending and terminal rows of
// the contact, group_add and occurrence_add purposes. (The lifecycle may end
// a pending row; that never touches the text.)
func TestAccountErasureLeavesContactRequestTextForT07(t *testing.T) {
	f := newContactAPIFixture(t, false)
	f.ac.holdNotices = true
	db := f.app.DB
	ev, occ := seedEvent(t, f.app, f.group, f.leader.ID, time.Now().UTC().Add(48*time.Hour))
	erased := f.outsider
	created := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	n := 0
	row := func(purpose, status string, requester, subject uint) {
		n++
		r := models.ContactRequest{Purpose: purpose, RequesterID: requester, SubjectUserID: subject, ContextType: policy.ContactGroup, ContextID: f.group.ID,
			Reason: fmt.Sprintf("identifying reason %d", n), SubjectDecision: policy.ContactPending, Status: status,
			CreatedAt: created, ExpiresAt: created.Add(24 * 14 * time.Hour)}
		switch purpose {
		case policy.RequestContact:
			r.Fields, r.NeedsAdmin, r.AdminDecision = policy.ContactBoth, true, policy.ContactPending
		case policy.RequestGroupAdd:
			r.AdminDecision = policy.ContactNotNeeded
		case policy.RequestOccurrenceAdd:
			r.ContextType, r.ContextID, r.OccurrenceID = policy.ContactEvent, ev.ID, &occ.ID
			r.AdminDecision, r.AddAttendanceMode = policy.ContactNotNeeded, models.AttendanceInPerson
			r.AddChildcareAgesNote = fmt.Sprintf("ages note %d", n)
		}
		if status == policy.ContactWithdrawn {
			done := created.Add(10 * time.Minute)
			r.CompletedAt, r.WithdrawnByID = &done, &requester
		}
		erasureMustCreate(t, db, &r)
	}
	for _, purpose := range []string{policy.RequestContact, policy.RequestGroupAdd, policy.RequestOccurrenceAdd} {
		for _, status := range []string{policy.ContactPending, policy.ContactWithdrawn} {
			row(purpose, status, erased.ID, f.subject.ID) // the erased person asked
			row(purpose, status, f.leader.ID, erased.ID)  // the erased person is asked about
		}
	}
	text := func() string {
		var out string
		if err := db.Raw(`SELECT coalesce(jsonb_agg(jsonb_build_array(id, reason, add_childcare_ages_note) ORDER BY id)::text, '[]') FROM contact_requests`).Scan(&out).Error; err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := text()
	if n := countRows(t, db, "contact_requests", "true"); n != 12 {
		t.Fatalf("seeded %d contact requests, want 12", n)
	}

	if r := deleteAccount(f.ac, erased.ID, nil); r.code != http.StatusOK {
		t.Fatalf("delete: %d %s", r.code, r.body)
	}

	if got := text(); got != before {
		t.Errorf("contact request text changed:\nbefore %s\nafter  %s", before, got)
	}
}

// ---------------------------------------------------------------------------
// R4-2a-6: races that need the full statement list.
// ---------------------------------------------------------------------------

// removalRaceFixture is a group event led by L with capacity 1 and a
// waitlist. The erased member U holds the one seat (with a manage token hash
// and a registration of its own), and a seeded waiter waits behind them.
// Every client holds its notices (F1) and nothing runs a job.
type removalRaceFixture struct {
	s       coreErasureScene
	u       *models.User
	leader  *models.User
	waiter  *models.User
	lc      *apiClient
	event   models.Event
	occ     models.EventOccurrence
	reg     models.Registration
	waitReg models.Registration
}

func newRemovalRaceFixture(t *testing.T, start time.Time) *removalRaceFixture {
	t.Helper()
	s := newCoreErasureScene(t)
	s.ac.holdNotices = true
	app, db := s.app, s.app.DB
	f := &removalRaceFixture{s: s}
	f.leader = newTestUser(t, app, "removal-lead", erasurePW, models.RoleMember)
	f.u = newTestUser(t, app, "removal-u", erasurePW, models.RoleMember)
	f.waiter = newTestUser(t, app, "removal-waiter", erasurePW, models.RoleMember)
	f.lc = loginAPI(t, app, "removal-lead", erasurePW)
	f.lc.holdNotices = true
	g := newTestGroup(t, app, f.leader, "Removal Club", models.GroupActive)
	addMember(t, app, f.u, g, models.MembershipMember)
	addMember(t, app, f.waiter, g, models.MembershipMember)
	f.event, f.occ = seedEvent(t, app, g, f.leader.ID, start)
	if err := db.Model(&models.Event{}).Where("id = ?", f.event.ID).
		Updates(map[string]any{"capacity": 1, "waitlist_enabled": true}).Error; err != nil {
		t.Fatal(err)
	}
	f.reg = models.Registration{UserID: &f.u.ID, OccurrenceID: f.occ.ID, Status: models.RegGoing, Source: models.RegSourceWeb,
		AttendanceMode: models.AttendanceInPerson, ManageTokenHash: "removal-u-manage-hash"}
	erasureMustCreate(t, db, &f.reg)
	waitedSince := time.Now().UTC().Add(-time.Hour)
	f.waitReg = models.Registration{UserID: &f.waiter.ID, OccurrenceID: f.occ.ID, Status: models.RegWaitlisted, Source: models.RegSourceWeb,
		AttendanceMode: models.AttendanceInPerson, WaitlistedAt: &waitedSince}
	erasureMustCreate(t, db, &f.waitReg)
	return f
}

// remove is L's real POST /events/:id/registrations/remove for U's seat.
func (f *removalRaceFixture) remove() <-chan httpResult {
	return bgDo(f.lc, "POST", fmt.Sprintf("/api/v1/events/%d/registrations/remove", f.event.ID),
		map[string]any{"registrationId": f.reg.ID, "date": models.FormatCalendarDate(f.occ.OriginalDate), "note": "Moved away"})
}

// erase is the acting admin's real DELETE of U.
func (f *removalRaceFixture) erase() <-chan httpResult {
	return bgDo(f.s.ac, "DELETE", "/api/v1/admin/users/"+itoa(f.u.ID), nil)
}

// assertWaiterPromotedOnce: the waiter is going, with exactly one
// registration.changed audit row that moved their row to going and exactly
// one promotion notice.
func (f *removalRaceFixture) assertWaiterPromotedOnce(t *testing.T) {
	t.Helper()
	db := f.s.app.DB
	if got := regStatus(t, f.s.app, f.waiter.ID, f.occ.ID); got != models.RegGoing {
		t.Errorf("waiter is %q, want going", got)
	}
	if n := countRows(t, db, "audit_logs", "action = ? AND meta_json->>'registrationId' = ? AND meta_json->>'newStatus' = ?",
		audit.RegistrationChanged, itoa(f.waitReg.ID), models.RegGoing); n != 1 {
		t.Errorf("%d audit rows moved the waiter to going, want 1", n)
	}
	if n := countRows(t, db, "notifications", "user_id = ? AND kind = ?", f.waiter.ID, notify.RegistrationPromoted); n != 1 {
		t.Errorf("%d promotion notices for the waiter, want 1", n)
	}
}

// TestRegistrationRemovalSerializesWithErasure: a leader's removal of a
// member's seat (apiRemoveRegistration) and the erasure of that member both
// run inside contactMutation, so they serialize on the exclusive contact gate
// in either order, and neither holds anything the other needs while it waits
// (L4); this is not a cycle and the test claims no deadlock.
func TestRegistrationRemovalSerializesWithErasure(t *testing.T) {
	start := time.Now().UTC().AddDate(0, 0, 30).Truncate(24 * time.Hour).Add(18 * time.Hour)

	t.Run("removal-first", func(t *testing.T) {
		f := newRemovalRaceFixture(t, start)
		app := f.s.app
		states := erasureTestSQLStates(t, app)
		db := app.DB

		// The removal stops before notify.Send's recipient lock. By then it holds
		// the contact gate, U FOR NO KEY UPDATE and O.
		var removalPID atomic.Int64
		removalReached, releaseRemoval := erasureTestPause(t, db, erasureTestPoint{Processor: "row", Step: "gorm:row",
			SQLContains: "deleted_at IS NULL ORDER BY id FOR KEY SHARE", PID: &removalPID})
		// The erasure stops before its notifications DELETE, so the audit row the
		// removal committed can be read before the erasure's later statements.
		erasureReached, releaseErasure := erasureTestPause(t, db, erasureTestBeforeNotificationsDelete(f.u.ID))

		rem := f.remove()
		awaitDBSignal(t, removalReached)
		del := f.erase()
		erasurePID := waitForLockedUsersQuery(t, app, "%pg_advisory_xact_lock%")
		waitDBBlocked(t, db, erasurePID, int(removalPID.Load()))
		// The erasure waits at the gate: U is still held by the removal, and no
		// backend waits on an occurrence.
		err := db.Transaction(func(tx *gorm.DB) error {
			return tx.Exec("SELECT 1 FROM users WHERE id = ? FOR UPDATE NOWAIT", f.u.ID).Error
		})
		var pe *pgconn.PgError
		if !errors.As(err, &pe) || pe.Code != "55P03" {
			t.Errorf("FOR UPDATE NOWAIT on U = %v, want SQLSTATE 55P03 while the removal holds it", err)
		}
		var onOccurrence int64
		if err := db.Raw(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()
			AND wait_event_type = 'Lock' AND state = 'active' AND query LIKE '%event_occurrences%'`).Scan(&onOccurrence).Error; err != nil {
			t.Fatal(err)
		}
		if onOccurrence != 0 {
			t.Errorf("%d backends wait on event_occurrences while the erasure waits at the gate", onOccurrence)
		}

		releaseRemoval()
		if r := awaitResult(t, rem); r.code != http.StatusOK {
			t.Fatalf("removal: %d %s", r.code, r.body)
		}
		awaitDBSignal(t, erasureReached) // the removal has committed; the erasure holds the gate and U
		if n := countRows(t, db, "audit_logs", "action = ?", audit.RegistrationRemoved); n != 1 {
			t.Fatalf("%d registration.removed rows before the erasure's notifications delete, want 1", n)
		}
		if n := countRows(t, db, "notifications", "user_id = ? AND kind = ?", f.u.ID, notify.RegistrationRemoved); n != 1 {
			t.Fatalf("U has %d removal notices before the erasure's notifications delete, want 1", n)
		}
		auditBefore := dumpRows(t, db, "audit_logs", "id", "action = ?", audit.RegistrationRemoved)
		releaseErasure()
		if r := awaitResult(t, del); r.code != http.StatusOK {
			t.Fatalf("erasure: %d %s", r.code, r.body)
		}

		var reg models.Registration
		if err := db.First(&reg, f.reg.ID).Error; err != nil {
			t.Fatal(err)
		}
		if reg.Status != models.RegRemoved || reg.RemovalNote != "" || reg.ManageTokenHash != "" {
			t.Errorf("U's registration: status %q note %q hash %q, want removed with both cleared", reg.Status, reg.RemovalNote, reg.ManageTokenHash)
		}
		if n := countRows(t, db, "notifications", "user_id = ?", f.u.ID); n != 0 {
			t.Errorf("%d notifications of U remain", n)
		}
		if n := countRows(t, db, "notification_deliveries", "user_id = ?", f.u.ID); n != 0 {
			t.Errorf("%d notification deliveries of U remain", n)
		}
		if got := dumpRows(t, db, "audit_logs", "id", "action = ?", audit.RegistrationRemoved); got != auditBefore {
			t.Errorf("the registration.removed audit row changed:\n%s\n%s", auditBefore, got)
		}
		f.assertWaiterPromotedOnce(t)
		if n := states.Count("40P01"); n != 0 {
			t.Errorf("%d deadlocks recorded: %v", n, states.entries)
		}
	})

	t.Run("erasure-first", func(t *testing.T) {
		f := newRemovalRaceFixture(t, start)
		app := f.s.app
		states := erasureTestSQLStates(t, app)
		db := app.DB

		var erasurePID atomic.Int64
		p := erasureTestBeforeNotificationsDelete(f.u.ID)
		p.PID = &erasurePID
		reached, release := erasureTestPause(t, db, p)
		del := f.erase()
		awaitDBSignal(t, reached) // the erasure holds the gate and U
		rem := f.remove()
		removalPID := waitForLockedUsersQuery(t, app, "%pg_advisory_xact_lock%")
		waitDBBlocked(t, db, removalPID, int(erasurePID.Load()))
		release()
		if r := awaitResult(t, del); r.code != http.StatusOK {
			t.Fatalf("erasure: %d %s", r.code, r.body)
		}
		// lockRegistrationHolder's default-scoped First finds no soft-deleted
		// holder (api_registrations.go:1917-1923), and the handler answers every
		// error that is not errRegNotFound with 500 (:1436-1438).
		r := awaitResult(t, rem)
		if r.code != http.StatusInternalServerError || strings.TrimSpace(r.body) != `{"code":"server_error","error":"server error"}` {
			t.Errorf("removal after the erasure: %d %s, want generic 500", r.code, r.body)
		}
		if n := auditCount(t, app, audit.RegistrationRemoved); n != 0 {
			t.Errorf("%d registration.removed rows, want none", n)
		}
		if n := countRows(t, db, "notifications", "kind = ?", notify.RegistrationRemoved); n != 0 {
			t.Errorf("%d removal notices were written, want none", n)
		}
		var reg models.Registration
		if err := db.First(&reg, f.reg.ID).Error; err != nil {
			t.Fatal(err)
		}
		if reg.Status == models.RegRemoved || reg.ManageTokenHash != "" {
			t.Errorf("U's registration after the erasure: status %q hash %q", reg.Status, reg.ManageTokenHash)
		}
		if n := states.Count("40P01"); n != 0 {
			t.Errorf("%d deadlocks recorded: %v", n, states.entries)
		}
	})
}

// erasureDeadlockFixture is the D-n world: an erased admin U with a future
// going seat at O (so promoteOnDisable locks O), a second admin Z, the acting
// admin, and a guest at address B with a pending signup S2 at O that asks for
// an account. Nobody has an account at B.
type erasureDeadlockFixture struct {
	s       coreErasureScene
	u, z    *models.User
	event   models.Event
	occ     models.EventOccurrence
	signup  models.GuestSignup
	raw     string
	address string // B
	anon    *apiClient
	labels  map[string]map[uint]string
}

func newErasureDeadlockFixture(t *testing.T, start time.Time) *erasureDeadlockFixture {
	t.Helper()
	s := newCoreErasureScene(t)
	s.ac.holdNotices = true
	app, db := s.app, s.app.DB
	f := &erasureDeadlockFixture{s: s, z: s.second, raw: "guest-admin-notice-deadlock-token", address: "deadlock.guest@example.com"}
	f.u = newTestUser(t, app, "erase-dn-u", erasurePW, models.RoleAdmin)
	g := newTestGroup(t, app, s.admin, "Deadlock Club", models.GroupActive)
	f.event, f.occ = seedEvent(t, app, g, s.admin.ID, start)
	erasureMustCreate(t, db, &models.Registration{UserID: &f.u.ID, OccurrenceID: f.occ.ID, Status: models.RegGoing,
		Source: models.RegSourceWeb, AttendanceMode: models.AttendanceInPerson})
	f.signup = models.GuestSignup{Name: "Deadlock Guest", Email: f.address, OccurrenceID: f.occ.ID, CreateAccount: true,
		TokenHash: hashToken(f.raw), ExpiresAt: time.Now().Add(time.Hour), IP: "203.0.113.10", Timezone: "UTC"}
	erasureMustCreate(t, db, &f.signup)
	f.anon = anonClient(t, app, "10.9.9.10")
	f.anon.holdNotices = true
	f.labels = map[string]map[uint]string{
		"user": {s.admin.ID: "admin", f.z.ID: "Z", f.u.ID: "U"},
	}
	return f
}

func (f *erasureDeadlockFixture) confirm() <-chan httpResult {
	return bgDo(f.anon, "POST", "/api/v1/guest-registrations/confirm", map[string]string{"token": f.raw})
}

func (f *erasureDeadlockFixture) erase() <-chan httpResult {
	return bgDo(f.s.ac, "DELETE", "/api/v1/admin/users/"+itoa(f.u.ID), nil)
}

// erasureTestProjection is the committed state the D-n race decides, read by
// natural key and without sequence ids, timestamps or token hashes (the
// shared projection helpers reduce them): U, Z and whoever is at B, the
// registrations at O, S2, the account requests at B, the notifications of U
// and Z, the audit rows that target U or the event, all account.requested audits, and the jobs counted by
// kind and status. A rolled-back loser leaves no row in any of them, so the
// race's projection equals the winner-only control's.
func erasureTestProjection(t *testing.T, db *gorm.DB, f *erasureDeadlockFixture) string {
	t.Helper()
	rows := func(table, where string, args ...any) []string {
		var raw []string
		if err := db.Raw("SELECT to_jsonb(r)::text FROM "+table+" r WHERE "+where, args...).Scan(&raw).Error; err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, text := range raw {
			var v map[string]any
			if err := json.Unmarshal([]byte(text), &v); err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(erasureTestProjectRow(v, f.labels))
			out = append(out, string(b))
		}
		sort.Strings(out)
		return out
	}
	var jobs []string
	if err := db.Raw(`SELECT kind || '/' || status || '=' || count(*)::text FROM jobs GROUP BY kind, status`).Scan(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	sort.Strings(jobs)
	b, err := json.Marshal(map[string][]string{
		"users":            rows("users", "r.id IN ? OR r.email IN ?", []uint{f.u.ID, f.z.ID}, []string{mail.NormalizeAddress(f.u.Email), f.address, f.z.Email}),
		"registrations":    rows("registrations", "r.occurrence_id = ?", f.occ.ID),
		"guest_signups":    rows("guest_signups", "r.id = ? OR r.email = ?", f.signup.ID, f.address),
		"account_requests": rows("account_requests", "r.email = ?", f.address),
		"notifications":    rows("notifications", "r.user_id IN ?", []uint{f.u.ID, f.z.ID}),
		"audit_logs":       rows("audit_logs", "(r.target_type = 'user' AND r.target_id = ?) OR (r.target_type = 'event' AND r.target_id = ?) OR r.action = ?", f.u.ID, f.event.ID, audit.AccountRequested),
		"jobs_by_kind":     jobs,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// erasureTestWaitPastDeadlockCheck waits, on the database clock, until the
// statement that started at queryStart has waited longer than
// deadlock_timeout (the figure SHOW deadlock_timeout prints, read in
// milliseconds) plus 500 ms. A waiter runs one deadlock check, after that
// time, and a waiter that joins the cycle later runs its own.
func erasureTestWaitPastDeadlockCheck(t *testing.T, app *App, queryStart time.Time) {
	t.Helper()
	var ms float64
	if err := app.DB.Raw("SELECT setting::float8 FROM pg_settings WHERE name = 'deadlock_timeout'").Scan(&ms).Error; err != nil {
		t.Fatal(err)
	}
	want := time.Duration(ms*float64(time.Millisecond)) + 500*time.Millisecond
	giveUp := time.Now().Add(want + 10*time.Second)
	for erasureTestSinceQueryStart(t, app, queryStart) <= want {
		if time.Now().After(giveUp) {
			t.Fatalf("the statement started at %v never passed %v", queryStart, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestErasureDeadlockWithGuestAdminNoticeRollsBackLoser is the kept limit D-n,
// recorded and not fixed: a guest redemption that asked for an account holds
// the occurrence O and the signup S2, then notifyAccountRequested locks every
// active admin FOR KEY SHARE, while the erasure of one of those admins holds
// its row FOR UPDATE and waits for O (promoteOnDisable). Postgres aborts the
// side that closes the cycle with 40P01. The schedule makes the erasure wait
// first and pass its single deadlock check before redemption starts waiting,
// so redemption is the later waiter, detects the cycle and loses. The control
// runs only the erasure on an identical seed; the race must leave exactly the
// control's committed state (F5), which proves the loser left no write. The
// test does not claim the erasure is deadlock-free.
func TestErasureDeadlockWithGuestAdminNoticeRollsBackLoser(t *testing.T) {
	start := time.Now().UTC().AddDate(0, 0, 30).Truncate(24 * time.Hour).Add(18 * time.Hour)
	var control string

	t.Run("control", func(t *testing.T) {
		f := newErasureDeadlockFixture(t, start)
		states := erasureTestSQLStates(t, f.s.app)
		if r := awaitResult(t, f.erase()); r.code != http.StatusOK {
			t.Fatalf("erasure: %d %s", r.code, r.body)
		}
		if n := states.Count("40P01"); n != 0 {
			t.Errorf("%d deadlocks recorded in the winner-only run", n)
		}
		control = erasureTestProjection(t, f.s.app.DB, f)
		// The rollback comparison must detect even an orphaned audit.
		orphan := models.AuditLog{Action: audit.AccountRequested, TargetType: "account_request", TargetID: 999999, MetaJSON: "{}"}
		erasureMustCreate(t, f.s.app.DB, &orphan)
		if erasureTestProjection(t, f.s.app.DB, f) == control {
			t.Fatal("projection ignored an orphan account.requested audit")
		}
		if err := f.s.app.DB.Delete(&orphan).Error; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("race", func(t *testing.T) {
		if control == "" {
			t.Fatal("the winner-only control did not run; run the whole test, not this subtest alone")
		}
		f := newErasureDeadlockFixture(t, start)
		app := f.s.app
		states := erasureTestSQLStates(t, app)
		db := app.DB

		// Redemption stops before it creates the guest_upgrade request: it holds O
		// and S2, has created the guest user at B, and has not yet notified anyone.
		var redemptionPID atomic.Int64
		reached, release := erasureTestPause(t, db, erasureTestPoint{Processor: "create", Step: "gorm:create",
			Table: "account_requests", PID: &redemptionPID})
		red := f.confirm()
		awaitDBSignal(t, reached)
		del := f.erase()
		erasurePID := waitForLockedUsersQuery(t, app, "%event_occurrences%FOR UPDATE%")
		waitDBBlocked(t, db, erasurePID, int(redemptionPID.Load()))
		var queryStart time.Time
		if err := db.Raw("SELECT query_start FROM pg_stat_activity WHERE pid = ?", erasurePID).Scan(&queryStart).Error; err != nil {
			t.Fatal(err)
		}
		erasureTestWaitPastDeadlockCheck(t, app, queryStart)
		// Still the same statement, still waiting on redemption: its one deadlock
		// check found no cycle, because redemption was not waiting yet.
		var again time.Time
		if err := db.Raw("SELECT query_start FROM pg_stat_activity WHERE pid = ? AND state = 'active'", erasurePID).Scan(&again).Error; err != nil {
			t.Fatal(err)
		}
		if !again.Equal(queryStart) {
			t.Fatalf("the erasure's statement changed while it waited: %v then %v", queryStart, again)
		}
		waitDBBlocked(t, db, erasurePID, int(redemptionPID.Load()))
		release()
		redemption, erasure := awaitResult(t, red), awaitResult(t, del)

		if erasure.code != http.StatusOK {
			t.Fatalf("erasure: %d %s; the erasure was meant to win", erasure.code, erasure.body)
		}
		entry := states.Only("40P01")
		if !strings.Contains(entry.SQL, "FOR KEY SHARE") {
			t.Errorf("the deadlock victim ran %q, want redemption's recipient lock (FOR KEY SHARE)", entry.SQL)
		}
		if redemption.code != http.StatusInternalServerError || strings.TrimSpace(redemption.body) != `{"code":"server_error","error":"server error"}` {
			t.Errorf("redemption: %d %s, want generic 500", redemption.code, redemption.body)
		}
		if got := erasureTestProjection(t, db, f); got != control {
			t.Errorf("the race left more than the winner-only control:\nrace    %s\ncontrol %s", got, control)
		}
		if n := countRows(t, db, "users", "email = ?", f.address); n != 0 {
			t.Errorf("%d users at B after the loser rolled back", n)
		}
		if n := countRows(t, db, "registrations", "occurrence_id = ? AND user_id IN (SELECT id FROM users WHERE email = ?)", f.occ.ID, f.address); n != 0 {
			t.Errorf("%d registrations for B after the loser rolled back", n)
		}
		if n := countRows(t, db, "guest_signups", "id = ? AND confirmed_at IS NULL", f.signup.ID); n != 1 {
			t.Errorf("S2 is no longer pending (%d pending rows)", n)
		}
		if n := countRows(t, db, "account_requests", "source = ?", models.RequestGuestUpgrade); n != 0 {
			t.Errorf("%d guest_upgrade requests after the loser rolled back", n)
		}
		if n := countRows(t, db, "notifications", "kind = ?", notify.AccountRequested); n != 0 {
			t.Errorf("%d AccountRequested tasks after the loser rolled back", n)
		}

		if n := countRows(t, db, "audit_logs", "action = ?", audit.AccountRequested); n != 0 {
			t.Errorf("%d account.requested audits survived rollback", n)
		}

		// The loser's link still works: the retry wins, with U already gone.
		if r := awaitResult(t, f.confirm()); r.code != http.StatusOK {
			t.Fatalf("retry: %d %s", r.code, r.body)
		}
		if n := countRows(t, db, "registrations", "occurrence_id = ? AND user_id IN (SELECT id FROM users WHERE email = ?)", f.occ.ID, f.address); n != 1 {
			t.Errorf("%d registrations for B after the retry, want 1", n)
		}
		var reqIDs []uint
		if err := db.Table("account_requests").Where("email = ? AND source = ?", f.address, models.RequestGuestUpgrade).Pluck("id", &reqIDs).Error; err != nil {
			t.Fatal(err)
		}
		if len(reqIDs) != 1 {
			t.Fatalf("%d guest_upgrade requests at B after the retry, want 1", len(reqIDs))
		}
		if n := countRows(t, db, "audit_logs", "action = ? AND target_type = ? AND target_id = ?", audit.AccountRequested, "account_request", reqIDs[0]); n != 1 {
			t.Errorf("retry has %d account.requested audits, want 1", n)
		}
		if n := countRows(t, db, "audit_logs", "action = ?", audit.AccountRequested); n != 1 {
			t.Errorf("%d total account.requested audits after retry, want 1", n)
		}
		if n := countRows(t, db, "notifications", "user_id = ? AND kind = ? AND subject_id = ? AND resolved_at IS NULL",
			f.z.ID, notify.AccountRequested, reqIDs[0]); n != 1 {
			t.Errorf("Z has %d waiting AccountRequested tasks, want 1", n)
		}
		if n := countRows(t, db, "notifications", "user_id = ? AND kind = ?", f.u.ID, notify.AccountRequested); n != 0 {
			t.Errorf("the erased admin got %d AccountRequested tasks; notify.Send skips a deleted recipient", n)
		}
		if n := states.Count("40P01"); n != 1 {
			t.Errorf("%d deadlocks recorded after the retry, want the one", n)
		}
	})
}
