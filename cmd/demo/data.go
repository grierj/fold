package main

import (
	"time"

	"fold/internal/events"
	"fold/internal/policy"
)

// Everyone here is made up, and every address is at example.org, a domain
// reserved so mail to it reaches no one.

const (
	church     = "Grace Community Church"
	churchZone = "America/Chicago"
	adminEmail = "alex.admin@example.org"

	secondAdmin    = "Ruth Okafor"
	demoLeader     = "Marcus Bell"
	demoMember     = "Hannah Price"
	demoInvitee    = "Priya Shah"
	disabledPerson = "Gary Lund"
	emailInvitee   = "Jordan Reyes"
)

// invitedPeople were invited by the admin and accepted.
var invitedPeople = []string{
	"Ruth Okafor", "Marcus Bell", "Danielle Ortiz", "Megan Lindqvist", "Harold Jensen", "Evelyn Tanaka",
	"Samuel Adeyemi", "Linda Kowalski", "Tom Whitaker", "Sarah Whitaker", "Dorothy Hayes", "Walter Brooks",
	"Nate Fischer", "Rosa Delgado", "Kevin Nguyen", "Chris Morgan", "Joyce Palmer", "Hannah Price",
	"Priya Shah", "Daniel Kim", "Olivia Grant", "Isaiah Thompson", "Maria Santos", "James O'Connor",
	"Aisha Rahman", "Luis Herrera", "Emily Carter", "Noah Patel", "Grace Lee", "Michael Brennan",
	"Fatima Diallo", "Ethan Russo", "Chloe Martin", "Peter Novak", "Abigail Moore", "Victor Mendes",
	"Naomi Feld", "Caleb Wright", "Gary Lund",
}

// requestedPeople asked for an account and an admin approved them.
var requestedPeople = []string{"Lucy Chen", "Andre Johnson", "Bethany Clark", "Oscar Lindgren"}

// waitingPeople asked for an account and wait for an admin.
var waitingPeople = []string{"Wendy Ashford", "Raj Mehta", "Tess Quinlan"}

// loners are in no group.
var loners = []string{"Naomi Feld", "Caleb Wright", "Oscar Lindgren"}

// group is one group: its leader starts it, an admin approves it unless it
// waits, size people drawn at random join it, and the named ones are added
// by the admin, made co-leader, or invited and have not answered.
type group struct {
	name, about, vis, status string
	leader, co               string
	size                     int
	added, invited           []string
}

var groupList = []group{
	{name: "Tuesday Men's Bible Study", vis: policy.Public, status: policy.GroupActive, leader: "Marcus Bell", size: 8,
		invited: []string{"Isaiah Thompson"},
		about:   "Men of every age reading through a book of the Bible together, Tuesdays at 6:30 am in the fellowship hall. Coffee is on by 6:15."},
	{name: "Young Adults", vis: policy.Public, status: policy.GroupActive, leader: "Danielle Ortiz", size: 11,
		about: "For people in their twenties and early thirties. We share a meal, talk about faith and life, and go out together once a month."},
	{name: "Moms of Littles", vis: policy.Church, status: policy.GroupActive, leader: "Megan Lindqvist", size: 7,
		about: "Moms of babies through kindergartners. We meet Thursday mornings while the kids play in the nursery next door."},
	{name: "Grief Share", vis: policy.InviteOnly, status: policy.GroupActive, leader: "Harold Jensen", size: 4,
		about: "A quiet, caring group for anyone who has lost someone close. Talk to Harold if you would like to join."},
	{name: "Choir", vis: policy.Public, status: policy.GroupActive, leader: "Evelyn Tanaka", size: 13,
		invited: []string{"Priya Shah"},
		about:   "We sing at the 10:30 service and rehearse Wednesdays at 7 pm. No audition; if you love to sing, come and try."},
	{name: "Youth Volunteers", vis: policy.InviteOnly, status: policy.GroupActive, leader: "Samuel Adeyemi", size: 5,
		about: "Adults who help with the youth group on Sunday evenings. Everyone here has completed the church's safety training."},
	{name: "Food Pantry Team", vis: policy.Church, status: policy.GroupActive, leader: "Linda Kowalski", size: 9,
		added: []string{"Hannah Price"},
		about: "We sort donations and hand out groceries every Saturday from 9 to noon. Come for one shift or every week."},
	{name: "Couples Small Group", vis: policy.Church, status: policy.GroupActive, leader: "Tom Whitaker", co: "Sarah Whitaker", size: 6,
		about: "Married and engaged couples meeting in the Whitakers' home every other Friday. Childcare is shared between families."},
	{name: "Senior Saints Lunch", vis: policy.Church, status: policy.GroupActive, leader: "Dorothy Hayes", co: "Walter Brooks", size: 8,
		about: "Lunch and good company on the first Wednesday of the month. Rides are available; just ask."},
	{name: "Newcomers", vis: policy.Public, status: policy.GroupActive, leader: "Nate Fischer", size: 4,
		about: "New to Grace? Join us after the second service for coffee, a short tour, and a chance to meet a few friendly faces."},
	{name: "Prayer Team", vis: policy.InviteOnly, status: policy.GroupActive, leader: "Rosa Delgado", size: 5,
		invited: []string{"Daniel Kim"},
		about:   "We pray for the requests the church receives, and keep them confidential."},
	{name: "Running Club", vis: policy.Public, status: policy.GroupArchived, leader: "Kevin Nguyen", size: 4,
		about: "Saturday morning 5Ks around Riverside Park. Resting for the winter."},
	{name: "Board Game Night", vis: policy.Church, status: policy.GroupPending, leader: "Chris Morgan",
		about: "Bring a game or learn a new one. Second Friday of the month in the youth room."},
	{name: "Saturday Morning Hikers", vis: policy.Public, status: policy.GroupPending, leader: "Joyce Palmer",
		about: "Easy to moderate trail hikes near the city, about two hours, with a picnic afterward."},
}

// demoEvent is one event its group's leader schedules, starting on the first
// day after today that is weekday (and, for a monthly one, that week of its
// month); untilWeeks ends it that many weeks on.
type demoEvent struct {
	group, title, place, at string
	weekday                 time.Weekday
	repeat                  string
	monthWeek, minutes      int
	untilWeeks              int
}

var eventList = []demoEvent{
	{group: "Tuesday Men's Bible Study", title: "Men's Bible Study", place: "Fellowship hall", at: "06:30", weekday: time.Tuesday, repeat: events.Weekly, minutes: 75},
	{group: "Couples Small Group", title: "Couples night", place: "The Whitakers' home, 412 Alder Lane", at: "19:00", weekday: time.Friday, repeat: events.Biweekly, minutes: 120},
	{group: "Food Pantry Team", title: "Pantry distribution", place: "Church parking lot, north entrance", at: "09:00", weekday: time.Saturday, repeat: events.Monthly, monthWeek: 2, minutes: 180},
	{group: "Young Adults", title: "Game night and chili supper", place: "Youth room", at: "18:30", weekday: time.Friday, repeat: events.Once, minutes: 150},
	{group: "Choir", title: "Christmas cantata rehearsal", place: "Choir room", at: "19:00", weekday: time.Wednesday, repeat: events.Weekly, minutes: 90, untilWeeks: 6},
	{group: "Senior Saints Lunch", title: "Senior Saints lunch", place: "Fellowship hall", at: "12:00", weekday: time.Wednesday, repeat: events.Monthly, monthWeek: 1, minutes: 90},
	{group: "Grief Share", title: "Grief Share evening", place: "Church library", at: "19:00", weekday: time.Monday, repeat: events.Weekly, minutes: 90},
}

// openedDate is one date of an invite-only group's event opened to the
// whole church.
var openedDate = struct{ group, event, reason string }{"Grief Share", "Grief Share evening", "An open evening for anyone carrying a loss this season."}

// churchEvent is a church-wide event, and its hosts.
var (
	churchEvent = demoEvent{title: "Wednesday family supper", place: "Fellowship hall", at: "17:30", weekday: time.Wednesday, repeat: events.Weekly, minutes: 90}
	churchHosts = []string{"Walter Brooks", "Joyce Palmer"}
)
