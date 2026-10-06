package main

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"church-groups/models"

	"github.com/gin-gonic/gin"
	"github.com/teambition/rrule-go"
)

// ---------------------------------------------------------------------------
// Meeting expansion
// ---------------------------------------------------------------------------

const maxInstances = 100

// expandMeeting returns occurrence start times within [from, to].
func expandMeeting(m *models.Meeting, from, to time.Time) []time.Time {
	if m.RecurrenceRule == "" {
		if !m.StartsAt.Before(from) && !m.StartsAt.After(to) {
			return []time.Time{m.StartsAt}
		}
		return nil
	}
	ruleStr := "DTSTART:" + m.StartsAt.UTC().Format("20060102T150405Z") +
		"\nRRULE:" + m.RecurrenceRule
	r, err := rrule.StrToRRule(ruleStr)
	if err != nil {
		return nil
	}
	occ := r.Between(from, to, true)
	if len(occ) > maxInstances {
		occ = occ[:maxInstances]
	}
	return occ
}

func (a *App) cancelledDates(meetingID uint) map[string]bool {
	var ex []models.MeetingException
	a.DB.Where("meeting_id = ? AND cancelled = ?", meetingID, true).Find(&ex)
	m := make(map[string]bool, len(ex))
	for _, e := range ex {
		m[e.Date] = true
	}
	return m
}

// buildInstances expands meetings into instances with RSVP counts.
// It always returns a non-nil slice so JSON encodes [] rather than null.
func (a *App) buildInstances(meetings []models.Meeting, from, to time.Time, me *models.User) []meetingInstanceJSON {
	out := []meetingInstanceJSON{}
	for i := range meetings {
		m := &meetings[i]
		cancelled := a.cancelledDates(m.ID)
		for _, occ := range expandMeeting(m, from, to) {
			date := occ.UTC().Format("2006-01-02")
			if cancelled[date] {
				continue
			}
			dur := m.EndsAt.Sub(m.StartsAt)
			loc := m.Location
			if loc == "" {
				loc = m.Group.Location
			}
			inst := meetingInstanceJSON{
				MeetingID: m.ID,
				Date:      date,
				StartsAt:  occ,
				EndsAt:    occ.Add(dur),
				Location:  loc,
				RSVPs:     map[string]int64{"going": 0, "maybe": 0, "declined": 0},
			}
			out = append(out, inst)
		}
	}
	if len(out) == 0 {
		return out
	}
	// Batch-load RSVP counts and the viewer's RSVPs.
	type row struct {
		MeetingID uint
		Date      string
		Status    string
		N         int64
	}
	var rows []row
	meetingIDs := make([]uint, 0, len(meetings))
	for i := range meetings {
		meetingIDs = append(meetingIDs, meetings[i].ID)
	}
	a.DB.Model(&models.RSVP{}).
		Select("meeting_id, date, status, count(*) as n").
		Where("meeting_id IN ?", meetingIDs).
		Group("meeting_id, date, status").
		Scan(&rows)
	counts := map[string]map[string]int64{} // "mid|date" -> status -> n
	for _, r := range rows {
		k := strconv.FormatUint(uint64(r.MeetingID), 10) + "|" + r.Date
		if counts[k] == nil {
			counts[k] = map[string]int64{}
		}
		counts[k][r.Status] = r.N
	}
	var mine map[string]string
	if me != nil {
		var my []models.RSVP
		a.DB.Where("user_id = ? AND meeting_id IN ?", me.ID, meetingIDs).Find(&my)
		mine = map[string]string{}
		for _, r := range my {
			mine[strconv.FormatUint(uint64(r.MeetingID), 10)+"|"+r.Date] = r.Status
		}
	}
	for i := range out {
		k := strconv.FormatUint(uint64(out[i].MeetingID), 10) + "|" + out[i].Date
		for s, n := range counts[k] {
			out[i].RSVPs[s] = n
		}
		if mine != nil {
			if s, ok := mine[k]; ok {
				out[i].MyRSVP = &s
			}
		}
	}
	return out
}

func (a *App) groupSummary(g *models.Group) groupSummaryJSON {
	var memberCount int64
	a.DB.Model(&models.Membership{}).Where("group_id = ? AND status = ?", g.ID, "active").Count(&memberCount)

	var leader models.User
	leaderName := ""
	if err := a.DB.First(&leader, g.LeaderID).Error; err == nil {
		leaderName = leader.Name
		if leaderName == "" {
			leaderName = leader.Username
		}
	}

	var next *time.Time
	var meetings []models.Meeting
	a.DB.Where("group_id = ?", g.ID).Find(&meetings)
	now := time.Now().UTC()
	insts := a.buildInstances(meetings, now, now.AddDate(0, 0, 90), nil)
	if len(insts) > 0 {
		t := insts[0].StartsAt
		next = &t
	}

	return groupSummaryJSON{
		ID: g.ID, Name: g.Name, Description: g.Description,
		Tags: splitTags(g.Tags), ImageURL: g.ImageURL, Location: g.Location,
		LeaderName: leaderName, MemberCount: memberCount,
		NextMeetingAt: next, Status: g.Status,
	}
}

// ---------------------------------------------------------------------------
// Meeting endpoints
// ---------------------------------------------------------------------------

func parseDateParam(s string, def time.Time) time.Time {
	if s == "" {
		return def
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t
	}
	return def
}

func (a *App) apiListMeetings(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil || g.Status != models.GroupActive {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	now := time.Now().UTC()
	from := parseDateParam(c.Query("from"), now)
	to := parseDateParam(c.Query("to"), now.AddDate(0, 0, 90))
	var meetings []models.Meeting
	a.DB.Where("group_id = ?", g.ID).Find(&meetings)
	insts := a.buildInstances(meetings, from, to, apiUser(c))
	c.JSON(http.StatusOK, gin.H{"meetings": insts})
}

func (a *App) apiCreateMeeting(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	if !canManageGroup(a, apiUser(c), g.ID) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return
	}
	var body struct {
		StartsAt       time.Time `json:"startsAt"`
		EndsAt         time.Time `json:"endsAt"`
		Location       string    `json:"location"`
		RecurrenceRule string    `json:"recurrenceRule"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.StartsAt.IsZero() || body.EndsAt.IsZero() {
		jsonError(c, http.StatusBadRequest, "startsAt and endsAt are required")
		return
	}
	if !body.EndsAt.After(body.StartsAt) {
		jsonError(c, http.StatusBadRequest, "endsAt must be after startsAt")
		return
	}
	m := models.Meeting{
		GroupID: g.ID, StartsAt: body.StartsAt.UTC(), EndsAt: body.EndsAt.UTC(),
		Location: strings.TrimSpace(body.Location),
		RecurrenceRule: strings.TrimSpace(body.RecurrenceRule),
	}
	if err := a.DB.Create(&m).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not create meeting")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"meeting": toMeetingJSON(a, &m)})
}

func toMeetingJSON(a *App, m *models.Meeting) meetingJSON {
	var ex []models.MeetingException
	a.DB.Where("meeting_id = ? AND cancelled = ?", m.ID, true).Find(&ex)
	dates := make([]string, 0, len(ex))
	for _, e := range ex {
		dates = append(dates, e.Date)
	}
	return meetingJSON{
		ID: m.ID, GroupID: m.GroupID, StartsAt: m.StartsAt, EndsAt: m.EndsAt,
		Location: m.Location, RecurrenceRule: m.RecurrenceRule, CancelledDates: dates,
	}
}

func (a *App) loadMeeting(c *gin.Context) (*models.Meeting, bool) {
	id, _ := strconv.Atoi(c.Param("id"))
	var m models.Meeting
	if err := a.DB.First(&m, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "meeting not found")
		return nil, false
	}
	if !canManageGroup(a, apiUser(c), m.GroupID) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return nil, false
	}
	return &m, true
}

func (a *App) apiUpdateMeeting(c *gin.Context) {
	m, ok := a.loadMeeting(c)
	if !ok {
		return
	}
	var body struct {
		StartsAt       *time.Time `json:"startsAt"`
		EndsAt         *time.Time `json:"endsAt"`
		Location       *string    `json:"location"`
		RecurrenceRule *string    `json:"recurrenceRule"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid body")
		return
	}
	if body.StartsAt != nil {
		m.StartsAt = body.StartsAt.UTC()
	}
	if body.EndsAt != nil {
		m.EndsAt = body.EndsAt.UTC()
	}
	if !m.EndsAt.After(m.StartsAt) {
		jsonError(c, http.StatusBadRequest, "endsAt must be after startsAt")
		return
	}
	if body.Location != nil {
		m.Location = strings.TrimSpace(*body.Location)
	}
	if body.RecurrenceRule != nil {
		m.RecurrenceRule = strings.TrimSpace(*body.RecurrenceRule)
	}
	if err := a.DB.Save(m).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not update meeting")
		return
	}
	c.JSON(http.StatusOK, gin.H{"meeting": toMeetingJSON(a, m)})
}

func (a *App) apiDeleteMeeting(c *gin.Context) {
	m, ok := a.loadMeeting(c)
	if !ok {
		return
	}
	a.DB.Delete(m)
	c.JSON(http.StatusOK, gin.H{})
}

func (a *App) apiCancelInstance(c *gin.Context) {
	m, ok := a.loadMeeting(c)
	if !ok {
		return
	}
	var body struct {
		Date string `json:"date"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "date is required (YYYY-MM-DD)")
		return
	}
	if _, err := time.Parse("2006-01-02", body.Date); err != nil {
		jsonError(c, http.StatusBadRequest, "date must be YYYY-MM-DD")
		return
	}
	ex := models.MeetingException{MeetingID: m.ID, Date: body.Date}
	a.DB.Where("meeting_id = ? AND date = ?", m.ID, body.Date).
		Attrs(models.MeetingException{Cancelled: true}).
		FirstOrCreate(&ex)
	ex.Cancelled = true
	a.DB.Save(&ex)
	c.JSON(http.StatusOK, gin.H{})
}

// ---------------------------------------------------------------------------
// RSVP endpoint
// ---------------------------------------------------------------------------

func (a *App) apiRSVP(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var m models.Meeting
	if err := a.DB.First(&m, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "meeting not found")
		return
	}
	var body struct {
		Status string `json:"status"`
		Date   string `json:"date"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "status and date are required")
		return
	}
	switch body.Status {
	case models.RSVPGoing, models.RSVPMaybe, models.RSVPDeclined:
	default:
		jsonError(c, http.StatusBadRequest, "status must be going, maybe, or declined")
		return
	}
	if _, err := time.Parse("2006-01-02", body.Date); err != nil {
		jsonError(c, http.StatusBadRequest, "date must be YYYY-MM-DD")
		return
	}
	u := apiUser(c)
	r := models.RSVP{UserID: u.ID, MeetingID: m.ID, Date: body.Date}
	a.DB.Where("user_id = ? AND meeting_id = ? AND date = ?", u.ID, m.ID, body.Date).
		Attrs(models.RSVP{Status: body.Status}).
		FirstOrCreate(&r)
	r.Status = body.Status
	a.DB.Save(&r)
	c.JSON(http.StatusOK, gin.H{"rsvp": gin.H{
		"userId": r.UserID, "meetingId": r.MeetingID,
		"date": r.Date, "status": r.Status,
	}})
}
