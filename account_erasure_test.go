package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"fold/internal/jobs"
	"fold/internal/mail"
	"fold/internal/policy"
	"fold/models"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
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
