package main

import (
	"errors"
	"time"

	"fold/models"

	"gorm.io/gorm"
)

// The event fields of an announcement a person is sent are filled when they
// are sent, by what that person may see of the event or date at that moment,
// through readerEventValues: the same per-field rules as the read page
// (exact-date invitations, occurrence overrides, cancellation, date-specific
// links). The recipient snapshot decides who is sent a message and never what
// event data they see; a rename, a tightened visibility or a lost membership
// between preparation and delivery reaches the next send, retry or push.
//
// The consistency window is the span between this read and the provider
// accepting the message (or the push service taking the payload). Reads here
// are not one snapshot and no lock is held across network delivery, so a
// change committed inside that window may or may not be reflected; the next
// row, retry or push rechecks.

// rowReaders loads the live people one claimed email row concerns: the
// recipient, and for a guardian copy the teen whose message it copies. The
// recipient is nil when that account is gone; the teen is absent from the map
// then too. Role and status come from this read, never from a batch-wide
// cache, because an administrator may have been demoted since an earlier row
// loaded.
func rowReaders(db *gorm.DB, row models.MessageRecipient) (*models.User, map[uint]*models.User, error) {
	ids := []uint{row.UserID}
	if row.CopyOfUserID != nil {
		ids = append(ids, *row.CopyOfUserID)
	}
	var users []models.User
	if err := db.Where("id IN ?", ids).Find(&users).Error; err != nil {
		return nil, nil, err
	}
	readers := make(map[uint]*models.User, len(users))
	for i := range users {
		readers[users[i].ID] = &users[i]
	}
	return readers[row.UserID], readers, nil
}

// rowValues are the merge values one email row renders with, keyed by the
// user whose values they are: the recipient, or for a guardian copy the teen,
// whose live access decides the fields. A person who is gone gets no entry,
// so no protected field is filled for them; a failed read is returned and
// fails the delivery, never read as "may not see". db is the transaction that
// claimed the row, so its reads never wait for a second connection while that
// one is held. A guest is personalized as themselves but sees only what a
// signed-out visitor does (eventViewer).
func (a *App) rowValues(db *gorm.DB, row models.MessageRecipient, readers map[uint]*models.User, au audienceJSON, now time.Time) (map[uint]map[string]string, error) {
	subject := row.UserID
	if row.CopyOfUserID != nil {
		subject = *row.CopyOfUserID
	}
	out := map[uint]map[string]string{}
	u := readers[subject]
	if u == nil {
		return out, nil
	}
	vals, err := a.readerEventValues(db, u, au, now)
	if err != nil {
		return nil, err
	}
	out[subject] = vals
	return out, nil
}

// pushEventValues are u's merge values for an announcement push, read now,
// just before the payload is built (after quiet hours have let it go), on
// the push job's context-bound handle. The row's user is reloaded in full,
// since the one the push job holds carries no role. An account that is gone
// has no event fields.
func (a *App) pushEventValues(db *gorm.DB, userID uint, au audienceJSON, now time.Time) (map[string]string, error) {
	var live models.User
	if err := db.First(&live, userID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return map[string]string{}, nil
	} else if err != nil {
		return nil, err
	}
	return a.readerEventValues(db, &live, au, now)
}
