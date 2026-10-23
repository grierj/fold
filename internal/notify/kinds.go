package notify

import (
	"fmt"
	"strings"
	"time"
)

// Categories (5.3). Settings per category arrive in M3.12.
const (
	CatAdmin  = "admin"
	CatGroups = "groups"
	CatEvents = "events"
	// Planned (PLAN 4, notifications.category); no kind uses them yet.
	CatMessages = "messages"
	CatSafety   = "safety"
)

// Categories lists every category; the SQL CHECK and the TS union must
// match it (TestNotificationCategoriesMatch in guards_test.go).
var Categories = []string{CatAdmin, CatGroups, CatEvents, CatMessages, CatSafety}

// Kind keys. Add a kind here and register it; TestEveryKindConstIsRegistered
// fails on a key that is never registered, and the registry test renders
// every kind with teen and hidden-adult fixtures.
const (
	GroupPending            = "group.pending"
	GroupDeletion           = "group.deletion"
	LeadershipChanged       = "group.leadership_changed"
	MemberNotRestored       = "membership.not_restored"
	OccurrenceCancelled     = "event.cancelled"
	RegistrationCancelled   = "registration.cancelled"
	VisibilityOverride      = "event.visibility_override"
	AccountRequested        = "account.requested"
	EventChanged            = "event.changed"
	RegistrationPromoted    = "registration.promoted"
	RegistrationRemoved     = "registration.removed"
	RegistrationNotRestored = "registration.not_restored"
	WaitlistCutoff          = "waitlist.cutoff"
	HouseholdLinkPending    = "household.link_pending"
	HouseholdConverted      = "household.converted"
	TeenConsentChanged      = "teen_consent.changed"
	AccountTurned18         = "account.turned_18"
	HouseholdChildTurned13  = "household.child_turned_13"
)

// Action is an inline button. Only allow-listed kinds carry one (6.6).
type Action struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// ActionApproveGroup approves a pending group from the bell.
const ActionApproveGroup = "approve"

// ActionApproveAccount approves an adult account request with no
// warnings from the bell (DEC-44). Send suppresses it per row for teen
// or flagged requests; the endpoint checks again (policy).
const ActionApproveAccount = "approve"

// Vars are the only things a kind can say. Callers fill in names and places
// the recipient may see; empty means "don't say".
type Vars struct {
	Actor      string // who did it
	Person     string // who it was done to
	Group      string // the group's name, only if the recipient can see it
	Event      string // the event's title (or a safe fallback)
	When       string // the date and time, already in the recipient's zone (DEC-58)
	Place      string // only if the recipient can see it
	Change     string // a kind-specific variant
	Visibility string // new visibility, in words
	GroupVis   string // the group's visibility, in words
	Note       string // a short reason the actor gave
	Count      int    // set by Send: how many events this row covers
	Teen       bool   // the subject is a teen account (badge in words; no contact details ever)
	// ManageURL is a full signed URL the recipient can act on (for
	// example a registration manage link). It appears in the email body
	// only, never in the bell, and only on rows for that recipient.
	ManageURL string
	// VideoURL is the event's video link (5.8, M1.8). The caller sets it
	// only when this recipient may see it (policy.CanSeeVideoLink); it
	// appears in the email body only, never in the bell.
	VideoURL string
}

// Kind is one registry entry (5.12).
type Kind struct {
	Key         string
	Category    string
	Waiting     bool          // a task: counted until resolved, never batched
	BatchWindow time.Duration // 0: never batched
	Actions     []Action      // the inline allow-list
	Render      func(Vars) (title, body string)
	// AllowEmail sends a transactional email through the minimal sender
	// right after the writing transaction commits (5.12, M1.2). The send
	// is recorded in notification_deliveries.
	AllowEmail bool
	// RenderEmail renders the email title and body. Nil means
	// RenderForEmail: the RenderEmailSafe link-only variant when the
	// kind has one, Render otherwise. A kind whose email must carry
	// something the bell must not (for example a signed manage link)
	// sets this.
	RenderEmail func(Vars) (title, body string)
	// RenderEmailSafe is the link-only variant for sensitive kinds
	// (minors, consent, contact access): it names nobody and only links
	// into Fold. Nil means Render is already safe.
	RenderEmailSafe func(Vars) (title, body string)
}

// RenderForEmail renders k for email: the kind's RenderEmail when set,
// the link-only safe variant when the kind has one, Render otherwise.
func (k Kind) RenderForEmail(v Vars) (title, body string) {
	if k.RenderEmail != nil {
		return k.RenderEmail(v)
	}
	if k.RenderEmailSafe != nil {
		return k.RenderEmailSafe(v)
	}
	return k.Render(v)
}

var registry = map[string]Kind{}

func register(k Kind) { registry[k.Key] = k }

// Lookup finds a kind by key.
func Lookup(key string) (Kind, bool) {
	k, ok := registry[key]
	return k, ok
}

// Kinds lists every registered kind (for tests).
func Kinds() []Kind {
	out := make([]Kind, 0, len(registry))
	for _, k := range registry {
		out = append(out, k)
	}
	return out
}

// Allows reports whether action is on kind's inline allow-list. The server
// checks this; the button is never the gate (6.13 #5).
func Allows(kind, action string) bool {
	k, ok := Lookup(kind)
	if !ok {
		return false
	}
	for _, a := range k.Actions {
		if a.Key == action {
			return true
		}
	}
	return false
}

func or(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func join(parts ...string) string {
	out := []string{}
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " · ")
}

func init() {
	register(Kind{Key: AccountTurned18, Category: CatSafety, AllowEmail: true,
		Render: func(Vars) (string, string) {
			return "Your account is now an adult account", "Teen consent is no longer required. Your consent history stays on file. An admin's suspension, if any, still applies."
		},
		RenderEmailSafe: func(Vars) (string, string) {
			return "Your Fold account was updated", "Sign in to Fold to review your account."
		},
	})
	register(Kind{Key: HouseholdChildTurned13, Category: CatSafety, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			return or(v.Person, "A child in your household") + " is now 13 or older", "Review their birth date and age group in My household. They can also request their own account. Their sign-ups stay with your household."
		},
		RenderEmailSafe: func(Vars) (string, string) {
			return "Review a household member's age in Fold", "Sign in to Fold to review your household."
		},
	})
	register(Kind{
		Key: TeenConsentChanged, Category: CatSafety, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			return "Teen consent changed", or(v.Person, "A teen") + ": " + v.Change + "."
		},
		RenderEmailSafe: func(Vars) (string, string) {
			return "Consent changed in Fold", "Sign in to Fold to review a consent change."
		},
	})
	register(Kind{
		Key: GroupPending, Category: CatAdmin, Waiting: true, AllowEmail: true,
		Actions: []Action{{Key: ActionApproveGroup, Label: "Approve"}},
		Render: func(v Vars) (string, string) {
			return or(v.Actor, "Someone") + " started a group: " + or(v.Group, "a new group"),
				"It needs an admin's approval before people can find it."
		},
	})
	register(Kind{
		Key: GroupDeletion, Category: CatAdmin, Waiting: true, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			return or(v.Actor, "Someone") + " asked to delete " + or(v.Group, "a group"),
				"They are its only member. Delete the group or keep it."
		},
	})
	register(Kind{
		Key: LeadershipChanged, Category: CatAdmin, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			actor, person := or(v.Actor, "Fold"), or(v.Person, "Someone")
			var title string
			switch v.Change {
			case "added":
				title = actor + " made " + person + " a leader"
			case "stepped_down":
				title = person + " stepped down as a leader"
			case "removed_from_group":
				title = actor + " removed " + person + " (a leader) from the group"
			default:
				title = actor + " removed " + person + " as a leader"
			}
			return title, v.Group
		},
	})
	register(Kind{
		Key: MemberNotRestored, Category: CatGroups, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			return "You're not back in " + or(v.Group, "one of your groups") + " yet",
				"The group was full when your account was turned back on. Ask a leader to add you."
		},
	})
	register(Kind{
		Key: OccurrenceCancelled, Category: CatEvents, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			return "Cancelled: " + or(v.Event, "Church event"), join(v.When, v.Place)
		},
	})
	register(Kind{
		Key: RegistrationCancelled, Category: CatEvents, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			return "Registration cancelled: " + or(v.Event, "An event you registered for"),
				"This event is now limited to its group's members, so your registration for upcoming dates was cancelled."
		},
	})
	register(Kind{
		Key: VisibilityOverride, Category: CatGroups, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			body := fmt.Sprintf("%s opened it up. Now %s. The group is %s.", or(v.Actor, "Someone"), v.Visibility, v.GroupVis)
			if v.Note != "" {
				body += " " + v.Note
			}
			return "Event opened up: " + or(v.Event, "an event"), body
		},
	})
	register(Kind{
		// An account request waits on an admin (DEC-4, DEC-44). Inline
		// Approve is emitted only for a clean adult request: Send is
		// told to suppress it for teens and flagged requests, and the
		// approval endpoint checks policy again. The email variant
		// names nobody (minors may be involved) and only links in.
		Key: AccountRequested, Category: CatAdmin, Waiting: true, AllowEmail: true,
		Actions: []Action{{Key: ActionApproveAccount, Label: "Approve"}},
		Render: func(v Vars) (string, string) {
			who := or(v.Person, "Someone")
			if v.Teen {
				who += " (teen)"
			}
			return who + " requested an account", or(v.Change, "Self-signup") + ". Needs an admin's approval."
		},
		RenderEmailSafe: func(Vars) (string, string) {
			return "New account request in Fold", "Someone asked for an account. Sign in to review it."
		},
	})
	register(Kind{
		Key: HouseholdConverted, Category: CatSafety, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			body := "Their past and upcoming event sign-ups have moved to their account."
			return or(v.Person, "A household member") + " created their own account", body
		},
		RenderEmailSafe: func(Vars) (string, string) {
			return "A household account was created", "Sign in to Fold to review your household."
		},
	})
	register(Kind{
		// An event's time or place changed (5.12, M1.5). Vars.Change is
		// "time", "place", or "time and place". Never batched: one item
		// per person per occurrence per change.
		Key: EventChanged, Category: CatEvents, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			what := or(v.Change, "details")
			return "Updated: " + or(v.Event, "an event"),
				"The " + what + " changed. " + join(v.When, v.Place)
		},
	})
	register(Kind{
		// "You're in" off the waitlist (5.12, M1.5). The email carries
		// the signed manage link with the one-click "I can't come"
		// cancel; the bell never shows the token.
		Key: RegistrationPromoted, Category: CatEvents, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			if v.Person != "" {
				return v.Person + " is in: " + or(v.Event, "an event"), join(v.When, v.Place)
			}
			return "You're in: " + or(v.Event, "an event"), join(v.When, v.Place)
		},
		RenderEmail: func(v Vars) (string, string) {
			title, body := "You're in: "+or(v.Event, "an event"), join(v.When, v.Place)
			if v.Person != "" {
				title = v.Person + " is in: " + or(v.Event, "an event")
			}
			if v.VideoURL != "" {
				body += "\n\nJoin by video: " + v.VideoURL
			}
			if v.ManageURL != "" {
				body += "\n\nCan't come after all? Cancel your registration here:\n" + v.ManageURL
			}
			return title, body
		},
	})
	register(Kind{
		// A leader or host pruned a registration (DEC-20, DEC-30, M1.5).
		// The note reaches only the removed person.
		Key: RegistrationRemoved, Category: CatEvents, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			body := "A leader or host removed your registration for " + or(v.Event, "an event") + "."
			if v.Person != "" {
				body = v.Person + "'s registration was removed for " + or(v.Event, "an event") + "."
			}
			if v.Note != "" {
				body += " Note: " + v.Note
			}
			return "Registration removed: " + or(v.Event, "an event"), body
		},
	})
	register(Kind{
		// A re-enabled account was left out of a full occurrence
		// (DEC-38, DEC-47, M1.5): the registration was dropped and the
		// person is told to ask a leader or host.
		Key: RegistrationNotRestored, Category: CatEvents, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			if v.Person != "" {
				return v.Person + " could not be added back to " + or(v.Event, "an event"), "Ask a leader or host to add them after checking their household details and available seats."
			}
			return "You're not back in " + or(v.Event, "one of your events") + " yet",
				"The event was full when your account was turned back on. Ask a leader or host to add you."
		},
	})
	register(Kind{
		// A child or teen added to a household waits for an admin to
		// confirm the guardian link (DEC-32, M1.9): a task, like a pending
		// group, resolved when an admin confirms or rejects it. No inline
		// button: confirming needs a check note on the Approvals tab. The
		// bell names the people for admins; the email variant names
		// nobody, since a minor is involved. Change is the kind in words
		// ("child" or "teen").
		Key: HouseholdLinkPending, Category: CatAdmin, Waiting: true, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			if v.Change == "teen_account" {
				return "Check a guardian link for " + or(v.Person, "a teen account"), "The account is waiting for consent. Confirming this link does not let the teen sign in yet."
			}
			who := or(v.Person, "Someone")
			if v.Change != "" {
				who += " (" + v.Change + ")"
			}
			return or(v.Actor, "Someone") + " added " + who + " to their household",
				"The link needs an admin to confirm it before the person can be registered."
		},
		RenderEmailSafe: func(Vars) (string, string) {
			return "New household link to confirm in Fold", "Someone added a child or teen to their household. Sign in to review it."
		},
	})
	register(Kind{
		// Inside the 2-hour cutoff a freed seat does not auto-promote
		// (5.4, M1.5). The leaders or hosts get this instead and promote
		// by hand. No inline button: the action lives on the event page.
		Key: WaitlistCutoff, Category: CatEvents, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			return "Seat opened too close to the start: " + or(v.Event, "an event"),
				"A seat opened within 2 hours of " + or(v.When, "the start") + ", so nobody was promoted automatically. Promote someone from the waitlist by hand."
		},
	})
}
