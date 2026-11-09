package main

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Admin: guardian links (M1.9 slice 1, DEC-32). A child or teen added to
// a household waits here; only an admin confirms it, with a note on how
// it was checked. There is no auto-confirm path.

type guardianLinkJSON struct {
	ID             uint       `json:"id"`
	GuardianUserID uint       `json:"guardianUserId"`
	GuardianName   string     `json:"guardianName"`
	TeenUserID     *uint      `json:"teenUserId,omitempty"`
	PersonID       *uint      `json:"personId,omitempty"`
	PersonName     string     `json:"personName"`
	PersonKind     string     `json:"personKind"` // child | teen
	BirthDate      string     `json:"birthDate,omitempty"`
	HouseholdID    uint       `json:"householdId,omitempty"`
	HouseholdName  string     `json:"householdName,omitempty"`
	Status         string     `json:"status"` // pending | confirmed | rejected | revoked
	CheckNote      string     `json:"checkNote"`
	ConfirmedBy    *uint      `json:"confirmedBy,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	DecidedAt      *time.Time `json:"decidedAt,omitempty"`
}

func (a *App) toGuardianLinkJSON(link *models.GuardianLink) (guardianLinkJSON, error) {
	out, err := a.guardianLinksJSON([]models.GuardianLink{*link})
	if err != nil {
		return guardianLinkJSON{}, err
	}
	return out[0], nil
}

func (a *App) guardianLinksJSON(links []models.GuardianLink) ([]guardianLinkJSON, error) {
	out := make([]guardianLinkJSON, 0, len(links))
	if len(links) == 0 {
		return out, nil
	}
	var userIDs, personIDs []uint
	for _, link := range links {
		userIDs = append(userIDs, link.GuardianUserID)
		if link.TeenUserID != nil {
			userIDs = append(userIDs, *link.TeenUserID)
		}
		if link.PersonID != nil {
			personIDs = append(personIDs, *link.PersonID)
		}
	}
	var users []models.User
	if err := a.DB.Unscoped().Where("id IN ?", userIDs).Find(&users).Error; err != nil {
		return nil, err
	}
	userByID := make(map[uint]models.User, len(users))
	for _, user := range users {
		userByID[user.ID] = user
	}
	var people []models.HouseholdPerson
	if len(personIDs) > 0 {
		if err := a.DB.Unscoped().Where("id IN ?", personIDs).Find(&people).Error; err != nil {
			return nil, err
		}
	}
	personByID := make(map[uint]models.HouseholdPerson, len(people))
	var householdIDs []uint
	for _, person := range people {
		personByID[person.ID] = person
		householdIDs = append(householdIDs, person.HouseholdID)
	}
	var households []models.Household
	if len(householdIDs) > 0 {
		if err := a.DB.Where("id IN ?", householdIDs).Find(&households).Error; err != nil {
			return nil, err
		}
	}
	householdByID := make(map[uint]models.Household, len(households))
	for _, household := range households {
		householdByID[household.ID] = household
	}
	for _, link := range links {
		out = append(out, presentGuardianLink(link, userByID, personByID, householdByID))
	}
	return out, nil
}

func presentGuardianLink(link models.GuardianLink, users map[uint]models.User, people map[uint]models.HouseholdPerson, households map[uint]models.Household) guardianLinkJSON {
	name := func(id uint) string {
		if user, ok := users[id]; ok {
			return user.PublicName()
		}
		return unknownPersonName
	}
	out := guardianLinkJSON{
		ID: link.ID, GuardianUserID: link.GuardianUserID, GuardianName: name(link.GuardianUserID),
		TeenUserID: link.TeenUserID, PersonID: link.PersonID, Status: link.Status,
		CheckNote: link.CheckNote, ConfirmedBy: link.ConfirmedBy,
		CreatedAt: link.CreatedAt, DecidedAt: link.DecidedAt,
	}
	if link.PersonID != nil {
		if person, ok := people[*link.PersonID]; ok {
			out.PersonName, out.PersonKind = person.FullName(), person.Kind
			out.BirthDate, out.HouseholdID = formatBirthDate(person.BirthDate), person.HouseholdID
			out.HouseholdName = households[person.HouseholdID].Name
		}
	} else if link.TeenUserID != nil {
		out.PersonName, out.PersonKind = name(*link.TeenUserID), models.HouseholdTeen
		out.BirthDate = formatBirthDate(users[*link.TeenUserID].BirthDate)
	}
	return out
}

func (a *App) apiListGuardianLinks(c *gin.Context) {
	status := c.DefaultQuery("status", models.GuardianPending)
	q := a.DB.Order("created_at, id")
	switch status {
	case models.GuardianPending, models.GuardianConfirmed, models.GuardianRejected, models.GuardianRevoked:
		q = q.Where("status = ?", status)
	case "all":
	default:
		jsonError(c, http.StatusBadRequest, "unknown status")
		return
	}
	var rows []models.GuardianLink
	if err := q.Find(&rows).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list guardian links")
		return
	}
	out, err := a.guardianLinksJSON(rows)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not list guardian links")
		return
	}
	nominations, err := unresolvedGuardianNominations(a.DB)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load guardian nominations")
		return
	}
	c.JSON(http.StatusOK, guardianLinksResponseJSON{GuardianLinks: out, Nominations: nominations})
}

func (a *App) apiConfirmGuardianLink(c *gin.Context) { a.decideGuardianLink(c, true) }
func (a *App) apiRejectGuardianLink(c *gin.Context)  { a.decideGuardianLink(c, false) }

// decideGuardianLink confirms or rejects a pending guardian link,
// conditional on it still being pending, and resolves every admin's task
// in the same transaction (5.12). Confirming requires a check note
// saying how the admin checked (DEC-32); "note" is accepted for the same
// field, like the other approval endpoints.
func (a *App) decideGuardianLink(c *gin.Context, confirm bool) {
	id, _ := strconv.Atoi(c.Param("id"))
	var link models.GuardianLink
	if err := a.DB.First(&link, id).Error; err != nil {
		jsonError(c, http.StatusNotFound, "guardian link not found")
		return
	}
	me := apiUser(c)
	if !policy.CanConfirmGuardianLink(asPerson(me)) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return
	}
	var body struct {
		CheckNote string `json:"checkNote"`
		Note      string `json:"note"`
	}
	if !bindOptionalJSON(c, &body) {
		return
	}
	note := strings.TrimSpace(body.CheckNote)
	if note == "" {
		note = strings.TrimSpace(body.Note)
	}
	if confirm && note == "" {
		fail(c, errCheckNoteRequired)
		return
	}
	if len(note) > 500 {
		fail(c, errCheckNoteTooLong)
		return
	}
	// DEC-79: an admin cannot confirm their own guardian link while another
	// active admin exists; a sole admin may, and the audit marks it.
	selfConfirm := confirm && link.GuardianUserID == me.ID
	status, outcome, auditAction := models.GuardianRejected, "rejected", audit.GuardianLinkRejected
	if confirm {
		status, outcome, auditAction = models.GuardianConfirmed, "approved", audit.GuardianLinkConfirmed
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if link.TeenUserID != nil {
			users, err := lockTeenLifecycleUsers(tx, *link.TeenUserID, me.ID, link.GuardianUserID)
			if err != nil {
				return err
			}
			var validAdmin, validGuardian, validTeen bool
			for _, u := range users {
				if u.ID == me.ID {
					validAdmin = sessionCurrent(u, me) && policy.CanConfirmGuardianLink(asPerson(u))
				}
				if u.ID == link.GuardianUserID {
					validGuardian = u.Status == models.UserActive && policy.CanRequestGuardianLink(asPerson(u))
				}
				if u.ID == *link.TeenUserID {
					validTeen = u.AccountType == models.AccountTeen
				}
			}
			if !validAdmin || (confirm && (!validGuardian || !validTeen)) {
				return errConsentForbidden
			}
		}
		if link.PersonID != nil {
			// The teen branch rechecks the admin under the gate and the user
			// locks; so does this one, before the household is locked.
			if err := lockContacts(tx); err != nil {
				return err
			}
			if _, err := lockAdminIn(tx, me, actorShare); err != nil {
				return err
			}
			var person models.HouseholdPerson
			if err := tx.First(&person, *link.PersonID).Error; err != nil {
				return err
			}
			if err := lockHousehold(tx, person.HouseholdID); err != nil {
				return err
			}
			if err := tx.First(&person, *link.PersonID).Error; err != nil {
				return err
			}
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&link, link.ID).Error; err != nil {
			return err
		}
		if selfConfirm {
			var otherAdmins int64
			if err := activeAdmins(tx).Where("id <> ?", me.ID).Count(&otherAdmins).Error; err != nil {
				return err
			}
			if otherAdmins > 0 {
				return errSelfConfirmBlocked
			}
		}
		now := time.Now().UTC()
		updates := map[string]any{"status": status, "decided_at": now, "check_note": note}
		if confirm {
			updates["confirmed_by"] = me.ID
		}
		if err := decidePendingIn(tx, &models.GuardianLink{}, link.ID, models.GuardianPending, updates); err != nil {
			return err
		}
		if _, err := notify.Resolve(tx, notify.HouseholdLinkPending, link.ID, &me.ID, outcome); err != nil {
			return err
		}
		meta := map[string]any{"guardianUserId": link.GuardianUserID, "checkNote": note}
		if selfConfirm {
			meta["selfConfirm"] = true
		}
		if link.PersonID != nil {
			meta["personId"] = *link.PersonID
		}
		if link.TeenUserID != nil {
			meta["teenUserId"] = *link.TeenUserID
		}
		if confirm && link.TeenUserID != nil {
			var teen models.User
			if err := tx.First(&teen, *link.TeenUserID).Error; err != nil {
				return err
			}
			today, err := churchClockIn(tx, time.Now().UTC())
			if err != nil {
				return err
			}
			if err := a.completeTeenConversionIfReady(tx, &teen, today, c.ClientIP()); err != nil {
				return err
			}
			if _, err := guardianConsentMailType.Enqueue(tx, guardianConsentMailJob{GuardianLinkID: link.ID}, time.Now(), ""); err != nil {
				return err
			}
		}
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: auditAction,
			TargetType: "guardian_link", TargetID: link.ID, Meta: meta, IP: c.ClientIP()})
	})
	if a.answerAlreadyHandled(c, err, notify.HouseholdLinkPending, link.ID) {
		return
	}
	if err != nil {
		fail(c, err)
		return
	}
	var fresh models.GuardianLink
	a.DB.First(&fresh, link.ID)
	linkJSON, err := a.toGuardianLinkJSON(&fresh)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load the guardian link")
		return
	}
	c.JSON(http.StatusOK, gin.H{"guardianLink": linkJSON})
}
