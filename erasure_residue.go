package main

import "fold/internal/audit"

// The rules of what a deletion may leave in the database and the blob store
// (inventory 11.1, R4-6d-V). This file is data only, read by
// TestErasureLeavesOnlyTheNamedResidue in erasure_residue_test.go; no
// production path calls it and it holds no function, so `make deadcode` has
// nothing to report.
//
// A rule says where a sentinel of one owner may or must still be found after
// the real DELETE, the tombstone sweep and avatar.cleanup have run:
//
//   - MAY: the erased person's (U) text may stay, but only in the row and place it
//     was seeded in; the same text in another row of the same column is a failure.
//   - MUST: the value stays byte for byte. Another living person's (L) or the
//     church's (C) value must; so must a U value that safeguarding or an
//     admin's Keep decision requires.
//
// A rule is **enabled only while the PLAN names its disposition**: every anchor
// in Plan must still match its document. A rule with a missing anchor accepts
// nothing, so a sentinel a disabled rule would have excused is a failure. S1
// (DEC-148) says residue with no authorized disposition has none, and a rule
// is never enabled to make a test pass: change the PLAN first, then this file.

// residueOwner says whose sentinel a rule is about.
type residueOwner string

const (
	// residueU is the erased person.
	residueU residueOwner = "U"
	// residueL is another living person.
	residueL residueOwner = "L"
	// residueC is the church: settings, announcements, church-owned text.
	residueC residueOwner = "C"
)

// residueFate says what the PLAN allows at a location.
type residueFate string

const (
	// residueMay: the sentinel may stay.
	residueMay residueFate = "MAY"
	// residueMust: the value must stay, byte for byte.
	residueMust residueFate = "MUST"
)

// residueAnchor is one place the PLAN says it: a document and a pattern that
// must match in it. Patterns are Go regular expressions in multi-line mode.
type residueAnchor struct {
	File    string
	Pattern string
}

// erasureResidueRule is one named residue. Table is the name of a Go model in
// `models` (the proof resolves it to its table, so this file holds no table
// name for the SQL guards to read), or "(blob)" for an uploaded file, or
// "(outside Fold)" for a copy no database row or file reaches. Column is the
// database column, or the store kind of a blob. JSONKey is the
// top-level key of a JSON column ("" for a scalar). Where narrows a rule to
// rows whose own columns equal the given text (Where) or one of the given texts
// (WhereIn), so a rule for one action of the audit log accepts no other.
type erasureResidueRule struct {
	RuleID  string
	Table   string
	Column  string
	JSONKey string
	Where   map[string]string
	WhereIn map[string][]string
	Owner   residueOwner
	Fate    residueFate
	// Plan are the dispositions that enable the rule, all of them.
	Plan []residueAnchor
	// Why says what stays, in a sentence a reviewer can check against Plan.
	Why string
}

const (
	residueDecisions = "docs/plan/decisions.md"
	residueInventory = "docs/plan/M7.4-ERASURE-INVENTORY.md"
)

// residueDEC148 is the owner's disposition of the activity-log addresses
// (S2 to S5, DEC-148, #304 and #305) and the closed C3 checklist that maps
// every row to its arm.
var residueDEC148 = []residueAnchor{
	{residueDecisions, `(?m)^\| DEC-148 \| \*\*Network addresses in the activity log after an erasure`},
	{residueInventory, `(?m)^\*\*Status: G-9 is CLOSED\.\*\*`},
}

// erasureResidueRules is the whole table. A new rule needs a PLAN disposition
// first (the anchors), a seeded sentinel in TestErasureLeavesOnlyTheNamedResidue
// that names it, and a row in the mutation map.
var erasureResidueRules = []erasureResidueRule{
	// Activity-log network addresses (RS-3, DEC-148, inventory 6.5.2).
	{RuleID: "IR-1", Table: "AuditLog", Column: "ip", Where: map[string]string{"action": "household.person_converted"},
		Owner: residueU, Fate: residueMay,
		Plan: append([]residueAnchor{{residueInventory, `(?m)^\| IR-1 \| \*\*C-completion rows\*\*`}}, residueDEC148...),
		Why:  "a completion row's address, when no retained key names the caller (S3)"},
	{RuleID: "IR-2", Table: "AuditLog", Column: "ip", Where: map[string]string{"action": "registration.changed"},
		Owner: residueU, Fate: residueMay,
		Plan: append([]residueAnchor{{residueInventory, `(?m)^\| IR-2 \| \*\*Transfer rows of an unestablished history\*\*`}}, residueDEC148...),
		Why:  "a transfer row's address while the history is unestablished (S3)"},
	{RuleID: "IR-3", Table: "AuditLog", Column: "ip", Where: map[string]string{"action": "guardian_link.requested"},
		Owner: residueU, Fate: residueMay,
		Plan: append([]residueAnchor{{residueInventory, `(?m)^\| IR-3 \| \*\*Groups retained whole\*\*`}}, residueDEC148...),
		Why:  "a malformed conversion nomination's address (S2)"},
	{RuleID: "IR-4", Table: "AuditLog", Column: "ip", Where: map[string]string{"action": "household.person_converted", "target_type": "other"},
		Owner: residueU, Fate: residueMay,
		Plan: append([]residueAnchor{{residueInventory, `(?m)^\| IR-4 \| \*\*Rows no retained key reaches\*\*`}}, residueDEC148...),
		Why:  "a row no retained key reaches, here a conversion-shaped row with another target type (S3)"},
	{RuleID: "RETAIN_UNATTRIBUTED", Table: "AuditLog", Column: "ip", Where: map[string]string{"action": "user.created"},
		Owner: residueU, Fate: residueMay,
		Plan: append([]residueAnchor{{residueInventory, `(?m)^\| Unmatched, malformed or unknown shapes with a nonempty IP \| RETAIN_UNATTRIBUTED`}}, residueDEC148...),
		Why:  "a registered action whose row has no distinguishable caller (S2, S3)"},
	// No registry rule uses RETAIN_ESSENTIAL and R4-GR-IP found no essential
	// address, so there is nothing to disposition: the anchor is the row that
	// would say a registered action keeps one, and it does not exist.
	{RuleID: "RETAIN_ESSENTIAL", Table: "AuditLog", Column: "ip", Where: map[string]string{"action": "none"},
		Owner: residueU, Fate: residueMay,
		Plan: []residueAnchor{{residueInventory, `(?m)^\| RETAIN_ESSENTIAL \| a registered action keeps`}},
		Why:  "no action: the C3 table records that none exists"},

	// Safeguarding prose and its audit mirrors (O-4, 15.5, RS-6).
	{RuleID: "O-4-check-note", Table: "GuardianLink", Column: "check_note", Owner: residueU, Fate: residueMust,
		Plan: []residueAnchor{{residueInventory, `(?m)^\*\*O-4: CONFIRMED\*\* \(retention of named safeguarding prose`}},
		Why:  "an admin's note on a guardian check, immutable evidence of who agreed"},
	{RuleID: "O-4-evidence", Table: "TeenConsent", Column: "evidence_text", Owner: residueU, Fate: residueMust,
		Plan: []residueAnchor{{residueInventory, `(?m)^\*\*O-4: CONFIRMED\*\* \(retention of named safeguarding prose`}},
		Why:  "a teen consent's evidence text, immutable by trigger"},
	{RuleID: "O-4-mirror-check-note", Table: "AuditLog", Column: "meta_json", JSONKey: "checkNote", WhereIn: map[string][]string{"action": {audit.GuardianLinkConfirmed, audit.GuardianLinkRejected}}, Owner: residueU, Fate: residueMust,
		Plan: []residueAnchor{{residueInventory, `(?m)^\*\*O-4: CONFIRMED\*\* \(retention of named safeguarding prose`}},
		Why:  "the audit mirror of a guardian note"},
	{RuleID: "O-4-mirror-evidence", Table: "AuditLog", Column: "meta_json", JSONKey: "evidenceText", WhereIn: map[string][]string{"action": {audit.TeenConsentGranted, audit.TeenConsentRevoked}}, Owner: residueU, Fate: residueMust,
		Plan: []residueAnchor{{residueInventory, `(?m)^\*\*O-4: CONFIRMED\*\* \(retention of named safeguarding prose`}},
		Why:  "the audit mirror of a teen consent's evidence"},

	// A household other people still share (O-9, T16, RS-4).
	{RuleID: "O-9-name", Table: "Household", Column: "name", Owner: residueU, Fate: residueMust,
		Plan: []residueAnchor{{residueInventory, `(?m)^\*\*O-9: CONFIRMED\.\*\*`}},
		Why:  "a shared household keeps its name: it is another family's record"},
	{RuleID: "O-9-address", Table: "Household", Column: "address", Owner: residueL, Fate: residueMust,
		Plan: []residueAnchor{{residueInventory, `(?m)^\*\*O-9: CONFIRMED\.\*\*`}},
		Why:  "a shared household keeps its address"},

	// Copies outside Fold (O-12) and church settings (O-13).
	{RuleID: "O-12", Table: "(outside Fold)", Column: "mailboxes, provider and server logs, push services", Owner: residueU, Fate: residueMay,
		Plan: []residueAnchor{{residueInventory, `(?m)^\| O-12 \|[^|]*\|\s*\*\*Settled as a documented limitation\*\*`}},
		Why:  "no row or file of Fold reaches these; no sentinel can be bound to this rule"},
	{RuleID: "O-13-reply-to", Table: "Setting", Column: "value", Where: map[string]string{"key": "reply_to_address"}, Owner: residueC, Fate: residueMust,
		Plan: []residueAnchor{{residueInventory, `(?m)^\| O-13 \|[^|]*\|\s*\*\*Settled as a documented limitation\*\*`}},
		Why:  "an address an admin typed into church settings"},
	{RuleID: "O-13-church-address", Table: "Setting", Column: "value", Where: map[string]string{"key": "church_address"}, Owner: residueC, Fate: residueMust,
		Plan: []residueAnchor{{residueInventory, `(?m)^\| O-13 \|[^|]*\|\s*\*\*Settled as a documented limitation\*\*`}},
		Why:  "the church's address, typed by an admin"},

	// Names inside other people's notices (O-16): proposed, never authorized,
	// so this rule is disabled until the PLAN settles it.
	{RuleID: "O-16", Table: "Notification", Column: "body", Owner: residueU, Fate: residueMay,
		Plan: []residueAnchor{{residueInventory, `(?m)^\| O-16 \|[^|]*\|\s*\*\*(Settled|Confirmed)`}},
		Why:  "a name in a notice another person already received (RS-5)"},
	// Messages the person wrote (O-8) have no rule here either: RS-11 is
	// Proposed. The church's own announcement text is C-owned below.

	// Rows tied only to an address the person no longer held (O-17, O-21).
	{RuleID: "O-17-invitation", Table: "Invitation", Column: "email", Owner: residueU, Fate: residueMay,
		Plan: []residueAnchor{{residueInventory, `(?m)^\| O-17 \|[^|]*\|\s*\*\*Bounded limitation accepted`}},
		Why:  "an invitation nobody accepted, addressed to a past address"},
	{RuleID: "O-17-guest-signup", Table: "GuestSignup", Column: "email", Owner: residueU, Fate: residueMay,
		Plan: []residueAnchor{{residueInventory, `(?m)^\| O-17 \|[^|]*\|\s*\*\*Bounded limitation accepted`}},
		Why:  "a guest signup on a past address"},
	{RuleID: "O-17-guardian-address", Table: "AccountRequest", Column: "guardian_email", Owner: residueU, Fate: residueMay,
		Plan: []residueAnchor{{residueInventory, `(?m)^\| O-17 \|[^|]*\|\s*\*\*Bounded limitation accepted`}},
		Why:  "a guardian address on an account request no id link reaches"},
	{RuleID: "O-21", Table: "LoginThrottle", Column: "key", Owner: residueU, Fate: residueMay,
		Plan: []residueAnchor{{residueInventory, `(?m)^\| O-21 \|.*signed for R4-1 by the independent privacy reviewer`}},
		Why:  "a throttle key on an address the account did not hold at erasure"},

	// A guardian's copy of a message about the erased person (M23, T23).
	{RuleID: "M23", Table: "MessageRecipient", Column: "address", Owner: residueL, Fate: residueMust,
		Plan: []residueAnchor{{residueInventory, `(?m)^\| M23 \|[^|]*\|[^|]*\| RET \| address is the guardian's`}},
		Why:  "the guardian's address on the guardian's copy"},

	// A reason the admin chose to keep (T07, owner decision 6, RS-7).
	{RuleID: "T07-KEEP", Table: "ContactRequest", Column: "reason", Owner: residueU, Fate: residueMust,
		Plan: []residueAnchor{{residueInventory, `(?m)^\*\*Owner decision 6: contact-request reasons on erasure`}},
		Why:  "a reason the admin reviewed and chose Keep for stays as written"},

	// Church-owned content (T24, T09, DEC-118): the message text and the
	// pictures belong to the church, not to the author.
	{RuleID: "C-message-subject", Table: "Message", Column: "subject", Owner: residueC, Fate: residueMust,
		Plan: []residueAnchor{{residueInventory, `(?m)^\| RS-11 \|`}},
		Why:  "a message's subject is church content and follows the message, not its author"},
	{RuleID: "C-message-text", Table: "Message", Column: "body_text", Owner: residueC, Fate: residueMust,
		Plan: []residueAnchor{{residueInventory, `(?m)^\| RS-11 \|`}},
		Why:  "a message's text is church content and follows the message, not its author"},
	{RuleID: "C-message-image", Table: "(blob)", Column: "messages", Owner: residueC, Fate: residueMust,
		Plan: []residueAnchor{{"docs/agents/erasure.md", `(?m)^- A message's pictures \(`}},
		Why:  "a message's pictures are church content tied to the message (DEC-118)"},

	// Another living person's own rows (inventory 3, 4: other people's records).
	{RuleID: "L-household-person", Table: "HouseholdPerson", Column: "first_name", Owner: residueL, Fate: residueMust,
		Plan: []residueAnchor{{residueInventory, `(?m)^\| M29 \|[^|]*\|[^|]*\| RET \|`}},
		Why:  "another member of a shared household keeps their own record"},
	{RuleID: "L-notice", Table: "Notification", Column: "title", Owner: residueL, Fate: residueMust,
		Plan: []residueAnchor{{residueDecisions, `(?m)^\| DEC-144 \| \*\*Deleting an account erases the person\*\*`}},
		Why:  "a notice addressed to another person is theirs"},
	{RuleID: "L-avatar", Table: "(blob)", Column: "avatars", Owner: residueL, Fate: residueMust,
		Plan: []residueAnchor{{residueDecisions, `(?m)^\| DEC-144 \| \*\*Deleting an account erases the person\*\*`}},
		Why:  "another person's picture is untouched"},
	{RuleID: "L-user", Table: "User", Column: "phone", Owner: residueL, Fate: residueMust,
		Plan: []residueAnchor{{residueDecisions, `(?m)^\| DEC-144 \| \*\*Deleting an account erases the person\*\*`}},
		Why:  "another person's account row is untouched"},
	// A registered row whose actor is another living person keeps that person's own
	// address even when the erased person is its target (6.5.2, "Rows that need no arm").
	{RuleID: "L-audit-ip", Table: "AuditLog", Column: "ip", WhereIn: map[string][]string{"action": {audit.UserDisabled}}, Owner: residueL, Fate: residueMust,
		Plan: []residueAnchor{{residueInventory, `(?m)^\*\*Rows that need no arm\.\*\* A registered row whose shape matches and whose owner is another living person`}},
		Why:  "the living actor's own address on a row that names the erased person as its target"},

	// Hash-only records an erasure never clears (RS-8, T35, T38). These two
	// hold no sentinel, since the address is a code: the opt-out and
	// suppression stages check them by row, separately from the scan.
	{RuleID: "RETAIN-OPTOUT-HASH", Table: "EmailAddressOptOut", Column: "address_hash", Owner: residueU, Fate: residueMust,
		Plan: []residueAnchor{{residueInventory, `(?m)^\| RS-8 \|`}},
		Why:  "an address-level opt-out stays so the choice still holds"},
	{RuleID: "RETAIN-SUPPRESSION-HASH", Table: "Suppression", Column: "address_hash", Owner: residueU, Fate: residueMust,
		Plan: []residueAnchor{{residueInventory, `(?m)^\| RS-8 \|`}},
		Why:  "a suppression stays and holds only the address's hash since 00008"},
}
