package main

import (
	"database/sql"
	"encoding/json"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"fold/internal/audit"
	"fold/models"

	"gorm.io/gorm"
)

// These rules concern the IP's writer, not the person mentioned in the row.
// R4-GR-IP found no essential-IP exception. Metadata has separate subject rules.
// Unknown actions and unmatched historical shapes always retain their IP.
type auditIPClass string

const (
	auditIPActor              auditIPClass = "ACTOR"
	auditIPRequester          auditIPClass = "REQUESTER"
	auditIPMetaUser           auditIPClass = "META_USER"
	auditIPTargetUser         auditIPClass = "TARGET_USER"
	auditIPLinked             auditIPClass = "LINKED"
	auditIPCApproval          auditIPClass = "C_APPROVAL"
	auditIPConversion         auditIPClass = "CONVERSION"
	auditIPRetainEssential    auditIPClass = "RETAIN_ESSENTIAL"
	auditIPRetainUnattributed auditIPClass = "RETAIN_UNATTRIBUTED"
	auditIPNone               auditIPClass = "NONE"
)

type auditIPRule struct {
	Class auditIPClass
	// Shape predicates are mutually exclusive, including when keys are malformed.
	Shape string
	Owner string
}

var auditIPOrdinary = []auditIPRule{{auditIPActor, "actor_id IS NOT NULL", "actor_id = @uid"}}
var auditIPSystem = []auditIPRule{{auditIPNone, "ip IS NULL OR ip = ''", "FALSE"}}

const auditSignupRequest = `SELECT 1 FROM account_requests r
 WHERE r.source <> 'household_conversion'
 AND to_jsonb(r.approved_user_id) = meta_json->'teenUserId'`
const auditOneSignup = `(SELECT count(*) FROM (` + auditSignupRequest + `) signup) = 1`
const auditConversionRequest = `SELECT 1 FROM account_requests r
 WHERE r.source = 'household_conversion' AND r.status = 'approved'
 AND to_jsonb(r.approved_user_id) = meta_json->'teenUserId'`
const auditConversionShape = `target_type = 'registration' OR jsonb_exists(meta_json, 'invitationId') OR jsonb_exists(meta_json, 'formerRegisteredBy')`

// This is a report link, not IP ownership. Keep the text comparison so malformed
// string identities are still reported under their approving admin.
const auditMalformedConversionLink = `action = 'guardian_link.requested'
 AND meta_json->'source' = '"household_conversion"'::jsonb
 AND EXISTS (SELECT 1 FROM account_requests r WHERE r.source = 'household_conversion' AND r.status = 'approved'
 AND r.decided_by = @uid AND meta_json->>'teenUserId' = r.approved_user_id::text)
 AND (target_type <> 'guardian_link' OR jsonb_typeof(meta_json->'teenUserId') IS DISTINCT FROM 'number')`

// Registry entries are explicit so a new action cannot silently acquire ACTOR.
// The source census in account_erasure_audit_test.go also pins writer inputs.
// GuardianLinkRevoked includes eraseHouseholdRecordsIn's account_deleted rows:
// the deleting admin's request supplies the actor and IP, not the linked subject.
var auditIPAttribution = map[string][]auditIPRule{
	audit.ContactHistoryExported:        auditIPOrdinary,
	audit.OccurrenceAccessAdded:         auditIPOrdinary,
	audit.OccurrenceBlocked:             auditIPOrdinary,
	audit.OccurrenceRefused:             auditIPOrdinary,
	audit.OccurrenceAccessEnded:         auditIPOrdinary,
	audit.AddRequested:                  auditIPOrdinary,
	audit.AddDecision:                   auditIPOrdinary,
	audit.AddWithdrawn:                  auditIPOrdinary,
	audit.AddExpired:                    auditIPSystem,
	audit.AddCompleted:                  auditIPOrdinary,
	audit.AddRefused:                    auditIPOrdinary,
	audit.AddEnded:                      auditIPOrdinary,
	audit.ContactRequested:              auditIPOrdinary,
	audit.ContactDecision:               auditIPOrdinary,
	audit.ContactWithdrawn:              auditIPOrdinary,
	audit.ContactExpired:                auditIPSystem,
	audit.ContactGranted:                auditIPOrdinary,
	audit.ContactReveal:                 auditIPOrdinary,
	audit.ContactRefused:                auditIPOrdinary,
	audit.ContactRevoked:                auditIPOrdinary,
	audit.ContactEnded:                  auditIPOrdinary,
	audit.UserTurned18:                  auditIPSystem,
	audit.HouseholdChildTurned13:        auditIPSystem,
	audit.HouseholdAccountTurnedAdult:   auditIPSystem,
	audit.TeenConsentGranted:            auditIPOrdinary,
	audit.TeenConsentRevoked:            auditIPOrdinary,
	audit.RegistrationRestored:          auditIPOrdinary,
	audit.HouseholdUpdated:              auditIPOrdinary,
	audit.HouseholdPersonUpdated:        auditIPOrdinary,
	audit.RoleChanged:                   auditIPOrdinary,
	audit.MemberRemoved:                 auditIPOrdinary,
	audit.MemberAdded:                   auditIPOrdinary,
	audit.UserCreated:                   auditIPOrdinary,
	audit.UserRoleChanged:               auditIPOrdinary,
	audit.UserEmailChanged:              auditIPOrdinary,
	audit.UserDeleted:                   auditIPOrdinary,
	audit.UserErasureSwept:              auditIPSystem,
	audit.UserDisabled:                  auditIPOrdinary,
	audit.UserEnabled:                   auditIPOrdinary,
	audit.MemberRestored:                auditIPOrdinary,
	audit.MemberNotRestored:             auditIPOrdinary,
	audit.VisibilityOverride:            auditIPOrdinary,
	audit.VisibilityChanged:             auditIPOrdinary,
	audit.EventCreated:                  auditIPOrdinary,
	audit.EventDeleted:                  auditIPOrdinary,
	audit.EventDatesRestored:            auditIPOrdinary,
	audit.EventHostAdded:                auditIPOrdinary,
	audit.EventHostRemoved:              auditIPOrdinary,
	audit.RegistrationRemoved:           auditIPOrdinary,
	audit.RegistrationAdded:             auditIPOrdinary,
	audit.RegistrationPromoted:          auditIPOrdinary,
	audit.RegistrationAddedOverCapacity: auditIPOrdinary,
	audit.RegistrationNotRestored:       auditIPOrdinary,
	audit.GroupApproved:                 auditIPOrdinary,
	audit.GroupRejected:                 auditIPOrdinary,
	audit.GroupDeleted:                  auditIPOrdinary,
	audit.InvitationCreated:             auditIPOrdinary,
	audit.InvitationResent:              auditIPOrdinary,
	audit.InvitationRefused:             auditIPOrdinary,
	audit.InvitationRevoked:             auditIPOrdinary,
	audit.InvitationRateLimited:         auditIPOrdinary,
	audit.PasswordResetCompleted:        auditIPOrdinary,
	audit.PasswordChanged:               auditIPOrdinary,
	audit.EmailChangeRequested:          auditIPOrdinary,
	audit.EmailChanged:                  auditIPOrdinary,
	audit.EmailChangeCancelled:          auditIPOrdinary,
	audit.AccountApproved:               auditIPOrdinary,
	audit.AccountRejected:               auditIPOrdinary,
	audit.AccountExpired:                auditIPSystem,
	audit.AccountSetupCompleted:         auditIPOrdinary,
	audit.SelfSignupChanged:             auditIPOrdinary,
	audit.RoomsEnabledChanged:           auditIPOrdinary,
	audit.RoomCreated:                   auditIPOrdinary,
	audit.RoomUpdated:                   auditIPOrdinary,
	audit.RoomBookingBooked:             auditIPOrdinary,
	audit.RoomBookingReleased:           auditIPOrdinary,
	audit.RoomBookingOverlap:            auditIPOrdinary,
	audit.RoomBookingSeries:             auditIPOrdinary,
	audit.RoomRequestConfirmed:          auditIPOrdinary,
	audit.RoomRequestRejected:           auditIPOrdinary,
	audit.RoomApprovalChanged:           auditIPOrdinary,
	audit.RoomLocationMatched:           auditIPOrdinary,
	audit.RoomFeedCreated:               auditIPOrdinary,
	audit.RoomFeedRevoked:               auditIPOrdinary,
	audit.JobRetried:                    auditIPOrdinary,
	audit.JobDismissed:                  auditIPOrdinary,
	audit.EmailSuppressionCleared:       auditIPOrdinary,
	audit.ChurchAddressChanged:          auditIPOrdinary,
	audit.GuardianCopiesChanged:         auditIPOrdinary,
	audit.ReplyToChanged:                auditIPOrdinary,
	audit.PushEnabledChanged:            auditIPOrdinary,
	audit.MessageScheduled:              auditIPOrdinary,
	audit.MessageCancelled:              auditIPOrdinary,
	audit.MessageEdited:                 auditIPOrdinary,
	audit.MessageTestSent:               auditIPOrdinary,
	audit.MessageRefused:                auditIPOrdinary,
	audit.MessageSent:                   auditIPOrdinary,
	audit.MessageRetried:                auditIPOrdinary,
	audit.CalendarTokenRegenerated:      auditIPOrdinary,
	audit.CalendarTokenRevoked:          auditIPOrdinary,
	audit.CalendarTokenScopeChanged:     auditIPOrdinary,
	audit.HouseholdCreated:              auditIPOrdinary,
	audit.HouseholdPersonAdded:          auditIPOrdinary,
	audit.HouseholdPersonRemoved:        auditIPOrdinary,
	audit.GuardianLinkConfirmed:         auditIPOrdinary,
	audit.GuardianLinkRejected:          auditIPOrdinary,
	audit.GuardianLinkRevoked:           auditIPOrdinary,
	audit.PushKeysRotated:               auditIPSystem,
	audit.PushKeysReset:                 auditIPSystem,
	audit.PushDeviceOn:                  auditIPOrdinary,
	audit.PushTestSent:                  auditIPOrdinary,
	// T07B-2b: a deletion decision carries the deciding admin as actor with their
	// request IP, so it is cleared with that admin's other actor rows; the page-view
	// row records no IP.
	audit.ContactReasonKept:        auditIPOrdinary,
	audit.ContactReasonRemoved:     auditIPOrdinary,
	audit.AddReasonKept:            auditIPOrdinary,
	audit.AddReasonRemoved:         auditIPOrdinary,
	audit.UserErasureReasonsViewed: auditIPSystem,
	contactPrivacyChanged:          auditIPOrdinary,
	audit.InvitationAccepted: {
		{auditIPMetaUser, `target_type = 'invitation' AND jsonb_typeof(meta_json->'userId') = 'number' AND NOT jsonb_exists(meta_json, 'personId')`, `meta_json->'userId' = to_jsonb(CAST(@uid AS bigint))`},
		{auditIPLinked, `target_type = 'invitation' AND jsonb_typeof(meta_json->'personId') = 'number' AND NOT jsonb_exists(meta_json, 'userId')`, `target_id = ANY(CAST(@invitations AS bigint[]))`},
	},
	audit.AddedOverLimit: {
		{auditIPMetaUser, `jsonb_exists(meta_json, 'invitationId') AND jsonb_typeof(meta_json->'invitationId') = 'number' AND jsonb_typeof(meta_json->'userId') = 'number'`, `meta_json->'userId' = to_jsonb(CAST(@uid AS bigint))`},
		{auditIPActor, `NOT jsonb_exists(meta_json, 'invitationId') AND jsonb_typeof(meta_json->'userId') = 'number' AND actor_id IS NOT NULL`, `actor_id = @uid`},
	},
	audit.GuardianLinkRequested: {
		{auditIPActor, `meta_json->'source' = '"claim"'::jsonb AND actor_id IS NOT NULL`, `actor_id = @uid`},
		{auditIPRequester, `meta_json->'source' = '"signup"'::jsonb AND target_type = 'guardian_link' AND (` + auditOneSignup + `) AND EXISTS (` + auditSignupRequest + ` AND r.source = 'member_invite' AND r.requested_by IS NOT NULL)`, `EXISTS (` + auditSignupRequest + ` AND r.requested_by = @uid)`},
		{auditIPLinked, `meta_json->'source' = '"signup"'::jsonb AND target_type = 'guardian_link' AND (` + auditOneSignup + `) AND EXISTS (` + auditSignupRequest + ` AND r.source IN ('self_signup', 'guest_upgrade'))`, `meta_json->'teenUserId' = to_jsonb(CAST(@uid AS bigint))`},
		{auditIPCApproval, `meta_json->'source' = '"household_conversion"'::jsonb AND target_type = 'guardian_link' AND (SELECT count(*) FROM (` + auditConversionRequest + `) conversion) = 1 AND EXISTS (` + auditConversionRequest + ` AND r.decided_by IS NOT NULL)`, `EXISTS (` + auditConversionRequest + ` AND r.decided_by = @uid)`},
	},
	audit.AccountRequested: {
		{auditIPActor, `meta_json->'source' = '"member_invite"'::jsonb AND actor_id IS NOT NULL`, `actor_id = @uid`},
		{auditIPLinked, `meta_json->'source' IN ('"self_signup"'::jsonb, '"guest_upgrade"'::jsonb, '"household_conversion"'::jsonb) AND target_type = 'account_request'`, `target_id = ANY(CAST(@requests AS bigint[]))`},
	},
	audit.RegistrationChanged: {
		{auditIPConversion, auditConversionShape, "FALSE"},
		{auditIPActor, `NOT (` + auditConversionShape + `) AND target_type = 'event' AND actor_id IS NOT NULL`, `actor_id = @uid`},
	},
	audit.HouseholdPersonConverted: {{auditIPConversion, "TRUE", "FALSE"}},
	audit.RegistrationCancelled: {
		{auditIPActor, "actor_id IS NOT NULL", "actor_id = @uid"},
		{auditIPLinked, `actor_id IS NULL AND target_type = 'event' AND (
   (meta_json->'subjectType' = '"user"'::jsonb AND jsonb_typeof(meta_json->'userId') = 'number' AND meta_json->'subjectId' = meta_json->'userId' AND (NOT jsonb_exists(meta_json, 'personId') OR meta_json->'personId' = 'null'::jsonb)) OR
   (meta_json->'subjectType' = '"household_person"'::jsonb AND jsonb_typeof(meta_json->'personId') = 'number' AND meta_json->'subjectId' = meta_json->'personId' AND (NOT jsonb_exists(meta_json, 'userId') OR meta_json->'userId' = 'null'::jsonb)))`,
			`meta_json->'userId' = to_jsonb(CAST(@uid AS bigint)) OR EXISTS (SELECT 1 FROM unnest(CAST(@people AS bigint[])) p(id) WHERE meta_json->'personId' = to_jsonb(p.id))`},
	},
	audit.PasswordResetRequested: {{auditIPTargetUser, `target_type = 'user'`, "target_id = @uid"}},
}

type auditErasureCounts struct {
	IPsBlanked           int64
	MetadataRowsScrubbed int64
	// A subset of KeptByClassAndAction, never an additional retained-row total.
	MalformedConversionLinksRetained int64
	// Only counts and fixed class/action/reason codes leave the helper. Retention
	// counts describe remaining nonempty IPs on rows linked to this subject, not
	// proven IP ownership. Unrelated database-wide residue is not counted.
	BlankedByClass       map[auditIPClass]int64
	KeptByClassAndAction map[auditIPClass]map[string]int64
	Conversion           auditConversionCounts
}

type auditConversionCounts struct {
	Groups         int64
	Discovered     int64
	Retained       int64
	Blanked        int64
	ByReason       map[string]int64
	GroupsByReason map[string]int64
	Details        []auditConversionGroupCounts
}

type auditConversionGroupCounts struct {
	ConvertedFound           int64
	TransfersFound           int64
	ConvertedRetained        int64
	TransfersRetained        int64
	Blanked                  int64
	TransferCount            int64 // -1 means missing or not a non-negative integer.
	HouseholdFallbackSkipped int64
	Reason                   string
}

func auditIDArray(ids []uint) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatUint(uint64(id), 10)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// eraseAuditDetailsIn runs within account deletion, after dependent records and
// before the owned throttle keys (which stay last, detected limit D2).
// Its caller supplies the transaction and the sets captured before any scrub.
// It changes neither audit attribution nor authorization/safeguarding evidence.
func eraseAuditDetailsIn(tx *gorm.DB, s erasureSubject) (auditErasureCounts, error) {
	out := auditErasureCounts{BlankedByClass: map[auditIPClass]int64{}, KeptByClassAndAction: map[auditIPClass]map[string]int64{}}
	args := []any{sql.Named("uid", s.ID), sql.Named("requests", auditIDArray(s.Requests)), sql.Named("invitations", auditIDArray(s.Invitations)), sql.Named("people", auditIDArray(s.People))}
	actions := make([]string, 0, len(auditIPAttribution))
	for action := range auditIPAttribution {
		actions = append(actions, action)
	}
	sort.Strings(actions)
	byClass := map[auditIPClass][]string{}
	classification := "CASE WHEN ip IS NULL OR ip = '' THEN 'NONE' "
	for _, action := range actions {
		for _, rule := range auditIPAttribution[action] {
			// Action constants and predicates are source-owned, never request input.
			shape := "(action = '" + action + "' AND (" + rule.Shape + "))"
			classification += "WHEN " + shape + " THEN '" + string(rule.Class) + "' "
			if rule.Owner != "FALSE" {
				byClass[rule.Class] = append(byClass[rule.Class], "("+shape+" AND ("+rule.Owner+"))")
			}
		}
	}
	classification += "ELSE 'RETAIN_UNATTRIBUTED' END"
	for _, class := range []auditIPClass{auditIPActor, auditIPRequester, auditIPMetaUser, auditIPTargetUser, auditIPLinked, auditIPCApproval} {
		if len(byClass[class]) == 0 {
			continue
		}
		res := tx.Exec("UPDATE audit_logs SET ip = '' WHERE ip <> '' AND ("+strings.Join(byClass[class], " OR ")+")", args...)
		if res.Error != nil {
			return auditErasureCounts{}, res.Error
		}
		out.BlankedByClass[class] = res.RowsAffected
		out.IPsBlanked += res.RowsAffected
	}
	// Subtract only the listed subject-owned keys. jsonb subtraction preserves
	// every other key; rows without those keys are not rewritten on retries.
	scrubs := []struct{ where, keys string }{
		{`action IN ('user.email_changed','email.change_requested','email.changed') AND target_type = 'user' AND target_id = @uid`, `ARRAY['oldEmail','newEmail']`},
		{`action = 'invitation.created' AND target_type = 'invitation' AND target_id = ANY(CAST(@invitations AS bigint[]))`, `ARRAY['email']`},
		{`action = 'membership.removed' AND meta_json->'userId' = to_jsonb(CAST(@uid AS bigint))`, `ARRAY['note']`},
		{`action = 'registration.removed' AND (meta_json->'userId' = to_jsonb(CAST(@uid AS bigint)) OR EXISTS (SELECT 1 FROM unnest(CAST(@people AS bigint[])) p(id) WHERE meta_json->'personId' = to_jsonb(p.id)))`, `ARRAY['note']`},
		{`action = 'push.device_on' AND target_type = 'user' AND target_id = @uid`, `ARRAY['label']`},
	}
	for _, scrub := range scrubs {
		res := tx.Exec("UPDATE audit_logs SET meta_json = meta_json - "+scrub.keys+" WHERE ("+scrub.where+") AND meta_json <> meta_json - "+scrub.keys, args...)
		if res.Error != nil {
			return auditErasureCounts{}, res.Error
		}
		out.MetadataRowsScrubbed += res.RowsAffected
	}
	var kept []struct {
		Class  auditIPClass
		Action string
		Count  int64
	}
	// Mentioning a subject is sufficient for a retention report, never for an
	// IP update. Match malformed string IDs too, without casting untrusted JSON.
	// Bind the uint as bigint before converting it to text; pgx cannot encode a
	// uint directly for a parameter PostgreSQL infers to be text.
	const subjectMention = `actor_id = @uid OR (target_type = 'user' AND target_id = @uid)
 OR meta_json->'userId' = to_jsonb(CAST(@uid AS bigint)) OR meta_json->>'userId' = CAST(CAST(@uid AS bigint) AS text)
 OR meta_json->'teenUserId' = to_jsonb(CAST(@uid AS bigint)) OR meta_json->>'teenUserId' = CAST(CAST(@uid AS bigint) AS text)
 OR EXISTS (SELECT 1 FROM unnest(CAST(@people AS bigint[])) p(id)
 WHERE meta_json->'personId' = to_jsonb(p.id) OR meta_json->>'personId' = p.id::text)`
	if err := tx.Raw("SELECT class, action, count(*) FROM (SELECT action, "+classification+" AS class FROM audit_logs WHERE ip <> '' AND (("+subjectMention+") OR ("+auditMalformedConversionLink+"))) classified WHERE class IN ('RETAIN_ESSENTIAL', 'RETAIN_UNATTRIBUTED') GROUP BY class, action", args...).Scan(&kept).Error; err != nil {
		return auditErasureCounts{}, err
	}
	for _, row := range kept {
		if out.KeptByClassAndAction[row.Class] == nil {
			out.KeptByClassAndAction[row.Class] = map[string]int64{}
		}
		out.KeptByClassAndAction[row.Class][row.Action] = row.Count
	}
	// A malformed conversion nomination is retained on its own, rather than
	// holding back its valid siblings or being mistaken for the inviter's IP.
	// This diagnostic overlaps RETAIN_UNATTRIBUTED above; do not sum the two.
	if err := tx.Raw("SELECT count(*) FROM audit_logs WHERE ip <> '' AND ("+auditMalformedConversionLink+")", args...).Scan(&out.MalformedConversionLinksRetained).Error; err != nil {
		return auditErasureCounts{}, err
	}
	var err error
	out.Conversion, err = discoverAuditConversionsIn(tx, s.ID)
	if err != nil {
		return auditErasureCounts{}, err
	}
	return out, nil
}

type auditConversionRequestRow struct {
	ID           uint
	PersonID     uint
	InvitationID uint
	UserID       uint
	HouseholdID  *uint
	Marker       bool
}

type auditConversionCandidate struct {
	RequestID uint
	models.AuditLog
}

// Discovery uses retained keys before checking any shape. A wrong household,
// string identity or missing writer key remains in its whole candidate group.
// G-8 has no approved provenance mechanism, so this path never writes an IP.
// Plan 6.5.1 specifies text-equal discovery: stored numbers such as 5.0 may
// be missed when no other arm matches, although auditJSONID accepts
// them during validation. Resolve this discovery limit before enabling G-8
// transfer scrubbing. The malformed-nomination diagnostic has the same limit.
func discoverAuditConversionsIn(tx *gorm.DB, uid uint) (auditConversionCounts, error) {
	out := auditConversionCounts{ByReason: map[string]int64{}, GroupsByReason: map[string]int64{}}
	const requests = `SELECT r.id, r.household_person_id AS person_id, r.invitation_id, r.approved_user_id AS user_id, h.household_id,
 EXISTS (SELECT 1 FROM audit_logs m WHERE m.action = 'household.person_updated' AND m.target_type = 'household_person' AND m.target_id = r.household_person_id) AS marker
 FROM account_requests r LEFT JOIN household_people h ON h.id = r.household_person_id
 WHERE r.source = 'household_conversion' AND r.status = 'approved' AND r.decided_by = ?`
	var reqs []auditConversionRequestRow
	if err := tx.Raw(requests, uid).Scan(&reqs).Error; err != nil {
		return out, err
	}
	if len(reqs) == 0 {
		return out, nil
	}
	var candidates []auditConversionCandidate
	// This is one candidate query for all the admin's conversions, not one scan
	// of registration.changed per request. household_people is read unscoped.
	if err := tx.Raw(`WITH r AS (`+requests+`) SELECT r.id AS request_id, a.* FROM r JOIN audit_logs a ON
 (a.action = 'household.person_converted' OR (a.action = 'registration.changed' AND a.target_type = 'registration')) AND
 (a.meta_json->>'personId' = r.person_id::text OR a.meta_json->>'userId' = r.user_id::text OR a.meta_json->>'invitationId' = r.invitation_id::text OR
 (a.action = 'household.person_converted' AND a.target_type = 'household' AND a.target_id = r.household_id AND
 (jsonb_typeof(a.meta_json->'personId') IS DISTINCT FROM 'number' OR jsonb_typeof(a.meta_json->'userId') IS DISTINCT FROM 'number' OR jsonb_typeof(a.meta_json->'invitationId') IS DISTINCT FROM 'number')))
 ORDER BY r.id, a.id`, uid).Scan(&candidates).Error; err != nil {
		return out, err
	}
	groups := map[uint][]models.AuditLog{}
	for _, c := range candidates {
		groups[c.RequestID] = append(groups[c.RequestID], c.AuditLog)
	}
	discoveredIDs := map[uint]bool{}
	idsByReason := map[string]map[uint]bool{}
	for _, r := range reqs {
		// The false is deliberate, not a setting or an operator assertion. Tests
		// exercise V4 separately without offering a production bypass of G-8.
		detail := classifyAuditConversion(r, groups[r.ID], false)
		out.Groups++
		if idsByReason[detail.Reason] == nil {
			idsByReason[detail.Reason] = map[uint]bool{}
		}
		for _, row := range groups[r.ID] {
			discoveredIDs[row.ID] = true
			idsByReason[detail.Reason][row.ID] = true
		}
		out.GroupsByReason[detail.Reason]++
		out.Details = append(out.Details, detail)
	}
	// A malformed household fallback may belong to several candidate groups.
	// Per-group counts retain that overlap; totals count each actual row once.
	out.Discovered = int64(len(discoveredIDs))
	out.Retained = out.Discovered // G-8 is disabled; no transfer IP was written.
	for reason, ids := range idsByReason {
		out.ByReason[reason] = int64(len(ids))
	}
	return out, nil
}

// classifyAuditConversion implements V0-V4 in order. It reads no current kind,
// account type, IP, timestamp, row adjacency or registration owner. Even the
// history-established test mode is classification only and cannot write rows.
func classifyAuditConversion(r auditConversionRequestRow, rows []models.AuditLog, historyEstablished bool) auditConversionGroupCounts {
	out := auditConversionGroupCounts{TransferCount: -1}
	if r.HouseholdID == nil {
		out.HouseholdFallbackSkipped = 1
	}
	var converted []map[string]json.RawMessage
	targets := map[uint]bool{}
	malformed, duplicateTarget := false, false
	for _, row := range rows {
		var meta map[string]json.RawMessage
		if json.Unmarshal([]byte(row.MetaJSON), &meta) != nil {
			malformed = true
		}
		identity := auditJSONID(meta["personId"], r.PersonID) && auditJSONID(meta["userId"], r.UserID) && auditJSONID(meta["invitationId"], r.InvitationID)
		if row.Action == audit.HouseholdPersonConverted {
			out.ConvertedFound++
			converted = append(converted, meta)
			if !identity || row.TargetType != "household" || !auditJSONID(meta["householdId"], row.TargetID) || (r.HouseholdID != nil && row.TargetID != *r.HouseholdID) {
				malformed = true
			}
		} else {
			out.TransfersFound++
			_, former := meta["formerRegisteredBy"]
			_, cleared := meta["clearedChildcareRequested"]
			if !identity || row.TargetType != "registration" || !former || !cleared {
				malformed = true
			}
			if targets[row.TargetID] {
				duplicateTarget = true
			}
			targets[row.TargetID] = true
		}
	}
	out.ConvertedRetained, out.TransfersRetained = out.ConvertedFound, out.TransfersFound
	if len(converted) == 1 {
		if n, ok := auditJSONCount(converted[0]["transferCount"]); ok {
			out.TransferCount = n
		}
	}
	switch {
	case malformed:
		out.Reason = "malformed_candidate"
	case out.ConvertedFound == 0:
		out.Reason = "no_converted_row"
	case out.ConvertedFound != 1:
		out.Reason = "duplicate_converted_row"
	case out.TransferCount < 0 || out.TransferCount != out.TransfersFound || duplicateTarget:
		out.Reason = "count_mismatch"
	case !historyEstablished:
		out.Reason = "history_unestablished"
	default:
		var accountType string
		_ = json.Unmarshal(converted[0]["accountType"], &accountType)
		switch {
		case accountType == "teen" || (accountType == "adult" && r.Marker):
			out.Reason = "completion"
		case accountType != "adult":
			out.Reason = "unclassified"
		default:
			out.Reason = "approval"
		}
	}
	return out
}

func auditJSONID(raw json.RawMessage, id uint) bool {
	// json.Number rejects strings, booleans and containers. Rational comparison
	// also handles JSON numbers such as 1.0 without float64 identity rounding.
	var n json.Number
	if len(raw) == 0 || raw[0] == '"' || json.Unmarshal(raw, &n) != nil {
		return false
	}
	return auditJSONNumberEquals(n.String(), strconv.FormatUint(uint64(id), 10))
}

func auditJSONCount(raw json.RawMessage) (int64, bool) {
	var n json.Number
	if len(raw) == 0 || raw[0] == '"' || json.Unmarshal(raw, &n) != nil {
		return 0, false
	}
	// Parse the JSON number as an exact rational, requiring an integer in range.
	return auditJSONInteger(n.String())
}

func auditJSONNumberEquals(value, want string) bool {
	n, ok := new(big.Rat).SetString(value)
	if !ok {
		return false
	}
	w, ok := new(big.Rat).SetString(want)
	return ok && n.Cmp(w) == 0
}

func auditJSONInteger(value string) (int64, bool) {
	n, ok := new(big.Rat).SetString(value)
	if !ok || !n.IsInt() || n.Sign() < 0 || !n.Num().IsInt64() {
		return 0, false
	}
	return n.Num().Int64(), true
}
