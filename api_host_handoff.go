package main

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"fold/internal/audit"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Church-wide event hosts (M7.4, R4-H; owner decision 3: "Require the admin to
// choose a replacement before deleting the account"). The helpers here are
// shared by host removal, adding and creating, event deletion and, in the same
// slice, the account-deletion handoff.
//
// Lock order for every event_hosts writer (event_hosts.event_id has foreign
// key fk_event_hosts_event, so any INSERT takes FOR KEY SHARE on the event row):
//
//   - Account deletion (gated) locks the target's hosted church-wide events FOR
//     KEY SHARE in ascending id, then the target user FOR UPDATE. Every ungated
//     event FOR UPDATE taker (room decisions, date and series room booking, room
//     locations, events.Extend) takes the event while holding no user row.
//     Everything that holds a user row and then wants an event FOR UPDATE takes
//     the contact gate first, so deletion serializes with it.
//   - Add and create (ungated) lock their users FOR SHARE in ascending id
//     (lockHostUsers) and take an existing event only FOR KEY SHARE, which never
//     conflicts with deletion's KEY SHARE and still waits for event deletion.
//   - Remove (gated) keeps its FOR UPDATE on the event and reads the actor
//     with a plain read; a SHARE lock there could cycle with the actor's own
//     ungated registration change.
//   - Event deletion locks its events FOR UPDATE before any child delete.

// hostSeatsByEvent is every host row of every live church-wide event that
// userID hosts, with whether each host's account is deleted. It joins users
// unscoped, so a tombstone row is a seat with Deleted set. No date filter.
func hostSeatsByEvent(db *gorm.DB, userID uint) (map[uint][]policy.HostSeat, error) {
	var rows []struct {
		EventID uint
		UserID  uint
		Deleted bool
	}
	err := db.Raw(`SELECT eh.event_id, eh.user_id, (u.deleted_at IS NOT NULL) AS deleted
		FROM event_hosts eh
		JOIN events e ON e.id = eh.event_id AND e.deleted_at IS NULL AND e.group_id IS NULL
		JOIN users u ON u.id = eh.user_id
		WHERE eh.event_id IN (SELECT event_id FROM event_hosts WHERE user_id = ?)
		ORDER BY eh.event_id, eh.user_id`, userID).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := map[uint][]policy.HostSeat{}
	for _, r := range rows {
		out[r.EventID] = append(out[r.EventID], policy.HostSeat{UserID: r.UserID, Deleted: r.Deleted})
	}
	return out, nil
}

// soleHosted is, ascending, the events in seats that userID cannot leave
// without leaving them hostless.
func soleHosted(seats map[uint][]policy.HostSeat, userID uint) []uint {
	var out []uint
	for id, hosts := range seats {
		if !policy.EventKeepsHost(hosts, userID) {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// lockHostUsers locks the named users that still exist FOR SHARE, in
// ascending id, with one statement (the one lockTeenLifecycleUsers uses to
// order its own). Zero ids and duplicates are dropped. The statement is soft
// delete scoped, so a row whose deletion committed while it waited is
// re-evaluated and comes back absent; the caller treats absent as gone.
func lockHostUsers(tx *gorm.DB, ids []uint) (map[uint]*models.User, error) {
	seen := map[uint]bool{}
	want := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		want = append(want, id)
	}
	locked := make(map[uint]*models.User, len(want))
	if len(want) == 0 {
		return locked, nil
	}
	var users []models.User
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id IN ?", want).Order("id").Find(&users).Error; err != nil {
		return nil, err
	}
	for i := range users {
		locked[users[i].ID] = &users[i]
	}
	return locked, nil
}

// lockedHostActor rechecks the signed-in admin under lockHostUsers: gone, no
// longer able to sign in, no longer an admin, or a bumped session epoch is
// errSecuritySession, which the handler answers 401 (adminCurrent).
func lockedHostActor(locked map[uint]*models.User, actor *models.User) error {
	if !adminCurrent(locked[actor.ID], actor) {
		return errSecuritySession
	}
	return nil
}

// hostEligibility is the one rule for who may host, used by the unlocked
// preflight (hostCandidate) and by every recheck under lockHostUsers. A nil u
// is an account that no longer exists.
func hostEligibility(actor, u *models.User) *apiError {
	if u == nil {
		return errNoAccount
	}
	if u.Status != models.UserActive {
		return errCannotHost
	}
	if !policy.CanAssignHost(asPerson(actor), asPerson(u)) {
		return errTeenHost
	}
	return nil
}

// removeDeletedHostRows deletes the host rows that deleted accounts left on
// the given events (a tombstone is invisible in the host list, so nobody can
// remove it by hand) and audits each one, ordered by (event, user). Call it
// in the transaction that changed the event's hosts.
func removeDeletedHostRows(tx *gorm.DB, actorID uint, eventIDs []uint, ip string) error {
	if len(eventIDs) == 0 {
		return nil
	}
	return deleteHostRowsAudited(tx, &actorID, ip, hostRemovedDeleted, `DELETE FROM event_hosts WHERE event_id IN ?
		AND user_id IN (SELECT id FROM users WHERE deleted_at IS NOT NULL)
		RETURNING event_id, user_id`, eventIDs)
}

// The reasons a host row is removed without a person choosing it.
const (
	// hostRemovedDeleted: the host's account was already deleted.
	hostRemovedDeleted = "deleted_host_cleanup"
	// hostRemovedAccountDeleted: the account is being deleted now.
	hostRemovedAccountDeleted = "account_deleted"
)

// deleteHostRowsAudited runs query, a DELETE FROM event_hosts ... RETURNING
// event_id, user_id, and writes one event.host_removed audit row per removed
// row, ordered by (event, user), with reason. actor is nil for a system
// action.
func deleteHostRowsAudited(tx *gorm.DB, actor *uint, ip, reason, query string, args ...any) error {
	var gone []struct {
		EventID uint
		UserID  uint
	}
	if err := tx.Raw(query, args...).Scan(&gone).Error; err != nil {
		return err
	}
	sort.Slice(gone, func(i, j int) bool {
		if gone[i].EventID != gone[j].EventID {
			return gone[i].EventID < gone[j].EventID
		}
		return gone[i].UserID < gone[j].UserID
	})
	for _, g := range gone {
		if err := audit.Write(tx, audit.Entry{Actor: actor, Action: audit.EventHostRemoved, TargetType: "event", TargetID: g.EventID,
			Meta: map[string]any{"userId": g.UserID, "reason": reason}, IP: ip}); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Replacement host before account deletion (M7.4, R4-H, owner decision 3)
// ---------------------------------------------------------------------------

// maxHostSuccessors bounds the hostSuccessors map of one deletion request.
const maxHostSuccessors = 1000

// soleHostEventJSON is a church-wide event whose only non-deleted host is the
// person being deleted. NextStartsAt is the next scheduled date as an instant;
// the browser formats it (lib/when.ts), so the server never converts a zone.
type soleHostEventJSON struct {
	ID           uint       `json:"id"`
	Title        string     `json:"title"`
	NextStartsAt *time.Time `json:"nextStartsAt,omitempty"`
}

// soleHostEventsJSON is the answer of GET /admin/users/:id/sole-host-events
// and the body of the 400 a deletion without replacements gets. Every listed
// event has no other live host, so one candidate list serves all of them.
type soleHostEventsJSON struct {
	Events     []soleHostEventJSON `json:"events"`
	Candidates []successorJSON     `json:"candidates"`
}

// errNeedHostSuccessors: some church-wide events the person hosts alone have no
// valid replacement, or the list changed while the request waited.
type errNeedHostSuccessors struct{ data soleHostEventsJSON }

func (errNeedHostSuccessors) Error() string { return errNeedsHostSuccessor.Message }

// Is matches the declared refusal this error answers with.
func (errNeedHostSuccessors) Is(target error) bool { return target == errNeedsHostSuccessor }

// apiAnswer is the 400 with the events still missing a host and who may host
// them.
func (e errNeedHostSuccessors) apiAnswer() *apiError {
	return errNeedsHostSuccessor.with(gin.H{"events": e.data.Events, "candidates": e.data.Candidates})
}

// soleHostEventsFor builds the events a person hosts alone, from the seats it
// is given, with who may host them next. Nothing else is read when there are
// none; otherwise exactly three reads, however many events there are. The
// second result is the sole-hosted event ids, ascending.
func soleHostEventsFor(db *gorm.DB, actor *models.User, targetID uint, seats map[uint][]policy.HostSeat, now time.Time) (soleHostEventsJSON, error) {
	out := soleHostEventsJSON{Events: []soleHostEventJSON{}, Candidates: []successorJSON{}}
	sole := soleHosted(seats, targetID)
	if len(sole) == 0 {
		return out, nil
	}
	var events []struct {
		ID    uint
		Title string
	}
	if err := db.Model(&models.Event{}).Select("id, title").Where("id IN ?", sole).Order("title, id").Scan(&events).Error; err != nil {
		return out, err
	}
	var next []struct {
		EventID  uint
		StartsAt time.Time
	}
	if err := db.Raw(`SELECT DISTINCT ON (event_id) event_id, starts_at FROM event_occurrences
		WHERE event_id IN ? AND status = ? AND starts_at >= ? ORDER BY event_id, starts_at`,
		sole, models.OccurrenceScheduled, now).Scan(&next).Error; err != nil {
		return out, err
	}
	nextAt := make(map[uint]time.Time, len(next))
	for _, n := range next {
		nextAt[n.EventID] = n.StartsAt
	}
	for _, e := range events {
		item := soleHostEventJSON{ID: e.ID, Title: e.Title}
		if at, ok := nextAt[e.ID]; ok {
			at := at
			item.NextStartsAt = &at
		}
		out.Events = append(out.Events, item)
	}
	var users []models.User
	if err := db.Where("status = ? AND id <> ?", models.UserActive, targetID).Order("name, id").Find(&users).Error; err != nil {
		return out, err
	}
	for i := range users {
		if policy.CanAssignHost(asPerson(actor), asPerson(&users[i])) {
			out.Candidates = append(out.Candidates, successorJSON{ID: users[i].ID, Name: users[i].PublicName(), Admin: asPerson(&users[i]).Admin})
		}
	}
	return out, nil
}

// apiAdminSoleHostEvents: GET /admin/users/:id/sole-host-events. No locks; the
// deletion checks the same facts again under its own.
func (a *App) apiAdminSoleHostEvents(c *gin.Context) {
	u, ok := a.adminTargetUser(c)
	if !ok {
		return
	}
	seats, err := hostSeatsByEvent(a.DB, u.ID)
	if err == nil {
		var out soleHostEventsJSON
		out, err = soleHostEventsFor(a.DB, apiUser(c), u.ID, seats, time.Now().UTC())
		if err == nil {
			c.JSON(http.StatusOK, out)
			return
		}
	}
	jsonError(c, http.StatusInternalServerError, "could not list events")
}

// parseHostSuccessors reads the hostSuccessors map of a deletion request: at
// most maxHostSuccessors entries, every key the canonical decimal form of a
// positive event id (so "1" and "01" cannot both name one event), every value
// a positive user id.
func parseHostSuccessors(raw map[string]uint) (map[uint]uint, bool) {
	if len(raw) > maxHostSuccessors {
		return nil, false
	}
	picks := make(map[uint]uint, len(raw))
	for k, v := range raw {
		n, err := strconv.ParseUint(k, 10, 64)
		if err != nil || n == 0 || k != strconv.FormatUint(n, 10) || v == 0 {
			return nil, false
		}
		picks[uint(n)] = v
	}
	return picks, true
}

// hostSuccessorsValid reports whether picks is a complete, eligible
// replacement plan: every sole-hosted event has a pick, every key is an event
// the person still hosts, and every pick is another active adult account the
// acting admin may name. Missing, extra, unknown, group, deleted, not-hosted,
// stale and ineligible picks are all "no".
func hostSuccessorsValid(tx *gorm.DB, actor *models.User, targetID uint, seats map[uint][]policy.HostSeat, sole []uint, picks map[uint]uint) (bool, error) {
	for _, id := range sole {
		if picks[id] == 0 {
			return false, nil
		}
	}
	distinct := map[uint]bool{}
	for eventID, pick := range picks {
		if _, hosted := seats[eventID]; !hosted || pick == targetID {
			return false, nil
		}
		distinct[pick] = true
	}
	if len(distinct) == 0 {
		return true, nil
	}
	ids := make([]uint, 0, len(distinct))
	for id := range distinct {
		ids = append(ids, id)
	}
	var users []models.User
	if err := tx.Where("id IN ?", ids).Find(&users).Error; err != nil {
		return false, err
	}
	if len(users) != len(ids) {
		return false, nil
	}
	for i := range users {
		if users[i].ID == targetID || users[i].Status != models.UserActive || !policy.CanAssignHost(asPerson(actor), asPerson(&users[i])) {
			return false, nil
		}
	}
	return true, nil
}

// handOffHosting runs in the deletion transaction after the picks are valid
// and the target's hosted events are locked. Each pick becomes a host of its
// event (a pick who already hosts it changes nothing and is not audited), the
// locked events lose their deleted-host rows, and every host row the target
// has goes, each with an audit row, in ascending event id. The deleting admin
// is added only when explicitly picked.
func handOffHosting(tx *gorm.DB, actorID, targetID uint, picks map[uint]uint, locked []uint, ip string) error {
	events := make([]uint, 0, len(picks))
	for id := range picks {
		events = append(events, id)
	}
	sort.Slice(events, func(i, j int) bool { return events[i] < events[j] })
	for _, eventID := range events {
		pick := picks[eventID]
		res := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "event_id"}, {Name: "user_id"}}, DoNothing: true}).
			Create(&models.EventHost{EventID: eventID, UserID: pick, AddedBy: actorID})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			continue
		}
		if err := audit.Write(tx, audit.Entry{Actor: &actorID, Action: audit.EventHostAdded, TargetType: "event", TargetID: eventID,
			Meta: map[string]any{"userId": pick, "reason": "account_deleted", "departingUserId": targetID}, IP: ip}); err != nil {
			return err
		}
	}
	if err := removeDeletedHostRows(tx, actorID, locked, ip); err != nil {
		return err
	}
	// Every row of the target, including those on deleted events; deleting a
	// child row takes no lock on its event.
	return deleteHostRowsAudited(tx, &actorID, ip, hostRemovedAccountDeleted, hostRowsOfUserSQL, targetID)
}

// hostRowsOfUserSQL deletes every host row of one account.
const hostRowsOfUserSQL = `DELETE FROM event_hosts WHERE user_id = ? RETURNING event_id, user_id`

// lockHostedEvents locks the live church-wide events among ids FOR KEY SHARE,
// ascending, and returns the ids it locked. KEY SHARE is what the host insert's
// foreign key takes anyway, taken before the account row so that no
// event-first transaction can hold an event while waiting for this account
// (see the lock order above).
func lockHostedEvents(tx *gorm.DB, ids []uint) (map[uint]bool, error) {
	locked := map[uint]bool{}
	if len(ids) == 0 {
		return locked, nil
	}
	var rows []models.Event
	if err := tx.Clauses(clause.Locking{Strength: "KEY SHARE"}).Select("id").
		Where("id IN ? AND group_id IS NULL", ids).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, e := range rows {
		locked[e.ID] = true
	}
	return locked, nil
}

// deletionHostPlan is step (1)-(6) of the deletion's host handling, in the
// order that keeps the lock graph acyclic: hosted events FOR KEY SHARE, then
// the target FOR UPDATE, then a plain read of the actor, then the facts again.
// It returns the validated picks, the events it holds and the actor it re-read
// (the last-admin rule decides on that row, R4-1-3).
func deletionHostPlan(tx *gorm.DB, u *models.User, me *models.User, requested map[uint]uint) (map[uint]uint, []uint, *models.User, error) {
	seats0, err := hostSeatsByEvent(tx, u.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	ids := make([]uint, 0, len(seats0))
	for id := range seats0 {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	lockedH, err := lockHostedEvents(tx, ids)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(u, u.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil, errAdminTargetGone
		}
		return nil, nil, nil, err
	}
	// A plain read: a share lock here could cycle with the admin's own ungated
	// change (the gate serializes the rest).
	actor, err := lockAdminIn(tx, me, actorRead)
	if err != nil {
		return nil, nil, nil, err
	}
	seats, err := hostSeatsByEvent(tx, u.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	need := func() error {
		data, err := soleHostEventsFor(tx, actor, u.ID, seats, time.Now().UTC())
		if err != nil {
			return err
		}
		return errNeedHostSuccessors{data}
	}
	for id := range seats {
		if !lockedH[id] {
			// An ungated add or create of this account committed between the
			// first read and the lock. Locking a new event now, after the user
			// row, would rebuild the cycle, so answer with the current list.
			return nil, nil, nil, need()
		}
	}
	valid, err := hostSuccessorsValid(tx, actor, u.ID, seats, soleHosted(seats, u.ID), requested)
	if err != nil {
		return nil, nil, nil, err
	}
	if !valid {
		return nil, nil, nil, need()
	}
	locked := make([]uint, 0, len(lockedH))
	for id := range lockedH {
		locked = append(locked, id)
	}
	sort.Slice(locked, func(i, j int) bool { return locked[i] < locked[j] })
	return requested, locked, actor, nil
}
