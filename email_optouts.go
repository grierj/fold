package main

import (
	"errors"
	"strings"

	"fold/internal/mail"
	"fold/models"

	"gorm.io/gorm"
)

type emailDest struct {
	UserID  uint
	Address string
}

type emailOff struct {
	account map[uint]map[string]bool
	address map[string]map[string]bool
}

func (o emailOff) Off(userID uint, address, scope string) bool {
	return o.account[userID][scope] || o.address[mail.NormalizeAddress(address)][scope]
}

// readEmailOff is the only reader of email consent choices and retained address
// opt-outs. Either choice turns mail off, with no time comparison: an account's
// earlier opt-in cannot override a retained choice at the destination address.
// Callers pass the actual destination, including a queued message's snapshot.
// Each chunk reads both arms in one statement, at READ COMMITTED's statement
// snapshot, with at most 500 destinations and hashes computed only in Go.
func readEmailOff(db *gorm.DB, dests []emailDest) (emailOff, error) {
	out := emailOff{account: map[uint]map[string]bool{}, address: map[string]map[string]bool{}}
	idsSeen, addressesSeen := map[uint]bool{}, map[string]bool{}
	unique := make([]emailDest, 0, len(dests))
	for _, d := range dests {
		d.Address = mail.NormalizeAddress(d.Address)
		if idsSeen[d.UserID] {
			d.UserID = 0
		}
		if addressesSeen[d.Address] {
			d.Address = ""
		}
		if d.UserID == 0 && d.Address == "" {
			continue
		}
		if d.UserID != 0 {
			idsSeen[d.UserID] = true
		}
		if d.Address != "" {
			addressesSeen[d.Address] = true
		}
		unique = append(unique, d)
	}
	for start := 0; start < len(unique); start += 500 {
		end := min(start+500, len(unique))
		var ids []uint
		var hashes []string
		addresses := map[string]string{}
		for _, d := range unique[start:end] {
			if d.UserID != 0 {
				ids = append(ids, d.UserID)
			}
			if d.Address != "" {
				h := mail.AddressHash(d.Address)
				hashes = append(hashes, h)
				addresses[h] = d.Address
			}
		}
		var arms []string
		var args []any
		if len(ids) != 0 {
			arms = append(arms, `SELECT c.user_id, '' AS address_hash, c.scope FROM (
				SELECT DISTINCT ON (user_id, scope) user_id, scope, status FROM consents
				WHERE user_id IN ? AND channel = ? ORDER BY user_id, scope, at DESC, id DESC
			) c WHERE c.status = ?`)
			args = append(args, ids, models.ConsentEmail, models.ConsentOptedOut)
		}
		if len(hashes) != 0 {
			arms = append(arms, `SELECT 0 AS user_id, address_hash, scope FROM email_address_optouts WHERE address_hash IN ?`)
			args = append(args, hashes)
		}
		var rows []struct {
			UserID             uint
			AddressHash, Scope string
		}
		if err := db.Raw(strings.Join(arms, " UNION ALL "), args...).Scan(&rows).Error; err != nil {
			return emailOff{}, err
		}
		for _, r := range rows {
			if r.UserID != 0 {
				if out.account[r.UserID] == nil {
					out.account[r.UserID] = map[string]bool{}
				}
				out.account[r.UserID][r.Scope] = true
			} else {
				address := addresses[r.AddressHash]
				if out.address[address] == nil {
					out.address[address] = map[string]bool{}
				}
				out.address[address][r.Scope] = true
			}
		}
	}
	return out, nil
}

// clearOptOutOnTurnOn runs only after setConsent has locked and checked the
// current holder. Refuse an unbound link before looking for any retained record,
// so the refusal cannot reveal whether that address has a record.
func clearOptOutOnTurnOn(scope string, session *models.User, link *unsubscribeLink) func(*gorm.DB, *models.User) (bool, error) {
	return func(tx *gorm.DB, fresh *models.User) (bool, error) {
		if session == nil && !link.boundTo(fresh.Email) {
			return false, errUnboundEmailLink
		}
		if mail.NormalizeAddress(fresh.Email) == "" {
			return false, nil
		}
		result := tx.Exec("DELETE FROM email_address_optouts WHERE address_hash = ? AND scope = ?", mail.AddressHash(fresh.Email), scope)
		return result.RowsAffected > 0, result.Error
	}
}

const errEmailLinkTurnOn = "this link can only turn email off; sign in or use a newer email to turn it on"

var errUnboundEmailLink = errors.New(errEmailLinkTurnOn)
