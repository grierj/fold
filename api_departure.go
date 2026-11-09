package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/events"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	errDepartureNotFound    = apiErr(http.StatusNotFound, "", "group not found")
	errDepartureForbidden   = apiErr(http.StatusForbidden, "", "forbidden")
	errDepartureUnavailable = apiErr(http.StatusConflict, "departure_unavailable", "this departure is no longer available; refresh the group and check membership and leadership")
	errDepartureStale       = apiErr(http.StatusConflict, "departure_stale", "this departure changed; review the updated dates before trying again")
)

const departureSnapshotVersion = 3

// departureKeepableDateJSON is deliberately date-only. It contains enough to
// choose retained dates without exposing attendees, a hidden parent group,
// recurrence rules, sibling dates, or other series metadata.
type departureKeepableDateJSON struct {
	OccurrenceID uint      `json:"occurrenceId"`
	Date         string    `json:"date"`
	Title        string    `json:"title"`
	StartsAt     time.Time `json:"startsAt"`
	EndsAt       time.Time `json:"endsAt"`
	Status       string    `json:"status"`
	Visibility   string    `json:"visibility"`
	DefaultKeep  bool      `json:"defaultKeep"`
	KeepReason   string    `json:"keepReason"`
}

// departurePreviewJSON is shared by self-leave and authorized removal. A zero
// affectedCount is still a valid confirmation context.
type departurePreviewJSON struct {
	Action           string                      `json:"action"` // leave | remove
	GroupID          uint                        `json:"groupId"`
	GroupName        string                      `json:"groupName"`
	SubjectID        uint                        `json:"subjectId"`
	SubjectName      string                      `json:"subjectName"`
	AffectedCount    int                         `json:"affectedCount"`
	GoingCount       int                         `json:"goingCount"`
	MaybeCount       int                         `json:"maybeCount"`
	WaitlistedCount  int                         `json:"waitlistedCount"`
	KeepableCount    int                         `json:"keepableCount"`
	DefaultKeepCount int                         `json:"defaultKeepCount"`
	Dates            []departureKeepableDateJSON `json:"dates"`
	Snapshot         string                      `json:"snapshot"`
}

// departureConfirmRequest contains only the opaque preview token and exact
// occurrence IDs the confirmer chose to retain. All counts and date facts are
// reloaded under locks; submitted IDs never grant authority.
type departureConfirmRequest struct {
	Snapshot          string `json:"snapshot"`
	KeepOccurrenceIDs []uint `json:"keepOccurrenceIds"`
	Note              string `json:"note"`
}

// departureConfirmationJSON is the small post-commit result shared by self,
// leader, and admin departures. It intentionally carries no hidden group or
// per-date metadata.
type departureConfirmationJSON struct {
	Action     string `json:"action"`
	GroupID    uint   `json:"groupId"`
	SubjectID  uint   `json:"subjectId"`
	KeptCount  int    `json:"keptCount"`
	EndedCount int    `json:"endedCount"`
}

type departureUserFact struct {
	ID             uint   `json:"id"`
	Role           string `json:"role"`
	Status         string `json:"status"`
	AccountType    string `json:"accountType"`
	CanSignIn      bool   `json:"canSignIn"`
	SessionVersion int    `json:"sessionVersion"`
}

type departureMembershipFact struct {
	ID       uint   `json:"id"`
	UserID   uint   `json:"userId"`
	GroupID  uint   `json:"groupId"`
	Role     string `json:"role"`
	Status   string `json:"status"`
	JoinedAt int64  `json:"joinedAt"`
	Tenure   uint   `json:"tenure"`
}

type departureRegistrationFact struct {
	ID                 uint   `json:"id"`
	OccurrenceID       uint   `json:"occurrenceId"`
	UserID             uint   `json:"userId"`
	Status             string `json:"status"`
	AttendanceMode     string `json:"attendanceMode"`
	GuestCount         int    `json:"guestCount"`
	ChildcareRequested bool   `json:"childcareRequested"`
	ChildcareCount     int    `json:"childcareCount"`
	ChildcareAgesNote  string `json:"childcareAgesNote"`
	WaitlistedAt       int64  `json:"waitlistedAt"`
	RemovedBy          uint   `json:"removedBy"`
}

type departureAccessFact struct {
	ID                    uint   `json:"id"`
	Origin                string `json:"origin"`
	OrganizerType         string `json:"organizerType"`
	OrganizerID           uint   `json:"organizerId"`
	RegistrationID        uint   `json:"registrationId"`
	IssuerID              uint   `json:"issuerId"`
	RequestID             uint   `json:"requestId"`
	DepartureMembershipID uint   `json:"departureMembershipId"`
	DepartureActorID      uint   `json:"departureActorId"`
	Status                string `json:"status"`
	GrantedAt             int64  `json:"grantedAt"`
}

type departureDateFact struct {
	OccurrenceID        uint                      `json:"occurrenceId"`
	EventID             uint                      `json:"eventId"`
	OriginalDate        string                    `json:"originalDate"`
	Title               string                    `json:"title"`
	StartsAt            int64                     `json:"startsAt"`
	EndsAt              int64                     `json:"endsAt"`
	OccurrenceStatus    string                    `json:"occurrenceStatus"`
	EffectiveVisibility string                    `json:"effectiveVisibility"`
	Keepable            bool                      `json:"keepable"`
	DefaultKeep         bool                      `json:"defaultKeep"`
	KeepReason          string                    `json:"keepReason"`
	Registration        departureRegistrationFact `json:"registration"`
	Access              *departureAccessFact      `json:"access"`
}

// departureFacts is the stable T4a/T4b handoff. It contains only facts that
// can change departure authority, the affected/keepable set, displayed chooser
// data, the subject's registration, or retained access provenance. It
// deliberately contains no observation timestamp, so ordinary clock movement
// does not stale a preview; recomputing
// the derived keepability at a cutoff/start/end boundary does.
type departureFacts struct {
	Version           int                     `json:"version"`
	Origin            string                  `json:"origin"`
	GroupID           uint                    `json:"groupId"`
	GroupName         string                  `json:"groupName"`
	GroupStatus       string                  `json:"groupStatus"`
	GroupVisibility   string                  `json:"groupVisibility"`
	Actor             departureUserFact       `json:"actor"`
	Subject           departureUserFact       `json:"subject"`
	SubjectName       string                  `json:"subjectName"`
	ActorMembership   departureMembershipFact `json:"actorMembership"`
	SubjectMembership departureMembershipFact `json:"subjectMembership"`
	LeaderCount       int                     `json:"leaderCount"`
	Dates             []departureDateFact     `json:"dates"`
}

type departureSnapshotToken struct {
	Version   int    `json:"version"`
	ActorID   uint   `json:"actorId"`
	SubjectID uint   `json:"subjectId"`
	GroupID   uint   `json:"groupId"`
	Digest    string `json:"digest"`
}

func departureUserSnapshot(u *models.User) departureUserFact {
	return departureUserFact{ID: u.ID, Role: u.Role, Status: u.Status, AccountType: u.AccountType, CanSignIn: u.CanSignIn(), SessionVersion: u.SessionVersion}
}

func departureMembershipSnapshot(m models.Membership) departureMembershipFact {
	return departureMembershipFact{ID: m.ID, UserID: m.UserID, GroupID: m.GroupID, Role: m.Role, Status: m.Status, JoinedAt: m.JoinedAt.UTC().UnixNano(), Tenure: m.Tenure}
}

func ptrUint(v *uint) uint {
	if v == nil {
		return 0
	}
	return *v
}

func unixNanoOrZero(v *time.Time) int64 {
	if v == nil {
		return 0
	}
	return v.UTC().UnixNano()
}

func departureRegistrationSnapshot(r *models.Registration) departureRegistrationFact {
	return departureRegistrationFact{
		ID: r.ID, OccurrenceID: r.OccurrenceID, UserID: ptrUint(r.UserID), Status: r.Status,
		AttendanceMode: r.AttendanceMode, GuestCount: r.GuestCount,
		ChildcareRequested: r.ChildcareRequested, ChildcareCount: r.ChildcareCount,
		ChildcareAgesNote: r.ChildcareAgesNote, WaitlistedAt: unixNanoOrZero(r.WaitlistedAt), RemovedBy: ptrUint(r.RemovedBy),
	}
}

// departureAccessSnapshot is the live exact-date grant on a registration, as
// the preview digest sees it. A registration without a live grant has none.
func departureAccessSnapshot(r *models.Registration, scope accessScope) *departureAccessFact {
	if r == nil || !r.AccessLive() {
		return nil
	}
	return &departureAccessFact{
		ID: r.ID, Origin: r.AccessOrigin, OrganizerType: scope.Organizer.Type, OrganizerID: scope.Organizer.ID,
		RegistrationID: r.ID, IssuerID: ptrUint(r.AccessIssuerID), RequestID: ptrUint(r.AccessRequestID),
		DepartureMembershipID: ptrUint(r.AccessDepartureMembershipID), DepartureActorID: ptrUint(r.AccessDepartureActorID),
		Status: policy.OccurrenceAccessActive, GrantedAt: r.AccessGrantedAt.UTC().UnixNano(),
	}
}

func currentDepartureRegistration(status string) bool {
	return slices.Contains(models.CurrentRegStatuses, status)
}

func loadMembership(tx *gorm.DB, userID, groupID uint) (models.Membership, error) {
	var m models.Membership
	err := tx.Where("user_id = ? AND group_id = ?", userID, groupID).Limit(1).Find(&m).Error
	return m, err
}

// loadDepartureFacts reads one repeatable snapshot for either self-leave or
// removal. The caller supplies the authenticated actor and the subject named by
// the route; authorization always goes through the pure departure policy.
func loadDepartureFacts(tx *gorm.DB, authenticated *models.User, groupID, subjectID uint, now time.Time) (departureFacts, error) {
	facts := departureFacts{Version: departureSnapshotVersion, GroupID: groupID, Dates: []departureDateFact{}}
	current, err := lockActorIn(tx, authenticated, actorRead)
	if errors.Is(err, errSecuritySession) {
		return facts, errDepartureForbidden
	}
	if err != nil {
		return facts, err
	}
	actor := *current
	var group models.Group
	if err := tx.First(&group, groupID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return facts, errDepartureNotFound
		}
		return facts, err
	}
	actorMembership, err := loadMembership(tx, actor.ID, group.ID)
	if err != nil {
		return facts, err
	}
	actorRel := membershipRel(actorMembership)
	if !policy.CanSeeGroup(asPerson(&actor), asGroup(&group), actorRel) {
		return facts, errDepartureNotFound
	}
	if actor.ID != subjectID && !policy.CanManageGroup(asPerson(&actor), actorRel) {
		return facts, errDepartureForbidden
	}
	var subject models.User
	if err := tx.First(&subject, subjectID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return facts, errDepartureNotFound
		}
		return facts, err
	}
	subjectMembership, err := loadMembership(tx, subject.ID, group.ID)
	if err != nil {
		return facts, err
	}
	leaders, err := leaderCount(tx, group.ID)
	if err != nil {
		return facts, err
	}
	origin := policy.OccurrenceOriginDepartureRemoval
	if actor.ID == subject.ID {
		origin = policy.OccurrenceOriginDepartureSelf
	}
	if !policy.CanRetainOccurrenceAfterDeparture(origin, asPerson(&actor), asPerson(&subject), actorRel, membershipRel(subjectMembership), leaders) {
		return facts, errDepartureUnavailable
	}

	facts.Origin = origin
	facts.GroupName, facts.GroupStatus, facts.GroupVisibility = group.Name, group.Status, group.Visibility
	facts.Actor, facts.Subject = departureUserSnapshot(&actor), departureUserSnapshot(&subject)
	facts.SubjectName = subject.PublicName()
	facts.ActorMembership, facts.SubjectMembership = departureMembershipSnapshot(actorMembership), departureMembershipSnapshot(subjectMembership)
	facts.LeaderCount = leaders

	var registrations []models.Registration
	if err := tx.Joins("JOIN event_occurrences ON event_occurrences.id = registrations.occurrence_id").
		Joins("JOIN events ON events.id = event_occurrences.event_id AND events.deleted_at IS NULL").
		Where("registrations.user_id = ? AND events.group_id = ? AND registrations.status IN ? AND event_occurrences.status = ? AND event_occurrences.ends_at > ?",
			subject.ID, group.ID, models.CurrentRegStatuses, models.OccurrenceScheduled, now).
		Preload("Occurrence").Preload("Occurrence.Event").
		Order("event_occurrences.starts_at, event_occurrences.id").Find(&registrations).Error; err != nil {
		return facts, err
	}
	for i := range registrations {
		r := &registrations[i]
		o, ev := &r.Occurrence, &r.Occurrence.Event
		visibility := policy.EffectiveVisibility(ev.Visibility, o.VisibilityOverride)
		decision := policy.DecideDepartureDate(policy.DepartureDateFacts{
			Scheduled: o.Status == models.OccurrenceScheduled, NotEnded: now.Before(o.EndsAt),
			RegistrationCurrent: currentDepartureRegistration(r.Status), SubjectEligible: subject.CanSignIn(),
			Underway:     !now.Before(o.StartsAt) && now.Before(o.EndsAt),
			InsideCutoff: events.InCutoff(now, o.StartsAt), Public: visibility == models.VisibilityPublic,
		})
		if !decision.Affected {
			continue
		}
		title := effectiveOccurrenceTitle(ev, o)
		facts.Dates = append(facts.Dates, departureDateFact{
			OccurrenceID: o.ID, EventID: ev.ID, OriginalDate: models.FormatCalendarDate(o.OriginalDate), Title: title,
			StartsAt: o.StartsAt.UTC().UnixNano(), EndsAt: o.EndsAt.UTC().UnixNano(), OccurrenceStatus: o.Status,
			EffectiveVisibility: visibility, Keepable: decision.Keepable, DefaultKeep: decision.DefaultKeep, KeepReason: decision.KeepReason,
			Registration: departureRegistrationSnapshot(r), Access: departureAccessSnapshot(r, accessScopeOf(ev)),
		})
	}
	sort.Slice(facts.Dates, func(i, j int) bool {
		if facts.Dates[i].StartsAt != facts.Dates[j].StartsAt {
			return facts.Dates[i].StartsAt < facts.Dates[j].StartsAt
		}
		return facts.Dates[i].OccurrenceID < facts.Dates[j].OccurrenceID
	})
	return facts, nil
}

func departureFactsDigest(facts departureFacts) ([sha256.Size]byte, error) {
	encoded, err := json.Marshal(facts)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

func (a *App) departureSnapshotCipher() (cipher.AEAD, error) {
	key, err := deriveKey(a.Config.SessionSecret, "departure-preview:v3")
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (a *App) newDepartureSnapshot(facts departureFacts) (string, error) {
	digest, err := departureFactsDigest(facts)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(departureSnapshotToken{
		Version: departureSnapshotVersion, ActorID: facts.Actor.ID, SubjectID: facts.Subject.ID, GroupID: facts.GroupID,
		Digest: base64.RawURLEncoding.EncodeToString(digest[:]),
	})
	if err != nil {
		return "", err
	}
	crypt, err := a.departureSnapshotCipher()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, crypt.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := crypt.Seal(nonce, nonce, payload, []byte("departure-preview"))
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// parseDepartureSnapshot verifies server authorship and binds the token to the
// exact route actor, subject and group. T4b must still recompute current facts
// under its locks and call departureSnapshotMatches before any write.
func (a *App) parseDepartureSnapshot(token string, actorID, subjectID, groupID uint) (departureSnapshotToken, error) {
	invalid := errors.New("invalid departure snapshot")
	if token == "" || len(token) > 1024 {
		return departureSnapshotToken{}, invalid
	}
	crypt, err := a.departureSnapshotCipher()
	if err != nil {
		return departureSnapshotToken{}, err
	}
	sealed, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(sealed) < crypt.NonceSize()+crypt.Overhead() {
		return departureSnapshotToken{}, invalid
	}
	plain, err := crypt.Open(nil, sealed[:crypt.NonceSize()], sealed[crypt.NonceSize():], []byte("departure-preview"))
	if err != nil {
		return departureSnapshotToken{}, invalid
	}
	var snapshot departureSnapshotToken
	if json.Unmarshal(plain, &snapshot) != nil || snapshot.Version != departureSnapshotVersion ||
		snapshot.ActorID != actorID || snapshot.SubjectID != subjectID || snapshot.GroupID != groupID {
		return departureSnapshotToken{}, invalid
	}
	digest, err := base64.RawURLEncoding.DecodeString(snapshot.Digest)
	if err != nil || len(digest) != sha256.Size {
		return departureSnapshotToken{}, invalid
	}
	return snapshot, nil
}

func departureSnapshotMatches(snapshot departureSnapshotToken, current departureFacts) bool {
	if snapshot.Version != departureSnapshotVersion || snapshot.ActorID != current.Actor.ID ||
		snapshot.SubjectID != current.Subject.ID || snapshot.GroupID != current.GroupID {
		return false
	}
	want, err := base64.RawURLEncoding.DecodeString(snapshot.Digest)
	if err != nil || len(want) != sha256.Size {
		return false
	}
	got, err := departureFactsDigest(current)
	return err == nil && subtle.ConstantTimeCompare(want, got[:]) == 1
}

// action is "leave" when the subject goes by their own choice, else "remove".
func (f departureFacts) action() string {
	if f.Origin == policy.OccurrenceOriginDepartureSelf {
		return "leave"
	}
	return "remove"
}

func departurePreviewFromFacts(facts departureFacts, snapshot string) departurePreviewJSON {
	action := facts.action()
	out := departurePreviewJSON{
		Action: action, GroupID: facts.GroupID, GroupName: facts.GroupName,
		SubjectID: facts.Subject.ID, SubjectName: facts.SubjectName, Dates: []departureKeepableDateJSON{}, Snapshot: snapshot,
	}
	for i := range facts.Dates {
		d := &facts.Dates[i]
		out.AffectedCount++
		switch d.Registration.Status {
		case models.RegGoing:
			out.GoingCount++
		case models.RegMaybe:
			out.MaybeCount++
		case models.RegWaitlisted:
			out.WaitlistedCount++
		}
		if !d.Keepable {
			continue
		}
		out.KeepableCount++
		if d.DefaultKeep {
			out.DefaultKeepCount++
		}
		out.Dates = append(out.Dates, departureKeepableDateJSON{
			OccurrenceID: d.OccurrenceID, Date: d.OriginalDate, Title: d.Title,
			StartsAt: time.Unix(0, d.StartsAt).UTC(), EndsAt: time.Unix(0, d.EndsAt).UTC(),
			Status: d.Registration.Status, Visibility: d.EffectiveVisibility,
			DefaultKeep: d.DefaultKeep, KeepReason: d.KeepReason,
		})
	}
	return out
}

func (a *App) departurePreview(c *gin.Context, subjectID uint) {
	groupID64, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || groupID64 == 0 {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	actor := apiUser(c)
	var facts departureFacts
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		var loadErr error
		facts, loadErr = loadDepartureFacts(tx, actor, uint(groupID64), subjectID, time.Now().UTC())
		return loadErr
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		fail(c, err)
		return
	}
	snapshot, err := a.newDepartureSnapshot(facts)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not preview group departure")
		return
	}
	preview := departurePreviewFromFacts(facts, snapshot)
	c.JSON(http.StatusOK, departurePreviewResponseJSON{Departure: preview})
}

func (a *App) apiLeaveGroupDeparturePreview(c *gin.Context) {
	a.departurePreview(c, apiUser(c).ID)
}

func (a *App) apiRemoveGroupMemberDeparturePreview(c *gin.Context) {
	subjectID64, err := strconv.ParseUint(c.Param("userId"), 10, 64)
	if err != nil || subjectID64 == 0 {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	a.departurePreview(c, uint(subjectID64))
}

func lockDepartureUsers(tx *gorm.DB, actorID, subjectID uint) error {
	ids := []uint{actorID}
	if subjectID != actorID {
		ids = append(ids, subjectID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var users []models.User
	return tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).Where("id IN ?", ids).Order("id").Find(&users).Error
}

func lockDepartureOccurrences(tx *gorm.DB, facts departureFacts) (map[uint]models.EventOccurrence, error) {
	ids := make([]uint, 0, len(facts.Dates))
	for i := range facts.Dates {
		ids = append(ids, facts.Dates[i].OccurrenceID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make(map[uint]models.EventOccurrence, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	// One ordered statement takes every row lock in id order.
	var rows []models.EventOccurrence
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id IN ?", ids).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, o := range rows {
		out[o.ID] = o
	}
	for _, id := range ids {
		if _, ok := out[id]; !ok {
			return nil, gorm.ErrRecordNotFound
		}
	}
	return out, nil
}

func departureKeepSet(ids []uint, facts departureFacts) (map[uint]bool, error) {
	keepable := make(map[uint]bool, len(facts.Dates))
	for i := range facts.Dates {
		if facts.Dates[i].Keepable {
			keepable[facts.Dates[i].OccurrenceID] = true
		}
	}
	selected := make(map[uint]bool, len(ids))
	for _, id := range ids {
		if id == 0 || selected[id] || !keepable[id] {
			return nil, errDepartureStale
		}
		selected[id] = true
	}
	return selected, nil
}

// recordDepartureOccurrenceAccess keeps one exact date for a departing member:
// the grant is a write to that member's own registration row, and the added
// snapshot carries the membership and actor that authorized it. A live grant is
// left alone.
func recordDepartureOccurrenceAccess(tx *gorm.DB, facts departureFacts, d departureDateFact, scope accessScope, now time.Time, ip string) error {
	membershipID, actorID := facts.SubjectMembership.ID, facts.Actor.ID
	_, _, err := grantRegistrationAccess(tx, d.Registration.ID, scope, accessGrant{
		Origin: facts.Origin, DepartureMembershipID: &membershipID, DepartureActorID: &actorID,
	}, &actorID, now, ip)
	return err
}

func notifyDeparture(tx *gorm.DB, facts departureFacts, kept, ended int) error {
	if facts.Origin == policy.OccurrenceOriginDepartureSelf {
		leaders, err := leaderIDs(tx, facts.GroupID)
		if err != nil {
			return err
		}
		actor := facts.Subject.ID
		_, err = notify.Send(tx, notify.Msg{
			Kind: notify.MembershipLeft, To: leaders, Actor: &actor,
			SubjectType: "group", SubjectID: facts.GroupID, Link: "/groups/" + strconv.FormatUint(uint64(facts.GroupID), 10),
			Vars: notify.Vars{Person: facts.SubjectName, Group: facts.GroupName}, // group-ok: current leaders can see their group
			Data: map[string]any{"groupId": facts.GroupID, "subjectUserId": facts.Subject.ID},
			DedupeKey: "membership-left:" + strconv.FormatUint(uint64(facts.SubjectMembership.ID), 10) + ":" +
				strconv.FormatUint(uint64(facts.SubjectMembership.Tenure), 10),
		})
		return err
	}
	recipients := []uint{facts.Subject.ID}
	if facts.Subject.AccountType == models.AccountTeen {
		guardians, err := contactGuardians(tx, facts.Subject.ID)
		if err != nil {
			return err
		}
		recipients = append(recipients, guardians...)
	}
	_, err := notify.Send(tx, notify.Msg{
		Kind: notify.MembershipDeparture, To: recipients,
		SubjectType: "membership", SubjectID: facts.SubjectMembership.ID, Link: "/",
		Vars: notify.Vars{KeptCount: kept, EndedCount: ended},
		Data: map[string]any{"keptCount": kept, "endedCount": ended},
		DedupeKey: "membership-departure:" + strconv.FormatUint(uint64(facts.SubjectMembership.ID), 10) + ":" +
			strconv.FormatUint(uint64(facts.SubjectMembership.Tenure), 10),
	})
	return err
}

func (a *App) applyDeparture(tx *gorm.DB, facts departureFacts, locked map[uint]models.EventOccurrence, keep map[uint]bool, now time.Time, note, ip string) (departureConfirmationJSON, error) {
	if !policy.CanRetainOccurrenceAfterDeparture(facts.Origin, asPersonFact(facts.Actor), asPersonFact(facts.Subject),
		membershipFactRel(facts.ActorMembership), membershipFactRel(facts.SubjectMembership), facts.LeaderCount) {
		return departureConfirmationJSON{}, errDepartureStale
	}
	events := map[uint]*models.Event{}
	for i := range facts.Dates {
		d := facts.Dates[i]
		o := locked[d.OccurrenceID]
		var reg models.Registration
		if err := tx.Where("id = ? AND occurrence_id = ? AND user_id = ?", d.Registration.ID, d.OccurrenceID, facts.Subject.ID).First(&reg).Error; err != nil {
			return departureConfirmationJSON{}, errDepartureStale
		}
		scope := groupAccessScope(d.EventID, facts.GroupID)
		if keep[d.OccurrenceID] {
			// A manager grant on a kept date is superseded in two updates under
			// the locks already held: end it as departure_superseded (no
			// per-date notice) and write its terminal snapshot, then replace
			// the now-ended grant with the departure grant at the same
			// normalized instant. A live departure grant is left untouched.
			if reg.AccessLive() && reg.AccessOrigin == policy.OccurrenceOriginManagerAdd {
				if err := endRegistrationAccessQuietly(tx, &reg, scope, policy.OccurrenceDepartureSuperseded, &facts.Actor.ID, now, ip); err != nil {
					return departureConfirmationJSON{}, err
				}
			}
			if !reg.AccessLive() {
				if err := recordDepartureOccurrenceAccess(tx, facts, d, scope, now, ip); err != nil {
					return departureConfirmationJSON{}, err
				}
			}
			continue
		}
		ev := events[d.EventID]
		if ev == nil {
			ev = &models.Event{}
			if err := tx.First(ev, d.EventID).Error; err != nil {
				return departureConfirmationJSON{}, err
			}
			events[d.EventID] = ev
		}
		zero, empty := 0, ""
		// Leaving cancels the answer; it is not an explicit Can't go RSVP.
		// Unlike a manager removal, it must allow a later public self-RSVP.
		status := models.RegCancelled
		if facts.Origin == policy.OccurrenceOriginDepartureRemoval {
			status = models.RegRemoved
		}
		change, err := prepareRegistration(reg, &o, userSubject(facts.Subject.ID), status, 0, reg.AttendanceMode,
			childcareRequest{ChildcareCount: &zero, ChildcareAgesNote: &empty}, nil, now)
		if err != nil {
			return departureConfirmationJSON{}, err
		}
		if status == models.RegRemoved {
			change.next.RemovedBy = &facts.Actor.ID
			change.next.RemovalNote = note
			change.auditAction = audit.RegistrationRemoved
		}
		err = a.applyRegistrationBatch(tx, ev, &o, []registrationChange{change}, now, &facts.Actor.ID, ip)
		if err != nil {
			return departureConfirmationJSON{}, err
		}
		if reg.AccessLive() {
			reason, endedBy := policy.OccurrenceDeclined, facts.Subject.ID
			if facts.Origin == policy.OccurrenceOriginDepartureRemoval {
				reason, endedBy = policy.OccurrenceRemoved, facts.Actor.ID
			}
			if err := endRegistrationAccessQuietly(tx, &reg, scope, reason, &endedBy, now, ip); err != nil {
				return departureConfirmationJSON{}, err
			}
		}
	}

	oldRole := facts.SubjectMembership.Role
	membershipStatus := models.MembershipRemoved
	if facts.Origin == policy.OccurrenceOriginDepartureSelf {
		membershipStatus = models.MembershipLeft
	}
	membershipUpdate := tx.Model(&models.Membership{}).Where("id = ? AND status = ?", facts.SubjectMembership.ID, models.MembershipActive).
		Updates(map[string]any{"status": membershipStatus, "role": models.MembershipMember})
	if membershipUpdate.Error != nil {
		return departureConfirmationJSON{}, membershipUpdate.Error
	}
	if membershipUpdate.RowsAffected != 1 {
		return departureConfirmationJSON{}, errDepartureStale
	}
	var g models.Group
	if err := tx.First(&g, facts.GroupID).Error; err != nil {
		return departureConfirmationJSON{}, err
	}
	if oldRole == models.MembershipLeader {
		if err := fixPrimaryContact(tx, &g); err != nil {
			return departureConfirmationJSON{}, err
		}
	}
	if facts.Origin == policy.OccurrenceOriginDepartureRemoval {
		meta := map[string]any{"userId": facts.Subject.ID, "oldRole": oldRole, "keptCount": len(keep), "endedCount": len(facts.Dates) - len(keep)}
		if note != "" {
			meta["note"] = note
		}
		if err := auditAndNotifyLeadership(tx, audit.Entry{Actor: &facts.Actor.ID, Action: audit.MemberRemoved,
			TargetType: "group", TargetID: facts.GroupID, Meta: meta, IP: ip}); err != nil {
			return departureConfirmationJSON{}, err
		}
	} else {
		entry := audit.Entry{Actor: &facts.Actor.ID, Action: audit.RoleChanged, TargetType: "group", TargetID: facts.GroupID,
			Meta: map[string]any{"userId": facts.Subject.ID, "oldRole": oldRole, "newRole": roleNone, "keptCount": len(keep), "endedCount": len(facts.Dates) - len(keep)}, IP: ip}
		if err := audit.Write(tx, entry); err != nil {
			return departureConfirmationJSON{}, err
		}
		if oldRole == models.MembershipLeader {
			if err := notifyLeadership(tx, entry); err != nil {
				return departureConfirmationJSON{}, err
			}
		}
	}
	if err := notifyDeparture(tx, facts, len(keep), len(facts.Dates)-len(keep)); err != nil {
		return departureConfirmationJSON{}, err
	}
	return departureConfirmationJSON{Action: facts.action(), GroupID: facts.GroupID, SubjectID: facts.Subject.ID,
		KeptCount: len(keep), EndedCount: len(facts.Dates) - len(keep)}, nil
}

func asPersonFact(f departureUserFact) *policy.Person {
	return personOf(f.ID, f.Role, f.Status, f.AccountType)
}

func membershipFactRel(f departureMembershipFact) policy.Rel {
	return relOfStatus(f.ID != 0, f.Status, f.Role)
}

func (a *App) departureConfirm(c *gin.Context, subjectID uint) {
	groupID64, idErr := strconv.ParseUint(c.Param("id"), 10, 64)
	if idErr != nil || groupID64 == 0 || subjectID == 0 {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	var body departureConfirmRequest
	bodyErr := c.ShouldBindJSON(&body)
	body.Note = strings.TrimSpace(body.Note)
	if len(body.Note) > 500 {
		fail(c, errNoteTooLong500)
		return
	}
	actorAtRequest := apiUser(c)
	groupID := uint(groupID64)
	var result departureConfirmationJSON
	err := a.contactMutation(contactChange{UserID: subjectID}, func(tx *gorm.DB) error {
		var group models.Group
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&group, groupID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errDepartureNotFound
			}
			return err
		}
		if err := lockDepartureUsers(tx, actorAtRequest.ID, subjectID); err != nil {
			return err
		}
		preliminary, err := loadDepartureFacts(tx, actorAtRequest, groupID, subjectID, time.Now().UTC())
		if err != nil {
			return err
		}
		if bodyErr != nil {
			return errDepartureStale
		}
		snapshot, err := a.parseDepartureSnapshot(body.Snapshot, actorAtRequest.ID, subjectID, groupID)
		if err != nil {
			return errDepartureStale
		}
		locked, err := lockDepartureOccurrences(tx, preliminary)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		current, err := loadDepartureFacts(tx, actorAtRequest, groupID, subjectID, now)
		if err != nil {
			return err
		}
		if !departureSnapshotMatches(snapshot, current) {
			return errDepartureStale
		}
		keep, err := departureKeepSet(body.KeepOccurrenceIDs, current)
		if err != nil {
			return err
		}
		result, err = a.applyDeparture(tx, current, locked, keep, now, body.Note, c.ClientIP())
		return err
	})
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, departureConfirmationResponseJSON{Departure: result})
}

func (a *App) apiLeaveGroupConfirmed(c *gin.Context) {
	a.departureConfirm(c, apiUser(c).ID)
}

// Both POST and legacy DELETE routes use this preview-bound confirmation path;
// a missing snapshot is refused rather than bypassing cleanup.
func (a *App) apiRemoveGroupMemberConfirmed(c *gin.Context) {
	subjectID64, err := strconv.ParseUint(c.Param("userId"), 10, 64)
	if err != nil || subjectID64 == 0 {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	if uint(subjectID64) == apiUser(c).ID {
		jsonError(c, http.StatusBadRequest, "to leave the group, use leave")
		return
	}
	a.departureConfirm(c, uint(subjectID64))
}
