package events

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ICS feed rendering (M1.7, 8.2). Pure: the handler loads facts and
// resolves permissions and effective titles and locations, and hands
// the result over as a Feed. Nothing here touches the database or HTTP,
// and nothing reads the clock, so the same Feed always renders the same
// bytes (that is what makes the ETag stable).

// FeedSingle is one authorized date with all effective fields already projected.
// Its UID carries the immutable original date, so reschedules keep its identity.
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
	for i := range singles {
		b, zone, ok := renderSingle(&singles[i], host)
		if !ok {
			continue
		}
		zones[singles[i].TZID] = zone
		blocks = append(blocks, b)
		addRef(singles[i].StartsAt)
		addRef(singles[i].EndsAt)
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

// A date UID uses its immutable original date, even after a reschedule.
func singleUID(id uint, originalDate, host string) string {
	return fmt.Sprintf("event-%d-%s@%s", id, strings.ReplaceAll(originalDate, "-", ""), host)
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
	}
	b = append(b,
		"DTSTART;TZID="+s.TZID+":"+s.StartsAt.In(loc).Format(icsWall),
		"DTEND;TZID="+s.TZID+":"+end.In(loc).Format(icsWall),
	)
	if s.Status != "" {
		b = append(b, "STATUS:"+s.Status)
	}
	b = append(b, textLines(s.Title, s.Description, s.URL, s.Location, s.VideoURL, s.RemoteAvailable, s.ChildcareAvailable, s.ChildcareAges)...)
	return append(b, "END:VEVENT"), loc, true
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

func dtstamp(updated, fallback time.Time) string {
	t := updated
	if t.IsZero() {
		t = fallback
	}
	return t.UTC().Format(icsWall) + "Z"
}

// timezoneBlocks builds one VTIMEZONE per zone, in zone-name order,
// covering the export window and emitted date clocks with a small margin.
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
	to = to.AddDate(0, 0, 2)

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
