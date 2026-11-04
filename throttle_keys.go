package main

import (
	"sort"
	"strconv"
	"strings"

	"fold/internal/mail"
)

// Every login_throttles key is built here and nowhere else. A key is a
// throttleKey, so a concatenated string cannot reach a throttle helper, and
// TestThrottleKeysComeFromBuilders (throttle_keys_test.go) rejects the
// source shapes that would get around the type.
//
// Each builder says whether the key it makes is owned or shared:
//
//	// owned:  keyed by one person's id, or by the address an account holds.
//	           Erasing that person deletes the key (ownedThrottleKeys).
//	// shared: keyed by an IP or by the whole site. It names no person and
//	           other people's budgets live in it, so erasure never touches it.
//
// The strings below are the ones Fold has always stored. Rows written before
// this file existed keep their meaning (TestThrottleKeyStringsAreUnchanged).
// Address builders normalize with mail.NormalizeAddress, which changes nothing
// in an address that is already normalized, and IP builders reduce the address
// with rateLimitIP, which is idempotent.
type throttleKey string

// loginEmailKey is the sign-in failure budget for one address.
// owned: the address an account holds.
func loginEmailKey(addr string) throttleKey {
	return throttleKey("email:" + mail.NormalizeAddress(addr))
}

// loginUserKey is the reauthentication failure budget for one signed-in person.
// owned: one person's id.
func loginUserKey(id uint) throttleKey {
	return throttleKey("user:" + strconv.FormatUint(uint64(id), 10))
}

// loginIPKey is the sign-in and reauthentication failure budget for one IP.
// shared: an IP.
func loginIPKey(ip string) throttleKey {
	return throttleKey("ip:" + rateLimitIP(ip))
}

// pwresetAddrKey is the password-reset mail budget for one address.
// owned: the address an account holds. Targeted (see targeted).
func pwresetAddrKey(addr string) throttleKey {
	return throttleKey("pwreset:addr:" + mail.NormalizeAddress(addr))
}

// pwresetIPKey is the password-reset request budget for one IP.
// shared: an IP.
func pwresetIPKey(ip string) throttleKey {
	return throttleKey("pwreset:ip:" + rateLimitIP(ip))
}

// emailChangeUserKey is the changer budget for email-change links.
// owned: one person's id. Targeted.
func emailChangeUserKey(id uint) throttleKey {
	return throttleKey("emailchange:user:" + strconv.FormatUint(uint64(id), 10))
}

// emailChangeAddrKey is the target budget for email-change links to one
// address. It is owned only for the address the account holds when it is
// erased: the row carries an address and a count, never a requester, so it
// can also hold other accounts' spends (T19 signs that disposition).
// owned: the address an account holds. Targeted.
func emailChangeAddrKey(addr string) throttleKey {
	return throttleKey("emailchange:addr:" + mail.NormalizeAddress(addr))
}

// accountRequestAddrKey is the per-address budget for account requests.
// owned: the address an account holds. Targeted.
func accountRequestAddrKey(addr string) throttleKey {
	return throttleKey("acctreq:addr:" + mail.NormalizeAddress(addr))
}

// accountRequestIPKey is the account-request budget for one IP.
// shared: an IP.
func accountRequestIPKey(ip string) throttleKey {
	return throttleKey("acctreq:ip:" + rateLimitIP(ip))
}

// accountRequestSiteKey is the site-wide account-request budget.
// shared: the whole site.
func accountRequestSiteKey() throttleKey {
	return throttleKey("acctreq:site")
}

// guestHintKey is the per-address budget for the "you already have an
// account" hint a guest registration can send.
// owned: the address an account holds. Targeted.
func guestHintKey(addr string) throttleKey {
	return throttleKey("guestreg:hint:" + mail.NormalizeAddress(addr))
}

// guestIPKey is the guest-registration budget for one IP.
// shared: an IP.
func guestIPKey(ip string) throttleKey {
	return throttleKey("guestreg:ip:" + rateLimitIP(ip))
}

// guestSiteKey is the site-wide guest-registration budget.
// shared: the whole site.
func guestSiteKey() throttleKey {
	return throttleKey("guestreg:site")
}

// guardianClaimAddrKey is the per-address budget for guardian claims. The
// address is the one searched for, which an account may hold (T19).
// owned: the address an account holds. Targeted.
func guardianClaimAddrKey(addr string) throttleKey {
	return throttleKey("guardian:addr:" + mail.NormalizeAddress(addr))
}

// guardianClaimIPKey is the guardian-claim budget for one IP.
// shared: an IP.
func guardianClaimIPKey(ip string) throttleKey {
	return throttleKey("guardian:ip:" + rateLimitIP(ip))
}

// guardianEmailAddrKey is the per-address budget for guardian consent resends.
// owned: the address an account holds. Targeted.
func guardianEmailAddrKey(addr string) throttleKey {
	return throttleKey("guardian-email:addr:" + mail.NormalizeAddress(addr))
}

// peopleSearchUserKey is the people-search budget for one person.
// owned: one person's id.
func peopleSearchUserKey(id uint) throttleKey {
	return throttleKey("people-search:user:" + strconv.FormatUint(uint64(id), 10))
}

// managerDateAddKey is the budget for a manager adding people to a date.
// owned: one person's id.
func managerDateAddKey(id uint) throttleKey {
	return throttleKey("manager-date-add:" + strconv.FormatUint(uint64(id), 10))
}

// targeted returns the hourly and daily keys a targetAllow call counts
// against. The suffix rule lives here and nowhere else.
func (k throttleKey) targeted() [2]throttleKey {
	return [2]throttleKey{k + ":h", k + ":d"}
}

// isIP reports whether the key is an IP's sign-in budget, which has the
// higher failure limit.
func (k throttleKey) isIP() bool {
	return strings.HasPrefix(string(k), "ip:")
}

// throttleKeyStrings converts keys for SQL binding. Statements bind strings,
// never the named type.
func throttleKeyStrings(ks []throttleKey) []string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = string(k)
	}
	return out
}

// ownedThrottleKeys returns every stored key an erased account owns: each
// owned builder's key, with both suffixes for the targeted families, sorted
// and de-duplicated. userID is the account's id and addr the address it holds
// at erasure (read inside the erasure transaction and held only in memory).
// Erasure (R4-1-2) is its only caller. A builder is listed here exactly when
// its doc line says owned (TestOwnedThrottleKeysCoverEveryOwnedBuilder).
func ownedThrottleKeys(userID uint, addr string) []string {
	keys := []throttleKey{
		loginEmailKey(addr),
		loginUserKey(userID),
		peopleSearchUserKey(userID),
		managerDateAddKey(userID),
	}
	for _, k := range []throttleKey{
		pwresetAddrKey(addr),
		emailChangeUserKey(userID),
		emailChangeAddrKey(addr),
		accountRequestAddrKey(addr),
		guestHintKey(addr),
		guardianClaimAddrKey(addr),
		guardianEmailAddrKey(addr),
	} {
		both := k.targeted()
		keys = append(keys, both[0], both[1])
	}
	out := throttleKeyStrings(keys)
	sort.Strings(out)
	n := 0
	for i, s := range out {
		if i == 0 || s != out[n-1] {
			out[n] = s
			n++
		}
	}
	return out[:n]
}
