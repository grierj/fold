package main

import (
	"errors"
	"strings"

	"fold/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Church settings (PLAN 5.3): one key/value table, read and written only
// through the helpers in this file. Every key is named here.
const (
	settingSiteName       = "site_name"
	settingTimezone       = "timezone"
	settingSelfSignup     = "self_signup"
	settingRoomsEnabled   = "rooms_enabled"
	settingRoomApproval   = "room_bookings_need_approval"
	settingChurchAddress  = "church_address" // postal address for email footers (PLAN 10.5, DEC-116); empty until an admin sets it
	settingJitsiDomain    = "jitsi_domain"
	settingPushEnabled    = "push_enabled" // the church's switch (DEC-124), default on
	settingVAPIDPublic    = "vapid_public_key"
	settingVAPIDPrivate   = "vapid_private_key_enc"
	settingGuardianCopies = "guardian_copies"  // "true" sends each confirmed guardian a marked copy of messages to their teen (DEC-19, DEC-29); default off
	settingReplyTo        = "reply_to_address" // where replies to an announcement go (DEC-130); empty: none
)

const defaultSiteName = "Fold"

// settingIn reads one setting on the caller's connection. found is false when
// the row does not exist; the value is returned as stored.
func settingIn(tx *gorm.DB, key string) (value string, found bool, err error) {
	var s models.Setting
	err = tx.Where("key = ?", key).First(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return s.Value, true, nil
}

// setSettingIn writes key = value, creating the row when it is missing. It is
// the one writer of the settings table through models.Setting, so it is the one
// place that refuses the restore barrier's row (restore_fence.go); the census
// in restore_fence_test.go keeps it so. An empty value is stored, not skipped.
func setSettingIn(tx *gorm.DB, key, value string) error {
	if err := refuseRestoreFenceKey(key); err != nil {
		return err
	}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value"})}).
		Create(&models.Setting{Key: key, Value: value}).Error
}

// boolSettingIn reads a switch stored as "true" or "false". A missing row, any
// other value and a failed read all give def, so each switch keeps the default
// it documents (on for push, off for rooms and guardian copies).
func boolSettingIn(tx *gorm.DB, key string, def bool) bool {
	v, found, err := settingIn(tx, key)
	if err != nil || !found {
		return def
	}
	switch strings.TrimSpace(v) {
	case "true":
		return true
	case "false":
		return false
	}
	return def
}

// settingOnSQL is boolSettingIn(key, false) as a SQL condition with the key
// as its one argument, for a query that must read the switch in the same
// statement (the rooms lookup pays one read whether rooms are on or off).
// TestSettingOnSQLAgreesWithTheGoReader pins it to the Go reader.
const settingOnSQL = `EXISTS (SELECT 1 FROM settings WHERE key = ? AND btrim(value) = 'true')`

// settingValueIn is the trimmed value of key, or "" when it is missing. A
// failed read is an error: a send must not go out without the church's
// footer address or reply-to because the database blinked.
func settingValueIn(tx *gorm.DB, key string) (string, error) {
	v, _, err := settingIn(tx, key)
	return strings.TrimSpace(v), err
}

// siteNameIn is the church's name, or defaultSiteName while none is set. It
// uses the caller's connection and context, so mail preparation reads it in
// its transaction and a failure retries.
func siteNameIn(tx *gorm.DB) (string, error) {
	v, _, err := settingIn(tx, settingSiteName)
	if err != nil {
		return "", err
	}
	if v == "" {
		return defaultSiteName, nil
	}
	return v, nil
}

// siteName is siteNameIn for pages that must render anyway: a failed read shows
// the default.
func (a *App) siteName() string {
	name, err := siteNameIn(a.DB)
	if err != nil {
		return defaultSiteName
	}
	return name
}
