package push

import (
	"strings"
	"time"
)

// Quiet hours for push (DEC-126, research 2.5). This is M3.13's interim
// copy of the send-window function: M3.8 builds the shared one for SMS
// (DEC-9, DEC-25, with the admin's sms_send_window_start and _end
// settings) and replaces this file with it. Until then the window is the
// default, 8 AM to 8 PM in the recipient's local time, with no setting.

// Window is a daily send window in the recipient's local time: a send at
// StartHour:00 is inside, one at EndHour:00 is not. EndHour 24 runs to
// midnight. A window that isn't 0 <= StartHour < EndHour <= 24 is always
// open, so a bad value can never hold a push forever.
type Window struct {
	StartHour, EndHour int
}

// DefaultWindow is 8 AM to 8 PM, the SMS default (DEC-9, DEC-25).
var DefaultWindow = Window{StartHour: 8, EndHour: 20}

// AlwaysOpen never waits.
var AlwaysOpen = Window{StartHour: 0, EndHour: 24}

func (w Window) valid() bool {
	return 0 <= w.StartHour && w.StartHour < w.EndHour && w.EndHour <= 24
}

// Zone is the recipient's zone: tzid when it names one (User.Timezone),
// else fallback (the church's zone, as emails use, DEC-58), else UTC.
func Zone(tzid string, fallback *time.Location) *time.Location {
	tzid = strings.TrimSpace(tzid)
	if tzid != "" && tzid != "Local" && len(tzid) <= 64 {
		if loc, err := time.LoadLocation(tzid); err == nil {
			return loc
		}
	}
	if fallback != nil {
		return fallback
	}
	return time.UTC
}

// Check reports whether t is inside w in the recipient's zone (tzid,
// falling back to fallback as Zone does) and, when it isn't, when the
// window next opens: later that day if t is before the start, else the
// next day's start. On a day whose start hour doesn't exist (a
// spring-forward gap) it opens at the first moment after the gap.
func (w Window) Check(t time.Time, tzid string, fallback *time.Location) (inside bool, opens time.Time) {
	if !w.valid() {
		return true, time.Time{}
	}
	loc := Zone(tzid, fallback)
	local := t.In(loc)
	mins := local.Hour()*60 + local.Minute()
	if mins >= w.StartHour*60 && mins < w.EndHour*60 {
		return true, time.Time{}
	}
	day := local.Day()
	if mins >= w.EndHour*60 {
		day++
	}
	opens = time.Date(local.Year(), local.Month(), day, w.StartHour, 0, 0, 0, loc)
	return false, afterGap(opens, w.StartHour)
}

// afterGap moves a wall time that doesn't exist (the clocks jumped past
// hour that day) to the moment of the jump, whichever side of the gap
// time.Date put it.
func afterGap(t time.Time, hour int) time.Time {
	if t.Hour() == hour {
		return t
	}
	start, end := t.ZoneBounds()
	switch {
	case !end.IsZero() && end.Sub(t) <= 3*time.Hour:
		return end
	case !start.IsZero() && t.Sub(start) <= 3*time.Hour:
		return start
	}
	return t
}

// LastAllowed is the reminder rule (PLAN 7.7, written once there): t
// itself when it is inside w, else the last allowed minute before it (one
// minute before the window closed, for example 7:59 PM the night before a
// 7:00 AM reminder) if that is no more than within earlier; ok is false
// when it would be, and the reminder is skipped. The 2-hour push reminder
// (M3.13d3) and, through M3.8, SMS reminders use it.
func (w Window) LastAllowed(t time.Time, tzid string, fallback *time.Location, within time.Duration) (at time.Time, ok bool) {
	if inside, _ := w.Check(t, tzid, fallback); inside {
		return t, true
	}
	loc := Zone(tzid, fallback)
	local := t.In(loc)
	day := local.Day()
	if local.Hour()*60+local.Minute() < w.StartHour*60 {
		day-- // before today's window: the last chance was last night
	}
	at = time.Date(local.Year(), local.Month(), day, w.EndHour, 0, 0, 0, loc).Add(-time.Minute)
	if t.Sub(at) > within {
		return time.Time{}, false
	}
	return at, true
}
