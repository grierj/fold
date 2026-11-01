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
	AddSubjectReview        = "add.subject_review"
	AddAdminReview          = "add.admin_review"
	OccurrenceChanged       = "occurrence.changed"
	OccurrenceAdded         = "occurrence.added"
	OccurrenceUpdated       = "occurrence.updated"
	AddUpdated              = "add.updated"
	ContactSubjectReview    = "contact.subject_review"
	ContactAdminReview      = "contact.admin_review"
	ContactUpdated          = "contact.updated"
	GroupPending            = "group.pending"
	GroupDeletion           = "group.deletion"
	GroupApproved           = "group.approved"
	GroupRejected           = "group.rejected"
	LeadershipChanged       = "group.leadership_changed"
	MembershipDeparture     = "membership.departure"
	MembershipLeft          = "membership.left"
	MemberNotRestored       = "membership.not_restored"
	OccurrenceCancelled     = "event.cancelled"
	OccurrenceRestored      = "event.restored"
	RegistrationCancelled   = "registration.cancelled"
	VisibilityOverride      = "event.visibility_override"
	AccountRequested        = "account.requested"
	EventChanged            = "event.changed"
	RegistrationPromoted    = "registration.promoted"
	RegistrationRemoved     = "registration.removed"
	RegistrationNotRestored = "registration.not_restored"
	WaitlistCutoff          = "waitlist.cutoff"
	RoomLost                = "room.lost"
	RoomRequested           = "room.requested"
	RoomRequestDecided      = "room.request_decided"
	HouseholdLinkPending    = "household.link_pending"
	HouseholdConverted      = "household.converted"
	TeenConsentChanged      = "teen_consent.changed"
	AccountTurned18         = "account.turned_18"
	HouseholdChildTurned13  = "household.child_turned_13"
	PushKeysReset           = "push.keys_reset"
	PushDevicesOff          = "push.devices_off"
	YourRole                = "group.your_role"
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
	KeptCount  int    // departure summary: exact dates retained
	EndedCount int    // departure summary: upcoming registrations ended
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
	Key      string
	Category string
	Waiting  bool // a task: counted until resolved, never batched
	// Required email can't be turned off (DEC-97, research 6.8): safety
	// kinds (minors, consent, contact access) and tasks waiting on the
	// recipient. register sets it; everything else is optional email.
	Required    bool
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
	// AllowPush sends the row to the recipient's devices with push on
	// (M3.13b, DEC-124). A lock screen is public, so PushRender is the
	// kind's own short text, never the bell body cut short: never a
	// contact detail, teen badge, note, place, signed or video link, or
	// Data. Safety, consent and contact kinds push only the safe text.
	// Default off.
	AllowPush  bool
	PushRender func(Vars) (title, body string)
	// PushSoon marks a change to a dated event: its push is urgent (a short
	// TTL, high urgency) when the date starts within 24 hours.
	PushSoon bool
}

// RenderForPush is the row's lock-screen text; ok is false when the kind
// doesn't push.
func (k Kind) RenderForPush(v Vars) (title, body string, ok bool) {
	if !k.AllowPush || k.PushRender == nil {
		return "", "", false
	}
	title, body = k.PushRender(v)
	return title, body, title != ""
}

// SafePushTitle and SafePushBody are the whole push for safety, consent
// and contact kinds, and for rows about someone in a household (research
// 2.4): they name nobody. The stored SafePushTitle is replaced with the
// church's name (site_name) when the push is sent (DEC-134 B), since the
// registry looks nothing up. (Variables, not constants: a dotted string
// constant in this file reads as a kind key to TestEveryKindConstIsRegistered.)
var (
	SafePushTitle = "Fold"
	SafePushBody  = "Open Fold to review an update."
)

func safePush(Vars) (string, string) { return SafePushTitle, SafePushBody }

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

func register(k Kind) {
	k.Required = k.Category == CatSafety || k.Waiting
	// Safety, consent and contact kinds push only the safe text (DEC-124),
	// whatever the entry says.
	if k.Category == CatSafety && k.AllowPush {
		k.PushRender = safePush
	}
	registry[k.Key] = k
}

// OptionalEmailCategories are the categories "Turn off all optional email"
// opts out of (DEC-97). Required kinds send whatever their category's
// consent says, so a category holding both kinds is still listed.
func OptionalEmailCategories() []string {
	return []string{CatAdmin, CatGroups, CatEvents, CatMessages}
}

// CategoryEmailName is what an optional category's emails are called in an
// email footer and on the email settings page (M3.3): "group updates".
func CategoryEmailName(category string) string {
	switch category {
	case CatAdmin:
		return "admin notices"
	case CatGroups:
		return "group updates"
	case CatEvents:
		return "event updates"
	case CatMessages:
		// Announcements that leaders, hosts and admins write (DEC-119).
		return "messages from leaders"
	}
	return "these emails"
}

// YoureGetting starts an optional email's footer: "You're getting group
// updates from First Church". Messages from leaders are "at" the church, so
// the sentence doesn't say "from" twice.
func YoureGetting(category, church string) string {
	prep := " from "
	if category == CatMessages {
		prep = " at "
	}
	return "You're getting " + CategoryEmailName(category) + prep + church
}

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
	register(Kind{Key: OccurrenceChanged, Category: CatSafety, AllowPush: true, AllowEmail: true, Render: func(Vars) (string, string) {
		return "Your date invitation was updated", "Open Fold to review this date and your attendance."
	}, RenderEmailSafe: func(Vars) (string, string) { return "Review an update in Fold", "Sign in to Fold to review an update." }})
	register(Kind{Key: OccurrenceAdded, Category: CatSafety, AllowPush: true, AllowEmail: true, Render: func(Vars) (string, string) {
		return "You have a date invitation", "Open Fold to review the date and your attendance choices."
	}, RenderEmailSafe: func(Vars) (string, string) { return "Review an update in Fold", "Sign in to Fold to review an update." }})
	register(Kind{Key: OccurrenceUpdated, Category: CatSafety, AllowPush: true, AllowEmail: true, Render: func(Vars) (string, string) {
		return "Your date invitation has ended", "Open Fold to review your upcoming dates."
	}, RenderEmailSafe: func(Vars) (string, string) { return "Review an update in Fold", "Sign in to Fold to review an update." }})
	for _, item := range []struct {
		key     string
		waiting bool
	}{{ContactSubjectReview, true}, {ContactAdminReview, true}, {ContactUpdated, false}} {
		waiting := item.waiting
		register(Kind{Key: item.key, Category: CatSafety, AllowPush: true, Waiting: waiting, AllowEmail: true, Render: func(Vars) (string, string) {
			if waiting {
				return "Review a direct-contact request", "Open Fold to review the requested access. No contact details have been shared."
			}
			return "Direct-contact access updated", "Open Fold to review the request or grant."
		}, RenderEmailSafe: func(Vars) (string, string) { return "Review an update in Fold", "Sign in to Fold to review an update." }})
	}
	for _, item := range []struct {
		key     string
		waiting bool
	}{{AddSubjectReview, true}, {AddAdminReview, true}, {AddUpdated, false}} {
		waiting := item.waiting
		register(Kind{Key: item.key, Category: CatSafety, AllowPush: true, Waiting: waiting, AllowEmail: true, Render: func(Vars) (string, string) {
			if waiting {
				return "Review an ask to add", "Sign in to Fold to review a request to add someone. No contact access is included."
			}
			return "Ask to add updated", "Sign in to Fold to review the request. No contact access is included."
		}, RenderEmailSafe: func(Vars) (string, string) { return "Review an update in Fold", "Sign in to Fold to review an update." }})
	}
	register(Kind{Key: AccountTurned18, Category: CatSafety, AllowPush: true, AllowEmail: true,
		Render: func(Vars) (string, string) {
			return "Your account is now an adult account", "Teen consent is no longer required. Your consent history stays on file. An admin's suspension, if any, still applies."
		},
		RenderEmailSafe: func(Vars) (string, string) {
			return "Your Fold account was updated", "Sign in to Fold to review your account."
		},
	})
	register(Kind{Key: HouseholdChildTurned13, Category: CatSafety, AllowPush: true, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			return or(v.Person, "A child in your household") + " is now 13 or older", "Review their birth date and age group in My household. They can also request their own account. Their sign-ups stay with your household."
		},
		RenderEmailSafe: func(Vars) (string, string) {
			return "Review a household member's age in Fold", "Sign in to Fold to review your household."
		},
	})
	register(Kind{
		Key: TeenConsentChanged, Category: CatSafety, AllowPush: true, AllowEmail: true,
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
		// Admin tasks push no requester or group name; the bell has them.
		AllowPush: true, PushRender: func(Vars) (string, string) { return "A new group is waiting for approval", "" },
	})
	register(Kind{
		Key: GroupDeletion, Category: CatAdmin, Waiting: true, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			return or(v.Actor, "Someone") + " asked to delete " + or(v.Group, "a group"),
				"They are its only member. Delete the group or keep it."
		},
		AllowPush: true, PushRender: func(Vars) (string, string) { return "A request to delete a group is waiting", "" },
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
		// It goes to the other admins, who may see every group, so the
		// group is named; the person isn't, on a lock screen (DEC-134 A).
		AllowPush: true, PushRender: func(v Vars) (string, string) {
			if v.Change == "added" {
				return "New leader in " + or(v.Group, "a group"), "Open Fold to see who."
			}
			return "Leader change in " + or(v.Group, "a group"), "Open Fold to see who."
		},
	})
	register(Kind{
		Key: MembershipDeparture, Category: CatGroups, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			return "Your group membership ended", fmt.Sprintf("%d upcoming dates were kept and %d ended. Open Fold to review your upcoming dates.", v.KeptCount, v.EndedCount)
		},
		RenderEmailSafe: func(v Vars) (string, string) {
			return "Your group membership ended", fmt.Sprintf("%d upcoming dates were kept and %d ended. Sign in to Fold to review your upcoming dates.", v.KeptCount, v.EndedCount)
		},
	})
	// The person whose role changed (DEC-134): Change is "leader" (they now
	// lead) or "member" (they stepped back to member by someone else's
	// hand). Group only when they may see it.
	yourRole := func(v Vars) (string, string) {
		if v.Change == "leader" {
			return "You're now a leader of " + or(v.Group, "a group"), "You can manage its members and events in Fold."
		}
		return "You're no longer a leader of " + or(v.Group, "a group"), "Thanks for leading. You're still a member."
	}
	register(Kind{Key: YourRole, Category: CatGroups, AllowEmail: true, Render: yourRole, AllowPush: true, PushRender: yourRole})
	// A leader hears how an admin decided their new group (DEC-96).
	register(Kind{
		Key: GroupApproved, Category: CatGroups, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			return or(v.Group, "Your group") + " was approved", "It's live now. Schedule its first meeting."
		},
		// Only its own leaders get it, so the group may be named.
		AllowPush: true, PushRender: func(v Vars) (string, string) {
			return or(v.Group, "Your group") + " was approved", "It's live now."
		},
	})
	register(Kind{
		Key: GroupRejected, Category: CatGroups, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			body := "An admin decided not to add it. Ask an admin if you have questions."
			if v.Note != "" {
				body = "An admin decided not to add it. Their note: " + v.Note
			}
			return or(v.Group, "Your group") + " wasn't approved", body
		},
		// The admin's note stays in Fold.
		AllowPush: true, PushRender: func(v Vars) (string, string) {
			return or(v.Group, "Your group") + " wasn't approved", "Open Fold to see more."
		},
	})
	register(Kind{
		Key: MembershipLeft, Category: CatGroups, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			return or(v.Person, "Someone") + " left " + or(v.Group, "your group"), "Their group membership has ended."
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
		// Event pushes carry the title and time, never the place: a small
		// group often meets at someone's home.
		AllowPush: true, PushSoon: true, PushRender: func(v Vars) (string, string) {
			return "Cancelled: " + or(v.Event, "Church event"), v.When
		},
	})
	register(Kind{
		// A date a series edit had cancelled is on again because the rule
		// includes it. Same recipients and privacy rules as OccurrenceCancelled.
		Key: OccurrenceRestored, Category: CatEvents, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			return "Back on: " + or(v.Event, "Church event"),
				join(v.When, v.Place, "Your sign-up is still in place.")
		},
		// Whoever got the cancellation on their phone hears it's back on.
		AllowPush: true, PushSoon: true, PushRender: func(v Vars) (string, string) {
			return "Back on: " + or(v.Event, "Church event"), v.When
		},
	})
	register(Kind{
		Key: RegistrationCancelled, Category: CatEvents, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			return "Registration cancelled: " + or(v.Event, "An event you registered for"),
				"Who can see this event has changed, so registrations for dates you can no longer access were cancelled."
		},
		// The caller names no group, place or date: the person can no
		// longer see the event (DEC-134 A).
		AllowPush: true, PushRender: func(v Vars) (string, string) {
			return "Your spot was cancelled: " + or(v.Event, "an event"), "Open Fold to see why."
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
		// No name and no teen badge on a lock screen.
		AllowPush: true, PushRender: func(Vars) (string, string) { return "A new account request is waiting", "" },
	})
	register(Kind{
		Key: HouseholdConverted, Category: CatSafety, AllowPush: true, AllowEmail: true,
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
		// A new place is in Fold, not on the lock screen.
		AllowPush: true, PushSoon: true, PushRender: func(v Vars) (string, string) {
			return "Updated: " + or(v.Event, "an event"), "The " + or(v.Change, "details") + " changed. " + v.When
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
		// A row about someone in the household names nobody (research 2.4).
		AllowPush: true, PushRender: func(v Vars) (string, string) {
			if v.Person != "" {
				return safePush(v)
			}
			return "You're in: " + or(v.Event, "an event"), v.When
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
		// The leader's note stays in Fold.
		AllowPush: true, PushRender: func(v Vars) (string, string) {
			if v.Person != "" {
				return safePush(v)
			}
			return "Registration removed: " + or(v.Event, "an event"), "A leader or host removed your registration."
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
		AllowPush: true, PushRender: func(Vars) (string, string) { return "A household link is waiting to be confirmed", "" },
	})
	// A date moved and its room is booked at the new time (9.2): the
	// booking stays requested until someone picks another room.
	register(Kind{
		// An admin task while room bookings need approval (M2.1d, DEC-102):
		// one per booking action (DEC-106). Change is the room and the date
		// count in words; resolved when an admin decides or the dates go.
		Key: RoomRequested, Category: CatAdmin, Waiting: true, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			return or(v.Actor, "Someone") + " asked for a room for " + or(v.Event, "an event"),
				or(v.Change, "A room") + ". Needs an admin's approval."
		},
		AllowPush: true, PushRender: func(Vars) (string, string) { return "A room request is waiting for approval", "" },
	})
	register(Kind{
		// The people running the event hear an admin's decision. Change is
		// "approved" or "not approved"; Note is the outcome in words.
		Key: RoomRequestDecided, Category: CatEvents, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			return "Room " + or(v.Change, "decided") + ": " + or(v.Event, "an event"), or(v.Note, "See the event page.")
		},
	})
	register(Kind{
		Key: RoomLost, Category: CatEvents, AllowEmail: true,
		Render: func(v Vars) (string, string) {
			return "Room lost on " + or(v.Change, "a date") + ": " + or(v.Event, "an event"),
				"The room you booked is taken at the new time. Pick another room on the event page."
		},
		// No room name: a room can say where a group meets (DEC-124).
		// Change is the date count ("1 date", "3 dates").
		AllowPush: true, PushRender: func(v Vars) (string, string) {
			return "Room booking lost: " + or(v.Event, "an event"), "Pick another room for " + or(v.Change, "a date") + " in Fold."
		},
	})
	register(Kind{
		// Every device of the person was removed because their sessions
		// were revoked or the account stopped being active (DEC-134 C):
		// written by the push.devices_off job the users trigger queues.
		// Bell only: the devices are gone, and the change already emailed
		// what needed emailing. Never required.
		Key: PushDevicesOff, Category: CatAdmin,
		Render: func(Vars) (string, string) {
			return "Notifications on your devices were turned off", "Your password, email or account changed, so we turned off notifications on your phones and computers. Turn them on again in Me → Notifications."
		},
	})
	register(Kind{
		// The web push keys were made again because SESSION_SECRET changed
		// and the stored private key could no longer be read (M3.13a,
		// DEC-125): every device was removed.
		Key: PushKeysReset, Category: CatAdmin, AllowEmail: true,
		Render: func(Vars) (string, string) {
			return "Notifications on devices were reset", "Members need to turn them on again. This happens when the server's SESSION_SECRET changes. If the change was a mistake, put the old SESSION_SECRET back and restart so people stay signed in; members still need to turn notifications on again."
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
