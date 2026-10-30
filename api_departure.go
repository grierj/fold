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
	errDepartureNotFound    = errors.New("departure context not found")
	errDepartureForbidden   = errors.New("departure forbidden")
	errDepartureUnavailable = errors.New("this departure is no longer available; refresh the group and check membership and leadership")
	errDepartureStale       = errors.New("this departure changed; review the updated dates before trying again")
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

func departureAccessSnapshot(a *models.OccurrenceAccess) *departureAccessFact {
	if a == nil || a.ID == 0 {
		return nil
	}
	return &departureAccessFact{
		ID: a.ID, Origin: a.Origin, OrganizerType: a.OrganizerType, OrganizerID: a.OrganizerID,
		RegistrationID: a.RegistrationID, IssuerID: ptrUint(a.IssuerID), RequestID: ptrUint(a.RequestID),
		DepartureMembershipID: ptrUint(a.DepartureMembershipID), DepartureActorID: ptrUint(a.DepartureActorID),
		Status: a.Status, GrantedAt: a.GrantedAt.UTC().UnixNano(),
	}
}

func currentDepartureRegistration(status string) bool {
	switch status {
	case models.RegGoing, models.RegMaybe, models.RegWaitlisted:
		return true
	default:
		return false
	}
}

func loadMembership(tx *gorm.DB, userID, groupID uint) (models.Membership, error) {
	var m models.Membership
	err := tx.Where("user_id = ? AND group_id = ?", userID, groupID).Limit(1).Find(&m).Error
	return m, err
}

func leaderCountIn(tx *gorm.DB, groupID uint) (int, error) {
	var n int64
	err := tx.Model(&models.Membership{}).Joins(listedUsers("memberships")).
		Where("memberships.group_id = ? AND memberships.role = ? AND memberships.status = ?", groupID, models.MembershipLeader, "active").Count(&n).Error
	return int(n), err
}

// loadDepartureFacts reads one repeatable snapshot for either self-leave or
// removal. The caller supplies the authenticated actor and the subject named by
// the route; authorization always goes through the pure departure policy.
func loadDepartureFacts(tx *gorm.DB, authenticated *models.User, groupID, subjectID uint, now time.Time) (departureFacts, error) {
	facts := departureFacts{Version: departureSnapshotVersion, GroupID: groupID, Dates: []departureDateFact{}}
	var actor models.User
	if err := tx.First(&actor, authenticated.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return facts, errDepartureForbidden
		}
		return facts, err
	}
	if actor.Status != models.UserActive || actor.SessionVersion != authenticated.SessionVersion {
		return facts, errDepartureForbidden
	}
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
	leaders, err := leaderCountIn(tx, group.ID)
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
			subject.ID, group.ID, []string{models.RegGoing, models.RegMaybe, models.RegWaitlisted}, models.OccurrenceScheduled, now).
		Preload("Occurrence").Preload("Occurrence.Event").
		Order("event_occurrences.starts_at, event_occurrences.id").Find(&registrations).Error; err != nil {
		return facts, err
	}
	occurrenceIDs := make([]uint, 0, len(registrations))
	for i := range registrations {
		occurrenceIDs = append(occurrenceIDs, registrations[i].OccurrenceID)
	}
	accesses := map[uint]models.OccurrenceAccess{}
	if len(occurrenceIDs) > 0 {
		var rows []models.OccurrenceAccess
		if err := tx.Where("subject_user_id = ? AND occurrence_id IN ? AND status = ? AND ended_at IS NULL", subject.ID, occurrenceIDs, policy.OccurrenceAccessActive).Find(&rows).Error; err != nil {
			return facts, err
		}
		for i := range rows {
			accesses[rows[i].OccurrenceID] = rows[i]
		}
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
		title := ev.Title
		if o.TitleOverride != nil {
			title = *o.TitleOverride
		}
		var access *models.OccurrenceAccess
		if row, ok := accesses[o.ID]; ok {
			copy := row
			access = &copy
		}
		facts.Dates = append(facts.Dates, departureDateFact{
			OccurrenceID: o.ID, EventID: ev.ID, OriginalDate: o.OriginalDate, Title: title,
			StartsAt: o.StartsAt.UTC().UnixNano(), EndsAt: o.EndsAt.UTC().UnixNano(), OccurrenceStatus: o.Status,
			EffectiveVisibility: visibility, Keepable: decision.Keepable, DefaultKeep: decision.DefaultKeep, KeepReason: decision.KeepReason,
			Registration: departureRegistrationSnapshot(r), Access: departureAccessSnapshot(access),
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
	if len(a.Config.SessionSecret) == 0 {
		return nil, errors.New("missing departure snapshot signing secret")
	}
	key := sha256.Sum256(append([]byte("fold:departure-preview:v3:"), a.Config.SessionSecret...))
	block, err := aes.NewCipher(key[:])
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

func departurePreviewFromFacts(facts departureFacts, snapshot string) departurePreviewJSON {
	action := "remove"
	if facts.Actor.ID == facts.Subject.ID {
		action = "leave"
	}
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
	switch {
	case errors.Is(err, errDepartureNotFound):
		jsonError(c, http.StatusNotFound, "group not found")
		return
	case errors.Is(err, errDepartureForbidden):
		jsonError(c, http.StatusForbidden, "forbidden")
		return
	case errors.Is(err, errDepartureUnavailable):
		jsonError(c, http.StatusConflict, errDepartureUnavailable.Error())
		return
	case err != nil:
		jsonError(c, http.StatusInternalServerError, "could not preview group departure")
		return
	}
	snapshot, err := a.newDepartureSnapshot(facts)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not preview group departure")
		return
	}
	preview := departurePreviewFromFacts(facts, snapshot)
	c.JSON(http.StatusOK, gin.H{"departure": preview})
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

func recordDepartureOccurrenceAccess(tx *gorm.DB, facts departureFacts, d departureDateFact, now time.Time, ip string) error {
	membershipID, actorID := facts.SubjectMembership.ID, facts.Actor.ID
	row := models.OccurrenceAccess{
		SubjectUserID: facts.Subject.ID, OccurrenceID: d.OccurrenceID, EventID: d.EventID,
		OrganizerType: policy.ContactGroup, OrganizerID: facts.GroupID, Origin: facts.Origin,
		RegistrationID: d.Registration.ID, DepartureMembershipID: &membershipID, DepartureActorID: &actorID,
		Status: policy.OccurrenceAccessActive, GrantedAt: now,
	}
	if err := tx.Create(&row).Error; err != nil {
		return err
	}
	return audit.Write(tx, audit.Entry{Actor: &actorID, Action: audit.OccurrenceAccessAdded,
		TargetType: "occurrence_access", TargetID: row.ID, IP: ip,
		Meta: map[string]any{"subjectUserId": facts.Subject.ID, "eventId": d.EventID, "occurrenceId": d.OccurrenceID, "origin": facts.Origin}})
}

func departureLeaderIDs(tx *gorm.DB, groupID uint) ([]uint, error) {
	var ids []uint
	err := tx.Model(&models.Membership{}).Joins(listedUsers("memberships")).
		Where("memberships.group_id = ? AND memberships.status = ? AND memberships.role = ?", groupID, "active", models.MembershipLeader).
		Order("memberships.user_id").Pluck("memberships.user_id", &ids).Error
	return ids, err
}

func notifyDeparture(tx *gorm.DB, facts departureFacts, kept, ended int) ([]uint, error) {
	if facts.Origin == policy.OccurrenceOriginDepartureSelf {
		leaders, err := departureLeaderIDs(tx, facts.GroupID)
		if err != nil {
			return nil, err
		}
		actor := facts.Subject.ID
		return notify.Send(tx, notify.Msg{
			Kind: notify.MembershipLeft, To: leaders, Actor: &actor,
			SubjectType: "group", SubjectID: facts.GroupID, Link: "/groups/" + strconv.FormatUint(uint64(facts.GroupID), 10),
			Vars: notify.Vars{Person: facts.SubjectName, Group: facts.GroupName}, // group-ok: current leaders can see their group
			Data: map[string]any{"groupId": facts.GroupID, "subjectUserId": facts.Subject.ID},
			DedupeKey: "membership-left:" + strconv.FormatUint(uint64(facts.SubjectMembership.ID), 10) + ":" +
				strconv.FormatUint(uint64(facts.SubjectMembership.Tenure), 10),
		})
	}
	recipients := []uint{facts.Subject.ID}
	if facts.Subject.AccountType == models.AccountTeen {
		guardians, err := contactGuardians(tx, facts.Subject.ID)
		if err != nil {
			return nil, err
		}
		recipients = append(recipients, guardians...)
	}
	return notify.Send(tx, notify.Msg{
		Kind: notify.MembershipDeparture, To: recipients,
		SubjectType: "membership", SubjectID: facts.SubjectMembership.ID, Link: "/",
		Vars: notify.Vars{KeptCount: kept, EndedCount: ended},
		Data: map[string]any{"keptCount": kept, "endedCount": ended},
		DedupeKey: "membership-departure:" + strconv.FormatUint(uint64(facts.SubjectMembership.ID), 10) + ":" +
			strconv.FormatUint(uint64(facts.SubjectMembership.Tenure), 10),
	})
}

func (a *App) applyDeparture(tx *gorm.DB, facts departureFacts, locked map[uint]models.EventOccurrence, keep map[uint]bool, now time.Time, note, ip string, mailed *[]uint) (departureConfirmationJSON, error) {
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
		var access models.OccurrenceAccess
		if err := tx.Where("subject_user_id = ? AND occurrence_id = ? AND status = ? AND ended_at IS NULL", facts.Subject.ID, d.OccurrenceID, policy.OccurrenceAccessActive).Limit(1).Find(&access).Error; err != nil {
			return departureConfirmationJSON{}, err
		}
		if keep[d.OccurrenceID] {
			if access.ID != 0 && access.Origin == policy.OccurrenceOriginManagerAdd {
				if err := endOccurrenceAccessWithoutNoticeIn(tx, &access, policy.OccurrenceDepartureSuperseded, &facts.Actor.ID, now, ip); err != nil {
					return departureConfirmationJSON{}, err
				}
				access = models.OccurrenceAccess{}
			}
			if access.ID == 0 {
				if err := recordDepartureOccurrenceAccess(tx, facts, d, now, ip); err != nil {
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
		ids, err := a.applyRegistrationBatch(tx, ev, &o, []registrationChange{change}, now, facts.Actor.ID, ip)
		if err != nil {
			return departureConfirmationJSON{}, err
		}
		*mailed = append(*mailed, ids...)
		if access.ID != 0 {
			reason, endedBy := policy.OccurrenceDeclined, facts.Subject.ID
			if facts.Origin == policy.OccurrenceOriginDepartureRemoval {
				reason, endedBy = policy.OccurrenceRemoved, facts.Actor.ID
			}
			if err := endOccurrenceAccessWithoutNoticeIn(tx, &access, reason, &endedBy, now, ip); err != nil {
				return departureConfirmationJSON{}, err
			}
		}
	}

	oldRole := facts.SubjectMembership.Role
	membershipStatus := memberRemoved
	if facts.Origin == policy.OccurrenceOriginDepartureSelf {
		membershipStatus = models.MembershipLeft
	}
	membershipUpdate := tx.Model(&models.Membership{}).Where("id = ? AND status = ?", facts.SubjectMembership.ID, "active").
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
		ids, err := auditAndNotifyLeadership(tx, audit.Entry{Actor: &facts.Actor.ID, Action: audit.MemberRemoved,
			TargetType: "group", TargetID: facts.GroupID, Meta: meta, IP: ip})
		if err != nil {
			return departureConfirmationJSON{}, err
		}
		*mailed = append(*mailed, ids...)
	} else {
		entry := audit.Entry{Actor: &facts.Actor.ID, Action: audit.RoleChanged, TargetType: "group", TargetID: facts.GroupID,
			Meta: map[string]any{"userId": facts.Subject.ID, "oldRole": oldRole, "newRole": "none", "keptCount": len(keep), "endedCount": len(facts.Dates) - len(keep)}, IP: ip}
		if err := audit.Write(tx, entry); err != nil {
			return departureConfirmationJSON{}, err
		}
		if oldRole == models.MembershipLeader {
			ids, err := notifyLeadership(tx, entry)
			if err != nil {
				return departureConfirmationJSON{}, err
			}
			*mailed = append(*mailed, ids...)
		}
	}
	ids, err := notifyDeparture(tx, facts, len(keep), len(facts.Dates)-len(keep))
	if err != nil {
		return departureConfirmationJSON{}, err
	}
	*mailed = append(*mailed, ids...)
	action := "remove"
	if facts.Origin == policy.OccurrenceOriginDepartureSelf {
		action = "leave"
	}
	return departureConfirmationJSON{Action: action, GroupID: facts.GroupID, SubjectID: facts.Subject.ID,
		KeptCount: len(keep), EndedCount: len(facts.Dates) - len(keep)}, nil
}

func asPersonFact(f departureUserFact) *policy.Person {
	return &policy.Person{ID: f.ID, Admin: f.Role == models.RoleAdmin && f.Status == models.UserActive, Teen: f.AccountType == models.AccountTeen}
}

func membershipFactRel(f departureMembershipFact) policy.Rel {
	active := f.ID != 0 && f.Status == "active"
	return policy.Rel{Member: active, Leader: active && f.Role == models.MembershipLeader, Removed: f.ID != 0 && f.Status == memberRemoved}
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
		jsonError(c, http.StatusBadRequest, "note must be at most 500 characters")
		return
	}
	actorAtRequest := apiUser(c)
	groupID := uint(groupID64)
	var result departureConfirmationJSON
	var mailed []uint
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
		result, err = a.applyDeparture(tx, current, locked, keep, now, body.Note, c.ClientIP(), &mailed)
		return err
	})
	switch {
	case errors.Is(err, errDepartureNotFound):
		jsonError(c, http.StatusNotFound, "group not found")
		return
	case errors.Is(err, errDepartureForbidden):
		jsonError(c, http.StatusForbidden, "forbidden")
		return
	case errors.Is(err, errDepartureUnavailable):
		jsonError(c, http.StatusConflict, errDepartureUnavailable.Error())
		return
	case errors.Is(err, errDepartureStale):
		jsonError(c, http.StatusConflict, errDepartureStale.Error())
		return
	case err != nil:
		jsonError(c, http.StatusInternalServerError, "could not complete group departure")
		return
	}
	a.deliverNotificationEmails(mailed)
	c.JSON(http.StatusOK, gin.H{"departure": result})
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
