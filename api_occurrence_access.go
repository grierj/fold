package main

import (
	"net/http"
	"strconv"
	"time"

	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// occurrenceRead holds current facts for ONE invitation, never series authority.
// Mutation owners must reload these facts under the shared contact gate and their
// domain locks; this read loader does not authorize or serialize any mutation.
type occurrenceRead struct {
	Event        models.Event
	Occurrence   models.EventOccurrence
	Access       policy.OccurrenceAccess
	Facts        policy.OccurrenceFacts
	Account      policy.ContactAccount
	Registration models.Registration
}

func occurrenceAccessPolicy(row models.OccurrenceAccess) policy.OccurrenceAccess {
	out := policy.OccurrenceAccess{SubjectUserID: row.SubjectUserID, OccurrenceID: row.OccurrenceID,
		EventID: row.EventID, Organizer: policy.OrganizerScope{Type: row.OrganizerType, ID: row.OrganizerID},
		IssuerID: row.IssuerID, RegistrationID: row.RegistrationID, Status: row.Status, GrantedAt: row.GrantedAt, EndedAt: row.EndedAt}
	if row.RequestID != nil {
		out.RequestID = *row.RequestID
	}
	return out
}

// scopedOccurrenceReads loads exact candidates independently of visibleEvents.
// q selects occurrence IDs (window/date and event filters). Parent IDs NEVER
// enter the ordinary visible set. All related facts are batched, including live
// registration provenance; no attendee/contact directory is loaded here.
func (a *App) scopedOccurrenceReads(viewer *models.User, q *gorm.DB, now time.Time) (map[uint]*occurrenceRead, error) {
	out := map[uint]*occurrenceRead{}
	if viewer == nil {
		return out, nil
	}
	var accesses []models.OccurrenceAccess
	err := a.DB.Where("subject_user_id = ? AND status = ? AND ended_at IS NULL AND occurrence_id IN (?)", viewer.ID,
		policy.OccurrenceAccessActive, q.Session(&gorm.Session{}).Model(&models.EventOccurrence{}).Select("id")).Find(&accesses).Error
	if err != nil || len(accesses) == 0 {
		return out, err
	}
	reads, err := occurrenceInvitationReadsIn(a.DB, accesses, now)
	if err != nil {
		return nil, err
	}
	for _, row := range accesses {
		if read := reads[row.ID]; read != nil {
			out[row.OccurrenceID] = read
		}
	}
	return out, nil
}

// occurrenceInvitationReadsIn batches current exact-date authority. Its keys are
// access IDs, so a collection of different recipients never shares authority.
// Mutation callers still own the contact gate and relevant domain locks.
func occurrenceInvitationReadsIn(tx *gorm.DB, accesses []models.OccurrenceAccess, now time.Time) (map[uint]*occurrenceRead, error) {
	out := map[uint]*occurrenceRead{}
	if len(accesses) == 0 {
		return out, nil
	}
	facts, err := loadOccurrenceInvitationFacts(tx, accesses, now)
	if err != nil {
		return nil, err
	}
	for _, row := range accesses {
		occ, ev, reg := facts.occurrences[row.OccurrenceID], facts.events[row.EventID], facts.registrations[row.RegistrationID]
		actor := facts.accounts[row.SubjectUserID]
		if reg.UserID == nil || *reg.UserID != row.SubjectUserID || occ.ID == 0 || ev.ID == 0 || reg.ID == 0 || reg.OccurrenceID != occ.ID || occ.EventID != ev.ID {
			continue
		}
		organizer := policy.OrganizerScope{Type: policy.ContactEvent, ID: ev.ID}
		live := true
		if ev.GroupID != nil {
			organizer = policy.OrganizerScope{Type: policy.ContactGroup, ID: *ev.GroupID}
			g := facts.groups[*ev.GroupID]
			live = g.ID != 0 && g.Status == models.GroupActive
		}
		f := policy.OccurrenceFacts{ID: occ.ID, EventID: ev.ID, Organizer: organizer, ContextActive: live, Scheduled: occ.Status == models.OccurrenceScheduled}
		access := occurrenceAccessPolicy(row)
		if policy.CanSeeOccurrence(actor, false, access, f, now) {
			out[row.ID] = &occurrenceRead{Event: ev, Occurrence: occ, Registration: reg, Account: actor, Access: access, Facts: f}
		}
	}
	return out, nil
}

type occurrenceInvitationFacts struct {
	users         map[uint]*models.User
	accounts      map[uint]policy.ContactAccount
	occurrences   map[uint]models.EventOccurrence
	events        map[uint]models.Event
	groups        map[uint]models.Group
	registrations map[uint]models.Registration
}

func loadOccurrenceInvitationFacts(tx *gorm.DB, accesses []models.OccurrenceAccess, now time.Time) (*occurrenceInvitationFacts, error) {
	subjectIDs := []uint{}
	for _, row := range accesses {
		subjectIDs = append(subjectIDs, row.SubjectUserID)
	}
	users, accounts, err := occurrenceAccountsIn(tx, subjectIDs, now, time.UTC)
	if err != nil {
		return nil, err
	}
	ids, eventIDs, regIDs := []uint{}, []uint{}, []uint{}
	for _, row := range accesses {
		ids = append(ids, row.OccurrenceID)
		eventIDs = append(eventIDs, row.EventID)
		regIDs = append(regIDs, row.RegistrationID)
	}
	var occs []models.EventOccurrence
	if err := tx.Where("id IN ?", ids).Find(&occs).Error; err != nil {
		return nil, err
	}
	occByID := map[uint]models.EventOccurrence{}
	for _, row := range occs {
		occByID[row.ID] = row
	}
	var evs []models.Event
	if err := tx.Where("id IN ?", eventIDs).Find(&evs).Error; err != nil {
		return nil, err
	}
	evByID, groupIDs := map[uint]models.Event{}, []uint{}
	for _, row := range evs {
		evByID[row.ID] = row
		if row.GroupID != nil {
			groupIDs = append(groupIDs, *row.GroupID)
		}
	}
	var groups []models.Group
	if len(groupIDs) > 0 {
		if err := tx.Where("id IN ?", groupIDs).Find(&groups).Error; err != nil {
			return nil, err
		}
	}
	groupByID := map[uint]models.Group{}
	for _, row := range groups {
		groupByID[row.ID] = row
	}
	var regs []models.Registration
	if err := tx.Where("id IN ? AND status IN ?", regIDs,
		[]string{models.RegGoing, models.RegMaybe, models.RegWaitlisted}).Find(&regs).Error; err != nil {
		return nil, err
	}
	regByID := map[uint]models.Registration{}
	for _, row := range regs {
		regByID[row.ID] = row
	}
	return &occurrenceInvitationFacts{users, accounts, occByID, evByID, groupByID, regByID}, nil
}

// endReason preserves the scalar lifecycle precedence, including retained past
// dates. It does not reconsider issuer authority or a recipient's add block.
func (f *occurrenceInvitationFacts) endReason(row models.OccurrenceAccess) string {
	u := f.users[row.SubjectUserID]
	if u == nil || !u.CanSignIn() {
		return policy.ContactAccountDisabled
	}
	if u.AccountType == models.AccountTeen && !f.accounts[u.ID].Active {
		return policy.OccurrenceConsentLost
	}
	ev := f.events[row.EventID]
	if ev.ID == 0 {
		return policy.OccurrenceEventDeleted
	}
	organizer := policy.OrganizerScope{Type: policy.ContactEvent, ID: ev.ID}
	if ev.GroupID != nil {
		organizer = policy.OrganizerScope{Type: policy.ContactGroup, ID: *ev.GroupID}
		g := f.groups[*ev.GroupID]
		if g.ID == 0 {
			return policy.OccurrenceGroupDeleted
		}
		if g.Status != models.GroupActive {
			return policy.OccurrenceContextChanged
		}
	}
	if organizer != (policy.OrganizerScope{Type: row.OrganizerType, ID: row.OrganizerID}) {
		return policy.OccurrenceContextChanged
	}
	occ := f.occurrences[row.OccurrenceID]
	if occ.ID == 0 || occ.EventID != row.EventID {
		return policy.OccurrenceDeleted
	}
	if occ.Status != models.OccurrenceScheduled {
		return policy.OccurrenceCancelled
	}
	reg := f.registrations[row.RegistrationID]
	if reg.ID == 0 || reg.UserID == nil || *reg.UserID != row.SubjectUserID || reg.OccurrenceID != row.OccurrenceID {
		return policy.OccurrenceContextChanged
	}
	return ""
}

func occurrenceDatePath(eventID uint, date string) string {
	return "/events/" + strconv.FormatUint(uint64(eventID), 10) + "/dates/" + date
}

// dateOccurrenceJSON deliberately cannot hold group, host, series, attendee,
// contact or management data. Both ordinary and invitation readers use it.
type dateOccurrenceJSON struct {
	ID               uint      `json:"id"`
	EventID          uint      `json:"eventId"`
	Date             string    `json:"date"`
	DateURL          string    `json:"dateUrl"`
	Title            string    `json:"title"`
	Location         string    `json:"location"`
	StartsAt         time.Time `json:"startsAt"`
	EndsAt           time.Time `json:"endsAt"`
	StartsLocal      string    `json:"startsLocal"`
	EndsLocal        string    `json:"endsLocal"`
	TZID             string    `json:"tzid"`
	DurationMinutes  int       `json:"durationMinutes"`
	RemoteAvailable  bool      `json:"remoteAvailable"`
	GuestsAllowed    int       `json:"guestsAllowed"`
	MyRegistration   *string   `json:"myRegistration"`
	MyAttendanceMode *string   `json:"myAttendanceMode,omitempty"`
	MyGuestCount     *int      `json:"myGuestCount,omitempty"`
	VideoProvider    string    `json:"videoProvider"`
	VideoURL         string    `json:"videoUrl,omitempty"`
	VideoHidden      bool      `json:"videoHidden"`
	CanSeeVideo      bool      `json:"canSeeVideo"`
	CanSeeAttendees  bool      `json:"canSeeAttendees"`
	CanDecline       bool      `json:"canDecline"`
	CanBlock         bool      `json:"canBlock"`
}

func presentOccurrenceDate(ev *models.Event, occ *models.EventOccurrence, groupLocation string, ordinary, manager, attendees bool, reg models.Registration, invited *occurrenceRead, now time.Time) dateOccurrenceJSON {
	full := occurrenceToJSON(ev, occ, groupLocation)
	var actor policy.ContactAccount
	var access policy.OccurrenceAccess
	var facts policy.OccurrenceFacts
	if invited != nil {
		actor, access, facts = invited.Account, invited.Access, invited.Facts
	}
	canVideo := policy.CanSeeOccurrenceVideo(actor, ordinary, manager, access, facts, now, videoRevealOf(ev), reg.Status)
	out := dateOccurrenceJSON{ID: occ.ID, EventID: ev.ID, Date: occ.OriginalDate, DateURL: occurrenceDatePath(ev.ID, occ.OriginalDate),
		Title: full.Title, Location: full.Location, StartsAt: full.StartsAt, EndsAt: full.EndsAt,
		StartsLocal: full.StartsLocal, EndsLocal: full.EndsLocal, TZID: full.TZID, DurationMinutes: full.DurationMinutes,
		RemoteAvailable: full.RemoteAvailable && (full.RemoteCapacity == nil || *full.RemoteCapacity > 0), GuestsAllowed: full.GuestsAllowed,
		VideoProvider: videoProviderOf(ev), CanSeeVideo: canVideo,
		CanSeeAttendees: policy.CanSeeOccurrenceAttendees(ordinary, attendees),
		CanDecline:      invited != nil && policy.CanEndOccurrenceAccess(actor, false, access),
		CanBlock:        invited != nil && policy.CanBlockManagerAdds(actor, access)}
	if reg.ID > 0 {
		out.MyRegistration = &reg.Status
		mode := attendanceModeOf(reg.AttendanceMode)
		out.MyAttendanceMode = &mode
		out.MyGuestCount = &reg.GuestCount
	}
	if link := videoLinkOf(ev); link != "" {
		if canVideo {
			out.VideoURL = link
		} else {
			out.VideoHidden = true
		}
	}
	return out
}

func (a *App) apiGetOccurrenceDate(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	date := c.Param("date")
	if _, dateErr := time.Parse("2006-01-02", date); err != nil || id == 0 || dateErr != nil {
		jsonError(c, http.StatusNotFound, "event not found")
		return
	}
	me, now := apiUser(c), time.Now().UTC()
	vs, err := a.visibleEvents(me, a.DB.Where("id = ?", id))
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load this date")
		return
	}
	q := a.DB.Where("event_id = ? AND original_date = ? AND status = ?", id, date, models.OccurrenceScheduled)
	scoped, err := a.scopedOccurrenceReads(me, q, now)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load this date")
		return
	}
	var occ models.EventOccurrence
	if err := q.First(&occ).Error; err != nil {
		jsonError(c, http.StatusNotFound, "event not found")
		return
	}
	ev, ordinary := vs.ByID[uint(id)]
	invited := scoped[occ.ID]
	if !ordinary && invited == nil {
		jsonError(c, http.StatusNotFound, "event not found")
		return
	}
	var reg models.Registration
	groupLocation, manager, attendees := "", false, false
	if ordinary {
		grp, rel := groupAndRel(ev, vs.Groups, vs.Rels)
		if shown := a.groupShownTo(me, grp, rel); shown != nil {
			groupLocation = shown.Location
		}
		gp := policy.Group{}
		if grp != nil {
			gp = asGroup(grp)
		}
		manager = policy.CanManageEvent(asPerson(me), viewOf(ev, vs.Hosted[ev.ID]), rel)
		attendees = policy.CanSeeAttendeeNames(asPerson(me), viewOf(ev, vs.Hosted[ev.ID]), gp, rel)
		a.DB.Where("user_id = ? AND occurrence_id = ? AND status <> ?", me.ID, occ.ID, models.RegCancelled).Find(&reg)
	} else {
		ev = &invited.Event
		reg = invited.Registration
	}
	c.JSON(http.StatusOK, gin.H{"occurrence": presentOccurrenceDate(ev, &occ, groupLocation, ordinary, manager, attendees, reg, invited, now)})
}

// occurrenceAccountsIn uses the same latest-consent ordering as the single-user
// loader. Facts are local to the caller's transaction, never cached authority.
func occurrenceAccountsIn(tx *gorm.DB, ids []uint, now time.Time, zone *time.Location) (map[uint]*models.User, map[uint]policy.ContactAccount, error) {
	users := map[uint]*models.User{}
	accounts := map[uint]policy.ContactAccount{}
	if len(ids) == 0 {
		return users, accounts, nil
	}
	var rows []models.User
	if err := tx.Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	teens := []uint{}
	for i := range rows {
		u := &rows[i]
		users[u.ID] = u
		accounts[u.ID] = contactAccount(u, now, zone)
		if u.AccountType == models.AccountTeen {
			teens = append(teens, u.ID)
		}
	}
	if len(teens) > 0 {
		var consents []models.TeenConsent
		if err := tx.Select("DISTINCT ON (teen_user_id) *").Where("teen_user_id IN ? AND scope = ?", teens, models.TeenConsentAccount).Order("teen_user_id, at DESC, id DESC").Find(&consents).Error; err != nil {
			return nil, nil, err
		}
		granted := map[uint]bool{}
		for _, c := range consents {
			granted[c.TeenUserID] = c.Status == models.ConsentGranted
		}
		for _, id := range teens {
			a := accounts[id]
			a.Active = a.Active && granted[id]
			accounts[id] = a
		}
	}
	return users, accounts, nil
}
