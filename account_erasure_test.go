package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"sort"
	"strconv"
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
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
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
// and a living guardian's used token naming the person stay (an unused one
// naming the erased teen is deleted by the household step, R4-3b-2).
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
	guardianUsed := time.Now().Add(-time.Hour)
	guardianTok := models.AccountToken{
		Kind: models.TokenTeenConsent, UserID: g.ID, TokenHash: hashToken("guardian-token"),
		Payload: fmt.Sprintf(`{"teenId":%d}`, u.ID), ExpiresAt: time.Now().Add(time.Hour), UsedAt: &guardianUsed,
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

	owned := ownedThrottleKeys(u.ID, heldAddress{value: addr, ok: true})
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
	owned := ownedThrottleKeys(u.ID, heldAddress{value: addr, ok: true})
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
	eventID, occ, groupID := erasureEventFixture(t, s)
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
	// R4-2d1b-2: the consent scrub runs right after the soft delete, so a
	// failure in the last statement must leave every consent row as it was.
	erasureSeedConsentEvidence(t, db, u.ID, "rollback")
	// R4-T07A: the ages-note scrub also runs right after the soft delete, so the
	// same late failure must leave the subject's notes as they were, on a
	// pending and on a terminal row.
	erasureMakeRequest(t, db, policy.RequestOccurrenceAdd, policy.ContactPending, l.ID, u.ID, groupID, eventID, occ.ID, 1, "rollback pending note")
	erasureMakeRequest(t, db, policy.RequestOccurrenceAdd, policy.ContactDenied, l.ID, u.ID, groupID, eventID, occ.ID, 2, "rollback denied note")
	// Both rows must change on success, so the late-failure snapshot proves
	// that the audit scrub participates in the handler's transaction.
	actorAudit := auditErasureLog(t, db, audit.UserCreated, "user", l.ID, &u.ID, map[string]any{"role": "member"})
	metadataAudit := auditErasureLog(t, db, audit.RegistrationRemoved, "event", eventID, &l.ID, map[string]any{"userId": u.ID, "note": "rollback sentinel"})
	tables := []string{"account_requests", "notifications", "invitations", "guest_signups", "mail_outboxes", "messages", "message_recipients",
		"audit_logs", "series_undo_receipts", "manager_add_blocks", "registrations", "notification_deliveries", "consents", "contact_requests", "users"}
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
	if r := deleteAccount(s.ac, u.ID, nil); r.code != http.StatusOK {
		t.Fatalf("delete after removing failure answered %d %s, want 200", r.code, r.body)
	}
	assertAuditErasureRows(t, db, []models.AuditLog{actorAudit, metadataAudit}, map[uint]bool{actorAudit.ID: true}, map[uint][]string{metadataAudit.ID: {"note"}})
}

// erasureSeedConsentEvidence gives uid six consent rows the way the writers
// record them: email and push, opted in and out, with every combination of
// ip, user_agent and evidence_text (the push turn-on path stores the device
// label as evidence_text). Five rows carry a non-empty field; the sixth is
// already blank, so a scrub has a row it must leave alone. At is the column
// default, so the rows keep their creation order.
func erasureSeedConsentEvidence(t *testing.T, db *gorm.DB, uid uint, tag string) {
	t.Helper()
	for _, c := range []models.Consent{
		{Channel: models.ConsentEmail, Scope: "category:groups", Status: models.ConsentOptedIn, Source: models.ConsentSourceWebForm,
			EvidenceText: "Turned on at sign-up " + tag, IP: "203.0.113.7", UserAgent: "Mail agent " + tag},
		{Channel: models.ConsentEmail, Scope: "category:groups", Status: models.ConsentOptedOut, Source: models.ConsentSourceUnsubscribe,
			IP: "203.0.113.8", UserAgent: "Gmail one-click " + tag},
		{Channel: models.ConsentEmail, Scope: "category:events", Status: models.ConsentOptedIn, Source: models.ConsentSourceWebForm,
			UserAgent: "Only an agent " + tag},
		{Channel: models.ConsentPush, Scope: "all", Status: models.ConsentOptedIn, Source: models.ConsentSourceWebForm,
			EvidenceText: "Pixel 9 " + tag, IP: "203.0.113.9", UserAgent: "Push agent " + tag},
		{Channel: models.ConsentPush, Scope: "category:groups", Status: models.ConsentOptedOut, Source: models.ConsentSourceWebForm,
			IP: "203.0.113.10"},
		{Channel: models.ConsentEmail, Scope: "category:messages", Status: models.ConsentOptedIn, Source: models.ConsentSourceWebForm},
	} {
		c.UserID = uid
		erasureMustCreate(t, db, &c)
	}
}

const erasureConsentRestOf = `SELECT coalesce(jsonb_agg(to_jsonb(c) - 'ip' - 'user_agent' - 'evidence_text' ORDER BY c.id)::text, '[]')
	FROM consents c WHERE c.user_id = ?`

// Erasing a person blanks the ip, user_agent and evidence_text of their
// consent rows (T05, M24) and nothing else: the opted-out choice stays, so the
// optional-mail and push readers decide as before, and another person's rows
// and every teen_consents row are untouched.
func TestAccountErasureScrubsConsentEvidence(t *testing.T) {
	s := newCoreErasureScene(t)
	s.ac.holdNotices = true
	db := s.app.DB
	u := newTestUser(t, s.app, "consent.erased@x.test", erasurePW, models.RoleMember)
	other := newTestUser(t, s.app, "consent.living@x.test", erasurePW, models.RoleMember)
	erasureSeedConsentEvidence(t, db, u.ID, "erased")
	erasureSeedConsentEvidence(t, db, other.ID, "living")
	for _, id := range []uint{u.ID, other.ID} {
		erasureMustCreate(t, db, &models.TeenConsent{TeenUserID: id, Scope: models.TeenConsentAccount, Status: models.ConsentRevoked,
			GrantedBy: s.admin.ID, GranterRole: models.ConsentAdmin, EvidenceText: "Teen evidence"})
	}

	const nonEmpty = "(ip <> '' OR user_agent <> '' OR evidence_text <> '')"
	var scrubbed []uint
	if err := db.Raw("SELECT id FROM consents WHERE user_id = ? AND "+nonEmpty+" ORDER BY id", u.ID).Scan(&scrubbed).Error; err != nil {
		t.Fatal(err)
	}
	if len(scrubbed) != 5 {
		t.Fatalf("seed: %d of the erased person's rows carry evidence, want 5", len(scrubbed))
	}
	var restBefore string
	if err := db.Raw(erasureConsentRestOf, u.ID).Scan(&restBefore).Error; err != nil {
		t.Fatal(err)
	}
	otherBefore := dumpRows(t, db, "consents", "id", "user_id = ?", other.ID)
	teenBefore := dumpRows(t, db, "teen_consents", "id", "true")
	optOutsBefore, err := emailOptOuts(db, []uint{u.ID})
	if err != nil {
		t.Fatal(err)
	}
	pushOnBefore, pushOffBefore, err := pushConsent(db, u.ID, "groups")
	if err != nil {
		t.Fatal(err)
	}
	if !optOutsBefore[u.ID]["category:groups"] || !pushOnBefore || !pushOffBefore {
		t.Fatalf("seed: readers see email opt-outs %v, push on %v, push groups off %v", optOutsBefore, pushOnBefore, pushOffBefore)
	}

	if r := deleteAccount(s.ac, u.ID, nil); r.code != http.StatusOK || !strings.HasPrefix(strings.TrimSpace(r.body), `{"erasure":{`) {
		t.Fatalf("delete answered %d %s, want 200 with the erasure report", r.code, r.body)
	}

	if n := countRows(t, db, "consents", "user_id = ? AND "+nonEmpty, u.ID); n != 0 {
		t.Errorf("%d of the erased person's rows still carry evidence", n)
	}
	if n := countRows(t, db, "consents", "id IN ? AND ip = '' AND user_agent = '' AND evidence_text = ''", scrubbed); n != int64(len(scrubbed)) {
		t.Errorf("%d of the %d rows that carried evidence are blank now", n, len(scrubbed))
	}
	if n := countRows(t, db, "consents", "user_id = ?", u.ID); n != 6 {
		t.Errorf("the erased person has %d consent rows, want the 6 they had", n)
	}
	var restAfter string
	if err := db.Raw(erasureConsentRestOf, u.ID).Scan(&restAfter).Error; err != nil {
		t.Fatal(err)
	}
	if restAfter != restBefore {
		t.Errorf("the scrub changed more than the three evidence columns:\n%s\n%s", restBefore, restAfter)
	}
	if got := dumpRows(t, db, "consents", "id", "user_id = ?", other.ID); got != otherBefore {
		t.Errorf("another person's consent rows changed:\n%s\n%s", otherBefore, got)
	}
	if got := dumpRows(t, db, "teen_consents", "id", "true"); got != teenBefore {
		t.Errorf("teen_consents changed:\n%s\n%s", teenBefore, got)
	}
	optOutsAfter, err := emailOptOuts(db, []uint{u.ID})
	if err != nil {
		t.Fatal(err)
	}
	pushOnAfter, pushOffAfter, err := pushConsent(db, u.ID, "groups")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(optOutsAfter, optOutsBefore) || pushOnAfter != pushOnBefore || pushOffAfter != pushOffBefore {
		t.Errorf("the readers decide differently after the scrub: email opt-outs %v then %v, push on %v then %v, push groups off %v then %v",
			optOutsBefore, optOutsAfter, pushOnBefore, pushOnAfter, pushOffBefore, pushOffAfter)
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

		t.Run("a timeout already in force is never raised", func(t *testing.T) {
			tx := conn.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			if err := tx.Exec("SET LOCAL statement_timeout = '1s'").Error; err != nil {
				t.Fatal(err)
			}
			var inside string
			if err := withStatementTimeout(tx, 5*time.Second, func() error {
				inside = show(t, tx)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if inside != "1s" {
				t.Errorf("inside = %q, want the 1s already in force", inside)
			}
			if got := show(t, tx); got != "1s" {
				t.Errorf("after the call = %q, want 1s", got)
			}
			if err := tx.Rollback().Error; err != nil {
				t.Fatal(err)
			}
		})

		t.Run("no timeout in force takes the one asked for", func(t *testing.T) {
			tx := conn.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			if err := tx.Exec("SET LOCAL statement_timeout = 0").Error; err != nil {
				t.Fatal(err)
			}
			var inside string
			if err := withStatementTimeout(tx, 5*time.Second, func() error {
				inside = show(t, tx)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if inside != "5s" {
				t.Errorf("inside = %q, want 5s", inside)
			}
			if err := tx.Rollback().Error; err != nil {
				t.Fatal(err)
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
	seedThrottleRows(t, db, ownedThrottleKeys(e.u.ID, heldAddress{value: e.address, ok: true}))
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
	// R4-CTX: the statement timeout is a timed-out deletion, which the contact
	// transaction classifies (classifyContactErr): 503 delete_timed_out, nothing
	// deleted. Neither the handler's own "could not delete user" text nor the
	// SQLSTATE or statement text reaches the body.
	if res.code != http.StatusServiceUnavailable || !strings.Contains(res.body, `"code":"delete_timed_out"`) {
		t.Fatalf("erasure: %d %s, want 503 delete_timed_out", res.code, res.body)
	}
	for _, leak := range []string{"57014", "statement timeout", "message_recipients", "could not delete user"} {
		if strings.Contains(res.body, leak) {
			t.Errorf("the 503 body leaks %q: %s", leak, res.body)
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
	var scheduledAudit []models.AuditLog
	if err := db.Where("action = ?", audit.MessageScheduled).Find(&scheduledAudit).Error; err != nil {
		t.Fatal(err)
	}
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
	assertAuditErasureRows(t, db, scheduledAudit, map[uint]bool{scheduledAudit[0].ID: true}, nil)
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

// ---------------------------------------------------------------------------
// R4-T07A: the childcare ages note of the subject's occurrence_add rows.
// ---------------------------------------------------------------------------

// erasureAgesNoteRow is one contact_requests row as the ages-note tests compare
// it: the few columns they branch on, the note, the whole row as JSON with and
// without the note, and the row without the columns the request lifecycle moves.
type erasureAgesNoteRow struct {
	ID            uint
	Purpose       string
	Status        string
	Reason        string
	Note          string
	SubjectUserID uint
	RequesterID   uint
	EndReason     string
	CompletedAt   *time.Time
	Rest          string
	Whole         string
	// Preserved is the whole row as JSON without status, end_reason and
	// completed_at (the three columns finishContactRequest moves) and without
	// the ages note (the scrub's). Every other column, decision timestamps and
	// the people who decided or withdrew included, appears with its value, so a
	// pending row that the lifecycle ends must keep it exactly.
	Preserved string
}

func erasureRequestRows(t testing.TB, db *gorm.DB) map[uint]erasureAgesNoteRow {
	t.Helper()
	var rows []erasureAgesNoteRow
	if err := db.Raw(`SELECT id, purpose, status, reason, subject_user_id, requester_id, add_childcare_ages_note AS note,
		end_reason, completed_at,
		(to_jsonb(r) - 'add_childcare_ages_note')::text AS rest, to_jsonb(r)::text AS whole,
		(to_jsonb(r) - 'status' - 'end_reason' - 'completed_at' - 'add_childcare_ages_note')::text AS preserved
		FROM contact_requests r ORDER BY id`).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	out := make(map[uint]erasureAgesNoteRow, len(rows))
	for _, r := range rows {
		out[r.ID] = r
	}
	return out
}

// erasureMakeRequest inserts one valid contact_requests row of purpose and
// status (pending, withdrawn, denied, expired or ended; granted rows come from
// erasureGrantedAdd) for a live subject. The row honors every CHECK; an
// occurrence_add row carries note as its childcare ages note.
func erasureMakeRequest(t *testing.T, db *gorm.DB, purpose, status string, requester, subject, groupID, eventID, occurrenceID uint, n int, note string) models.ContactRequest {
	t.Helper()
	created := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	r := models.ContactRequest{Purpose: purpose, RequesterID: requester, SubjectUserID: subject, ContextType: policy.ContactGroup, ContextID: groupID,
		Reason: fmt.Sprintf("identifying reason %d", n), SubjectDecision: policy.ContactPending, Status: status,
		CreatedAt: created, ExpiresAt: created.Add(24 * 14 * time.Hour)}
	switch purpose {
	case policy.RequestContact:
		r.Fields, r.NeedsAdmin, r.AdminDecision = policy.ContactBoth, true, policy.ContactPending
	case policy.RequestGroupAdd:
		r.AdminDecision = policy.ContactNotNeeded
	case policy.RequestOccurrenceAdd:
		r.ContextType, r.ContextID, r.OccurrenceID = policy.ContactEvent, eventID, &occurrenceID
		r.AdminDecision, r.AddAttendanceMode = policy.ContactNotNeeded, models.AttendanceInPerson
		r.AddChildcareAgesNote = note
	}
	done := created.Add(10 * time.Minute)
	switch status {
	case policy.ContactWithdrawn:
		r.CompletedAt, r.WithdrawnByID = &done, &requester
	case policy.ContactDenied:
		decided := created.Add(5 * time.Minute)
		r.SubjectDecision, r.SubjectDecidedBy, r.SubjectDecidedAt = policy.ContactDenied, &subject, &decided
		r.CompletedAt = &done
	case policy.ContactEnded:
		r.CompletedAt, r.EndReason = &done, "event_ended"
	case policy.ContactExpired:
		r.CreatedAt = created.AddDate(0, 0, -30)
		r.ExpiresAt = r.CreatedAt.Add(24 * 14 * time.Hour)
		end := r.ExpiresAt.Add(time.Minute)
		r.CompletedAt = &end
	}
	erasureMustCreate(t, db, &r)
	return r
}

// erasureGrantedAdd makes a granted occurrence_add request for subject on occ
// the way the completion writes it, in one transaction with the real triggers
// enabled: the request, a registration (reg, when given, is reused for a
// replacement), the grant decision and the access provenance with its added
// snapshot. at is the instant of the grant. The request is created one minute
// before at: the baseline provenance trigger refuses a replacement request
// created before the access it replaces ended, so a one-hour lead put B ahead
// of A's end and failed the seed with 23514.
func erasureGrantedAdd(t *testing.T, db *gorm.DB, issuer, subject *models.User, eventID uint, occ models.EventOccurrence, note string, at time.Time, reg *models.Registration) (models.ContactRequest, models.Registration) {
	t.Helper()
	at = at.UTC().Truncate(time.Microsecond)
	req := models.ContactRequest{RequesterID: issuer.ID, SubjectUserID: subject.ID, Purpose: policy.RequestOccurrenceAdd,
		ContextType: policy.ContactEvent, ContextID: eventID, OccurrenceID: &occ.ID, AddAttendanceMode: models.AttendanceInPerson,
		AddChildcareAgesNote: note, Reason: "identifying reason granted", SubjectDecision: policy.ContactPending,
		AdminDecision: policy.ContactNotNeeded, Status: policy.ContactPending, CreatedAt: at.Add(-time.Minute), ExpiresAt: at.AddDate(0, 0, 14)}
	var out models.Registration
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&req).Error; err != nil {
			return err
		}
		if reg == nil {
			out = models.Registration{UserID: &subject.ID, OccurrenceID: occ.ID, Status: models.RegGoing, Source: "admin", AttendanceMode: models.AttendanceInPerson}
			if err := tx.Create(&out).Error; err != nil {
				return err
			}
		} else if err := tx.First(&out, reg.ID).Error; err != nil {
			return err
		}
		if err := tx.Model(&req).Updates(map[string]any{"subject_decision": policy.ContactApproved, "subject_decided_by": subject.ID,
			"subject_decided_at": at, "status": policy.ContactGranted, "completed_at": at, "completed_target_id": out.ID}).Error; err != nil {
			return err
		}
		issuerID := issuer.ID
		_, err := seedAccessGrantIn(tx, out.ID, accessGrant{Origin: policy.OccurrenceOriginManagerAdd, IssuerID: &issuerID, RequestID: &req.ID}, &issuerID, at)
		return err
	})
	if err != nil {
		t.Fatal("seed granted add request:", err)
	}
	if err := db.First(&out, out.ID).Error; err != nil {
		t.Fatal(err)
	}
	return req, out
}

// erasureNote is the ages note a granted-row builder writes: the text for the
// erased person's rows, empty for the twin's, whose erasure the helper cannot
// change and which therefore stands for the erasure before T07.
func erasureNote(noted bool, text string) string {
	if noted {
		return text
	}
	return ""
}

// erasureGrantedGroupAdd is a granted group_add request for subject into g,
// written the way the completion writes it: the request, the active membership
// it names and the grant decision in one transaction under the real triggers.
func erasureGrantedGroupAdd(t *testing.T, db *gorm.DB, requester, subject *models.User, g *models.Group) models.ContactRequest {
	t.Helper()
	at := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Microsecond)
	req := models.ContactRequest{RequesterID: requester.ID, SubjectUserID: subject.ID, Purpose: policy.RequestGroupAdd,
		ContextType: policy.ContactGroup, ContextID: g.ID, Reason: "identifying reason granted group add",
		SubjectDecision: policy.ContactPending, AdminDecision: policy.ContactNotNeeded, Status: policy.ContactPending,
		CreatedAt: at.Add(-time.Minute), ExpiresAt: at.AddDate(0, 0, 14)}
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&req).Error; err != nil {
			return err
		}
		m := models.Membership{UserID: subject.ID, GroupID: g.ID, Role: models.MembershipMember, Status: "active", JoinedAt: at}
		if err := tx.Create(&m).Error; err != nil {
			return err
		}
		return tx.Model(&req).Updates(map[string]any{"subject_decision": policy.ContactApproved, "subject_decided_by": subject.ID,
			"subject_decided_at": at, "status": policy.ContactGranted, "completed_at": at, "completed_target_id": m.ID}).Error
	})
	if err != nil {
		t.Fatal("seed granted group_add request:", err)
	}
	if err := db.First(&req, req.ID).Error; err != nil {
		t.Fatal(err)
	}
	return req
}

// erasureForgedGrantedAdd inserts a granted occurrence_add request with no
// completed registration behind it. Its deferred completion event fails with
// 23514 whenever it is checked, so it shows whether a queued event survived.
func erasureForgedGrantedAdd(tx *gorm.DB, requester, subject, eventID, occurrenceID uint) error {
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	decided, done, target := at.Add(time.Minute), at.Add(2*time.Minute), uint(2000000000)
	r := models.ContactRequest{RequesterID: requester, SubjectUserID: subject, Purpose: policy.RequestOccurrenceAdd,
		ContextType: policy.ContactEvent, ContextID: eventID, OccurrenceID: &occurrenceID, AddAttendanceMode: models.AttendanceInPerson,
		CompletedTargetID: &target, Reason: "forged granted add", SubjectDecision: policy.ContactApproved,
		SubjectDecidedBy: &subject, SubjectDecidedAt: &decided, AdminDecision: policy.ContactNotNeeded,
		Status: policy.ContactGranted, CreatedAt: at, ExpiresAt: at.AddDate(0, 0, 14), CompletedAt: &done}
	return tx.Create(&r).Error
}

// erasureReplacedAdd is historical case (a): request A was granted on a
// registration, its access ended, and request B then took the same
// registration over. Both notes are nonempty.
func erasureReplacedAdd(t *testing.T, db *gorm.DB, issuer, subject *models.User, eventID uint, occ models.EventOccurrence, noted bool) (a, b models.ContactRequest, reg models.Registration) {
	t.Helper()
	first := time.Now().UTC().Add(-3 * time.Hour)
	a, reg = erasureGrantedAdd(t, db, issuer, subject, eventID, occ, erasureNote(noted, "ages note A (replaced)"), first, nil)
	endSeededAccess(t, db, reg.ID, policy.OccurrenceRemoved, &issuer.ID, first.Add(10*time.Minute))
	b, reg = erasureGrantedAdd(t, db, issuer, subject, eventID, occ, erasureNote(noted, "ages note B (current)"), first.Add(30*time.Minute), &reg)
	return a, b, reg
}

// erasureRetiredAdd is historical case (b): a granted request whose
// registration was retired (access ended, both snapshots written) and deleted.
func erasureRetiredAdd(t *testing.T, db *gorm.DB, issuer, subject *models.User, eventID uint, occ models.EventOccurrence, noted bool) models.ContactRequest {
	t.Helper()
	at := time.Now().UTC().Add(-3 * time.Hour)
	req, reg := erasureGrantedAdd(t, db, issuer, subject, eventID, occ, erasureNote(noted, "ages note (registration deleted)"), at, nil)
	endSeededAccess(t, db, reg.ID, policy.OccurrenceRemoved, &issuer.ID, at.Add(10*time.Minute))
	if err := db.Delete(&models.Registration{}, reg.ID).Error; err != nil {
		t.Fatal("delete the retired registration:", err)
	}
	return req
}

// erasureEndedAccessAdd is historical case (c), the control: the access only
// ended and the registration is still in place.
func erasureEndedAccessAdd(t *testing.T, db *gorm.DB, issuer, subject *models.User, eventID uint, occ models.EventOccurrence, noted bool) models.ContactRequest {
	t.Helper()
	at := time.Now().UTC().Add(-3 * time.Hour)
	req, reg := erasureGrantedAdd(t, db, issuer, subject, eventID, occ, erasureNote(noted, "ages note (access ended)"), at, nil)
	endSeededAccess(t, db, reg.ID, policy.OccurrenceRemoved, &issuer.ID, at.Add(10*time.Minute))
	return req
}

// The classes of fixture row in the ages-note erasure test: the erased person
// is the subject, is the requester, or is neither.
const (
	erasureAsSubject   = "subject"
	erasureAsRequester = "requester"
	erasureNeither     = "neither"
)

// erasureSeedRequestTasks writes the waiting review tasks of a pending request the way
// request creation does (contactNotice): the subject's, and the admin's when
// the request needs one.
func erasureSeedRequestTasks(t *testing.T, db *gorm.DB, r models.ContactRequest, admin uint) {
	t.Helper()
	err := db.Transaction(func(tx *gorm.DB) error {
		if _, err := contactNotice(tx, r, notify.ContactSubjectReview, []uint{r.SubjectUserID}, "subject-review"); err != nil {
			return err
		}
		if !r.NeedsAdmin {
			return nil
		}
		_, err := contactNotice(tx, r, notify.ContactAdminReview, []uint{admin}, "admin-review")
		return err
	})
	if err != nil {
		t.Fatalf("seed the review tasks of request %d: %v", r.ID, err)
	}
}

// erasureTask is one review task a pending request leaves open: its kind and
// the user who holds it.
type erasureTask struct {
	Kind  string
	Owner uint
}

// erasureReviewTasks is who holds a task for a pending request: its subject,
// and the reviewing admin when the request needs one.
func erasureReviewTasks(r models.ContactRequest, admin uint) []erasureTask {
	subjectKind, adminKind := notify.ContactSubjectReview, notify.ContactAdminReview
	if r.Purpose != policy.RequestContact {
		subjectKind, adminKind = notify.AddSubjectReview, notify.AddAdminReview
	}
	out := []erasureTask{{subjectKind, r.SubjectUserID}}
	if r.NeedsAdmin {
		out = append(out, erasureTask{adminKind, admin})
	}
	return out
}

// erasureSpan is the wall-clock window of one DELETE: from just before the
// request to just after the response.
type erasureSpan struct{ Start, End time.Time }

func (s erasureSpan) has(at time.Time) bool {
	return !at.Before(s.Start.Truncate(time.Microsecond)) && !at.After(s.End)
}

func (s erasureSpan) hasPtr(at *time.Time) bool { return at != nil && s.has(*at) }

func (s erasureSpan) hasMicro(micro int64) bool {
	return micro >= s.Start.Truncate(time.Microsecond).UnixMicro() && micro <= s.End.UnixMicro()
}

func erasureJSON(t testing.TB, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// erasureMetaOf decodes a JSON object the way an audit row's meta reads back:
// numbers as float64.
func erasureMetaOf(t testing.TB, raw string) map[string]any {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("not a JSON object: %q: %v", raw, err)
	}
	return m
}

// erasureWire is a wanted meta map as it reads back from the database.
func erasureWire(t testing.TB, want map[string]any) map[string]any {
	t.Helper()
	return erasureMetaOf(t, erasureJSON(t, want))
}

func erasureSameID(a, b *uint) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// erasureLedger is every notice, delivery, job and audit row, by id. A run is
// judged by the rows it added (ids absent before) and by what became of each
// row that was there.
type erasureLedger struct {
	Notices    map[uint]models.Notification
	Deliveries map[uint]models.NotificationDelivery
	Jobs       map[uint]models.Job
	Audit      map[uint]models.AuditLog
}

func erasureLedgerOf(t testing.TB, db *gorm.DB) erasureLedger {
	t.Helper()
	l := erasureLedger{Notices: map[uint]models.Notification{}, Deliveries: map[uint]models.NotificationDelivery{},
		Jobs: map[uint]models.Job{}, Audit: map[uint]models.AuditLog{}}
	var notices []models.Notification
	var deliveries []models.NotificationDelivery
	var queued []models.Job
	var logs []models.AuditLog
	for _, load := range []struct {
		name string
		dest any
	}{{"notifications", &notices}, {"notification_deliveries", &deliveries}, {"jobs", &queued}, {"audit_logs", &logs}} {
		if err := db.Order("id").Find(load.dest).Error; err != nil {
			t.Fatalf("read %s: %v", load.name, err)
		}
	}
	for _, n := range notices {
		l.Notices[n.ID] = n
	}
	for _, d := range deliveries {
		l.Deliveries[d.ID] = d
	}
	for _, j := range queued {
		l.Jobs[j.ID] = j
	}
	for _, a := range logs {
		l.Audit[a.ID] = a
	}
	return l
}

// erasureRun is what one DELETE of one person is judged against.
type erasureRun struct {
	Name    string
	Subject uint // the person erased
	Admin   uint // the admin who deletes them and reviews contact requests
	// Pending is every request that was pending and names Subject, as seeded.
	Pending   []models.ContactRequest
	Span      erasureSpan
	Reg       models.Registration // the replacement registration with live access, before the DELETE
	RegAfter  models.Registration // and after it
	GroupID   uint
	EventID   uint // the event of Reg
	IssuerID  uint // who granted the access
	RequestID uint // the add request the access names
}

// erasureLedgerFacts is what checkErasureLedger saw, for the checks that span
// both runs.
type erasureLedgerFacts struct {
	Name       string
	EndedMeta  map[string]any // meta of the one new occurrence_access.ended snapshot
	Living     int            // waiting tasks of other people the DELETE closed
	Deleted    int            // waiting tasks of the erased person that are gone
	NewNotices int
}

// erasureEndMeta is the meta of the audit row that ends a pending request
// because its person is gone (finishContactRequest, contactAudit).
func erasureEndMeta(r models.ContactRequest) map[string]any {
	if r.Purpose == policy.RequestContact {
		return map[string]any{"requesterId": r.RequesterID, "subjectUserId": r.SubjectUserID, "contextType": r.ContextType,
			"contextId": r.ContextID, "fields": r.Fields, "needsAdmin": r.NeedsAdmin, "outcome": policy.ContactEnded,
			"reason": policy.ContactAccountDisabled}
	}
	m := map[string]any{"purpose": r.Purpose, "requesterId": r.RequesterID, "subjectUserId": r.SubjectUserID,
		"contextType": r.ContextType, "contextId": r.ContextID, "needsAdmin": r.NeedsAdmin, "outcome": policy.ContactEnded,
		"reason": policy.ContactAccountDisabled}
	if r.Purpose == policy.RequestOccurrenceAdd {
		m["occurrenceId"] = *r.OccurrenceID
	}
	return m
}

// erasureWantNotice is the "updated" notice a pending request's end sends to
// the party that is still there, as the registry renders it.
func erasureWantNotice(t testing.TB, r models.ContactRequest, to uint) models.Notification {
	t.Helper()
	kind, subjectType, link, prefix := notify.ContactUpdated, "contact_request", "contact-requests", "contact"
	if r.Purpose != policy.RequestContact {
		kind, subjectType, link, prefix = notify.AddUpdated, "add_request", "add-requests", "add"
	}
	k, ok := notify.Lookup(kind)
	if !ok {
		t.Fatalf("kind %q is not registered", kind)
	}
	v := notify.Vars{Teen: r.NeedsAdmin, Count: 1}
	title, body := k.Render(v)
	emailTitle, emailBody := k.RenderForEmail(v)
	pushTitle, pushBody, _ := k.RenderForPush(v)
	id := r.ID
	dedupe := fmt.Sprintf("%s:%d:%s", prefix, r.ID, policy.ContactEnded)
	return models.Notification{UserID: to, Kind: kind, Category: notify.CatSafety, SubjectType: subjectType, SubjectID: &id,
		DedupeKey: &dedupe, Title: title, Body: body, EmailTitle: emailTitle, EmailBody: emailBody, PushTitle: pushTitle, PushBody: pushBody,
		Link: fmt.Sprintf("/%s/%d", link, r.ID), Actions: "[]", BatchCount: 1, Data: "{}"}
}

// erasureAccessColumns is the identity and access columns of a registration as
// one JSON string. A DELETE may change only the ending three of them.
func erasureAccessColumns(t testing.TB, r models.Registration) string {
	t.Helper()
	return erasureJSON(t, struct {
		ID                  uint
		UserID              *uint
		OccurrenceID        uint
		Status              string
		Origin              string
		Issuer              *uint
		Request             *uint
		DepartureMembership *uint
		DepartureActor      *uint
		GrantedAt           *time.Time
		EndedAt             *time.Time
		EndReason           string
		EndedBy             *uint
	}{r.ID, r.UserID, r.OccurrenceID, r.Status, r.AccessOrigin, r.AccessIssuerID, r.AccessRequestID,
		r.AccessDepartureMembershipID, r.AccessDepartureActorID, r.AccessGrantedAt, r.AccessEndedAt, r.AccessEndReason, r.AccessEndedBy})
}

// checkErasureRequests judges the contact_requests rows of one DELETE. Every row
// that does not name the erased person is byte-identical (the other run's rows
// included). A row that was pending and names them ends as "ended" with
// "account_disabled", completed inside the DELETE, and keeps every other column
// it had: status, end_reason and completed_at are the lifecycle's, and the note
// of the subject's occurrence_add row is the scrub's, and nothing else moves.
// The rest of the person's rows keep their status, decision, reason and
// timestamps; the subject's occurrence_add rows differ by the note alone.
func checkErasureRequests(t *testing.T, run erasureRun, before, after map[uint]erasureAgesNoteRow) {
	t.Helper()
	if len(after) != len(before) {
		t.Fatalf("%s: %d contact_requests rows after the DELETE, %d before", run.Name, len(after), len(before))
	}
	pending := map[uint]bool{}
	for _, r := range run.Pending {
		pending[r.ID] = true
	}
	endedPending := 0
	for id, b := range before {
		a, ok := after[id]
		if !ok {
			t.Errorf("%s: request %d is gone", run.Name, id)
			continue
		}
		label := fmt.Sprintf("%s: request %d (%s %s)", run.Name, id, b.Purpose, b.Status)
		subjectAdd := b.Purpose == policy.RequestOccurrenceAdd && b.SubjectUserID == run.Subject
		involved := b.SubjectUserID == run.Subject || b.RequesterID == run.Subject
		if a.Reason != b.Reason {
			t.Errorf("%s: reason changed to %q, was %q", label, a.Reason, b.Reason)
		}
		if subjectAdd && a.Note != "" {
			t.Errorf("%s: subject note is %q, want empty", label, a.Note)
		}
		if !subjectAdd && a.Note != b.Note {
			t.Errorf("%s: note is %q, was %q", label, a.Note, b.Note)
		}
		switch {
		case !involved:
			if a.Whole != b.Whole {
				t.Errorf("%s: a row naming %d in neither role changed:\n%s\n%s", label, run.Subject, b.Whole, a.Whole)
			}
		case b.Status == policy.ContactPending:
			if !pending[id] {
				t.Errorf("%s: pending row naming the person is not in the run's fixture list", label)
			}
			endedPending++
			if a.Status != policy.ContactEnded || a.EndReason != policy.ContactAccountDisabled {
				t.Errorf("%s: ended as status %q end_reason %q, want %q and %q", label, a.Status, a.EndReason, policy.ContactEnded, policy.ContactAccountDisabled)
			}
			if !run.Span.hasPtr(a.CompletedAt) {
				t.Errorf("%s: completed_at %v is outside the DELETE (%v to %v)", label, a.CompletedAt, run.Span.Start, run.Span.End)
			}
			if a.Preserved != b.Preserved {
				t.Errorf("%s: a column other than status, end_reason, completed_at and the subject's note changed:\n%s\n%s", label, b.Preserved, a.Preserved)
			}
		case subjectAdd:
			if a.Rest != b.Rest {
				t.Errorf("%s: more than the note changed:\n%s\n%s", label, b.Rest, a.Rest)
			}
		default:
			if a.Whole != b.Whole {
				t.Errorf("%s: a settled row of the person changed:\n%s\n%s", label, b.Whole, a.Whole)
			}
		}
	}
	if endedPending != len(run.Pending) {
		t.Errorf("%s: %d pending rows naming the person ended, want %d", run.Name, endedPending, len(run.Pending))
	}
}

// erasurePreservedShape is the Preserved projection of a row with the fixture
// mapping applied: the ids of the erased person and their twin become "person",
// the row's own id is dropped and every timestamp is reduced to whether it is
// set (a row and its twin are seeded at different instants).
func erasurePreservedShape(t testing.TB, preserved string, erased, twin uint) map[string]any {
	t.Helper()
	m := erasureMetaOf(t, preserved)
	delete(m, "id")
	for _, k := range []string{"requester_id", "subject_user_id", "subject_decided_by", "admin_decided_by", "withdrawn_by_id"} {
		if id, ok := m[k].(float64); ok && (uint(id) == erased || uint(id) == twin) {
			m[k] = "person"
		}
	}
	for _, k := range []string{"created_at", "expires_at", "subject_decided_at", "admin_decided_at"} {
		if m[k] != nil {
			m[k] = "set"
		}
	}
	return m
}

// checkErasurePairs sets each erased-side request beside its twin's, by the ids
// the fixture recorded: one to one, the same purpose, status and reason, the
// person in the same role, and after both DELETEs the same status and end
// reason and the same preserved columns under erasurePreservedShape.
func checkErasurePairs(t *testing.T, erased, twin uint, twinOf map[uint]uint, before, after map[uint]erasureAgesNoteRow) {
	t.Helper()
	seen := map[uint]uint{}
	for id, tid := range twinOf {
		if prev, dup := seen[tid]; dup {
			t.Errorf("twin request %d is paired with both %d and %d", tid, prev, id)
			continue
		}
		seen[tid] = id
		be, bt, ae, at := before[id], before[tid], after[id], after[tid]
		label := fmt.Sprintf("request %d (%s %s) and its twin %d", id, be.Purpose, be.Status, tid)
		switch {
		case be.SubjectUserID == erased:
			if bt.SubjectUserID != twin || bt.RequesterID != be.RequesterID {
				t.Errorf("%s: the twin row has requester %d and subject %d, want %d and %d", label, bt.RequesterID, bt.SubjectUserID, be.RequesterID, twin)
			}
		case be.RequesterID == erased:
			if bt.RequesterID != twin || bt.SubjectUserID != be.SubjectUserID {
				t.Errorf("%s: the twin row has requester %d and subject %d, want %d and %d", label, bt.RequesterID, bt.SubjectUserID, twin, be.SubjectUserID)
			}
		default:
			t.Errorf("%s: the erased-side row does not name the erased person", label)
			continue
		}
		if be.Purpose != bt.Purpose || be.Status != bt.Status || be.Reason != bt.Reason {
			t.Errorf("%s: purpose/status/reason differ before the DELETE: %q/%q/%q and %q/%q/%q", label, be.Purpose, be.Status, be.Reason, bt.Purpose, bt.Status, bt.Reason)
		}
		if ae.Status != at.Status || ae.EndReason != at.EndReason || (ae.CompletedAt == nil) != (at.CompletedAt == nil) {
			t.Errorf("%s: after the DELETE the rows have status %q/%q, end reason %q/%q, completed %t/%t",
				label, ae.Status, at.Status, ae.EndReason, at.EndReason, ae.CompletedAt != nil, at.CompletedAt != nil)
		}
		for what, pair := range map[string][2]erasureAgesNoteRow{"before": {be, bt}, "after": {ae, at}} {
			if !reflect.DeepEqual(erasurePreservedShape(t, pair[0].Preserved, erased, twin), erasurePreservedShape(t, pair[1].Preserved, erased, twin)) {
				t.Errorf("%s: the preserved columns differ %s the DELETE:\n%s\n%s", label, what, pair[0].Preserved, pair[1].Preserved)
			}
		}
	}
}

// The contact/ages-note ledger fixtures use signed-in actors for these actions.
// Keep this expectation independent of auditIPAttribution. It is deliberately
// limited to these fixtures, not a second implementation of the IP selector.
// With gone = 0 it only normalizes JSON for comparison after key removal.
func erasureFixtureAuditAfter(t *testing.T, row models.AuditLog, gone uint) models.AuditLog {
	t.Helper()
	meta := erasureMetaOf(t, row.MetaJSON)
	if gone != 0 {
		if row.ActorID != nil && *row.ActorID == gone {
			switch row.Action {
			case audit.AddRequested, audit.AddDecision, audit.AddWithdrawn, audit.AddCompleted, audit.AddRefused, audit.AddEnded,
				audit.ContactRequested, audit.ContactDecision, audit.ContactWithdrawn, audit.ContactGranted, audit.ContactReveal,
				audit.ContactRefused, audit.ContactRevoked, audit.ContactEnded, audit.OccurrenceAccessAdded, audit.OccurrenceAccessEnded,
				audit.OccurrenceBlocked, audit.RegistrationAdded, audit.RegistrationRemoved, audit.RegistrationChanged, audit.EventCreated:
				row.IP = ""
			}
		}
		if row.Action == audit.RegistrationRemoved && meta["userId"] == float64(gone) {
			delete(meta, "note")
		}
	}
	row.MetaJSON = erasureJSON(t, meta)
	return row
}

// checkErasureLedger judges the audit, notification, delivery and job rows of
// one DELETE against the expected rows stated in full:
//
//   - audit: exactly user.deleted, one end row per pending request that named
//     the person (contact.ended or add.ended, no actor) and one
//     occurrence_access.ended snapshot of the replacement registration, each
//     with its complete meta; earlier rows keep everything except the erased
//     actor's IP and the subject's registration removal note (R4-4a);
//   - the replacement registration changes only its ending columns (reason
//     "account_disabled", no ender, an end inside the DELETE);
//   - notices: exactly one contact.updated or add.updated per pending request,
//     to the party still there, rendered by the registry, with the link, the
//     dedupe key and the subject of its request; none to the erased person,
//     none "occurrence.updated" for the erased holder of the access; the person
//     keeps no notice or delivery row; the review tasks of other people on
//     these requests close as "ended" (no resolver) and are otherwise
//     unchanged; every other notice is byte-identical;
//   - deliveries: one queued email row per new notice, none for the person;
//     others unchanged;
//   - jobs: one notify.deliver job per new notice, one avatar.cleanup job for
//     the person, nothing else; earlier jobs unchanged.
func checkErasureLedger(t *testing.T, run erasureRun, before, after erasureLedger) erasureLedgerFacts {
	t.Helper()
	name, span := run.Name, run.Span
	facts := erasureLedgerFacts{Name: name}

	// The replacement registration: only the ending moved.
	reg, regAfter := run.Reg, run.RegAfter
	if reg.AccessGrantedAt == nil || reg.AccessEndedAt != nil || reg.AccessOrigin != policy.OccurrenceOriginManagerAdd ||
		!erasureSameID(reg.AccessIssuerID, &run.IssuerID) || !erasureSameID(reg.AccessRequestID, &run.RequestID) {
		t.Fatalf("%s: the registration was not seeded with live access granted by %d for request %d: %+v", name, run.IssuerID, run.RequestID, reg)
	}
	if regAfter.AccessEndedAt == nil {
		t.Fatalf("%s: registration %d still has live access after the DELETE", name, reg.ID)
	}
	wantReg := reg
	wantReg.AccessEndedAt, wantReg.AccessEndReason, wantReg.AccessEndedBy = regAfter.AccessEndedAt, policy.ContactAccountDisabled, nil
	if got, want := erasureAccessColumns(t, regAfter), erasureAccessColumns(t, wantReg); got != want {
		t.Errorf("%s: the access columns of registration %d after the DELETE:\n%s\nwant (only the ending, reason %q, no ender):\n%s", name, reg.ID, got, policy.ContactAccountDisabled, want)
	}
	endedMicro := accessMicro(*regAfter.AccessEndedAt)
	if !span.hasMicro(endedMicro) {
		t.Errorf("%s: access ended at %v, outside the DELETE (%v to %v)", name, regAfter.AccessEndedAt, span.Start, span.End)
	}

	// Audit.
	for id, b := range before.Audit {
		b = erasureFixtureAuditAfter(t, b, run.Subject)
		a, ok := after.Audit[id]
		if ok {
			a = erasureFixtureAuditAfter(t, a, 0)
		}
		if !ok || erasureJSON(t, a) != erasureJSON(t, b) {
			t.Errorf("%s: audit row %d (%s) changed or is gone", name, id, b.Action)
		}
	}
	type auditKey struct {
		Type   string
		ID     uint
		Action string
	}
	type wantAudit struct {
		Actor *uint
		Meta  map[string]any
		AnyIP bool
	}
	wantRows := map[auditKey]wantAudit{}
	addAudit := func(k auditKey, w wantAudit) {
		if _, dup := wantRows[k]; dup {
			t.Fatalf("%s: the fixture expects two audit rows %+v", name, k)
		}
		wantRows[k] = w
	}
	admin := run.Admin
	addAudit(auditKey{"user", run.Subject, audit.UserDeleted}, wantAudit{Actor: &admin, Meta: map[string]any{}, AnyIP: true})
	for _, r := range run.Pending {
		action, targetType := audit.ContactEnded, "contact_request"
		if r.Purpose != policy.RequestContact {
			action, targetType = audit.AddEnded, "add_request"
		}
		addAudit(auditKey{targetType, r.ID, action}, wantAudit{Meta: erasureEndMeta(r)})
	}
	addAudit(auditKey{accessTargetType, reg.ID, audit.OccurrenceAccessEnded}, wantAudit{Meta: map[string]any{
		"subjectUserId": run.Subject, "occurrenceId": reg.OccurrenceID, "eventId": run.EventID,
		"organizerType": policy.ContactGroup, "organizerId": run.GroupID, "registrationId": reg.ID,
		"origin": policy.OccurrenceOriginManagerAdd, "grantedAtMicro": accessMicro(*reg.AccessGrantedAt),
		"issuerId": run.IssuerID, "requestId": run.RequestID,
		"endedAtMicro": endedMicro, "endReason": policy.ContactAccountDisabled, "outcome": policy.OccurrenceAccessEnded}})
	seenAudit := map[auditKey]bool{}
	for id, a := range after.Audit {
		if _, old := before.Audit[id]; old {
			continue
		}
		k := auditKey{a.TargetType, a.TargetID, a.Action}
		w, ok := wantRows[k]
		switch {
		case !ok:
			t.Errorf("%s: unexpected new audit row %d: %s on %s %d, meta %s", name, id, a.Action, a.TargetType, a.TargetID, a.MetaJSON)
			continue
		case seenAudit[k]:
			t.Errorf("%s: audit row %+v was written twice", name, k)
			continue
		}
		seenAudit[k] = true
		if !erasureSameID(a.ActorID, w.Actor) {
			t.Errorf("%s: audit %+v has actor %v, want %v", name, k, a.ActorID, w.Actor)
		}
		if !w.AnyIP && a.IP != "" {
			t.Errorf("%s: audit %+v has ip %q, want none", name, k, a.IP)
		}
		if !span.has(a.At) {
			t.Errorf("%s: audit %+v written at %v, outside the DELETE (%v to %v)", name, k, a.At, span.Start, span.End)
		}
		got := erasureMetaOf(t, a.MetaJSON)
		if want := erasureWire(t, w.Meta); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: audit %+v has meta %v, want %v", name, k, got, want)
		}
		if a.Action == audit.OccurrenceAccessEnded {
			facts.EndedMeta = got
		}
	}
	for k := range wantRows {
		if !seenAudit[k] {
			t.Errorf("%s: expected audit row %+v was not written", name, k)
		}
	}
	endedSnapshots := 0
	for id, a := range after.Audit {
		if _, old := before.Audit[id]; !old && a.Action == audit.OccurrenceAccessEnded && a.TargetType == accessTargetType && a.TargetID == reg.ID {
			endedSnapshots++
		}
	}
	if endedSnapshots != 1 {
		t.Errorf("%s: %d new ended snapshots for registration %d, want exactly 1", name, endedSnapshots, reg.ID)
	}

	// Notices: the person's are gone; the tasks of others close; nothing else moves.
	closed := map[uint]bool{}
	for _, r := range run.Pending {
		for _, task := range erasureReviewTasks(r, run.Admin) {
			var open []models.Notification
			for _, n := range before.Notices {
				if n.UserID == task.Owner && n.Kind == task.Kind && n.SubjectID != nil && *n.SubjectID == r.ID && n.Waiting && n.ResolvedAt == nil {
					open = append(open, n)
				}
			}
			if len(open) != 1 {
				t.Fatalf("%s: request %d: %d open %s tasks for user %d before the DELETE, want 1", name, r.ID, len(open), task.Kind, task.Owner)
			}
			b := open[0]
			a, kept := after.Notices[b.ID]
			if task.Owner == run.Subject {
				if kept {
					t.Errorf("%s: the erased person's task %d (%s) survives", name, b.ID, task.Kind)
				}
				facts.Deleted++
				continue
			}
			if !kept {
				t.Errorf("%s: the task %d (%s) of user %d is gone", name, b.ID, task.Kind, task.Owner)
				continue
			}
			closed[b.ID] = true
			facts.Living++
			if !span.hasPtr(a.ResolvedAt) || a.ResolvedByID != nil || a.Resolution != policy.ContactEnded {
				t.Errorf("%s: task %d (%s) resolved at %v by %v as %q, want inside the DELETE, nobody, %q", name, b.ID, task.Kind, a.ResolvedAt, a.ResolvedByID, a.Resolution, policy.ContactEnded)
			}
			want := b
			want.ResolvedAt, want.ResolvedByID, want.Resolution = a.ResolvedAt, nil, policy.ContactEnded
			if erasureJSON(t, a) != erasureJSON(t, want) {
				t.Errorf("%s: task %d changed beyond its resolution:\n%s\n%s", name, b.ID, erasureJSON(t, b), erasureJSON(t, a))
			}
		}
	}
	for id, b := range before.Notices {
		a, kept := after.Notices[id]
		switch {
		case b.UserID == run.Subject:
			if kept {
				t.Errorf("%s: notice %d of the erased person survives", name, id)
			}
		case closed[id]:
		case !kept:
			t.Errorf("%s: notice %d (%s) of user %d is gone", name, id, b.Kind, b.UserID)
		case erasureJSON(t, a) != erasureJSON(t, b):
			t.Errorf("%s: notice %d (%s) of user %d changed:\n%s\n%s", name, id, b.Kind, b.UserID, erasureJSON(t, b), erasureJSON(t, a))
		}
	}
	for id, n := range after.Notices {
		if n.UserID == run.Subject {
			t.Errorf("%s: the erased person holds notice %d (%s)", name, id, n.Kind)
		}
	}
	for id, d := range after.Deliveries {
		if d.UserID == run.Subject {
			t.Errorf("%s: the erased person holds delivery %d", name, id)
		}
	}
	wantNotices := map[string]models.Notification{}
	for _, r := range run.Pending {
		to := r.RequesterID
		if r.RequesterID == run.Subject {
			to = r.SubjectUserID
		}
		w := erasureWantNotice(t, r, to)
		wantNotices[fmt.Sprintf("%d|%s", w.UserID, *w.DedupeKey)] = w
	}
	if len(wantNotices) != len(run.Pending) {
		t.Fatalf("%s: the fixture expects %d distinct notices for %d pending requests", name, len(wantNotices), len(run.Pending))
	}
	shape := func(n models.Notification) string {
		n.ID, n.CreatedAt, n.UpdatedAt = 0, time.Time{}, time.Time{}
		return erasureJSON(t, n)
	}
	matched := map[uint]models.Notification{}
	for id, n := range after.Notices {
		if _, old := before.Notices[id]; old {
			continue
		}
		if n.DedupeKey == nil {
			t.Errorf("%s: unexpected new notice %d (%s) for user %d", name, id, n.Kind, n.UserID)
			continue
		}
		key := fmt.Sprintf("%d|%s", n.UserID, *n.DedupeKey)
		w, ok := wantNotices[key]
		if !ok {
			t.Errorf("%s: unexpected new notice %d: %s for user %d, key %s, subject %s %v", name, id, n.Kind, n.UserID, *n.DedupeKey, n.SubjectType, n.SubjectID)
			continue
		}
		delete(wantNotices, key)
		matched[id] = n
		if !span.has(n.CreatedAt) || !n.CreatedAt.Equal(n.UpdatedAt) {
			t.Errorf("%s: notice %d created %v updated %v, want equal and inside the DELETE", name, id, n.CreatedAt, n.UpdatedAt)
		}
		if got, want := shape(n), shape(w); got != want {
			t.Errorf("%s: notice %d:\n%s\nwant:\n%s", name, id, got, want)
		}
	}
	for key := range wantNotices {
		t.Errorf("%s: expected notice %s was not written", name, key)
	}
	facts.NewNotices = len(matched)

	// Deliveries.
	for id, b := range before.Deliveries {
		a, kept := after.Deliveries[id]
		switch {
		case b.UserID == run.Subject:
			if kept {
				t.Errorf("%s: delivery %d of the erased person survives", name, id)
			}
		case !kept || erasureJSON(t, a) != erasureJSON(t, b):
			t.Errorf("%s: delivery %d of user %d changed or is gone", name, id, b.UserID)
		}
	}
	newDeliveries := map[uint]models.NotificationDelivery{}
	for id, d := range after.Deliveries {
		if _, old := before.Deliveries[id]; old {
			continue
		}
		if _, dup := newDeliveries[d.NotificationID]; dup {
			t.Errorf("%s: notice %d has two new deliveries", name, d.NotificationID)
		}
		newDeliveries[d.NotificationID] = d
	}
	for id, n := range matched {
		d, ok := newDeliveries[id]
		if !ok {
			t.Errorf("%s: notice %d has no delivery row", name, id)
			continue
		}
		delete(newDeliveries, id)
		if !span.has(d.CreatedAt) || !span.has(d.UpdatedAt) {
			t.Errorf("%s: delivery %d written at %v/%v, outside the DELETE", name, d.ID, d.CreatedAt, d.UpdatedAt)
		}
		d.ID, d.CreatedAt, d.UpdatedAt = 0, time.Time{}, time.Time{}
		want := models.NotificationDelivery{NotificationID: id, Channel: models.DeliveryEmail, UserID: n.UserID, Status: models.DeliveryQueued}
		if erasureJSON(t, d) != erasureJSON(t, want) {
			t.Errorf("%s: delivery of notice %d:\n%s\nwant:\n%s", name, id, erasureJSON(t, d), erasureJSON(t, want))
		}
	}
	for id, d := range newDeliveries {
		t.Errorf("%s: unexpected new delivery for notice %d (user %d)", name, id, d.UserID)
	}

	// Jobs.
	for id, b := range before.Jobs {
		if a, ok := after.Jobs[id]; !ok || erasureJSON(t, a) != erasureJSON(t, b) {
			t.Errorf("%s: job %d (%s) changed or is gone", name, id, b.Kind)
		}
	}
	queuedJob := func(j models.Job, maxAttempts int) {
		t.Helper()
		if j.Status != models.JobQueued || j.Attempts != 0 || j.MaxAttempts != maxAttempts || j.LockedAt != nil || j.LockedBy != "" || j.LastError != "" ||
			!span.has(j.RunAt) || !span.has(j.CreatedAt) {
			t.Errorf("%s: job %d (%s) is %s with %d attempts of %d, run at %v, created %v; want queued, no attempts, max %d, inside the DELETE",
				name, j.ID, j.Kind, j.Status, j.Attempts, j.MaxAttempts, j.RunAt, j.CreatedAt, maxAttempts)
		}
	}
	deliverJobs := map[string]models.Job{}
	var cleanups []models.Job
	for id, j := range after.Jobs {
		if _, old := before.Jobs[id]; old {
			continue
		}
		switch {
		case j.Kind == notify.DeliverKind && j.UniqueKey != nil:
			deliverJobs[*j.UniqueKey] = j
		case j.Kind == kindAvatarCleanup:
			cleanups = append(cleanups, j)
		default:
			t.Errorf("%s: unexpected new job %d (%s) %s", name, id, j.Kind, j.Payload)
		}
	}
	for id := range matched {
		key := notify.DeliverKey(id)
		j, ok := deliverJobs[key]
		if !ok {
			t.Errorf("%s: notice %d has no delivery job", name, id)
			continue
		}
		delete(deliverJobs, key)
		queuedJob(j, 8)
		if got, want := erasureMetaOf(t, j.Payload), map[string]any{"notificationId": float64(id)}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: job %d payload %v, want %v", name, j.ID, got, want)
		}
	}
	for key, j := range deliverJobs {
		t.Errorf("%s: unexpected delivery job %s (%s)", name, key, j.Payload)
	}
	if len(cleanups) != 1 {
		t.Errorf("%s: %d new avatar cleanup jobs, want 1", name, len(cleanups))
	}
	for _, j := range cleanups {
		queuedJob(j, jobs.DefaultMaxAttempts)
		if got, want := erasureMetaOf(t, j.Payload), map[string]any{"userId": float64(run.Subject)}; !reflect.DeepEqual(got, want) || j.UniqueKey != nil {
			t.Errorf("%s: cleanup job %d payload %v key %v, want %v and none", name, j.ID, got, j.UniqueKey, want)
		}
	}
	return facts
}

// agesNoteStatement is one UPDATE of the ages-note scrub as the recorder saw
// it: the bind parameters it carried and the rows it changed.
type agesNoteStatement struct {
	Vars int
	Rows int64
}

// agesNoteRecorder is a test-only pair of GORM callbacks. The After gorm:row
// one keeps the SQL of the lock selection (Raw(...).Scan runs the row
// processor), the After gorm:raw one the shape of each ages-note UPDATE.
type agesNoteRecorder struct {
	mu      sync.Mutex
	selects []string
	updates []agesNoteStatement
}

const (
	agesNoteSelectPrefix = "SELECT id FROM contact_requests WHERE purpose = 'occurrence_add'"
	agesNoteUpdatePrefix = "UPDATE contact_requests SET add_childcare_ages_note"
)

func recordAgesNoteStatements(t *testing.T, db *gorm.DB) *agesNoteRecorder {
	t.Helper()
	rec := &agesNoteRecorder{}
	seq := queryCounterSeq.Add(1)
	rowName := fmt.Sprintf("test:ages_note_select:%d", seq)
	rawName := fmt.Sprintf("test:ages_note_update:%d", seq)
	collapse := func(tx *gorm.DB) string { return strings.Join(strings.Fields(tx.Statement.SQL.String()), " ") }
	if err := db.Callback().Row().After("gorm:row").Register(rowName, func(tx *gorm.DB) {
		if sql := collapse(tx); strings.HasPrefix(sql, agesNoteSelectPrefix) {
			rec.mu.Lock()
			rec.selects = append(rec.selects, sql)
			rec.mu.Unlock()
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Raw().After("gorm:raw").Register(rawName, func(tx *gorm.DB) {
		if strings.HasPrefix(collapse(tx), agesNoteUpdatePrefix) {
			rec.mu.Lock()
			rec.updates = append(rec.updates, agesNoteStatement{Vars: len(tx.Statement.Vars), Rows: tx.RowsAffected})
			rec.mu.Unlock()
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Row().Remove(rowName)
		_ = db.Callback().Raw().Remove(rawName)
	})
	return rec
}

func (r *agesNoteRecorder) seen() ([]string, []agesNoteStatement) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.selects...), append([]agesNoteStatement(nil), r.updates...)
}

// erasureTestCountThrottleFailures fails the erasure's last statement, the
// owned throttle delete, and counts how often it did, so a test can show the
// late failure it meant to force really fired.
func erasureTestCountThrottleFailures(t *testing.T, db *gorm.DB) *atomic.Int64 {
	t.Helper()
	var fired atomic.Int64
	name := fmt.Sprintf("test:count_throttle_failures:%d", queryCounterSeq.Add(1))
	if err := db.Callback().Raw().Before("gorm:raw").Register(name, func(tx *gorm.DB) {
		if strings.HasPrefix(tx.Statement.SQL.String(), "DELETE FROM login_throttles") {
			fired.Add(1)
			tx.AddError(errors.New("injected throttle failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Raw().Remove(name) })
	return &fired
}

// agesNoteState summarizes contact_requests for the erased person U with
// aggregates only, so it stays cheap at 70,001 rows: how many of U's subject
// rows still hold a note, and digests of U's subject rows minus the note, of
// every id and reason, of the rows that name U in neither role, of the notes of
// the rows where U only asked, and of the ones of those rows that were already
// terminal when the scene was seeded (pinned by id: the existing lifecycle ends
// U's pending rows during the erasure, and those rows become terminal then).
type agesNoteState struct {
	Total             int64
	SubjectNotes      int64
	SubjectRest       string
	Reasons           string
	Unrelated         string
	RequesterNotes    string
	RequesterTerminal string
}

func agesNoteStateOf(t testing.TB, db *gorm.DB, u uint, terminal []uint) agesNoteState {
	t.Helper()
	var st agesNoteState
	if err := db.Raw(`SELECT
		(SELECT count(*) FROM contact_requests) AS total,
		(SELECT count(*) FROM contact_requests WHERE purpose = 'occurrence_add' AND subject_user_id = ? AND add_childcare_ages_note <> '') AS subject_notes,
		coalesce((SELECT md5(string_agg((to_jsonb(r) - 'add_childcare_ages_note')::text, '|' ORDER BY id)) FROM contact_requests r
			WHERE purpose = 'occurrence_add' AND subject_user_id = ?), '') AS subject_rest,
		coalesce((SELECT md5(string_agg(id || ':' || reason, '|' ORDER BY id)) FROM contact_requests), '') AS reasons,
		coalesce((SELECT md5(string_agg(to_jsonb(r)::text, '|' ORDER BY id)) FROM contact_requests r
			WHERE subject_user_id <> ? AND requester_id <> ?), '') AS unrelated,
		coalesce((SELECT md5(string_agg(id || ':' || add_childcare_ages_note, '|' ORDER BY id)) FROM contact_requests
			WHERE requester_id = ?), '') AS requester_notes,
		coalesce((SELECT md5(string_agg(to_jsonb(r)::text, '|' ORDER BY id)) FROM contact_requests r
			WHERE requester_id = ? AND id IN ?), '') AS requester_terminal`,
		u, u, u, u, u, u, terminal).Scan(&st).Error; err != nil {
		t.Fatal(err)
	}
	return st
}

// agesNoteScene is an acting admin, an erased person U, the person U is only
// the requester for, a leader, and three dates of one group event.
type agesNoteScene struct {
	app    *App
	ac     *apiClient
	erased *models.User
	other  *models.User
	leader *models.User
	group  *models.Group
	events [3]models.Event
	occs   [3]models.EventOccurrence
	// terminal holds the ids of the rows where U only asked that are terminal
	// before the erasure.
	terminal []uint
}

// state is agesNoteStateOf for the scene's erased person.
func (sc *agesNoteScene) state(t testing.TB) agesNoteState {
	t.Helper()
	return agesNoteStateOf(t, sc.app.DB, sc.erased.ID, sc.terminal)
}

// newAgesNoteApp is testApp, or, when fresh, an App on its own migrated
// database (the 70,001-row case starts from nothing, as the scale test does).
func newAgesNoteApp(t *testing.T, fresh bool) *App {
	t.Helper()
	base := testApp(t)
	if !fresh {
		return base
	}
	db, err := testdb.Open(testdb.Fresh(t, true))
	if err != nil {
		t.Fatalf("fresh database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return &App{DB: db, Store: base.Store, Config: base.Config, Uploads: base.Uploads,
		Mailer: mail.WithSuppressions(db, mail.NewDevOutbox(db, "Fold <noreply@example.com>"))}
}

// agesNoteBulkSQL inserts $n terminal occurrence_add rows (denied, withdrawn,
// expired and ended in turn, each honoring every CHECK, each with a nonempty
// note) from requester to subject, spread over three dates, in one statement.
const agesNoteBulkSQL = `WITH p AS (SELECT ?::bigint AS req, ?::bigint AS subj, ?::timestamptz AS created,
		?::bigint AS e1, ?::bigint AS e2, ?::bigint AS e3, ?::bigint AS o1, ?::bigint AS o2, ?::bigint AS o3),
	s AS (SELECT g, g % 4 AS k FROM generate_series(1, ?::int) g)
INSERT INTO contact_requests (requester_id, subject_user_id, context_type, context_id, fields, reason, needs_admin,
	subject_decision, subject_decided_by, subject_decided_at, admin_decision, status, created_at, expires_at, completed_at,
	withdrawn_by_id, end_reason, purpose, occurrence_id, add_attendance_mode, add_childcare_ages_note)
SELECT p.req, p.subj, 'event', (ARRAY[p.e1, p.e2, p.e3])[1 + s.g % 3], '', 'identifying reason ' || s.g, false,
	CASE WHEN s.k = 0 THEN 'denied' ELSE 'pending' END,
	CASE WHEN s.k = 0 THEN p.subj END,
	CASE WHEN s.k = 0 THEN p.created + interval '5 minutes' END,
	'not_needed',
	CASE s.k WHEN 0 THEN 'denied' WHEN 1 THEN 'withdrawn' WHEN 2 THEN 'expired' ELSE 'ended' END,
	p.created, p.created + interval '14 days',
	CASE s.k WHEN 2 THEN p.created + interval '14 days 1 minute' ELSE p.created + interval '10 minutes' END,
	CASE WHEN s.k = 1 THEN p.req END,
	CASE WHEN s.k = 3 THEN 'event_ended' ELSE '' END,
	'occurrence_add', (ARRAY[p.o1, p.o2, p.o3])[1 + s.g % 3], 'in_person', 'ages note ' || s.g
FROM p CROSS JOIN s`

func (sc *agesNoteScene) bulk(t *testing.T, requester, subject uint, n int) {
	t.Helper()
	created := time.Now().UTC().AddDate(0, 0, -30).Truncate(time.Microsecond)
	if err := sc.app.DB.Exec(agesNoteBulkSQL, requester, subject, created,
		sc.events[0].ID, sc.events[1].ID, sc.events[2].ID, sc.occs[0].ID, sc.occs[1].ID, sc.occs[2].ID, n).Error; err != nil {
		t.Fatalf("seed %d rows: %v", n, err)
	}
}

// newAgesNoteScene seeds n terminal occurrence_add rows of U as the subject,
// and the controls: two rows where U only asked (one terminal, one pending),
// and three rows naming U in neither role (two terminal, one pending).
func newAgesNoteScene(t *testing.T, fresh bool, n int) *agesNoteScene {
	t.Helper()
	app := newAgesNoteApp(t, fresh)
	newTestUser(t, app, "erase-admin", erasurePW, models.RoleAdmin)
	newTestUser(t, app, "erase-second", erasurePW, models.RoleAdmin)
	sc := &agesNoteScene{app: app}
	sc.leader = newTestUser(t, app, "ages-leader", erasurePW, models.RoleMember)
	sc.erased = newTestUser(t, app, "ages-erased", erasurePW, models.RoleMember)
	sc.other = newTestUser(t, app, "ages-other", erasurePW, models.RoleMember)
	sc.ac = loginAPI(t, app, "erase-admin", erasurePW)
	sc.ac.holdNotices = true
	sc.group = newTestGroup(t, app, sc.leader, "Ages note group", models.GroupActive)
	for i := range sc.events {
		sc.events[i], sc.occs[i] = seedEvent(t, app, sc.group, sc.leader.ID, time.Now().UTC().Add(time.Duration(48+i)*time.Hour))
	}
	sc.bulk(t, sc.leader.ID, sc.erased.ID, n)
	sc.bulk(t, sc.erased.ID, sc.other.ID, 1)
	sc.bulk(t, sc.leader.ID, sc.other.ID, 2)
	db := app.DB
	erasureMakeRequest(t, db, policy.RequestOccurrenceAdd, policy.ContactPending, sc.erased.ID, sc.other.ID, sc.group.ID, sc.events[0].ID, sc.occs[0].ID, 1, "requester-only pending note")
	erasureMakeRequest(t, db, policy.RequestOccurrenceAdd, policy.ContactPending, sc.leader.ID, sc.other.ID, sc.group.ID, sc.events[0].ID, sc.occs[0].ID, 2, "unrelated pending note")
	if err := db.Raw(`SELECT id FROM contact_requests WHERE requester_id = ? AND status <> 'pending' ORDER BY id`, sc.erased.ID).Scan(&sc.terminal).Error; err != nil {
		t.Fatal(err)
	}
	if len(sc.terminal) == 0 {
		t.Fatal("the scene seeded no terminal row where the erased person only asked")
	}
	return sc
}

// checkAgesNoteErased asserts the erased state against the state before:
// U's subject rows lost the note and nothing else, no reason anywhere moved, and
// the controls are as the pre-T07 erasure leaves them.
func checkAgesNoteErased(t *testing.T, before, after agesNoteState, wantNotes int64) {
	t.Helper()
	if before.SubjectNotes != wantNotes {
		t.Fatalf("seeded %d subject rows with a note, want %d", before.SubjectNotes, wantNotes)
	}
	if after.SubjectNotes != 0 {
		t.Errorf("%d of the erased person's occurrence_add rows still hold a note", after.SubjectNotes)
	}
	if after.Total != before.Total {
		t.Errorf("contact_requests has %d rows, was %d", after.Total, before.Total)
	}
	if after.SubjectRest != before.SubjectRest {
		t.Errorf("the scrub changed more than the note of the subject rows: md5 %s, was %s", after.SubjectRest, before.SubjectRest)
	}
	if after.Reasons != before.Reasons {
		t.Errorf("a reason (or an id) changed: md5 %s, was %s", after.Reasons, before.Reasons)
	}
	if after.Unrelated != before.Unrelated {
		t.Errorf("a row naming the erased person in neither role changed: md5 %s, was %s", after.Unrelated, before.Unrelated)
	}
	if after.RequesterNotes != before.RequesterNotes {
		t.Errorf("a note on a row where the erased person only asked changed: md5 %s, was %s", after.RequesterNotes, before.RequesterNotes)
	}
	if after.RequesterTerminal != before.RequesterTerminal {
		t.Errorf("a terminal row where the erased person only asked changed: md5 %s, was %s", after.RequesterTerminal, before.RequesterTerminal)
	}
}

// TestAccountErasureScrubsSubjectAgesNote (R4-T07A) runs the real DELETE over
// every class of contact_requests row. The erased person's occurrence_add rows
// as the subject lose the childcare ages note in every status, including the
// historical granted ones the baseline completion check would refuse to touch:
// (a) request A replaced by B on the same registration, (b) a retired and
// deleted registration, (c) access ended but the registration kept. Nothing
// else changes: reason stays on every row of every purpose and status, the
// erased person's contact and group_add rows (a granted group_add among them,
// whose reason the database still protects) are not scrubbed, rows where they
// only asked keep their note, and rows naming them in neither role are
// byte-identical.
//
// The existing lifecycle ends the erased person's pending rows during the same
// DELETE. A twin account holds the same rows with empty notes, which the scrub
// does not select, and its erasure stands for the erasure before T07. The twin
// is a cross-check only: what the lifecycle must leave behind is also stated
// outright, per class of row, so a regression that hit both runs alike still
// fails. Each run is judged by its own before and after (checkErasureRequests,
// checkErasureLedger):
//   - every initially pending row naming the person ends as "ended" with
//     "account_disabled" and a completed_at inside the DELETE, and every other
//     column of it is exactly as it was (the note aside, on the subject's
//     occurrence_add rows);
//   - the audit rows, notices, deliveries and jobs the DELETE adds are exactly
//     the expected ones, by id mapping the fixture rows, with waiting tasks
//     seeded for the living reviewers and for the person, and everything else
//     (the other person's rows and tasks, unrelated tasks) untouched;
//   - the replacement registration's live access ends once, with one new
//     occurrence_access.ended snapshot, and no notice goes to the erased holder.
//
// Rows are paired erased-to-twin by the ids the fixture recorded, never by
// reason text.
func TestAccountErasureScrubsSubjectAgesNote(t *testing.T) {
	f := newContactAPIFixture(t, false)
	f.ac.holdNotices = true
	db := f.app.DB
	erased, other, leader := f.outsider, f.subject, f.leader
	// twin is built exactly like erased and gets the same rows with empty
	// notes. The helper selects only a nonempty note, so erasing twin is the
	// erasure before T07 (T07A "lifecycle columns equal to what the pre-T07
	// erasure produces").
	twin := newTestUser(t, f.app, "contact-twin", "password1234", models.RoleMember)
	ev, occ := seedEvent(t, f.app, f.group, leader.ID, time.Now().UTC().Add(48*time.Hour))

	// fixtureRow is one seeded request: the erased side's row and, when the
	// request names the erased person, the twin's row beside it.
	type fixtureRow struct {
		Class   string
		Req     models.ContactRequest
		TwinReq models.ContactRequest // zero for a row naming neither person
		Pending bool
	}
	var fixture []fixtureRow
	// twinOf maps an erased-side request id to its twin's request id.
	twinOf := map[uint]uint{}
	seed := func(purpose, status string, requester, subject uint, n int, note string) {
		row := fixtureRow{Class: erasureNeither}
		switch {
		case subject == erased.ID:
			row.Class = erasureAsSubject
		case requester == erased.ID:
			row.Class = erasureAsRequester
		}
		row.Req = erasureMakeRequest(t, db, purpose, status, requester, subject, f.group.ID, ev.ID, occ.ID, n, note)
		row.Pending = status == policy.ContactPending
		if row.Class != erasureNeither {
			twinRequester, twinSubject := requester, subject
			if row.Class == erasureAsRequester {
				twinRequester = twin.ID
			} else {
				twinSubject = twin.ID
			}
			row.TwinReq = erasureMakeRequest(t, db, purpose, status, twinRequester, twinSubject, f.group.ID, ev.ID, occ.ID, n, "") // same reason, no note
			twinOf[row.Req.ID] = row.TwinReq.ID
		}
		fixture = append(fixture, row)
	}
	n := 0
	mk := func(purpose, status string, requester, subject uint) {
		n++
		seed(purpose, status, requester, subject, n, fmt.Sprintf("ages note %d", n))
	}
	purposes := []string{policy.RequestContact, policy.RequestGroupAdd, policy.RequestOccurrenceAdd}
	for _, purpose := range purposes {
		for _, status := range []string{policy.ContactPending, policy.ContactWithdrawn, policy.ContactDenied, policy.ContactExpired, policy.ContactEnded} {
			mk(purpose, status, leader.ID, erased.ID) // the erased person is asked about
			mk(purpose, status, erased.ID, other.ID)  // the erased person asked
			mk(purpose, status, leader.ID, other.ID)  // neither
		}
	}
	// The same class with an empty note and a long note, so an unselected
	// subject row is part of the fixture.
	for i, c := range []struct{ status, note string }{{policy.ContactPending, ""}, {policy.ContactDenied, strings.Repeat("x", 200)}} {
		seed(policy.RequestOccurrenceAdd, c.status, leader.ID, erased.ID, 100+i, c.note)
	}

	// Granted rows of the erased person, each on its own date, and the twin's
	// without notes on dates of their own.
	date := func(i int) (uint, models.EventOccurrence) {
		e, o := seedEvent(t, f.app, f.group, leader.ID, time.Now().UTC().Add(time.Duration(72+i)*time.Hour))
		return e.ID, o
	}
	eA, oA := date(0)
	reqA, reqB, regB := erasureReplacedAdd(t, db, leader, erased, eA, oA, true)
	eB, oB := date(1)
	reqRetired := erasureRetiredAdd(t, db, leader, erased, eB, oB, true)
	eC, oC := date(2)
	reqEnded := erasureEndedAccessAdd(t, db, leader, erased, eC, oC, true)
	eT, oT := date(3)
	_, twinReqB, regTwinB := erasureReplacedAdd(t, db, leader, twin, eT, oT, false)
	// A baseline recheck of the control passes today, in its own committed
	// transaction, so the exception is not what makes (c) work.
	if err := db.Exec(`UPDATE contact_requests SET reason = reason WHERE id = ?`, reqEnded.ID).Error; err != nil {
		t.Fatalf("control (c): the unchanged completion lookup failed: %v", err)
	}
	// A granted group_add of the erased person: the scrub does not select it,
	// and the baseline rule on its reason stays (checked after the erasure).
	addGroup := newTestGroup(t, f.app, leader, "Granted group add", models.GroupActive)
	reqGroupAdd := erasureGrantedGroupAdd(t, db, leader, erased, addGroup)
	granted := []models.ContactRequest{reqA, reqB, reqRetired, reqEnded}

	// Waiting tasks, written the way a request writes them: the subject's
	// review task, and the admin's when the request needs one. Every pending
	// request gets them, the erased person's, the twin's and the rows naming
	// neither, so the erasure has living reviewers to close tasks for and
	// unrelated tasks to leave alone.
	var erasedPending, twinPending []models.ContactRequest
	neitherPending := 0
	for _, r := range fixture {
		if !r.Pending {
			continue
		}
		erasureSeedRequestTasks(t, db, r.Req, f.admin.ID)
		if r.Class == erasureNeither {
			neitherPending++
			continue
		}
		erasureSeedRequestTasks(t, db, r.TwinReq, f.admin.ID)
		erasedPending = append(erasedPending, r.Req)
		twinPending = append(twinPending, r.TwinReq)
	}
	// Per purpose: the person is asked about, the person asks, and neither;
	// plus the extra pending occurrence_add row of the person as the subject.
	if len(erasedPending) != 3*2+1 || len(twinPending) != 3*2+1 || neitherPending != 3 {
		t.Fatalf("seeded %d erased, %d twin and %d unrelated pending rows, want 7, 7 and 3", len(erasedPending), len(twinPending), neitherPending)
	}
	if len(twinOf) != 3*5*2+2 {
		t.Fatalf("%d erased rows have a twin, want %d", len(twinOf), 3*5*2+2)
	}

	before := erasureRequestRows(t, db)
	snapshotActions := []string{audit.OccurrenceAccessAdded, audit.OccurrenceAccessEnded}
	var snapshotsBefore []models.AuditLog
	if err := db.Where("action IN ?", snapshotActions).Order("id").Find(&snapshotsBefore).Error; err != nil {
		t.Fatal(err)
	}
	state := agesNoteStateOf(t, db, erased.ID, nil)
	if state.SubjectNotes < int64(len(granted)) {
		t.Fatalf("only %d subject rows with a note were seeded", state.SubjectNotes)
	}

	// erase deletes person through the real DELETE and judges the run: the
	// request rows, then the audit, notice, delivery and job rows, each against
	// the state taken immediately before this one DELETE.
	erase := func(name string, person *models.User, pending []models.ContactRequest, reg models.Registration, eventID, requestID uint) (erasureLedgerFacts, models.Registration) {
		t.Helper()
		var regBefore, regAfter models.Registration
		if err := db.First(&regBefore, reg.ID).Error; err != nil {
			t.Fatal(err)
		}
		rowsBefore, ledgerBefore := erasureRequestRows(t, db), erasureLedgerOf(t, db)
		start := time.Now().UTC()
		r := deleteAccount(f.ac, person.ID, nil)
		end := time.Now().UTC()
		if r.code != http.StatusOK {
			t.Fatalf("delete %s: %d %s", name, r.code, r.body)
		}
		if err := db.First(&regAfter, reg.ID).Error; err != nil {
			t.Fatal(err)
		}
		run := erasureRun{Name: name, Subject: person.ID, Admin: f.admin.ID, Pending: pending, Span: erasureSpan{Start: start, End: end},
			Reg: regBefore, RegAfter: regAfter, GroupID: f.group.ID, EventID: eventID, IssuerID: leader.ID, RequestID: requestID}
		checkErasureRequests(t, run, rowsBefore, erasureRequestRows(t, db))
		return checkErasureLedger(t, run, ledgerBefore, erasureLedgerOf(t, db)), regAfter
	}
	erasedFacts, regAfter := erase("erased", erased, erasedPending, regB, eA, reqB.ID)
	twinFacts, twinRegAfter := erase("twin", twin, twinPending, regTwinB, eT, twinReqB.ID)
	// What each run was expected to do, stated once for both: the living
	// reviewers' tasks it closes (the other party's review task on each pending
	// contact, group_add and occurrence_add the person asked, and the admin's
	// on the contact rows) and the person's own tasks it removes (the
	// person's review task on each of the four pending rows asking about
	// them). Neither run adds an access-ended notice for the erased holder.
	for _, facts := range []erasureLedgerFacts{erasedFacts, twinFacts} {
		if facts.Living != 5 || facts.Deleted != 4 || facts.NewNotices != 7 {
			t.Errorf("%s: closed %d living tasks, removed %d of the person's, added %d notices; want 5, 4 and 7", facts.Name, facts.Living, facts.Deleted, facts.NewNotices)
		}
	}

	after := erasureRequestRows(t, db)
	if len(after) != len(before) {
		t.Fatalf("%d rows after the erasure, %d before", len(after), len(before))
	}
	checkErasurePairs(t, erased.ID, twin.ID, twinOf, before, after)
	for _, g := range granted {
		if a := after[g.ID]; a.Status != policy.ContactGranted || a.Note != "" {
			t.Errorf("granted request %d: status %q note %q, want granted with an empty note", g.ID, a.Status, a.Note)
		}
	}
	// group_add: granted and unchanged, and the database still refuses a change
	// of its reason (the scrub is no licence for anything but the note).
	if a, b := after[reqGroupAdd.ID], before[reqGroupAdd.ID]; a.Whole != b.Whole || a.Status != policy.ContactGranted {
		t.Errorf("granted group_add changed:\n%s\n%s", b.Whole, a.Whole)
	}
	if err := db.Exec(`UPDATE contact_requests SET reason = 'changed' WHERE id = ?`, reqGroupAdd.ID).Error; err == nil || !strings.Contains(err.Error(), "23514") {
		t.Errorf("a direct change of the granted group_add reason: %v, want SQLSTATE 23514", err)
	}
	// (a) the registration keeps B, and B's live access ended in the usual way,
	// the way the twin's does (the erasure before T07). The ending itself, its
	// snapshot and the absence of a notice were judged per run above; here the
	// two runs are set side by side with the ids mapped by the fixture.
	if regAfter.AccessRequestID == nil || *regAfter.AccessRequestID != reqB.ID || regAfter.AccessEndedAt == nil {
		t.Errorf("case (a): registration %d names request %v with access ended %v, want B (%d), ended", regAfter.ID, regAfter.AccessRequestID, regAfter.AccessEndedAt, reqB.ID)
	}
	if twinRegAfter.AccessRequestID == nil || *twinRegAfter.AccessRequestID != twinReqB.ID || twinRegAfter.AccessEndedAt == nil {
		t.Errorf("case (a) twin: registration %d names request %v with access ended %v, want B (%d), ended", twinRegAfter.ID, twinRegAfter.AccessRequestID, twinRegAfter.AccessEndedAt, twinReqB.ID)
	}
	snapshotShape := func(label string, meta map[string]any, subject, occurrence, event, registration, request uint) map[string]any {
		out := map[string]any{}
		for k, v := range meta {
			out[k] = v
		}
		for k, id := range map[string]uint{"subjectUserId": subject, "occurrenceId": occurrence, "eventId": event, "registrationId": registration, "requestId": request} {
			if got, ok := out[k].(float64); !ok || uint(got) != id {
				t.Errorf("case (a) %s: snapshot %s is %v, want %d", label, k, out[k], id)
			}
			out[k] = "mapped"
		}
		delete(out, "grantedAtMicro") // bounded against its registration in checkErasureLedger
		delete(out, "endedAtMicro")
		return out
	}
	gotShape := snapshotShape("erased", erasedFacts.EndedMeta, erased.ID, oA.ID, eA, regB.ID, reqB.ID)
	wantShape := snapshotShape("twin", twinFacts.EndedMeta, twin.ID, oT.ID, eT, regTwinB.ID, twinReqB.ID)
	if len(gotShape) == 0 || !reflect.DeepEqual(gotShape, wantShape) {
		t.Errorf("case (a): the ended snapshot of B is %v, the erasure before T07 writes %v for the twin", gotShape, wantShape)
	}
	blankedSnapshots := map[uint]bool{}
	for _, row := range snapshotsBefore {
		blankedSnapshots[row.ID] = row.ActorID != nil && (*row.ActorID == erased.ID || *row.ActorID == twin.ID)
	}
	assertAuditErasureRows(t, db, snapshotsBefore, blankedSnapshots, nil)
	if st := agesNoteStateOf(t, db, erased.ID, nil); st.SubjectNotes != 0 {
		t.Errorf("%d subject notes left", st.SubjectNotes)
	}
}

// TestAccountErasureScrubsSubjectAgesNoteStatement calls the helper alone,
// inside a test transaction after the tombstone and the soft delete, and
// checks statement-level preservation (helper-only scope): every selected row
// changes by its note alone, every other contact_requests row is byte
// identical, and the grants, registrations, audit, notifications, deliveries
// and jobs are untouched. It also pins the shape of the statements: the
// selection takes its row locks in ascending id order, and when no row
// qualifies no UPDATE is sent.
func TestAccountErasureScrubsSubjectAgesNoteStatement(t *testing.T) {
	sc := newAgesNoteScene(t, false, 6)
	db := sc.app.DB
	g, ev, occ := sc.group.ID, sc.events[0].ID, sc.occs[0].ID
	n := 10
	for _, purpose := range []string{policy.RequestContact, policy.RequestGroupAdd, policy.RequestOccurrenceAdd} {
		n++
		erasureMakeRequest(t, db, purpose, policy.ContactPending, sc.leader.ID, sc.erased.ID, g, ev, occ, n, "subject pending note")
		n++
		erasureMakeRequest(t, db, purpose, policy.ContactWithdrawn, sc.leader.ID, sc.erased.ID, g, ev, occ, n, "subject withdrawn note")
	}
	erasureMakeRequest(t, db, policy.RequestOccurrenceAdd, policy.ContactDenied, sc.leader.ID, sc.erased.ID, g, ev, occ, 50, "")
	rec := recordAgesNoteStatements(t, db)
	errRollBack := errors.New("roll the statement test back")
	snap := func(tx *gorm.DB) string {
		return dumpRows(t, tx, "contact_grants", "id", "true") + dumpRows(t, tx, "registrations", "id", "true") +
			dumpRows(t, tx, "audit_logs", "id", "true") + dumpRows(t, tx, "notifications", "id", "true") +
			dumpRows(t, tx, "notification_deliveries", "id", "true") + dumpRows(t, tx, "jobs", "id", "true")
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		var u models.User
		if err := tx.First(&u, sc.erased.ID).Error; err != nil {
			return err
		}
		if err := tombstoneUserIn(tx, &u); err != nil {
			return err
		}
		if err := tx.Delete(&u).Error; err != nil {
			return err
		}
		s := captureErasureSubject(&u)
		rowsBefore, othersBefore := erasureRequestRows(t, tx), snap(tx)
		selected := 0
		for _, r := range rowsBefore {
			if r.Purpose == policy.RequestOccurrenceAdd && r.SubjectUserID == sc.erased.ID && r.Note != "" {
				selected++
			}
		}
		if selected != 8 {
			t.Errorf("fixture selects %d rows, want 8 (6 bulk rows plus the pending and withdrawn occurrence_add rows)", selected)
		}

		if err := eraseContactRequestTextIn(tx, s); err != nil {
			return err
		}
		rowsAfter := erasureRequestRows(t, tx)
		for id, b := range rowsBefore {
			a := rowsAfter[id]
			if b.Purpose == policy.RequestOccurrenceAdd && b.SubjectUserID == sc.erased.ID && b.Note != "" {
				if a.Note != "" || a.Rest != b.Rest {
					t.Errorf("selected request %d: note %q, rest changed %t", id, a.Note, a.Rest != b.Rest)
				}
			} else if a.Whole != b.Whole {
				t.Errorf("request %d was not selected and changed:\n%s\n%s", id, b.Whole, a.Whole)
			}
			if a.Reason != b.Reason {
				t.Errorf("request %d: reason changed", id)
			}
		}
		if got := snap(tx); got != othersBefore {
			t.Errorf("the helper changed a table other than contact_requests")
		}
		selects, updates := rec.seen()
		if len(selects) != 1 || !strings.Contains(selects[0], "ORDER BY id FOR UPDATE") {
			t.Errorf("lock selection statements %q, want one that orders by id and takes FOR UPDATE", selects)
		}
		var changed int64
		for _, u := range updates {
			changed += u.Rows
		}
		if len(updates) != 1 || changed != int64(selected) {
			t.Errorf("%d UPDATEs changed %d rows, want one that changed %d", len(updates), changed, selected)
		}

		// Nothing qualifies now: the selection runs and no UPDATE follows.
		if err := eraseContactRequestTextIn(tx, s); err != nil {
			return err
		}
		selects2, updates2 := rec.seen()
		if len(selects2) != 2 || len(updates2) != len(updates) {
			t.Errorf("second run sent %d selections and %d UPDATEs in all, want 2 and %d", len(selects2), len(updates2), len(updates))
		}
		return errRollBack
	})
	if !errors.Is(err, errRollBack) {
		t.Fatalf("statement transaction: %v", err)
	}
}

// TestAccountErasureScrubsSubjectAgesNoteInBatches seeds lookupChunk+1 rows of
// the erased subject, one more than one batch, and erases through the real
// DELETE: the helper sends exactly two UPDATEs, a full batch and a batch of
// one, each carrying its ids and the subject as bind parameters. A late
// failure after both batches rolls them back together.
func TestAccountErasureScrubsSubjectAgesNoteInBatches(t *testing.T) {
	const rows = lookupChunk + 1
	want := []agesNoteStatement{{Vars: lookupChunk + 1, Rows: lookupChunk}, {Vars: 2, Rows: 1}}
	t.Run("commit", func(t *testing.T) {
		sc := newAgesNoteScene(t, false, rows)
		rec := recordAgesNoteStatements(t, sc.app.DB)
		before := sc.state(t)
		if r := deleteAccount(sc.ac, sc.erased.ID, nil); r.code != http.StatusOK {
			t.Fatalf("delete: %d %s", r.code, r.body)
		}
		checkAgesNoteErased(t, before, sc.state(t), rows)
		selects, updates := rec.seen()
		if len(selects) != 1 {
			t.Errorf("%d lock selections, want 1", len(selects))
		}
		if !reflect.DeepEqual(updates, want) {
			t.Errorf("UPDATEs (bind parameters, rows changed) = %+v, want %+v", updates, want)
		}
	})
	t.Run("late failure", func(t *testing.T) {
		sc := newAgesNoteScene(t, false, rows)
		rec := recordAgesNoteStatements(t, sc.app.DB)
		failed := erasureTestCountThrottleFailures(t, sc.app.DB)
		before := sc.state(t)
		if r := deleteAccount(sc.ac, sc.erased.ID, nil); r.code != http.StatusInternalServerError {
			t.Fatalf("delete answered %d %s, want 500", r.code, r.body)
		}
		// Both batches ran and committed nothing: the failure came after them.
		if _, updates := rec.seen(); !reflect.DeepEqual(updates, want) {
			t.Errorf("UPDATEs before the failure = %+v, want %+v", updates, want)
		}
		if got := failed.Load(); got != 1 {
			t.Errorf("the injected last-statement failure fired %d times, want 1", got)
		}
		if after := sc.state(t); after != before {
			t.Errorf("a failed erasure left a change in contact_requests:\n%+v\n%+v", before, after)
		}
		if n := countRows(t, sc.app.DB, "users", "id = ? AND deleted_at IS NOT NULL", sc.erased.ID); n != 0 {
			t.Errorf("the failed erasure left the user soft-deleted")
		}
	})
}

// TestAccountErasureAgesNoteSavepointRollback rolls the scrub back to a
// savepoint after the soft delete (inventory H7). The rows have their notes
// again, and the outer transaction still commits with a historical granted
// row in the scrub.
//
// The scrub's own queued completion events cannot show whether they survived:
// the licensed blank's event is skipped by design, so a survivor would pass
// too. So the savepoint also queues an event that must fail, a forged granted
// request with no registration behind it, and the proof is that it is gone:
// SET CONSTRAINTS add_request_completed IMMEDIATE, which checks every event
// still queued, passes after the rollback, and so does the COMMIT. The control
// subtest does the same without the rollback and gets 23514 from the forged
// event, so the passing case is not a no-op. No trigger is disabled.
func TestAccountErasureAgesNoteSavepointRollback(t *testing.T) {
	setup := func(t *testing.T) (*contactAPIFixture, map[uint]erasureAgesNoteRow, models.Event, models.EventOccurrence) {
		f := newContactAPIFixture(t, false)
		db := f.app.DB
		erased, leader := f.outsider, f.leader
		ev, occ := seedEvent(t, f.app, f.group, leader.ID, time.Now().UTC().Add(48*time.Hour))
		erasureMakeRequest(t, db, policy.RequestOccurrenceAdd, policy.ContactPending, leader.ID, erased.ID, f.group.ID, ev.ID, occ.ID, 1, "pending note")
		erasureMakeRequest(t, db, policy.RequestOccurrenceAdd, policy.ContactDenied, leader.ID, erased.ID, f.group.ID, ev.ID, occ.ID, 2, "denied note")
		eA, oA := seedEvent(t, f.app, f.group, leader.ID, time.Now().UTC().Add(72*time.Hour))
		erasureReplacedAdd(t, db, leader, erased, eA.ID, oA, true)
		return f, erasureRequestRows(t, db), ev, occ
	}
	// softDeleted tombstones and soft-deletes the erased person in tx, the two
	// statements the scrub's licence needs before it.
	softDeleted := func(tx *gorm.DB, id uint) (erasureSubject, error) {
		var u models.User
		if err := tx.First(&u, id).Error; err != nil {
			return erasureSubject{}, err
		}
		if err := tombstoneUserIn(tx, &u); err != nil {
			return erasureSubject{}, err
		}
		if err := tx.Delete(&u).Error; err != nil {
			return erasureSubject{}, err
		}
		return captureErasureSubject(&u), nil
	}

	t.Run("rollback to savepoint", func(t *testing.T) {
		f, before, ev, occ := setup(t)
		db := f.app.DB
		errSavepoint := errors.New("roll back to the savepoint")
		err := db.Transaction(func(tx *gorm.DB) error {
			s, err := softDeleted(tx, f.outsider.ID)
			if err != nil {
				return err
			}
			inner := tx.Transaction(func(sp *gorm.DB) error {
				if err := eraseContactRequestTextIn(sp, s); err != nil {
					return err
				}
				if st := agesNoteStateOf(t, sp, f.outsider.ID, nil); st.SubjectNotes != 0 {
					t.Errorf("%d notes left inside the savepoint after the scrub", st.SubjectNotes)
				}
				if err := erasureForgedGrantedAdd(sp, f.leader.ID, f.subject.ID, ev.ID, occ.ID); err != nil {
					return fmt.Errorf("forged insert: %w", err)
				}
				return errSavepoint
			})
			if !errors.Is(inner, errSavepoint) {
				return fmt.Errorf("savepoint: %v", inner)
			}
			for id, r := range erasureRequestRows(t, tx) {
				if r.Whole != before[id].Whole {
					t.Errorf("request %d after the rollback to the savepoint:\n%s\n%s", id, before[id].Whole, r.Whole)
				}
			}
			// Every event still queued is checked now. The forged one was
			// queued inside the savepoint, so this passes only if the rollback
			// discarded it with the scrub.
			if err := tx.Exec(`SET CONSTRAINTS add_request_completed IMMEDIATE`).Error; err != nil {
				return fmt.Errorf("a completion event queued inside the rolled-back savepoint survived: %w", err)
			}
			return tx.Exec(`SET CONSTRAINTS add_request_completed DEFERRED`).Error
		})
		if err != nil {
			t.Fatalf("the outer transaction did not commit after the savepoint rollback: %v", err)
		}
		for id, r := range erasureRequestRows(t, db) {
			if r.Whole != before[id].Whole {
				t.Errorf("request %d after the commit:\n%s\n%s", id, before[id].Whole, r.Whole)
			}
		}
	})

	t.Run("control without the rollback", func(t *testing.T) {
		f, before, ev, occ := setup(t)
		db := f.app.DB
		errDone := errors.New("control finished")
		err := db.Transaction(func(tx *gorm.DB) error {
			s, err := softDeleted(tx, f.outsider.ID)
			if err != nil {
				return err
			}
			if err := eraseContactRequestTextIn(tx, s); err != nil {
				return err
			}
			if err := erasureForgedGrantedAdd(tx, f.leader.ID, f.subject.ID, ev.ID, occ.ID); err != nil {
				return fmt.Errorf("forged insert: %w", err)
			}
			err = tx.Exec(`SET CONSTRAINTS add_request_completed IMMEDIATE`).Error
			if err == nil || !strings.Contains(err.Error(), "23514") || !strings.Contains(err.Error(), "date add requires completed occurrence access") {
				t.Errorf("SET CONSTRAINTS IMMEDIATE with the forged event queued = %v, want 23514 from the forged completion check", err)
			}
			return errDone
		})
		if !errors.Is(err, errDone) {
			t.Fatalf("control transaction: %v", err)
		}
		// Nothing of the control persisted.
		for id, r := range erasureRequestRows(t, db) {
			if b, ok := before[id]; !ok || r.Whole != b.Whole {
				t.Errorf("request %d after the control rolled back:\n%s", id, r.Whole)
			}
		}
		if len(erasureRequestRows(t, db)) != len(before) {
			t.Errorf("the control left the forged request behind")
		}
	})
}

// TestAccountErasureScrubsSubjectAgesNoteBeyondBindLimit (opt-in) erases a
// subject with 70,001 qualifying rows, more than PostgreSQL's 65,535 bind
// parameters in one statement, through the real DELETE on a fresh database.
// Skipped unless FOLD_ERASURE_SCALE=1. Run:
//
//	FOLD_ERASURE_SCALE=1 DATABASE_URL=<scratch> go test -run TestAccountErasureScrubsSubjectAgesNoteBeyondBindLimit -count=1 -v .
//
// Commit: HTTP 200, 15 UPDATEs (14 of 5,000 rows and one of 1), none with more
// than lookupChunk+1 parameters, 70,001 rows changed in all, every note blank,
// reason and the controls unchanged. Rollback, on a second fresh database: the
// same seed, the erasure's last statement is made to fail after the helper, the
// batches have all run, and every note and the user are as they were.
func TestAccountErasureScrubsSubjectAgesNoteBeyondBindLimit(t *testing.T) {
	if os.Getenv("FOLD_ERASURE_SCALE") != "1" {
		t.Skip("set FOLD_ERASURE_SCALE=1 to run the 70,001-row bind-limit erasure (opt-in, not run in CI)")
	}
	const rows = 70001
	if rows <= math.MaxUint16 {
		t.Fatalf("%d rows do not exceed the %d bind-parameter limit", rows, math.MaxUint16)
	}
	statements := (rows + lookupChunk - 1) / lookupChunk
	seed := func(t *testing.T) (*agesNoteScene, *agesNoteRecorder, time.Duration) {
		started := time.Now()
		sc := newAgesNoteScene(t, true, rows)
		return sc, recordAgesNoteStatements(t, sc.app.DB), time.Since(started)
	}
	check := func(t *testing.T, updates []agesNoteStatement) (largest int) {
		t.Helper()
		var changed int64
		for _, u := range updates {
			changed += u.Rows
			largest = max(largest, u.Vars)
			if u.Vars > lookupChunk+1 {
				t.Errorf("an UPDATE carried %d bind parameters, more than %d", u.Vars, lookupChunk+1)
			}
		}
		if len(updates) != statements || changed != rows {
			t.Errorf("%d UPDATEs changed %d rows, want %d UPDATEs changing %d", len(updates), changed, statements, rows)
		}
		if last := updates[len(updates)-1]; last.Rows != int64(rows-(statements-1)*lookupChunk) {
			t.Errorf("the last batch changed %d rows", last.Rows)
		}
		return largest
	}
	t.Run("commit", func(t *testing.T) {
		sc, rec, seeded := seed(t)
		before := sc.state(t)
		started := time.Now()
		r := deleteAccount(sc.ac, sc.erased.ID, nil)
		erased := time.Since(started)
		if r.code != http.StatusOK {
			t.Fatalf("delete: %d %s", r.code, r.body)
		}
		checkAgesNoteErased(t, before, sc.state(t), rows)
		_, updates := rec.seen()
		largest := check(t, updates)
		t.Logf("70,001-row commit: seeded in %s, erased in %s, %d UPDATEs, largest bind-parameter count %d", seeded.Round(time.Millisecond), erased.Round(time.Millisecond), len(updates), largest)
	})
	t.Run("rollback", func(t *testing.T) {
		sc, rec, seeded := seed(t)
		failed := erasureTestCountThrottleFailures(t, sc.app.DB)
		before := sc.state(t)
		started := time.Now()
		r := deleteAccount(sc.ac, sc.erased.ID, nil)
		erased := time.Since(started)
		if r.code != http.StatusInternalServerError {
			t.Fatalf("delete answered %d %s, want 500", r.code, r.body)
		}
		_, updates := rec.seen()
		largest := check(t, updates)
		if got := failed.Load(); got != 1 {
			t.Errorf("the injected last-statement failure fired %d times, want 1", got)
		}
		if after := sc.state(t); after != before {
			t.Errorf("a failed erasure left a change in contact_requests:\n%+v\n%+v", before, after)
		}
		if n := countRows(t, sc.app.DB, "users", "id = ? AND deleted_at IS NOT NULL", sc.erased.ID); n != 0 {
			t.Errorf("the failed erasure left the user soft-deleted")
		}
		t.Logf("70,001-row rollback: seeded in %s, failed erasure took %s, %d UPDATEs ran before the failure, largest bind-parameter count %d", seeded.Round(time.Millisecond), erased.Round(time.Millisecond), len(updates), largest)
	})
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
		var auditBefore []models.AuditLog
		if err := db.Where("action = ?", audit.RegistrationRemoved).Find(&auditBefore).Error; err != nil {
			t.Fatal(err)
		}
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
		removedNotes := map[uint][]string{}
		for _, row := range auditBefore {
			removedNotes[row.ID] = []string{"note"}
		}
		assertAuditErasureRows(t, db, auditBefore, nil, removedNotes)
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

// ---------------------------------------------------------------------------
// Representative-scale measurement (M7.4 R4-2a-7)
// ---------------------------------------------------------------------------

// The scale fixture is one church at the size the database review asked for:
// 5,000 people and, around the erased person U (user 3, address
// scaleAddress), the rows every R4-2a statement has to look through. Ids are
// fixed so the numbers in the PR can be reproduced:
//
//	users                5,000 (1 and 2 are the admins, 3 is U, 4 is a teen)
//	messages             300 (1-290 sent, 291-300 scheduled and written by U)
//	message_recipients   120,000; U on 300 rows: 280 direct, 20 as the copy a
//	                     guardian gets about teen 4 (every one with address A)
//	account_requests     10,000; 40 are U's: 20 REQ (10 by approved_user_id,
//	                     10 pending at A), 10 with guardian_email A and 10
//	                     member_invite rows U requested
//	invitations          5,000; 10 in INV (3 accepted by U, 2 for U's household
//	                     people, 5 unaccepted at A)
//	guest_signups        5,000; 10 at A (5 unconfirmed, 5 confirmed)
//	mail_outboxes        2,000; 5 to A in five spellings
//	registrations        50,000; 60 are U's
//	consents             20,000; 4 a person
//	notifications        24,140 including 4,044 admins' waiting tasks
//	notification_deliveries 20,106; waiting tasks have no delivery
//	contact_requests     10,000 across 3,005 subjects (R4-T07A-4), no granted row;
//	                     9,992 name neither U (1-9992, 1,998 pending), U is the
//	                     subject of four occurrence_add rows with notes (9993-9996:
//	                     denied, ended, pending, pending) and only the requester
//	                     of four (9997-10000: denied, ended, pending, pending)
//
// Nothing here asserts time. The test is opt-in (FOLD_ERASURE_SCALE=1), not
// run in CI, and its job is to print numbers for the reviewer.
const (
	scaleTargetName = "scale-target"
	scaleAddress    = "scale-target@example.com"
	scaleAdminName  = "scale-admin"
	scaleSecondName = "scale-second"
	scaleTargetID   = 3
)

// scaleSeed is one named statement of the fixture. They run on the plain
// database handle, not through GORM, because GORM reads "@" in a statement as
// a named parameter and the fixture is full of addresses.
type scaleSeed struct {
	name string
	sql  string
	args []any
}

var scaleSeeds = []scaleSeed{
	{name: "users", sql: `INSERT INTO users (id, email, name, role, status, account_type, session_version, timezone, created_at, updated_at)
		SELECT n, 'scale-user-' || n || '@example.com', 'Scale User ' || n, 'member', 'active', 'adult', 0, 'America/Denver', now(), now()
		FROM generate_series(4, 5000) n`},
	{name: "teen", sql: `UPDATE users SET account_type = 'teen', birth_date = current_date - interval '15 years' WHERE id = 4`},
	{name: "events", sql: `INSERT INTO events (id, group_id, title, starts_local, duration_minutes, tzid, rrule, location, created_by, created_at, updated_at, visibility, description)
		SELECT n, NULL, 'Scale event ' || n, '2027-01-01T18:00', 60, 'America/Denver', '', 'Fellowship hall', 1, now(), now(), 'public', 'Seeded'
		FROM generate_series(1, 5) n`},
	{name: "occurrences", sql: `INSERT INTO event_occurrences (id, event_id, original_date, starts_at, ends_at, status, created_at, updated_at)
		SELECT o, 1 + (o - 1) / 100, current_date + 1 + ((o - 1) % 100),
			now() + (1 + ((o - 1) % 100)) * interval '1 day', now() + (1 + ((o - 1) % 100)) * interval '1 day' + interval '1 hour',
			'scheduled', now(), now()
		FROM generate_series(1, 500) o`},
	{name: "households", sql: `INSERT INTO households (id, name, created_by) SELECT n, 'Household ' || n, 1 FROM generate_series(1, 1001) n`},
	{name: "household people of U", sql: `INSERT INTO household_people (id, household_id, first_name, last_name, kind, created_by, converted_user_id)
		VALUES (1, 1, 'Pat', 'Target', 'adult', 1, 3), (2, 1, 'Sam', 'Target', 'adult', 1, NULL)`},
	// U is one completed adult conversion: person 1 (converted_user_id = 3), the
	// approved conversion request and its accepted invitation name person 1.
	// Person 2 is a sibling who is not U (validatePersIn would abort an
	// erasure of a user with two different people, R4-3b-2).
	{name: "household people", sql: `INSERT INTO household_people (id, household_id, first_name, kind, created_by)
		SELECT 2 + n, 2 + (n - 1) / 2, 'Person ' || n, 'child', 1 FROM generate_series(1, 2000) n`},
	{name: "invitations", sql: `INSERT INTO invitations (id, email, name, invited_by, role, account_type, token_hash, expires_at, accepted_at, created_at)
		SELECT n, 'invitee-' || n || '@example.com', 'Invitee ' || n, 1 + n % 4000, 'member', 'adult', 'scale-inv-' || n,
			now() + interval '7 days', CASE WHEN n % 5 < 2 THEN now() - interval '1 day' END, now()
		FROM generate_series(1, 4990) n`},
	{name: "invitations U accepted", sql: `INSERT INTO invitations (id, email, name, invited_by, role, account_type, token_hash, expires_at, accepted_at, created_at)
		SELECT n, 'scale-target-prior-' || n || '@example.com', 'Scale Target', 1, 'member', 'adult', 'scale-inv-' || n,
			now() + interval '7 days', now() - interval '30 days', now() - interval '31 days'
		FROM generate_series(4991, 4993) n`},
	{name: "invitations U household", sql: `INSERT INTO invitations (id, email, name, invited_by, role, account_type, token_hash, expires_at, accepted_at, household_person_id, created_at)
		VALUES (4994, 'conversion@example.com', 'Pat Target', 1, 'member', 'adult', 'scale-inv-4994', now() + interval '7 days', now() - interval '2 days', 1, now()),
			(4995, 'conversion-open@example.com', 'Pat Target', 1, 'member', 'adult', 'scale-inv-4995', now() + interval '7 days', NULL, 1, now())`},
	{name: "invitations at A", sql: `INSERT INTO invitations (id, email, phone, name, invited_by, role, account_type, token_hash, expires_at, created_at)
		SELECT n, $1::text, '555-0111', 'Scale Target', 1 + n % 4000, 'member', 'adult', 'scale-inv-' || n, now() + interval '7 days', now()
		FROM generate_series(4996, 5000) n`, args: []any{scaleAddress}},
	{name: "account requests", sql: `INSERT INTO account_requests (id, name, email, phone, account_type, message, source, requested_by, status, decided_at, ip, expires_at, created_at, updated_at, guardian_email, approved_user_id)
		SELECT n, 'Requester ' || n, 'requester-' || n || '@example.com', '555-0100',
			CASE WHEN n % 50 = 0 THEN 'teen' ELSE 'adult' END, 'Hello',
			(ARRAY['self_signup', 'member_invite', 'guest_upgrade'])[1 + n % 3],
			CASE WHEN n % 3 = 1 THEN 4 + n % 4997 END,
			CASE n % 5 WHEN 0 THEN 'pending' WHEN 1 THEN 'approved' WHEN 2 THEN 'rejected' WHEN 3 THEN 'expired' ELSE 'withdrawn' END,
			CASE WHEN n % 5 <> 0 THEN now() END,
			'203.0.113.' || (n % 250), now() + interval '30 days', now(), now(),
			CASE WHEN n % 50 = 0 THEN 'guardian-' || n || '@example.com' ELSE '' END,
			CASE WHEN n % 5 = 1 THEN 4 + n % 4997 END
		FROM generate_series(1, 9960) n`},
	{name: "requests approved for U", sql: `INSERT INTO account_requests (id, name, email, phone, message, source, status, decided_at, decision_note, ip, expires_at, approved_user_id)
		SELECT n, 'Scale Target', 'scale-target-old-' || n || '@example.com', '555-0111', 'Please approve', 'guest_upgrade', 'approved', now(), 'Welcome',
			'203.0.113.7', now() + interval '30 days', 3
		FROM generate_series(9961, 9969) n`},
	{name: "request conversion for U", sql: `INSERT INTO account_requests (id, name, email, phone, message, source, status, decided_at, ip, expires_at, household_person_id, invitation_id, approved_user_id)
		VALUES (9970, 'Pat Target', 'conversion@example.com', '555-0111', 'Convert me', 'household_conversion', 'approved', now(), '203.0.113.8',
			now() + interval '30 days', 1, 4994, 3)`},
	{name: "requests pending at A", sql: `INSERT INTO account_requests (id, name, email, phone, birth_date, message, source, status, ip, expires_at, password_hash)
		SELECT n, 'Scale Target', $1::text, '555-0111', date '1990-01-01', 'Let me in', 'self_signup', 'pending', '203.0.113.9',
			now() + interval '30 days', '$2a$04$scaleseedscaleseedscaleseedscaleseedscaleseedscalese'
		FROM generate_series(9971, 9980) n`, args: []any{scaleAddress}},
	{name: "requests with guardian A", sql: `INSERT INTO account_requests (id, name, email, account_type, birth_date, message, source, status, ip, expires_at, guardian_email)
		SELECT n, 'Teen ' || n, 'teen-' || n || '@example.com', 'teen', date '2011-01-01', 'Please', 'self_signup', 'pending', '203.0.113.10',
			now() + interval '30 days', $1::text
		FROM generate_series(9981, 9990) n`, args: []any{scaleAddress}},
	{name: "requests by U", sql: `INSERT INTO account_requests (id, name, email, source, requested_by, status, ip, expires_at)
		SELECT n, 'Friend ' || n, 'friend-' || n || '@example.com', 'member_invite', 3, 'pending', '198.51.100.7', now() + interval '30 days'
		FROM generate_series(9991, 10000) n`},
	{name: "audit acceptance", sql: `INSERT INTO audit_logs (actor_id, action, target_type, target_id, meta_json, ip, at)
		SELECT NULL, $1::text, 'invitation', id, jsonb_build_object('userId', 4 + id % 4997), '203.0.113.11', now()
		FROM invitations WHERE id <= 4990 AND accepted_at IS NOT NULL`, args: []any{audit.InvitationAccepted}},
	{name: "audit acceptance of U", sql: `INSERT INTO audit_logs (actor_id, action, target_type, target_id, meta_json, ip, at)
		SELECT NULL, $1::text, 'invitation', n, jsonb_build_object('userId', 3), '203.0.113.12', now()
		FROM generate_series(4991, 4993) n`, args: []any{audit.InvitationAccepted}},
	{name: "audit noise", sql: `INSERT INTO audit_logs (actor_id, action, target_type, target_id, meta_json, ip, at)
		SELECT 1 + n % 50, 'group.updated', 'group', n, '{}'::jsonb, '203.0.113.13', now() - n * interval '1 minute'
		FROM generate_series(1, 20000) n`},
	{name: "guest signups", sql: `INSERT INTO guest_signups (id, name, email, occurrence_id, token_hash, expires_at, confirmed_at, ip, timezone, created_at, updated_at)
		SELECT n, 'Guest ' || n, 'guest-' || n || '@example.com', 1 + n % 500, 'scale-gs-' || n, now() + interval '1 day',
			CASE WHEN n % 2 = 0 THEN now() END, '203.0.113.5', 'America/Denver', now(), now()
		FROM generate_series(1, 4990) n`},
	{name: "guest signups at A", sql: `INSERT INTO guest_signups (id, name, email, occurrence_id, token_hash, expires_at, confirmed_at, ip, timezone, created_at, updated_at)
		SELECT n, 'Scale Target', $1::text, 1 + n % 500, 'scale-gs-' || n, now() + interval '1 day',
			CASE WHEN n >= 4996 THEN now() END, '203.0.113.6', 'America/Denver', now(), now()
		FROM generate_series(4991, 5000) n`, args: []any{scaleAddress}},
	{name: "mail outbox", sql: `INSERT INTO mail_outboxes (id, to_address, subject, body, created_at)
		SELECT n, 'outbox-' || n || '@example.com', 'Subject ' || n, 'Body', now() FROM generate_series(1, 1995) n`},
	{name: "mail outbox to A", sql: `INSERT INTO mail_outboxes (id, to_address, subject, body, created_at)
		SELECT 1995 + n, (ARRAY['scale-target@example.com', 'Scale-Target@Example.com', ' scale-target@example.com', 'scale-target@example.com ', 'SCALE-TARGET@EXAMPLE.COM'])[n],
			'Subject ' || n, 'Body', now()
		FROM generate_series(1, 5) n`},
	{name: "messages", sql: `INSERT INTO messages (id, channel, author_id, status, category, subject, body_text, scheduled_at, sent_at, recipient_count, created_at, updated_at)
		SELECT m, 'email', CASE WHEN m > 290 THEN 3 ELSE 1 END, CASE WHEN m > 290 THEN 'scheduled' ELSE 'sent' END,
			'announcement', 'Scale message ' || m, 'Seeded body',
			CASE WHEN m > 290 THEN now() + interval '1 day' END, CASE WHEN m <= 290 THEN now() - interval '1 day' END,
			413, now(), now()
		FROM generate_series(1, 300) m`},
	{name: "recipients of others", sql: `INSERT INTO message_recipients (message_id, user_id, channel, address, status, error, created_at, updated_at)
		SELECT m, u, 'email', 'recipient-' || u || '@example.com', st, CASE WHEN st = 'failed' THEN 'mailbox full' ELSE '' END, now(), now()
		FROM (SELECT 1 + r % 290 AS m, 4 + ((1 + r % 290) * 397 + r / 290) % 4997 AS u, (ARRAY['queued', 'sent', 'failed'])[1 + r % 3] AS st
			FROM generate_series(0, 119699) r) o`},
	{name: "recipients U direct", sql: `INSERT INTO message_recipients (message_id, user_id, channel, address, status, error, created_at, updated_at)
		SELECT m, 3, 'email', $1::text, (ARRAY['queued', 'sent', 'failed'])[1 + m % 3], CASE WHEN m % 3 = 2 THEN 'mailbox full' ELSE '' END, now(), now()
		FROM generate_series(1, 280) m`, args: []any{scaleAddress}},
	{name: "recipients U guardian copy", sql: `INSERT INTO message_recipients (message_id, user_id, channel, address, status, copy_of_user_id, created_at, updated_at)
		SELECT m, 3, 'email', $1::text, (ARRAY['queued', 'sent'])[1 + m % 2], 4, now(), now()
		FROM generate_series(1, 20) m`, args: []any{scaleAddress}},
	{name: "registrations", sql: `INSERT INTO registrations (id, occurrence_id, user_id, status, source, manage_token_hash, childcare_ages_note, removal_note, created_at, updated_at)
		SELECT id, o, u, 'going', 'web', CASE WHEN u = 3 OR id % 20 = 0 THEN 'scale-mt-' || id ELSE '' END,
			CASE WHEN u = 3 THEN 'ages 2 and 4' ELSE '' END, '', now(), now()
		FROM (SELECT (o - 1) * 100 + k + 1 AS id, o, CASE WHEN k = 0 AND o <= 60 THEN 3 ELSE 4 + (o * 37 + k) % 4997 END AS u
			FROM generate_series(1, 500) o, generate_series(0, 99) k) r`},
	// Every row carries evidence as setConsent's web_form path records it: the
	// request's ip and user agent on all four, and an evidence_text on the
	// scope 'all' row (one a person) only.
	{name: "consents", sql: `INSERT INTO consents (user_id, channel, scope, status, source, ip, user_agent, evidence_text)
		SELECT u, 'email', s, 'opted_in', 'web_form', '198.51.100.' || (1 + u % 250), 'Scale agent ' || (u % 7),
			CASE WHEN s = 'all' THEN 'Scale evidence ' || u ELSE '' END
		FROM generate_series(1, 5000) u, unnest(ARRAY['all', 'category:groups', 'category:events', 'category:messages']) s`},
	{name: "notifications", sql: `INSERT INTO notifications (user_id, kind, category, title, link, created_at, updated_at)
		SELECT u, 'group.joined', 'groups', 'Notice ' || k, '/notifications', now(), now()
		FROM generate_series(1, 5000) u, generate_series(1, 4) k`},
	{name: "notifications of U", sql: `INSERT INTO notifications (user_id, kind, category, title, link, created_at, updated_at)
		SELECT 3, 'group.joined', 'groups', 'Notice ' || k, '/notifications', now(), now() FROM generate_series(5, 100) k`},
	{name: "tasks", sql: `INSERT INTO notifications (user_id, kind, category, waiting, subject_type, subject_id, title, link, created_at, updated_at)
		SELECT a.id, $1::text, 'admin', true, 'account_request', r.id, 'Account request', '/admin/requests', now(), now()
		FROM account_requests r CROSS JOIN (VALUES (1), (2)) AS a(id) WHERE r.status = 'pending'`, args: []any{notify.AccountRequested}},
	{name: "deliveries", sql: `INSERT INTO notification_deliveries (notification_id, user_id, channel, status, sent_at, created_at, updated_at)
		SELECT id, user_id, 'email', 'sent', now(), now(), now() FROM notifications WHERE kind <> $1::text`, args: []any{notify.AccountRequested}},
	{name: "deliveries of U on others", sql: `INSERT INTO notification_deliveries (notification_id, user_id, channel, status, sent_at, created_at, updated_at)
		SELECT id, 3, 'sms', 'sent', now(), now(), now() FROM notifications
		WHERE user_id BETWEEN 10 AND 19 AND kind <> $1::text ORDER BY id LIMIT 10`, args: []any{notify.AccountRequested}},
	{name: "series undo receipts", sql: `INSERT INTO series_undo_receipts (token_hash, actor_id, event_id, snapshot, expires_at, created_at)
		SELECT 'scale-undo-' || n, CASE WHEN n <= 5 THEN 3 ELSE 4 + n % 4997 END, 1 + n % 5, '[]'::jsonb,
			now() + interval '1 hour', now() - interval '1 hour'
		FROM generate_series(1, 1000) n`},
	{name: "manager add blocks", sql: `INSERT INTO manager_add_blocks (subject_user_id, organizer_type, organizer_id)
		SELECT CASE WHEN n <= 5 THEN 3 ELSE 4 + n % 4997 END, 'group', n FROM generate_series(1, 1000) n`},
	// R4-T07A-4: contact_requests at the size of a large church. Synthetic text
	// only. Subjects 4-3003 and requesters 3004-4503 never meet U (3), so the
	// bulk rows are the unrelated control, pending ones included. They sit in
	// contexts U neither leads nor hosts (the fixture has no groups, and U has
	// no membership or hosting). No granted row: the deferred completion check
	// would need a matching registration.
	{name: "contact requests unrelated", sql: scaleContactRequestsSQL(`SELECT n,
			3004 + n % 1500, 4 + n % 3000,
			(ARRAY['contact', 'group_add', 'occurrence_add'])[1 + n % 3],
			(ARRAY['pending', 'denied', 'withdrawn', 'expired', 'ended'])[1 + n % 5]
		FROM generate_series(1, 9992) n`)},
	{name: "contact requests with U as subject", sql: scaleContactRequestsSQL(`VALUES
			(9993, 3004, 3, 'occurrence_add', 'denied'), (9994, 3005, 3, 'occurrence_add', 'ended'),
			(9995, 3006, 3, 'occurrence_add', 'pending'), (9996, 3007, 3, 'occurrence_add', 'pending')`)},
	{name: "contact requests by U only", sql: scaleContactRequestsSQL(`VALUES
			(9997, 3, 4010, 'occurrence_add', 'denied'), (9998, 3, 4011, 'contact', 'ended'),
			(9999, 3, 4012, 'occurrence_add', 'pending'), (10000, 3, 4013, 'group_add', 'pending')`)},
}

// scaleContactRequestsSQL is the INSERT of contact_requests rows for the
// tuples (n, requester, subject, purpose, status) that source selects. Every
// row honors the table's CHECKs: the purpose's scope columns, the decision
// and lifecycle columns of its status (a denied row carries the subject's
// denial, a withdrawn one its requester, an expired one completes after
// expires_at), and an occurrence_add row carries a non-empty synthetic ages
// note. Reasons are synthetic too. 'granted' is not a status it can write.
func scaleContactRequestsSQL(source string) string {
	return `INSERT INTO contact_requests (id, requester_id, subject_user_id, context_type, context_id, fields, reason, needs_admin,
			subject_decision, subject_decided_by, subject_decided_at, admin_decision, status, created_at, expires_at, completed_at,
			withdrawn_by_id, end_reason, purpose, occurrence_id, add_attendance_mode, add_guest_count, add_childcare_count, add_childcare_ages_note)
		SELECT s.n, s.requester, s.subject,
			CASE s.purpose WHEN 'occurrence_add' THEN 'event' WHEN 'group_add' THEN 'group' ELSE (ARRAY['group', 'event'])[1 + s.n % 2] END,
			CASE WHEN s.purpose = 'occurrence_add' THEN 1 + (s.n % 500) / 100
				WHEN s.purpose = 'contact' AND s.n % 2 = 1 THEN 1 + s.n % 5
				ELSE 1 + s.n % 40 END,
			CASE WHEN s.purpose = 'contact' THEN (ARRAY['email', 'phone', 'both'])[1 + s.n % 3] ELSE '' END,
			'Synthetic reason ' || s.n, false,
			CASE WHEN s.status = 'denied' THEN 'denied' ELSE 'pending' END,
			CASE WHEN s.status = 'denied' THEN s.subject END,
			CASE WHEN s.status = 'denied' THEN s.made + interval '5 minutes' END,
			'not_needed', s.status, s.made, s.made + interval '14 days',
			CASE s.status WHEN 'pending' THEN NULL
				WHEN 'expired' THEN s.made + interval '14 days' + interval '1 minute'
				ELSE s.made + interval '10 minutes' END,
			CASE WHEN s.status = 'withdrawn' THEN s.requester END,
			CASE WHEN s.status = 'ended' THEN 'event_ended' ELSE '' END,
			s.purpose,
			CASE WHEN s.purpose = 'occurrence_add' THEN 1 + s.n % 500 END,
			CASE WHEN s.purpose = 'occurrence_add' THEN (ARRAY['in_person', 'remote'])[1 + s.n % 2] ELSE '' END,
			CASE WHEN s.purpose = 'occurrence_add' THEN s.n % 6 ELSE 0 END,
			CASE WHEN s.purpose = 'occurrence_add' THEN s.n % 4 ELSE 0 END,
			CASE WHEN s.purpose = 'occurrence_add' THEN 'Synthetic ages note ' || s.n ELSE '' END
		FROM (SELECT v.*, CASE WHEN v.status = 'expired' THEN now() - interval '40 days' ELSE now() - interval '1 hour' END AS made
			FROM (` + source + `) AS v(n, requester, subject, purpose, status)) s`
}

// scaleSequenceTables are the tables the fixture gave explicit ids; their
// sequences are moved past the fixture so later inserts (audit rows, new
// notifications) cannot collide.
var scaleSequenceTables = []string{"users", "events", "event_occurrences", "households", "household_people",
	"messages", "account_requests", "invitations", "guest_signups", "mail_outboxes", "registrations", "contact_requests"}

// newScaleScene creates a fresh migrated database, the two admins and U, seeds
// the rest of the fixture with SQL, refreshes the planner statistics (without
// them the plans would be the ones of an empty database) and signs the admin
// in. base only lends its configuration. Every call is a new database, so each
// timing starts from the same untouched rows.
func newScaleScene(t *testing.T, base *App) (*App, *apiClient) {
	t.Helper()
	db, err := testdb.Open(testdb.Fresh(t, true))
	if err != nil {
		t.Fatalf("scale database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	// Registered after Fresh's drop, so it runs before it.
	t.Cleanup(func() { sqlDB.Close() })
	app := &App{DB: db, Store: base.Store, Config: base.Config, Uploads: base.Uploads,
		Mailer: mail.WithSuppressions(db, mail.NewDevOutbox(db, "Fold <noreply@example.com>"))}
	admin := newTestUser(t, app, scaleAdminName, erasurePW, models.RoleAdmin)
	second := newTestUser(t, app, scaleSecondName, erasurePW, models.RoleAdmin)
	target := newTestUser(t, app, scaleTargetName, erasurePW, models.RoleMember)
	if admin.ID != 1 || second.ID != 2 || target.ID != scaleTargetID || target.Email != scaleAddress {
		t.Fatalf("fixture ids are fixed: admin %d, second %d, target %d (%s)", admin.ID, second.ID, target.ID, target.Email)
	}
	ctx := context.Background()
	started := time.Now()
	for _, s := range scaleSeeds {
		if _, err := sqlDB.ExecContext(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("seed %s: %v", s.name, err)
		}
	}
	for _, table := range scaleSequenceTables {
		if _, err := sqlDB.ExecContext(ctx, fmt.Sprintf(
			"SELECT setval(pg_get_serial_sequence('%s', 'id'), (SELECT max(id) FROM %s))", table, table)); err != nil {
			t.Fatalf("sequence of %s: %v", table, err)
		}
	}
	if _, err := sqlDB.ExecContext(ctx, "ANALYZE"); err != nil {
		t.Fatalf("analyze: %v", err)
	}
	t.Logf("seeded in %s", time.Since(started).Round(time.Millisecond))
	checkScaleFixture(t, db)
	return app, loginAPI(t, app, scaleAdminName, erasurePW)
}

// checkScaleFixture fails the test when the fixture is not the shape the
// plan names, so a typo in the seed cannot quietly measure a smaller church.
// It also reads the three sets through the real capture, so the selectors the
// erasure uses see exactly REQ 20, PERS 1 and INV 10.
func checkScaleFixture(t *testing.T, db *gorm.DB) {
	t.Helper()
	want := []struct {
		what  string
		query string
		args  []any
		n     int64
	}{
		{"users", "SELECT count(*) FROM users", nil, 5000},
		{"messages", "SELECT count(*) FROM messages", nil, 300},
		{"message_recipients", "SELECT count(*) FROM message_recipients", nil, 120000},
		{"recipient rows of U", "SELECT count(*) FROM message_recipients WHERE user_id = 3", nil, 300},
		{"direct rows of U", "SELECT count(*) FROM message_recipients WHERE user_id = 3 AND copy_of_user_id IS NULL", nil, 280},
		{"guardian copies of U", "SELECT count(*) FROM message_recipients WHERE user_id = 3 AND copy_of_user_id IS NOT NULL", nil, 20},
		{"queued rows of U", "SELECT count(*) FROM message_recipients WHERE user_id = 3 AND status = 'queued'", nil, 103},
		{"scheduled messages of U", "SELECT count(*) FROM messages WHERE author_id = 3 AND status = 'scheduled'", nil, 10},
		{"account_requests", "SELECT count(*) FROM account_requests", nil, 10000},
		{"requests of U (REQ, guardian_email, member_invite)", `SELECT count(*) FROM account_requests
			WHERE approved_user_id = 3 OR (approved_user_id IS NULL AND email = ?) OR guardian_email = ?
			OR (source = 'member_invite' AND requested_by = 3)`, []any{scaleAddress, scaleAddress}, 40},
		{"pending REQ of U", "SELECT count(*) FROM account_requests WHERE status = 'pending' AND approved_user_id IS NULL AND email = ?", []any{scaleAddress}, 10},
		{"invitations", "SELECT count(*) FROM invitations", nil, 5000},
		{"guest_signups", "SELECT count(*) FROM guest_signups", nil, 5000},
		{"guest_signups at A", "SELECT count(*) FROM guest_signups WHERE email = ?", []any{scaleAddress}, 10},
		{"unconfirmed guest_signups at A", "SELECT count(*) FROM guest_signups WHERE email = ? AND confirmed_at IS NULL", []any{scaleAddress}, 5},
		{"mail_outboxes", "SELECT count(*) FROM mail_outboxes", nil, 2000},
		{"mail_outboxes to A", "SELECT count(*) FROM mail_outboxes WHERE lower(btrim(to_address)) = ?", []any{scaleAddress}, 5},
		{"registrations", "SELECT count(*) FROM registrations", nil, 50000},
		{"registrations of U", "SELECT count(*) FROM registrations WHERE user_id = 3", nil, 60},
		{"consents", "SELECT count(*) FROM consents", nil, 20000},
		// R4-2d1b-2: an empty measurement cannot pass. U's four rows all carry
		// evidence (one with an evidence_text) and so do everyone else's.
		{"consents of U with a non-empty field", `SELECT count(*) FROM consents
			WHERE user_id = 3 AND (ip <> '' OR user_agent <> '' OR evidence_text <> '')`, nil, 4},
		{"consents of U with a non-empty evidence_text", "SELECT count(*) FROM consents WHERE user_id = 3 AND evidence_text <> ''", nil, 1},
		{"consents of everyone else with a non-empty ip and user_agent", `SELECT count(*) FROM consents
			WHERE user_id <> 3 AND ip <> '' AND user_agent <> ''`, nil, 19996},
		{"series_undo_receipts of U", "SELECT count(*) FROM series_undo_receipts WHERE actor_id = 3", nil, 5},
		{"manager_add_blocks of U", "SELECT count(*) FROM manager_add_blocks WHERE subject_user_id = 3", nil, 5},
		{"deliveries of U on others", `SELECT count(*) FROM notification_deliveries d JOIN notifications n ON n.id = d.notification_id
			WHERE d.user_id = 3 AND n.user_id <> 3`, nil, 10},
		// R4-T07A-4: the counts the ages-note oracles and the verdict's S_U read.
		{"contact_requests", "SELECT count(*) FROM contact_requests", nil, 10000},
		{"distinct subjects of contact_requests", "SELECT count(DISTINCT subject_user_id) FROM contact_requests", nil, 3005},
		{"granted contact_requests", "SELECT count(*) FROM contact_requests WHERE status = 'granted'", nil, 0},
		{"occurrence_add contact_requests with an empty note", "SELECT count(*) FROM contact_requests WHERE purpose = 'occurrence_add' AND add_childcare_ages_note = ''", nil, 0},
		{"contact_requests of other purposes with a note", "SELECT count(*) FROM contact_requests WHERE purpose <> 'occurrence_add' AND add_childcare_ages_note <> ''", nil, 0},
		{"contact_requests with a reason", "SELECT count(*) FROM contact_requests WHERE reason <> ''", nil, 10000},
		{"contact purpose rows", "SELECT count(*) FROM contact_requests WHERE purpose = 'contact'", nil, 3330 + 1},
		{"group_add purpose rows", "SELECT count(*) FROM contact_requests WHERE purpose = 'group_add'", nil, 3331 + 1},
		{"occurrence_add purpose rows", "SELECT count(*) FROM contact_requests WHERE purpose = 'occurrence_add'", nil, 3331 + 4 + 2},
		{"S_U: contact_requests with U as subject", "SELECT count(*) FROM contact_requests WHERE subject_user_id = 3", nil, 4},
		{"U subject occurrence_add rows with a note", "SELECT count(*) FROM contact_requests WHERE subject_user_id = 3 AND purpose = 'occurrence_add' AND add_childcare_ages_note <> ''", nil, 4},
		{"U subject rows, terminal", "SELECT count(*) FROM contact_requests WHERE subject_user_id = 3 AND status <> 'pending'", nil, 2},
		{"U subject rows, pending", "SELECT count(*) FROM contact_requests WHERE subject_user_id = 3 AND status = 'pending'", nil, 2},
		{"rows where U is only the requester", "SELECT count(*) FROM contact_requests WHERE requester_id = 3 AND subject_user_id <> 3", nil, 4},
		{"rows where U is only the requester, terminal", "SELECT count(*) FROM contact_requests WHERE requester_id = 3 AND subject_user_id <> 3 AND status <> 'pending'", nil, 2},
		{"rows where U is only the requester, pending", "SELECT count(*) FROM contact_requests WHERE requester_id = 3 AND subject_user_id <> 3 AND status = 'pending'", nil, 2},
		{"rows where U is only the requester with a note", "SELECT count(*) FROM contact_requests WHERE requester_id = 3 AND subject_user_id <> 3 AND add_childcare_ages_note <> ''", nil, 2},
		{"unrelated contact_requests (neither subject nor requester is U)", "SELECT count(*) FROM contact_requests WHERE subject_user_id <> 3 AND requester_id <> 3", nil, 9992},
		{"unrelated pending contact_requests", "SELECT count(*) FROM contact_requests WHERE subject_user_id <> 3 AND requester_id <> 3 AND status = 'pending'", nil, 1998},
		{"unrelated pending occurrence_add contact_requests with a note", "SELECT count(*) FROM contact_requests WHERE subject_user_id <> 3 AND requester_id <> 3 AND status = 'pending' AND purpose = 'occurrence_add' AND add_childcare_ages_note <> ''", nil, 666},
		{"unrelated contact_requests in a status", "SELECT count(DISTINCT status) FROM contact_requests WHERE subject_user_id <> 3 AND requester_id <> 3", nil, 5},
		// U leads and hosts nothing, so no unrelated request sits in a context U runs.
		{"memberships of U", "SELECT count(*) FROM memberships WHERE user_id = 3", nil, 0},
		{"event_hosts of U", "SELECT count(*) FROM event_hosts WHERE user_id = 3", nil, 0},
	}
	for _, w := range want {
		var n int64
		if err := db.Raw(w.query, w.args...).Scan(&n).Error; err != nil {
			t.Fatalf("fixture check %s: %v", w.what, err)
		}
		if n != w.n {
			t.Fatalf("fixture has %d %s, want %d", n, w.what, w.n)
		}
	}
	var u models.User
	if err := db.First(&u, scaleTargetID).Error; err != nil {
		t.Fatal(err)
	}
	s := captureErasureSubject(&u)
	if err := captureErasureSetsIn(db, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Requests) != 20 || len(s.People) != 1 || len(s.Invitations) != 10 {
		t.Fatalf("captured REQ %d, PERS %d, INV %d, want 20, 1 and 10", len(s.Requests), len(s.People), len(s.Invitations))
	}
}

// scaleStatement names one statement the report covers: the erasure statements
// R4-2a added, the three capture queries it added to the lock, and the
// snapshot lock query. A statement belongs to the first entry whose every
// fragment its (whitespace-collapsed) SQL contains. The fragments are the
// literal text of the production SQL, so a reworded statement stops matching
// and the test says which one went missing.
type scaleStatement struct {
	label string
	has   []string
}

var scaleStatements = []scaleStatement{
	{"capture REQ", []string{"FROM account_requests", "approved_user_id IS NULL AND email"}},
	{"capture PERS", []string{"FROM household_people WHERE converted_user_id"}},
	{"capture INV a: acceptance audit", []string{"FROM audit_logs", "invitation.accepted"}},
	{"capture INV b: conversion request", []string{"SELECT invitation_id FROM account_requests"}},
	{"capture INV c: household person", []string{"FROM invitations WHERE household_person_id IN"}},
	{"capture INV d: unaccepted at address", []string{"FROM invitations WHERE email", "accepted_at IS NULL"}},
	{"requests 1: withdraw pending", []string{"UPDATE account_requests SET status = 'withdrawn'"}},
	{"requests 2: scrub REQ", []string{"UPDATE account_requests SET name"}},
	{"requests 3: member_invite ip", []string{"UPDATE account_requests SET ip"}},
	{"requests 4: guardian_email", []string{"UPDATE account_requests SET guardian_email"}},
	{"invitations 5: scrub INV", []string{"UPDATE invitations SET"}},
	{"guest_signups 6: delete unconfirmed", []string{"DELETE FROM guest_signups"}},
	{"guest_signups 6: anonymize confirmed", []string{"UPDATE guest_signups SET"}},
	{"mail_outboxes 7: delete", []string{"DELETE FROM mail_outboxes"}},
	{"messages 8: cancel scheduled", []string{"UPDATE messages SET"}},
	{"message_recipients 9: scrub", []string{"UPDATE message_recipients SET"}},
	{"series_undo_receipts 10: delete", []string{"DELETE FROM series_undo_receipts"}},
	{"manager_add_blocks 11: delete", []string{"DELETE FROM manager_add_blocks"}},
	{"registrations 12: scrub", []string{"UPDATE registrations SET"}},
	{"notification_deliveries 13: delete", []string{"DELETE FROM notification_deliveries"}},
	{scaleConsentLabel, []string{"UPDATE consents SET ip"}},
	{"snapshot lock: users FOR KEY SHARE", []string{`FROM "users"`, "FOR KEY SHARE"}},
	// R4-T07A-4. Dropping ORDER BY id from the helper's lock selection makes the
	// first fragment set match nothing, and the run fails with "no plan for".
	{scaleAgesNoteSelectLabel, []string{"FROM contact_requests WHERE purpose = 'occurrence_add' AND subject_user_id", "ORDER BY id FOR UPDATE"}},
	{scaleAgesNoteUpdateLabel, []string{"UPDATE contact_requests SET add_childcare_ages_note"}},
}

const (
	scaleRecipientLabel      = "message_recipients 9: scrub"
	scaleConsentLabel        = "consents 14: scrub evidence"
	scaleAgesNoteSelectLabel = "contact_requests 15: ages-note lock selection"
	scaleAgesNoteUpdateLabel = "contact_requests 16: ages-note scrub"
)

func scaleLabel(sqlText string) string {
	sqlText = strings.Join(strings.Fields(sqlText), " ")
	for _, s := range scaleStatements {
		match := true
		for _, f := range s.has {
			if !strings.Contains(sqlText, f) {
				match = false
				break
			}
		}
		if match {
			return s.label
		}
	}
	return ""
}

// scalePlan is the EXPLAIN (ANALYZE, BUFFERS) output of one statement.
type scalePlan struct {
	label string
	lines []string
	// sql and vars are the statement text and bind values EXPLAIN ANALYZE
	// really executed (the real execution follows with the same ones). They
	// let the ages-note classifier bind the plan's displayed values to this
	// exact execution. vars hold ids only; they never reach a log.
	sql  string
	vars []any
}

// scaleExecution is one real execution of a statement whose RowsAffected the
// explainer records: its text, its bind values and the rows the driver
// reported.
type scaleExecution struct {
	sql  string
	vars []any
	rows int64
}

// scaleExplainer runs EXPLAIN (ANALYZE, BUFFERS) on the statements of the real
// erasure functions without a copy of their SQL. While armed, a GORM callback
// before each raw, row and query statement that sits in a transaction and
// matches scaleStatements opens a savepoint, runs the EXPLAIN on the same
// connection (ANALYZE really executes the statement), rolls back to the
// savepoint and lets the statement run for real, so the production code sees
// the state it would have seen and the erasure order is kept. Statements that
// GORM builds inside its own callbacks (the model Delete of notifications, the
// Updates in Resolve, Create in audit.Write) are not seen.
type scaleExplainer struct {
	armed atomic.Bool
	mu    sync.Mutex
	plans []scalePlan
	// affected is, per label that records it (scaleConsentLabel), the
	// RowsAffected of each real execution, read by an After gorm:raw callback.
	// The execution runs after the EXPLAIN's savepoint is rolled back, so it
	// sees the same rows the EXPLAIN did.
	affected map[string][]int64
	// execs is, for scaleAgesNoteUpdateLabel, the text, binds and RowsAffected
	// of each real execution (same After gorm:raw callback).
	execs map[string][]scaleExecution
	name  string
	db    *gorm.DB
}

func newScaleExplainer(t *testing.T, db *gorm.DB) *scaleExplainer {
	t.Helper()
	x := &scaleExplainer{name: "test:scale_explain", db: db, affected: map[string][]int64{}, execs: map[string][]scaleExecution{}}
	if err := db.Callback().Raw().Before("gorm:raw").Register(x.name, x.hook); err != nil {
		t.Fatalf("register raw explainer: %v", err)
	}
	if err := db.Callback().Raw().After("gorm:raw").Register(x.name+":rows", x.rowsHook); err != nil {
		t.Fatalf("register raw rows callback: %v", err)
	}
	if err := db.Callback().Row().Before("gorm:row").Register(x.name, x.hook); err != nil {
		t.Fatalf("register row explainer: %v", err)
	}
	if err := db.Callback().Query().Before("gorm:query").Register(x.name, x.hook); err != nil {
		t.Fatalf("register query explainer: %v", err)
	}
	t.Cleanup(func() {
		x.armed.Store(false)
		_ = db.Callback().Raw().Remove(x.name)
		_ = db.Callback().Raw().Remove(x.name + ":rows")
		_ = db.Callback().Row().Remove(x.name)
		_ = db.Callback().Query().Remove(x.name)
	})
	return x
}

// rowsHook records how many rows the real execution of the consent scrub and
// of the ages-note scrub touched (db.RowsAffected, which RawExec sets from the
// driver's result), and the ages-note statement's text and binds. It adds no
// RETURNING to the production statement and runs no statement of its own.
func (x *scaleExplainer) rowsHook(db *gorm.DB) {
	if !x.armed.Load() || db.Error != nil || db.DryRun || db.Statement.ConnPool == nil {
		return
	}
	label := scaleLabel(db.Statement.SQL.String())
	if label != scaleConsentLabel && label != scaleAgesNoteUpdateLabel {
		return
	}
	x.mu.Lock()
	x.affected[label] = append(x.affected[label], db.RowsAffected)
	if label == scaleAgesNoteUpdateLabel {
		x.execs[label] = append(x.execs[label], scaleExecution{sql: db.Statement.SQL.String(),
			vars: append([]any(nil), db.Statement.Vars...), rows: db.RowsAffected})
	}
	x.mu.Unlock()
}

func (x *scaleExplainer) hook(db *gorm.DB) {
	if !x.armed.Load() || db.Error != nil || db.DryRun || db.Statement.ConnPool == nil {
		return
	}
	// Only inside a transaction: the savepoint below needs one.
	if _, ok := db.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return
	}
	if db.Statement.SQL.Len() == 0 {
		callbacks.BuildQuerySQL(db)
		if db.Error != nil {
			return
		}
	}
	text := db.Statement.SQL.String()
	label := scaleLabel(text)
	if label == "" {
		return
	}
	ctx, pool := db.Statement.Context, db.Statement.ConnPool
	var lines []string
	err := func() error {
		if _, err := pool.ExecContext(ctx, "SAVEPOINT scale_explain"); err != nil {
			return err
		}
		rows, err := pool.QueryContext(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+text, db.Statement.Vars...)
		if err == nil {
			for rows.Next() {
				var line string
				if err = rows.Scan(&line); err != nil {
					break
				}
				lines = append(lines, line)
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
		}
		// Undo the executed statement, also after a failure, so the
		// transaction is usable again either way.
		if _, rerr := pool.ExecContext(ctx, "ROLLBACK TO SAVEPOINT scale_explain"); rerr != nil && err == nil {
			err = rerr
		}
		if _, rerr := pool.ExecContext(ctx, "RELEASE SAVEPOINT scale_explain"); rerr != nil && err == nil {
			err = rerr
		}
		return err
	}()
	if err != nil {
		db.AddError(fmt.Errorf("explain %s: %w", label, err))
		return
	}
	x.mu.Lock()
	x.plans = append(x.plans, scalePlan{label: label, lines: lines, sql: text, vars: append([]any(nil), db.Statement.Vars...)})
	x.mu.Unlock()
}

var (
	scaleActualRe = regexp.MustCompile(`actual time=[\d.]+\.\.[\d.]+ rows=([\d.]+)`)
	scaleExecRe   = regexp.MustCompile(`Execution Time: ([\d.]+) ms`)
	scaleScanRe   = regexp.MustCompile(`(?:Seq Scan|Index Scan|Index Only Scan|Bitmap Heap Scan)`)
	scaleSeqRe    = regexp.MustCompile(`Seq Scan on (\w+)`)
	scaleRemovedR = regexp.MustCompile(`Rows Removed by Filter: (\d+)`)
)

// scaleRow is one line of the report.
type scaleRow struct {
	label string
	ms    float64
	rows  string
	node  string
	seq   string
}

func summarizeScalePlan(p scalePlan) scaleRow {
	r := scaleRow{label: p.label, rows: "-", node: "-", seq: "-"}
	var seqs []string
	for i, line := range p.lines {
		if m := scaleExecRe.FindStringSubmatch(line); m != nil {
			r.ms, _ = strconv.ParseFloat(m[1], 64)
		}
		if m := scaleSeqRe.FindStringSubmatch(line); m != nil {
			seen := false
			for _, s := range seqs {
				seen = seen || s == m[1]
			}
			if !seen {
				seqs = append(seqs, m[1])
			}
		}
		if r.node != "-" || !scaleScanRe.MatchString(line) {
			continue
		}
		node := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "->"))
		if cut := strings.Index(node, "  (cost="); cut >= 0 {
			node = node[:cut]
		}
		r.node = node
		if m := scaleActualRe.FindStringSubmatch(line); m != nil {
			r.rows = m[1]
		}
		for _, next := range p.lines[i+1:] {
			if strings.Contains(next, "->") {
				break
			}
			if m := scaleRemovedR.FindStringSubmatch(next); m != nil {
				r.rows += " (" + m[1] + " removed by filter)"
				break
			}
		}
	}
	if len(seqs) > 0 {
		r.seq = strings.Join(seqs, ", ")
	}
	return r
}

var errScaleRollback = errors.New("scale explain: roll back")

// explainScaleErasure runs the erasure's statements in the order of
// apiAdminDeleteUser (capture, account requests, the notifications delete,
// dependent records) plus the snapshot lock query at a 5,000-person
// audience, all in one transaction that is rolled back, and returns the plans.
// It leaves out the gate, the credential deletes, the hand-offs, the tombstone,
// promoteOnDisable and the throttle delete: none of them is a statement R4-2a
// added.
//
// R4-2d1b-2: after the snapshot-lock check, which needs all 5,000 users live,
// it runs the model soft delete apiAdminDeleteUser runs and then
// eraseConsentEvidenceIn, in that order, so the consent scrub's EXPLAIN runs
// after the transaction's own soft delete with the trigger's users lookup
// seeing deleted_at (before it the statement raises P0001, so a recorded plan
// proves the position).
//
// R4-T07A-4: eraseContactRequestTextIn follows, still after the soft delete
// (before it the UPDATE raises 23514 and no plan is recorded), with the
// helper-only oracle around it. This is a rolled-back statement-plan
// measurement, not production order: the capture, account-request,
// notification, dependent-record and snapshot-lock statements run before the
// soft delete here, and it omits the contact gate, deletionHostPlan, the
// hand-offs, promoteOnDisable, the final throttle delete and contactMutation's
// reconcileContacts. It proves the plans on representative data and the
// helper's own scope; it does not prove production statement order or lock
// order. The harness is not reordered in this lane.
func explainScaleErasure(t *testing.T, app *App) scaleExplain {
	t.Helper()
	facts := readScaleAgesNoteFacts(t, app.DB)
	if len(facts.BatchIDs) == 0 {
		t.Fatal("the fixture has no ages note for U to scrub")
	}
	var helperOnly scaleHelperOnlyResult
	x := newScaleExplainer(t, app.DB)
	err := app.DB.Transaction(func(tx *gorm.DB) error {
		var u models.User
		if err := tx.First(&u, scaleTargetID).Error; err != nil {
			return err
		}
		x.armed.Store(true)
		defer x.armed.Store(false)
		s := captureErasureSubject(&u)
		if err := captureErasureSetsIn(tx, &s); err != nil {
			return err
		}
		if err := eraseAccountRequestsIn(tx, s, 1); err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", u.ID).Delete(&models.Notification{}).Error; err != nil {
			return err
		}
		if err := eraseDependentRecordsIn(tx, s, 1, "198.51.100.1"); err != nil {
			return err
		}
		// The send path's lock over a 5,000-person audience: every user
		// once, a hundred of them also as the teen of a guardian copy.
		audience := make([]models.MessageRecipient, 0, 5100)
		for id := uint(1); id <= 5000; id++ {
			audience = append(audience, models.MessageRecipient{UserID: id, Channel: models.DeliveryEmail})
		}
		teen := uint(4)
		for i := 0; i < 100; i++ {
			audience = append(audience, models.MessageRecipient{UserID: 5 + uint(i), Channel: models.DeliveryEmail, CopyOfUserID: &teen})
		}
		live, err := lockSnapshotUsers(tx, audience)
		if err != nil {
			return err
		}
		if len(live) != 5000 {
			return fmt.Errorf("snapshot lock saw %d live users, want 5000", len(live))
		}
		if err := tx.Delete(&u).Error; err != nil {
			return err
		}
		if err := eraseConsentEvidenceIn(tx, s); err != nil {
			return err
		}
		scopeBefore, err := scaleHelperScopeState(tx, facts.BatchIDs)
		if err != nil {
			return err
		}
		if err := eraseContactRequestTextIn(tx, s); err != nil {
			return err
		}
		x.armed.Store(false)
		scopeAfter, err := scaleHelperScopeState(tx, facts.BatchIDs)
		if err != nil {
			return err
		}
		helperOnly = scaleHelperOnlyCompare(scopeBefore, scopeAfter)
		return errScaleRollback
	})
	if !errors.Is(err, errScaleRollback) {
		t.Fatalf("explain transaction: %v", err)
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	out := scaleExplain{plans: append([]scalePlan(nil), x.plans...), affected: map[string][]int64{}, execs: map[string][]scaleExecution{},
		agesFacts: facts, helperOnly: helperOnly}
	for label, n := range x.affected {
		out.affected[label] = append([]int64(nil), n...)
	}
	for label, e := range x.execs {
		out.execs[label] = append([]scaleExecution(nil), e...)
	}
	return out
}

// scaleExplain is what explainScaleErasure measured: the plans, and the rows
// each real execution of the consent scrub (scaleConsentLabel) and of the
// ages-note UPDATE (scaleAgesNoteUpdateLabel) touched.
type scaleExplain struct {
	plans    []scalePlan
	affected map[string][]int64
	// execs are the real executions of the ages-note UPDATE (R4-T07A-4); the
	// two fields after them are the fixture facts and the helper-only oracle.
	execs      map[string][]scaleExecution
	agesFacts  scaleAgesNoteFacts
	helperOnly scaleHelperOnlyResult
}

var (
	// scaleTriggerRe is the line EXPLAIN ANALYZE prints for a trigger that
	// fired: one call per updated row, none for a row the filter skipped.
	scaleTriggerRe = regexp.MustCompile(`Trigger consents_append_only: time=[\d.]+ calls=(\d+)`)
	// scaleConsentScanRe is the node that feeds 'Update on consents': whichever
	// scan of consents the planner chose, with its actual rows and loops from
	// the same line.
	scaleConsentScanRe = regexp.MustCompile(`(?:Seq Scan|Index Scan|Index Only Scan|Bitmap Heap Scan)(?: using \S+)? on consents\b.*actual time=[\d.]+\.\.[\d.]+ rows=([\d.]+) loops=(\d+)`)
)

// scaleConsentFacts are the two independent counts in the plan of the consent
// scrub. The top-level 'Update on consents' node reports actual rows=0 because
// the statement has no RETURNING; that is expected and is not read here.
type scaleConsentFacts struct {
	scanNode     string
	scanRows     int64 // actual rows x loops at the feeding scan
	scanFound    bool
	triggerCalls int64
	triggerFound bool
	usesLatest   bool // idx_consents_latest appears in the plan
}

func scaleConsentPlanFacts(lines []string) scaleConsentFacts {
	var f scaleConsentFacts
	update := -1
	for i, line := range lines {
		if strings.Contains(line, "idx_consents_latest") {
			f.usesLatest = true
		}
		if update < 0 && strings.Contains(line, "Update on consents") {
			update = i
		}
		if m := scaleTriggerRe.FindStringSubmatch(line); m != nil && !f.triggerFound {
			f.triggerCalls, _ = strconv.ParseInt(m[1], 10, 64)
			f.triggerFound = true
		}
		if update < 0 || i <= update || f.scanFound {
			continue
		}
		if m := scaleConsentScanRe.FindStringSubmatch(line); m != nil {
			rows, _ := strconv.ParseFloat(m[1], 64)
			loops, _ := strconv.ParseFloat(m[2], 64)
			f.scanRows = int64(math.Round(rows * loops))
			f.scanNode = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "->"))
			if cut := strings.Index(f.scanNode, "  (cost="); cut >= 0 {
				f.scanNode = f.scanNode[:cut]
			}
			f.scanFound = true
		}
	}
	return f
}

// checkScaleConsentScrub asserts the three counts that tell a real scrub from
// an empty one, each on its own so one passing never stands in for another:
// (c1) the real execution's RowsAffected, with the feeding scan's actual rows
// x loops as its cross-check, and (c2) the trigger's calls. The unrelated-
// person control (c3) is asserted by the timed deletes.
func checkScaleConsentScrub(t *testing.T, run scaleExplain) scaleConsentFacts {
	t.Helper()
	var plan *scalePlan
	for i := range run.plans {
		if run.plans[i].label == scaleConsentLabel {
			plan = &run.plans[i]
		}
	}
	if plan == nil {
		t.Fatalf("no plan for %q: the statement did not run or its SQL no longer matches", scaleConsentLabel)
	}
	facts := scaleConsentPlanFacts(plan.lines)
	if got := run.affected[scaleConsentLabel]; len(got) != 1 || got[0] != 4 {
		t.Errorf("(c1) the real UPDATE's RowsAffected: %v, want exactly one execution touching 4 rows", got)
	}
	if !facts.scanFound || facts.scanRows != 4 {
		t.Errorf("(c1 cross-check) the scan feeding 'Update on consents' reports %d actual rows x loops (found %v), want 4:\n%s",
			facts.scanRows, facts.scanFound, strings.Join(plan.lines, "\n"))
	}
	if !facts.triggerFound || facts.triggerCalls != 4 {
		t.Errorf("(c2) 'Trigger consents_append_only' reports calls=%d (found %v), want calls=4:\n%s",
			facts.triggerCalls, facts.triggerFound, strings.Join(plan.lines, "\n"))
	}
	t.Logf("consent scrub: RowsAffected %v, feeding scan %q rows x loops = %d, trigger calls %d, idx_consents_latest used: %v",
		run.affected[scaleConsentLabel], facts.scanNode, facts.scanRows, facts.triggerCalls, facts.usesLatest)
	return facts
}

// logScaleReport prints the table, the totals the review cares about and,
// below them, every full plan with its buffer counts. It returns the
// recipient statement's time.
func logScaleReport(t *testing.T, plans []scalePlan) float64 {
	t.Helper()
	counts := map[string]int{}
	seen := map[string]bool{}
	for _, p := range plans {
		counts[p.label]++
		seen[p.label] = true
	}
	for _, s := range scaleStatements {
		if !seen[s.label] {
			t.Errorf("no plan for %q: the statement did not run or its SQL no longer matches", s.label)
		}
	}
	t.Logf("%-40s %10s  %-34s %-44s %s", "statement", "time (ms)", "rows at scan node", "plan node", "sequential scans")
	var recipientMS, snapshotMS float64
	snapshotQueries := 0
	index := map[string]int{}
	for _, p := range plans {
		r := summarizeScalePlan(p)
		label := r.label
		if counts[label] > 1 {
			index[label]++
			label = fmt.Sprintf("%s #%d", label, index[label])
		}
		t.Logf("%-40s %10.3f  %-34s %-44s %s", label, r.ms, r.rows, r.node, r.seq)
		switch p.label {
		case scaleRecipientLabel:
			recipientMS = r.ms
		case "snapshot lock: users FOR KEY SHARE":
			snapshotMS += r.ms
			snapshotQueries++
		}
	}
	bound := float64(erasureRecipientWait.Milliseconds())
	t.Logf("recipient statement: %.3f ms against the %.0f ms (%s) statement_timeout, %.4f%% of the bound",
		recipientMS, bound, erasureRecipientWait, recipientMS/bound*100)
	t.Logf("snapshot lock: %d queries of up to 500 users, %.3f ms in all", snapshotQueries, snapshotMS)
	for _, p := range plans {
		t.Logf("--- %s\n%s", p.label, strings.Join(p.lines, "\n"))
	}
	return recipientMS
}

// TestAccountErasureScale measures the erasure at the size of a large church
// (see the fixture comment above). It skips unless FOLD_ERASURE_SCALE=1, needs
// a scratch DATABASE_URL (it creates and drops four databases) and asserts
// nothing about time: it checks only that the fixture has the planned shape and
// that the erasure answered 200 and left no copy of the address in the
// tables it names.
//
//	FOLD_ERASURE_SCALE=1 DATABASE_URL=<scratch> go test -run TestAccountErasureScale -count=1 -v .
//
// R4-2d1b-2: the consent scrub is asserted, not only timed. The explain
// subtest requires the real UPDATE's RowsAffected = 4 (cross-checked by the
// feeding scan's actual rows x loops) and the trigger's calls = 4, each on its
// own; the three timed deletes require the unrelated-person control. Set
// FOLD_ERASURE_SCALE_JSON=<path> to write the plan, the counts and the medians
// as JSON (results/r4-2d1b-scale.json).
//
// R4-T07A-4: the explain subtest also measures the ages-note lock selection and
// UPDATE (RowsAffected = 4, five binds, four add_request_identity calls, the
// helper-only scope) and classifies both plans with scaleAgesNoteScanVerdict;
// bounded = false is logged and recorded, never a failure. Each of the three
// timed deletes reads the contact_requests controls before and after: (u)
// requests naming neither U as subject nor requester stay whole, pending ones
// included; (r) U's requester-only rows keep their id, note and reason, and
// their lifecycle is the pre-T07 result; (s) U's subject rows lose only the
// note. Reason is unchanged on every row.
//
// Three fresh seeds each take the real DELETE /admin/users/:id once; the
// median is reported. A fourth fresh seed gets the EXPLAIN (ANALYZE, BUFFERS)
// run in a rolled-back transaction, so the plans' dead tuples never touch a
// timed run.
func TestAccountErasureScale(t *testing.T) {
	if os.Getenv("FOLD_ERASURE_SCALE") != "1" {
		t.Skip("set FOLD_ERASURE_SCALE=1 to run the representative-scale erasure measurement (opt-in, not run in CI)")
	}
	base := testApp(t)
	var timings []time.Duration
	for i := 1; i <= 3; i++ {
		t.Run(fmt.Sprintf("delete seed %d", i), func(t *testing.T) {
			app, ac := newScaleScene(t, base)
			// The unrelated-person control (c3), recorded right after seeding.
			othersBefore, targetRestBefore := scaleConsentControl(t, app.DB)
			// R4-T07A-4: the contact_requests controls, read right before the same DELETE.
			requestsBefore := readScaleContactState(t, app.DB)
			started := time.Now()
			r := deleteAccount(ac, scaleTargetID, nil)
			elapsed := time.Since(started)
			if r.code != http.StatusOK {
				t.Fatalf("delete: %d %s", r.code, r.body)
			}
			othersAfter, targetRestAfter := scaleConsentControl(t, app.DB)
			checkScaleContactErasure(t, requestsBefore, readScaleContactState(t, app.DB))
			if othersAfter != othersBefore {
				t.Errorf("(c3) another person's consent rows changed: md5 %s, was %s", othersAfter, othersBefore)
			}
			if n := countRows(t, app.DB, "consents", "user_id <> ? AND ip <> '' AND user_agent <> ''", scaleTargetID); n != 19996 {
				t.Errorf("(c3) %d of everyone else's consent rows still carry an ip and user agent, want 19996", n)
			}
			if n := countRows(t, app.DB, "consents", "user_id = ? AND ip = '' AND user_agent = '' AND evidence_text = ''", scaleTargetID); n != 4 {
				t.Errorf("(c3) %d of the erased person's 4 consent rows are blank, want all 4", n)
			}
			if n := countRows(t, app.DB, "consents", "user_id = ?", scaleTargetID); n != 4 {
				t.Errorf("(c3) the erased person has %d consent rows, want 4", n)
			}
			if targetRestAfter != targetRestBefore {
				t.Errorf("(c3) the scrub changed more than the three evidence columns: md5 %s, was %s", targetRestAfter, targetRestBefore)
			}
			var left int64
			if err := app.DB.Raw(`SELECT
				(SELECT count(*) FROM message_recipients WHERE lower(address) = ?)
				+ (SELECT count(*) FROM account_requests WHERE lower(email) = ? OR lower(guardian_email) = ?)
				+ (SELECT count(*) FROM invitations WHERE lower(email) = ?)
				+ (SELECT count(*) FROM guest_signups WHERE lower(email) = ?)
				+ (SELECT count(*) FROM mail_outboxes WHERE lower(btrim(to_address)) = ?)`,
				scaleAddress, scaleAddress, scaleAddress, scaleAddress, scaleAddress, scaleAddress).Scan(&left).Error; err != nil {
				t.Fatal(err)
			}
			if left != 0 {
				t.Fatalf("%d rows still hold the erased address", left)
			}
			if n := countRows(t, app.DB, "users", "id = ? AND deleted_at IS NOT NULL", scaleTargetID); n != 1 {
				t.Fatalf("erased user is not soft-deleted (%d rows)", n)
			}
			timings = append(timings, elapsed)
			t.Logf("real DELETE on seed %d: %s", i, elapsed.Round(time.Millisecond))
		})
	}
	var recipientMS float64
	var run scaleExplain
	var facts scaleConsentFacts
	var ages scaleAgesNoteEvidence
	t.Run("explain", func(t *testing.T) {
		app, _ := newScaleScene(t, base)
		run = explainScaleErasure(t, app)
		recipientMS = logScaleReport(t, run.plans)
		facts = checkScaleConsentScrub(t, run)
		ages = checkScaleAgesNoteScrub(t, run)
	})
	if len(timings) != 3 {
		t.Fatalf("only %d of 3 timed deletes completed", len(timings))
	}
	sorted := append([]time.Duration(nil), timings...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	t.Logf("real DELETE of U at scale: median %s of %v (fastest %s, slowest %s)",
		sorted[1].Round(time.Millisecond), timings, sorted[0].Round(time.Millisecond), sorted[2].Round(time.Millisecond))
	t.Logf("recipient statement %.3f ms; whole DELETE median %s; bound %s", recipientMS, sorted[1].Round(time.Millisecond), erasureRecipientWait)
	writeScaleResults(t, run, facts, ages, timings, sorted[1])
}

// scaleConsentControl is the unrelated-person control: one hash over every
// other person's consent rows, whole, and one over the erased person's rows
// without the three evidence columns, so a scrub that touched anything else
// changes the second and a scrub that reached another person changes the first.
func scaleConsentControl(t *testing.T, db *gorm.DB) (others, targetRest string) {
	t.Helper()
	if err := db.Raw(`SELECT coalesce(md5(string_agg(to_jsonb(c)::text, ',' ORDER BY c.id)), '') FROM consents c WHERE c.user_id <> ?`,
		scaleTargetID).Scan(&others).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT coalesce(md5(string_agg((to_jsonb(c) - 'ip' - 'user_agent' - 'evidence_text')::text, ',' ORDER BY c.id)), '')
		FROM consents c WHERE c.user_id = ?`, scaleTargetID).Scan(&targetRest).Error; err != nil {
		t.Fatal(err)
	}
	if others == "" || targetRest == "" {
		t.Fatal("the consent control hashed no rows")
	}
	return others, targetRest
}

// writeScaleResults records the consent scrub's full plan, the recorded
// RowsAffected, the feeding scan's rows and loops, the trigger's calls and the
// timed medians for the database review, as JSON at FOLD_ERASURE_SCALE_JSON
// (the results directory's r4-2d1b-scale.json). It writes nothing when the
// variable is unset.
func writeScaleResults(t *testing.T, run scaleExplain, facts scaleConsentFacts, ages scaleAgesNoteEvidence, timings []time.Duration, median time.Duration) {
	t.Helper()
	path := os.Getenv("FOLD_ERASURE_SCALE_JSON")
	if path == "" {
		return
	}
	var lines []string
	for _, p := range run.plans {
		if p.label == scaleConsentLabel {
			lines = p.lines
		}
	}
	millis := make([]float64, 0, len(timings))
	for _, d := range timings {
		millis = append(millis, float64(d.Microseconds())/1000)
	}
	out, err := json.MarshalIndent(map[string]any{
		"statement":               scaleConsentLabel,
		"plan":                    lines,
		"rowsAffected":            run.affected[scaleConsentLabel],
		"feedingScan":             map[string]any{"node": facts.scanNode, "actualRowsTimesLoops": facts.scanRows},
		"triggerCalls":            facts.triggerCalls,
		"usesIdxConsentsLatest":   facts.usesLatest,
		"timedDeleteMillis":       millis,
		"timedDeleteMedianMillis": float64(median.Microseconds()) / 1000,
		// R4-T07A-4: the ages-note statements' plans, counts, verdicts and the
		// helper-only oracle (no secrets, addresses, capabilities or note text).
		"agesNote": ages,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	t.Logf("wrote %s", path)
}

// ---------------------------------------------------------------------------
// R4-T07A-4: statement plans and whole-erasure controls for the ages-note scrub
// ---------------------------------------------------------------------------

// What the scale run can and cannot show about eraseContactRequestTextIn.
//
// explainScaleErasure measures the helper's two statements with EXPLAIN
// (ANALYZE, BUFFERS) on a 10,000-row contact_requests table, in a transaction
// it rolls back, after that transaction's own soft delete (before it the UPDATE
// would raise 23514 and no plan would be recorded). The harness is not the
// production statement order: it runs the capture, account-request, notification
// and dependent-record statements and the snapshot-lock probe BEFORE the soft
// delete, and it leaves out the contact gate, deletionHostPlan, the hand-offs,
// promoteOnDisable, the final throttle delete and contactMutation's
// reconcileContacts. So it proves the plans on representative data and the
// helper's scope; it does not prove production statement order or lock order.
// Whole-erasure evidence is the three real DELETE runs (their controls below)
// and the concurrency tests.
//
// Boundedness is claimed only where scaleAgesNoteScanVerdict says bounded for
// both statements. The scale run never fails on bounded = false: the verdict and
// the raw plans go to the evidence file, and a false verdict is an open
// database-review finding (RC-4), not a reason to change a query here.

// The id blocks of the fixture's contact_requests rows for U (see the seeds).
var (
	scaleCRSubjectTerminal   = []uint{9993, 9994}
	scaleCRSubjectPending    = []uint{9995, 9996}
	scaleCRRequesterTerminal = []uint{9997, 9998}
	scaleCRRequesterPending  = []uint{9999, 10000}
)

// scaleMD5 is one md5 over expr of the contact_requests rows (alias c) matching
// where, in id order; ” for no rows.
func scaleMD5(db *gorm.DB, expr, where string, args ...any) (string, error) {
	var out string
	err := db.Raw(fmt.Sprintf("SELECT coalesce(md5(string_agg(%s, ',' ORDER BY c.id)), '') FROM contact_requests c WHERE %s", expr, where), args...).Scan(&out).Error
	return out, err
}

// scaleAgesNoteFacts are the fixture facts recorded beside the plans, read
// before the erasure: the table's size, S_U (every contact_requests row with
// subject U, any purpose and status), the unrelated rows and the ids statement
// 1 must select.
type scaleAgesNoteFacts struct {
	Total            int64  `json:"contactRequests"`
	SU               int64  `json:"subjectRowsOfU"`
	Unrelated        int64  `json:"unrelated"`
	UnrelatedPending int64  `json:"unrelatedPending"`
	BatchIDs         []uint `json:"batchIds"`
}

func readScaleAgesNoteFacts(t *testing.T, db *gorm.DB) scaleAgesNoteFacts {
	t.Helper()
	var f scaleAgesNoteFacts
	count := func(dst *int64, query string, args ...any) {
		if err := db.Raw(query, args...).Scan(dst).Error; err != nil {
			t.Fatal(err)
		}
	}
	count(&f.Total, "SELECT count(*) FROM contact_requests")
	count(&f.SU, "SELECT count(*) FROM contact_requests WHERE subject_user_id = ?", scaleTargetID)
	count(&f.Unrelated, "SELECT count(*) FROM contact_requests WHERE subject_user_id <> ? AND requester_id <> ?", scaleTargetID, scaleTargetID)
	count(&f.UnrelatedPending, "SELECT count(*) FROM contact_requests WHERE subject_user_id <> ? AND requester_id <> ? AND status = 'pending'", scaleTargetID, scaleTargetID)
	if err := db.Raw("SELECT id FROM contact_requests WHERE purpose = ? AND subject_user_id = ? AND add_childcare_ages_note <> '' ORDER BY id",
		policy.RequestOccurrenceAdd, scaleTargetID).Scan(&f.BatchIDs).Error; err != nil {
		t.Fatal(err)
	}
	return f
}

// scaleHelperScope is what the helper-only oracle hashes inside the rolled-back
// transaction: every row the lock selection did not pick, the picked rows
// without their note, every id and reason, and how many picked rows still hold
// a note. Hashes only; no row text is kept.
type scaleHelperScope struct {
	Others, SelectedRest, Reasons string
	SelectedRows, SelectedNotes   int64
}

func scaleHelperScopeState(db *gorm.DB, selected []uint) (scaleHelperScope, error) {
	var s scaleHelperScope
	var err error
	if s.Others, err = scaleMD5(db, "to_jsonb(c)::text", "c.id NOT IN ?", selected); err != nil {
		return s, err
	}
	if s.SelectedRest, err = scaleMD5(db, "(to_jsonb(c) - 'add_childcare_ages_note')::text", "c.id IN ?", selected); err != nil {
		return s, err
	}
	if s.Reasons, err = scaleMD5(db, "c.id::text || ':' || c.reason", "true"); err != nil {
		return s, err
	}
	err = db.Raw("SELECT count(*), count(*) FILTER (WHERE add_childcare_ages_note <> '') FROM contact_requests WHERE id IN ?", selected).
		Row().Scan(&s.SelectedRows, &s.SelectedNotes)
	return s, err
}

// scaleHelperOnlyResult is the "helper-only scope" oracle: inside
// explainScaleErasure, the helper changed the picked rows' note and nothing
// else, and no other row at all.
type scaleHelperOnlyResult struct {
	SelectedRows          int64 `json:"selectedRows"`
	SelectedNotesBefore   int64 `json:"selectedNotesBefore"`
	SelectedNotesAfter    int64 `json:"selectedNotesAfter"`
	OthersUnchanged       bool  `json:"otherRowsUnchanged"`
	SelectedRestUnchanged bool  `json:"selectedRowsUnchangedExceptNote"`
	ReasonsUnchanged      bool  `json:"reasonsUnchanged"`
}

func scaleHelperOnlyCompare(before, after scaleHelperScope) scaleHelperOnlyResult {
	return scaleHelperOnlyResult{
		SelectedRows:          after.SelectedRows,
		SelectedNotesBefore:   before.SelectedNotes,
		SelectedNotesAfter:    after.SelectedNotes,
		OthersUnchanged:       before.Others != "" && before.Others == after.Others,
		SelectedRestUnchanged: before.SelectedRest != "" && before.SelectedRest == after.SelectedRest && before.SelectedRows == after.SelectedRows,
		ReasonsUnchanged:      before.Reasons != "" && before.Reasons == after.Reasons,
	}
}

// ---- real-erasure controls (the three timed DELETE runs) -------------------

// scaleLifecycle is the lifecycle of one request after the erasure.
type scaleLifecycle struct {
	Status    string
	EndReason string
	Done      bool
}

// scaleContactState is the control read before and after a real erasure of U:
// hashes of the classes the erasure must leave alone, counts, and the
// lifecycle of U's pending rows.
type scaleContactState struct {
	Total, SubjectRows, SubjectNotes, SubjectBlank int64
	UnrelatedPendingRows                           int64
	Reasons                                        string // (id, reason) of every row
	Unrelated, UnrelatedPending                    string // (u) whole rows
	RequesterNoteReason                            string // (r) (id, note, reason)
	RequesterTerminal, RequesterPendingKept        string // (r) whole / all but lifecycle
	SubjectTerminalRest, SubjectPendingKept        string // (s) all but the note / all but lifecycle and note
	Lifecycle                                      map[uint]scaleLifecycle
}

func readScaleContactState(t *testing.T, db *gorm.DB) scaleContactState {
	t.Helper()
	var s scaleContactState
	sum := func(expr, where string, args ...any) string {
		out, err := scaleMD5(db, expr, where, args...)
		if err != nil {
			t.Fatal(err)
		}
		if out == "" {
			t.Fatalf("the contact_requests control hashed no rows (%s)", where)
		}
		return out
	}
	count := func(dst *int64, query string, args ...any) {
		if err := db.Raw(query, args...).Scan(dst).Error; err != nil {
			t.Fatal(err)
		}
	}
	const lifecycle = "'status' - 'end_reason' - 'completed_at'"
	count(&s.Total, "SELECT count(*) FROM contact_requests")
	count(&s.SubjectRows, "SELECT count(*) FROM contact_requests WHERE subject_user_id = ?", scaleTargetID)
	count(&s.SubjectNotes, "SELECT count(*) FROM contact_requests WHERE subject_user_id = ? AND purpose = 'occurrence_add' AND add_childcare_ages_note <> ''", scaleTargetID)
	count(&s.SubjectBlank, "SELECT count(*) FROM contact_requests WHERE subject_user_id = ? AND purpose = 'occurrence_add' AND add_childcare_ages_note = ''", scaleTargetID)
	count(&s.UnrelatedPendingRows, "SELECT count(*) FROM contact_requests WHERE subject_user_id <> ? AND requester_id <> ? AND status = 'pending'", scaleTargetID, scaleTargetID)
	s.Reasons = sum("c.id::text || ':' || c.reason", "true")
	s.Unrelated = sum("to_jsonb(c)::text", "c.subject_user_id <> ? AND c.requester_id <> ?", scaleTargetID, scaleTargetID)
	s.UnrelatedPending = sum("to_jsonb(c)::text", "c.subject_user_id <> ? AND c.requester_id <> ? AND c.status = 'pending'", scaleTargetID, scaleTargetID)
	s.RequesterNoteReason = sum("c.id::text || ':' || c.add_childcare_ages_note || ':' || c.reason", "c.requester_id = ? AND c.subject_user_id <> ?", scaleTargetID, scaleTargetID)
	s.RequesterTerminal = sum("to_jsonb(c)::text", "c.id IN ?", scaleCRRequesterTerminal)
	s.RequesterPendingKept = sum("(to_jsonb(c) - "+lifecycle+")::text", "c.id IN ?", scaleCRRequesterPending)
	s.SubjectTerminalRest = sum("(to_jsonb(c) - 'add_childcare_ages_note')::text", "c.id IN ?", scaleCRSubjectTerminal)
	s.SubjectPendingKept = sum("(to_jsonb(c) - "+lifecycle+" - 'add_childcare_ages_note')::text", "c.id IN ?", scaleCRSubjectPending)
	pending := append(append([]uint(nil), scaleCRSubjectPending...), scaleCRRequesterPending...)
	var rows []struct {
		ID        uint
		Status    string
		EndReason string
		Done      bool
	}
	if err := db.Raw("SELECT id, status, end_reason, completed_at IS NOT NULL AS done FROM contact_requests WHERE id IN ? ORDER BY id", pending).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	s.Lifecycle = map[uint]scaleLifecycle{}
	for _, r := range rows {
		s.Lifecycle[r.ID] = scaleLifecycle{Status: r.Status, EndReason: r.EndReason, Done: r.Done}
	}
	return s
}

// checkScaleContactErasure judges one real erasure of U against the state read
// just before it. The classes: (u) requests that name neither U as subject nor
// as requester, (r) U's requester-only rows, (s) U's subject rows. The fixed
// pre-T07 lifecycle result is what reconcileContacts does today, with the
// ages-note scrub not involved: U's account is disabled, so AddIneligibility
// answers account_disabled before it reads anything else, and a pending row of
// U's, as subject or as requester, is ended with that reason (nothing here
// expires: every pending row's expires_at is in the future). Terminal rows are
// not candidates for reconcileContacts and stay whole. The reason column is
// R4-T07B's and stays on every row of every class.
func checkScaleContactErasure(t *testing.T, before, after scaleContactState) {
	t.Helper()
	if before.Total != 10000 || after.Total != before.Total || after.SubjectRows != before.SubjectRows {
		t.Errorf("contact_requests rows: %d before and %d after, U's subject rows %d before and %d after; the erasure keeps every row",
			before.Total, after.Total, before.SubjectRows, after.SubjectRows)
	}
	if after.Reasons != before.Reasons {
		t.Errorf("a request's reason changed (every class): md5 %s, was %s", after.Reasons, before.Reasons)
	}
	// (u) the control set holds pending rows, checked on their own so a hash over
	// terminal rows alone cannot pass.
	if before.UnrelatedPendingRows < 1 || after.UnrelatedPendingRows != before.UnrelatedPendingRows {
		t.Errorf("(u) unrelated pending rows: %d before and %d after, want at least one and the same", before.UnrelatedPendingRows, after.UnrelatedPendingRows)
	}
	if after.Unrelated != before.Unrelated {
		t.Errorf("(u) an unrelated request changed: md5 %s, was %s", after.Unrelated, before.Unrelated)
	}
	if after.UnrelatedPending != before.UnrelatedPending {
		t.Errorf("(u) an unrelated pending request changed: md5 %s, was %s", after.UnrelatedPending, before.UnrelatedPending)
	}
	// (r)
	if after.RequesterNoteReason != before.RequesterNoteReason {
		t.Errorf("(r) a requester-only row's id, note or reason changed: md5 %s, was %s", after.RequesterNoteReason, before.RequesterNoteReason)
	}
	if after.RequesterTerminal != before.RequesterTerminal {
		t.Errorf("(r) a terminal requester-only row changed: md5 %s, was %s", after.RequesterTerminal, before.RequesterTerminal)
	}
	if after.RequesterPendingKept != before.RequesterPendingKept {
		t.Errorf("(r) a pending requester-only row changed beyond its lifecycle: md5 %s, was %s", after.RequesterPendingKept, before.RequesterPendingKept)
	}
	// (s)
	if before.SubjectNotes != 4 || after.SubjectNotes != 0 || after.SubjectBlank != 4 {
		t.Errorf("(s) U's subject occurrence_add notes: %d before and %d after (%d blank), want 4, 0 and 4", before.SubjectNotes, after.SubjectNotes, after.SubjectBlank)
	}
	if after.SubjectTerminalRest != before.SubjectTerminalRest {
		t.Errorf("(s) a terminal subject row changed beyond its note: md5 %s, was %s", after.SubjectTerminalRest, before.SubjectTerminalRest)
	}
	if after.SubjectPendingKept != before.SubjectPendingKept {
		t.Errorf("(s) a pending subject row changed beyond its lifecycle and note: md5 %s, was %s", after.SubjectPendingKept, before.SubjectPendingKept)
	}
	for _, id := range append(append([]uint(nil), scaleCRSubjectPending...), scaleCRRequesterPending...) {
		if b := before.Lifecycle[id]; b.Status != "pending" || b.Done {
			t.Errorf("request %d was %+v before the erasure, want pending", id, b)
		}
		if a := after.Lifecycle[id]; a.Status != "ended" || a.EndReason != "account_disabled" || !a.Done {
			t.Errorf("pending request %d after the erasure: status %q, end_reason %q, completed %v; the pre-T07 result is ended / account_disabled / completed",
				id, a.Status, a.EndReason, a.Done)
		}
	}
}

// ---- plan parsing and the boundedness classifier --------------------------

// scalePlanNode is one node of EXPLAIN (ANALYZE, BUFFERS) text.
type scalePlanNode struct {
	col       int
	label     string
	typ       string // "" when the classifier does not know the node
	relation  string
	index     string
	hasActual bool
	never     bool
	rows      float64
	loops     int64
	attrs     map[string]string // first value of each "Name: value" line under the node
	parent    int
	children  []int
}

var (
	scaleNodeRe  = regexp.MustCompile(`^(\s*)(->\s+)?(\S.*?)\s+\(cost=[^)]*\)(?:\s+\(actual time=[\d.]+\.\.[\d.]+ rows=([\d.]+) loops=(\d+)\)|\s+\((never executed)\))?\s*$`)
	scaleAttrRe  = regexp.MustCompile(`^\s+([A-Z][A-Za-z ]*): (.*)$`)
	scaleUsingRe = regexp.MustCompile(`^ using (\S+) on (\S+)`)
	scaleOnRe    = regexp.MustCompile(`^ on (\S+)`)

	// Longest first: "Index Scan" is a prefix of "Index Scan Backward".
	scaleNodeTypes = []string{"Index Only Scan Backward", "Index Only Scan", "Index Scan Backward", "Index Scan",
		"Bitmap Heap Scan", "Bitmap Index Scan", "BitmapAnd", "BitmapOr", "Seq Scan", "LockRows", "Sort", "Update"}
)

func scaleClassifyLabel(n *scalePlanNode) {
	for _, typ := range scaleNodeTypes {
		if n.label != typ && !strings.HasPrefix(n.label, typ+" ") {
			continue
		}
		n.typ = typ
		rest := strings.TrimPrefix(n.label, typ)
		switch typ {
		case "Index Scan", "Index Scan Backward", "Index Only Scan", "Index Only Scan Backward":
			if m := scaleUsingRe.FindStringSubmatch(rest); m != nil {
				n.index, n.relation = m[1], m[2]
			}
		case "Bitmap Index Scan":
			if m := scaleOnRe.FindStringSubmatch(rest); m != nil {
				n.index = m[1]
			}
		case "Seq Scan", "Bitmap Heap Scan", "Update":
			if m := scaleOnRe.FindStringSubmatch(rest); m != nil {
				n.relation = m[1]
			}
		}
		return
	}
}

// scaleParsePlan reads plan text into nodes (parents by indentation of the
// "->" arrow). It refuses a node line it cannot read, a second root and a plan
// that does not start at a root node. Attribute lines attach to the node above
// them until the footer (Planning, Trigger, Execution Time) begins.
func scaleParsePlan(lines []string) ([]*scalePlanNode, error) {
	var nodes []*scalePlanNode
	var stack []int
	cur, footer := -1, false
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		trimmed := strings.TrimLeft(line, " ")
		isNode := strings.Contains(line, "(cost=") || strings.HasPrefix(trimmed, "->")
		if !isNode && i > 0 && len(line) == len(trimmed) {
			footer, cur = true, -1
			continue
		}
		if isNode {
			m := scaleNodeRe.FindStringSubmatch(line)
			if m == nil || footer {
				return nil, fmt.Errorf("line %d is not a plan node the classifier can read", i+1)
			}
			col := len(m[1])
			if len(nodes) == 0 && (col != 0 || m[2] != "") {
				return nil, fmt.Errorf("the plan does not start at a root node")
			}
			for len(stack) > 0 && nodes[stack[len(stack)-1]].col >= col {
				stack = stack[:len(stack)-1]
			}
			n := &scalePlanNode{col: col, label: strings.TrimSpace(m[3]), attrs: map[string]string{}, parent: -1}
			if len(stack) > 0 {
				n.parent = stack[len(stack)-1]
			} else if len(nodes) > 0 {
				return nil, fmt.Errorf("line %d starts a second root", i+1)
			}
			switch {
			case m[6] != "":
				n.never = true
			case m[4] != "":
				rows, err := strconv.ParseFloat(m[4], 64)
				loops, err2 := strconv.ParseInt(m[5], 10, 64)
				if err != nil || err2 != nil {
					return nil, fmt.Errorf("line %d has unreadable actual counters", i+1)
				}
				n.hasActual, n.rows, n.loops = true, rows, loops
			}
			scaleClassifyLabel(n)
			nodes = append(nodes, n)
			idx := len(nodes) - 1
			if n.parent >= 0 {
				nodes[n.parent].children = append(nodes[n.parent].children, idx)
			}
			stack = append(stack, idx)
			cur = idx
			continue
		}
		if cur < 0 {
			continue
		}
		if m := scaleAttrRe.FindStringSubmatch(line); m != nil {
			if _, seen := nodes[cur].attrs[m[1]]; !seen {
				nodes[cur].attrs[m[1]] = strings.TrimSpace(m[2])
			}
		}
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("the plan has no nodes")
	}
	return nodes, nil
}

// scaleTriggerCalls is the calls= of the named trigger's line in the plan footer.
func scaleTriggerCalls(lines []string, trigger string) (int64, bool) {
	re := regexp.MustCompile(`^Trigger ` + regexp.QuoteMeta(trigger) + `: time=[\d.]+ calls=(\d+)`)
	for _, line := range lines {
		if m := re.FindStringSubmatch(line); m != nil {
			n, err := strconv.ParseInt(m[1], 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}

// scaleStripParens removes parentheses that wrap the whole text.
func scaleStripParens(s string) string {
	for {
		s = strings.TrimSpace(s)
		if len(s) < 2 || s[0] != '(' || s[len(s)-1] != ')' {
			return s
		}
		depth, wraps, quoted := 0, false, false
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c == '\'' {
				quoted = !quoted
			}
			if quoted || c == '\'' {
				continue
			}
			if c == '(' {
				depth++
			} else if c == ')' {
				depth--
				if depth == 0 {
					wraps = i == len(s)-1
					break
				}
			}
		}
		if !wraps {
			return s
		}
		s = s[1 : len(s)-1]
	}
}

// scaleSplitAt splits at the top-level separator, outside quotes and brackets.
func scaleSplitAt(s, sep string) []string {
	var parts []string
	depth, start, quoted := 0, 0, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\'' {
			quoted = !quoted
			continue
		}
		if quoted {
			continue
		}
		if c == '(' || c == '[' {
			depth++
		} else if c == ')' || c == ']' {
			depth--
		} else if depth == 0 && strings.HasPrefix(s[i:], sep) {
			parts = append(parts, s[start:i])
			start = i + len(sep)
			i = start - 1
		}
	}
	return append(parts, s[start:])
}

// scaleSplitAnd splits at the top-level " AND ".
func scaleSplitAnd(s string) []string { return scaleSplitAt(s, " AND ") }

// scaleSplitOr splits at the top-level " OR ".
func scaleSplitOr(s string) []string { return scaleSplitAt(s, " OR ") }

// scaleVarUint reads a captured bind value as an id.
func scaleVarUint(v any) (uint64, bool) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Uint(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if rv.Int() < 0 {
			return 0, false
		}
		return uint64(rv.Int()), true
	}
	return 0, false
}

var (
	scaleValueRe     = regexp.MustCompile(`^(?:'(\d+)'::bigint|(\d+)|\$(\d+))$`)
	scaleArrayLitRe  = regexp.MustCompile(`^'\{([\d,]*)\}'::bigint\[\]$`)
	scaleArrayExprRe = regexp.MustCompile(`^ARRAY\[(.*)\]$`)
)

// scaleDecodeValue reads one displayed value: a bigint literal, a number or a
// $n bind parameter resolved against the captured Vars of that execution.
func scaleDecodeValue(tok string, vars []any) (uint64, bool) {
	m := scaleValueRe.FindStringSubmatch(scaleStripParens(tok))
	if m == nil {
		return 0, false
	}
	switch {
	case m[1] != "":
		n, err := strconv.ParseUint(m[1], 10, 64)
		return n, err == nil
	case m[2] != "":
		n, err := strconv.ParseUint(m[2], 10, 64)
		return n, err == nil
	}
	i, err := strconv.Atoi(m[3])
	if err != nil || i < 1 || i > len(vars) {
		return 0, false
	}
	return scaleVarUint(vars[i-1])
}

// scaleDecodeArray reads the argument of "id = ANY (...)": a '{...}'::bigint[]
// literal or ARRAY[...] of values.
func scaleDecodeArray(s string, vars []any) ([]uint64, bool) {
	var items []string
	if m := scaleArrayLitRe.FindStringSubmatch(s); m != nil {
		if m[1] != "" {
			items = strings.Split(m[1], ",")
		}
	} else if m := scaleArrayExprRe.FindStringSubmatch(s); m != nil {
		items = strings.Split(m[1], ",")
	} else {
		return nil, false
	}
	out := make([]uint64, 0, len(items))
	for _, it := range items {
		v, ok := scaleDecodeValue(it, vars)
		if !ok {
			return nil, false
		}
		out = append(out, v)
	}
	return out, true
}

// scaleSameIDs compares decoded ids with the batch as sorted lists: the same
// set and the same count, not the same length alone.
func scaleSameIDs(got []uint64, batch []uint) bool {
	if len(got) != len(batch) {
		return false
	}
	a := append([]uint64(nil), got...)
	b := make([]uint64, len(batch))
	for i, id := range batch {
		b[i] = uint64(id)
	}
	sort.Slice(a, func(i, j int) bool { return a[i] < a[j] })
	sort.Slice(b, func(i, j int) bool { return b[i] < b[j] })
	return reflect.DeepEqual(a, b)
}

// scaleRestriction is what a displayed condition restricts, decoded. A problem
// that is only a mismatch (two subjects, two id restrictions) is a problem; one
// the classifier could not resolve at all also sets unresolved, which withholds
// the exact totals.
type scaleRestriction struct {
	hasSubject bool
	subject    uint64
	hasIDs     bool
	ids        []uint64
	problems   []string
	unresolved bool
}

// scaleIDTerm reads one "id = ANY (...)" or "id = value" term. isID is false for
// any other text; ok is false when it is an id term the classifier cannot decode.
func scaleIDTerm(term string, vars []any) (ids []uint64, isID, ok bool) {
	switch {
	case strings.HasPrefix(term, "id = ANY "):
		ids, ok = scaleDecodeArray(scaleStripParens(strings.TrimPrefix(term, "id = ANY ")), vars)
		return ids, true, ok
	case strings.HasPrefix(term, "id = "):
		v, ok := scaleDecodeValue(strings.TrimPrefix(term, "id = "), vars)
		return []uint64{v}, true, ok
	}
	return nil, false, true
}

// scaleParseRestriction reads the subject_user_id and id terms of a condition:
// top-level conjuncts, and an OR group made only of id terms (a BitmapOr's
// Recheck Cond) as one id set. Other conjuncts are ignored: they can only narrow
// the scan, and the classifier credits only what it understands. An OR group
// that mixes id or subject terms with other text, or a displayed value it cannot
// resolve, is unresolved.
var scaleRelevantColumnRe = regexp.MustCompile(`\b(?:id|subject_user_id)\b`)

func scaleParseRestriction(cond string, vars []any) scaleRestriction {
	var r scaleRestriction
	if strings.TrimSpace(cond) == "" {
		return r
	}
	unresolved := func(msg string) {
		r.problems = append(r.problems, msg)
		r.unresolved = true
	}
	addIDs := func(ids []uint64) {
		if r.hasIDs {
			r.problems = append(r.problems, "the condition restricts ids twice")
			return
		}
		r.hasIDs, r.ids = true, ids
	}
	for _, c := range scaleSplitAnd(scaleStripParens(cond)) {
		c = scaleStripParens(c)
		if parts := scaleSplitOr(c); len(parts) > 1 {
			var union []uint64
			relevant, other, bad := false, false, false
			for _, p := range parts {
				p = scaleStripParens(p)
				if strings.HasPrefix(p, "subject_user_id = ") {
					relevant, bad = true, true
					unresolved("an OR group names a subject, which the classifier does not read")
					continue
				}
				ids, isID, ok := scaleIDTerm(p, vars)
				switch {
				case !isID:
					other = true
					if scaleRelevantColumnRe.MatchString(p) {
						bad = true
						unresolved("an unsupported OR subtree names an id or subject")
					}
				case !ok:
					relevant, bad = true, true
					unresolved("a displayed id in an OR group is not a number or bind parameter the classifier resolves")
				default:
					relevant = true
					union = append(union, ids...)
				}
			}
			if relevant && other && !bad {
				unresolved("an OR group mixes id restrictions with other terms the classifier does not read")
			} else if relevant && !bad {
				addIDs(union)
			}
			continue
		}
		switch {
		case strings.HasPrefix(c, "subject_user_id = "):
			v, ok := scaleDecodeValue(strings.TrimPrefix(c, "subject_user_id = "), vars)
			if !ok {
				unresolved("a displayed subject value is not a number or bind parameter the classifier resolves")
				continue
			}
			if r.hasSubject && r.subject != v {
				r.problems = append(r.problems, "the condition names two subjects")
			}
			r.hasSubject, r.subject = true, v
		default:
			ids, isID, ok := scaleIDTerm(c, vars)
			switch {
			case !isID:
				if scaleRelevantColumnRe.MatchString(c) {
					unresolved("an unsupported condition names an id or subject")
				}
			case !ok:
				unresolved("a displayed id or id array is not one the classifier resolves")
			default:
				addIDs(ids)
			}
		}
	}
	return r
}

// scaleScanInput is one measured execution: its plan text, the statement text
// and bind values EXPLAIN ANALYZE ran, and the facts the caller knows
// independently (U, the batch ids, S_U).
type scaleScanInput struct {
	Statement string // "select" or "update"
	Lines     []string
	SQL       string
	Vars      []any
	Subject   uint
	Batch     []uint
	SU        int64
}

// scaleBindingProblems checks the captured SQL and Vars of the execution
// against the independent facts: the binds are exactly U (select) or exactly
// the batch ids and U (update), in the placeholders the SQL shows. unresolved
// is true when a captured bind could not be read as an id at all, not merely
// when it differs from the independent fact.
func scaleBindingProblems(in scaleScanInput) (out []string, unresolved bool) {
	text := strings.ReplaceAll(strings.Join(strings.Fields(in.SQL), " "), ", ", ",")
	isSubject := func(v any) bool {
		u, ok := scaleVarUint(v)
		return ok && u == uint64(in.Subject)
	}
	switch in.Statement {
	case "select":
		if len(in.Batch) != 0 {
			out = append(out, "a select has no batch ids")
		}
		if len(in.Vars) != 1 || !isSubject(in.Vars[0]) {
			out = append(out, fmt.Sprintf("the captured binds (%d) are not exactly U", len(in.Vars)))
			if len(in.Vars) == 1 {
				if _, ok := scaleVarUint(in.Vars[0]); !ok {
					unresolved = true
				}
			}
		}
		if !strings.Contains(text, "subject_user_id = $1") {
			out = append(out, "the captured SQL does not bind U as $1")
		}
	case "update":
		n := len(in.Batch)
		if n == 0 {
			return append(out, "no batch ids to bind"), true
		}
		if len(in.Vars) != n+1 {
			return append(out, fmt.Sprintf("%d bind values captured, want the %d batch ids and U", len(in.Vars), n)), true
		}
		got := make([]uint64, 0, n)
		placeholders := make([]string, n)
		for i, v := range in.Vars[:n] {
			id, ok := scaleVarUint(v)
			if !ok {
				return append(out, "a captured id bind is not an id"), true
			}
			got = append(got, id)
			placeholders[i] = "$" + strconv.Itoa(i+1)
		}
		if !scaleSameIDs(got, in.Batch) {
			out = append(out, "the captured id binds are not the batch ids")
		}
		if !isSubject(in.Vars[n]) {
			out = append(out, "the captured subject bind is not U")
			if _, ok := scaleVarUint(in.Vars[n]); !ok {
				unresolved = true
			}
		}
		if !strings.Contains(text, "id IN ("+strings.Join(placeholders, ",")+")") {
			out = append(out, "the captured SQL does not bind the batch ids as $1.. in one IN list")
		}
		if !strings.Contains(text, "subject_user_id = $"+strconv.Itoa(n+1)) {
			out = append(out, "the captured SQL does not bind U as the last parameter")
		}
	default:
		out = append(out, "unknown statement kind "+strconv.Quote(in.Statement))
		unresolved = true
	}
	return out, unresolved
}

// scaleScanNodeEvidence is what the report keeps about one node that reads
// contact_requests (role "heap") or feeds a bitmap heap scan ("bitmap-index").
// Plan text holds ids, column names and constants only: no note text, address
// or capability.
type scaleScanNodeEvidence struct {
	Role             string  `json:"role"`
	Node             string  `json:"node"`
	Index            string  `json:"index,omitempty"`
	IndexCond        string  `json:"indexCond,omitempty"`
	RecheckCond      string  `json:"recheckCond,omitempty"`
	Filter           string  `json:"filter,omitempty"`
	ActualRows       float64 `json:"actualRows"`
	Loops            int64   `json:"loops"`
	RemovedByFilter  float64 `json:"rowsRemovedByFilter"`
	RemovedByRecheck float64 `json:"rowsRemovedByIndexRecheck"`
	HeapBlocksExact  int64   `json:"heapBlocksExact"`
	HeapBlocksLossy  int64   `json:"heapBlocksLossy"`
	BuffersHit       int64   `json:"buffersSharedHit"`
	BuffersRead      int64   `json:"buffersSharedRead"`
	Restriction      string  `json:"restriction"`
}

// scaleScanVerdict is the classifier's answer for one execution. Bounded is true
// only when every proof predicate holds and every accounted observation is
// within its bound. Candidates (heap rows examined: actual rows plus rows
// removed by filter and by index recheck) and BitmapEntries (index entries,
// attributed apart so an entry and its heap tuple are never two candidates) are
// valid only when their Known flag is set; an unsupported, incomplete or
// rounded measurement leaves them unavailable, never zero.
type scaleScanVerdict struct {
	Statement            string                  `json:"statement"`
	ExactBindingOK       bool                    `json:"exactBindingOk"`
	AccessConditionOK    bool                    `json:"accessConditionOk"`
	MeasurementComplete  bool                    `json:"measurementComplete"`
	NonLossy             bool                    `json:"nonLossy"`
	LoopsOne             bool                    `json:"loopsOne"`
	AllBranchesAccounted bool                    `json:"allBranchesAccounted"`
	Candidates           int64                   `json:"candidates"`
	CandidatesKnown      bool                    `json:"candidatesKnown"`
	BitmapEntries        int64                   `json:"bitmapEntries"`
	BitmapEntriesKnown   bool                    `json:"bitmapEntriesKnown"`
	FeedingRows          int64                   `json:"feedingRows"`
	FeedingRowsKnown     bool                    `json:"feedingRowsKnown"`
	Bound                int64                   `json:"bound"`
	Bounded              bool                    `json:"bounded"`
	Reasons              []string                `json:"reasons"`
	OmittedZero          []string                `json:"omittedZeroCounters,omitempty"`
	Nodes                []scaleScanNodeEvidence `json:"nodes"`
}

// scaleAttrNumber reads a "Name: N" counter of a node. An absent counter is
// present=false, ok=true (the text format omits zero counters, which the caller
// decodes as zero only on a node that has its actual counters). A counter that
// is not exactly one finite, nonnegative number is ok=false with value 0, so a
// bad value is never carried into an export or a sum.
func scaleAttrNumber(n *scalePlanNode, name string) (value float64, present, ok bool) {
	raw, found := n.attrs[name]
	if !found {
		return 0, false, true
	}
	fields := strings.Fields(raw)
	if len(fields) != 1 {
		return 0, true, false
	}
	parsed, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 {
		return 0, true, false
	}
	return parsed, true, true
}

// scaleMaxCount is an exclusive bound: raw 2^53+1 rounds to 2^53 in
// float64, so the threshold itself must also be refused.
const scaleMaxCount = 1 << 53

// scaleWholeCount converts a finite, nonnegative, integral counter that a
// float64 represents exactly; anything else is refused with ok=false.
func scaleWholeCount(v float64) (int64, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v != math.Trunc(v) || v >= scaleMaxCount {
		return 0, false
	}
	return int64(v), true
}

// scaleAddCount adds two nonnegative counters, refusing an overflow.
func scaleAddCount(a, b int64) (int64, bool) {
	if a < 0 || b < 0 || a > math.MaxInt64-b {
		return 0, false
	}
	return a + b, true
}

var scaleDigitsRe = regexp.MustCompile(`^[0-9]+$`)

// scaleHeapBlocks reads "Heap Blocks: exact=N lossy=M". The text format omits a
// zero part, so an absent part is zero; a part that is unknown, repeated, not a
// plain nonnegative integer or too large for int64 makes the whole line
// unreadable (ok=false) rather than a zero.
func scaleHeapBlocks(raw string) (exact, lossy int64, ok bool) {
	seen := map[string]bool{}
	for _, f := range strings.Fields(raw) {
		k, val, cut := strings.Cut(f, "=")
		if !cut || (k != "exact" && k != "lossy") || seen[k] || !scaleDigitsRe.MatchString(val) {
			return 0, 0, false
		}
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		seen[k] = true
		if k == "exact" {
			exact = n
		} else {
			lossy = n
		}
	}
	return exact, lossy, true
}

// scaleBuffers reads the shared hit and read counts of a "Buffers:" line.
func scaleBuffers(raw string) (hit, read int64) {
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, "shared ") {
			continue
		}
		for _, f := range strings.Fields(part)[1:] {
			k, val, ok := strings.Cut(f, "=")
			n, err := strconv.ParseInt(val, 10, 64)
			if !ok || err != nil {
				continue
			}
			switch k {
			case "hit":
				hit = n
			case "read":
				read = n
			}
		}
	}
	return hit, read
}

func scaleNodeEvidence(n *scalePlanNode, role string) scaleScanNodeEvidence {
	e := scaleScanNodeEvidence{Role: role, Node: n.label, Index: n.index, IndexCond: n.attrs["Index Cond"],
		RecheckCond: n.attrs["Recheck Cond"], Filter: n.attrs["Filter"], ActualRows: n.rows, Loops: n.loops, Restriction: "none"}
	e.RemovedByFilter, _, _ = scaleAttrNumber(n, "Rows Removed by Filter")
	e.RemovedByRecheck, _, _ = scaleAttrNumber(n, "Rows Removed by Index Recheck")
	if exact, lossy, ok := scaleHeapBlocks(n.attrs["Heap Blocks"]); ok {
		e.HeapBlocksExact, e.HeapBlocksLossy = exact, lossy
	}
	e.BuffersHit, e.BuffersRead = scaleBuffers(n.attrs["Buffers"])
	return e
}

// scaleIDsWithin reports whether ids are distinct members of the batch.
func scaleIDsWithin(ids []uint64, batch []uint) bool {
	in := map[uint64]bool{}
	for _, b := range batch {
		in[uint64(b)] = true
	}
	seen := map[uint64]bool{}
	for _, id := range ids {
		if !in[id] || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

// scaleAgesNoteScanVerdict decides, conservatively, whether the measured
// execution of one ages-note statement was bounded by the subject's own rows
// (RC-4). It claims nothing it did not prove:
//
//   - the displayed parameters are bound to the captured SQL and Vars of this
//     execution and to the caller's independent facts (U, the batch ids as a
//     set). Every subject and id the plan displays in an Index Cond, a Recheck
//     Cond or a Filter, and each BitmapOr leaf on its own, is decoded and checked
//     that way; a Filter or Recheck Cond validates a binding but never qualifies
//     as access;
//   - every contact_requests feeding scan must restrict access through an Index
//     Cond (or, for a bitmap heap scan, through the Index Cond of its feeding
//     Bitmap Index Scan leaves) to U or, for the UPDATE, to exactly the batch
//     ids; Filter alone does not qualify, and a sequential scan never does;
//   - only supported, non-lossy shapes with loops=1 and complete counters prove
//     anything. Counters the text format omits when zero (Rows Removed by
//     Filter, Rows Removed by Index Recheck) read as zero only on a node that
//     has its actual counters; a missing actual, a "never executed" node, a
//     rounded average, any rows removed by index recheck or lossy page, an
//     unreadable, negative, nonfinite or oversized counter, an unknown node or an
//     unclaimed bitmap node leaves the measurement unproven;
//   - candidates are summed over feeding scans and bitmap entries over bitmap
//     index leaves, apart from each other, and each sum is held to the bound
//     (S_U for a subject restriction, the batch size for exact ids; the smaller
//     one when scans differ);
//   - an exact total is published only when the measurement is complete,
//     non-lossy, loops=1, fully accounted and every displayed binding resolved;
//     otherwise the Known flags stay false and the totals zero, and only the
//     per-node counters in Nodes remain.
func scaleAgesNoteScanVerdict(in scaleScanInput) scaleScanVerdict {
	v := scaleScanVerdict{Statement: in.Statement, Bound: -1, ExactBindingOK: true, AccessConditionOK: true,
		MeasurementComplete: true, NonLossy: true, LoopsOne: true, AllBranchesAccounted: true, Reasons: []string{}}
	fail := func(format string, args ...any) { v.Reasons = append(v.Reasons, fmt.Sprintf(format, args...)) }
	// unresolved: some displayed or captured binding could not be read at all.
	problems, unresolved := scaleBindingProblems(in)
	for _, p := range problems {
		v.ExactBindingOK = false
		fail("binding: %s", p)
	}
	nodes, err := scaleParsePlan(in.Lines)
	if err != nil {
		fail("not proven: %v", err)
		v.AccessConditionOK, v.MeasurementComplete, v.NonLossy, v.LoopsOne, v.AllBranchesAccounted = false, false, false, false, false
		return v
	}

	var heaps []int
	claimed := map[int]bool{}
	for i, n := range nodes {
		switch n.typ {
		case "Update":
			if n.relation != "contact_requests" {
				v.AllBranchesAccounted = false
				fail("not proven: the plan updates %q, not contact_requests", n.relation)
			}
		case "LockRows", "Sort":
		case "Seq Scan", "Index Scan", "Index Scan Backward", "Index Only Scan", "Index Only Scan Backward", "Bitmap Heap Scan":
			if n.relation != "contact_requests" {
				v.AllBranchesAccounted = false
				fail("not proven: %q reads %q, which this statement's measurement does not account for", n.label, n.relation)
				continue
			}
			heaps = append(heaps, i)
		case "Bitmap Index Scan", "BitmapOr", "BitmapAnd":
			// Judged with the bitmap heap scan above them; unclaimed ones are
			// reported after the loop.
		default:
			v.AllBranchesAccounted = false
			fail("not proven: unknown plan node %q", n.label)
		}
	}

	var candidates, entries, feeding, bound int64
	bound = -1
	sawBitmap, overflow := false, false
	lower := func(b int64) {
		if bound < 0 || b < bound {
			bound = b
		}
	}
	// add sums nonnegative counters; an overflow leaves the measurement unproven.
	add := func(dst *int64, k int64) {
		sum, ok := scaleAddCount(*dst, k)
		if !ok {
			if !overflow {
				fail("not proven: the counters overflow when summed")
			}
			overflow, v.MeasurementComplete = true, false
			return
		}
		*dst = sum
	}
	// validate decodes one displayed condition and checks every subject and id it
	// names against U and the batch. A subset condition (one BitmapOr leaf) need
	// only name distinct members of the batch; any other names exactly the batch.
	// It says nothing about whether the condition qualifies as access.
	validate := func(where, cond string, subset bool) (scaleRestriction, bool) {
		r := scaleParseRestriction(cond, in.Vars)
		bad := false
		for _, p := range r.problems {
			fail("binding: %s: %s", where, p)
			bad = true
		}
		unresolved = unresolved || r.unresolved
		if r.hasSubject && r.subject != uint64(in.Subject) {
			fail("binding: %s: the displayed subject %d is not U (%d)", where, r.subject, in.Subject)
			bad = true
		}
		if r.hasIDs {
			if subset && !scaleIDsWithin(r.ids, in.Batch) {
				fail("binding: %s: the displayed ids (%d) are not distinct members of the captured batch ids (%d)", where, len(r.ids), len(in.Batch))
				bad = true
			} else if !subset && !scaleSameIDs(r.ids, in.Batch) {
				fail("binding: %s: the displayed ids (%d) are not the captured batch ids (%d) as a set", where, len(r.ids), len(in.Batch))
				bad = true
			}
		}
		if bad {
			v.ExactBindingOK = false
		}
		return r, !bad
	}
	// judge turns a validated restriction into its access kind and bound.
	judge := func(r scaleRestriction, valid bool) (kind string, b int64) {
		if !valid {
			return "", -1
		}
		switch {
		case r.hasIDs && in.Statement == "update":
			return "ids", int64(len(in.Batch))
		case r.hasSubject:
			if in.SU <= 0 {
				fail("not proven: S_U is not known, so a subject restriction has no bound")
				return "", -1
			}
			return "subject", in.SU
		}
		return "", -1
	}

	for _, i := range heaps {
		n := nodes[i]
		e := scaleNodeEvidence(n, "heap")
		if n.never || !n.hasActual {
			v.MeasurementComplete = false
			fail("not proven: %q has no actual counters (never executed or not analyzed)", n.label)
		} else {
			if n.loops != 1 {
				v.LoopsOne = false
				fail("not proven: %q ran %d loops, so its row counts are rounded averages", n.label, n.loops)
			}
			if _, ok := scaleWholeCount(n.rows); !ok {
				v.MeasurementComplete = false
				fail("not proven: %q reports a fractional row count (or one that is unreadable or oversized)", n.label)
			}
		}
		rows, _ := scaleWholeCount(n.rows)
		removedF, hasF, okF := scaleAttrNumber(n, "Rows Removed by Filter")
		removedR, _, okR := scaleAttrNumber(n, "Rows Removed by Index Recheck")
		for _, c := range []struct {
			name string
			f    float64
			ok   bool
		}{{"Rows Removed by Filter", removedF, okF}, {"Rows Removed by Index Recheck", removedR, okR}} {
			if _, ok := scaleWholeCount(c.f); !c.ok || !ok {
				v.MeasurementComplete = false
				fail("not proven: %q reports an unreadable, fractional or oversized %q", n.label, c.name)
			}
		}
		if !hasF && n.attrs["Filter"] != "" && n.hasActual {
			v.OmittedZero = append(v.OmittedZero, n.label+": Rows Removed by Filter")
		}
		removedFilter, _ := scaleWholeCount(removedF)
		removedRecheck, _ := scaleWholeCount(removedR)
		cand := rows
		add(&cand, removedFilter)
		add(&cand, removedRecheck)
		// Every displayed Filter is decoded for its bindings on every scan type; it
		// is never credited as access.
		validate(n.label+" Filter", n.attrs["Filter"], false)
		var lossy int64

		switch n.typ {
		case "Seq Scan":
			v.AccessConditionOK = false
			fail("a sequential scan of contact_requests examined %d rows to find its output; no index restricts access", cand)
		case "Bitmap Heap Scan":
			sawBitmap = true
			if n.attrs["Recheck Cond"] == "" {
				v.MeasurementComplete = false
				fail("not proven: %q has no Recheck Cond", n.label)
			}
			validate(n.label+" Recheck Cond", n.attrs["Recheck Cond"], false)
			raw, shown := n.attrs["Heap Blocks"]
			var exact int64
			blocksOK := true
			if shown {
				exact, lossy, blocksOK = scaleHeapBlocks(raw)
				if !blocksOK {
					v.MeasurementComplete = false
					fail("not proven: %q shows a Heap Blocks line the classifier cannot read", n.label)
				}
			}
			e.HeapBlocksExact, e.HeapBlocksLossy = exact, lossy
			if cand > 0 {
				blocks, sumOK := scaleAddCount(exact, lossy)
				switch {
				case !shown:
					v.MeasurementComplete = false
					fail("not proven: %q read %d rows but shows no Heap Blocks line", n.label, cand)
				case blocksOK && (!sumOK || blocks == 0):
					v.MeasurementComplete = false
					fail("not proven: %q read %d rows but reports no valid heap block count", n.label, cand)
				}
			}
			// The feeding bitmap tree: leaves are Bitmap Index Scans; a BitmapOr is
			// walked; a BitmapAnd or anything else is not classified.
			var leaves []int
			var walk func(parent int)
			walk = func(parent int) {
				for _, c := range nodes[parent].children {
					claimed[c] = true
					switch nodes[c].typ {
					case "Bitmap Index Scan":
						leaves = append(leaves, c)
					case "BitmapOr":
						walk(c)
					default:
						v.AllBranchesAccounted = false
						fail("not proven: %q under a bitmap heap scan is not classified", nodes[c].label)
					}
				}
			}
			walk(i)
			orWrapped := len(leaves) > 1
			if len(leaves) == 0 {
				v.AllBranchesAccounted = false
				fail("not proven: %q has no feeding Bitmap Index Scan", n.label)
			}
			var union scaleRestriction
			unionValid := true
			for _, l := range leaves {
				ln := nodes[l]
				le := scaleNodeEvidence(ln, "bitmap-index")
				if ln.never || !ln.hasActual {
					v.MeasurementComplete = false
					fail("not proven: %q has no actual counters", ln.label)
				} else {
					if ln.loops != 1 {
						v.LoopsOne = false
						fail("not proven: %q ran %d loops", ln.label, ln.loops)
					}
					if k, ok := scaleWholeCount(ln.rows); !ok {
						v.MeasurementComplete = false
						fail("not proven: %q reports an unreadable, fractional or oversized entry count", ln.label)
					} else {
						add(&entries, k)
					}
				}
				// Each leaf is validated on its own before any aggregation.
				r, valid := validate(ln.label+" Index Cond", ln.attrs["Index Cond"], orWrapped)
				if orWrapped {
					// Every leaf of a BitmapOr must restrict to ids; together they
					// are the batch.
					if !r.hasIDs {
						v.AccessConditionOK = false
						fail("a BitmapOr leaf %q does not restrict to ids through its Index Cond", ln.label)
					}
					unionValid = unionValid && valid
					union.hasIDs, union.ids = true, append(union.ids, r.ids...)
					le.Restriction = "ids"
				} else {
					kind, b := judge(r, valid)
					le.Restriction = kind
					if kind == "" {
						v.AccessConditionOK = false
						fail("no selective Index Cond on %q (a Filter alone does not qualify)", ln.label)
					} else {
						lower(b)
					}
				}
				v.Nodes = append(v.Nodes, le)
			}
			if orWrapped {
				if unionValid && !scaleSameIDs(union.ids, in.Batch) {
					v.ExactBindingOK, unionValid = false, false
					fail("binding: the BitmapOr leaves name %d ids, not the captured batch ids (%d) as a set", len(union.ids), len(in.Batch))
				}
				kind, b := judge(union, unionValid)
				if kind != "ids" {
					v.AccessConditionOK = false
					fail("the BitmapOr leaves do not restrict access to exactly the batch ids")
				} else {
					lower(b)
					e.Restriction = kind
				}
			} else if len(leaves) == 1 {
				e.Restriction = v.Nodes[len(v.Nodes)-1].Restriction
			}
		default: // Index Scan, Index Only Scan, backward forms
			r, valid := validate(n.label+" Index Cond", n.attrs["Index Cond"], false)
			kind, b := judge(r, valid)
			e.Restriction = kind
			if kind == "" {
				v.AccessConditionOK = false
				fail("no selective Index Cond on %q (a Filter alone does not qualify); it examined %d rows to find its output", n.label, cand)
			} else {
				lower(b)
			}
		}
		// Index recheck work, on any scan type, and lossy bitmap pages are work the
		// classifier does not bound.
		if lossy > 0 || removedRecheck > 0 {
			v.NonLossy = false
			fail("not proven: %q did index recheck or lossy bitmap work (lossy blocks %d, rows removed by index recheck %d)", n.label, lossy, removedRecheck)
		}
		add(&candidates, cand)
		add(&feeding, rows)
		v.Nodes = append(v.Nodes, e)
	}
	for i, n := range nodes {
		if (n.typ == "Bitmap Index Scan" || n.typ == "BitmapOr" || n.typ == "BitmapAnd") && !claimed[i] {
			v.AllBranchesAccounted = false
			fail("not proven: %q feeds no bitmap heap scan the classifier read", n.label)
		}
	}
	if len(heaps) == 0 {
		v.AccessConditionOK = false
		fail("no scan of contact_requests was found in the plan")
	}
	v.Bound = bound
	// An exact total exists only for a complete, non-lossy, fully resolved
	// measurement; the raw per-node counters stay in Nodes either way.
	known := v.MeasurementComplete && v.NonLossy && v.LoopsOne && v.AllBranchesAccounted && !unresolved && !overflow && len(heaps) > 0
	if unresolved {
		fail("not proven: a displayed or captured binding could not be resolved, so no exact total is published")
	}
	over := false
	if known {
		v.Candidates, v.CandidatesKnown = candidates, true
		v.FeedingRows, v.FeedingRowsKnown = feeding, true
		if sawBitmap {
			v.BitmapEntries, v.BitmapEntriesKnown = entries, true
		}
		if bound >= 0 && candidates > bound {
			over = true
			fail("examined %d candidates, above the bound %d", candidates, bound)
		}
		if bound >= 0 && sawBitmap && entries > bound {
			over = true
			fail("%d bitmap index entries, above the bound %d", entries, bound)
		}
	}
	v.Bounded = v.ExactBindingOK && v.AccessConditionOK && v.MeasurementComplete && v.NonLossy && v.LoopsOne &&
		v.AllBranchesAccounted && known && bound >= 0 && !over
	return v
}

// ---- the evidence of one scale run ----------------------------------------

type scaleStatementEvidence struct {
	Label        string           `json:"label"`
	SQL          string           `json:"sql"`
	Plan         []string         `json:"plan"`
	Millis       float64          `json:"executionMillis"`
	Binds        int              `json:"bindCount"`
	RowsAffected []int64          `json:"rowsAffected,omitempty"`
	TopRows      int64            `json:"topNodeRows"`
	TriggerCalls int64            `json:"addRequestIdentityCalls"`
	TriggerFound bool             `json:"addRequestIdentityFound"`
	Verdict      scaleScanVerdict `json:"verdict"`
}

type scaleAgesNoteEvidence struct {
	Facts      scaleAgesNoteFacts     `json:"fixture"`
	HelperOnly scaleHelperOnlyResult  `json:"helperOnlyScope"`
	Select     scaleStatementEvidence `json:"select"`
	Update     scaleStatementEvidence `json:"update"`
}

// checkScaleAgesNoteScrub asserts the counts that tell a real scrub from an
// empty one, each on its own (the lock selection's four rows, the real UPDATE's
// RowsAffected = 4 with five binds, the trigger's four calls, the helper-only
// scope), and classifies both statements' plans. A false verdict is logged as
// an open database-review finding; it does not fail the run.
func checkScaleAgesNoteScrub(t *testing.T, run scaleExplain) scaleAgesNoteEvidence {
	t.Helper()
	facts := run.agesFacts
	want := int64(len(facts.BatchIDs))
	if want != 4 {
		t.Fatalf("the fixture selects %d ages notes for U, want 4", want)
	}
	find := func(label string) *scalePlan {
		var found *scalePlan
		n := 0
		for i := range run.plans {
			if run.plans[i].label == label {
				found = &run.plans[i]
				n++
			}
		}
		if n != 1 {
			t.Fatalf("%d plans recorded for %q, want exactly 1 (four rows are one lock selection and one UPDATE batch)", n, label)
		}
		return found
	}
	ev := scaleAgesNoteEvidence{Facts: facts, HelperOnly: run.helperOnly}
	h := run.helperOnly
	if !h.OthersUnchanged || !h.SelectedRestUnchanged || !h.ReasonsUnchanged || h.SelectedRows != want || h.SelectedNotesBefore != want || h.SelectedNotesAfter != 0 {
		t.Errorf("helper-only scope: %+v; want every row the lock selection did not pick unchanged, each of the %d picked rows unchanged except a blank note, and no reason changed", h, want)
	}
	millis := func(lines []string) float64 {
		for _, l := range lines {
			if m := scaleExecRe.FindStringSubmatch(l); m != nil {
				f, _ := strconv.ParseFloat(m[1], 64)
				return f
			}
		}
		return -1
	}

	sel := find(scaleAgesNoteSelectLabel)
	ev.Select = scaleStatementEvidence{Label: sel.label, SQL: strings.Join(strings.Fields(sel.sql), " "), Plan: sel.lines, Millis: millis(sel.lines), Binds: len(sel.vars), TopRows: -1}
	if nodes, err := scaleParsePlan(sel.lines); err != nil {
		t.Errorf("lock selection plan: %v", err)
	} else if top := nodes[0]; top.typ != "LockRows" || !top.hasActual || top.loops != 1 || top.rows != float64(want) {
		t.Errorf("lock selection: the top node %q does not report %d rows in one loop", top.label, want)
	} else {
		ev.Select.TopRows = int64(top.rows)
	}
	if len(sel.vars) != 1 {
		t.Errorf("lock selection carried %d binds, want 1", len(sel.vars))
	}
	ev.Select.Verdict = scaleAgesNoteScanVerdict(scaleScanInput{Statement: "select", Lines: sel.lines, SQL: sel.sql, Vars: sel.vars,
		Subject: scaleTargetID, SU: facts.SU})

	upd := find(scaleAgesNoteUpdateLabel)
	ev.Update = scaleStatementEvidence{Label: upd.label, SQL: strings.Join(strings.Fields(upd.sql), " "), Plan: upd.lines, Millis: millis(upd.lines),
		Binds: len(upd.vars), RowsAffected: run.affected[scaleAgesNoteUpdateLabel], TopRows: 0}
	if got := run.affected[scaleAgesNoteUpdateLabel]; len(got) != 1 || got[0] != want {
		t.Errorf("the real UPDATE's RowsAffected: %v, want exactly one execution touching %d rows", got, want)
	}
	if len(upd.vars) != int(want)+1 {
		t.Errorf("the UPDATE carried %d binds, want %d (the batch ids and U)", len(upd.vars), want+1)
	}
	if execs := run.execs[scaleAgesNoteUpdateLabel]; len(execs) != 1 || execs[0].sql != upd.sql || !reflect.DeepEqual(execs[0].vars, upd.vars) {
		t.Errorf("the measured UPDATE and its real execution are not one statement with one set of binds (%d executions recorded)", len(execs))
	}
	ev.Update.TriggerCalls, ev.Update.TriggerFound = scaleTriggerCalls(upd.lines, "add_request_identity")
	if !ev.Update.TriggerFound || ev.Update.TriggerCalls != want {
		t.Errorf("'Trigger add_request_identity' reports calls=%d (found %v), want calls=%d:\n%s", ev.Update.TriggerCalls, ev.Update.TriggerFound, want, strings.Join(upd.lines, "\n"))
	}
	ev.Update.Verdict = scaleAgesNoteScanVerdict(scaleScanInput{Statement: "update", Lines: upd.lines, SQL: upd.sql, Vars: upd.vars,
		Subject: scaleTargetID, Batch: facts.BatchIDs, SU: facts.SU})

	for _, s := range []scaleStatementEvidence{ev.Select, ev.Update} {
		v := s.Verdict
		t.Logf("ages-note %s: bounded=%v (binding %v, access %v, complete %v, non-lossy %v, loops=1 %v, branches %v); candidates %d (known %v), bitmap entries %d (known %v), feeding rows %d (known %v), bound %d; %.3f ms; reasons %q",
			v.Statement, v.Bounded, v.ExactBindingOK, v.AccessConditionOK, v.MeasurementComplete, v.NonLossy, v.LoopsOne, v.AllBranchesAccounted,
			v.Candidates, v.CandidatesKnown, v.BitmapEntries, v.BitmapEntriesKnown, v.FeedingRows, v.FeedingRowsKnown, v.Bound, s.Millis, v.Reasons)
		if !v.Bounded {
			t.Logf("ages-note %s: OPEN DATABASE-REVIEW FINDING: boundedness is not proven, so no claim is made; the raw plan is in the evidence file", v.Statement)
		}
	}
	t.Logf("ages-note UPDATE: RowsAffected %v, %d binds, add_request_identity calls %d (found %v), helper-only scope %+v",
		ev.Update.RowsAffected, ev.Update.Binds, ev.Update.TriggerCalls, ev.Update.TriggerFound, ev.HelperOnly)
	return ev
}

// ---- the classifier's unit test (ordinary CI, no database) ----------------

func agesPad(n int) string { return strings.Repeat(" ", n) }

// agesNode renders one node of EXPLAIN (ANALYZE, BUFFERS) text with its
// attribute lines. rows "" omits the actual counters and "never" prints
// "(never executed)".
func agesNode(col int, label, rows string, loops int, attrs ...string) string {
	cost := "(cost=0.29..8.31 rows=1 width=14)"
	arrow := ""
	if col > 0 {
		arrow = "->  "
	}
	switch rows {
	case "":
	case "never":
		cost += " (never executed)"
	default:
		cost += fmt.Sprintf(" (actual time=0.020..0.022 rows=%s loops=%d)", rows, loops)
	}
	out := agesPad(col) + arrow + label + "  " + cost + "\n"
	for _, a := range attrs {
		out += agesPad(col+6) + a + "\n"
	}
	return out
}

const agesFooter = "Planning Time: 0.110 ms\nExecution Time: 0.098 ms\n"

func agesSelectPlan(scan string) string {
	return "LockRows  (cost=4.30..16.69 rows=1 width=14) (actual time=0.050..0.056 rows=4 loops=1)\n  Buffers: shared hit=9\n" +
		"  ->  Sort  (cost=4.30..4.31 rows=1 width=14) (actual time=0.040..0.041 rows=4 loops=1)\n" +
		"        Sort Key: id\n        Sort Method: quicksort  Memory: 25kB\n        Buffers: shared hit=5\n" + scan + agesFooter
}

func agesUpdatePlan(children, trigger string) string {
	return "Update on contact_requests  (cost=0.29..16.34 rows=0 width=0) (actual time=0.310..0.310 rows=0 loops=1)\n" + children +
		"Planning Time: 0.210 ms\n" + trigger + "Execution Time: 0.404 ms\n"
}

const agesTrigger4 = "Trigger add_request_identity: time=0.200 calls=4\n"

const agesFilter = "Filter: ((add_childcare_ages_note <> ''::text) AND (purpose = 'occurrence_add'::text))"

func agesUpdateSQL(n int) string {
	ph := make([]string, n)
	for i := range ph {
		ph[i] = "$" + strconv.Itoa(i+1)
	}
	return "UPDATE contact_requests SET add_childcare_ages_note = '' WHERE id IN (" + strings.Join(ph, ",") + ") AND purpose = 'occurrence_add' AND subject_user_id = $" +
		strconv.Itoa(n+1) + " AND add_childcare_ages_note <> ''"
}

const agesSelectSQL = "SELECT id FROM contact_requests WHERE purpose = 'occurrence_add' AND subject_user_id = $1 AND add_childcare_ages_note <> '' ORDER BY id FOR UPDATE"

func agesVars(subject uint, ids ...uint) []any {
	out := make([]any, 0, len(ids)+1)
	for _, id := range ids {
		out = append(out, id)
	}
	return append(out, subject)
}

func TestAgesNoteErasureScanVerdict(t *testing.T) {
	batch := []uint{9993, 9994, 9995, 9996}
	subjectIdx := func(rows string, loops int, attrs ...string) string {
		return agesNode(8, "Index Scan using contact_requests_subject on contact_requests", rows, loops, attrs...)
	}
	idxCond := "Index Cond: (subject_user_id = '3'::bigint)"
	pkAny := "Index Cond: (id = ANY ('{9993,9994,9995,9996}'::bigint[]))"
	updScan := func(rows string, loops int, attrs ...string) string {
		return agesNode(2, "Index Scan using contact_requests_pkey on contact_requests", rows, loops, attrs...)
	}
	bitmapSubject := func(heapRows string, heapAttrs []string, leafRows string, leafCond string) string {
		return agesNode(8, "Bitmap Heap Scan on contact_requests", heapRows, 1, heapAttrs...) +
			agesNode(14, "Bitmap Index Scan on contact_requests_subject", leafRows, 1, leafCond, "Buffers: shared hit=2")
	}
	orLeaves := func(rowsPer string, ids ...string) string {
		out := agesNode(2, "Bitmap Heap Scan on contact_requests", "4", 1,
			"Recheck Cond: ((id = 9993) OR (id = 9994) OR (id = 9995) OR (id = 9996))", agesFilter, "Heap Blocks: exact=1", "Buffers: shared hit=6") +
			agesNode(8, "BitmapOr", "4", 1)
		for _, id := range ids {
			out += agesNode(14, "Bitmap Index Scan on contact_requests_pkey", rowsPer, 1, "Index Cond: (id = '"+id+"'::bigint)", "Buffers: shared hit=2")
		}
		return out
	}
	subjRecheck := "Recheck Cond: (subject_user_id = '3'::bigint)"
	realFilter := func(ids string) string {
		return "Filter: ((add_childcare_ages_note <> ''::text) AND (purpose = 'occurrence_add'::text) AND (id = ANY ('{" + ids + "}'::bigint[])))"
	}
	// bmSel is the select's subject bitmap pair with the given heap attribute lines.
	bmSel := func(heapAttrs ...string) string {
		return agesSelectPlan(bitmapSubject("4", append([]string{subjRecheck, agesFilter}, heapAttrs...), "4", idxCond))
	}
	// bmUpd is the recorded UPDATE shape: subject access, the batch ids only in Filter.
	bmUpd := func(recheck, filter string) string {
		return agesUpdatePlan(agesNode(2, "Bitmap Heap Scan on contact_requests", "4", 1, recheck, filter, "Heap Blocks: exact=1", "Buffers: shared hit=3")+
			agesNode(8, "Bitmap Index Scan on contact_requests_subject", "4", 1, idxCond, "Buffers: shared hit=2"), agesTrigger4)
	}
	// orSubjects is a BitmapOr whose leaves also name a subject each.
	orSubjects := func(subjects ...string) string {
		out := agesNode(2, "Bitmap Heap Scan on contact_requests", "4", 1,
			"Recheck Cond: ((id = 9993) OR (id = 9994) OR (id = 9995) OR (id = 9996))", agesFilter, "Heap Blocks: exact=1", "Buffers: shared hit=6") +
			agesNode(8, "BitmapOr", "4", 1)
		for i, sj := range subjects {
			out += agesNode(14, "Bitmap Index Scan on contact_requests_pkey", "1", 1,
				"Index Cond: ((id = '"+strconv.Itoa(9993+i)+"'::bigint) AND (subject_user_id = '"+sj+"'::bigint))", "Buffers: shared hit=2")
		}
		return out
	}
	updVars := agesVars(3, batch...)
	// sel runs the select against S_U = 4 unless a bound is given.
	sel := func(plan string, su ...int64) scaleScanInput {
		bound := int64(4)
		if len(su) > 0 {
			bound = su[0]
		}
		return scaleScanInput{Statement: "select", Lines: strings.Split(strings.TrimSuffix(plan, "\n"), "\n"), SQL: agesSelectSQL, Vars: agesVars(3), Subject: 3, SU: bound}
	}
	upd := func(plan string, su int64, ids []uint, vars []any) scaleScanInput {
		return scaleScanInput{Statement: "update", Lines: strings.Split(strings.TrimSuffix(plan, "\n"), "\n"), SQL: agesUpdateSQL(len(ids)), Vars: vars, Subject: 3, Batch: ids, SU: su}
	}

	type flag int
	const (
		fBinding flag = iota
		fAccess
		fComplete
		fNonLossy
		fLoops
		fBranches
	)
	cases := []struct {
		name       string
		in         scaleScanInput
		bounded    bool
		bound      int64
		candidates int64
		entries    int64 // -1: not a bitmap plan
		falseFlags []flag
		reason     string
		unknown    bool // candidates unavailable
	}{
		// ---- positive: loops=1, complete non-lossy counters, exact binding, every branch accounted
		{name: "select: selective subject index scan, omitted-zero counters",
			in:      sel(agesSelectPlan(subjectIdx("4", 1, idxCond, agesFilter, "Buffers: shared hit=5")), 4),
			bounded: true, bound: 4, candidates: 4, entries: -1},
		{name: "select: subject index scan, filtered rows count as candidates within S_U",
			in:      sel(agesSelectPlan(subjectIdx("4", 1, idxCond, agesFilter, "Rows Removed by Filter: 4", "Buffers: shared hit=5")), 8),
			bounded: true, bound: 8, candidates: 8, entries: -1},
		{name: "select: subject bitmap pair, index entries and heap candidates attributed separately",
			in: sel(agesSelectPlan(bitmapSubject("4", []string{"Recheck Cond: (subject_user_id = '3'::bigint)", agesFilter, "Rows Removed by Filter: 2", "Heap Blocks: exact=1", "Buffers: shared hit=4"},
				"6", idxCond)), 6),
			bounded: true, bound: 6, candidates: 6, entries: 6},
		{name: "select: PostgreSQL 18 decimal counters that are whole numbers",
			in:      sel(agesSelectPlan(subjectIdx("4.00", 1, idxCond, agesFilter)), 4),
			bounded: true, bound: 4, candidates: 4, entries: -1},
		{name: "update: exact batch id set through the primary key",
			in:      upd(agesUpdatePlan(updScan("4", 1, pkAny, agesFilter, "Buffers: shared hit=12"), agesTrigger4), 4, batch, agesVars(3, batch...)),
			bounded: true, bound: 4, candidates: 4, entries: -1},
		{name: "update: bind placeholders in the plan resolve against the captured Vars",
			in:      upd(agesUpdatePlan(updScan("4", 1, "Index Cond: (id = ANY (ARRAY[$1, $2, $3, $4]))", agesFilter), agesTrigger4), 4, batch, agesVars(3, batch...)),
			bounded: true, bound: 4, candidates: 4, entries: -1},
		{name: "update: subject access through a bound parameter",
			in:      upd(agesUpdatePlan(agesNode(2, "Index Scan using contact_requests_subject on contact_requests", "4", 1, "Index Cond: (subject_user_id = $5)", agesFilter), agesTrigger4), 4, batch, agesVars(3, batch...)),
			bounded: true, bound: 4, candidates: 4, entries: -1},
		{name: "update: BitmapOr of scalar id equalities, entries aggregated",
			in:      upd(agesUpdatePlan(orLeaves("1", "9993", "9994", "9995", "9996"), agesTrigger4), 4, batch, agesVars(3, batch...)),
			bounded: true, bound: 4, candidates: 4, entries: 4},
		{name: "update: scalar equality for a one-id batch",
			in: upd(agesUpdatePlan(updScan("1", 1, "Index Cond: (id = '9993'::bigint)", agesFilter), "Trigger add_request_identity: time=0.100 calls=1\n"),
				4, []uint{9993}, agesVars(3, 9993)),
			bounded: true, bound: 1, candidates: 1, entries: -1},
		{name: "update: the recorded shape, subject index access with the batch ids in Filter",
			in:      upd(bmUpd(subjRecheck, realFilter("9993,9994,9995,9996")), 4, batch, updVars),
			bounded: true, bound: 4, candidates: 4, entries: 4},
		{name: "update: BitmapOr leaves that each also name U",
			in:      upd(agesUpdatePlan(orSubjects("3", "3", "3", "3"), agesTrigger4), 4, batch, updVars),
			bounded: true, bound: 4, candidates: 4, entries: 4},

		// ---- negative
		{name: "nonselective primary-key scan: four outputs, 9,996 filtered, no Index Cond",
			in: sel(agesSelectPlan(agesNode(8, "Index Scan using contact_requests_pkey on contact_requests", "4", 1,
				"Filter: ((add_childcare_ages_note <> ''::text) AND (purpose = 'occurrence_add'::text) AND (subject_user_id = '3'::bigint))", "Rows Removed by Filter: 9996")), 4),
			falseFlags: []flag{fAccess}, reason: "no selective Index Cond", candidates: 10000},
		{name: "sequential scan",
			in: sel(agesSelectPlan(agesNode(8, "Seq Scan on contact_requests", "4", 1,
				"Filter: (subject_user_id = '3'::bigint)", "Rows Removed by Filter: 9996")), 4),
			falseFlags: []flag{fAccess}, reason: "sequential scan", candidates: 10000},
		{name: "update: correct length, wrong ids",
			in:         upd(agesUpdatePlan(updScan("4", 1, "Index Cond: (id = ANY ('{9993,9994,9995,9999}'::bigint[]))", agesFilter), agesTrigger4), 4, batch, agesVars(3, batch...)),
			falseFlags: []flag{fBinding, fAccess}, reason: "not the captured batch ids", candidates: 4},
		{name: "update: wrong element count (three of four)",
			in:         upd(agesUpdatePlan(updScan("3", 1, "Index Cond: (id = ANY ('{9993,9994,9995}'::bigint[]))", agesFilter), agesTrigger4), 4, batch, agesVars(3, batch...)),
			falseFlags: []flag{fBinding, fAccess}, reason: "not the captured batch ids", candidates: 3},
		{name: "update: wrong element count (five of four)",
			in:         upd(agesUpdatePlan(updScan("4", 1, "Index Cond: (id = ANY ('{9993,9994,9995,9996,9996}'::bigint[]))", agesFilter), agesTrigger4), 4, batch, agesVars(3, batch...)),
			falseFlags: []flag{fBinding, fAccess}, reason: "not the captured batch ids", candidates: 4},
		{name: "update: displayed subject is not the captured U",
			in:         upd(agesUpdatePlan(agesNode(2, "Index Scan using contact_requests_subject on contact_requests", "4", 1, "Index Cond: (subject_user_id = '4'::bigint)", agesFilter), agesTrigger4), 4, batch, agesVars(3, batch...)),
			falseFlags: []flag{fBinding, fAccess}, reason: "is not U", candidates: 4},
		{name: "update: captured subject bind is not U",
			in:         upd(agesUpdatePlan(updScan("4", 1, pkAny, agesFilter), agesTrigger4), 4, batch, agesVars(4, batch...)),
			falseFlags: []flag{fBinding}, reason: "captured subject bind is not U", candidates: 4},
		{name: "update: captured id binds differ from the batch",
			in:         upd(agesUpdatePlan(updScan("4", 1, pkAny, agesFilter), agesTrigger4), 4, batch, agesVars(3, 9993, 9994, 9995, 9997)),
			falseFlags: []flag{fBinding}, reason: "captured id binds are not the batch ids", candidates: 4},
		{name: "update: placeholder outside the captured Vars",
			in:         upd(agesUpdatePlan(updScan("4", 1, "Index Cond: (id = ANY (ARRAY[$1, $2, $3, $9]))", agesFilter), agesTrigger4), 4, batch, agesVars(3, batch...)),
			falseFlags: []flag{fBinding, fAccess}, reason: "id array is not one the classifier resolves", unknown: true, bound: -1},
		{name: "filter-only restriction on the subject index",
			in:         sel(agesSelectPlan(subjectIdx("4", 1, "Filter: (subject_user_id = '3'::bigint)", "Rows Removed by Filter: 9996")), 4),
			falseFlags: []flag{fAccess}, reason: "no selective Index Cond", candidates: 10000},
		{name: "observed count above the bound",
			in:         sel(agesSelectPlan(subjectIdx("4", 1, idxCond, agesFilter, "Rows Removed by Filter: 5")), 4),
			falseFlags: nil, reason: "above the bound 4", candidates: 9, bound: 4},
		{name: "loops greater than one with an in-bound rounded product",
			in:         sel(agesSelectPlan(subjectIdx("2", 2, idxCond, agesFilter)), 4),
			falseFlags: []flag{fLoops}, reason: "rounded averages", unknown: true, bound: 4},
		{name: "fractional row count",
			in:         sel(agesSelectPlan(subjectIdx("3.50", 1, idxCond, agesFilter)), 4),
			falseFlags: []flag{fComplete}, reason: "fractional row count", unknown: true, bound: 4},
		{name: "lossy bitmap work",
			in: sel(agesSelectPlan(bitmapSubject("4", []string{"Recheck Cond: (subject_user_id = '3'::bigint)", agesFilter, "Rows Removed by Index Recheck: 3", "Heap Blocks: exact=1 lossy=1"},
				"4", idxCond)), 4),
			falseFlags: []flag{fNonLossy}, reason: "lossy bitmap work", unknown: true, bound: 4},
		{name: "incomplete counters: a scan without its actual part",
			in:         sel(agesSelectPlan(subjectIdx("", 0, idxCond, agesFilter)), 4),
			falseFlags: []flag{fComplete}, reason: "no actual counters", unknown: true, bound: 4},
		{name: "incomplete counters: a scan that never executed",
			in:         sel(agesSelectPlan(subjectIdx("never", 0, idxCond, agesFilter)), 4),
			falseFlags: []flag{fComplete}, reason: "no actual counters", unknown: true, bound: 4},
		{name: "incomplete counters: a bitmap heap scan that returned rows but shows no Heap Blocks",
			in: sel(agesSelectPlan(bitmapSubject("4", []string{"Recheck Cond: (subject_user_id = '3'::bigint)", agesFilter},
				"4", idxCond)), 4),
			falseFlags: []flag{fComplete}, reason: "shows no Heap Blocks line", unknown: true, bound: 4},
		{name: "unparseable node",
			in: sel(agesSelectPlan("        ->  Index Scan using contact_requests_subject on contact_requests  (cost=0.29..8.31 rows=1 width=14) (actual time=garbage)\n"+
				"              "+idxCond+"\n"), 4),
			falseFlags: []flag{fAccess, fComplete, fNonLossy, fLoops, fBranches}, reason: "not a plan node the classifier can read", unknown: true, bound: -1},
		{name: "unknown node",
			in: sel(agesSelectPlan(agesNode(8, "Nested Loop", "4", 1)+
				agesNode(14, "Index Scan using contact_requests_subject on contact_requests", "4", 1, idxCond, agesFilter)), 4),
			falseFlags: []flag{fBranches}, reason: "unknown plan node", unknown: true, bound: 4},
		{name: "unaccounted extra branch: a second scan of another relation",
			in: upd(agesUpdatePlan(updScan("4", 1, pkAny, agesFilter)+agesNode(2, "Index Scan using users_pkey on users", "1", 1, "Index Cond: (id = '3'::bigint)"), agesTrigger4),
				4, batch, agesVars(3, batch...)),
			falseFlags: []flag{fBranches}, reason: "does not account for", unknown: true, bound: 4},
		{name: "unaccounted extra branch: a BitmapOr leaf outside the batch",
			in:         upd(agesUpdatePlan(orLeaves("1", "9993", "9994", "9995", "9996", "9999"), agesTrigger4), 4, batch, agesVars(3, batch...)),
			falseFlags: []flag{fBinding, fAccess}, reason: "do not restrict access to exactly the batch ids", candidates: 4, entries: 5},
		{name: "BitmapOr entries above the bound",
			in:         upd(agesUpdatePlan(orLeaves("2", "9993", "9994", "9995", "9996"), agesTrigger4), 4, batch, agesVars(3, batch...)),
			falseFlags: nil, reason: "bitmap index entries, above the bound 4", bound: 4, candidates: 4, entries: 8},
		{name: "bitmap index scan under no bitmap heap scan",
			in:         upd(agesUpdatePlan(agesNode(2, "Bitmap Index Scan on contact_requests_pkey", "4", 1, pkAny), agesTrigger4), 4, batch, agesVars(3, batch...)),
			falseFlags: []flag{fAccess, fBranches}, reason: "feeds no bitmap heap scan", unknown: true, bound: -1},

		// ---- T07A-SR1: recheck work and unavailable totals
		{name: "SR1: an ordinary index scan with 9,996 rows removed by index recheck",
			in:         sel(agesSelectPlan(subjectIdx("4", 1, idxCond, agesFilter, "Rows Removed by Index Recheck: 9996")), 4),
			falseFlags: []flag{fNonLossy}, reason: "index recheck or lossy bitmap work", unknown: true, bound: 4},
		{name: "SR1: the update's primary-key scan with rows removed by index recheck",
			in:         upd(agesUpdatePlan(updScan("4", 1, pkAny, agesFilter, "Rows Removed by Index Recheck: 1"), agesTrigger4), 4, batch, updVars),
			falseFlags: []flag{fNonLossy}, reason: "index recheck or lossy bitmap work", unknown: true, bound: 4},
		{name: "SR1: a bitmap heap scan with recheck removals but no lossy page",
			in:         sel(bmSel("Rows Removed by Index Recheck: 2", "Heap Blocks: exact=1")),
			falseFlags: []flag{fNonLossy}, reason: "index recheck or lossy bitmap work", unknown: true, bound: 4},
		{name: "SR1: an unreadable recheck counter",
			in:         sel(agesSelectPlan(subjectIdx("4", 1, idxCond, agesFilter, "Rows Removed by Index Recheck: many")), 4),
			falseFlags: []flag{fComplete}, reason: "unreadable", unknown: true, bound: 4},

		// ---- T07A-SR2: every displayed binding is validated, none is credited as access
		{name: "SR2: the update's Filter names a wrong id set",
			in:         upd(bmUpd(subjRecheck, realFilter("9993,9994,9995,9999")), 4, batch, updVars),
			falseFlags: []flag{fBinding}, reason: "not the captured batch ids", candidates: 4, entries: 4, bound: 4},
		{name: "SR2: the update's Filter names too few ids",
			in:         upd(bmUpd(subjRecheck, realFilter("9993,9994,9995")), 4, batch, updVars),
			falseFlags: []flag{fBinding}, reason: "not the captured batch ids", candidates: 4, entries: 4, bound: 4},
		{name: "SR2: the update's Filter id array cannot be resolved",
			in:         upd(bmUpd(subjRecheck, "Filter: (id = ANY (ARRAY[$1, $2, $3, $9]))"), 4, batch, updVars),
			falseFlags: []flag{fBinding}, reason: "id array is not one the classifier resolves", unknown: true, bound: 4},
		{name: "SR2: the select's Recheck Cond names another subject",
			in:         sel(agesSelectPlan(bitmapSubject("4", []string{"Recheck Cond: (subject_user_id = '4'::bigint)", agesFilter, "Heap Blocks: exact=1"}, "4", idxCond)), 4),
			falseFlags: []flag{fBinding}, reason: "is not U", candidates: 4, entries: 4, bound: 4},
		{name: "SR2: the update's Recheck Cond names another subject",
			in:         upd(bmUpd("Recheck Cond: (subject_user_id = '4'::bigint)", realFilter("9993,9994,9995,9996")), 4, batch, updVars),
			falseFlags: []flag{fBinding}, reason: "is not U", candidates: 4, entries: 4, bound: 4},
		{name: "SR2: the select's Recheck Cond has an out-of-range placeholder",
			in:         sel(agesSelectPlan(bitmapSubject("4", []string{"Recheck Cond: (subject_user_id = $9)", agesFilter, "Heap Blocks: exact=1"}, "4", idxCond)), 4),
			falseFlags: []flag{fBinding}, reason: "not a number or bind parameter the classifier resolves", unknown: true, bound: 4},
		{name: "SR2: a Filter naming another subject on a qualifying index scan",
			in:         sel(agesSelectPlan(subjectIdx("4", 1, idxCond, "Filter: (subject_user_id = '4'::bigint)")), 4),
			falseFlags: []flag{fBinding}, reason: "is not U", candidates: 4, bound: 4},
		{name: "SR2: a Filter with an unresolved subject on a qualifying index scan",
			in:         sel(agesSelectPlan(subjectIdx("4", 1, idxCond, "Filter: (subject_user_id = $7)")), 4),
			falseFlags: []flag{fBinding}, reason: "not a number or bind parameter the classifier resolves", unknown: true, bound: 4},
		{name: "SR2: a Filter naming ids on the select, which has no batch",
			in:         sel(agesSelectPlan(subjectIdx("4", 1, idxCond, "Filter: (id = ANY ('{9993}'::bigint[]))")), 4),
			falseFlags: []flag{fBinding}, reason: "not the captured batch ids", candidates: 4, bound: 4},
		{name: "SR2: an earlier BitmapOr leaf names another subject, the last names U",
			in:         upd(agesUpdatePlan(orSubjects("4", "3", "3", "3"), agesTrigger4), 4, batch, updVars),
			falseFlags: []flag{fBinding, fAccess}, reason: "is not U", candidates: 4, entries: 4},
		{name: "SR2: a middle BitmapOr leaf names another subject",
			in:         upd(agesUpdatePlan(orSubjects("3", "5", "3", "3"), agesTrigger4), 4, batch, updVars),
			falseFlags: []flag{fBinding, fAccess}, reason: "is not U", candidates: 4, entries: 4},
		{name: "SR2: the BitmapOr Recheck Cond names ids outside the batch",
			in: upd(agesUpdatePlan(agesNode(2, "Bitmap Heap Scan on contact_requests", "4", 1,
				"Recheck Cond: ((id = 9993) OR (id = 9994) OR (id = 9995) OR (id = 9999))", agesFilter, "Heap Blocks: exact=1")+
				agesNode(8, "BitmapOr", "4", 1)+
				agesNode(14, "Bitmap Index Scan on contact_requests_pkey", "1", 1, "Index Cond: (id = '9993'::bigint)")+
				agesNode(14, "Bitmap Index Scan on contact_requests_pkey", "1", 1, "Index Cond: (id = '9994'::bigint)")+
				agesNode(14, "Bitmap Index Scan on contact_requests_pkey", "1", 1, "Index Cond: (id = '9995'::bigint)")+
				agesNode(14, "Bitmap Index Scan on contact_requests_pkey", "1", 1, "Index Cond: (id = '9996'::bigint)"), agesTrigger4), 4, batch, updVars),
			falseFlags: []flag{fBinding}, reason: "not the captured batch ids", candidates: 4, entries: 4, bound: 4},
		{name: "SR2: a BitmapOr leaf repeats an id",
			in:         upd(agesUpdatePlan(orLeaves("1", "9993", "9994", "9995", "9993"), agesTrigger4), 4, batch, updVars),
			falseFlags: []flag{fBinding, fAccess}, reason: "the BitmapOr leaves", candidates: 4, entries: 4},

		{name: "SR2: nested OR of ANDs hides wrong displayed ID",
			in:         upd(bmUpd(subjRecheck, "Filter: (((status = 'denied'::text) AND (id = ANY ('{9993,9999}'::bigint[]))) OR ((status = 'ended'::text) AND (id = '9994'::bigint)))"), 4, batch, updVars),
			falseFlags: []flag{fBinding}, reason: "unsupported OR subtree", unknown: true, bound: 4},
		{name: "SR2: nested OR of ANDs hides unresolved ID",
			in:         upd(bmUpd(subjRecheck, "Filter: (((status = 'denied'::text) AND (id = ANY (ARRAY[9993,$9]))) OR ((status = 'ended'::text) AND (id = '9994'::bigint)))"), 4, batch, updVars),
			falseFlags: []flag{fBinding}, reason: "unsupported OR subtree", unknown: true, bound: 4},
		{name: "SR3: raw removed counter beyond float precision",
			in:         sel(agesSelectPlan(subjectIdx("4", 1, idxCond, agesFilter, "Rows Removed by Filter: 9007199254740993"))),
			falseFlags: []flag{fComplete}, reason: "oversized", unknown: true, bound: 4},
		{name: "SR3: raw node counter beyond float precision",
			in:         sel(agesSelectPlan(subjectIdx("9007199254740993", 1, idxCond, agesFilter))),
			falseFlags: []flag{fComplete}, reason: "oversized", unknown: true, bound: 4},

		// ---- T07A-SR3: unreadable, negative, nonfinite and oversized counters
		{name: "SR3: lossy blocks that are not a number",
			in:         sel(bmSel("Heap Blocks: exact=1 lossy=unknown")),
			falseFlags: []flag{fComplete}, reason: "Heap Blocks line the classifier cannot read", unknown: true, bound: 4},
		{name: "SR3: negative lossy blocks",
			in:         sel(bmSel("Heap Blocks: exact=1 lossy=-1")),
			falseFlags: []flag{fComplete}, reason: "Heap Blocks line the classifier cannot read", unknown: true, bound: 4},
		{name: "SR3: a signed exact block count",
			in:         sel(bmSel("Heap Blocks: exact=+1")),
			falseFlags: []flag{fComplete}, reason: "Heap Blocks line the classifier cannot read", unknown: true, bound: 4},
		{name: "SR3: a malformed exact block count",
			in:         sel(bmSel("Heap Blocks: exact=x lossy=0")),
			falseFlags: []flag{fComplete}, reason: "Heap Blocks line the classifier cannot read", unknown: true, bound: 4},
		{name: "SR3: a repeated heap block part",
			in:         sel(bmSel("Heap Blocks: exact=1 exact=1")),
			falseFlags: []flag{fComplete}, reason: "Heap Blocks line the classifier cannot read", unknown: true, bound: 4},
		{name: "SR3: an unknown heap block part",
			in:         sel(bmSel("Heap Blocks: exact=1 frozen=2")),
			falseFlags: []flag{fComplete}, reason: "Heap Blocks line the classifier cannot read", unknown: true, bound: 4},
		{name: "SR3: a heap block count past int64",
			in:         sel(bmSel("Heap Blocks: exact=9223372036854775808")),
			falseFlags: []flag{fComplete}, reason: "Heap Blocks line the classifier cannot read", unknown: true, bound: 4},
		{name: "SR3: rows read but zero heap blocks",
			in:         sel(bmSel("Heap Blocks: exact=0")),
			falseFlags: []flag{fComplete}, reason: "no valid heap block count", unknown: true, bound: 4},
		{name: "SR3: a nonfinite filter counter",
			in:         sel(agesSelectPlan(subjectIdx("4", 1, idxCond, agesFilter, "Rows Removed by Filter: +Inf"))),
			falseFlags: []flag{fComplete}, reason: "unreadable", unknown: true, bound: 4},
		{name: "SR3: a NaN filter counter",
			in:         sel(agesSelectPlan(subjectIdx("4", 1, idxCond, agesFilter, "Rows Removed by Filter: NaN"))),
			falseFlags: []flag{fComplete}, reason: "unreadable", unknown: true, bound: 4},
		{name: "SR3: a negative filter counter",
			in:         sel(agesSelectPlan(subjectIdx("4", 1, idxCond, agesFilter, "Rows Removed by Filter: -1"))),
			falseFlags: []flag{fComplete}, reason: "unreadable", unknown: true, bound: 4},
		{name: "SR3: a filter counter with a trailing word",
			in:         sel(agesSelectPlan(subjectIdx("4", 1, idxCond, agesFilter, "Rows Removed by Filter: 5 rows"))),
			falseFlags: []flag{fComplete}, reason: "unreadable", unknown: true, bound: 4},
		{name: "SR3: a filter counter past int64",
			in:         sel(agesSelectPlan(subjectIdx("4", 1, idxCond, agesFilter, "Rows Removed by Filter: 9223372036854775808"))),
			falseFlags: []flag{fComplete}, reason: "oversized", unknown: true, bound: 4},
		{name: "SR3: a filter counter past exact float64 range",
			in:         sel(agesSelectPlan(subjectIdx("4", 1, idxCond, agesFilter, "Rows Removed by Filter: 9007199254740994"))),
			falseFlags: []flag{fComplete}, reason: "oversized", unknown: true, bound: 4},
		{name: "SR3: an actual row count past int64",
			in:         sel(agesSelectPlan(subjectIdx("99999999999999999999999", 1, idxCond, agesFilter)), 4),
			falseFlags: []flag{fComplete}, reason: "oversized", unknown: true, bound: 4},
		{name: "SR3: a bitmap index entry count past int64",
			in:         sel(agesSelectPlan(bitmapSubject("4", []string{subjRecheck, agesFilter, "Heap Blocks: exact=1"}, "99999999999999999999999", idxCond)), 4),
			falseFlags: []flag{fComplete}, reason: "oversized", unknown: true, bound: 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := scaleAgesNoteScanVerdict(tc.in)
			if v.Bounded != tc.bounded {
				t.Fatalf("bounded = %v, want %v\nverdict %+v", v.Bounded, tc.bounded, v)
			}
			flags := map[flag]bool{fBinding: v.ExactBindingOK, fAccess: v.AccessConditionOK, fComplete: v.MeasurementComplete,
				fNonLossy: v.NonLossy, fLoops: v.LoopsOne, fBranches: v.AllBranchesAccounted}
			falseSet := map[flag]bool{}
			for _, f := range tc.falseFlags {
				falseSet[f] = true
			}
			for f, got := range flags {
				if got == falseSet[f] {
					t.Errorf("predicate %d = %v, want %v\nverdict %+v", f, got, !falseSet[f], v)
				}
			}
			if tc.bounded {
				if len(v.Reasons) != 0 || v.Bound != tc.bound || !v.CandidatesKnown || v.Candidates != tc.candidates || !v.FeedingRowsKnown {
					t.Errorf("positive case: reasons %q, bound %d, candidates %d (known %v), feeding known %v; want no reasons, bound %d, candidates %d\nverdict %+v",
						v.Reasons, v.Bound, v.Candidates, v.CandidatesKnown, v.FeedingRowsKnown, tc.bound, tc.candidates, v)
				}
				if tc.entries >= 0 && (!v.BitmapEntriesKnown || v.BitmapEntries != tc.entries) {
					t.Errorf("bitmap entries %d (known %v), want %d", v.BitmapEntries, v.BitmapEntriesKnown, tc.entries)
				}
				if tc.entries < 0 && v.BitmapEntriesKnown {
					t.Errorf("a plan without a bitmap reports bitmap entries")
				}
				return
			}
			found := false
			for _, r := range v.Reasons {
				found = found || strings.Contains(r, tc.reason)
			}
			if !found {
				t.Errorf("no reason mentions %q: %q", tc.reason, v.Reasons)
			}
			if tc.unknown && (v.CandidatesKnown || v.Candidates != 0 || v.BitmapEntriesKnown || v.FeedingRowsKnown) {
				t.Errorf("an unproven measurement reports counts: candidates %d (known %v), entries known %v, feeding known %v",
					v.Candidates, v.CandidatesKnown, v.BitmapEntriesKnown, v.FeedingRowsKnown)
			}
			if !tc.unknown && tc.candidates > 0 && (!v.CandidatesKnown || v.Candidates != tc.candidates) {
				t.Errorf("candidates %d (known %v), want %d", v.Candidates, v.CandidatesKnown, tc.candidates)
			}
			if !tc.unknown && tc.entries > 0 && (!v.BitmapEntriesKnown || v.BitmapEntries != tc.entries) {
				t.Errorf("bitmap entries %d (known %v), want %d", v.BitmapEntries, v.BitmapEntriesKnown, tc.entries)
			}
			if tc.bound != 0 && v.Bound != tc.bound {
				t.Errorf("bound %d, want %d", v.Bound, tc.bound)
			}
		})
	}

	t.Run("a plan with no scan of contact_requests proves nothing", func(t *testing.T) {
		v := scaleAgesNoteScanVerdict(upd(agesUpdatePlan("", agesTrigger4), 4, batch, agesVars(3, batch...)))
		if v.Bounded || v.AccessConditionOK {
			t.Errorf("bounded %v, access %v for a plan with no scan", v.Bounded, v.AccessConditionOK)
		}
	})
	t.Run("parsers", func(t *testing.T) {
		if got := scaleSplitAnd("(a = 1) AND (b = ANY ('{1,2}'::bigint[])) AND c"); len(got) != 3 {
			t.Errorf("split into %q, want 3 conjuncts", got)
		}
		if got := scaleStripParens("((subject_user_id = 3))"); got != "subject_user_id = 3" {
			t.Errorf("strip gave %q", got)
		}
		if got := scaleStripParens("(a) AND (b)"); got != "(a) AND (b)" {
			t.Errorf("strip changed %q", got)
		}
		for tok, want := range map[string]uint64{"3": 3, "'3'::bigint": 3, "($2)": 7} {
			if got, ok := scaleDecodeValue(tok, []any{uint(5), uint(7)}); !ok || got != want {
				t.Errorf("decode %q = %d, %v; want %d", tok, got, ok, want)
			}
		}
		for _, tok := range []string{"$3", "$0", "abc", "'3'::text", "3 OR 4"} {
			if got, ok := scaleDecodeValue(tok, []any{uint(5), uint(7)}); ok {
				t.Errorf("decode %q = %d, want a refusal", tok, got)
			}
		}
		if got, ok := scaleDecodeArray("'{9993,9994}'::bigint[]", nil); !ok || !scaleSameIDs(got, []uint{9994, 9993}) {
			t.Errorf("array literal decoded to %v, %v", got, ok)
		}
		if scaleSameIDs([]uint64{1, 2, 4}, []uint{1, 2, 3}) || scaleSameIDs([]uint64{1, 2}, []uint{1, 2, 3}) || scaleSameIDs([]uint64{1, 1, 2}, []uint{1, 2, 3}) {
			t.Errorf("scaleSameIDs accepted ids that are not the batch")
		}
		if n, found := scaleTriggerCalls([]string{"Trigger consents_append_only: time=1.0 calls=9", "Trigger add_request_identity: time=0.200 calls=4"}, "add_request_identity"); !found || n != 4 {
			t.Errorf("trigger calls %d, %v", n, found)
		}
		if _, found := scaleTriggerCalls([]string{"Execution Time: 1 ms"}, "add_request_identity"); found {
			t.Errorf("found a trigger line in a plan without one")
		}
		if hit, read := scaleBuffers("shared hit=5 read=2 dirtied=1, temp read=9"); hit != 5 || read != 2 {
			t.Errorf("buffers hit %d read %d", hit, read)
		}
		if e, l, ok := scaleHeapBlocks("exact=3 lossy=2"); !ok || e != 3 || l != 2 {
			t.Errorf("heap blocks %d %d %v", e, l, ok)
		}
		if e, l, ok := scaleHeapBlocks("exact=3"); !ok || e != 3 || l != 0 {
			t.Errorf("an omitted lossy part is zero: %d %d %v", e, l, ok)
		}
		for _, raw := range []string{"exact=1 lossy=unknown", "exact=1 lossy=-1", "exact=+1", "exact=1 exact=2", "exact=1 frozen=2", "exact", "1",
			"exact=9223372036854775808", "exact=1.5"} {
			if e, l, ok := scaleHeapBlocks(raw); ok || e != 0 || l != 0 {
				t.Errorf("heap blocks %q read as %d %d %v, want a refusal", raw, e, l, ok)
			}
		}
		for _, v := range []float64{math.Inf(1), math.Inf(-1), math.NaN(), -1, 0.5, scaleMaxCount + 2, 1e30} {
			if n, ok := scaleWholeCount(v); ok {
				t.Errorf("scaleWholeCount(%v) = %d, want a refusal", v, n)
			}
		}
		if n, ok := scaleWholeCount(scaleMaxCount); ok {
			t.Errorf("scaleWholeCount(2^53) = %d, want a refusal", n)
		}
		if _, ok := scaleAddCount(math.MaxInt64, 1); ok {
			t.Errorf("scaleAddCount accepted an overflow")
		}
		if _, ok := scaleAddCount(-1, 1); ok {
			t.Errorf("scaleAddCount accepted a negative counter")
		}
		if n, ok := scaleAddCount(math.MaxInt64-1, 1); !ok || n != math.MaxInt64 {
			t.Errorf("scaleAddCount(MaxInt64-1, 1) = %d, %v", n, ok)
		}
		for _, raw := range []string{"+Inf", "-Inf", "NaN", "-1", "1 2", "abc", ""} {
			node := &scalePlanNode{attrs: map[string]string{"Rows Removed by Filter": raw}}
			if v, present, ok := scaleAttrNumber(node, "Rows Removed by Filter"); ok || !present || v != 0 {
				t.Errorf("counter %q read as %v present %v ok %v, want an unreadable counter", raw, v, present, ok)
			}
		}
		if r := scaleParseRestriction("((id = 9993) OR (id = '9994'::bigint) OR (id = $1))", []any{uint(9995)}); !r.hasIDs || len(r.problems) != 0 ||
			!scaleSameIDs(r.ids, []uint{9993, 9994, 9995}) {
			t.Errorf("an OR of id equalities read as %+v", r)
		}
		for _, cond := range []string{"((id = 9993) OR (purpose = 'x'::text))", "((id = 9993) OR (subject_user_id = 3))", "((id = 9993) OR (id = $9))"} {
			if r := scaleParseRestriction(cond, []any{uint(1)}); !r.unresolved || len(r.problems) == 0 {
				t.Errorf("condition %q was not left unresolved: %+v", cond, r)
			}
		}
		if r := scaleParseRestriction("(subject_user_id = 3) AND (id = 9993) AND (id = 9994)", nil); r.unresolved || len(r.problems) != 1 {
			t.Errorf("two id restrictions are a mismatch, not an unresolved value: %+v", r)
		}
	})
}

// ---------------------------------------------------------------------------
// R4-T07A-3: concurrency acceptance CA-1 to CA-5 and CA-9 against the real
// erasure (DELETE /admin/users/:id) and the real competing operations.
//
// Every case holds one participant at a barrier that fires once (F3), on facts
// that are legal for its position (F6): Table, Where, Assign and Locking
// before a GORM chain; SQLContains and VarAt only on a row or raw statement or
// after the step. The holder's own backend PID is stored by the barrier or by
// startDBSession (F7). The waiter is then proved by an ungranted advisory row
// in pg_locks for contactLockID whose pg_blocking_pids is exactly the holder.
// SQLSTATEs come from the bounded traced handle (F4) and every case ends by
// saying which statements failed. The cases claim only what they show: one
// ordering per test.
//
// Bounds and joins (T07A3-R4, T07A3-FIX-R4-1). agesRaceRun swaps in, as the
// handle the production code reads as App.DB, one whose pool is a test-owned
// wrapper (agesRacePool). The wrapper puts the run's context on every begin,
// statement, query and prepare, in a transaction as well as outside one, and
// that context is what stop cancels. This is how cancellation reaches the one
// call site that discards App.DB's context: rolloverAges runs its preliminary
// sweep on context.Background, so WithContext alone would not bound it, but the
// wrapper sees the begin and each statement of that sweep whatever context GORM
// hands it. Connections also send the server a cancel request when a context is
// canceled, and carry lock_timeout, statement_timeout and
// idle_in_transaction_session_timeout. Those server settings are per lock wait,
// per statement and per idle gap; they are a backstop for a backend whose
// client is gone and bound no whole transaction. What bounds the work is the
// run's context deadline (agesRaceBound), and only for calls that go through
// the wrapper; a goroutine that does not touch the database is not bounded by
// it at all.
//
// stop is the only teardown. It releases the barriers, cancels the context and
// joins every tracked participant (HTTP requests, rollovers, sweeps) and then
// checks from a separate control connection that no backend of this run's
// application_name is still working. If either check fails inside grace, stop
// reports it, terminates the run's backends with pg_terminate_backend on that
// control connection, and joins and checks again within final. If a participant
// is still alive then, stop calls abort, which exits the process, so no
// later test can run against a live participant and no callback is unregistered,
// no handle restored and no shared database reset under it. Exiting is the
// only isolation available to a test-only helper, because the shared helpers
// cannot be changed and a failed Cleanup does not stop later tests. Sessions
// the shared startDBSession starts are not tracked by the wait group; they are
// covered by the backend check and by their own cleanup join, which runs before
// callbacks are removed (cleanups run last in, first out and hold registers
// stop after every barrier).
//
// Oracles (T07A3-R1 to R3). Each case states, apart from any twin, the access,
// lifecycle and time it expects, and judges the audit, notice, delivery and job
// rows of the operation that finishes the request or the access with
// agesRaceLedger: exact ids, actors, metas and times, and no row it did not
// expect. A step marked Partial (the approval, whose own effects the contact
// suites cover) requires its rows but allows others.
// ---------------------------------------------------------------------------

const agesRaceNote = "ages 2 and 5"

// agesRaceBound is the deadline of the run's context: after it every begin,
// statement and pool wait made through the traced handle fails. It does not
// bound a goroutine that does not use the database, which stop's join reports.
const agesRaceBound = 30 * time.Second

// agesRaceSeq numbers runs so each one's connections carry their own
// application_name, which is how the control connection finds them.
var agesRaceSeq atomic.Int64

// agesRaceRun owns every database participant of one race test.
type agesRaceRun struct {
	t       *testing.T
	db      *gorm.DB // the traced handle: pauses and sessions register on its callbacks
	ctx     context.Context
	cancel  context.CancelFunc
	appName string    // application_name of every pooled connection of db
	control *sql.Conn // a connection of its own, never used by a participant
	wg      sync.WaitGroup
	allDone chan struct{} // closed once wg reaches zero
	waitOne sync.Once

	mu       sync.Mutex
	releases []func()
	sealed   bool // stop has begun: no participant may start

	stopMu  sync.Mutex
	stopped bool // every participant is verified gone; a failed stop leaves it false

	// grace is how long stop waits after the barriers are released and the
	// context is canceled; final is the wait after the backends are terminated.
	grace, final time.Duration
	// report records a problem and fails the test (t.Errorf). abort is the
	// fail-closed step: it exits, ending the process. Tests of stop replace
	// both; nothing else does.
	report func(format string, args ...any)
	abort  func(msg string)
}

// agesRaceMerge returns a context that ends when ctx or stop does. The caller
// that reads rows (and so cannot cancel when the call returns) drops the cancel
// func: the context is released when the run's context ends, which stop
// guarantees.
func agesRaceMerge(ctx, stop context.Context) (context.Context, func()) {
	merged, cancel := context.WithCancel(ctx)
	unhook := context.AfterFunc(stop, cancel)
	return merged, func() { unhook(); cancel() }
}

// agesRacePool is the ConnPool of the traced handle: *sql.DB, with the run's
// context added to every call.
type agesRacePool struct {
	db   *sql.DB
	stop context.Context
}

func (p *agesRacePool) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	c, _ := agesRaceMerge(ctx, p.stop)
	return p.db.PrepareContext(c, query)
}

func (p *agesRacePool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	c, done := agesRaceMerge(ctx, p.stop)
	defer done()
	return p.db.ExecContext(c, query, args...)
}

func (p *agesRacePool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	c, _ := agesRaceMerge(ctx, p.stop)
	return p.db.QueryContext(c, query, args...)
}

func (p *agesRacePool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	c, _ := agesRaceMerge(ctx, p.stop)
	return p.db.QueryRowContext(c, query, args...)
}

// BeginTx gives GORM a transaction whose own statements carry the run's context
// too: database/sql does not pass the transaction's context to a statement
// made with another, so a begin alone would not reach a statement already
// waiting on a lock.
func (p *agesRacePool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	c, _ := agesRaceMerge(ctx, p.stop)
	tx, err := p.db.BeginTx(c, opts)
	if err != nil {
		return nil, err
	}
	return &agesRaceTx{tx: tx, db: p.db, stop: p.stop}, nil
}

// GetDBConn lets db.DB() and Connection reach the pool. Sessions made that way
// carry their own contexts.
func (p *agesRacePool) GetDBConn() (*sql.DB, error) { return p.db, nil }

// agesRaceTx is agesRacePool inside a transaction. It is a gorm.TxCommitter, as
// *sql.Tx is, so savepoints, txguard and notify see an ordinary transaction.
type agesRaceTx struct {
	tx   *sql.Tx
	db   *sql.DB
	stop context.Context
}

func (x *agesRaceTx) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	c, _ := agesRaceMerge(ctx, x.stop)
	return x.tx.PrepareContext(c, query)
}

func (x *agesRaceTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	c, done := agesRaceMerge(ctx, x.stop)
	defer done()
	return x.tx.ExecContext(c, query, args...)
}

func (x *agesRaceTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	c, _ := agesRaceMerge(ctx, x.stop)
	return x.tx.QueryContext(c, query, args...)
}

func (x *agesRaceTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	c, _ := agesRaceMerge(ctx, x.stop)
	return x.tx.QueryRowContext(c, query, args...)
}

func (x *agesRaceTx) Commit() error   { return x.tx.Commit() }
func (x *agesRaceTx) Rollback() error { return x.tx.Rollback() }

func (x *agesRaceTx) GetDBConn() (*sql.DB, error) { return x.db, nil }

// newAgesRaceRun swaps app.DB for a traced handle on the same database, like
// erasureTestSQLStates, and adds the bounds described above, plus a control
// connection opened now so that terminating works even when the pool is full.
// The original is put back on cleanup, after stop has verified the participants
// gone (cleanups run last in, first out). The returned log records every failed
// statement by SQLSTATE.
func newAgesRaceRun(t *testing.T, app *App) (*agesRaceRun, *erasureTestStateLog) {
	t.Helper()
	var name string
	if err := app.DB.Raw("SELECT current_database()").Scan(&name).Error; err != nil {
		t.Fatal(err)
	}
	dsn, err := testdb.URL(name)
	if err != nil {
		t.Fatal(err)
	}
	appName := fmt.Sprintf("fold-race-%d-%d", os.Getpid(), agesRaceSeq.Add(1))

	ctrlCfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctrlCfg.RuntimeParams["application_name"] = appName + "-ctl"
	ctrlCfg.RuntimeParams["statement_timeout"] = "5s"
	ctrlCfg.RuntimeParams["lock_timeout"] = "5s"
	ctrlDB := stdlib.OpenDB(*ctrlCfg)
	ctrlDB.SetMaxOpenConns(1)
	connCtx, connCancel := context.WithTimeout(context.Background(), 10*time.Second)
	control, err := ctrlDB.Conn(connCtx)
	connCancel()
	if err != nil {
		_ = ctrlDB.Close()
		t.Fatalf("open the control connection: %v", err)
	}

	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		_ = control.Close()
		_ = ctrlDB.Close()
		t.Fatal(err)
	}
	log := &erasureTestStateLog{t: t}
	cfg.Tracer = erasureTestTracer{log: log}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["application_name"] = appName
	cfg.RuntimeParams["lock_timeout"] = "20s"
	cfg.RuntimeParams["statement_timeout"] = "30s"
	cfg.RuntimeParams["idle_in_transaction_session_timeout"] = "30s"
	// A canceled context also cancels the statement on the server, not only
	// the client's wait for it.
	cfg.BuildContextWatcherHandler = func(c *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{Conn: c, DeadlineDelay: time.Second}
	}
	sqlDB := stdlib.OpenDB(*cfg)
	ctx, cancel := context.WithTimeout(t.Context(), agesRaceBound)
	traced, err := gorm.Open(postgres.New(postgres.Config{Conn: &agesRacePool{db: sqlDB, stop: ctx}}), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		cancel()
		_ = sqlDB.Close()
		_ = control.Close()
		_ = ctrlDB.Close()
		t.Fatal(err)
	}
	if err := traced.Use(txguard.Plugin{}); err != nil {
		t.Fatal(err)
	}
	original := app.DB
	run := &agesRaceRun{t: t, db: traced, ctx: ctx, cancel: cancel, appName: appName, control: control,
		allDone: make(chan struct{}), grace: 5 * time.Second, final: 10 * time.Second,
		report: t.Errorf, abort: func(msg string) { fmt.Fprintln(os.Stderr, msg); os.Exit(1) }}
	app.DB = traced.WithContext(ctx)
	// Registered first so they run last, after stop.
	t.Cleanup(func() {
		app.DB = original
		_ = sqlDB.Close()
	})
	t.Cleanup(func() {
		cancel()
		_ = control.Close()
		_ = ctrlDB.Close()
	})
	t.Cleanup(run.stop)
	return run, log
}

// hold keeps release to be called when the run stops, and registers stop again:
// the last cleanup registered runs first, so stop always comes before the
// cleanup of any pause or probe made so far.
func (r *agesRaceRun) hold(release func()) {
	r.mu.Lock()
	r.releases = append(r.releases, release)
	r.mu.Unlock()
	r.t.Cleanup(r.stop)
}

// pause is erasureTestPause on the traced handle, released by stop.
func (r *agesRaceRun) pause(p erasureTestPoint) (<-chan struct{}, func()) {
	r.t.Helper()
	reached, release := erasureTestPause(r.t, r.db, p)
	r.hold(release)
	return reached, release
}

// probe is erasureTestProbe on the traced handle; stop runs before it is
// unregistered.
func (r *agesRaceRun) probe(p erasureTestPoint) *atomic.Int64 {
	r.t.Helper()
	pid := erasureTestProbe(r.t, r.db, p)
	r.t.Cleanup(r.stop)
	return pid
}

// spawn runs work as a tracked participant.
func (r *agesRaceRun) spawn(work func()) {
	r.mu.Lock()
	if r.sealed {
		r.mu.Unlock()
		r.t.Fatal("a participant was started after the run began to stop")
	}
	r.wg.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		work()
	}()
}

// do is bgDo with the run's context on the request and the participant tracked.
func (r *agesRaceRun) do(c *apiClient, method, path string, body any) <-chan httpResult {
	c.ctx = r.ctx
	ch := make(chan httpResult, 1)
	r.spawn(func() {
		w := c.do(method, path, body, true)
		ch <- httpResult{w.Code, w.Body.String()}
	})
	return ch
}

// rollover runs app.rolloverAges(now) as a tracked participant.
func (r *agesRaceRun) rollover(app *App, now time.Time) <-chan agesRaceRolloverResult {
	ch := make(chan agesRaceRolloverResult, 1)
	r.spawn(func() {
		n, err := app.rolloverAges(now)
		ch <- agesRaceRolloverResult{n, err}
	})
	return ch
}

// sweep runs app.sweepContacts(now) under the run's context as a tracked
// participant.
func (r *agesRaceRun) sweep(app *App, now time.Time) <-chan error {
	ch := make(chan error, 1)
	r.spawn(func() { ch <- app.sweepContacts(r.ctx, now) })
	return ch
}

// joined waits up to d for every tracked participant to return.
func (r *agesRaceRun) joined(d time.Duration) bool {
	r.waitOne.Do(func() {
		go func() {
			r.wg.Wait()
			close(r.allDone)
		}()
	})
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-r.allDone:
		return true
	case <-timer.C:
		return false
	}
}

const agesRaceBackends = `FROM pg_stat_activity WHERE datname = current_database() AND backend_type = 'client backend'
	AND application_name = $1 AND pid <> pg_backend_pid()`

// busy waits up to d for the run's backends to have no work in progress (idle
// pooled connections do not count) and returns how many still do. It asks from
// the control connection, so a canceled run context cannot affect it.
func (r *agesRaceRun) busy(d time.Duration) (int, error) {
	deadline := time.Now().Add(d)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var n int
		err := r.control.QueryRowContext(ctx, `SELECT count(*) `+agesRaceBackends+` AND state IS DISTINCT FROM 'idle'`, r.appName).Scan(&n)
		cancel()
		if err != nil || n == 0 || !time.Now().Before(deadline) {
			return n, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// terminate ends every backend of the run from the control connection and
// returns how many it signaled.
func (r *agesRaceRun) terminate() (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var n int
	err := r.control.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT pg_terminate_backend(pid) `+agesRaceBackends+`) s`, r.appName).Scan(&n)
	return n, err
}

// backendState returns the application_name and state of backend pid, and false
// when there is no such backend.
func (r *agesRaceRun) backendState(pid int) (app, state string, ok bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var a, s sql.NullString
	err = r.control.QueryRowContext(ctx, "SELECT application_name, state FROM pg_stat_activity WHERE pid = $1", pid).Scan(&a, &s)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return a.String, s.String, err == nil, err
}

// stop releases every barrier, cancels the context and joins the participants,
// then verifies from the control connection that none of the run's backends is
// still working. A participant that outlasts grace is reported and its backends
// terminated; one that outlasts final is a fail-closed abort. It returns only
// once the participants are gone (or abort has returned in a test of stop), and
// a failed stop is retried by the next call instead of being recorded as done.
func (r *agesRaceRun) stop() {
	r.stopMu.Lock()
	defer r.stopMu.Unlock()
	if r.stopped {
		return
	}
	r.mu.Lock()
	r.sealed = true
	releases := append([]func(){}, r.releases...)
	r.mu.Unlock()
	for _, release := range releases {
		release()
	}
	r.cancel()

	joined := r.joined(r.grace)
	left, err := r.busy(r.grace)
	if joined && err == nil && left == 0 {
		r.stopped = true
		return
	}
	r.report("a database participant did not stop within %v after its barriers were released and its context was canceled (joined: %v, backends still working: %d, check: %v); terminating the run's backends", r.grace, joined, left, err)
	killed, kerr := r.terminate()
	joined = r.joined(r.final)
	left, err = r.busy(r.final)
	if kerr != nil {
		r.report("terminate the run's backends: %v", kerr)
	}
	if !joined || err != nil || left != 0 || kerr != nil {
		r.abort(fmt.Sprintf("a database participant is still alive after %d backends were terminated (joined: %v, backends still working: %d, check: %v, terminate: %v); the process ends so that no later test runs against it", killed, joined, left, err, kerr))
		return
	}
	r.stopped = true
}

// agesRaceWorld is a contact fixture with one group event, a traced and bounded
// database handle and clients that hold their notices. f.outsider is the erased
// person, f.leader asks, f.subject is the unrelated person and f.admin acts.
type agesRaceWorld struct {
	t      *testing.T
	f      *contactAPIFixture
	app    *App
	db     *gorm.DB
	run    *agesRaceRun
	states *erasureTestStateLog
	ev     models.Event
	occ    models.EventOccurrence
	seq    int
}

func newAgesRaceWorld(t *testing.T) *agesRaceWorld {
	t.Helper()
	f := newContactAPIFixture(t, false)
	for _, c := range []*apiClient{f.lc, f.sc, f.ac, f.gc, f.oc} {
		c.holdNotices = true
	}
	w := &agesRaceWorld{t: t, f: f, app: f.app}
	w.ev, w.occ = seedEvent(t, f.app, f.group, f.leader.ID, time.Now().UTC().Add(48*time.Hour))
	// Barriers and sessions are registered on the swapped handle (F4).
	w.run, w.states = newAgesRaceRun(t, f.app)
	w.db = w.run.db
	return w
}

// request inserts one occurrence_add request on the world's date. The caller
// may adjust the row before it is written (the identity columns cannot be
// changed afterwards).
func (w *agesRaceWorld) request(requester, subject uint, status, note string, adjust ...func(*models.ContactRequest)) models.ContactRequest {
	w.t.Helper()
	w.seq++
	created := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	occ := w.occ.ID
	r := models.ContactRequest{Purpose: policy.RequestOccurrenceAdd, RequesterID: requester, SubjectUserID: subject,
		ContextType: policy.ContactEvent, ContextID: w.ev.ID, OccurrenceID: &occ,
		Reason: fmt.Sprintf("identifying race reason %d", w.seq), SubjectDecision: policy.ContactPending, AdminDecision: policy.ContactNotNeeded,
		Status: status, AddAttendanceMode: models.AttendanceInPerson, AddChildcareAgesNote: note,
		CreatedAt: created, ExpiresAt: created.Add(14 * 24 * time.Hour)}
	if status == policy.ContactWithdrawn {
		done := created.Add(10 * time.Minute)
		r.CompletedAt, r.WithdrawnByID = &done, &requester
	}
	for _, f := range adjust {
		f(&r)
	}
	erasureMustCreate(w.t, w.db, &r)
	return r
}

// seedTasks writes the waiting review tasks a pending request has (the
// subject's, and the admin's when it needs one) the way request creation does.
func (w *agesRaceWorld) seedTasks(r models.ContactRequest) {
	w.t.Helper()
	erasureSeedRequestTasks(w.t, w.db, r, w.f.admin.ID)
}

// agesRaceExpired makes a pending request whose lifetime ended long ago.
func agesRaceExpired(r *models.ContactRequest) {
	r.CreatedAt = r.CreatedAt.AddDate(0, 0, -30)
	r.ExpiresAt = r.CreatedAt.Add(14 * 24 * time.Hour)
}

// agesRaceNeedsAdmin makes a pending request the kind a teen's subject gets.
func agesRaceNeedsAdmin(r *models.ContactRequest) {
	r.NeedsAdmin, r.AdminDecision = true, policy.ContactPending
}

func (w *agesRaceWorld) rows() map[uint]erasureAgesNoteRow { return erasureRequestRows(w.t, w.db) }

// erase is the real DELETE of id by the admin behind c.
func (w *agesRaceWorld) erase(c *apiClient, id uint) <-chan httpResult {
	return w.run.do(c, "DELETE", "/api/v1/admin/users/"+itoa(id), nil)
}

// decide is the subject's real approval of request id.
func (w *agesRaceWorld) decide(c *apiClient, id uint) <-chan httpResult {
	return w.run.do(c, "POST", "/api/v1/add-requests/"+itoa(id)+"/decision", map[string]string{"party": "subject", "decision": policy.ContactApproved})
}

// remove is the leader's real removal of registration reg from the world's date.
func (w *agesRaceWorld) remove(reg uint) <-chan httpResult {
	return w.run.do(w.f.lc, "POST", fmt.Sprintf("/api/v1/events/%d/registrations/remove", w.ev.ID),
		map[string]any{"registrationId": reg, "date": models.FormatCalendarDate(w.occ.OriginalDate), "note": "Moved away"})
}

// makeTeen turns u into a teen who is already past eighteen, so the age
// rollover will convert them.
func (w *agesRaceWorld) makeTeen(u *models.User) {
	w.t.Helper()
	birth := time.Now().UTC().AddDate(-18, 0, -2)
	if err := w.db.Model(u).Updates(map[string]any{"account_type": models.AccountTeen, "birth_date": birth}).Error; err != nil {
		w.t.Fatal(err)
	}
	erasureMustCreate(w.t, w.db, &models.TeenConsent{
		TeenUserID: u.ID, Scope: models.TeenConsentAccount, Status: models.ConsentGranted,
		GrantedBy: w.f.admin.ID, GranterRole: models.ConsentAdmin,
		EvidenceText: "Verified guardian consent", At: time.Now().UTC().Add(-time.Hour),
	})
}

// holdSweep does what sweepContacts does (the try-lock, then
// reconcileContacts(contactChange{}) at now) on a pinned session and holds the
// open transaction until release. It returns once the sweep has done its work
// and holds the gate; pid is that session's backend.
func (w *agesRaceWorld) holdSweep(now time.Time) (pid int, done <-chan error, release func()) {
	t := w.t
	t.Helper()
	reached, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(resume) }) }
	pid, done = startDBSession(t, w.db, "ages-sweep", func(conn *gorm.DB) error {
		return conn.Transaction(func(tx *gorm.DB) error {
			var locked bool
			if err := tx.Raw("SELECT pg_try_advisory_xact_lock(?)", contactLockID).Scan(&locked).Error; err != nil {
				return err
			}
			if !locked {
				return errors.New("the model sweep could not take the contact gate")
			}
			if _, err := w.app.reconcileContacts(tx, now, contactChange{}); err != nil {
				return err
			}
			close(reached)
			select {
			case <-resume:
				return nil
			case <-tx.Statement.Context.Done():
				return tx.Statement.Context.Err()
			}
		})
	})
	w.run.hold(release)
	awaitDBSignal(t, reached)
	return pid, done, release
}

// agesRaceAfterScrub is the first ages-note UPDATE of the erasure of uid,
// stopped after it ran: SQLContains and VarAt are legal after gorm:raw (F6).
// noted is how many ids that batch carries; the subject's bind parameter
// follows them, so a later batch of another size never matches and the barrier
// fires on the first batch only. The caller sets PID.
func agesRaceAfterScrub(uid uint, noted int) erasureTestPoint {
	return erasureTestPoint{Processor: "raw", After: true, Step: "gorm:raw",
		SQLContains: agesNoteUpdatePrefix, VarAt: map[int]any{noted: uid}}
}

// agesRaceGateWaiter polls until exactly one backend of this database waits for
// the contact gate (an ungranted advisory row in pg_locks for contactLockID) and
// pg_blocking_pids of that backend is exactly the blocker, then returns it.
// The blocker must hold the gate.
func agesRaceGateWaiter(t *testing.T, db *gorm.DB, blocker int, who string) int {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		var waiters []int
		if err := db.Raw(`SELECT l.pid FROM pg_locks l WHERE l.locktype = 'advisory' AND NOT l.granted
			AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())
			AND l.classid::bigint = ? AND l.objid::bigint = ? AND l.objsubid = 1
			AND pg_blocking_pids(l.pid) = ARRAY[?]::int[] ORDER BY l.pid`,
			contactLockID>>32, contactLockID&0xffffffff, blocker).Scan(&waiters).Error; err != nil {
			t.Fatal(err)
		}
		if len(waiters) == 1 {
			requireContactGate(t, db, waiters[0], false, who)
			requireContactGate(t, db, blocker, true, "the gate holder")
			return waiters[0]
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatalf("%s never waited for the contact gate behind backend %d (%d candidates)", who, blocker, len(waiters))
		}
	}
}

// agesRaceNoGateWaiter fails when anyone in this database waits for the contact
// gate. Other databases of the cluster (another test's) are not counted.
func agesRaceNoGateWaiter(t *testing.T, db *gorm.DB) {
	t.Helper()
	var n int64
	if err := db.Raw(`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted
		AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
		AND classid::bigint = ? AND objid::bigint = ? AND objsubid = 1`, contactLockID>>32, contactLockID&0xffffffff).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d backends wait for the contact gate, want none", n)
	}
}

// agesRaceAwait waits for a result with a bound.
func agesRaceAwait[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("%s never finished", what)
		var zero T
		return zero
	}
}

// agesRaceOK awaits an HTTP result and requires 200.
func agesRaceOK(t *testing.T, ch <-chan httpResult, what string) {
	t.Helper()
	if r := awaitResult(t, ch); r.code != http.StatusOK {
		t.Fatalf("%s: %d %s", what, r.code, r.body)
	}
}

// agesRaceStates requires that the failed statements recorded on the traced
// handle are exactly want, counted by SQLSTATE (F4).
func agesRaceStates(t *testing.T, log *erasureTestStateLog, want map[string]int) {
	t.Helper()
	log.mu.Lock()
	defer log.mu.Unlock()
	got := map[string]int{}
	for _, e := range log.entries {
		got[e.Code]++
	}
	if want == nil {
		want = map[string]int{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("failed statements by SQLSTATE = %v, want %v (all: %v)", got, want, log.entries)
	}
}

// agesRaceSame requires the request row to be byte-identical.
func agesRaceSame(t *testing.T, label string, b, a erasureAgesNoteRow) {
	t.Helper()
	if a.Whole != b.Whole {
		t.Errorf("%s changed:\n%s\n%s", label, b.Whole, a.Whole)
	}
}

// agesRaceScrubbed requires the note gone and nothing else of the row changed.
func agesRaceScrubbed(t *testing.T, label string, b, a erasureAgesNoteRow) {
	t.Helper()
	if a.Note != "" {
		t.Errorf("%s: note is %q, want empty", label, a.Note)
	}
	if a.Reason != b.Reason {
		t.Errorf("%s: reason is %q, was %q", label, a.Reason, b.Reason)
	}
	if a.Rest != b.Rest {
		t.Errorf("%s: more than the note changed:\n%s\n%s", label, b.Rest, a.Rest)
	}
}

// agesRaceEndedByErasure requires a pending row to be ended by the erasure's
// own lifecycle inside span, with every column but the lifecycle's and the
// note as it was.
func agesRaceEndedByErasure(t *testing.T, label string, b, a erasureAgesNoteRow, span erasureSpan) {
	t.Helper()
	if a.Note != "" {
		t.Errorf("%s: note is %q, want empty", label, a.Note)
	}
	if a.Reason != b.Reason {
		t.Errorf("%s: reason is %q, was %q", label, a.Reason, b.Reason)
	}
	if a.Status != policy.ContactEnded || a.EndReason != policy.ContactAccountDisabled {
		t.Errorf("%s: status %q end_reason %q, want %q and %q", label, a.Status, a.EndReason, policy.ContactEnded, policy.ContactAccountDisabled)
	}
	if !span.hasPtr(a.CompletedAt) {
		t.Errorf("%s: completed_at %v is outside the erasure (%v to %v)", label, a.CompletedAt, span.Start, span.End)
	}
	if a.Preserved != b.Preserved {
		t.Errorf("%s: a column other than status, end_reason, completed_at and the note changed:\n%s\n%s", label, b.Preserved, a.Preserved)
	}
}

// agesRaceTurned18 requires the lifecycle the age rollover's preliminary
// sweep gives the pending request of a subject who has turned eighteen
// (policy.ContactTurned18, from needs_admin and the subject's age): ended with
// that reason at the rollover's own injected clock now (stored to the
// microsecond), the reason unchanged, and every column but the lifecycle's and
// the note as it was.
func agesRaceTurned18(t *testing.T, label string, b, a erasureAgesNoteRow, now time.Time) {
	t.Helper()
	if a.Status != policy.ContactEnded || a.EndReason != policy.ContactTurned18 {
		t.Errorf("%s: status %q end_reason %q, want %q and %q", label, a.Status, a.EndReason, policy.ContactEnded, policy.ContactTurned18)
	}
	if a.CompletedAt == nil || !a.CompletedAt.Equal(now) {
		t.Errorf("%s: completed_at %v, want the rollover's clock %v", label, a.CompletedAt, now)
	}
	if a.Reason != b.Reason {
		t.Errorf("%s: reason is %q, was %q", label, a.Reason, b.Reason)
	}
	if a.Preserved != b.Preserved {
		t.Errorf("%s: a column other than status, end_reason, completed_at and the note changed:\n%s\n%s", label, b.Preserved, a.Preserved)
	}
}

func agesRaceID(id *uint) string {
	if id == nil {
		return "none"
	}
	return strconv.FormatUint(uint64(*id), 10)
}

// agesRaceAccessKind is the access columns of a registration as values, with
// the instants and the fixture's own ids left out (the issuer and the request
// as whether they are the expected ones), so a race and its twin can be set
// side by side. It never replaces the fixed rules of agesRaceGrantedAccess and
// agesRaceEndedAccess.
func agesRaceAccessKind(r models.Registration, issuer, request uint) string {
	return fmt.Sprintf("status=%s origin=%s end_reason=%s issuer_is_leader=%t request_is_own=%t ended_by=%s departure_membership=%s departure_actor=%s",
		r.Status, r.AccessOrigin, r.AccessEndReason, erasureSameID(r.AccessIssuerID, &issuer), erasureSameID(r.AccessRequestID, &request),
		agesRaceID(r.AccessEndedBy), agesRaceID(r.AccessDepartureMembershipID), agesRaceID(r.AccessDepartureActorID))
}

// agesRaceGrantedAccess requires a registration as the subject's approval of
// request left it: going, with live manager-add access issued by issuer for
// that request, granted inside the approval, with no ending and no departure
// provenance.
func agesRaceGrantedAccess(t *testing.T, label string, reg models.Registration, issuer, request uint, span erasureSpan) {
	t.Helper()
	if reg.UserID == nil || reg.Status != models.RegGoing || reg.AccessOrigin != policy.OccurrenceOriginManagerAdd ||
		!erasureSameID(reg.AccessIssuerID, &issuer) || !erasureSameID(reg.AccessRequestID, &request) ||
		reg.AccessDepartureMembershipID != nil || reg.AccessDepartureActorID != nil {
		t.Fatalf("%s: the approval did not leave a going registration with manager-add access issued by %d for request %d: %s",
			label, issuer, request, erasureAccessColumns(t, reg))
	}
	if !span.hasPtr(reg.AccessGrantedAt) {
		t.Fatalf("%s: access granted at %v, outside the approval (%v to %v)", label, reg.AccessGrantedAt, span.Start, span.End)
	}
	if reg.AccessEndedAt != nil || reg.AccessEndReason != "" || reg.AccessEndedBy != nil {
		t.Fatalf("%s: access is already ended: %s", label, erasureAccessColumns(t, reg))
	}
}

// agesRaceEndedAccess requires the registration as the erasure leaves it: the
// registration as the approval left it (every identity, origin, issuer,
// request, departure and grant column, the grant instant included), with only
// the ending changed, to reason account_disabled and no ender, at an instant
// inside the erasure and not before the grant.
func agesRaceEndedAccess(t *testing.T, label string, granted, ended models.Registration, span erasureSpan) {
	t.Helper()
	if ended.AccessEndedAt == nil {
		t.Fatalf("%s: registration %d still has live access after the erasure", label, granted.ID)
	}
	want := granted
	want.AccessEndedAt, want.AccessEndReason, want.AccessEndedBy = ended.AccessEndedAt, policy.ContactAccountDisabled, nil
	if got, w := erasureAccessColumns(t, ended), erasureAccessColumns(t, want); got != w {
		t.Errorf("%s: the access columns after the erasure:\n%s\nwant (only the ending, reason %q, no ender):\n%s", label, got, policy.ContactAccountDisabled, w)
	}
	if !span.hasMicro(accessMicro(*ended.AccessEndedAt)) {
		t.Errorf("%s: access ended at %v, outside the erasure (%v to %v)", label, ended.AccessEndedAt, span.Start, span.End)
	}
	if granted.AccessGrantedAt != nil && ended.AccessEndedAt.Before(*granted.AccessGrantedAt) {
		t.Errorf("%s: access ended at %v, before it was granted at %v", label, ended.AccessEndedAt, granted.AccessGrantedAt)
	}
}

// agesRaceAuditWant is one audit row a step must have written. Meta is the
// complete meta; when it is nil, Has lists the keys that must carry these
// values and the rest of the meta is not judged. AnyIP allows the client
// address of an HTTP actor; a system row must have none.
type agesRaceAuditWant struct {
	Type   string
	ID     uint
	Action string
	Actor  *uint
	Meta   map[string]any
	Has    map[string]any
	AnyIP  bool
}

// agesRaceNoticeWant is one new notice a step must have written, to a person
// who is still there. Dedupe is its exact dedupe key ("" when it has none).
// Each must have one queued email delivery and one notify.deliver job.
type agesRaceNoticeWant struct {
	To     uint
	Kind   string
	Type   string // subject type
	ID     uint   // subject id
	Dedupe string
}

// agesRaceTaskWant is an open task of the ledger before the step that the step
// must close: who holds it, its kind and subject, how it was resolved and by whom.
type agesRaceTaskWant struct {
	Owner      uint
	Kind       string
	ID         uint
	Resolution string
	By         *uint
}

// agesRaceStep is what one operation (or the two that finish a request together)
// must have done to the audit, notice, delivery and job rows, judged by
// agesRaceLedger between the ledger before it and the ledger after it.
type agesRaceStep struct {
	Name    string
	Span    erasureSpan // when its rows were written; the zero value skips the time checks
	Audit   []agesRaceAuditWant
	Notices []agesRaceNoticeWant
	Tasks   []agesRaceTaskWant
	Gone    uint // a person the step erased: their notices and deliveries must be gone, and nothing new may name them
	Cleanup int  // new avatar.cleanup jobs, for Gone
	Partial bool // the wanted rows are required but others are allowed
}

func agesRaceDeleted(admin, subject uint) agesRaceAuditWant {
	return agesRaceAuditWant{Type: "user", ID: subject, Action: audit.UserDeleted, Actor: &admin, Meta: map[string]any{}, AnyIP: true}
}

// agesRaceEndMeta is the meta of the audit row that finishes a pending
// occurrence_add request (finishContactRequest, contactAudit).
func agesRaceEndMeta(r models.ContactRequest, outcome, reason string) map[string]any {
	return map[string]any{"purpose": r.Purpose, "requesterId": r.RequesterID, "subjectUserId": r.SubjectUserID,
		"contextType": r.ContextType, "contextId": r.ContextID, "needsAdmin": r.NeedsAdmin, "outcome": outcome,
		"reason": reason, "occurrenceId": *r.OccurrenceID}
}

// agesRaceEndRow is the audit row for a pending request ended (outcome ended,
// with a reason) or expired (outcome expired, no reason) with no actor.
func agesRaceEndRow(r models.ContactRequest, outcome, reason string) agesRaceAuditWant {
	action := audit.AddEnded
	if outcome == policy.ContactExpired {
		action = audit.AddExpired
	}
	return agesRaceAuditWant{Type: "add_request", ID: r.ID, Action: action, Meta: agesRaceEndMeta(r, outcome, reason)}
}

// agesRaceUpdated is the add.updated notice a finished request sends to a
// person; key is the request's status at that point (ended or expired).
func agesRaceUpdated(to uint, r models.ContactRequest, key string) agesRaceNoticeWant {
	return agesRaceNoticeWant{To: to, Kind: notify.AddUpdated, Type: "add_request", ID: r.ID, Dedupe: fmt.Sprintf("add:%d:%s", r.ID, key)}
}

// agesRaceTask is the review task of owner for r that a finished request closes.
func agesRaceTask(owner uint, r models.ContactRequest, admin bool, resolution string, by *uint) agesRaceTaskWant {
	kind := notify.AddSubjectReview
	if admin {
		kind = notify.AddAdminReview
	}
	return agesRaceTaskWant{Owner: owner, Kind: kind, ID: r.ID, Resolution: resolution, By: by}
}

// agesRaceLedger judges what one step did between two ledgers. Earlier audit
// rows permit only R4-4a's fixture-specific scrubs; delivery and job rows are
// byte-identical (Gone's deliveries aside); every
// earlier notice is byte-identical except the tasks the step closes (resolved
// inside the step, by the wanted resolver, as the wanted outcome, changing
// nothing else) and Gone's, which are gone. New rows are exactly the wanted ones,
// unless Partial: audit rows with their actor and meta, notices with their
// dedupe key and subject, one queued email delivery and one notify.deliver job
// per notice, and Cleanup avatar.cleanup jobs for Gone.
func agesRaceLedger(t *testing.T, st agesRaceStep, before, after erasureLedger) {
	t.Helper()
	name := st.Name
	timed := !st.Span.Start.IsZero()
	inside := func(at time.Time) bool { return !timed || st.Span.has(at) }

	// Audit.
	for id, b := range before.Audit {
		b = erasureFixtureAuditAfter(t, b, st.Gone)
		a, ok := after.Audit[id]
		if ok {
			a = erasureFixtureAuditAfter(t, a, 0)
		}
		if !ok || erasureJSON(t, a) != erasureJSON(t, b) {
			t.Errorf("%s: audit row %d (%s) changed or is gone", name, id, b.Action)
		}
	}
	type auditKey struct {
		Type   string
		ID     uint
		Action string
	}
	wantAudit := map[auditKey]agesRaceAuditWant{}
	for _, w := range st.Audit {
		k := auditKey{w.Type, w.ID, w.Action}
		if _, dup := wantAudit[k]; dup {
			t.Fatalf("%s: the step expects two audit rows %+v", name, k)
		}
		wantAudit[k] = w
	}
	seenAudit := map[auditKey]bool{}
	for id, a := range after.Audit {
		if _, old := before.Audit[id]; old {
			continue
		}
		k := auditKey{a.TargetType, a.TargetID, a.Action}
		w, ok := wantAudit[k]
		switch {
		case !ok:
			if !st.Partial {
				t.Errorf("%s: unexpected new audit row %d: %s on %s %d, meta %s", name, id, a.Action, a.TargetType, a.TargetID, a.MetaJSON)
			}
			continue
		case seenAudit[k]:
			t.Errorf("%s: audit row %+v was written twice", name, k)
			continue
		}
		seenAudit[k] = true
		if !erasureSameID(a.ActorID, w.Actor) {
			t.Errorf("%s: audit %+v has actor %v, want %v", name, k, a.ActorID, w.Actor)
		}
		if !w.AnyIP && a.IP != "" {
			t.Errorf("%s: audit %+v has ip %q, want none", name, k, a.IP)
		}
		if !inside(a.At) {
			t.Errorf("%s: audit %+v written at %v, outside the step (%v to %v)", name, k, a.At, st.Span.Start, st.Span.End)
		}
		got := erasureMetaOf(t, a.MetaJSON)
		if w.Meta != nil {
			if want := erasureWire(t, w.Meta); !reflect.DeepEqual(got, want) {
				t.Errorf("%s: audit %+v has meta %v, want %v", name, k, got, want)
			}
		}
		if w.Has != nil {
			for key, want := range erasureWire(t, w.Has) {
				if !reflect.DeepEqual(got[key], want) {
					t.Errorf("%s: audit %+v has meta %q = %v, want %v (meta %v)", name, k, key, got[key], want, got)
				}
			}
		}
	}
	for k := range wantAudit {
		if !seenAudit[k] {
			t.Errorf("%s: expected audit row %+v was not written", name, k)
		}
	}

	// Notices: Gone's are gone, the tasks the step closes are resolved, nothing else moves.
	closed := map[uint]agesRaceTaskWant{}
	for _, tw := range st.Tasks {
		var open []models.Notification
		for _, n := range before.Notices {
			if n.UserID == tw.Owner && n.Kind == tw.Kind && n.SubjectID != nil && *n.SubjectID == tw.ID && n.Waiting && n.ResolvedAt == nil {
				open = append(open, n)
			}
		}
		if len(open) != 1 {
			t.Fatalf("%s: %d open %s tasks for user %d on subject %d before the step, want 1", name, len(open), tw.Kind, tw.Owner, tw.ID)
		}
		closed[open[0].ID] = tw
	}
	for id, b := range before.Notices {
		a, kept := after.Notices[id]
		tw, isTask := closed[id]
		switch {
		case st.Gone != 0 && b.UserID == st.Gone:
			if kept {
				t.Errorf("%s: notice %d (%s) of the erased person survives", name, id, b.Kind)
			}
		case !kept:
			t.Errorf("%s: notice %d (%s) of user %d is gone", name, id, b.Kind, b.UserID)
		case isTask:
			if a.ResolvedAt == nil || !inside(*a.ResolvedAt) || !erasureSameID(a.ResolvedByID, tw.By) || a.Resolution != tw.Resolution {
				t.Errorf("%s: task %d (%s) of user %d resolved at %v by %v as %q, want inside the step, by %v, as %q",
					name, id, b.Kind, b.UserID, a.ResolvedAt, a.ResolvedByID, a.Resolution, tw.By, tw.Resolution)
			}
			want := b
			want.ResolvedAt, want.ResolvedByID, want.Resolution = a.ResolvedAt, tw.By, tw.Resolution
			if erasureJSON(t, a) != erasureJSON(t, want) {
				t.Errorf("%s: task %d changed beyond its resolution:\n%s\n%s", name, id, erasureJSON(t, b), erasureJSON(t, a))
			}
		case erasureJSON(t, a) != erasureJSON(t, b):
			t.Errorf("%s: notice %d (%s) of user %d changed:\n%s\n%s", name, id, b.Kind, b.UserID, erasureJSON(t, b), erasureJSON(t, a))
		}
	}
	type noticeKey struct {
		To     uint
		Kind   string
		ID     uint
		Dedupe string
	}
	wantNotices := map[noticeKey]agesRaceNoticeWant{}
	for _, w := range st.Notices {
		k := noticeKey{w.To, w.Kind, w.ID, w.Dedupe}
		if _, dup := wantNotices[k]; dup {
			t.Fatalf("%s: the step expects two notices %+v", name, k)
		}
		wantNotices[k] = w
	}
	matched := map[uint]models.Notification{}
	for id, n := range after.Notices {
		if _, old := before.Notices[id]; old {
			continue
		}
		if st.Gone != 0 && n.UserID == st.Gone {
			t.Errorf("%s: the erased person holds new notice %d (%s)", name, id, n.Kind)
			continue
		}
		var sid uint
		if n.SubjectID != nil {
			sid = *n.SubjectID
		}
		dedupe := ""
		if n.DedupeKey != nil {
			dedupe = *n.DedupeKey
		}
		k := noticeKey{n.UserID, n.Kind, sid, dedupe}
		w, ok := wantNotices[k]
		if !ok {
			if !st.Partial {
				t.Errorf("%s: unexpected new notice %d: %s for user %d, key %q, subject %s %d", name, id, n.Kind, n.UserID, dedupe, n.SubjectType, sid)
			}
			continue
		}
		delete(wantNotices, k)
		matched[id] = n
		if n.SubjectType != w.Type {
			t.Errorf("%s: notice %d has subject type %q, want %q", name, id, n.SubjectType, w.Type)
		}
		if !inside(n.CreatedAt) {
			t.Errorf("%s: notice %d created %v, outside the step (%v to %v)", name, id, n.CreatedAt, st.Span.Start, st.Span.End)
		}
	}
	for k := range wantNotices {
		t.Errorf("%s: expected notice %+v was not written", name, k)
	}

	// Deliveries.
	for id, b := range before.Deliveries {
		a, kept := after.Deliveries[id]
		switch {
		case st.Gone != 0 && b.UserID == st.Gone:
			if kept {
				t.Errorf("%s: delivery %d of the erased person survives", name, id)
			}
		case !kept || erasureJSON(t, a) != erasureJSON(t, b):
			t.Errorf("%s: delivery %d of user %d changed or is gone", name, id, b.UserID)
		}
	}
	newDeliveries := map[uint]models.NotificationDelivery{}
	for id, d := range after.Deliveries {
		if _, old := before.Deliveries[id]; old {
			continue
		}
		if st.Gone != 0 && d.UserID == st.Gone {
			t.Errorf("%s: the erased person holds new delivery %d", name, id)
			continue
		}
		if _, dup := newDeliveries[d.NotificationID]; dup {
			t.Errorf("%s: notice %d has two new deliveries", name, d.NotificationID)
		}
		newDeliveries[d.NotificationID] = d
	}
	for id, n := range matched {
		d, ok := newDeliveries[id]
		if !ok {
			t.Errorf("%s: notice %d has no delivery row", name, id)
			continue
		}
		delete(newDeliveries, id)
		if d.Channel != models.DeliveryEmail || d.UserID != n.UserID || d.Status != models.DeliveryQueued || !inside(d.CreatedAt) || !inside(d.UpdatedAt) {
			t.Errorf("%s: delivery %d of notice %d is %s/%s for user %d written %v/%v; want a queued email for user %d inside the step",
				name, d.ID, id, d.Channel, d.Status, d.UserID, d.CreatedAt, d.UpdatedAt, n.UserID)
		}
	}
	if !st.Partial {
		for id, d := range newDeliveries {
			t.Errorf("%s: unexpected new delivery for notice %d (user %d)", name, id, d.UserID)
		}
	}

	// Jobs.
	for id, b := range before.Jobs {
		if a, ok := after.Jobs[id]; !ok || erasureJSON(t, a) != erasureJSON(t, b) {
			t.Errorf("%s: job %d (%s) changed or is gone", name, id, b.Kind)
		}
	}
	queuedJob := func(j models.Job, maxAttempts int) {
		t.Helper()
		if j.Status != models.JobQueued || j.Attempts != 0 || j.MaxAttempts != maxAttempts || j.LockedAt != nil || j.LockedBy != "" || j.LastError != "" ||
			!inside(j.RunAt) || !inside(j.CreatedAt) {
			t.Errorf("%s: job %d (%s) is %s with %d attempts of %d, run at %v, created %v; want queued, no attempts, max %d, inside the step",
				name, j.ID, j.Kind, j.Status, j.Attempts, j.MaxAttempts, j.RunAt, j.CreatedAt, maxAttempts)
		}
	}
	deliverJobs := map[string]models.Job{}
	var cleanups []models.Job
	for id, j := range after.Jobs {
		if _, old := before.Jobs[id]; old {
			continue
		}
		switch {
		case j.Kind == notify.DeliverKind && j.UniqueKey != nil:
			deliverJobs[*j.UniqueKey] = j
		case j.Kind == kindAvatarCleanup:
			cleanups = append(cleanups, j)
		default:
			if !st.Partial {
				t.Errorf("%s: unexpected new job %d (%s) %s", name, id, j.Kind, j.Payload)
			}
		}
	}
	for id := range matched {
		key := notify.DeliverKey(id)
		j, ok := deliverJobs[key]
		if !ok {
			t.Errorf("%s: notice %d has no delivery job", name, id)
			continue
		}
		delete(deliverJobs, key)
		queuedJob(j, 8)
		if got, want := erasureMetaOf(t, j.Payload), map[string]any{"notificationId": float64(id)}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: job %d payload %v, want %v", name, j.ID, got, want)
		}
	}
	if !st.Partial {
		for key, j := range deliverJobs {
			t.Errorf("%s: unexpected delivery job %s (%s)", name, key, j.Payload)
		}
	}
	if len(cleanups) != st.Cleanup {
		t.Errorf("%s: %d new avatar cleanup jobs, want %d", name, len(cleanups), st.Cleanup)
	}
	for _, j := range cleanups {
		queuedJob(j, jobs.DefaultMaxAttempts)
		if got, want := erasureMetaOf(t, j.Payload), map[string]any{"userId": float64(st.Gone)}; !reflect.DeepEqual(got, want) || j.UniqueKey != nil {
			t.Errorf("%s: cleanup job %d payload %v key %v, want %v and none", name, j.ID, got, j.UniqueKey, want)
		}
	}
}

func agesRacePID(v *atomic.Int64) int { return int(v.Load()) }

// TestAgesNoteErasureRaceDecisionThenErasure is CA-1. The subject's approval
// holds the gate and has not written; the erasure waits behind it (advisory
// evidence, blocked only by the decision's backend). The approval grants and
// commits, the erasure then scrubs the granted row through the completion
// exception and commits. The request is exactly as the decision left it but for
// the note, and the registration still names the request.
//
// The access is judged against fixed rules, not against the twin: as the
// approval leaves it (agesRaceGrantedAccess) and, after the erasure, the same
// columns with only the ending changed (agesRaceEndedAccess). The twin, whose
// note was empty and which no race touched (the pre-T07 result), is held to the
// same rules with its own ids and times and must also match the race by kind.
// The erasure's audit, notice, delivery and job rows are judged in full.
func TestAgesNoteErasureRaceDecisionThenErasure(t *testing.T) {
	w := newAgesRaceWorld(t)
	f, db := w.f, w.db
	u, leader := f.outsider, f.leader

	// The control: the same approval and an erasure with no note and no race.
	twin := newTestUser(t, w.app, "ages-twin", erasurePW, models.RoleMember)
	twinClient := loginAPI(t, w.app, "ages-twin", erasurePW)
	twinClient.holdNotices = true
	tp := w.request(leader.ID, twin.ID, policy.ContactPending, "")
	twinApproval := erasureSpan{Start: time.Now().UTC()}
	agesRaceOK(t, w.decide(twinClient, tp.ID), "twin approval")
	twinApproval.End = time.Now().UTC()
	var twinGranted models.Registration
	if err := db.Where("user_id = ? AND occurrence_id = ?", twin.ID, w.occ.ID).First(&twinGranted).Error; err != nil {
		t.Fatalf("the twin's approval left no registration: %v", err)
	}
	agesRaceGrantedAccess(t, "twin", twinGranted, leader.ID, tp.ID, twinApproval)
	twinErasure := erasureSpan{Start: time.Now().UTC()}
	agesRaceOK(t, w.erase(f.ac, twin.ID), "twin erasure")
	twinErasure.End = time.Now().UTC()
	var twinEnded models.Registration
	if err := db.First(&twinEnded, twinGranted.ID).Error; err != nil {
		t.Fatal(err)
	}
	agesRaceEndedAccess(t, "twin", twinGranted, twinEnded, twinErasure)

	p := w.request(leader.ID, u.ID, policy.ContactPending, agesRaceNote)
	w.seedTasks(p)
	base := erasureLedgerOf(t, db)

	var decisionPID, erasurePID atomic.Int64
	decisionReached, releaseDecision := w.run.pause(erasureTestPoint{Processor: "update", Step: "gorm:update",
		Table: "contact_requests", PID: &decisionPID})
	hold := erasureTestBeforeNotificationsDelete(u.ID)
	hold.PID = &erasurePID
	erasureReached, releaseErasure := w.run.pause(hold)

	approval := erasureSpan{Start: time.Now().UTC()}
	dec := w.decide(f.oc, p.ID)
	awaitDBSignal(t, decisionReached) // the approval holds the gate and has written nothing
	requireContactGate(t, db, agesRacePID(&decisionPID), true, "the approval")
	erasure := erasureSpan{Start: time.Now().UTC()}
	del := w.erase(f.ac, u.ID)
	waiter := agesRaceGateWaiter(t, db, agesRacePID(&decisionPID), "the erasure")
	if n := countRows(t, db, "users", "id = ? AND deleted_at IS NOT NULL", u.ID); n != 0 {
		t.Fatal("the erasure soft-deleted the subject while the approval held the gate")
	}
	releaseDecision()
	agesRaceOK(t, dec, "approval")
	approval.End = time.Now().UTC()
	awaitDBSignal(t, erasureReached) // the approval has committed; the erasure holds the gate and has not scrubbed
	if waiter != agesRacePID(&erasurePID) {
		t.Errorf("the gate waiter was backend %d, the erasure runs on %d", waiter, erasurePID.Load())
	}
	left := w.rows()[p.ID]
	atGrant := erasureLedgerOf(t, db)
	var granted models.ContactRequest
	if err := db.First(&granted, p.ID).Error; err != nil {
		t.Fatal(err)
	}
	var reg models.Registration
	if err := db.Where("user_id = ? AND occurrence_id = ?", u.ID, w.occ.ID).First(&reg).Error; err != nil {
		t.Fatalf("the approval left no registration: %v", err)
	}
	if granted.Status != policy.ContactGranted || granted.CompletedTargetID == nil || *granted.CompletedTargetID != reg.ID ||
		reg.AccessRequestID == nil || *reg.AccessRequestID != p.ID {
		t.Fatalf("the approval did not grant: request %q target %v, registration names %v", granted.Status, granted.CompletedTargetID, reg.AccessRequestID)
	}
	agesRaceGrantedAccess(t, "race", reg, leader.ID, p.ID, approval)
	if left.Note != agesRaceNote {
		t.Errorf("the note is %q before the scrub, want %q", left.Note, agesRaceNote)
	}
	// What the approval itself wrote. The contact suites judge it in full; this
	// race needs these rows and notices to exist, and the task closed.
	agesRaceLedger(t, agesRaceStep{Name: "the approval", Partial: true,
		Audit: []agesRaceAuditWant{
			{Type: "add_request", ID: p.ID, Action: audit.AddDecision, Actor: &u.ID, AnyIP: true, Has: map[string]any{
				"purpose": policy.RequestOccurrenceAdd, "requesterId": leader.ID, "subjectUserId": u.ID, "occurrenceId": w.occ.ID,
				"reason": policy.ContactSubject + ":" + policy.ContactApproved}},
			{Type: "add_request", ID: p.ID, Action: audit.AddCompleted, Actor: &u.ID, AnyIP: true, Has: map[string]any{
				"purpose": policy.RequestOccurrenceAdd, "requesterId": leader.ID, "subjectUserId": u.ID, "occurrenceId": w.occ.ID,
				"outcome": policy.ContactGranted}},
			{Type: "event", ID: w.ev.ID, Action: audit.RegistrationAdded, Actor: &leader.ID, AnyIP: true, Has: map[string]any{
				"registrationId": reg.ID, "userId": u.ID, "newStatus": models.RegGoing}},
			{Type: accessTargetType, ID: reg.ID, Action: audit.OccurrenceAccessAdded, Actor: &leader.ID, AnyIP: true, Has: map[string]any{
				"subjectUserId": u.ID, "occurrenceId": w.occ.ID, "registrationId": reg.ID, "origin": policy.OccurrenceOriginManagerAdd,
				"issuerId": leader.ID, "requestId": p.ID, "grantedAtMicro": accessMicro(*reg.AccessGrantedAt)}},
		},
		Notices: []agesRaceNoticeWant{
			agesRaceUpdated(leader.ID, p, "decision:subject"),
			agesRaceUpdated(u.ID, p, "decision:subject"),
			agesRaceUpdated(leader.ID, p, "completed"),
			{To: u.ID, Kind: notify.OccurrenceAdded, Type: accessTargetType, ID: reg.ID, Dedupe: accessAddedKey(reg)},
		},
		Tasks: []agesRaceTaskWant{agesRaceTask(u.ID, p, false, policy.ContactApproved, &u.ID)},
	}, base, atGrant)

	releaseErasure()
	agesRaceOK(t, del, "erasure")
	erasure.End = time.Now().UTC()

	after := w.rows()[p.ID]
	agesRaceScrubbed(t, "granted request", left, after)
	var ended models.Registration
	if err := db.First(&ended, reg.ID).Error; err != nil {
		t.Fatal(err)
	}
	if ended.AccessRequestID == nil || *ended.AccessRequestID != p.ID {
		t.Errorf("the registration names request %v, want %d", ended.AccessRequestID, p.ID)
	}
	agesRaceEndedAccess(t, "race", reg, ended, erasure)
	if got, want := agesRaceAccessKind(ended, leader.ID, p.ID), agesRaceAccessKind(twinEnded, leader.ID, tp.ID); got != want {
		t.Errorf("the registration's access after the race (%s) is not of the kind the twin's erasure leaves (%s)", got, want)
	}
	// The erasure finishes the access: its snapshot, its one extra audit row and
	// what it removes are all that it adds.
	agesRaceLedger(t, agesRaceStep{Name: "the erasure", Span: erasure, Gone: u.ID, Cleanup: 1,
		Audit: []agesRaceAuditWant{
			agesRaceDeleted(f.admin.ID, u.ID),
			{Type: accessTargetType, ID: reg.ID, Action: audit.OccurrenceAccessEnded, Meta: map[string]any{
				"subjectUserId": u.ID, "occurrenceId": w.occ.ID, "eventId": w.ev.ID,
				"organizerType": policy.ContactGroup, "organizerId": f.group.ID, "registrationId": reg.ID,
				"origin": policy.OccurrenceOriginManagerAdd, "grantedAtMicro": accessMicro(*reg.AccessGrantedAt),
				"issuerId": leader.ID, "requestId": p.ID,
				"endedAtMicro": accessMicro(*ended.AccessEndedAt), "endReason": policy.ContactAccountDisabled, "outcome": policy.OccurrenceAccessEnded}},
		},
	}, atGrant, erasureLedgerOf(t, db))
	agesRaceStates(t, w.states, nil)
	t.Logf("approval backend %d, erasure backend %d, gate waiter %d; request %q", decisionPID.Load(), erasurePID.Load(), waiter, granted.Status)
}

// TestAgesNoteErasureRaceErasureThenDecision is CA-2. The erasure holds the
// gate with its scrub done and uncommitted. The subject's approval (authorized
// before the erasure committed) waits behind it, then finds a deleted subject
// and is refused. The note was already empty when it ran, and the approval
// changed nothing on the request. The two together write exactly the erasure's
// audit row and ending of the request, the notice to the requester and the
// approval's refusal, and the subject's task goes with them.
func TestAgesNoteErasureRaceErasureThenDecision(t *testing.T) {
	w := newAgesRaceWorld(t)
	f, db := w.f, w.db
	u, leader := f.outsider, f.leader
	p := w.request(leader.ID, u.ID, policy.ContactPending, agesRaceNote)
	w.seedTasks(p)
	before := w.rows()
	l0 := erasureLedgerOf(t, db)

	var erasurePID atomic.Int64
	hold := agesRaceAfterScrub(u.ID, 1)
	hold.PID = &erasurePID
	reached, release := w.run.pause(hold)

	span := erasureSpan{Start: time.Now().UTC()}
	del := w.erase(f.ac, u.ID)
	awaitDBSignal(t, reached) // the scrub ran; nothing has committed
	requireContactGate(t, db, agesRacePID(&erasurePID), true, "the erasure")
	if got := w.rows()[p.ID].Note; got != agesRaceNote {
		t.Errorf("another session sees note %q before the erasure commits, want %q", got, agesRaceNote)
	}
	dec := w.decide(f.oc, p.ID)
	agesRaceGateWaiter(t, db, agesRacePID(&erasurePID), "the approval")
	release()
	agesRaceOK(t, del, "erasure")
	span.End = time.Now().UTC()
	r := awaitResult(t, dec)
	both := erasureSpan{Start: span.Start, End: time.Now().UTC()}
	if r.code < 400 || r.code >= 500 {
		t.Errorf("approval after the erasure: %d %s, want a refusal", r.code, r.body)
	}
	t.Logf("approval after the erasure: %d %s", r.code, r.body)

	after := w.rows()[p.ID]
	agesRaceEndedByErasure(t, "request", before[p.ID], after, span)
	if after.Note != "" {
		t.Errorf("note %q after the erasure", after.Note)
	}
	if n := countRows(t, db, "registrations", "access_request_id = ?", p.ID); n != 0 {
		t.Errorf("%d registrations name the request, want none", n)
	}
	agesRaceLedger(t, agesRaceStep{Name: "the erasure and the refused approval", Span: both, Gone: u.ID, Cleanup: 1,
		Audit: []agesRaceAuditWant{
			agesRaceDeleted(f.admin.ID, u.ID),
			agesRaceEndRow(p, policy.ContactEnded, policy.ContactAccountDisabled),
			// The deleted subject has no live account behind the gate: the
			// approval is refused as a stale session and recorded, with the
			// subject as its actor.
			{Type: "add_request", ID: 0, Action: audit.AddRefused, Actor: &u.ID, AnyIP: true, Has: map[string]any{"reason": "stale_session"}},
		},
		Notices: []agesRaceNoticeWant{agesRaceUpdated(leader.ID, p, policy.ContactEnded)},
	}, l0, erasureLedgerOf(t, db))
	agesRaceStates(t, w.states, nil)
}

// agesRaceExpiry is CA-3: one erasure against the expiry sweep. T is the erased
// subject's pending request, H their terminal one, X an unrelated pending
// request that has already expired. order is "erasure-first", "sweep-first" or
// "crossing" (sweep first, with T's expiry between the two clocks).
//
// T's fate is fixed by the case: ended by the removal of its subject when it has
// not expired at the erasure's clock, expired when (in the crossing case) its
// expiry is after the sweep's clock and before the erasure's. The sweep's rows
// (X expired, a notice to both parties, X's task closed) and the erasure's (T
// finished, the notice to the requester, T's task removed with the subject) are
// each judged in full.
func agesRaceExpiry(t *testing.T, order string) {
	w := newAgesRaceWorld(t)
	f, db := w.f, w.db
	u, other, leader := f.outsider, f.subject, f.leader
	crossing := order == "crossing"
	sweepNow := time.Now().UTC().Truncate(time.Microsecond)
	var tReq models.ContactRequest
	if crossing {
		// Expired at the erasure's clock, not at the sweep's.
		exp := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
		tReq = w.request(leader.ID, u.ID, policy.ContactPending, agesRaceNote, func(r *models.ContactRequest) {
			r.ExpiresAt, r.CreatedAt = exp, exp.Add(-14*24*time.Hour)
		})
		sweepNow = exp.Add(-time.Minute)
		if !sweepNow.Before(tReq.ExpiresAt) {
			t.Fatalf("the sweep's clock %v is not before T's expiry %v", sweepNow, tReq.ExpiresAt)
		}
	} else {
		tReq = w.request(leader.ID, u.ID, policy.ContactPending, agesRaceNote)
	}
	hReq := w.request(leader.ID, u.ID, policy.ContactWithdrawn, agesRaceNote)
	xReq := w.request(leader.ID, other.ID, policy.ContactPending, "unrelated note", agesRaceExpired)
	w.seedTasks(tReq)
	w.seedTasks(xReq)
	before := w.rows()
	l0 := erasureLedgerOf(t, db)

	// The fixed fate of T under the erasure.
	tStatus, tReason := policy.ContactEnded, policy.ContactAccountDisabled
	if crossing {
		tStatus, tReason = policy.ContactExpired, ""
	}
	// The rows the sweep writes for X, and the rows the erasure writes for T.
	sweepStep := func(span erasureSpan) agesRaceStep {
		return agesRaceStep{Name: "the sweep", Span: span,
			Audit: []agesRaceAuditWant{agesRaceEndRow(xReq, policy.ContactExpired, "")},
			Notices: []agesRaceNoticeWant{
				agesRaceUpdated(leader.ID, xReq, policy.ContactExpired),
				agesRaceUpdated(other.ID, xReq, policy.ContactExpired),
			},
			Tasks: []agesRaceTaskWant{agesRaceTask(other.ID, xReq, false, policy.ContactExpired, nil)},
		}
	}
	erasureStep := func(span erasureSpan) agesRaceStep {
		return agesRaceStep{Name: "the erasure", Span: span, Gone: u.ID, Cleanup: 1,
			Audit:   []agesRaceAuditWant{agesRaceDeleted(f.admin.ID, u.ID), agesRaceEndRow(tReq, tStatus, tReason)},
			Notices: []agesRaceNoticeWant{agesRaceUpdated(leader.ID, tReq, tStatus)},
		}
	}

	var erasurePID atomic.Int64
	hold := agesRaceAfterScrub(u.ID, 2)
	hold.PID = &erasurePID
	erasureReached, releaseErasure := w.run.pause(hold)

	var span erasureSpan
	var del <-chan httpResult
	var mid map[uint]erasureAgesNoteRow
	var l1, l2 erasureLedger
	var laterSpan erasureSpan
	switch order {
	case "erasure-first":
		// The sweep's try-lock is observed with its result in hand and its
		// transaction still open: it holds no gate and waits for none.
		sweepPID := w.run.probe(erasureTestPoint{Processor: "row", Step: "gorm:row", SQLContains: "pg_try_advisory_xact_lock"})
		sweepReached, releaseSweep := w.run.pause(erasureTestPoint{Processor: "row", After: true, Step: "gorm:row", SQLContains: "pg_try_advisory_xact_lock"})
		span.Start = time.Now().UTC()
		del = w.erase(f.ac, u.ID)
		awaitDBSignal(t, erasureReached)
		requireContactGate(t, db, agesRacePID(&erasurePID), true, "the erasure")
		sweepDone := w.run.sweep(w.app, sweepNow)
		awaitDBSignal(t, sweepReached)
		sp := agesRacePID(sweepPID)
		if sp == 0 || sp == agesRacePID(&erasurePID) {
			t.Fatalf("the sweep ran on backend %d, the erasure on %d", sp, erasurePID.Load())
		}
		var advisory, blockers int64
		if err := db.Raw("SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND pid = ?", sp).Scan(&advisory).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Raw("SELECT cardinality(pg_blocking_pids(?))", sp).Scan(&blockers).Error; err != nil {
			t.Fatal(err)
		}
		if advisory != 0 || blockers != 0 {
			t.Errorf("the sweep holds or waits for %d advisory locks and is blocked by %d backends, want none of either", advisory, blockers)
		}
		agesRaceNoGateWaiter(t, db)
		releaseSweep()
		if err := agesRaceAwait(t, sweepDone, "the sweep"); err != nil {
			t.Fatalf("sweepContacts: %v", err)
		}
		// It committed having changed nothing, while the erasure still holds the gate.
		unchanged := w.rows()
		for id, b := range before {
			agesRaceSame(t, fmt.Sprintf("request %d after the sweep that found the gate taken", id), b, unchanged[id])
		}
		agesRaceLedger(t, agesRaceStep{Name: "the sweep that found the gate taken"}, l0, erasureLedgerOf(t, db))
		releaseErasure()
		agesRaceOK(t, del, "erasure")
		span.End = time.Now().UTC()
		l1 = erasureLedgerOf(t, db)
	default: // sweep-first, crossing
		sweepSpan := erasureSpan{Start: time.Now().UTC()}
		sweepPID, sweepDone, releaseSweep := w.holdSweep(sweepNow)
		requireContactGate(t, db, sweepPID, true, "the sweep")
		span.Start = time.Now().UTC()
		del = w.erase(f.ac, u.ID)
		waiter := agesRaceGateWaiter(t, db, sweepPID, "the erasure")
		if n := countRows(t, db, "users", "id = ? AND deleted_at IS NOT NULL", u.ID); n != 0 {
			t.Fatal("the erasure soft-deleted the subject while the sweep held the gate")
		}
		releaseSweep()
		if err := awaitDBResult(t, sweepDone); err != nil {
			t.Fatalf("model sweep: %v", err)
		}
		sweepSpan.End = time.Now().UTC()
		awaitDBSignal(t, erasureReached) // the sweep has committed; the erasure has scrubbed and holds the gate
		if waiter != agesRacePID(&erasurePID) {
			t.Errorf("the gate waiter was backend %d, the erasure runs on %d", waiter, erasurePID.Load())
		}
		mid = w.rows()
		x := mid[xReq.ID]
		if x.Status != policy.ContactExpired || x.EndReason != "" || x.CompletedAt == nil || !x.CompletedAt.Equal(sweepNow) {
			t.Errorf("X after the sweep: status %q end_reason %q completed_at %v, want expired, none and %v", x.Status, x.EndReason, x.CompletedAt, sweepNow)
		}
		if x.Preserved != before[xReq.ID].Preserved || x.Note != before[xReq.ID].Note {
			t.Errorf("the sweep changed more than X's lifecycle columns")
		}
		agesRaceSame(t, "T after the sweep (eligible, not expired at the sweep's clock)", before[tReq.ID], mid[tReq.ID])
		agesRaceSame(t, "H after the sweep", before[hReq.ID], mid[hReq.ID])
		l1 = erasureLedgerOf(t, db)
		agesRaceLedger(t, sweepStep(sweepSpan), l0, l1)
		releaseErasure()
		agesRaceOK(t, del, "erasure")
		span.End = time.Now().UTC()
		l2 = erasureLedgerOf(t, db)
		if crossing && !tReq.ExpiresAt.Before(span.Start) {
			t.Fatalf("T's expiry %v is not before the erasure began (%v)", tReq.ExpiresAt, span.Start)
		}
	}

	after := w.rows()
	// H is terminal: only the note goes.
	agesRaceScrubbed(t, "H", before[hReq.ID], after[hReq.ID])
	if h := after[hReq.ID]; h.Status != before[hReq.ID].Status || h.EndReason != before[hReq.ID].EndReason {
		t.Errorf("H's lifecycle changed: %s/%s", h.Status, h.EndReason)
	}
	// T is finished by the erasure, at the erasure's clock: expired when the
	// case puts its expiry before that clock, otherwise ended by the removal of
	// its subject.
	tt := after[tReq.ID]
	if tt.Note != "" || tt.Reason != before[tReq.ID].Reason || tt.Status != tStatus || tt.EndReason != tReason ||
		!span.hasPtr(tt.CompletedAt) || tt.Preserved != before[tReq.ID].Preserved {
		t.Errorf("T: note %q status %q end_reason %q completed_at %v, want empty, %q, %q inside %v..%v with every other column unchanged",
			tt.Note, tt.Status, tt.EndReason, tt.CompletedAt, tStatus, tReason, span.Start, span.End)
	}
	// X: still pending after an erasure that never visits it, and unchanged by
	// it when the sweep had not run yet.
	x := after[xReq.ID]
	if order == "erasure-first" {
		agesRaceSame(t, "X after the erasure", before[xReq.ID], x)
		agesRaceLedger(t, erasureStep(span), l0, l1)
		later := time.Now().UTC().Truncate(time.Microsecond)
		laterSpan.Start = time.Now().UTC()
		if err := agesRaceAwait(t, w.run.sweep(w.app, later), "the later sweep"); err != nil {
			t.Fatalf("later sweep: %v", err)
		}
		laterSpan.End = time.Now().UTC()
		x = w.rows()[xReq.ID]
		if x.Status != policy.ContactExpired || x.EndReason != "" || x.CompletedAt == nil || !x.CompletedAt.Equal(later) {
			t.Errorf("X after a later sweep: status %q end_reason %q completed_at %v, want expired, none and %v", x.Status, x.EndReason, x.CompletedAt, later)
		}
		agesRaceLedger(t, sweepStep(laterSpan), l1, erasureLedgerOf(t, db))
	} else {
		agesRaceSame(t, "X after the erasure that followed the sweep", mid[xReq.ID], x)
		agesRaceLedger(t, erasureStep(span), l1, l2)
	}
	agesRaceStates(t, w.states, nil)
}

func TestAgesNoteErasureRaceErasureThenSweep(t *testing.T) { agesRaceExpiry(t, "erasure-first") }
func TestAgesNoteErasureRaceSweepThenErasure(t *testing.T) { agesRaceExpiry(t, "sweep-first") }
func TestAgesNoteErasureRaceSweepThenErasureCrossingExpiry(t *testing.T) {
	agesRaceExpiry(t, "crossing")
}

// agesRaceTwoErasures is CA-4: the requester's erasure and the subject's share
// one request and serialize on the gate, in either order. Both hold at the
// notifications delete, which comes before the scrub, so the state between the
// two commits can be read. The first erasure ends the request and tells the
// person still there (closing their task unless it is theirs); the second
// removes that person's rows and adds only its own deletion.
func agesRaceTwoErasures(t *testing.T, subjectFirst bool) {
	w := newAgesRaceWorld(t)
	f, db := w.f, w.db
	asker := newTestUser(t, w.app, "ages-asker", erasurePW, models.RoleMember)
	subject := newTestUser(t, w.app, "ages-asked", erasurePW, models.RoleMember)
	admin2 := newTestUser(t, w.app, "ages-admin-2", erasurePW, models.RoleAdmin)
	ac2 := loginAPI(t, w.app, "ages-admin-2", erasurePW)
	ac2.holdNotices = true
	r := w.request(asker.ID, subject.ID, policy.ContactPending, agesRaceNote)
	w.seedTasks(r)
	before := w.rows()
	l0 := erasureLedgerOf(t, db)

	var askerPID, subjectPID atomic.Int64
	pa := erasureTestBeforeNotificationsDelete(asker.ID)
	pa.PID = &askerPID
	askerReached, releaseAsker := w.run.pause(pa)
	ps := erasureTestBeforeNotificationsDelete(subject.ID)
	ps.PID = &subjectPID
	subjectReached, releaseSubject := w.run.pause(ps)

	firstReached, releaseFirst, firstPID := askerReached, releaseAsker, &askerPID
	secondReached, releaseSecond, secondPID := subjectReached, releaseSubject, &subjectPID
	firstID, secondID := asker.ID, subject.ID
	// The first erasure leaves the other person; their task closes if they are
	// the subject, and goes with them if the subject is the one erased.
	livingID := subject.ID
	firstTasks := []agesRaceTaskWant{agesRaceTask(subject.ID, r, false, policy.ContactEnded, nil)}
	if subjectFirst {
		firstReached, releaseFirst, firstPID, secondReached, releaseSecond, secondPID =
			subjectReached, releaseSubject, &subjectPID, askerReached, releaseAsker, &askerPID
		firstID, secondID = subject.ID, asker.ID
		livingID, firstTasks = asker.ID, nil
	}
	span := erasureSpan{Start: time.Now().UTC()}
	first := w.erase(f.ac, firstID)
	awaitDBSignal(t, firstReached)
	requireContactGate(t, db, agesRacePID(firstPID), true, "the first erasure")
	secondSpan := erasureSpan{Start: time.Now().UTC()}
	second := w.erase(ac2, secondID)
	waiter := agesRaceGateWaiter(t, db, agesRacePID(firstPID), "the second erasure")
	if n := countRows(t, db, "users", "id = ? AND deleted_at IS NOT NULL", secondID); n != 0 {
		t.Fatal("the second erasure deleted its person while the first held the gate")
	}
	if got := w.rows()[r.ID]; got.Whole != before[r.ID].Whole {
		t.Fatal("the request changed before the first erasure released the gate")
	}
	releaseFirst()
	agesRaceOK(t, first, "first erasure")
	firstSpan := erasureSpan{Start: span.Start, End: time.Now().UTC()}
	awaitDBSignal(t, secondReached) // the first has committed; the second holds the gate and has not scrubbed
	if waiter != agesRacePID(secondPID) {
		t.Errorf("the gate waiter was backend %d, the second erasure runs on %d", waiter, secondPID.Load())
	}
	// Between the two commits the request is ended by whoever went first; the
	// note is the scrub's alone, so it is gone only if the subject went first.
	mid := w.rows()[r.ID]
	if mid.Status != policy.ContactEnded || mid.EndReason != policy.ContactAccountDisabled || mid.Reason != before[r.ID].Reason {
		t.Errorf("between the commits: status %q end_reason %q reason %q", mid.Status, mid.EndReason, mid.Reason)
	}
	wantMidNote := agesRaceNote
	if subjectFirst {
		wantMidNote = ""
	}
	if mid.Note != wantMidNote {
		t.Errorf("between the commits the note is %q, want %q (subject first: %t)", mid.Note, wantMidNote, subjectFirst)
	}
	l1 := erasureLedgerOf(t, db)
	agesRaceLedger(t, agesRaceStep{Name: "the first erasure", Span: firstSpan, Gone: firstID, Cleanup: 1, Tasks: firstTasks,
		Audit: []agesRaceAuditWant{
			agesRaceDeleted(f.admin.ID, firstID),
			agesRaceEndRow(r, policy.ContactEnded, policy.ContactAccountDisabled),
		},
		Notices: []agesRaceNoticeWant{agesRaceUpdated(livingID, r, policy.ContactEnded)},
	}, l0, l1)
	releaseSecond()
	agesRaceOK(t, second, "second erasure")
	span.End = time.Now().UTC()
	secondSpan.End = span.End

	after := w.rows()[r.ID]
	agesRaceEndedByErasure(t, "shared request", before[r.ID], after, span)
	if after.Rest != mid.Rest {
		t.Errorf("the second erasure changed more than the note:\n%s\n%s", mid.Rest, after.Rest)
	}
	// The second erasure ends nothing again: it adds its own deletion, removes
	// its person's rows (the notice the first sent them included) and queues its
	// own avatar cleanup.
	agesRaceLedger(t, agesRaceStep{Name: "the second erasure", Span: secondSpan, Gone: secondID, Cleanup: 1,
		Audit: []agesRaceAuditWant{agesRaceDeleted(admin2.ID, secondID)},
	}, l1, erasureLedgerOf(t, db))
	agesRaceStates(t, w.states, nil)
}

func TestAgesNoteErasureRaceRequesterThenSubjectErasure(t *testing.T) { agesRaceTwoErasures(t, false) }
func TestAgesNoteErasureRaceSubjectThenRequesterErasure(t *testing.T) { agesRaceTwoErasures(t, true) }

// agesRaceRemoval is the removal half of CA-5: the leader removes the subject's
// registration on the request's date, in either order against the subject's
// erasure.
//
// Removal first writes the usual rows: registration.removed with its actor and
// meta, add.ended ("removed") for the request, the notices to the requester and
// to the subject (the update and the removal), and closes the subject's task.
// The erasure that follows ends nothing again. Erasure first finishes the
// request itself; the removal, whatever it answers, leaves no row at all.
func agesRaceRemoval(t *testing.T, removalFirst bool) {
	w := newAgesRaceWorld(t)
	f, db := w.f, w.db
	u, leader := f.outsider, f.leader
	uid := u.ID
	reg := models.Registration{UserID: &uid, OccurrenceID: w.occ.ID, Status: models.RegGoing, Source: models.RegSourceWeb,
		AttendanceMode: models.AttendanceInPerson}
	erasureMustCreate(t, db, &reg)
	p := w.request(leader.ID, u.ID, policy.ContactPending, agesRaceNote)
	w.seedTasks(p)
	before := w.rows()
	l0 := erasureLedgerOf(t, db)

	var erasurePID, removalPID atomic.Int64
	if removalFirst {
		// The removal holds the gate and has not written; the erasure is held
		// after the removal commits so the row can be read as the removal left it.
		reachedRemoval, releaseRemoval := w.run.pause(erasureTestPoint{Processor: "update", Step: "gorm:update",
			Table: "registrations", Assign: map[string]any{"status": models.RegRemoved}, PID: &removalPID})
		hold := erasureTestBeforeNotificationsDelete(u.ID)
		hold.PID = &erasurePID
		reachedErasure, releaseErasure := w.run.pause(hold)
		removal := erasureSpan{Start: time.Now().UTC()}
		rem := w.remove(reg.ID)
		awaitDBSignal(t, reachedRemoval)
		requireContactGate(t, db, agesRacePID(&removalPID), true, "the removal")
		erasure := erasureSpan{Start: time.Now().UTC()}
		del := w.erase(f.ac, u.ID)
		waiter := agesRaceGateWaiter(t, db, agesRacePID(&removalPID), "the erasure")
		releaseRemoval()
		agesRaceOK(t, rem, "removal")
		removal.End = time.Now().UTC()
		awaitDBSignal(t, reachedErasure)
		if waiter != agesRacePID(&erasurePID) {
			t.Errorf("the gate waiter was backend %d, the erasure runs on %d", waiter, erasurePID.Load())
		}
		left := w.rows()[p.ID]
		if left.Status != policy.ContactEnded || left.EndReason != policy.OccurrenceRemoved || !removal.hasPtr(left.CompletedAt) || left.Note != agesRaceNote {
			t.Errorf("request as the removal left it: status %q end_reason %q completed_at %v note %q, want ended, %q inside the removal and the note",
				left.Status, left.EndReason, left.CompletedAt, left.Note, policy.OccurrenceRemoved)
		}
		if left.Preserved != before[p.ID].Preserved || left.Reason != before[p.ID].Reason {
			t.Errorf("the removal changed a column other than the lifecycle's:\n%s\n%s", before[p.ID].Preserved, left.Preserved)
		}
		l1 := erasureLedgerOf(t, db)
		agesRaceLedger(t, agesRaceStep{Name: "the removal", Span: removal,
			Audit: []agesRaceAuditWant{
				{Type: "event", ID: w.ev.ID, Action: audit.RegistrationRemoved, Actor: &leader.ID, AnyIP: true, Has: map[string]any{
					"registrationId": reg.ID, "userId": u.ID, "occurrenceId": w.occ.ID,
					"oldStatus": models.RegGoing, "newStatus": models.RegRemoved, "note": "Moved away"}},
				agesRaceEndRow(p, policy.ContactEnded, policy.OccurrenceRemoved),
			},
			Notices: []agesRaceNoticeWant{
				agesRaceUpdated(leader.ID, p, policy.ContactEnded),
				agesRaceUpdated(u.ID, p, policy.ContactEnded),
				{To: u.ID, Kind: notify.RegistrationRemoved, Type: "occurrence", ID: w.occ.ID},
			},
			Tasks: []agesRaceTaskWant{agesRaceTask(u.ID, p, false, policy.ContactEnded, nil)},
		}, l0, l1)
		releaseErasure()
		agesRaceOK(t, del, "erasure")
		erasure.End = time.Now().UTC()

		// The erasure scrubs the ended row through the identity exception and
		// does not end it again: the completion branch is not entered and
		// reconcileContacts does not select it.
		agesRaceScrubbed(t, "request", left, w.rows()[p.ID])
		if n := auditCount(t, w.app, audit.RegistrationRemoved); n != 1 {
			t.Errorf("%d registration.removed rows, want 1", n)
		}
		agesRaceLedger(t, agesRaceStep{Name: "the erasure", Span: erasure, Gone: u.ID, Cleanup: 1,
			Audit: []agesRaceAuditWant{agesRaceDeleted(f.admin.ID, u.ID)},
		}, l1, erasureLedgerOf(t, db))
	} else {
		hold := agesRaceAfterScrub(u.ID, 1)
		hold.PID = &erasurePID
		reached, release := w.run.pause(hold)
		span := erasureSpan{Start: time.Now().UTC()}
		del := w.erase(f.ac, u.ID)
		awaitDBSignal(t, reached)
		requireContactGate(t, db, agesRacePID(&erasurePID), true, "the erasure")
		rem := w.remove(reg.ID)
		agesRaceGateWaiter(t, db, agesRacePID(&erasurePID), "the removal")
		release()
		agesRaceOK(t, del, "erasure")
		span.End = time.Now().UTC()
		// Whatever the removal answers for a deleted person's registration is
		// existing behavior; it is recorded and only its effect on the request
		// and the ledger is asserted.
		r := awaitResult(t, rem)
		both := erasureSpan{Start: span.Start, End: time.Now().UTC()}
		t.Logf("removal after the erasure: %d %s", r.code, r.body)

		after := w.rows()
		agesRaceEndedByErasure(t, "request", before[p.ID], after[p.ID], span)
		if after[p.ID].EndReason == policy.OccurrenceRemoved {
			t.Error("the removal ended a request the erasure had already ended")
		}
		for id, b := range before {
			if id != p.ID {
				agesRaceSame(t, fmt.Sprintf("request %d", id), b, after[id])
			}
		}
		agesRaceLedger(t, agesRaceStep{Name: "the erasure and the removal after it", Span: both, Gone: u.ID, Cleanup: 1,
			Audit: []agesRaceAuditWant{
				agesRaceDeleted(f.admin.ID, u.ID),
				agesRaceEndRow(p, policy.ContactEnded, policy.ContactAccountDisabled),
			},
			Notices: []agesRaceNoticeWant{agesRaceUpdated(leader.ID, p, policy.ContactEnded)},
		}, l0, erasureLedgerOf(t, db))
	}
	agesRaceStates(t, w.states, nil)
}

func TestAgesNoteErasureRaceRemovalThenErasure(t *testing.T) { agesRaceRemoval(t, true) }
func TestAgesNoteErasureRaceErasureThenRemoval(t *testing.T) { agesRaceRemoval(t, false) }

// agesRaceRolloverResult is what app.rolloverAges returned.
type agesRaceRolloverResult struct {
	n   int
	err error
}

// TestAgesNoteErasureRaceRolloverThenErasure is the first rollover order of
// CA-5. The subject is a teen past eighteen whose pending request needs an
// admin. rolloverAges first runs its preliminary sweep, which holds the gate and
// ends that request with policy.ContactTurned18 at the rollover's injected clock
// (the fixture is ended there, not by the per-user step; this test makes no
// claim that the per-user reconciliation ended a still-pending request). Its
// per-user transaction then holds the gate (taken in lockTeenLifecycleUsers)
// before it converts the teen; the erasure waits behind that, then scrubs.
//
// The request ends as ended/turned_18 at the injected clock, the reason and
// every column but the lifecycle's and the note as before, through the rollover
// and again through the erasure; an identical twin put through the same
// sequence is held to the same rules and must match. The rollover's rows
// (the request's end, the turned-18 audit row, the notices and the tasks it
// closes) and the erasure's are judged in full.
func TestAgesNoteErasureRaceRolloverThenErasure(t *testing.T) {
	w := newAgesRaceWorld(t)
	f, db := w.f, w.db
	u, leader := f.outsider, f.leader
	// The rollover's explicit clock, as the database stores it.
	now := time.Now().UTC().Truncate(time.Microsecond)

	twin := newTestUser(t, w.app, "ages-twin", erasurePW, models.RoleMember)
	w.makeTeen(twin)
	tp := w.request(leader.ID, twin.ID, policy.ContactPending, "", agesRaceNeedsAdmin)
	twinBefore := w.rows()[tp.ID]
	if n, err := w.app.rolloverAges(now); err != nil || n != 1 {
		t.Fatalf("twin rollover: %d changed, %v", n, err)
	}
	twinMid := w.rows()[tp.ID]
	agesRaceTurned18(t, "twin after the rollover", twinBefore, twinMid, now)
	agesRaceOK(t, w.erase(f.ac, twin.ID), "twin erasure")
	twinAfter := w.rows()[tp.ID]
	agesRaceTurned18(t, "twin after the erasure", twinBefore, twinAfter, now)

	w.makeTeen(u)
	p := w.request(leader.ID, u.ID, policy.ContactPending, agesRaceNote, agesRaceNeedsAdmin)
	w.seedTasks(p)
	before := w.rows()
	l0 := erasureLedgerOf(t, db)

	var rolloverPID, erasurePID atomic.Int64
	reachedRollover, releaseRollover := w.run.pause(erasureTestPoint{Processor: "update", Step: "gorm:update",
		Table: "users", Assign: map[string]any{"account_type": models.AccountAdult}, PID: &rolloverPID})
	hold := erasureTestBeforeNotificationsDelete(u.ID)
	hold.PID = &erasurePID
	reachedErasure, releaseErasure := w.run.pause(hold)

	rollover := erasureSpan{Start: time.Now().UTC()}
	done := w.run.rollover(w.app, now)
	awaitDBSignal(t, reachedRollover) // the per-user transaction holds the gate and has not converted the teen
	requireContactGate(t, db, agesRacePID(&rolloverPID), true, "the rollover")
	// The preliminary sweep has already committed the end of the request, and
	// the teen is still a teen: the request was not ended by the conversion.
	agesRaceTurned18(t, "request while the per-user transaction holds the gate", before[p.ID], w.rows()[p.ID], now)
	if n := countRows(t, db, "users", "id = ? AND account_type = ?", u.ID, models.AccountTeen); n != 1 {
		t.Fatalf("the subject is not still a teen while the per-user transaction holds the gate (%d rows)", n)
	}
	erasure := erasureSpan{Start: time.Now().UTC()}
	del := w.erase(f.ac, u.ID)
	waiter := agesRaceGateWaiter(t, db, agesRacePID(&rolloverPID), "the erasure")
	releaseRollover()
	if r := agesRaceAwait(t, done, "the rollover"); r.err != nil || r.n != 1 {
		t.Fatalf("rolloverAges = (%d, %v), want (1, nil)", r.n, r.err)
	}
	rollover.End = time.Now().UTC()
	awaitDBSignal(t, reachedErasure)
	if waiter != agesRacePID(&erasurePID) {
		t.Errorf("the gate waiter was backend %d, the erasure runs on %d", waiter, erasurePID.Load())
	}
	mid := w.rows()[p.ID]
	var teen models.User
	if err := db.First(&teen, u.ID).Error; err != nil || teen.AccountType != models.AccountAdult {
		t.Fatalf("after the rollover the subject is %q (%v), want adult", teen.AccountType, err)
	}
	agesRaceTurned18(t, "request as the rollover left it", before[p.ID], mid, now)
	if mid.Note != agesRaceNote {
		t.Errorf("the note is %q after the rollover, want %q", mid.Note, agesRaceNote)
	}
	l1 := erasureLedgerOf(t, db)
	agesRaceLedger(t, agesRaceStep{Name: "the rollover", Span: rollover,
		Audit: []agesRaceAuditWant{
			agesRaceEndRow(p, policy.ContactEnded, policy.ContactTurned18),
			{Type: "user", ID: u.ID, Action: audit.UserTurned18, Meta: map[string]any{
				"previousStatus": models.UserActive, "status": models.UserActive, "churchDate": now.In(churchZone(db)).Format("2006-01-02")}},
		},
		Notices: []agesRaceNoticeWant{
			agesRaceUpdated(leader.ID, p, policy.ContactEnded),
			agesRaceUpdated(u.ID, p, policy.ContactEnded),
			{To: u.ID, Kind: notify.AccountTurned18, Type: "user", ID: u.ID, Dedupe: fmt.Sprintf("account.turned_18:%d", u.ID)},
		},
		Tasks: []agesRaceTaskWant{
			agesRaceTask(u.ID, p, false, policy.ContactEnded, nil),
			agesRaceTask(f.admin.ID, p, true, policy.ContactEnded, nil),
		},
	}, l0, l1)
	releaseErasure()
	agesRaceOK(t, del, "erasure")
	erasure.End = time.Now().UTC()

	after := w.rows()[p.ID]
	agesRaceScrubbed(t, "request", mid, after)
	agesRaceTurned18(t, "request after rollover then erasure", before[p.ID], after, now)
	if after.Status != twinAfter.Status || after.EndReason != twinAfter.EndReason {
		t.Errorf("lifecycle after rollover then erasure = %s/%s, an identical twin gets %s/%s", after.Status, after.EndReason, twinAfter.Status, twinAfter.EndReason)
	}
	// The erasure ends nothing again: it adds its own deletion and removes the
	// subject's rows, the notices the rollover sent them included.
	agesRaceLedger(t, agesRaceStep{Name: "the erasure", Span: erasure, Gone: u.ID, Cleanup: 1,
		Audit: []agesRaceAuditWant{agesRaceDeleted(f.admin.ID, u.ID)},
	}, l1, erasureLedgerOf(t, db))
	agesRaceStates(t, w.states, nil)
}

// TestAgesNoteErasureRaceErasureThenRollover is the second rollover order of
// CA-5. The erasure holds the gate, uncommitted. rolloverAges runs its
// preliminary sweep (no try-lock, so nothing happens) and its discovery, which
// still sees the subject because the soft delete is not committed; that
// barrier is the evidence that discovery came first. The per-user transaction
// then waits on the gate in lockTeenLifecycleUsers. After the erasure commits
// it finds no subject and returns without converting anyone or reconciling.
// The erasure alone ends the request (account_disabled), closes the admin's
// task, tells the requester and removes the subject's rows; no turned-18 row
// exists.
func TestAgesNoteErasureRaceErasureThenRollover(t *testing.T) {
	w := newAgesRaceWorld(t)
	f, db := w.f, w.db
	u, leader := f.outsider, f.leader
	now := time.Now().UTC().Truncate(time.Microsecond)
	w.makeTeen(u)
	p := w.request(leader.ID, u.ID, policy.ContactPending, agesRaceNote, agesRaceNeedsAdmin)
	w.seedTasks(p)
	before := w.rows()
	l0 := erasureLedgerOf(t, db)

	var erasurePID atomic.Int64
	hold := agesRaceAfterScrub(u.ID, 1)
	hold.PID = &erasurePID
	reachedErasure, releaseErasure := w.run.pause(hold)
	// rolloverAges' Pluck of the teens past eighteen, stopped after it ran.
	reachedDiscovery, releaseDiscovery := w.run.pause(erasureTestPoint{Processor: "query", After: true, Step: "gorm:query",
		Table: "users", SQLContains: "birth_date <= "})

	span := erasureSpan{Start: time.Now().UTC()}
	del := w.erase(f.ac, u.ID)
	awaitDBSignal(t, reachedErasure)
	requireContactGate(t, db, agesRacePID(&erasurePID), true, "the erasure")
	done := w.run.rollover(w.app, now)
	awaitDBSignal(t, reachedDiscovery)
	// Discovery ran while the erasure held the gate and before it commits: the
	// subject is not deleted for anyone else, and the preliminary sweep, which
	// found the gate taken, changed nothing.
	if n := countRows(t, db, "users", "id = ? AND deleted_at IS NULL AND account_type = ?", u.ID, models.AccountTeen); n != 1 {
		t.Fatalf("the subject is not a live teen for other sessions at discovery (%d rows)", n)
	}
	unchanged := w.rows()
	for id, b := range before {
		agesRaceSame(t, fmt.Sprintf("request %d after the preliminary sweep", id), b, unchanged[id])
	}
	agesRaceLedger(t, agesRaceStep{Name: "the preliminary sweep that found the gate taken"}, l0, erasureLedgerOf(t, db))
	agesRaceNoGateWaiter(t, db)
	releaseDiscovery()
	agesRaceGateWaiter(t, db, agesRacePID(&erasurePID), "the per-user rollover transaction")
	releaseErasure()
	agesRaceOK(t, del, "erasure")
	span.End = time.Now().UTC()
	r := agesRaceAwait(t, done, "the rollover")
	both := erasureSpan{Start: span.Start, End: time.Now().UTC()}
	if r.err != nil || r.n != 0 {
		t.Fatalf("rolloverAges = (%d, %v), want (0, nil): the deleted subject is skipped", r.n, r.err)
	}

	after := w.rows()[p.ID]
	agesRaceEndedByErasure(t, "request", before[p.ID], after, span)
	var gone models.User
	if err := db.Unscoped().First(&gone, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	if gone.AccountType != models.AccountTeen || !gone.DeletedAt.Valid {
		t.Errorf("subject: account type %q deleted %t, want a deleted teen that was never converted", gone.AccountType, gone.DeletedAt.Valid)
	}
	if n := auditCount(t, w.app, audit.UserTurned18); n != 0 {
		t.Errorf("%d user.turned_18 rows, want none: rolloverTeen must not run", n)
	}
	agesRaceLedger(t, agesRaceStep{Name: "the erasure and the rollover after it", Span: both, Gone: u.ID, Cleanup: 1,
		Audit: []agesRaceAuditWant{
			agesRaceDeleted(f.admin.ID, u.ID),
			agesRaceEndRow(p, policy.ContactEnded, policy.ContactAccountDisabled),
		},
		Notices: []agesRaceNoticeWant{agesRaceUpdated(leader.ID, p, policy.ContactEnded)},
		Tasks:   []agesRaceTaskWant{agesRaceTask(f.admin.ID, p, true, policy.ContactEnded, nil)},
	}, l0, erasureLedgerOf(t, db))
	agesRaceStates(t, w.states, nil)
}

// TestAgesNoteErasureRaceUnlockedReader is CA-9. The erasure is held after its
// first ages-note batch has run (the barrier cannot match the second batch),
// with its row locks and uncommitted blanks in place. A reader on its own
// connection, in a transaction that stays open across the erasure's commit,
// runs a plain SELECT with no lock and no gate: it is not blocked, and sees
// every old note. The erasure then commits without waiting for the reader, and
// the reader's next SELECT sees the blank.
func TestAgesNoteErasureRaceUnlockedReader(t *testing.T) {
	const rows = lookupChunk + 1
	sc := newAgesNoteScene(t, false, rows)
	run, states := newAgesRaceRun(t, sc.app)
	db := run.db
	rec := recordAgesNoteStatements(t, db)
	before := sc.state(t)
	uid := sc.erased.ID

	var erasurePID atomic.Int64
	hold := agesRaceAfterScrub(uid, lookupChunk)
	hold.PID = &erasurePID
	reached, release := run.pause(hold)

	del := run.do(sc.ac, "DELETE", "/api/v1/admin/users/"+itoa(uid), nil)
	awaitDBSignal(t, reached)
	ePID := agesRacePID(&erasurePID)
	requireContactGate(t, db, ePID, true, "the erasure")
	// The control: the erasure's row locks are real. A reader that asked for a
	// lock would not get one.
	lockProbe := db.Transaction(func(tx *gorm.DB) error {
		return tx.Exec(`SELECT 1 FROM contact_requests WHERE id = (SELECT min(id) FROM contact_requests
			WHERE purpose = 'occurrence_add' AND subject_user_id = ?) FOR UPDATE NOWAIT`, uid).Error
	})
	var pe *pgconn.PgError
	if !errors.As(lockProbe, &pe) || pe.Code != "55P03" {
		t.Fatalf("FOR UPDATE NOWAIT on the subject's first row = %v, want SQLSTATE 55P03 while the erasure holds it", lockProbe)
	}

	const count = `SELECT count(*) FROM contact_requests WHERE purpose = 'occurrence_add' AND subject_user_id = ? AND add_childcare_ages_note <> ''`
	first, second := make(chan int64, 1), make(chan int64, 1)
	resume := make(chan struct{})
	var once sync.Once
	resumeReader := func() { once.Do(func() { close(resume) }) }
	readerPID, readerDone := startDBSession(t, db, "ages-reader", func(conn *gorm.DB) error {
		return conn.Transaction(func(tx *gorm.DB) error {
			var n int64
			if err := tx.Raw(count, uid).Scan(&n).Error; err != nil {
				return err
			}
			first <- n
			select {
			case <-resume:
			case <-tx.Statement.Context.Done():
				return tx.Statement.Context.Err()
			}
			if err := tx.Raw(count, uid).Scan(&n).Error; err != nil {
				return err
			}
			second <- n
			return nil
		})
	})
	run.hold(resumeReader)
	if got := agesRaceAwait(t, first, "the unlocked read"); got != rows {
		t.Errorf("the reader sees %d notes while the erasure is uncommitted, want %d", got, rows)
	}
	if readerPID == ePID {
		t.Fatalf("the reader shares the erasure's backend %d", ePID)
	}
	var waits, blockers int64
	if err := db.Raw("SELECT count(*) FROM pg_locks WHERE pid = ? AND NOT granted", readerPID).Scan(&waits).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw("SELECT cardinality(pg_blocking_pids(?)) + cardinality(pg_blocking_pids(?))", readerPID, ePID).Scan(&blockers).Error; err != nil {
		t.Fatal(err)
	}
	if waits != 0 || blockers != 0 {
		t.Errorf("the reader has %d ungranted locks, and the reader and the erasure are blocked by %d backends in all; want none", waits, blockers)
	}
	var state string
	if err := db.Raw("SELECT state FROM pg_stat_activity WHERE pid = ?", readerPID).Scan(&state).Error; err != nil || state != "idle in transaction" {
		t.Fatalf("reader backend state = %q (%v), want idle in transaction: its transaction must stay open across the commit", state, err)
	}

	release()
	agesRaceOK(t, del, "erasure") // committed while the reader's transaction was still open
	resumeReader()
	if got := agesRaceAwait(t, second, "the reader's second read"); got != 0 {
		t.Errorf("the reader sees %d notes after the erasure committed, want 0", got)
	}
	if err := awaitDBResult(t, readerDone); err != nil {
		t.Fatalf("reader: %v", err)
	}
	checkAgesNoteErased(t, before, sc.state(t), rows)
	_, updates := rec.seen()
	if want := []agesNoteStatement{{Vars: lookupChunk + 1, Rows: lookupChunk}, {Vars: 2, Rows: 1}}; !reflect.DeepEqual(updates, want) {
		t.Errorf("UPDATEs (bind parameters, rows changed) = %+v, want %+v: the barrier is on the first batch and the second ran after it", updates, want)
	}
	agesRaceStates(t, states, map[string]int{"55P03": 1})
}

// Regression tests for agesRaceRun.stop (T07A3-FIX-R4-1). They fail on the
// teardown it replaces: one that stopped waiting after a timeout, recorded
// itself as done and let the callbacks, App.DB and the shared database go while
// a participant was still alive. Each replaces report (and abort) on its own
// run; nothing is disabled in production code and no shared helper changes.

// TestAgesRaceStopTerminatesWhatItsContextCannotReach starts a participant on a
// session that no context of the run reaches (Connection hands out a raw
// connection, which is how startDBSession's sessions work) and sleeps on the
// server. Cancelation cannot end it, so stop must report it, terminate the
// run's backends from the control connection and join it, all before cleanups
// that unregister callbacks and restore App.DB run. The check that it has
// ended runs as one of those cleanups, registered before hold so that it runs
// after stop.
func TestAgesRaceStopTerminatesWhatItsContextCannotReach(t *testing.T) {
	w := newAgesRaceWorld(t)
	r := w.run
	r.grace = 300 * time.Millisecond
	var reports []string
	r.report = func(format string, args ...any) { reports = append(reports, fmt.Sprintf(format, args...)) }
	r.abort = func(msg string) { t.Errorf("stop gave up: %s", msg) }

	pids, result := make(chan int, 1), make(chan error, 1)
	r.spawn(func() {
		result <- r.db.WithContext(context.Background()).Connection(func(conn *gorm.DB) error {
			conn = conn.Session(&gorm.Session{NewDB: true})
			var pid int
			if err := conn.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
				return err
			}
			pids <- pid
			return conn.Exec("SELECT pg_sleep(120)").Error
		})
	})
	var pid int
	select {
	case pid = <-pids:
	case err := <-result:
		t.Fatalf("the participant ended before it slept: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the participant never reported its backend")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		app, state, ok, err := r.backendState(pid)
		if err != nil {
			t.Fatal(err)
		}
		if ok && state == "active" {
			if app != r.appName {
				t.Fatalf("participant backend %d has application_name %q, want the run's %q", pid, app, r.appName)
			}
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("participant backend %d is %q (found: %v), want active in pg_sleep", pid, state, ok)
		}
		time.Sleep(5 * time.Millisecond)
	}

	started := time.Now()
	t.Cleanup(func() {
		// Runs after stop, and before the callbacks come off and App.DB and the
		// control connection are put back.
		select {
		case err := <-result:
			var pe *pgconn.PgError
			switch {
			case err == nil:
				t.Error("the unreachable participant finished without an error")
			case errors.As(err, &pe) && pe.Code == "57014":
				t.Errorf("the participant ended at the server's statement_timeout, not by termination: %v", err)
			}
		default:
			t.Error("the participant was still running when stop returned")
		}
		if _, state, ok, err := r.backendState(pid); err != nil || (ok && state == "active") {
			t.Errorf("backend %d is still working after stop (found: %v, state %q, check: %v)", pid, ok, state, err)
		}
		if len(reports) != 1 || !strings.Contains(reports[0], "did not stop") {
			t.Errorf("reports = %q, want exactly one saying a participant did not stop", reports)
		}
		if elapsed := time.Since(started); elapsed > 20*time.Second {
			t.Errorf("stop took %v: it waited for the server's timeout instead of terminating", elapsed)
		}
		r.stopMu.Lock()
		stopped := r.stopped
		r.stopMu.Unlock()
		if !stopped {
			t.Error("stop returned without recording the participants as gone")
		}
	})
	r.hold(func() {})
}

// TestAgesRaceStopCancelsABackgroundContextTransaction is the shape of the
// preliminary sweep in rolloverAges: a transaction on a handle whose context is
// context.Background. WithContext on App.DB does not reach it; the pool
// wrapper does, so canceling the run ends its statement on the server without
// a termination and without a report.
func TestAgesRaceStopCancelsABackgroundContextTransaction(t *testing.T) {
	w := newAgesRaceWorld(t)
	r := w.run
	var reports []string
	r.report = func(format string, args ...any) { reports = append(reports, fmt.Sprintf(format, args...)) }
	r.abort = func(msg string) { t.Errorf("stop gave up: %s", msg) }

	result := make(chan error, 1)
	r.spawn(func() {
		result <- r.db.WithContext(context.Background()).Transaction(func(tx *gorm.DB) error {
			return tx.Exec("SELECT pg_sleep(120)").Error
		})
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		n, err := r.busy(0)
		if err != nil {
			t.Fatal(err)
		}
		if n >= 1 {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("%d backends of the run are working, want the sleeping transaction", n)
		}
		time.Sleep(5 * time.Millisecond)
	}

	started := time.Now()
	r.stop()
	if len(reports) != 0 {
		t.Errorf("stop reported %q: the run's cancelation did not reach the Background-context transaction", reports)
	}
	if elapsed := time.Since(started); elapsed >= r.grace {
		t.Errorf("stop took %v, as long as the %v it waits before it terminates", elapsed, r.grace)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Error("the canceled transaction finished without an error")
		}
	default:
		t.Error("the transaction was still running when stop returned")
	}
}

// TestAgesRaceStopFailsClosedWhenAParticipantCannotBeStopped uses a participant
// that no cancelation and no termination can end (it waits on a channel). stop
// must call abort, must not record the run as stopped, and must try again on the
// next call, which succeeds once the participant ends.
func TestAgesRaceStopFailsClosedWhenAParticipantCannotBeStopped(t *testing.T) {
	w := newAgesRaceWorld(t)
	r := w.run
	r.grace, r.final = 100*time.Millisecond, 200*time.Millisecond
	var reports, aborts []string
	r.report = func(format string, args ...any) { reports = append(reports, fmt.Sprintf(format, args...)) }
	r.abort = func(msg string) { aborts = append(aborts, msg) }

	block, finished := make(chan struct{}), make(chan struct{})
	r.spawn(func() {
		defer close(finished)
		<-block
	})
	r.stop()
	if len(aborts) != 1 || !strings.Contains(aborts[0], "still alive") {
		t.Fatalf("aborts = %q, want one saying a participant is still alive", aborts)
	}
	if len(reports) != 1 {
		t.Errorf("reports = %q, want the one timeout report", reports)
	}
	if r.stopped {
		t.Error("a stop that gave up was recorded as done")
	}

	close(block)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the participant did not end after it was released")
	}
	r.stop()
	if len(aborts) != 1 {
		t.Errorf("the retry aborted again: %q", aborts)
	}
	if !r.stopped {
		t.Error("the retry did not record the participants as gone")
	}

}

// TestAgesRaceStopAbortPreventsCleanup exercises the real abort from a cleanup
// in a subprocess, with a participant that remains alive. The sentinel is a
// later cleanup and must not run before the process exits.
func TestAgesRaceStopAbortPreventsCleanup(t *testing.T) {
	const marker = "AGES_RACE_ABORT_CHILD"
	if name := os.Getenv(marker); name != "" {
		db, err := testdb.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		r, _ := newAgesRaceRun(t, &App{DB: db})
		r.grace, r.final = 100*time.Millisecond, 200*time.Millisecond
		r.spawn(func() { select {} })
		t.Cleanup(func() { fmt.Fprintln(os.Stderr, "AGES_RACE_TEARDOWN_SENTINEL") })
		r.hold(func() {})
		return
	}
	app := testApp(t)
	var name string
	if err := app.DB.Raw("SELECT current_database()").Scan(&name).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAgesRaceStopAbortPreventsCleanup$")
	cmd.Env = append(os.Environ(), marker+"="+name)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("child exit = %v, want exit 1; output: %s", err, out)
	}
	if !strings.Contains(string(out), "a database participant is still alive") || strings.Contains(string(out), "AGES_RACE_TEARDOWN_SENTINEL") {
		t.Fatalf("child did not abort before later cleanup: %s", out)
	}
}
