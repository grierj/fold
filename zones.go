package main

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"fold/internal/events"

	"gorm.io/gorm"
)

// Time zones. The church has one (the `timezone` setting, set at setup and in
// Admin → Settings). A person may have their own (User.Timezone, DEC-58); an
// event has the one it was written in (Event.TZID). Every zone is parsed with
// events.LoadZone, which refuses "" and "Local".

// errBadChurchZone is a stored church zone that is no zone. A failed read is a
// different error.
var errBadChurchZone = errors.New("the church time zone setting is not a zone")

// churchZoneIn is the church's zone, read on the caller's connection so a
// caller inside a transaction sees what the transaction sees. An unset or empty
// setting is UTC. A failed read, or a stored value that is not a zone (the
// setting is validated on every write, so only a damaged row), is an error: a
// caller that can fail returns it rather than guess a day or a clock. That
// covers every decision made from a person's age (household eligibility,
// contact rules), the avatar sweep and the age rollover.
func churchZoneIn(tx *gorm.DB) (*time.Location, error) {
	v, found, err := settingIn(tx, settingTimezone)
	if err != nil {
		return nil, err
	}
	if !found || strings.TrimSpace(v) == "" {
		return time.UTC, nil
	}
	loc, err := events.LoadZone(v)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errBadChurchZone, err)
	}
	return loc, nil
}

// churchZoneLenient is churchZoneIn for work that must still happen when the
// stored zone is damaged: it logs a stored value that is no zone and uses UTC.
// A failed read is still an error, so a job retries instead of guessing. It is
// the one place that falls back for a damaged value. The daily job queue uses
// it directly (its jobs are keyed by date and idempotent, and privacy cleanups
// matter more than a day boundary a few hours off), and so does the guest
// verification mail.
func churchZoneLenient(tx *gorm.DB) (*time.Location, error) {
	loc, err := churchZoneIn(tx)
	if errors.Is(err, errBadChurchZone) {
		log.Printf("zones: using UTC: %v", err)
		return time.UTC, nil
	}
	return loc, err
}

// churchZoneOrUTC is churchZoneLenient for the paths where even a failed read
// must not drop a notice or break a page: emails and notices, push quiet hours,
// reminders, the site payload and defaults for calendar views, and age shown on
// a page. It logs the problem and shows UTC.
func churchZoneOrUTC(tx *gorm.DB) *time.Location {
	loc, err := churchZoneLenient(tx)
	if err != nil {
		log.Printf("zones: using UTC: %v", err)
		return time.UTC
	}
	return loc
}

// churchClockIn is now on the church's wall clock: the instant from which a
// household person's age is counted.
func churchClockIn(tx *gorm.DB, now time.Time) (time.Time, error) {
	loc, err := churchZoneIn(tx)
	if err != nil {
		return time.Time{}, err
	}
	return now.In(loc), nil
}

// userZone is the zone a person reads times in: tz (User.Timezone or a guest's
// form value) when it names a zone, else the church's. A nil church is UTC.
func userZone(tz string, church *time.Location) *time.Location {
	if loc, err := events.LoadZone(tz); err == nil {
		return loc
	}
	if church == nil {
		return time.UTC
	}
	return church
}

// userZoneIn is userZone for a caller that has not read the church zone: it
// reads it only when tz is no zone.
func userZoneIn(tx *gorm.DB, tz string) *time.Location {
	if loc, err := events.LoadZone(tz); err == nil {
		return loc
	}
	return churchZoneOrUTC(tx)
}

// eventZone is the zone an event's times are written in. It was validated when
// the event was saved, so UTC here only covers a damaged row.
func eventZone(tzid string) *time.Location {
	if loc, err := events.LoadZone(tzid); err == nil {
		return loc
	}
	return time.UTC
}
