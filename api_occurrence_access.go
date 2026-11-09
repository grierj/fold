package main

import (
	"net/http"
	"strconv"
	"time"

	"fold/internal/events"
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

// scopedOccurrenceReads loads exact candidates independently of series-filtered eventCandidates.
// q selects occurrence IDs (window/date and event filters). Parent IDs NEVER
// enter the ordinary visible set. All related facts are batched, including live
// registration provenance; no attendee/contact directory is loaded here.
func (a *App) scopedOccurrenceReads(viewer *models.User, q *gorm.DB, now time.Time) (map[uint]*occurrenceRead, error) {
	out := map[uint]*occurrenceRead{}
	if viewer == nil {
		return out, nil
	}
	var grants []models.Registration
	err := a.DB.Where("registrations.user_id = ? AND "+registrationAccessLiveSQL+" AND registrations.occurrence_id IN (?)", viewer.ID,
		q.Session(&gorm.Session{}).Model(&models.EventOccurrence{}).Select("id")).Find(&grants).Error
	if err != nil || len(grants) == 0 {
		return out, err
	}
	reads, err := occurrenceInvitationReadsIn(a.DB, grants, now)
	if err != nil {
		return nil, err
	}
	for _, row := range grants {
		if read := reads[row.ID]; read != nil {
			out[row.OccurrenceID] = read
		}
	}
	return out, nil
}

// occurrenceInvitationReadsIn batches current exact-date authority. Its keys are
// registration IDs, so a collection of different recipients never shares
// authority. The rows are registrations carrying live provenance; event and
// organizer are derived from each row's date, never stored twice. Mutation
// callers still own the contact gate and relevant domain locks.
func occurrenceInvitationReadsIn(tx *gorm.DB, grants []models.Registration, now time.Time) (map[uint]*occurrenceRead, error) {
	out := map[uint]*occurrenceRead{}
	if len(grants) == 0 {
		return out, nil
	}
	facts, err := loadOccurrenceInvitationFacts(tx, grants, now)
	if err != nil {
		return nil, err
	}
	for _, row := range grants {
		if row.UserID == nil || !currentDepartureRegistration(row.Status) {
			continue
		}
		occ := facts.occurrences[row.OccurrenceID]
		ev := facts.events[occ.EventID]
		actor := facts.accounts[*row.UserID]
		if occ.ID == 0 || ev.ID == 0 || occ.EventID != ev.ID {
			continue
		}
		scope := accessScopeOf(&ev)
		f := occurrenceFactsOf(&occ, &ev, facts.group(&ev))
		access := occurrenceAccessPolicy(row, scope)
		if policy.CanSeeOccurrence(actor, false, access, f, now) {
			out[row.ID] = &occurrenceRead{Event: ev, Occurrence: occ, Registration: row, Account: actor, Access: access, Facts: f}
		}
	}
	return out, nil
}

// occurrenceFactsOf is the one builder of the lifecycle facts policy judges an
// exact date by, from rows already loaded: the date, its event, and the
// event's group (nil for a church-wide event, and for a group that is gone).
func occurrenceFactsOf(occ *models.EventOccurrence, ev *models.Event, group *models.Group) policy.OccurrenceFacts {
	f := accessScopeOf(ev)
	live := ev.GroupID == nil || (group != nil && group.Status == models.GroupActive)
	return policy.OccurrenceFacts{
		ID: occ.ID, EventID: ev.ID, Organizer: f.Organizer, ContextActive: live,
		Scheduled: occ.Status == models.OccurrenceScheduled, EndsAt: occ.EndsAt,
	}
}

type occurrenceInvitationFacts struct {
	users       map[uint]*models.User
	accounts    map[uint]policy.ContactAccount
	occurrences map[uint]models.EventOccurrence
	events      map[uint]models.Event
	groups      map[uint]*models.Group
}

func loadOccurrenceInvitationFacts(tx *gorm.DB, grants []models.Registration, now time.Time) (*occurrenceInvitationFacts, error) {
	subjectIDs, ids := []uint{}, []uint{}
	for _, row := range grants {
		if row.UserID != nil {
			subjectIDs = append(subjectIDs, *row.UserID)
		}
		ids = append(ids, row.OccurrenceID)
	}
	users, accounts, err := occurrenceAccountsIn(tx, subjectIDs, now)
	if err != nil {
		return nil, err
	}
	var occs []models.EventOccurrence
	if err := tx.Where("id IN ?", ids).Find(&occs).Error; err != nil {
		return nil, err
	}
	occByID, eventIDs := map[uint]models.EventOccurrence{}, []uint{}
	for _, row := range occs {
		occByID[row.ID] = row
		eventIDs = append(eventIDs, row.EventID)
	}
	var evs []models.Event
	if len(eventIDs) > 0 {
		if err := tx.Where("id IN ?", eventIDs).Find(&evs).Error; err != nil {
			return nil, err
		}
	}
	evByID, groupIDs := map[uint]models.Event{}, []uint{}
	for _, row := range evs {
		evByID[row.ID] = row
		if row.GroupID != nil {
			groupIDs = append(groupIDs, *row.GroupID)
		}
	}
	groupByID, err := groupsByID(tx, groupIDs, false)
	if err != nil {
		return nil, err
	}
	return &occurrenceInvitationFacts{users, accounts, occByID, evByID, groupByID}, nil
}

// group is the loaded group of ev, nil for a church-wide event or a missing group.
func (f *occurrenceInvitationFacts) group(ev *models.Event) *models.Group {
	if ev.GroupID == nil {
		return nil
	}
	return f.groups[*ev.GroupID]
}

// endReason preserves the scalar lifecycle precedence, including retained past
// dates. It does not reconsider issuer authority or a recipient's add block.
// The organizer cannot disagree with the event any more (it is derived from the
// event), so the deleted and context-changed reasons come from the event, group
// and registration liveness checks alone.
func (f *occurrenceInvitationFacts) endReason(row models.Registration, now time.Time) string {
	if row.UserID == nil {
		return policy.OccurrenceContextChanged
	}
	u := f.users[*row.UserID]
	if u == nil || !u.CanSignIn() {
		return policy.ContactAccountDisabled
	}
	if u.AccountType == models.AccountTeen && !f.accounts[u.ID].Active {
		return policy.OccurrenceConsentLost
	}
	occ := f.occurrences[row.OccurrenceID]
	if occ.ID == 0 {
		return policy.OccurrenceDeleted
	}
	ev := f.events[occ.EventID]
	if ev.ID == 0 {
		return policy.OccurrenceEventDeleted
	}
	group := f.group(&ev)
	if ev.GroupID != nil && group == nil {
		return policy.OccurrenceGroupDeleted
	}
	lifecycle := occurrenceFactsOf(&occ, &ev, group)
	if !lifecycle.ContextActive {
		return policy.OccurrenceContextChanged
	}
	if !lifecycle.Scheduled {
		return policy.OccurrenceCancelled
	}
	if row.AccessOrigin != policy.OccurrenceOriginManagerAdd && !occ.EndsAt.IsZero() && !now.Before(occ.EndsAt) {
		return policy.OccurrenceEnded
	}
	if !currentDepartureRegistration(row.Status) {
		return policy.OccurrenceContextChanged
	}
	return ""
}

// isCalendarDate is true for a real YYYY-MM-DD. Date inputs that go into a
// lookup check it first: events.original_date is a date column, so a
// shape-only string such as 2026-02-30 would fail inside Postgres (SQLSTATE
// 22008) instead of simply matching no row.
func isCalendarDate(s string) bool {
	_, err := models.ParseCalendarDate(s)
	return err == nil
}

// firstScheduledOn loads the scheduled date of an event, or reports not found
// for a date that cannot exist.
func firstScheduledOn(db *gorm.DB, eventID uint, date string, out *models.EventOccurrence) error {
	if !isCalendarDate(date) {
		return gorm.ErrRecordNotFound
	}
	return db.Where("event_id = ? AND original_date = ? AND status = ?", eventID, date, models.OccurrenceScheduled).First(out).Error
}

func occurrenceDatePath(eventID uint, date string) string {
	return "/events/" + strconv.FormatUint(uint64(eventID), 10) + "/dates/" + date
}

// dateOccurrenceJSON never carries host people, series, attendee, contact or
// management data. Only ordinary reads may attach a group label/link, after an
// independent groupShownTo check; invitation-only reads omit that metadata.
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
	Visibility       string    `json:"visibility,omitempty"`
	CanRegister      bool      `json:"canRegister,omitempty"`
	HostedBy         string    `json:"hostedBy,omitempty"`
	GroupID          *uint     `json:"groupId,omitempty"`
	GroupName        string    `json:"groupName,omitempty"`
	// Room is the date's confirmed room, shown as its place (9.2, M2.1b).
	Room *occurrenceRoomJSON `json:"room,omitempty"`
	// Reminders is this date's email reminders (M3.6b), only for its
	// event's managers; absent for everyone else.
	Reminders *dateRemindersJSON `json:"reminders,omitempty"`
}

func (a *App) apiGetOccurrenceDate(c *gin.Context) {
	if out, ok := a.loadOccurrenceDate(c); ok {
		c.JSON(http.StatusOK, eventDateResponseJSON{Occurrence: out})
	}
}

// apiOccurrenceCalendar downloads one date as an .ics file (DEC-93). It goes
// through the same load as the date page, so it shows exactly what that page
// may show: the video link only when the page would, and no description,
// hosts or attendees. The UID matches the feeds', so a calendar that also
// subscribes to a feed keeps one copy.
func (a *App) apiOccurrenceCalendar(c *gin.Context) {
	out, ok := a.loadOccurrenceDate(c)
	if !ok {
		return
	}
	status := ""
	if out.MyRegistration != nil {
		status = calendarStatus(*out.MyRegistration, false)
	}
	body := events.RenderFeed(events.Feed{
		Host:        a.feedHost(),
		WindowStart: out.StartsAt.Add(-24 * time.Hour),
		WindowEnd:   out.EndsAt.Add(24 * time.Hour),
		Singles:     []events.FeedSingle{dateFeedSingle(out, a.emailBaseURL(), time.Time{}, status)},
	})
	c.Header("Content-Disposition", `attachment; filename="event-`+out.Date+`.ics"`)
	c.Data(http.StatusOK, "text/calendar; charset=utf-8", body)
}

// loadOccurrenceDate authorizes and presents one date for the date page and
// its calendar download. It answers the error itself when ok is false.
func (a *App) loadOccurrenceDate(c *gin.Context) (dateOccurrenceJSON, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	dateStr := c.Param("date")
	if _, dateErr := models.ParseCalendarDate(dateStr); err != nil || id == 0 || dateErr != nil {
		jsonError(c, http.StatusNotFound, "event not found")
		return dateOccurrenceJSON{}, false
	}
	me, now := apiUser(c), time.Now().UTC()
	vs, err := a.dateCandidates(me, a.DB.Where("id = ?", id))
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load this date")
		return dateOccurrenceJSON{}, false
	}
	q := a.DB.Where("event_id = ? AND original_date = ? AND status = ?", id, dateStr, models.OccurrenceScheduled)
	scoped, err := a.scopedOccurrenceReads(me, q, now)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load this date")
		return dateOccurrenceJSON{}, false
	}
	var occ models.EventOccurrence
	if err := q.First(&occ).Error; err != nil {
		jsonError(c, http.StatusNotFound, "event not found")
		return dateOccurrenceJSON{}, false
	}
	date, ok := a.projectDate(me, vs, &occ, scoped[occ.ID])
	if !ok {
		jsonError(c, http.StatusNotFound, "event not found")
		return dateOccurrenceJSON{}, false
	}
	var reg models.Registration
	if date.Ordinary {
		if me != nil {
			if err := a.DB.Where("user_id = ? AND occurrence_id = ? AND status <> ?", me.ID, occ.ID, models.RegCancelled).Limit(1).Find(&reg).Error; err != nil {
				jsonError(c, http.StatusInternalServerError, "could not load this date")
				return dateOccurrenceJSON{}, false
			}
		}
	} else {
		reg = date.Invited.Registration
	}
	out := date.dateJSON(&siteNameOnce{a: a}, reg, now)
	out.Room = a.occurrenceRooms(a.DB, []uint{occ.ID})[occ.ID]
	if date.Ordinary && date.Manager {
		if out.Reminders, err = a.dateReminders(date.Event, &occ); err != nil {
			jsonError(c, http.StatusInternalServerError, "could not load this date")
			return dateOccurrenceJSON{}, false
		}
	}
	if date.Ordinary {
		out.CanRegister = me != nil && a.canRegisterDateIn(a.DB, me, date.Event, occ.ID)
	}
	return out, true
}

// occurrenceAccountsIn uses the same latest-consent ordering as the single-user
// loader. Facts are local to the caller's transaction, never cached authority.
func occurrenceAccountsIn(tx *gorm.DB, ids []uint, now time.Time) (map[uint]*models.User, map[uint]policy.ContactAccount, error) {
	if len(ids) == 0 {
		return map[uint]*models.User{}, map[uint]policy.ContactAccount{}, nil
	}
	zone, err := churchZoneIn(tx)
	if err != nil {
		return nil, nil, err
	}
	return occurrenceAccountsInZone(tx, ids, now, zone)
}

// occurrenceAccountsInZone is occurrenceAccountsIn for a caller that already
// read the church zone.
func occurrenceAccountsInZone(tx *gorm.DB, ids []uint, now time.Time, zone *time.Location) (map[uint]*models.User, map[uint]policy.ContactAccount, error) {
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
