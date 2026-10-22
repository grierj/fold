package events

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/teambition/rrule-go"
)

// ICS feed rendering (M1.7, 8.2). Pure: the handler loads facts and
// resolves permissions and effective titles and locations, and hands
// the result over as a Feed. Nothing here touches the database or HTTP,
// and nothing reads the clock, so the same Feed always renders the same
// bytes (that is what makes the ETag stable).

// FeedOccurrence is one date of an event with its effective (per-date
// override applied) title, location and clock.
type FeedOccurrence struct {
	OriginalDate string // YYYY-MM-DD, the series date in the event's zone
	StartsAt     time.Time
	EndsAt       time.Time
	Title        string
	Location     string
	UpdatedAt    time.Time
}

// FeedEvent is one series or one-time event for the feed. Occurrences
// holds the in-window scheduled dates; a series normally renders as one
// RRULE entry and uses it only when its stored rule cannot be emitted
// safely. Overrides holds the in-window customized, non-cancelled dates
// and CancelledDates the in-window cancelled series dates.
type FeedEvent struct {
	ID          uint
	Title       string
	Description string
	// VideoURL is the event's video link, already cleared for this feed's
	// viewer by the handler (5.8, M1.8); "" renders nothing.
	VideoURL string
	// RemoteAvailable adds "Remote attendance available" to the
	// description (8.2, M1.8 slice 2).
	ChildcareAvailable bool
	ChildcareAges      string
	RemoteAvailable    bool
	Location           string
	TZID               string
	StartsLocal        string // YYYY-MM-DDTHH:MM, the series wall clock
	DurationMinutes    int
	RRule              string
	RRuleUntil         string // YYYY-MM-DD inclusive, or ""
	URL                string
	UpdatedAt          time.Time
	CancelledDates     []string
	Overrides          []FeedOccurrence
	Occurrences        []FeedOccurrence
}

// FeedSingle is one standalone date with no series entry in this feed:
// a personal feed's "I'm going to this one date of someone else's
// series". Its UID carries the date for that reason (see singleUID).
type FeedSingle struct {
	EventID      uint
	OriginalDate string
	Title        string
	Description  string
	// VideoURL is the event's video link, already cleared for this date's
	// viewer by the handler (5.8, M1.8); "" renders nothing.
	VideoURL string
	// RemoteAvailable adds "Remote attendance available" to the
	// description (8.2, M1.8 slice 2).
	ChildcareAvailable bool
	ChildcareAges      string
	RemoteAvailable    bool
	Location           string
	TZID               string
	StartsAt           time.Time
	EndsAt             time.Time
	URL                string
	UpdatedAt          time.Time
	Status             string // CONFIRMED or TENTATIVE, or "" to omit
}

type Feed struct {
	Name        string
	Host        string // UID host; "fold" when empty
	WindowStart time.Time
	WindowEnd   time.Time
	Events      []FeedEvent
	Singles     []FeedSingle
}

const feedProdID = "-//Fold//Events//EN"

const icsWall = "20060102T150405"

// RenderFeed renders the whole calendar. An entry it cannot render (a
// zone or wall clock it cannot parse) is skipped, so one bad row never
// takes down the feed other people's calendar apps are polling.
func RenderFeed(f Feed) []byte {
	host := f.Host
	if host == "" {
		host = "fold"
	}
	evs := append([]FeedEvent(nil), f.Events...)
	sort.Slice(evs, func(i, j int) bool { return evs[i].ID < evs[j].ID })
	singles := append([]FeedSingle(nil), f.Singles...)
	sort.Slice(singles, func(i, j int) bool {
		if singles[i].EventID != singles[j].EventID {
			return singles[i].EventID < singles[j].EventID
		}
		return singles[i].OriginalDate < singles[j].OriginalDate
	})

	var blocks [][]string
	zones := map[string]*time.Location{}
	var refs []time.Time
	addRef := func(t time.Time) {
		if !t.IsZero() {
			refs = append(refs, t.UTC())
		}
	}
	for i := range evs {
		bs, zone, start, ok := renderEvent(&evs[i], host)
		if !ok {
			continue
		}
		zones[evs[i].TZID] = zone
		blocks = append(blocks, bs...)
		addRef(start)
		for _, o := range evs[i].Overrides {
			addRef(o.StartsAt)
		}
		for _, o := range evs[i].Occurrences {
			addRef(o.StartsAt)
		}
	}
	for i := range singles {
		b, zone, ok := renderSingle(&singles[i], host)
		if !ok {
			continue
		}
		zones[singles[i].TZID] = zone
		blocks = append(blocks, b)
		addRef(singles[i].StartsAt)
	}

	lines := []string{
		"BEGIN:VCALENDAR",
		"PRODID:" + feedProdID,
		"VERSION:2.0",
		"CALSCALE:GREGORIAN",
		"METHOD:PUBLISH",
	}
	if f.Name != "" {
		lines = append(lines, "X-WR-CALNAME:"+escapeText(f.Name))
	}
	for _, b := range timezoneBlocks(zones, f, refs) {
		lines = append(lines, b...)
	}
	for _, b := range blocks {
		lines = append(lines, b...)
	}
	lines = append(lines, "END:VCALENDAR")

	var out strings.Builder
	for _, l := range lines {
		out.WriteString(foldLine(l))
		out.WriteString("\r\n")
	}
	return []byte(out.String())
}

func seriesUID(id uint, host string) string {
	return fmt.Sprintf("event-%d@%s", id, host)
}

// A standalone date has no series entry in the feed to share a UID with,
// so the date is part of its UID. That is the one deliberate exception
// to the event-<id>@<host> form.
func singleUID(id uint, originalDate, host string) string {
	return fmt.Sprintf("event-%d-%s@%s", id, strings.ReplaceAll(originalDate, "-", ""), host)
}

func renderEvent(ev *FeedEvent, host string) ([][]string, *time.Location, time.Time, bool) {
	loc, err := LoadZone(ev.TZID)
	if err != nil {
		return nil, nil, time.Time{}, false
	}
	start, _, err := ParseWall(ev.StartsLocal, ev.TZID)
	if err != nil {
		return nil, nil, time.Time{}, false
	}
	if ev.RRule == "" {
		b, ok := renderOneTime(ev, loc, start, host)
		if !ok {
			return nil, nil, time.Time{}, false
		}
		return [][]string{b}, loc, start, true
	}
	rule, safe := safeRRule(ev.RRule, loc, start)
	if !safe {
		occs := sortedOccurrences(ev.Occurrences)
		if len(occs) == 0 {
			return nil, nil, time.Time{}, false
		}
		out := make([][]string, 0, len(occs))
		for i := range occs {
			out = append(out, singleBlock(singleUID(ev.ID, occs[i].OriginalDate, host), ev, &occs[i], loc, ""))
		}
		return out, loc, start, true
	}
	if until := untilUTC(ev.RRuleUntil, loc); until != "" &&
		!strings.Contains(strings.ToUpper(rule), "UNTIL=") &&
		!strings.Contains(strings.ToUpper(rule), "COUNT=") {
		rule += ";UNTIL=" + until
	}
	dur := time.Duration(ev.DurationMinutes) * time.Minute
	lines := []string{
		"BEGIN:VEVENT",
		"UID:" + seriesUID(ev.ID, host),
		"DTSTAMP:" + dtstamp(ev.UpdatedAt, start),
		"DTSTART;TZID=" + ev.TZID + ":" + start.Format(icsWall),
		"DTEND;TZID=" + ev.TZID + ":" + start.Add(dur).Format(icsWall),
		"RRULE:" + rule,
	}
	if dates := sortedDates(ev.CancelledDates); len(dates) > 0 {
		walls := make([]string, len(dates))
		for i, d := range dates {
			walls[i] = originalWall(d, start)
		}
		lines = append(lines, "EXDATE;TZID="+ev.TZID+":"+strings.Join(walls, ","))
	}
	lines = append(lines, textLines(ev.Title, ev.Description, ev.URL, ev.Location, ev.VideoURL, ev.RemoteAvailable, ev.ChildcareAvailable, ev.ChildcareAges)...)
	lines = append(lines, "END:VEVENT")

	out := [][]string{lines}
	for _, o := range sortedOccurrences(ev.Overrides) {
		b := []string{
			"BEGIN:VEVENT",
			"UID:" + seriesUID(ev.ID, host),
			"DTSTAMP:" + dtstamp(o.UpdatedAt, o.StartsAt),
			"RECURRENCE-ID;TZID=" + ev.TZID + ":" + originalWall(o.OriginalDate, start),
			"DTSTART;TZID=" + ev.TZID + ":" + o.StartsAt.In(loc).Format(icsWall),
			"DTEND;TZID=" + ev.TZID + ":" + endsAt(&o, ev.DurationMinutes).In(loc).Format(icsWall),
		}
		b = append(b, textLines(orSeries(o.Title, ev.Title), ev.Description, ev.URL,
			orSeries(o.Location, ev.Location), ev.VideoURL, ev.RemoteAvailable, ev.ChildcareAvailable, ev.ChildcareAges)...)
		b = append(b, "END:VEVENT")
		out = append(out, b)
	}
	return out, loc, start, true
}

func renderOneTime(ev *FeedEvent, loc *time.Location, start time.Time, host string) ([]string, bool) {
	if len(ev.Occurrences) == 0 {
		if len(ev.CancelledDates) > 0 {
			return nil, false
		}
		dur := time.Duration(ev.DurationMinutes) * time.Minute
		b := []string{
			"BEGIN:VEVENT",
			"UID:" + seriesUID(ev.ID, host),
			"DTSTAMP:" + dtstamp(ev.UpdatedAt, start),
			"DTSTART;TZID=" + ev.TZID + ":" + start.Format(icsWall),
			"DTEND;TZID=" + ev.TZID + ":" + start.Add(dur).Format(icsWall),
		}
		b = append(b, textLines(ev.Title, ev.Description, ev.URL, ev.Location, ev.VideoURL, ev.RemoteAvailable, ev.ChildcareAvailable, ev.ChildcareAges)...)
		return append(b, "END:VEVENT"), true
	}
	o := sortedOccurrences(ev.Occurrences)[0]
	return singleBlock(seriesUID(ev.ID, host), ev, &o, loc, ""), true
}

func renderSingle(s *FeedSingle, host string) ([]string, *time.Location, bool) {
	loc, err := LoadZone(s.TZID)
	if err != nil || s.StartsAt.IsZero() {
		return nil, nil, false
	}
	date := s.OriginalDate
	if date == "" {
		date = s.StartsAt.In(loc).Format("2006-01-02")
	}
	end := s.EndsAt
	if end.IsZero() {
		end = s.StartsAt.Add(time.Hour)
	}
	b := []string{
		"BEGIN:VEVENT",
		"UID:" + singleUID(s.EventID, date, host),
		"DTSTAMP:" + dtstamp(s.UpdatedAt, s.StartsAt),
		"DTSTART;TZID=" + s.TZID + ":" + s.StartsAt.In(loc).Format(icsWall),
		"DTEND;TZID=" + s.TZID + ":" + end.In(loc).Format(icsWall),
	}
	if s.Status != "" {
		b = append(b, "STATUS:"+s.Status)
	}
	b = append(b, textLines(s.Title, s.Description, s.URL, s.Location, s.VideoURL, s.RemoteAvailable, s.ChildcareAvailable, s.ChildcareAges)...)
	return append(b, "END:VEVENT"), loc, true
}

func singleBlock(uid string, ev *FeedEvent, o *FeedOccurrence, loc *time.Location, status string) []string {
	b := []string{
		"BEGIN:VEVENT",
		"UID:" + uid,
		"DTSTAMP:" + dtstamp(o.UpdatedAt, o.StartsAt),
		"DTSTART;TZID=" + ev.TZID + ":" + o.StartsAt.In(loc).Format(icsWall),
		"DTEND;TZID=" + ev.TZID + ":" + endsAt(o, ev.DurationMinutes).In(loc).Format(icsWall),
	}
	if status != "" {
		b = append(b, "STATUS:"+status)
	}
	b = append(b, textLines(orSeries(o.Title, ev.Title), ev.Description, ev.URL,
		orSeries(o.Location, ev.Location), ev.VideoURL, ev.RemoteAvailable, ev.ChildcareAvailable, ev.ChildcareAges)...)
	return append(b, "END:VEVENT")
}

func textLines(title, description, url, location, videoURL string, remoteAvailable, childcareAvailable bool, childcareAges string) []string {
	lines := []string{"SUMMARY:" + escapeText(title)}
	desc := strings.TrimRight(description, " \t\r\n")
	switch {
	case desc != "" && url != "":
		desc += "\n\n" + url
	case desc == "":
		desc = url
	}
	if remoteAvailable {
		if desc != "" {
			desc += "\n\n"
		}
		desc += "Remote attendance available"
	}
	if childcareAvailable {
		if desc != "" {
			desc += "\n\n"
		}
		desc += "Childcare available"
		if childcareAges != "" {
			desc += " (" + childcareAges + ")"
		}
	}
	if videoURL != "" {
		if desc != "" {
			desc += "\n\n"
		}
		desc += "Join by video: " + videoURL
	}
	if desc != "" {
		lines = append(lines, "DESCRIPTION:"+escapeText(desc))
	}
	if location != "" {
		lines = append(lines, "LOCATION:"+escapeText(location))
	}
	return lines
}

func orSeries(own, series string) string {
	if own != "" {
		return own
	}
	return series
}

func endsAt(o *FeedOccurrence, durationMinutes int) time.Time {
	if !o.EndsAt.IsZero() {
		return o.EndsAt
	}
	return o.StartsAt.Add(time.Duration(durationMinutes) * time.Minute)
}

func dtstamp(updated, fallback time.Time) string {
	t := updated
	if t.IsZero() {
		t = fallback
	}
	return t.UTC().Format(icsWall) + "Z"
}

// originalWall is the wall clock a series date would have started at
// without a per-date override: the original date at the series' time of
// day (start is the series start in the event's zone).
func originalWall(date string, start time.Time) string {
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return strings.ReplaceAll(date, "-", "") + start.Format("T150405")
	}
	t := time.Date(d.Year(), d.Month(), d.Day(),
		start.Hour(), start.Minute(), start.Second(), 0, time.UTC)
	return t.Format(icsWall)
}

// untilUTC converts an inclusive local end date to the UTC instant of
// the end of that day, the only form RFC 5545 allows for UNTIL when
// DTSTART carries a TZID.
func untilUTC(date string, loc *time.Location) string {
	if strings.TrimSpace(date) == "" {
		return ""
	}
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ""
	}
	end := time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 59, 0, loc)
	return end.UTC().Format(icsWall) + "Z"
}

func safeRRule(rule string, loc *time.Location, start time.Time) (string, bool) {
	r := strings.TrimSpace(rule)
	if len(r) >= len("RRULE:") && strings.EqualFold(r[:len("RRULE:")], "RRULE:") {
		r = strings.TrimSpace(r[len("RRULE:"):])
	}
	if r == "" || strings.Contains(strings.ToUpper(r), "DTSTART") {
		return "", false
	}
	for _, c := range r {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == ';' || c == '=' || c == ',' || c == '+' || c == '-':
		default:
			return "", false
		}
	}
	ics := fmt.Sprintf("DTSTART;TZID=%s:%s\nRRULE:%s", loc.String(), start.Format(icsWall), r)
	if _, err := rrule.StrToRRule(ics); err != nil {
		return "", false
	}
	return r, true
}

func sortedOccurrences(in []FeedOccurrence) []FeedOccurrence {
	out := append([]FeedOccurrence(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].OriginalDate != out[j].OriginalDate {
			return out[i].OriginalDate < out[j].OriginalDate
		}
		return out[i].StartsAt.Before(out[j].StartsAt)
	})
	return out
}

func sortedDates(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// timezoneBlocks builds one VTIMEZONE per zone, in zone-name order,
// from the actual offset transitions in Go's tzdata. Coverage runs from
// the earliest referenced instant to two years past the feed window, so
// a client expanding an RRULE past the window still gets the right
// offsets until its next refresh.
func timezoneBlocks(zones map[string]*time.Location, f Feed, refs []time.Time) [][]string {
	if len(zones) == 0 {
		return nil
	}
	from := f.WindowStart
	to := f.WindowEnd
	for _, t := range refs {
		if from.IsZero() || t.Before(from) {
			from = t
		}
		if to.IsZero() || t.After(to) {
			to = t
		}
	}
	if from.IsZero() {
		from = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if to.IsZero() || !to.After(from) {
		to = from.AddDate(1, 0, 0)
	}
	from = from.AddDate(0, 0, -2)
	to = to.AddDate(2, 0, 0)

	names := make([]string, 0, len(zones))
	for name := range zones {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([][]string, 0, len(names))
	for _, name := range names {
		lines := []string{"BEGIN:VTIMEZONE", "TZID:" + name}
		for _, ob := range observances(zones[name], from, to) {
			kind := "STANDARD"
			if ob.state.dst {
				kind = "DAYLIGHT"
			}
			lines = append(lines, "BEGIN:"+kind,
				"DTSTART:"+ob.onset,
				"TZOFFSETFROM:"+formatOffset(ob.from),
				"TZOFFSETTO:"+formatOffset(ob.state.offset))
			if ob.state.abbrev != "" {
				lines = append(lines, "TZNAME:"+escapeText(ob.state.abbrev))
			}
			lines = append(lines, "END:"+kind)
		}
		lines = append(lines, "END:VTIMEZONE")
		out = append(out, lines)
	}
	return out
}

type zoneState struct {
	offset int
	abbrev string
	dst    bool
}

type observance struct {
	onset string // wall clock in the offset in use before the change
	from  int
	state zoneState
}

func stateAt(t time.Time, loc *time.Location) zoneState {
	lt := t.In(loc)
	abbrev, offset := lt.Zone()
	return zoneState{offset: offset, abbrev: abbrev, dst: lt.IsDST()}
}

func observances(loc *time.Location, from, to time.Time) []observance {
	prev := stateAt(from, loc)
	out := []observance{{onset: "19700101T000000", from: prev.offset, state: prev}}
	prevT := from
	for prevT.Before(to) {
		curT := prevT.Add(12 * time.Hour)
		if curT.After(to) {
			curT = to
		}
		if stateAt(curT, loc) == prev {
			prevT = curT
			continue
		}
		lo, hi := prevT, curT
		for hi.Sub(lo) > time.Second {
			mid := lo.Add(hi.Sub(lo) / 2)
			if stateAt(mid, loc) == prev {
				lo = mid
			} else {
				hi = mid
			}
		}
		next := stateAt(hi, loc)
		wall := hi.Add(time.Duration(prev.offset) * time.Second).UTC().Format(icsWall)
		out = append(out, observance{onset: wall, from: prev.offset, state: next})
		prev, prevT = next, hi
	}
	return out
}

func formatOffset(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign = "-"
		seconds = -seconds
	}
	h, m, s := seconds/3600, (seconds%3600)/60, seconds%60
	if s != 0 {
		return fmt.Sprintf("%s%02d%02d%02d", sign, h, m, s)
	}
	return fmt.Sprintf("%s%02d%02d", sign, h, m)
}

var textEscaper = strings.NewReplacer(
	`\`, `\\`,
	";", `\;`,
	",", `\,`,
	"\r\n", `\n`,
	"\r", `\n`,
	"\n", `\n`,
)

func escapeText(s string) string { return textEscaper.Replace(s) }

// foldLine folds at 75 octets (RFC 5545) without splitting a rune.
func foldLine(s string) string {
	var b strings.Builder
	col := 0
	limit := 75
	for _, r := range s {
		n := len(string(r))
		if col+n > limit {
			b.WriteString("\r\n ")
			col = 1
			limit = 75
		}
		b.WriteRune(r)
		col += n
	}
	return b.String()
}
