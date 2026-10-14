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
)

// Kind keys. Add a kind here and in registry; the registry test renders
// every kind with teen and hidden-adult fixtures.
const (
	GroupPending          = "group.pending"
	GroupDeletion         = "group.deletion"
	LeadershipChanged     = "group.leadership_changed"
	MemberNotRestored     = "membership.not_restored"
	OccurrenceCancelled   = "event.cancelled"
	RegistrationCancelled = "registration.cancelled"
	VisibilityOverride    = "event.visibility_override"
)

// Action is an inline button. Only allow-listed kinds carry one (6.6).
type Action struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// ActionApproveGroup approves a pending group from the bell.
const ActionApproveGroup = "approve"

// Vars are the only things a kind can say. Callers fill in names and places
// the recipient may see; empty means "don't say".
type Vars struct {
	Actor      string // who did it
	Person     string // who it was done to
	Group      string // the group's name, only if the recipient can see it
	Event      string // the event's title (or a safe fallback)
	When       string // the date and time, already in the event's zone
	Place      string // only if the recipient can see it
	Change     string // a kind-specific variant
	Visibility string // new visibility, in words
	GroupVis   string // the group's visibility, in words
	Note       string // a short reason the actor gave
	Count      int    // set by Send: how many events this row covers
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
	// RenderEmailSafe is the link-only variant for sensitive kinds
	// (minors, consent, contact access): it names nobody and only links
	// into Fold. Nil means Render is already safe.
	RenderEmailSafe func(Vars) (title, body string)
}

// RenderForEmail renders k for email: the link-only safe variant when the
// kind has one, Render otherwise.
func (k Kind) RenderForEmail(v Vars) (title, body string) {
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
}
