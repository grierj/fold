package main

import (
	"fold/models"

	"gorm.io/gorm"
)

// defaultReminderMinutes is a new event's reminder time: 1 day (DEC-133).
const defaultReminderMinutes = 1440

// createEvent inserts ev. GORM leaves a zero value out of an INSERT, so the
// columns' defaults (1 day, push on) would replace a leader's "Off"; an
// Off is written explicitly after. A caller builds ev with ReminderPush
// true unless the leader turned it off.
func createEvent(tx *gorm.DB, ev *models.Event) error {
	// Create reads the columns' defaults back into ev, so the choices are
	// kept from before it.
	emailOff, pushOff := ev.ReminderEmailMinutes == 0, !ev.ReminderPush
	if err := tx.Create(ev).Error; err != nil {
		return err
	}
	off := map[string]any{}
	if emailOff {
		ev.ReminderEmailMinutes = 0
		off["reminder_email_minutes"] = 0
	}
	if pushOff {
		ev.ReminderPush = false
		off["reminder_push"] = false
	}
	if len(off) == 0 {
		return nil
	}
	return tx.Model(ev).Updates(off).Error
}
