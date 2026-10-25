package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const peopleSearchLimit = 30 // per actor per minute; persisted with the existing throttle seam
const peopleSelectionLifetime = 10 * time.Minute

var errTeenAddApproval = errors.New("adding a teen needs their approval and an admin's approval; ask your church office for help")

// Selections identify a search result, not permission to add it or see an event.
// Authenticated encryption hides sequential IDs and binds the result to its searcher.
type peopleSelection struct {
	ActorID   uint  `json:"actor"`
	SubjectID uint  `json:"subject"`
	IssuedAt  int64 `json:"issued"`
	ExpiresAt int64 `json:"expires"`
}

func (a *App) peopleSelectionCipher() (cipher.AEAD, error) {
	if len(a.Config.SessionSecret) == 0 {
		return nil, errors.New("missing selection signing secret")
	}
	key := sha256.Sum256(append([]byte("fold:people-selection:v1:"), a.Config.SessionSecret...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (a *App) newPeopleSelection(actor, subject uint, now time.Time) (string, error) {
	crypt, err := a.peopleSelectionCipher()
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(peopleSelection{actor, subject, now.Unix(), now.Add(peopleSelectionLifetime).Unix()})
	if err != nil {
		return "", err
	}
	nonce := make([]byte, crypt.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(crypt.Seal(nonce, nonce, data, []byte("ordinary-person-add"))), nil
}

func (a *App) parsePeopleSelection(token string, actor uint, now time.Time) (uint, error) {
	invalid := errors.New("invalid selection")
	if len(token) > 512 {
		return 0, invalid
	}
	crypt, err := a.peopleSelectionCipher()
	if err != nil {
		return 0, err
	}
	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(data) < crypt.NonceSize()+crypt.Overhead() {
		return 0, invalid
	}
	plain, err := crypt.Open(nil, data[:crypt.NonceSize()], data[crypt.NonceSize():], []byte("ordinary-person-add"))
	if err != nil {
		return 0, invalid
	}
	var s peopleSelection
	if json.Unmarshal(plain, &s) != nil || s.ActorID != actor || s.SubjectID == 0 || s.IssuedAt > now.Unix() || s.ExpiresAt <= now.Unix() || s.ExpiresAt-s.IssuedAt != int64(peopleSelectionLifetime/time.Second) {
		return 0, invalid
	}
	return s.SubjectID, nil
}

func (a *App) canSearchPeople(db *gorm.DB, actor *models.User) (bool, error) {
	zone, err := contactZone(db)
	if err != nil {
		return false, err
	}
	var facts struct {
		Leader bool
		Host   bool
	}
	err = db.Raw(`SELECT EXISTS (SELECT 1 FROM memberships m JOIN groups g ON g.id=m.group_id WHERE m.user_id=? AND m.role='leader' AND m.status='active' AND g.deleted_at IS NULL) AS leader,
 EXISTS (SELECT 1 FROM event_hosts h JOIN events e ON e.id=h.event_id WHERE h.user_id=? AND e.group_id IS NULL AND e.deleted_at IS NULL) AS host`, actor.ID, actor.ID).Scan(&facts).Error
	return policy.CanSearchPeople(contactAccount(actor, time.Now(), zone), facts.Leader, facts.Host), err
}

var errPeopleAddForbidden = errors.New("people add forbidden")

// peopleAddActor runs after the contact gate, shared with account and role
// revocation. Request authentication alone cannot authorize a queued add.
func peopleAddActor(tx *gorm.DB, authenticated *models.User) (*models.User, error) {
	actor, err := contactUser(tx, authenticated.ID)
	if err != nil {
		return nil, err
	}
	if actor == nil || actor.SessionVersion != authenticated.SessionVersion {
		return nil, errPeopleAddForbidden
	}
	zone, err := contactZone(tx)
	if err != nil {
		return nil, err
	}
	if !policy.CanInitiatePeopleAdd(contactAccount(actor, time.Now(), zone)) {
		return nil, errPeopleAddForbidden
	}
	return actor, nil
}

// resolvePeopleSelection checks search eligibility and target availability.
// Mutations must still refresh actor/session and destination authority under
// the contact gate; this resolution is not an authorization boundary.
func (a *App) resolvePeopleSelection(c *gin.Context, token string) (*models.User, bool) {
	actor := apiUser(c)
	allowed, err := a.canSearchPeople(a.DB, actor)
	if err != nil {
		jsonError(c, 500, "could not search people")
		return nil, false
	}
	id, err := a.parsePeopleSelection(token, actor.ID, time.Now())
	var target models.User
	if !allowed || err != nil || a.DB.Where("id = ? AND status = ? AND account_type IN ?", id, models.UserActive, []string{models.AccountAdult, models.AccountTeen}).First(&target).Error != nil {
		jsonError(c, 400, "that selection is unavailable; search for the person again")
		return nil, false
	}
	return &target, true
}

func (a *App) apiSearchPeople(c *gin.Context) {
	// Limit database work even when a literal substring has many candidates.
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	db := a.DB.WithContext(ctx)
	actor := apiUser(c)
	allowed, err := a.canSearchPeople(db, actor)
	if err != nil {
		jsonError(c, 500, "could not search people")
		return
	}
	if !allowed {
		jsonError(c, 403, "forbidden")
		return
	}
	out := peopleSearchJSON{State: "short_query", People: []personSearchJSON{}}
	query := strings.TrimSpace(c.Query("q"))
	if utf8.RuneCountInString(query) < 2 {
		c.JSON(200, out)
		return
	}
	if utf8.RuneCountInString(query) > 100 {
		c.JSON(200, peopleSearchJSON{State: "no_results", People: out.People})
		return
	}
	allowed, err = a.throttleAllow(ctx, fmt.Sprintf("people-search:user:%d", actor.ID), peopleSearchLimit, time.Minute)
	if err != nil {
		jsonError(c, 500, "could not search people")
		return
	}
	if !allowed {
		out.State = "throttled"
		c.JSON(200, out)
		return
	}
	// Escape SQL wildcards: punctuation must not become an account-browse query.
	pattern := "%" + strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(query) + "%"
	var users []models.User
	if err := db.Select("id", "name", "display_name", "account_type", "avatar_key", "avatar_focal_x", "avatar_focal_y").Where("status = ? AND account_type IN ? AND (name ILIKE ? OR display_name ILIKE ?)", models.UserActive, []string{models.AccountAdult, models.AccountTeen}, pattern, pattern).Order("lower(COALESCE(NULLIF(display_name,''),name)), id").Limit(10).Find(&users).Error; err != nil {
		jsonError(c, 500, "could not search people")
		return
	}
	ids := make([]uint, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.ID)
	}
	groups, err := a.peopleSearchGroups(db, actor, ids)
	if err != nil {
		jsonError(c, 500, "could not search people")
		return
	}
	for _, u := range users {
		token, err := a.newPeopleSelection(actor.ID, u.ID, time.Now())
		if err != nil {
			jsonError(c, 500, "could not search people")
			return
		}
		item := personSearchJSON{SelectionID: token, DisplayName: u.PublicName(), Teen: u.AccountType == models.AccountTeen, SharedGroups: []string{}, OtherGroups: []string{}}
		if !item.Teen && u.AvatarKey != "" {
			item.AvatarURL = a.Uploads.URL(u.AvatarKey)
			item.AvatarFocalX = &u.AvatarFocalX
			item.AvatarFocalY = &u.AvatarFocalY
		}
		for _, g := range groups[u.ID] {
			if g.Shared {
				item.SharedGroups = append(item.SharedGroups, g.Name)
			} else {
				item.OtherGroups = append(item.OtherGroups, g.Name)
			} // group-ok: peopleSearchGroups applies current viewer visibility
		}
		out.People = append(out.People, item)
	}
	out.State = "results"
	if len(out.People) == 0 {
		out.State = "no_results"
	}
	c.JSON(http.StatusOK, out)
}

type peopleSearchGroup struct {
	Name   string
	Shared bool
}

// One bounded lateral query for all ten subjects, at most ten visible labels
// per subject. Filtering before LIMIT prevents hidden groups crowding out safe
// labels; the pure policy check below remains the disclosure authority.
func (a *App) peopleSearchGroups(db *gorm.DB, actor *models.User, ids []uint) (map[uint][]peopleSearchGroup, error) {
	out := map[uint][]peopleSearchGroup{}
	if len(ids) == 0 {
		return out, nil
	}
	type row struct {
		SubjectID  uint
		Name       string
		Status     string
		Visibility string
		Shared     bool
		Leader     bool
	}
	var rows []row
	err := db.Raw(`SELECT subjects.id AS subject_id, labels.* FROM users subjects CROSS JOIN LATERAL (
 SELECT g.name, g.status, g.visibility, COALESCE(viewer.status='active',false) AS shared,
 COALESCE(viewer.status='active' AND viewer.role='leader',false) AS leader
 FROM memberships target JOIN groups g ON g.id=target.group_id
 LEFT JOIN memberships viewer ON viewer.group_id=g.id AND viewer.user_id=?
 WHERE target.user_id=subjects.id AND target.status='active' AND g.deleted_at IS NULL
 AND (? OR (g.status='active' AND (g.visibility IN ('public','private') OR viewer.status='active')) OR (viewer.status='active' AND viewer.role='leader'))
 ORDER BY shared DESC, lower(g.name), g.id LIMIT 10
 ) labels WHERE subjects.id IN ?`, actor.ID, asPerson(actor).Admin, ids).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if policy.CanSeeGroup(asPerson(actor), policy.Group{Status: r.Status, Visibility: r.Visibility}, policy.Rel{Member: r.Shared, Leader: r.Leader}) {
			out[r.SubjectID] = append(out[r.SubjectID], peopleSearchGroup{r.Name, r.Shared})
		}
	} // group-ok: pure visibility check before labels leave the API
	return out, nil
}
