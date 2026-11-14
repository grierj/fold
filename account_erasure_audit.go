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
// An action no writer uses (absent from the registry) loses its IP when its actor
// is the erased person (S5); the registered actions follow their rules, and an
// unmatched shape of a registered action or any other unknown row keeps its IP
// (S2, S3).
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
	// auditIPHistoricalActor counts S5's rows. It is never an attribution rule
	// class, so it is not in the CASE, the retention report or the public report.
	auditIPHistoricalActor auditIPClass = "HISTORICAL_ACTOR"
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
	audit.ContactPrivacyChanged:    auditIPOrdinary,
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
	// Only counts and fixed class/action/reason codes leave the helper. Retention
	// counts describe remaining nonempty IPs on rows linked to this subject, not
	// proven IP ownership. Unrelated database-wide residue is not counted.
	BlankedByClass       map[auditIPClass]int64
	KeptByClassAndAction map[auditIPClass]map[string]int64
	Conversion           auditConversionCounts
}

type auditConversionCounts struct {
	Groups     int64
	Discovered int64
	Retained   int64
	Blanked    int64
	ByReason   map[string]int64
	Details    []auditConversionGroupCounts
}

type auditConversionGroupCounts struct {
	ConvertedFound           int64
	TransfersFound           int64
	ConvertedRetained        int64
	TransfersRetained        int64
	Blanked                  int64
	TransferCount            int64 // -1 means missing or not a non-negative integer.
	HouseholdFallbackSkipped int64
	// Reason is a diagnostic classification, not a retention ledger: a cleared
	// group keeps its history_unestablished reason.
	Reason string
	// Identified is G1 to G5 of design r4-conversion-ip-minimization section 5. It
	// is set by classifyAuditConversion from the same facts as Reason, never read
	// off it, so a later reason cannot widen what is cleared.
	Identified bool
}

// auditHistoricalActorSQL is S5. An action absent from the registry has no
// current writer, so a row of one whose actor is the erased person comes from an
// earlier build and has no shape to check: it reads neither meta_json nor the
// target. A registered action, however malformed, is never reached; the registry
// is bound when the erasure runs, so an action added later leaves S5.
const auditHistoricalActorSQL = `UPDATE audit_logs SET ip = '' WHERE ip <> '' AND actor_id = @uid AND action <> ALL(CAST(@registered AS text[]))`

// Mentioning a subject is sufficient for a retention report, never for an
// IP update. Match malformed string IDs too, without casting untrusted JSON.
// Bind the uint as bigint before converting it to text; pgx cannot encode a
// uint directly for a parameter PostgreSQL infers to be text.
const auditSubjectMention = `actor_id = @uid OR (target_type = 'user' AND target_id = @uid)
 OR meta_json->'userId' = to_jsonb(CAST(@uid AS bigint)) OR meta_json->>'userId' = CAST(CAST(@uid AS bigint) AS text)
 OR meta_json->'teenUserId' = to_jsonb(CAST(@uid AS bigint)) OR meta_json->>'teenUserId' = CAST(CAST(@uid AS bigint) AS text)
 OR EXISTS (SELECT 1 FROM unnest(CAST(@people AS bigint[])) p(id)
 WHERE meta_json->'personId' = to_jsonb(p.id) OR meta_json->>'personId' = p.id::text)`

// auditRetentionReportSQL is the statement the erasure runs to report the
// audit rows whose IP it keeps. The QC-1 harness finds it in the server log by
// its text and checks what it found against deploy/digitalocean/qc1/app-facts.json,
// which TestQC1AppFacts holds equal to this function's output.
func auditRetentionReportSQL(classification string) string {
	return "SELECT class, action, count(*) FROM (SELECT action, " + classification + " AS class FROM audit_logs WHERE ip <> '' AND ((" + auditSubjectMention + ") OR (" + auditMalformedConversionLink + "))) classified WHERE class IN ('RETAIN_ESSENTIAL', 'RETAIN_UNATTRIBUTED') GROUP BY class, action"
}

// eraseAuditDetailsIn runs within account deletion, after dependent records and
// before the owned throttle keys (which stay last, detected limit D2).
// Its caller supplies the transaction and the sets captured before any scrub.
// It changes neither audit attribution nor authorization/safeguarding evidence.
func eraseAuditDetailsIn(tx *gorm.DB, s erasureSubject) (auditErasureCounts, error) {
	out := auditErasureCounts{BlankedByClass: map[auditIPClass]int64{}, KeptByClassAndAction: map[auditIPClass]map[string]int64{}}
	args := []any{sql.Named("uid", s.ID), sql.Named("requests", pgBigintArray(s.Requests)), sql.Named("invitations", pgBigintArray(s.Invitations)), sql.Named("people", pgBigintArray(s.People))}
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
	res := tx.Exec(auditHistoricalActorSQL, sql.Named("uid", s.ID), sql.Named("registered", pgTextArray(actions)))
	if res.Error != nil {
		return auditErasureCounts{}, res.Error
	}
	out.BlankedByClass[auditIPHistoricalActor] = res.RowsAffected
	out.IPsBlanked += res.RowsAffected
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
	if err := tx.Raw(auditRetentionReportSQL(classification), args...).Scan(&kept).Error; err != nil {
		return auditErasureCounts{}, err
	}
	for _, row := range kept {
		if out.KeptByClassAndAction[row.Class] == nil {
			out.KeptByClassAndAction[row.Class] = map[string]int64{}
		}
		out.KeptByClassAndAction[row.Class][row.Action] = row.Count
	}
	var err error
	out.Conversion, err = discoverAuditConversionsIn(tx, s.ID)
	if err != nil {
		return auditErasureCounts{}, err
	}
	out.BlankedByClass[auditIPConversion] = out.Conversion.Blanked
	out.IPsBlanked += out.Conversion.Blanked
	return out, nil
}

// auditConversionClearSQL is step 5 of eraseAuditDetailsIn, P2: the one statement
// that writes ip on a conversion row. It takes only the ids discovery cleared,
// and repeats the fixed action, target and key that validation already proved, so
// a wrong id list can never change another kind of row. RETURNING id is what
// each group's Blanked is counted from.
const auditConversionClearSQL = `UPDATE audit_logs SET ip = ''
 WHERE id = ANY(CAST(? AS bigint[])) AND ip <> ''
 AND ((action = 'household.person_converted' AND target_type = 'household')
 OR (action = 'registration.changed' AND target_type = 'registration'
 AND (jsonb_exists(meta_json, 'invitationId') OR jsonb_exists(meta_json, 'formerRegisteredBy'))))
 RETURNING id`

// The three identity links of an approved conversion are nullable in the schema
// (the user's is ON DELETE SET NULL). A missing one is nil, never 0: it keeps the
// request discoverable for the report and keeps its group out of the cleared set (G5).
type auditConversionRequestRow struct {
	ID           uint
	PersonID     *uint
	InvitationID *uint
	UserID       *uint
	HouseholdID  *uint
	Marker       bool
}

type auditConversionCandidate struct {
	RequestID uint
	models.AuditLog
}

// Discovery uses retained keys before checking any shape. A wrong household,
// string identity or missing writer key remains in its whole candidate group.
// The candidate callers of a request are the five keys of design
// r4-conversion-ip-minimization section 3: K1 is the decider and is read by the
// first statement; K2 to K5 follow in a statement of their own, because the
// QC-1 harness holds the text of the first one and counts the blocks that begin
// as it does (TestAuditDiscoveryStatementsAndBinds). The two are one set, joined
// by request id here. Plan 6.5.1 specifies text-equal discovery: stored numbers
// such as 5.0 may be missed when no other arm matches, although auditJSONID
// accepts them during validation. A row nothing discovers keeps its address.
//
// A group is cleared (P2) only when it is identified (G1 to G5) and none of its
// rows also belongs to a group that is not. G-8 stays unestablished: nothing here
// reads a kind, an account type, a marker, a time or the address itself.
func discoverAuditConversionsIn(tx *gorm.DB, uid uint) (auditConversionCounts, error) {
	out := auditConversionCounts{ByReason: map[string]int64{}}
	// K1. Its text is what the QC-1 harness checks the log against, byte for byte,
	// and nothing may be sent between the retention report and this statement.
	const requests = `SELECT r.id, r.household_person_id AS person_id, r.invitation_id, r.approved_user_id AS user_id, h.household_id,
 EXISTS (SELECT 1 FROM audit_logs m WHERE m.action = 'household.person_updated' AND m.target_type = 'household_person' AND m.target_id = r.household_person_id) AS marker
 FROM account_requests r LEFT JOIN household_people h ON h.id = r.household_person_id
 WHERE r.source = 'household_conversion' AND r.status = 'approved' AND r.decided_by = ?`
	// K2 to K5: the inviter, a confirmer of a link for the converted account, the
	// recorder of its account consent, and an admin who enabled it. No arm reads a
	// time, an address, a name or a kind, so a late confirmer or enabler is a
	// candidate too (J5). It must not begin as K1 does.
	const laterRequests = `WITH subject AS (SELECT CAST(? AS bigint) AS id)
 SELECT r.id, r.household_person_id AS person_id, r.invitation_id, r.approved_user_id AS user_id, h.household_id,
 EXISTS (SELECT 1 FROM audit_logs m WHERE m.action = 'household.person_updated' AND m.target_type = 'household_person' AND m.target_id = r.household_person_id) AS marker
 FROM account_requests r CROSS JOIN subject s LEFT JOIN household_people h ON h.id = r.household_person_id
 WHERE r.source = 'household_conversion' AND r.status = 'approved'
 AND (EXISTS (SELECT 1 FROM invitations i WHERE i.id = r.invitation_id AND i.invited_by = s.id)
 OR EXISTS (SELECT 1 FROM guardian_links g WHERE g.teen_user_id = r.approved_user_id AND g.confirmed_by = s.id)
 OR EXISTS (SELECT 1 FROM teen_consents c WHERE c.teen_user_id = r.approved_user_id AND c.granted_by = s.id AND c.scope = 'account' AND c.status = 'granted')
 OR EXISTS (SELECT 1 FROM audit_logs e WHERE e.action = 'user.enabled' AND e.target_type = 'user' AND e.target_id = r.approved_user_id AND e.actor_id = s.id))`
	var decided, later []auditConversionRequestRow
	if err := tx.Raw(requests, uid).Scan(&decided).Error; err != nil {
		return out, err
	}
	if err := tx.Raw(laterRequests, uid).Scan(&later).Error; err != nil {
		return out, err
	}
	// Ascending request id makes Details and every later step deterministic.
	seen := map[uint]bool{}
	var reqs []auditConversionRequestRow
	for _, r := range append(decided, later...) {
		if !seen[r.ID] {
			seen[r.ID] = true
			reqs = append(reqs, r)
		}
	}
	if len(reqs) == 0 {
		return out, nil
	}
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].ID < reqs[j].ID })
	ids := make([]uint, len(reqs))
	for i, r := range reqs {
		ids[i] = r.ID
	}
	var candidates []auditConversionCandidate
	// This is one candidate query for all the person's conversions, not one scan
	// of registration.changed per request. household_people is read unscoped.
	if err := tx.Raw(`WITH r AS (SELECT r.id, r.household_person_id AS person_id, r.invitation_id, r.approved_user_id AS user_id, h.household_id
 FROM account_requests r LEFT JOIN household_people h ON h.id = r.household_person_id
 WHERE r.id = ANY(CAST(? AS bigint[]))) SELECT r.id AS request_id, a.* FROM r JOIN audit_logs a ON
 (a.action = 'household.person_converted' OR (a.action = 'registration.changed' AND a.target_type = 'registration')) AND
 (a.meta_json->>'personId' = r.person_id::text OR a.meta_json->>'userId' = r.user_id::text OR a.meta_json->>'invitationId' = r.invitation_id::text OR
 (a.action = 'household.person_converted' AND a.target_type = 'household' AND a.target_id = r.household_id AND
 (jsonb_typeof(a.meta_json->'personId') IS DISTINCT FROM 'number' OR jsonb_typeof(a.meta_json->'userId') IS DISTINCT FROM 'number' OR jsonb_typeof(a.meta_json->'invitationId') IS DISTINCT FROM 'number')))
 ORDER BY r.id, a.id`, pgBigintArray(ids)).Scan(&candidates).Error; err != nil {
		return out, err
	}
	groups := map[uint][]models.AuditLog{}
	for _, c := range candidates {
		groups[c.RequestID] = append(groups[c.RequestID], c.AuditLog)
	}
	discoveredIDs := map[uint]bool{}
	idsByReason := map[string]map[uint]bool{}
	identified, other := map[uint]bool{}, map[uint]bool{}
	for _, r := range reqs {
		detail := classifyAuditConversion(r, groups[r.ID])
		out.Groups++
		if idsByReason[detail.Reason] == nil {
			idsByReason[detail.Reason] = map[uint]bool{}
		}
		for _, row := range groups[r.ID] {
			discoveredIDs[row.ID] = true
			idsByReason[detail.Reason][row.ID] = true
			if detail.Identified {
				identified[row.ID] = true
			} else {
				other[row.ID] = true
			}
		}
		out.Details = append(out.Details, detail)
	}
	// The cleared set is the rows of identified groups that no other group
	// claims. A valid row of one fully linked request cannot match another's
	// arms today; a restored backup or a link-missing request can break that, and
	// the subtraction keeps it failing closed.
	var cleared []uint
	clearedSet := map[uint]bool{}
	for id := range identified {
		if !other[id] {
			cleared = append(cleared, id)
			clearedSet[id] = true
		}
	}
	sort.Slice(cleared, func(i, j int) bool { return cleared[i] < cleared[j] })
	// Step 5. The ids are sorted for determinism only, not for lock order.
	changed := map[uint]bool{}
	if err := eachIDChunk(cleared, func(chunk []uint) error {
		var got []uint
		if err := tx.Raw(auditConversionClearSQL, pgBigintArray(chunk)).Scan(&got).Error; err != nil {
			return err
		}
		for _, id := range got {
			changed[id] = true
		}
		return nil
	}); err != nil {
		return out, err
	}
	// Per-group counts follow the rows the statement returned. A row shared by two
	// groups counts in each group's found counts and once in every total, and a
	// changed row is attributed to the first group (lowest request id) that has it.
	claimed := map[uint]bool{}
	for i := range out.Details {
		d := &out.Details[i]
		for _, row := range groups[reqs[i].ID] {
			if !clearedSet[row.ID] {
				continue
			}
			if row.Action == audit.HouseholdPersonConverted {
				d.ConvertedRetained--
			} else {
				d.TransfersRetained--
			}
			if changed[row.ID] && !claimed[row.ID] {
				claimed[row.ID] = true
				d.Blanked++
			}
		}
	}
	out.Discovered = int64(len(discoveredIDs))
	out.Retained = out.Discovered - int64(len(cleared))
	out.Blanked = int64(len(changed))
	for reason, ids := range idsByReason {
		out.ByReason[reason] = int64(len(ids))
	}
	return out, nil
}

// classifyAuditConversion implements V0-V3 in order, and sets Identified (G1 to
// G5). It reads no current kind, account type, IP, timestamp, row adjacency or
// registration owner. V4 needs G-8 (established history), which is unestablished:
// a group that passes V0-V3 is history_unestablished, and nothing can offer a
// production bypass of G-8. P2 does not need V4, because it does not decide who
// the caller was.
func classifyAuditConversion(r auditConversionRequestRow, rows []models.AuditLog) auditConversionGroupCounts {
	out := auditConversionGroupCounts{TransferCount: -1}
	if r.HouseholdID == nil {
		out.HouseholdFallbackSkipped = 1
	}
	// G5: a request that lacks a link names no real row, so it is reported
	// malformed and never identified.
	linked := r.PersonID != nil && r.InvitationID != nil && r.UserID != nil
	var converted []map[string]json.RawMessage
	targets := map[uint]bool{}
	malformed, duplicateTarget := !linked, false
	for _, row := range rows {
		var meta map[string]json.RawMessage
		if json.Unmarshal([]byte(row.MetaJSON), &meta) != nil {
			malformed = true
		}
		identity := linked && auditJSONID(meta["personId"], *r.PersonID) && auditJSONID(meta["userId"], *r.UserID) && auditJSONID(meta["invitationId"], *r.InvitationID)
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
	default:
		out.Reason = "history_unestablished"
	}
	// G1 (V0), G2 (V1), G3 (V2), G4 (the household was read, so the check of the
	// converted row was real) and G5.
	out.Identified = !malformed && len(converted) == 1 && out.TransferCount >= 0 && out.TransferCount == out.TransfersFound && !duplicateTarget && r.HouseholdID != nil && linked
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
