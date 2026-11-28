// Command demo fills a new data folder with a made-up church, for trying Fold
// on a developer's computer. The Dockerfile builds only ./cmd/fold, so this
// never ships in a church's image.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math/rand/v2"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"fold/internal/accounts"
	"fold/internal/clock"
	"fold/internal/events"
	"fold/internal/groups"
	"fold/internal/mail"
	"fold/internal/people"
	"fold/internal/policy"
	"fold/internal/server"
	"fold/internal/write"
)

func main() {
	dir := flag.String("data", "data-demo", "the data folder to create")
	flag.Parse()
	if err := seed(context.Background(), *dir, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}

// seed builds the demo church in dir through the write executor and the real
// commands, so the audit chain is real. It never touches an existing database.
func seed(ctx context.Context, dir string, out io.Writer) (err error) {
	path := server.DBPath(dir)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already has a database; delete the folder %s to start over", dir, dir)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// The same keys fold serve will use, so a job secret left queued opens.
	env, err := server.ReadEnv(func(k string) string {
		switch k {
		case "DEV":
			return "1"
		case "DATA_DIR":
			return dir
		}
		return os.Getenv(k)
	})
	if err != nil {
		return err
	}
	// Checked first, so every database file left after a failure is this
	// run's own and a running server's never is.
	l, err := server.Lock(dir)
	if err != nil {
		return err
	}
	l.Close()
	removeDB := func() { // a half-built site would be served as if whole
		for _, suffix := range []string{"", "-wal", "-shm"} {
			os.Remove(path + suffix)
		}
	}
	o, err := server.Open(ctx, dir, env.Keys, clock.Real{})
	if err != nil {
		removeDB()
		return err
	}
	defer func() {
		o.Close()
		if err != nil {
			removeDB()
		}
	}()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	s := &seeder{ctx: ctx, o: o, runner: write.NewRunner(o.Exec, log), outbox: &mail.Outbox{}, who: map[string]write.ActorRef{}}
	server.RegisterJobs(s.runner, server.JobDeps{Exec: o.Exec, Reader: o.Reader, Keys: env.Keys, SiteURL: env.SiteURL.String(),
		Outbox: s.outbox, Dev: true, Clock: clock.Real{}, DataDir: dir, Log: log})
	s.build()
	if s.err != nil {
		return s.err
	}
	site := env.SiteURL.String()
	fmt.Fprintf(out, `%s is ready at %s

Sign in with one of these addresses. The sign-in link appears at %s/dev/outbox.
  An admin:                  %s
  A leader:                  %s (leads %s)
  A member:                  %s
  Has an invitation waiting: %s
  Invited by email:          %s (no account yet; the invitation is in the outbox and works for 24 hours)
`, church, site, site, adminEmail, email(demoLeader), groupList[0].name, email(demoMember), email(demoInvitee), email(emailInvitee))
	return nil
}

type seeder struct {
	ctx    context.Context
	o      *server.Opened
	runner *write.Runner
	outbox *mail.Outbox
	who    map[string]write.ActorRef // signed-in people, by email
	err    error                     // the first failure; every later step is skipped
}

func (s *seeder) build() {
	in := accounts.SetupInput{ChurchName: church, Zone: churchZone, Name: "Alex Admin", Email: adminEmail,
		Mail: accounts.MailInput{Provider: "outbox", From: "office@example.org"}}
	// The seeder is the installer at the server, where the setup code comes from.
	s.run("", accounts.StartSetup{CodeVerified: true, In: in})
	fin := &accounts.FinishSetup{Token: s.token(adminEmail)}
	s.run("", fin)
	s.signedIn(adminEmail, fin.SessionToken)

	for _, name := range invitedPeople {
		e := email(name)
		s.run(adminEmail, accounts.InviteByEmail{Name: name, Email: e})
		r := &accounts.RedeemInvitation{Token: s.token(e), Adult: true}
		s.run("", r)
		s.signedIn(e, r.SessionToken)
	}
	if s.err == nil {
		s.err = s.o.Exec.Run(s.ctx, write.System(), makeAdmin{email(secondAdmin)})
	}
	for _, name := range requestedPeople {
		e := email(name)
		s.run("", accounts.RequestAccount{Name: name, Email: e})
		s.run(adminEmail, accounts.DecideAccount{PersonUID: s.person(e).UID, Approve: true})
		l := &accounts.UseSignInLink{Token: s.token(e)}
		s.run("", l)
		s.signedIn(e, l.SessionToken)
	}

	rng := rand.New(rand.NewPCG(2026, 11))
	var pool []string
	for _, name := range slices.Concat(invitedPeople, requestedPeople) {
		if !slices.Contains(loners, name) {
			pool = append(pool, name)
		}
	}
	uids := map[string]string{}
	for _, g := range groupList {
		leader := email(g.leader)
		c := &groups.Create{Name: g.name, About: g.about, Visibility: g.vis}
		s.run(leader, c)
		uids[g.name] = c.UID
		if g.status == policy.GroupPending {
			continue
		}
		s.run(adminEmail, groups.Decide{UID: c.UID, Approve: true})
		skip := slices.Concat([]string{g.leader, g.co}, g.invited, g.added)
		members := draw(rng, pool, g.size, skip)
		if g.co != "" {
			members = append(members, g.co)
		}
		for _, m := range members {
			s.join(c.UID, g.vis, leader, email(m))
		}
		if g.co != "" {
			s.run(leader, groups.SetRole{GroupUID: c.UID, PersonUID: s.person(email(g.co)).UID, Leader: true})
		}
		for _, name := range g.added {
			s.run(adminEmail, groups.Add{GroupUID: c.UID, PersonUID: s.person(email(name)).UID})
		}
		for _, name := range g.invited {
			s.run(leader, accounts.InviteByName{GroupUID: c.UID, PersonUID: s.person(email(name)).UID})
		}
		if g.status == policy.GroupArchived {
			s.run(leader, groups.SetStatus{UID: c.UID, To: policy.GroupArchived})
		}
	}

	zone, _ := events.LoadZone(churchZone)
	today := time.Now().In(zone)
	made := map[string]string{} // event uids by title
	pattern := func(e demoEvent) events.Pattern {
		day := firstDay(today, e)
		p := events.Pattern{Start: day.Format(events.DayLayout) + "T" + e.at, Minutes: e.minutes, Repeat: e.repeat, MonthWeek: e.monthWeek}
		if e.untilWeeks > 0 {
			p.Until = day.AddDate(0, 0, 7*e.untilWeeks).Format(events.DayLayout)
		}
		return p
	}
	for _, e := range eventList {
		g := groupNamed(e.group)
		c := &events.Create{GroupUID: uids[g.name], Input: events.Input{Title: e.title, Place: e.place, Visibility: g.vis, Pattern: pattern(e), VideoURL: e.video, Seats: e.seats}}
		s.run(email(g.leader), c)
		made[e.title] = c.UID
		// Members answer the first date: mostly going, some maybe, some
		// can't go; one answers every date still to come. The event with
		// seats fills, and two wait.
		first := s.nthDate(c.UID, 0)
		for i, m := range s.members(uids[g.name]) {
			a := &events.Answer{DateUID: first, Status: []string{"cant", "maybe", "going", "going"}[i%4], Scope: "date"}
			if i == 2 && e.repeat != events.Once {
				a.Scope = "all"
			}
			s.run(m, a)
		}
	}
	// One date cancelled by hand, and one date of an invite-only group's
	// event opened to the church (P7, DEC-28).
	s.run(email(groupList[0].leader), events.CancelDate{UID: s.nthDate(made[eventList[0].title], 2)})
	grief := groupNamed(openedDate.group)
	s.run(email(grief.leader), &events.OpenDate{UID: s.nthDate(made[openedDate.event], 1), Visibility: policy.Church, Reason: openedDate.reason})
	// A visitor RSVPs to the choir's public rehearsal as a guest (R35).
	rehearsal := s.nthDate(made["Christmas cantata rehearsal"], 0)
	s.run("", accounts.RequestGuest{Name: guestName, Email: email(guestName), DateUID: rehearsal})
	s.run("", &accounts.UseGuestLink{Token: s.token(email(guestName))})
	// A church-wide event with two hosts (DEC-7).
	wide := &events.Create{Input: events.Input{Title: churchEvent.title, Place: churchEvent.place, Visibility: policy.Church, Pattern: pattern(churchEvent)}}
	s.run(adminEmail, wide)
	for _, h := range churchHosts {
		s.run(adminEmail, events.AddHost{EventUID: wide.UID, PersonUID: s.person(email(h)).UID})
	}

	s.run(adminEmail, accounts.Disable{PersonUID: s.person(email(disabledPerson)).UID})
	for _, name := range waitingPeople {
		s.run("", accounts.RequestAccount{Name: name, Email: email(name)})
	}
	s.drain()
	var unfinished int
	s.read(func(q *sql.Tx) error {
		return q.QueryRowContext(s.ctx, `SELECT count(*) FROM jobs WHERE state IN ('pending', 'failed')`).Scan(&unfinished)
	})
	if s.err == nil && unfinished > 0 {
		s.err = fmt.Errorf("%d emails could not be sent while seeding", unfinished)
	}
	// Left queued on purpose: fold serve sends it to /dev/outbox when it starts.
	s.run(email(groupList[1].leader), accounts.InviteByEmail{GroupUID: uids[groupList[1].name], Name: emailInvitee, Email: email(emailInvitee)})
}

func (s *seeder) run(by string, c write.Command) {
	if s.err != nil {
		return
	}
	as := write.Anonymous()
	if by != "" {
		as = s.who[by]
	}
	if err := s.o.Exec.Run(s.ctx, as, c); err != nil {
		s.err = fmt.Errorf("%s by %q: %w", c.Action(), by, err)
	}
}

func (s *seeder) read(f func(q *sql.Tx) error) {
	if s.err == nil {
		s.err = s.o.Reader.Read(s.ctx, f)
	}
}

func (s *seeder) drain() {
	if s.err == nil {
		_, s.err = s.runner.RunDue(s.ctx)
	}
}

var linkPattern = regexp.MustCompile(`/(?:l|invite)/([A-Za-z0-9_-]{43})`)

// token sends the queued mail and reads the link in the newest email to addr.
func (s *seeder) token(addr string) string {
	s.drain()
	if s.err != nil {
		return ""
	}
	m, ok := s.outbox.LatestTo(addr)
	l := linkPattern.FindStringSubmatch(m.Text)
	if !ok || l == nil {
		s.err = fmt.Errorf("no link was emailed to %s", addr)
		return ""
	}
	return l[1]
}

func (s *seeder) signedIn(addr, session string) { s.who[addr] = write.Session(accounts.Hash(session)) }

func (s *seeder) person(addr string) (p people.Person) {
	s.read(func(q *sql.Tx) (err error) {
		p, err = people.ByEmail(s.ctx, q, addr)
		return err
	})
	return p
}

// join puts member into a group the way a person would: joining an open
// group, or accepting the leader's invitation to an invite-only one.
func (s *seeder) join(groupUID, vis, leader, member string) {
	if vis != policy.InviteOnly {
		s.run(member, groups.Join{UID: groupUID})
		return
	}
	p := s.person(member)
	s.run(leader, accounts.InviteByName{GroupUID: groupUID, PersonUID: p.UID})
	var uid string
	s.read(func(q *sql.Tx) error {
		g, err := groups.ByUID(s.ctx, q, groupUID)
		if err == nil {
			uid, _, err = accounts.OpenInvitationFor(s.ctx, q, g.ID, p.ID, time.Now())
		}
		return err
	})
	s.run(member, &accounts.AnswerInvitation{UID: uid, Accept: true})
}

// draw picks n names from pool, none of them in skip.
func draw(rng *rand.Rand, pool []string, n int, skip []string) []string {
	var out []string
	for _, i := range rng.Perm(len(pool)) {
		if len(out) < n && !slices.Contains(skip, pool[i]) {
			out = append(out, pool[i])
		}
	}
	return out
}

// email makes a reserved example.org address from a name.
func email(name string) string {
	local := strings.Map(func(r rune) rune {
		switch {
		case r == ' ':
			return '.'
		case r >= 'a' && r <= 'z':
			return r
		}
		return -1
	}, strings.ToLower(name))
	return local + "@example.org"
}

// makeAdmin makes a second admin. Fold has no command for that yet, so the
// demo brings its own, through the same door and into the audit log.
type makeAdmin struct{ email string }

func (makeAdmin) Action() string { return "demo.make_admin" }

func (c makeAdmin) Apply(tx *write.Tx) error {
	p, err := people.ByEmail(tx.Context(), tx, c.email)
	if err != nil {
		return err
	}
	tx.Subject(p.UID)
	_, err = tx.Exec(`UPDATE people SET is_admin = 1, updated_at = ?, version = version + 1 WHERE id = ?`, tx.Now.Unix(), p.ID)
	return err
}

func groupNamed(name string) group {
	for _, g := range groupList {
		if g.name == name {
			return g
		}
	}
	panic("demo: no group " + name)
}

// firstDay is the first day after today on the event's weekday: for a
// monthly event, in its week of the month, so the pattern starts on a date
// it makes; for a one-time event, in next month.
func firstDay(today time.Time, e demoEvent) time.Time {
	d := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	for d.Weekday() != e.weekday || (e.monthWeek != 0 && !slices.Contains(events.WeeksOf(d), e.monthWeek)) || (e.repeat == events.Once && d.Month() == today.Month()) {
		d = d.AddDate(0, 0, 1)
	}
	return d
}

// nthDate is the uid of an event's nth date from the soonest, counting from 0.
func (s *seeder) nthDate(eventUID string, n int) (uid string) {
	s.read(func(q *sql.Tx) error {
		return q.QueryRowContext(s.ctx, `SELECT d.uid FROM dates d JOIN events e ON e.id = d.event_id
			WHERE e.uid = ? ORDER BY d.starts_at LIMIT 1 OFFSET ?`, eventUID, n).Scan(&uid)
	})
	return uid
}

// members are the addresses of a group's active members who are not leaders.
func (s *seeder) members(groupUID string) (out []string) {
	s.read(func(q *sql.Tx) error {
		rows, err := q.QueryContext(s.ctx, `SELECT p.email FROM active_memberships am JOIN people p ON p.id = am.person_id
			JOIN groups g ON g.id = am.group_id WHERE g.uid = ? AND am.role = 'member' ORDER BY p.email`, groupUID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e string
			rows.Scan(&e)
			out = append(out, e)
		}
		return rows.Err()
	})
	return out
}
