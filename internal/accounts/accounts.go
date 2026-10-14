// Package accounts holds the age gate shared by every door that can
// create an account (5.10, DEC-8, M1.3): self-signup, member
// invite-a-friend, leader and admin invitations, and (M1.4) guest
// registration. Nobody under 13 gets an account; 13 to 17 is a teen
// account that starts pending_consent; 18 or older is an adult.
package accounts

import (
	"errors"
	"time"

	"fold/models"
)

// Age bands as the person picks them on a form.
const (
	BandAdult   = "adult"   // "18 or older"
	BandTeen    = "teen"    // "13 to 17"
	BandUnder13 = "under13" // "under 13"
)

// ErrUnder13: the person said they are under 13. Callers store nothing
// and show the household explanation (a parent or guardian adds the
// child to their household instead).
var ErrUnder13 = errors.New("accounts: under 13")

// ErrAgeMismatch: the birth date disagrees with the picked band, or a
// required birth date is missing or unusable. Callers answer 400.
var ErrAgeMismatch = errors.New("accounts: birth date does not match the age choice")

// AgeOn returns the age in whole years on the given day. Only the
// calendar dates count, so time zones never shift a birthday.
func AgeOn(birth, on time.Time) int {
	age := on.Year() - birth.Year()
	if on.Month() < birth.Month() || (on.Month() == birth.Month() && on.Day() < birth.Day()) {
		age--
	}
	return age
}

// Classify turns a picked band and an optional birth date into the
// account type to store. A teen must give a birth date aged 13 to 17;
// an adult's birth date is optional (adults only confirm 18+, 5.3),
// but one that is given must agree. Under 13 is ErrUnder13 whatever
// else was typed.
func Classify(band string, birthDate *time.Time, today time.Time) (string, error) {
	switch band {
	case BandUnder13:
		return "", ErrUnder13
	case BandTeen:
		if birthDate == nil {
			return "", ErrAgeMismatch
		}
		if age := AgeOn(*birthDate, today); age < 13 || age > 17 {
			return "", ErrAgeMismatch
		}
		return models.AccountTeen, nil
	case BandAdult:
		if birthDate != nil && AgeOn(*birthDate, today) < 18 {
			return "", ErrAgeMismatch
		}
		return models.AccountAdult, nil
	default:
		return "", ErrAgeMismatch
	}
}

// ParseBirthDate parses a YYYY-MM-DD form value. Empty means none.
func ParseBirthDate(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil, ErrAgeMismatch
	}
	return &t, nil
}
