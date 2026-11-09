package main

import (
	"strconv"
	"strings"
)

// reminderKey names one reminder: its kind (going, waitlist or push for a
// written message, "reminder.send" for the job that writes it), the date's
// occurrence, the start it was planned for, and how many minutes before the
// start it goes. It is the messages.reminder_key text and the reminder.send
// job key: "<kind>:<occurrence>:<start unix>:<minutes>". The start names the
// date as it was, so a moved date gets a new key.
type reminderKey struct {
	Kind    string
	Occ     uint
	Start   int64
	Minutes int
}

func (k reminderKey) String() string {
	return k.Kind + ":" + idString(k.Occ) + ":" + strconv.FormatInt(k.Start, 10) + ":" + strconv.Itoa(k.Minutes)
}

func parseReminderKey(s string) (reminderKey, bool) {
	parts := strings.Split(s, ":")
	if len(parts) != 4 {
		return reminderKey{}, false
	}
	occ, err1 := strconv.ParseUint(parts[1], 10, 64)
	start, err2 := strconv.ParseInt(parts[2], 10, 64)
	minutes, err3 := strconv.Atoi(parts[3])
	if err1 != nil || err2 != nil || err3 != nil {
		return reminderKey{}, false
	}
	return reminderKey{Kind: parts[0], Occ: uint(occ), Start: start, Minutes: minutes}, true
}

func reminderKeyLike(kind string) string { return kind + ":%" }

// reminderKeySQL is the SQL that builds the key of kind and minutes for the
// event occurrence row aliased occ, as String does. Both arguments are program
// constants, never user input.
func reminderKeySQL(occ, kind string, minutes int) string {
	return "'" + kind + ":' || " + occ + ".id || ':' || extract(epoch FROM " + occ + ".starts_at)::bigint || ':" + strconv.Itoa(minutes) + "'"
}
