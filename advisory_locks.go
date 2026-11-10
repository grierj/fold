package main

// Every Postgres advisory lock id this process takes, in one place so two
// features cannot pick the same number. TestAdvisoryLockIDsAreUnique checks
// this block together with the ids held by the migrations runner and the
// occurrence-extension job (eventdates).
const (
	// setupLockID serializes first-account creation across server processes.
	setupLockID int64 = 0x466f6c6453657475
	// contactLockID is the coarse contact gate (lockContacts).
	contactLockID int64 = 0x464f4c44434f4e
	// invitationEmailLockSeed seeds the hash that locks one invitation email
	// (lockInvitationEmail): ordinary and household invitations reserve the
	// same address namespace.
	invitationEmailLockSeed int64 = 7316193
	// inviteSendLockClass is the first key of the two-key lock that serializes
	// one actor's invitation sends (inviteSendAllowed); the actor id is the second.
	inviteSendLockClass int32 = 7316194
	// guestVerifyLockClass is the first key of the two-key lock that serializes
	// one address's guest verification count (guestVerifyAllowedIn); the second
	// key is the address's citext-folded hashtext. Postgres keeps the two-key
	// space separate from the one-key ids above, so no address, however
	// crafted, can take setupLockID, contactLockID, migrations.LockID or
	// eventdates.LockID.
	guestVerifyLockClass int32 = 7316195
)
