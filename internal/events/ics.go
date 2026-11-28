package events

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Calendar files (RFC 5545), written fresh after Fold's internal/events/ics.go.
// Pure: the caller decides what the viewer may see and hands over only that;
// nothing here reads the database or the clock, so the same input writes the
// same bytes. Each date is its own VEVENT with no repeat rule, so a calendar
// app can never bring back a date the viewer may not see.

// ICSEvent is one date, already cleared for its viewer.
type ICSEvent struct {
	UID         string // stable across downloads: the date's own id
	Stamp       time.Time
	Title       string
	Description string // the event's about, only when the viewer may see the event
	Link        string // the date's page
	Video       string // only when policy.CanSeeVideo allows; "" writes nothing
	Location    string
	Zone        string // the event's IANA zone; one that cannot be loaded writes UTC
	Start, End  time.Time
	Status      string    // CONFIRMED, TENTATIVE, CANCELLED or ""
	Sequence    int       // goes up with every change, so apps replace their copy
	Modified    time.Time // LAST-MODIFIED; zero writes none
}

// ICS writes a calendar of dates, with a VTIMEZONE for every zone they use.
// A subscribed calendar names itself and says how often to check again.
func ICS(name string, refresh time.Duration, list []ICSEvent) []byte {
	lines := []string{"BEGIN:VCALENDAR", "PRODID:-//Fold//Events//EN", "VERSION:2.0", "CALSCALE:GREGORIAN", "METHOD:PUBLISH"}
	if name != "" {
		lines = append(lines, "X-WR-CALNAME:"+escapeText(name))
	}
	if refresh > 0 {
		d := fmt.Sprintf("PT%dM", int(refresh.Minutes()))
		lines = append(lines, "REFRESH-INTERVAL;VALUE=DURATION:"+d, "X-PUBLISHED-TTL:"+d)
	}
	zones := map[string]*time.Location{}
	spans := map[string][2]time.Time{}
	for _, e := range list {
		loc, err := LoadZone(e.Zone)
		if err != nil || loc.String() == "UTC" {
			continue
		}
		zones[e.Zone] = loc
		s, ok := spans[e.Zone]
		if !ok || e.Start.Before(s[0]) {
			s[0] = e.Start
		}
		if !ok || e.End.After(s[1]) {
			s[1] = e.End
		}
		spans[e.Zone] = s
	}
	names := make([]string, 0, len(zones))
	for n := range zones {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		lines = append(lines, timezone(n, zones[n], spans[n][0].AddDate(0, 0, -2), spans[n][1].AddDate(0, 0, 2))...)
	}
	for _, e := range list {
		lines = append(lines, "BEGIN:VEVENT", "UID:"+escapeText(e.UID), "DTSTAMP:"+e.Stamp.UTC().Format(icsUTC), fmt.Sprintf("SEQUENCE:%d", e.Sequence))
		if !e.Modified.IsZero() {
			lines = append(lines, "LAST-MODIFIED:"+e.Modified.UTC().Format(icsUTC))
		}
		if loc, ok := zones[e.Zone]; ok {
			lines = append(lines, "DTSTART;TZID="+e.Zone+":"+e.Start.In(loc).Format(icsWall), "DTEND;TZID="+e.Zone+":"+e.End.In(loc).Format(icsWall))
		} else {
			lines = append(lines, "DTSTART:"+e.Start.UTC().Format(icsUTC), "DTEND:"+e.End.UTC().Format(icsUTC))
		}
		if e.Status != "" {
			lines = append(lines, "STATUS:"+e.Status)
		}
		lines = append(lines, "SUMMARY:"+escapeText(e.Title))
		var desc []string
		if d := strings.TrimSpace(e.Description); d != "" {
			desc = append(desc, d)
		}
		if e.Link != "" {
			desc = append(desc, e.Link)
		}
		if e.Video != "" {
			desc = append(desc, "Join by video: "+e.Video)
		}
		if len(desc) > 0 {
			lines = append(lines, "DESCRIPTION:"+escapeText(strings.Join(desc, "\n\n")))
		}
		if e.Location != "" {
			lines = append(lines, "LOCATION:"+escapeText(e.Location))
		}
		if e.Link != "" {
			lines = append(lines, "URL:"+e.Link) // a URI, made by the server from its own address
		}
		lines = append(lines, "END:VEVENT")
	}
	lines = append(lines, "END:VCALENDAR")
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(foldLine(l))
		b.WriteString("\r\n")
	}
	return []byte(b.String())
}

const (
	icsWall = "20060102T150405"
	icsUTC  = "20060102T150405Z"
)

// timezone is a VTIMEZONE for loc from just before from to just after to:
// the offset in force at from, then each change, found by halving the gap
// between two readings that differ.
func timezone(name string, loc *time.Location, from, to time.Time) []string {
	type state struct {
		offset int
		abbrev string
		dst    bool
	}
	at := func(t time.Time) state {
		lt := t.In(loc)
		abbrev, offset := lt.Zone()
		return state{offset, abbrev, lt.IsDST()}
	}
	block := func(onset string, fromOffset int, s state) []string {
		kind := "STANDARD"
		if s.dst {
			kind = "DAYLIGHT"
		}
		return []string{"BEGIN:" + kind, "DTSTART:" + onset, "TZOFFSETFROM:" + offset(fromOffset), "TZOFFSETTO:" + offset(s.offset),
			"TZNAME:" + escapeText(s.abbrev), "END:" + kind}
	}
	prev := at(from)
	lines := append([]string{"BEGIN:VTIMEZONE", "TZID:" + name}, block("19700101T000000", prev.offset, prev)...)
	for t := from; t.Before(to); {
		next := t.Add(12 * time.Hour)
		if at(next) == prev {
			t = next
			continue
		}
		lo, hi := t, next
		for hi.Sub(lo) > time.Second {
			mid := lo.Add(hi.Sub(lo) / 2)
			if at(mid) == prev {
				lo = mid
			} else {
				hi = mid
			}
		}
		s := at(hi)
		// The onset is the wall clock just before the change.
		onset := hi.Add(time.Duration(prev.offset) * time.Second).UTC().Format(icsWall)
		lines = append(lines, block(onset, prev.offset, s)...)
		prev, t = s, hi
	}
	return append(lines, "END:VTIMEZONE")
}

func offset(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign, seconds = "-", -seconds
	}
	if s := seconds % 60; s != 0 {
		return fmt.Sprintf("%s%02d%02d%02d", sign, seconds/3600, seconds%3600/60, s)
	}
	return fmt.Sprintf("%s%02d%02d", sign, seconds/3600, seconds%3600/60)
}

var textEscaper = strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\r", `\n`, "\n", `\n`)

// escapeText escapes a TEXT value. Other control characters are dropped:
// they are not allowed in iCalendar text and can break a calendar app.
func escapeText(s string) string {
	return textEscaper.Replace(strings.Map(func(r rune) rune {
		if (r < 0x20 && r != '\t' && r != '\r' && r != '\n') || r == 0x7f {
			return -1
		}
		return r
	}, s))
}

// foldLine folds a line at 75 octets without splitting a character.
func foldLine(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		size := len(string(r))
		if n+size > 75 {
			b.WriteString("\r\n ")
			n = 1
		}
		b.WriteRune(r)
		n += size
	}
	return b.String()
}
