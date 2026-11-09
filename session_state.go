package main

import (
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/sessions"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"fold/internal/policy"
	"fold/models"
)

// One home for what "this request's session is still current" means: the
// cookie keys, the one predicate, the one actor lock built on it, the one
// way to end every session of an account, and the one 401 answer.

// The session cookie's keys. A session holds the account and the two epochs
// the account's row must still carry (session_version: the credential epoch;
// logout_version: the sign-out epoch, M7.2).
const (
	sessionUserKey    = "userID"
	sessionVersionKey = "sessionVersion"
	sessionLogoutKey  = "logoutVersion"
)

func setSessionLogin(s *sessions.Session, u *models.User) {
	s.Values[sessionUserKey] = u.ID
	s.Values[sessionVersionKey] = u.SessionVersion
	s.Values[sessionLogoutKey] = u.LogoutVersion
}

func clearSessionLogin(s *sessions.Session) {
	delete(s.Values, sessionUserKey)
	delete(s.Values, sessionVersionKey)
	delete(s.Values, sessionLogoutKey)
}

// sessionCurrent is the one decision that a session may still act: fresh is
// the account's row as read now (live or under a lock), session is the user as
// the request authenticated it (only ID, SessionVersion and LogoutVersion are
// read). The row must be there, not soft deleted, able to sign in (an active
// account with a password) and carry both of the session's epochs. Every
// recheck of a signed-in actor, from the middleware to the actor seal, decides
// with this function, so a new epoch is added here and nowhere else.
func sessionCurrent(fresh, session *models.User) bool {
	return fresh != nil && session != nil && fresh.ID != 0 && fresh.ID == session.ID &&
		!fresh.DeletedAt.Valid && fresh.CanSignIn() &&
		fresh.SessionVersion == session.SessionVersion && fresh.LogoutVersion == session.LogoutVersion
}

// adminCurrent is sessionCurrent for a request that needs an admin: the same
// decision plus the row's own role, for the sites that hold the actor in a
// batch of locked rows (or lock it themselves) and refuse with their own error.
func adminCurrent(fresh, session *models.User) bool {
	return sessionCurrent(fresh, session) && policy.IsAdmin(asPerson(fresh))
}

// actorLock is how lockActorIn reads the actor's row. The set is small and
// fixed; each site keeps the strength it had, because the lock order and the
// deadlock analysis of the transaction depend on it.
type actorLock int

const (
	// actorRead is a plain read, for a transaction in which a lock on the
	// actor could cycle with the actor's own ungated change (the contact gate
	// serializes the rest), or that reads one snapshot.
	actorRead actorLock = iota
	// actorShare takes the row FOR SHARE: no change to the actor until commit.
	actorShare
	// actorNoKeyUpdate takes the row FOR NO KEY UPDATE, the user lock of the
	// writers that go on to change the account's own rows.
	actorNoKeyUpdate
)

func (m actorLock) clause() []clause.Expression {
	switch m {
	case actorShare:
		return []clause.Expression{clause.Locking{Strength: "SHARE"}}
	case actorNoKeyUpdate:
		return []clause.Expression{clause.Locking{Strength: "NO KEY UPDATE"}}
	}
	return nil
}

// lockActorIn reads the signed-in actor's own row (locked as mode says) and
// returns it when the session is still current. A missing or soft-deleted row
// and a stale session both answer errSecuritySession and the caller writes
// nothing; any other error is the database's. The caller uses the returned
// row, never the preloaded one, for every later fact.
func lockActorIn(tx *gorm.DB, session *models.User, mode actorLock) (*models.User, error) {
	var fresh models.User
	if err := tx.Clauses(mode.clause()...).First(&fresh, session.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errSecuritySession
		}
		return nil, err
	}
	if !sessionCurrent(&fresh, session) {
		return nil, errSecuritySession
	}
	return &fresh, nil
}

// lockAdminIn is lockActorIn for a writer that also needs the actor to be an
// admin on the locked row. A current non-admin answers errSecuritySession too:
// every admin route is behind the admin middleware and a role change bumps the
// session version, so the two are the same stale request.
func lockAdminIn(tx *gorm.DB, session *models.User, mode actorLock) (*models.User, error) {
	fresh, err := lockActorIn(tx, session, mode)
	if err != nil {
		return nil, err
	}
	if !adminCurrent(fresh, session) {
		return nil, errSecuritySession
	}
	return fresh, nil
}

// sessionVersionBump is the column value that ends every login and every
// emailed or pushed capability bound to the credential epoch.
func sessionVersionBump() clause.Expr { return gorm.Expr("session_version + 1") }

// endAllSessionsIn ends every session of the account in the caller's
// transaction, after the contact gate and the user lock: it sets set (columns
// that change with it, such as the new email or password hash) and bumps the
// session version in one UPDATE of user's row, then revokes every outstanding
// account capability. Writers that bump the epoch without revoking tokens (a
// re-enabled account keeps the setup link it was sent) use sessionVersionBump
// directly.
func endAllSessionsIn(tx *gorm.DB, user *models.User, set map[string]any) error {
	updates := map[string]any{"session_version": sessionVersionBump()}
	for k, v := range set {
		updates[k] = v
	}
	if err := tx.Model(user).Updates(updates).Error; err != nil {
		return err
	}
	return revokeAccountTokens(tx, user.ID)
}

func answerStaleSession(c *gin.Context, err error) bool {
	if !errors.Is(err, errSecuritySession) {
		return false
	}
	fail(c, errSecuritySession)
	return true
}

// sessionVersion reads the password generation stored at login.
// A cookie issued before this field existed has no value and counts as 0.
func sessionVersion(s *sessions.Session) int { return sessionInt(s, sessionVersionKey) }

// logoutVersion reads the logout generation stored at login (M7.2); an
// older cookie has none and counts as 0, the column's default.
func logoutVersion(s *sessions.Session) int { return sessionInt(s, sessionLogoutKey) }

func sessionInt(s *sessions.Session, key string) int {
	switch v := s.Values[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case uint:
		return int(v)
	default:
		return 0
	}
}
