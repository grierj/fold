package main

import (
	"errors"
	"net/http"
	"strconv"

	"fold/internal/audit"
	"fold/internal/policy"
	"fold/models"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var (
	errHouseholdHolder   = apiErr(http.StatusConflict, "household_holder", "choose another active adult account to hold this household")
	errHouseholdExists   = apiErr(http.StatusConflict, "household_exists", "this account already has a household; use its existing household")
	errHouseholdRequired = apiErr(http.StatusConflict, "household_required", "create this account's household before adding people")
	errHouseholdGuardian = apiErr(http.StatusConflict, "household_guardian", "choose an active adult account in this household as guardian, not the acting admin")
)

func (a *App) adminHouseholdError(c *gin.Context, err error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		jsonError(c, http.StatusNotFound, "user not found")
		return
	}
	fail(c, err)
}

func adminHouseholdHolderID(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		jsonError(c, http.StatusNotFound, "user not found")
		return 0, false
	}
	return uint(id), true
}

// lockAdminHouseholdParticipants follows contact gate -> accounts (ID order)
// -> household. Authority is checked again after the locks, not from the cookie.
func lockAdminHouseholdParticipants(tx *gorm.DB, session *models.User, holderID, guardianID uint) (*models.User, *models.User, *models.User, error) {
	if err := lockContacts(tx); err != nil {
		return nil, nil, nil, err
	}
	byID, err := lockUsersByID(tx, []uint{session.ID, holderID, guardianID}, false)
	if err != nil {
		return nil, nil, nil, err
	}
	actor, holder, guardian := byID[session.ID], byID[holderID], byID[guardianID]
	if !adminCurrent(actor, session) {
		return nil, nil, nil, errSecuritySession
	}
	if holder == nil {
		return nil, nil, nil, gorm.ErrRecordNotFound
	}
	if !policy.CanDelegateHousehold(asPerson(actor), asPerson(holder), holder.CanSignIn()) {
		return nil, nil, nil, errHouseholdHolder
	}
	return actor, holder, guardian, nil
}

func (a *App) apiAdminGetHousehold(c *gin.Context) {
	id, ok := adminHouseholdHolderID(c)
	if !ok {
		return
	}
	var holder models.User
	if err := a.DB.First(&holder, id).Error; err != nil {
		a.adminHouseholdError(c, err)
		return
	}
	if !policy.CanDelegateHousehold(asPerson(apiUser(c)), asPerson(&holder), holder.CanSignIn()) {
		a.adminHouseholdError(c, errHouseholdHolder)
		return
	}
	var account models.HouseholdAccount
	if err := a.DB.Where("user_id = ?", holder.ID).First(&account).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusOK, gin.H{"household": nil})
		return
	} else if err != nil {
		a.adminHouseholdError(c, err)
		return
	}
	var household models.Household
	if err := a.DB.First(&household, account.HouseholdID).Error; err != nil {
		a.adminHouseholdError(c, err)
		return
	}
	a.adminHouseholdResponse(c, &household, http.StatusOK)
}

func (a *App) adminHouseholdResponse(c *gin.Context, household *models.Household, status int) {
	out, err := a.toHouseholdJSON(household, apiUser(c))
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load household")
		return
	}
	c.JSON(status, gin.H{"household": out})
}

// Creation is explicit and atomic. Unlike first-use self-service, an existing
// account link is a conflict, never permission to silently reuse or move it.
func (a *App) apiAdminCreateHousehold(c *gin.Context) {
	id, ok := adminHouseholdHolderID(c)
	if !ok {
		return
	}
	var body struct {
		Name    *string `json:"name"`
		Address string  `json:"address"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Name != nil {
		name, err := requiredName(*body.Name)
		if err != nil {
			fail(c, err)
			return
		}
		body.Name = &name
	}
	address, err := postalAddress(body.Address)
	if err != nil {
		fail(c, err)
		return
	}
	var household models.Household
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		actor, holder, _, err := lockAdminHouseholdParticipants(tx, apiUser(c), id, 0)
		if err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&models.HouseholdAccount{}).Where("user_id = ?", holder.ID).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return errHouseholdExists
		}
		household = models.Household{Name: defaultHouseholdName(holder), Address: address, CreatedBy: actor.ID}
		if body.Name != nil {
			household.Name = *body.Name
		}
		if err := tx.Create(&household).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.HouseholdAccount{HouseholdID: household.ID, UserID: holder.ID, Relation: models.HouseholdRelationAdult}).Error; err != nil {
			if emailTaken(err) {
				return errHouseholdExists
			}
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &actor.ID, Action: audit.HouseholdCreated, TargetType: "household", TargetID: household.ID,
			Meta: map[string]any{"holderUserId": holder.ID}, IP: c.ClientIP()})
	})
	if err != nil {
		a.adminHouseholdError(c, err)
		return
	}
	a.adminHouseholdResponse(c, &household, http.StatusCreated)
}

func (a *App) apiAdminAddHouseholdPerson(c *gin.Context) {
	id, ok := adminHouseholdHolderID(c)
	if !ok {
		return
	}
	var body struct {
		householdPersonInput
		GuardianUserID uint `json:"guardianUserId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	person, ok := a.parseHouseholdPersonInput(c, body.householdPersonInput)
	if !ok {
		return
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		actor, holder, guardian, err := lockAdminHouseholdParticipants(tx, apiUser(c), id, body.GuardianUserID)
		if err != nil {
			return err
		}
		var account models.HouseholdAccount
		if err := tx.Where("user_id = ?", holder.ID).First(&account).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return errHouseholdRequired
		} else if err != nil {
			return err
		}
		if err := lockHousehold(tx, account.HouseholdID); err != nil {
			return err
		}
		if !householdRelOf(account, true).Adult {
			return errHouseholdHolder
		}
		if policy.NeedsGuardianLink(person.Kind) {
			var guardianAccount models.HouseholdAccount
			if err := tx.Where("user_id = ? AND household_id = ?", body.GuardianUserID, account.HouseholdID).Limit(1).Find(&guardianAccount).Error; err != nil {
				return err
			}
			if !policy.CanChooseHouseholdGuardian(asPerson(actor), asPerson(guardian), householdRelOf(guardianAccount, guardianAccount.ID != 0), guardian.CanSignIn()) {
				return errHouseholdGuardian
			}
		}
		person.HouseholdID, person.CreatedBy = account.HouseholdID, actor.ID
		if err := tx.Create(&person).Error; err != nil {
			return err
		}
		meta := map[string]any{"personId": person.ID, "kind": person.Kind, "holderUserId": holder.ID}
		if policy.NeedsGuardianLink(person.Kind) {
			link, err := createGuardianLink(tx, guardian.ID, &person)
			if err != nil {
				return err
			}
			meta["guardianLinkId"], meta["guardianUserId"] = link.ID, guardian.ID
		}
		return audit.Write(tx, audit.Entry{Actor: &actor.ID, Action: audit.HouseholdPersonAdded, TargetType: "household", TargetID: account.HouseholdID, Meta: meta, IP: c.ClientIP()})
	})
	if err != nil {
		a.adminHouseholdError(c, err)
		return
	}
	row, err := a.householdPersonRow(a.DB, &person, apiUser(c))
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load that person")
		return
	}
	c.JSON(http.StatusCreated, householdPersonResponseJSON{Person: row})
}
