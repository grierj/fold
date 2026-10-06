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
// JSON shapes
// ---------------------------------------------------------------------------

type userJSON struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Phone    string `json:"phone"`
	Role     string `json:"role"`
}

func toUserJSON(u *models.User) userJSON {
	return userJSON{ID: u.ID, Username: u.Username, Name: u.Name, Email: u.Email, Phone: u.Phone, Role: u.Role}
}

type groupSummaryJSON struct {
	ID            uint       `json:"id"`
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	Tags          []string   `json:"tags"`
	ImageURL      string     `json:"imageUrl"`
	Location      string     `json:"location"`
	LeaderName    string     `json:"leaderName"`
	MemberCount   int64      `json:"memberCount"`
	NextMeetingAt *time.Time `json:"nextMeetingAt"`
	Status        string     `json:"status"`
}

type memberJSON struct {
	ID       uint   `json:"id"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	JoinedAt string `json:"joinedAt"`
}

type meetingInstanceJSON struct {
	MeetingID uint             `json:"meetingId"`
	Date      string           `json:"date"`
	StartsAt  time.Time        `json:"startsAt"`
	EndsAt    time.Time        `json:"endsAt"`
	Location  string           `json:"location"`
	RSVPs     map[string]int64 `json:"rsvps"`
	MyRSVP    *string          `json:"myRsvp"`
}

type meetingJSON struct {
	ID             uint     `json:"id"`
	GroupID        uint     `json:"groupId"`
	StartsAt       time.Time `json:"startsAt"`
	EndsAt         time.Time `json:"endsAt"`
	Location       string   `json:"location"`
	RecurrenceRule string   `json:"recurrenceRule"`
	CancelledDates []string `json:"cancelledDates"`
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func jsonError(c *gin.Context, code int, msg string) {
	c.JSON(code, gin.H{"error": msg})
}

// apiAuth requires a logged-in user; 401 JSON otherwise.
func (a *App) apiAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		session, err := a.Store.Get(c.Request, sessionName)
		if err != nil {
			jsonError(c, http.StatusUnauthorized, "unauthorized")
			c.Abort()
			return
		}
		uid, ok := session.Values["userID"].(uint)
		if !ok {
			jsonError(c, http.StatusUnauthorized, "unauthorized")
			c.Abort()
			return
		}
		var user models.User
		if err := a.DB.First(&user, uid).Error; err != nil {
			jsonError(c, http.StatusUnauthorized, "unauthorized")
			c.Abort()
			return
		}
		c.Set("apiUser", &user)
		c.Next()
	}
}

// apiAdmin requires an admin user.
func (a *App) apiAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		u, ok := c.Get("apiUser")
		if !ok || !u.(*models.User).IsAdmin() {
			jsonError(c, http.StatusForbidden, "admin required")
			c.Abort()
			return
		}
		c.Next()
	}
}

func apiUser(c *gin.Context) *models.User {
	if u, ok := c.Get("apiUser"); ok {
		return u.(*models.User)
	}
	return nil
}

// optionalUser loads the user when logged in but never rejects.
func (a *App) optionalUser() gin.HandlerFunc {
	return func(c *gin.Context) {
		session, err := a.Store.Get(c.Request, sessionName)
		if err == nil {
			if uid, ok := session.Values["userID"].(uint); ok {
				var user models.User
				if err := a.DB.First(&user, uid).Error; err == nil {
					c.Set("apiUser", &user)
				}
			}
		}
		c.Next()
	}
}

// canManageGroup reports whether u is the group's leader or an admin.
func canManageGroup(a *App, u *models.User, groupID uint) bool {
	if u.IsAdmin() {
		return true
	}
	var m models.Membership
	err := a.DB.Where("user_id = ? AND group_id = ? AND role = ? AND status = ?",
		u.ID, groupID, models.MembershipLeader, "active").First(&m).Error
	return err == nil
}

func splitTags(s string) []string {
	out := []string{}
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func joinTags(tags []string) string {
	var clean []string
	for _, t := range tags {
		if t = strings.TrimSpace(t); t != "" {
			clean = append(clean, t)
		}
	}
	return strings.Join(clean, ",")
}

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
// Auth endpoints
// ---------------------------------------------------------------------------

func (a *App) apiGetCSRF(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"csrfToken": c.GetString("csrfToken")})
}

func (a *App) apiLogin(c *gin.Context) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "username and password are required")
		return
	}
	var user models.User
	if err := a.DB.Where("username = ?", body.Username).First(&user).Error; err != nil {
		jsonError(c, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if !user.CheckPassword(body.Password) {
		jsonError(c, http.StatusUnauthorized, "invalid credentials")
		return
	}
	session, err := a.Store.Get(c.Request, sessionName)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "session error")
		return
	}
	session.Values["userID"] = user.ID
	if err := session.Save(c.Request, c.Writer); err != nil {
		jsonError(c, http.StatusInternalServerError, "session error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": toUserJSON(&user)})
}

func (a *App) apiLogout(c *gin.Context) {
	session, _ := a.Store.Get(c.Request, sessionName)
	session.Values["userID"] = nil
	_ = session.Save(c.Request, c.Writer)
	c.JSON(http.StatusOK, gin.H{})
}

func (a *App) apiMe(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"user": toUserJSON(apiUser(c))})
}

// apiMyGroups lists the groups the current user is an active member of.
func (a *App) apiMyGroups(c *gin.Context) {
	u := apiUser(c)
	var memberships []models.Membership
	a.DB.Preload("Group").Where("user_id = ? AND status = ?", u.ID, "active").
		Order("joined_at").Find(&memberships)
	out := make([]groupSummaryJSON, 0, len(memberships))
	for i := range memberships {
		if memberships[i].Group.Status == models.GroupActive {
			out = append(out, a.groupSummary(&memberships[i].Group))
		}
	}
	c.JSON(http.StatusOK, gin.H{"groups": out})
}

// ---------------------------------------------------------------------------
// Group endpoints
// ---------------------------------------------------------------------------

func (a *App) apiListGroups(c *gin.Context) {
	search := c.Query("search")
	tag := c.Query("tag")

	var groups []models.Group
	query := a.DB.Where("status = ?", models.GroupActive)
	if search != "" {
		like := "%" + strings.ToLower(search) + "%"
		query = query.Where("LOWER(name) LIKE ? OR LOWER(description) LIKE ?", like, like)
	}
	if tag != "" {
		query = query.Where("tags LIKE ?", "%"+tag+"%")
	}
	query.Order("name").Find(&groups)

	out := make([]groupSummaryJSON, 0, len(groups))
	for i := range groups {
		out = append(out, a.groupSummary(&groups[i]))
	}
	c.JSON(http.StatusOK, gin.H{"groups": out})
}

func (a *App) apiCreateGroup(c *gin.Context) {
	var body struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
		ImageURL    string   `json:"imageUrl"`
		Location    string   `json:"location"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		jsonError(c, http.StatusBadRequest, "name is required")
		return
	}
	u := apiUser(c)
	g := models.Group{
		Name:        strings.TrimSpace(body.Name),
		Description: strings.TrimSpace(body.Description),
		Tags:        joinTags(body.Tags),
		ImageURL:    strings.TrimSpace(body.ImageURL),
		Location:    strings.TrimSpace(body.Location),
		LeaderID:    u.ID,
		Status:      models.GroupPending,
	}
	if err := a.DB.Create(&g).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not create group")
		return
	}
	// The creator becomes the group's leader-member.
	a.DB.Create(&models.Membership{
		UserID: u.ID, GroupID: g.ID,
		Role: models.MembershipLeader, Status: "active", JoinedAt: time.Now().UTC(),
	})
	c.JSON(http.StatusCreated, gin.H{"group": a.groupSummary(&g)})
}

func (a *App) apiGetGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	me := apiUser(c)
	if g.Status != models.GroupActive {
		// Pending/archived groups are visible to admins and the leader only.
		if me == nil || (!me.IsAdmin() && g.LeaderID != me.ID) {
			jsonError(c, http.StatusNotFound, "group not found")
			return
		}
	}

	var members []models.Membership
	memberList := []memberJSON{}
	if me != nil {
		a.DB.Preload("User").Where("group_id = ? AND status = ?", g.ID, "active").
			Order("joined_at").Find(&members)
		for _, m := range members {
			name := m.User.Name
			if name == "" {
				name = m.User.Username
			}
			memberList = append(memberList, memberJSON{
				ID: m.User.ID, Name: name, Role: m.Role,
				JoinedAt: m.JoinedAt.Format(time.RFC3339),
			})
		}
	}

	var meetings []models.Meeting
	a.DB.Where("group_id = ?", g.ID).Find(&meetings)
	now := time.Now().UTC()
	upcoming := a.buildInstances(meetings, now, now.AddDate(0, 0, 90), me)

	summary := a.groupSummary(&g)
	c.JSON(http.StatusOK, gin.H{
		"group":            summary,
		"members":          memberList,
		"upcomingMeetings": upcoming,
		"canManage":        me != nil && canManageGroup(a, me, g.ID),
		"isMember":         me != nil && a.isMember(me.ID, g.ID),
	})
}

func (a *App) isMember(userID, groupID uint) bool {
	var n int64
	a.DB.Model(&models.Membership{}).
		Where("user_id = ? AND group_id = ? AND status = ?", userID, groupID, "active").
		Count(&n)
	return n > 0
}

func (a *App) apiUpdateGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	u := apiUser(c)
	if !canManageGroup(a, u, g.ID) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return
	}
	var body struct {
		Name        *string  `json:"name"`
		Description *string  `json:"description"`
		Tags        []string `json:"tags"`
		ImageURL    *string  `json:"imageUrl"`
		Location    *string  `json:"location"`
		Status      *string  `json:"status"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Name != nil {
		g.Name = strings.TrimSpace(*body.Name)
	}
	if body.Description != nil {
		g.Description = strings.TrimSpace(*body.Description)
	}
	if body.Tags != nil {
		g.Tags = joinTags(body.Tags)
	}
	if body.ImageURL != nil {
		g.ImageURL = strings.TrimSpace(*body.ImageURL)
	}
	if body.Location != nil {
		g.Location = strings.TrimSpace(*body.Location)
	}
	if body.Status != nil {
		// Leaders may archive their own group; only admins may (re)activate.
		if *body.Status == models.GroupArchived || u.IsAdmin() {
			g.Status = *body.Status
		} else {
			jsonError(c, http.StatusForbidden, "only admins can change status to " + *body.Status)
			return
		}
	}
	if err := a.DB.Save(&g).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not update group")
		return
	}
	c.JSON(http.StatusOK, gin.H{"group": a.groupSummary(&g)})
}

// ---------------------------------------------------------------------------
// Membership endpoints
// ---------------------------------------------------------------------------

func (a *App) apiJoinGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil || g.Status != models.GroupActive {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	u := apiUser(c)
	m := models.Membership{UserID: u.ID, GroupID: g.ID}
	if err := a.DB.Where("user_id = ? AND group_id = ?", u.ID, g.ID).
		Attrs(models.Membership{Role: models.MembershipMember, Status: "active", JoinedAt: time.Now().UTC()}).
		FirstOrCreate(&m).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not join group")
		return
	}
	c.JSON(http.StatusOK, gin.H{"membership": gin.H{
		"id": m.ID, "userId": m.UserID, "groupId": m.GroupID,
		"role": m.Role, "status": m.Status,
	}})
}

func (a *App) apiLeaveGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	u := apiUser(c)
	a.DB.Where("user_id = ? AND group_id = ?", u.ID, id).Delete(&models.Membership{})
	c.JSON(http.StatusOK, gin.H{})
}

func (a *App) apiGroupMembers(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var members []models.Membership
	a.DB.Preload("User").Where("group_id = ? AND status = ?", id, "active").
		Order("joined_at").Find(&members)
	out := make([]memberJSON, 0, len(members))
	for _, m := range members {
		name := m.User.Name
		if name == "" {
			name = m.User.Username
		}
		out = append(out, memberJSON{
			ID: m.User.ID, Name: name, Role: m.Role,
			JoinedAt: m.JoinedAt.Format(time.RFC3339),
		})
	}
	c.JSON(http.StatusOK, gin.H{"members": out})
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

// ---------------------------------------------------------------------------
// Admin endpoints
// ---------------------------------------------------------------------------

func (a *App) apiPendingGroups(c *gin.Context) {
	var groups []models.Group
	a.DB.Where("status = ?", models.GroupPending).Order("created_at").Find(&groups)
	out := make([]groupSummaryJSON, 0, len(groups))
	for i := range groups {
		out = append(out, a.groupSummary(&groups[i]))
	}
	c.JSON(http.StatusOK, gin.H{"groups": out})
}

func (a *App) apiApproveGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	var body struct {
		Tags []string `json:"tags"`
	}
	_ = c.ShouldBindJSON(&body)
	if body.Tags != nil {
		g.Tags = joinTags(body.Tags)
	}
	g.Status = models.GroupActive
	if err := a.DB.Save(&g).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not approve group")
		return
	}
	c.JSON(http.StatusOK, gin.H{"group": a.groupSummary(&g)})
}

func (a *App) apiRejectGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g models.Group
	if err := a.DB.First(&g, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "group not found")
		return
	}
	g.Status = models.GroupArchived
	if err := a.DB.Save(&g).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not reject group")
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

// ---------------------------------------------------------------------------
// Route registration
// ---------------------------------------------------------------------------

func (a *App) registerAPIRoutes(r *gin.Engine) {
	v1 := r.Group("/api/v1")

	// Auth (csrf token endpoint is public; login/logout need the token).
	v1.GET("/auth/csrf", a.apiGetCSRF)
	v1.POST("/auth/login", a.apiLogin)
	v1.POST("/auth/logout", a.apiLogout)
	v1.GET("/auth/me", a.apiAuth(), a.apiMe)

	// Groups.
	v1.GET("/groups", a.optionalUser(), a.apiListGroups)
	v1.GET("/me/groups", a.apiAuth(), a.apiMyGroups)
	v1.POST("/groups", a.apiAuth(), a.apiCreateGroup)
	v1.GET("/groups/:id", a.optionalUser(), a.apiGetGroup)
	v1.PUT("/groups/:id", a.apiAuth(), a.apiUpdateGroup)
	v1.POST("/groups/:id/join", a.apiAuth(), a.apiJoinGroup)
	v1.POST("/groups/:id/leave", a.apiAuth(), a.apiLeaveGroup)
	v1.GET("/groups/:id/members", a.apiAuth(), a.apiGroupMembers)
	v1.GET("/groups/:id/meetings", a.optionalUser(), a.apiListMeetings)
	v1.POST("/groups/:id/meetings", a.apiAuth(), a.apiCreateMeeting)

	// Meetings.
	v1.PUT("/meetings/:id", a.apiAuth(), a.apiUpdateMeeting)
	v1.DELETE("/meetings/:id", a.apiAuth(), a.apiDeleteMeeting)
	v1.POST("/meetings/:id/cancel", a.apiAuth(), a.apiCancelInstance)
	v1.PUT("/meetings/:id/rsvp", a.apiAuth(), a.apiRSVP)

	// Admin.
	v1.GET("/admin/pending", a.apiAuth(), a.apiAdmin(), a.apiPendingGroups)
	v1.POST("/admin/groups/:id/approve", a.apiAuth(), a.apiAdmin(), a.apiApproveGroup)
	v1.POST("/admin/groups/:id/reject", a.apiAuth(), a.apiAdmin(), a.apiRejectGroup)
}
