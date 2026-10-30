package main

import (
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"fold/internal/audit"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const contactHistoryPageSize = 100

// Closed safeguarding projection: no names, contacts, request text, arbitrary
// audit metadata, IPs or live-domain joins. IDs are retained provenance, not
// authority to open a currently hidden/deleted context.
type contactHistoryEntry struct {
	ID            uint      `json:"id"`
	At            time.Time `json:"at"`
	Action        string    `json:"action"`
	ActorID       *uint     `json:"actorId,omitempty"`
	RequesterID   *uint     `json:"requesterId,omitempty"`
	SubjectUserID *uint     `json:"subjectUserId,omitempty"`
	Purpose       string    `json:"purpose,omitempty"`
	RequestID     *uint     `json:"requestId,omitempty"`
	AccessID      *uint     `json:"accessId,omitempty"`
	ContextType   string    `json:"contextType,omitempty"`
	ContextID     *uint     `json:"contextId,omitempty"`
	OccurrenceID  *uint     `json:"occurrenceId,omitempty"`
	Fields        string    `json:"fields,omitempty"`
	Outcome       string    `json:"outcome"`
	Party         string    `json:"party,omitempty"`
	Decision      string    `json:"decision,omitempty"`
}

// All serialized IDs are exact decimal strings, including IDs above 2^53.
type contactHistoryEntryJSON struct {
	ID            string    `json:"id"`
	At            time.Time `json:"at"`
	Action        string    `json:"action"`
	ActorID       string    `json:"actorId,omitempty"`
	RequesterID   string    `json:"requesterId,omitempty"`
	SubjectUserID string    `json:"subjectUserId,omitempty"`
	Purpose       string    `json:"purpose,omitempty"`
	RequestID     string    `json:"requestId,omitempty"`
	AccessID      string    `json:"accessId,omitempty"`
	ContextType   string    `json:"contextType,omitempty"`
	ContextID     string    `json:"contextId,omitempty"`
	OccurrenceID  string    `json:"occurrenceId,omitempty"`
	Fields        string    `json:"fields,omitempty"`
	Outcome       string    `json:"outcome"`
	Party         string    `json:"party,omitempty"`
	Decision      string    `json:"decision,omitempty"`
}

type contactHistoryJSON struct {
	Entries    []contactHistoryEntryJSON `json:"entries"`
	NextBefore string                    `json:"nextBefore,omitempty"`
}

func historyID(n uint) string { return strconv.FormatUint(uint64(n), 10) }
func historyOptionalID(n *uint) string {
	if n == nil {
		return ""
	}
	return historyID(*n)
}
func contactHistoryDTO(e contactHistoryEntry) contactHistoryEntryJSON {
	return contactHistoryEntryJSON{
		ID: historyID(e.ID), At: e.At, Action: e.Action,
		ActorID: historyOptionalID(e.ActorID), RequesterID: historyOptionalID(e.RequesterID), SubjectUserID: historyOptionalID(e.SubjectUserID),
		Purpose: e.Purpose, RequestID: historyOptionalID(e.RequestID), AccessID: historyOptionalID(e.AccessID),
		ContextType: e.ContextType, ContextID: historyOptionalID(e.ContextID), OccurrenceID: historyOptionalID(e.OccurrenceID),
		Fields: e.Fields, Outcome: e.Outcome, Party: e.Party, Decision: e.Decision,
	}
}

// Each action has exactly one known target shape. Never search arbitrary JSON
// for a person ID (or accept submitted IDs on a deliberately generic refusal).
var contactHistoryTargets = map[string]string{
	audit.ContactRequested: "contact_request", audit.ContactDecision: "contact_request", audit.ContactWithdrawn: "contact_request", audit.ContactExpired: "contact_request", audit.ContactGranted: "contact_request", audit.ContactReveal: "contact_request", audit.ContactRefused: "contact_request", audit.ContactRevoked: "contact_request", audit.ContactEnded: "contact_request",
	audit.AddRequested: "add_request", audit.AddDecision: "add_request", audit.AddWithdrawn: "add_request", audit.AddExpired: "add_request", audit.AddCompleted: "add_request", audit.AddRefused: "add_request", audit.AddEnded: "add_request",
	audit.OccurrenceAccessAdded: "occurrence_access", audit.OccurrenceAccessEnded: "occurrence_access", audit.OccurrenceBlocked: "occurrence_access",
	audit.MemberAdded: "group", audit.MemberRemoved: "group", audit.AddedOverLimit: "group",
	audit.RegistrationAdded: "event", audit.RegistrationAddedOverCapacity: "event", audit.RegistrationRemoved: "event",
}

type contactHistoryFilter struct {
	PersonID                     uint
	Before                       uint
	Limit                        int
	Purpose, Action, ContextType string
	ContextID, OccurrenceID      uint
	From, Until                  time.Time
}

func historyPositiveID(s string) (uint, bool) {
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 63)
	return uint(n), err == nil && n > 0
}

func parseContactHistoryFilter(c *gin.Context, export bool) (contactHistoryFilter, bool) {
	f := contactHistoryFilter{Limit: 50}
	var ok bool
	f.PersonID, ok = historyPositiveID(c.Param("id"))
	if !ok {
		return f, false
	}
	if len(c.Request.URL.RawQuery) > 1024 {
		return f, false
	}
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return f, false
	}
	for key, values := range query {
		if len(values) != 1 || len(values[0]) == 0 || len(values[0]) > 64 {
			return f, false
		}
		v := values[0]
		switch key {
		case "before":
			if export {
				return f, false
			}
			f.Before, ok = historyPositiveID(v)
			if !ok {
				return f, false
			}
		case "limit":
			if export {
				return f, false
			}
			n, valid := historyPositiveID(v)
			if !valid || n > contactHistoryPageSize {
				return f, false
			}
			f.Limit = int(n)
		case "purpose":
			if v != policy.RequestContact && v != policy.RequestGroupAdd && v != policy.RequestOccurrenceAdd {
				return f, false
			}
			f.Purpose = v
		case "action":
			if _, exists := contactHistoryTargets[v]; !exists && v != audit.OccurrenceRefused {
				return f, false
			}
			f.Action = v
		case "contextType":
			if v != "group" && v != "event" {
				return f, false
			}
			f.ContextType = v
		case "contextId":
			f.ContextID, ok = historyPositiveID(v)
			if !ok {
				return f, false
			}
		case "occurrenceId":
			f.OccurrenceID, ok = historyPositiveID(v)
			if !ok {
				return f, false
			}
		case "from", "until":
			at, err := time.Parse(time.RFC3339Nano, v)
			if err != nil || at.IsZero() {
				return f, false
			}
			if key == "from" {
				f.From = at
			} else {
				f.Until = at
			}
		default:
			return f, false
		}
	}
	if (f.ContextID > 0 && f.ContextType == "") || (!f.From.IsZero() && !f.Until.IsZero() && !f.From.Before(f.Until)) {
		return f, false
	}
	return f, true
}

// Numeric JSON IDs are validated before conversion; malformed types, fractions,
// negatives and oversized numbers are omitted, never cast into an SQL error.
func historyMetaID(key string) string {
	return "CASE WHEN jsonb_typeof(l.meta_json->'" + key + "') = 'number' AND l.meta_json->>'" + key + "' ~ '^[1-9][0-9]{0,18}$' THEN CASE WHEN (l.meta_json->>'" + key + "')::numeric <= 9223372036854775807 THEN (l.meta_json->>'" + key + "')::bigint END END"
}

func contactHistoryQuery(tx *gorm.DB, f contactHistoryFilter) *gorm.DB {
	pairs := tx.Where("1=0")
	// Sorted, so the SQL text is the same on every request and pgx reuses the
	// prepared statement.
	actions := make([]string, 0, len(contactHistoryTargets))
	for action := range contactHistoryTargets {
		actions = append(actions, action)
	}
	sort.Strings(actions)
	for _, action := range actions {
		pairs = pairs.Or("l.action = ? AND l.target_type = ?", action, contactHistoryTargets[action])
	}
	pairs = pairs.Or("l.action = ? AND l.target_type = ?", audit.ContactRefused, "contact_endpoint").Or("l.action = ? AND l.target_type = ?", audit.AddRefused, "add_endpoint").Or("l.action = ? AND l.target_type = ?", audit.OccurrenceRefused, "occurrence_endpoint")
	inner := tx.Table("audit_logs l").Select(`l.id, l.at, l.action, l.actor_id,
 CASE WHEN l.meta_json->>'reason' IN ('subject:approved','subject:denied','admin:approved','admin:denied') THEN l.meta_json->>'reason' END AS decision_reason,
 COALESCE(r.requester_id, oa.issuer_id) AS requester_id,
 COALESCE(r.subject_user_id, oa.subject_user_id, CASE WHEN l.target_type IN ('group','event') THEN ` + historyMetaID("userId") + ` END) AS subject_user_id,
 CASE WHEN r.id IS NOT NULL THEN r.purpose WHEN oa.id IS NOT NULL OR l.target_type = 'event' THEN 'occurrence_add' WHEN l.target_type = 'group' THEN 'group_add' END AS purpose,
 COALESCE(r.id, oa.request_id) AS request_id, oa.id AS access_id,
 COALESCE(r.context_type, CASE WHEN oa.id IS NOT NULL THEN 'event' WHEN l.target_type IN ('group','event') THEN l.target_type END) AS context_type,
 COALESCE(r.context_id, oa.event_id, CASE WHEN l.target_type IN ('group','event') AND l.target_id > 0 THEN l.target_id END) AS context_id,
 COALESCE(r.occurrence_id, oa.occurrence_id, CASE WHEN l.target_type = 'event' THEN ` + historyMetaID("occurrenceId") + ` END) AS occurrence_id,
 CASE WHEN r.purpose = 'contact' THEN r.fields END AS fields`).
		Joins(`LEFT JOIN contact_requests r ON r.id = l.target_id AND ((l.target_type = 'contact_request' AND r.purpose = 'contact') OR (l.target_type = 'add_request' AND r.purpose IN ('group_add','occurrence_add')))`).
		Joins(`LEFT JOIN occurrence_accesses oa ON oa.id = l.target_id AND l.target_type = 'occurrence_access'`).Where(pairs)
	q := tx.Table("(?) AS history", inner).Where("actor_id = ? OR requester_id = ? OR subject_user_id = ?", f.PersonID, f.PersonID, f.PersonID)
	if f.Before > 0 {
		q = q.Where("id < ?", f.Before)
	}
	if f.Purpose != "" {
		q = q.Where("purpose = ?", f.Purpose)
	}
	if f.Action != "" {
		q = q.Where("action = ?", f.Action)
	}
	if f.ContextType != "" {
		q = q.Where("context_type = ?", f.ContextType)
	}
	if f.ContextID > 0 {
		q = q.Where("context_id = ?", f.ContextID)
	}
	if f.OccurrenceID > 0 {
		q = q.Where("occurrence_id = ?", f.OccurrenceID)
	}
	if !f.From.IsZero() {
		q = q.Where("at >= ?", f.From)
	}
	if !f.Until.IsZero() {
		q = q.Where("at < ?", f.Until)
	}
	return q.Order("id DESC")
}

type contactHistoryRow struct {
	Entry          contactHistoryEntry `gorm:"embedded"`
	DecisionReason string
}

func presentContactHistory(row contactHistoryRow) contactHistoryEntry {
	out := row.Entry
	// No raw metadata leaves this function. Even a corrupt audit action's
	// decision text cannot become user content or a CSV formula.
	switch out.Action {
	case audit.ContactRequested, audit.AddRequested:
		out.Outcome = "requested"
	case audit.ContactDecision, audit.AddDecision:
		out.Outcome = "decision"
		switch row.DecisionReason {
		case "subject:approved", "subject:denied", "admin:approved", "admin:denied":
			parts := strings.Split(row.DecisionReason, ":")
			out.Party, out.Decision = parts[0], parts[1]
			out.Outcome = parts[1]
		}
	case audit.ContactWithdrawn, audit.AddWithdrawn:
		out.Outcome = "withdrawn"
	case audit.ContactExpired, audit.AddExpired:
		out.Outcome = "expired"
	case audit.ContactGranted:
		out.Outcome = "granted"
	case audit.ContactReveal:
		out.Outcome = "revealed"
	case audit.ContactRefused, audit.AddRefused, audit.OccurrenceRefused:
		out.Outcome = "refused"
	case audit.ContactRevoked:
		out.Outcome = "revoked"
	case audit.ContactEnded, audit.AddEnded, audit.OccurrenceAccessEnded:
		out.Outcome = "ended"
	case audit.MemberRemoved, audit.RegistrationRemoved:
		out.Outcome = "removed"
	case audit.OccurrenceBlocked:
		out.Outcome = "blocked"
	default:
		out.Outcome = "added"
	}
	if !policy.ValidContactFields(out.Fields) {
		out.Fields = ""
	}
	return out
}

func loadContactHistoryPage(tx *gorm.DB, f contactHistoryFilter, limit int) ([]contactHistoryEntry, error) {
	var rows []contactHistoryRow
	if err := contactHistoryQuery(tx, f).Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]contactHistoryEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, presentContactHistory(row))
	}
	return out, nil
}

func (a *App) canViewContactHistoryAt(user *models.User, now time.Time) bool {
	today := a.householdClock(now)
	return policy.CanViewContactHistory(contactAccount(user, today, today.Location()))
}

func (a *App) contactHistoryFilter(c *gin.Context, export bool) (contactHistoryFilter, bool) {
	// apiAuth reloads the session user on every request (including version and
	// disabled/deleted checks). Own-person IDs confer no administrative right.
	if !a.canViewContactHistoryAt(apiUser(c), time.Now().UTC()) {
		jsonError(c, http.StatusForbidden, "admin required")
		return contactHistoryFilter{}, false
	}
	f, ok := parseContactHistoryFilter(c, export)
	if !ok {
		jsonError(c, http.StatusBadRequest, "invalid contact history filters")
	}
	return f, ok
}

func (a *App) apiContactHistory(c *gin.Context) {
	f, ok := a.contactHistoryFilter(c, false)
	if !ok {
		return
	}
	entries, err := loadContactHistoryPage(a.DB.WithContext(c.Request.Context()), f, f.Limit+1)
	if err != nil {
		jsonError(c, 500, "could not load contact history")
		return
	}
	out := contactHistoryJSON{Entries: make([]contactHistoryEntryJSON, 0, len(entries))}
	if len(entries) > f.Limit {
		entries = entries[:f.Limit]
		out.NextBefore = historyID(entries[len(entries)-1].ID)
	}
	for _, entry := range entries {
		out.Entries = append(out.Entries, contactHistoryDTO(entry))
	}
	c.JSON(http.StatusOK, out)
}

var contactHistoryCSVHeader = []string{"auditId", "at", "action", "actorId", "requesterId", "subjectUserId", "purpose", "requestId", "accessId", "contextType", "contextId", "occurrenceId", "fields", "outcome", "party", "decision"}

// Quoting is not spreadsheet escaping. Prefix a literal apostrophe when the
// first significant rune is a formula operator, or a leading control could
// cause a spreadsheet to reinterpret the value. Applied to EVERY cell.
func contactHistoryCSVCell(value string) string {
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "'" + value
		}
		if unicode.IsSpace(r) {
			continue
		}
		if strings.ContainsRune("=+-@", r) {
			return "'" + value
		}
		break
	}
	return value
}
func historyCSVRecord(e contactHistoryEntry) []string {
	id := func(n *uint) string {
		if n == nil {
			return ""
		}
		return strconv.FormatUint(uint64(*n), 10)
	}
	cells := []string{strconv.FormatUint(uint64(e.ID), 10), e.At.UTC().Format(time.RFC3339Nano), e.Action, id(e.ActorID), id(e.RequesterID), id(e.SubjectUserID), e.Purpose, id(e.RequestID), id(e.AccessID), e.ContextType, id(e.ContextID), id(e.OccurrenceID), e.Fields, e.Outcome, e.Party, e.Decision}
	for i := range cells {
		cells[i] = contactHistoryCSVCell(cells[i])
	}
	return cells
}

// Materialize to a private, immediately cleaned temporary file, not request
// memory. A repeatable snapshot and bounded pages cover ALL matching rows.
// Query/encoding/disk/audit/commit failures precede any protected HTTP headers
// or bytes. Only the final authorization check locks the actor, never the
// contact mutation gate or live context rows.
func (a *App) apiExportContactHistory(c *gin.Context) {
	f, ok := a.contactHistoryFilter(c, true)
	if !ok {
		return
	}
	file, err := os.CreateTemp("", "fold-contact-history-*.csv")
	if err != nil {
		jsonError(c, 500, "could not export contact history")
		return
	}
	defer os.Remove(file.Name())
	defer file.Close()
	var count int64
	var size int64
	err = a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		writer := csv.NewWriter(file)
		writer.UseCRLF = true
		if err := writer.Write(contactHistoryCSVHeader); err != nil {
			return err
		}
		pageFilter := f
		for {
			entries, err := loadContactHistoryPage(tx, pageFilter, contactHistoryPageSize)
			if err != nil {
				return err
			}
			for _, e := range entries {
				if err := writer.Write(historyCSVRecord(e)); err != nil {
					return err
				}
				count++
			}
			if len(entries) < contactHistoryPageSize {
				break
			}
			pageFilter.Before = entries[len(entries)-1].ID
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			return err
		}
		if err := file.Sync(); err != nil {
			return err
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		info, err := file.Stat()
		if err != nil {
			return err
		}
		size = info.Size()
		// In REPEATABLE READ a row changed since the snapshot causes this
		// locking read to fail with serialization failure, not stale approval.
		// FOR SHARE blocks all actor updates/deletion until audit commit; it is
		// deliberately acquired only after the lengthy materialization.
		actor := apiUser(c)
		var current models.User
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&current, actor.ID).Error; err != nil {
			return err
		}
		zone, err := contactZone(tx)
		if err != nil {
			return err
		}
		if current.SessionVersion != actor.SessionVersion || !policy.CanViewContactHistory(contactAccount(&current, time.Now().UTC(), zone)) {
			return errors.New("contact history export authority changed")
		}
		// Authorization linearizes at this checked row lock, held through
		// audit commit. Later revocation cannot retract a prepared export.
		// Explicit safe filters only; no query string, filename, contact or reason.
		return audit.Write(tx, audit.Entry{Actor: &apiUser(c).ID, Action: audit.ContactHistoryExported, TargetType: "user", TargetID: f.PersonID,
			Meta: map[string]any{"count": count, "purpose": f.Purpose, "action": f.Action, "contextType": f.ContextType, "contextId": f.ContextID, "occurrenceId": f.OccurrenceID, "from": f.From, "until": f.Until}})
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		jsonError(c, 500, "could not export contact history")
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="contact-history-%d.csv"`, f.PersonID))
	c.DataFromReader(http.StatusOK, size, "text/csv; charset=utf-8", file, nil)
}
