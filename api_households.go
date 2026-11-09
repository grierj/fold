package main

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"fold/internal/accounts"
	"fold/internal/audit"
	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// My household (M1.9 slice 1, 5.3, 5.10, DEC-32). An account gets a
// household on first use, named after the account's last name. Any adult
// account in the household manages it (policy.CanManageHousehold).
// Adding a child or teen writes a pending guardian link and asks the
// admins to confirm it; an adult needs no approval.

type householdPersonJSON struct {
	Conversion              *conversionJSON `json:"conversion,omitempty"`
	CanConvert              bool            `json:"canConvert"`
	ID                      uint            `json:"id"`
	HouseholdID             uint            `json:"householdId"`
	FirstName               string          `json:"firstName"`
	LastName                string          `json:"lastName"`
	Name                    string          `json:"name"`
	Kind                    string          `json:"kind"` // child | teen | adult
	BirthDate               string          `json:"birthDate,omitempty"`
	CareNotes               *string         `json:"careNotes,omitempty"`
	RegistrationEligibility string          `json:"registrationEligibility"`
	Age                     *int            `json:"age,omitempty"`
	PhotoConsent            bool            `json:"photoConsent"`
	LinkStatus              string          `json:"linkStatus"` // pending | confirmed | rejected | revoked | none
	GuardianLinkID          *uint           `json:"guardianLinkId,omitempty"`
	ConvertedUserID         *uint           `json:"convertedUserId,omitempty"`
	CreatedBy               uint            `json:"createdBy"`
}

type householdAccountJSON struct {
	UserID   uint   `json:"userId"`
	Name     string `json:"name"`
	Relation string `json:"relation"` // guardian | adult | teen
	Status   string `json:"status"`
}

type householdJSON struct {
	ID        uint                   `json:"id"`
	Name      string                 `json:"name"`
	Address   string                 `json:"address"`
	CreatedBy uint                   `json:"createdBy"`
	People    []householdPersonJSON  `json:"people"`
	Accounts  []householdAccountJSON `json:"accounts"`
}

// householdAccountIn loads a user's household account row in db (their
// only one: user_id is unique).
func householdAccountIn(db *gorm.DB, userID uint) (models.HouseholdAccount, bool) {
	var acct models.HouseholdAccount
	if err := db.Where("user_id = ?", userID).First(&acct).Error; err != nil {
		return acct, false
	}
	return acct, true
}

func householdRelOf(acct models.HouseholdAccount, found bool) policy.HouseholdRel {
	return policy.HouseholdRel{
		Account: found,
		Adult:   found && (acct.Relation == models.HouseholdRelationGuardian || acct.Relation == models.HouseholdRelationAdult),
	}
}

func defaultHouseholdName(u *models.User) string {
	name := strings.TrimSpace(u.Name)
	if name == "" {
		name = strings.TrimSpace(u.DisplayName)
	}
	fields := strings.Fields(name)
	if len(fields) == 0 {
		return "My household"
	}
	return fields[len(fields)-1] + " household"
}

var (
	// errHouseholdForbidden is a failed policy.CanManageHousehold or
	// CanCreateHousehold check made under the user lock.
	errHouseholdForbidden = apiErr(http.StatusForbidden, "", "forbidden")
	// errHouseholdPersonGone means the person is not in the household the
	// locked seat names.
	errHouseholdPersonGone = apiErr(http.StatusNotFound, "", "household person not found")
)

// lockHouseholdSeat is the first lock of every household self-service writer:
// the acting user row FOR NO KEY UPDATE (lockActorIn), taken before
// lockHousehold (user, then household, as the erasure,
// lockConversionParticipants and lockAdminHouseholdParticipants do), plus the
// seat and the policy check, both read under the lock. It takes no contact
// gate; a writer that takes one takes it before this. The caller uses the
// returned row, never the preloaded one, for every later fact. noSeat is the
// error for an account with no seat: forbidden for the household routes, not
// found for the person routes.
func lockHouseholdSeat(tx *gorm.DB, me *models.User, noSeat error) (*models.User, models.HouseholdAccount, error) {
	fresh, err := lockActorIn(tx, me, actorNoKeyUpdate)
	if err != nil {
		return nil, models.HouseholdAccount{}, err
	}
	seat, found := householdAccountIn(tx, fresh.ID)
	if !found {
		return nil, seat, noSeat
	}
	if !policy.CanManageHousehold(asPerson(fresh), householdRelOf(seat, true)) {
		return nil, seat, errHouseholdForbidden
	}
	return fresh, seat, nil
}

// ensureHousehold loads the user's household, creating it on first use
// (audited household.created). It answers and reports false on failure.
func (a *App) ensureHousehold(c *gin.Context, u *models.User) (*models.Household, models.HouseholdAccount, bool) {
	if acct, found := householdAccountIn(a.DB, u.ID); found {
		var household models.Household
		if err := a.DB.First(&household, acct.HouseholdID).Error; err != nil {
			jsonError(c, http.StatusInternalServerError, "could not load household")
			return nil, acct, false
		}
		return &household, acct, true
	}
	if !policy.CanCreateHousehold(asPerson(u)) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return nil, models.HouseholdAccount{}, false
	}
	var household models.Household
	var acct models.HouseholdAccount
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		// The user lock comes first. Under it the account is still live, a
		// concurrent first use has either committed its seat (returned here)
		// or waits for this one, and the name comes from the locked row.
		fresh, err := lockActorIn(tx, u, actorNoKeyUpdate)
		if err != nil {
			return err
		}
		if seat, found := householdAccountIn(tx, fresh.ID); found {
			acct, household = seat, models.Household{}
			return tx.First(&household, seat.HouseholdID).Error
		}
		if !policy.CanCreateHousehold(asPerson(fresh)) {
			return errHouseholdForbidden
		}
		household = models.Household{Name: defaultHouseholdName(fresh), CreatedBy: fresh.ID}
		if err := tx.Create(&household).Error; err != nil {
			return err
		}
		acct = models.HouseholdAccount{HouseholdID: household.ID, UserID: fresh.ID, Relation: models.HouseholdRelationGuardian}
		if err := tx.Create(&acct).Error; err != nil {
			return err
		}
		return audit.Write(tx, audit.Entry{Actor: &fresh.ID, Action: audit.HouseholdCreated,
			TargetType: "household", TargetID: household.ID, IP: c.ClientIP()})
	})
	if isAPIError(err) {
		fail(c, err)
		return nil, models.HouseholdAccount{}, false
	}
	if err != nil {
		// A concurrent first use may have won the unique user row.
		if acct, found := householdAccountIn(a.DB, u.ID); found {
			var household models.Household
			if ferr := a.DB.First(&household, acct.HouseholdID).Error; ferr == nil {
				return &household, acct, true
			}
		}
		jsonError(c, http.StatusInternalServerError, "could not create household")
		return nil, acct, false
	}
	return &household, acct, true
}

// manageMyHousehold is ensureHousehold plus policy.CanManageHousehold:
// a teen household account sees the household exists but manages none of
// it (403, section 6).
func (a *App) manageMyHousehold(c *gin.Context) (*models.Household, bool) {
	u := apiUser(c)
	household, acct, ok := a.ensureHousehold(c, u)
	if !ok {
		return nil, false
	}
	if !policy.CanManageHousehold(asPerson(u), householdRelOf(acct, true)) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return nil, false
	}
	return household, true
}

// loadGuardianLinks reads the guardian links of personIDs by person; a partial
// map is never returned alongside an error.
func loadGuardianLinks(db *gorm.DB, personIDs []uint) (map[uint][]models.GuardianLink, error) {
	out := map[uint][]models.GuardianLink{}
	if len(personIDs) == 0 {
		return out, nil
	}
	var links []models.GuardianLink
	if err := db.Where("person_id IN ?", personIDs).Order("id").Find(&links).Error; err != nil {
		return nil, err
	}
	for _, l := range links {
		if l.PersonID != nil {
			out[*l.PersonID] = append(out[*l.PersonID], l)
		}
	}
	return out, nil
}

// effectiveLink is a person's link status for display: a confirmed link
// wins, then a pending one (still waiting), then the latest decided one.
// No link at all is "none" (an adult, who needs no approval).
func effectiveLink(links []models.GuardianLink) (string, *uint) {
	if len(links) == 0 {
		return "none", nil
	}
	for _, status := range []string{models.GuardianConfirmed, models.GuardianPending} {
		for _, l := range links {
			if l.Status == status {
				id := l.ID
				return status, &id
			}
		}
	}
	last := links[len(links)-1]
	return last.Status, &last.ID
}

func (a *App) toHouseholdPersonJSON(person *models.HouseholdPerson, links []models.GuardianLink, viewer *models.User, batch *conversionBatch) (householdPersonJSON, error) {
	status, linkID := effectiveLink(links)
	if person.Kind == models.HouseholdAdult {
		// An adult needs no approval, whatever links a former kind left.
		status, linkID = "none", nil
	}
	var notes *string
	rel := policy.HouseholdRel{}
	if viewer != nil {
		for _, link := range links {
			if link.GuardianUserID == viewer.ID && link.Status == models.GuardianConfirmed {
				rel.Guardian = true
			}
		}
	}
	facts := householdFacts(person, links, batch.today)
	if policy.CanSeeCareNotes(asPerson(viewer), facts, rel, nil, time.Now().UTC()) {
		notes = &person.CareNotes
	}
	eligibility := policy.HouseholdRegistrationProblem(facts)
	if eligibility == "" {
		eligibility = "eligible"
	} else {
		eligibility = strings.TrimPrefix(eligibility, "household_")
	}
	if person.ConvertedUserID != nil {
		eligibility = "converted"
	}
	canConvert, err := batch.allowed(person)
	if errors.Is(err, errConversionHolder) {
		canConvert, err = false, nil
	}
	if err != nil {
		return householdPersonJSON{}, err
	}
	return householdPersonJSON{
		CanConvert:              canConvert,
		RegistrationEligibility: eligibility, Age: facts.AgeYears,
		ID: person.ID, HouseholdID: person.HouseholdID,
		FirstName: person.FirstName, LastName: person.LastName, Name: person.FullName(),
		Kind: person.Kind, BirthDate: formatBirthDate(person.BirthDate),
		CareNotes: notes, PhotoConsent: person.PhotoConsent,
		LinkStatus: status, GuardianLinkID: linkID,
		ConvertedUserID: person.ConvertedUserID, CreatedBy: person.CreatedBy,
	}, nil
}

func (a *App) toHouseholdJSON(household *models.Household, viewer *models.User) (householdJSON, error) {
	out := householdJSON{
		ID: household.ID, Name: household.Name, Address: household.Address,
		CreatedBy: household.CreatedBy,
		People:    []householdPersonJSON{}, Accounts: []householdAccountJSON{},
	}
	var people []models.HouseholdPerson
	if err := a.DB.Where("household_id = ? AND converted_user_id IS NULL", household.ID).Order("id").Find(&people).Error; err != nil {
		return out, err
	}
	ids := make([]uint, 0, len(people))
	for i := range people {
		ids = append(ids, people[i].ID)
	}
	links, err := loadGuardianLinks(a.DB, ids)
	if err != nil {
		return out, err
	}
	var accounts []models.HouseholdAccount
	if err := a.DB.Where("household_id = ?", household.ID).Order("id").Find(&accounts).Error; err != nil {
		return out, err
	}
	var rel policy.HouseholdRel
	for _, acct := range accounts {
		if viewer != nil && acct.UserID == viewer.ID {
			rel = householdRelOf(acct, true)
		}
	}
	batch, err := a.loadConversionBatchFor(a.DB, viewer, rel, ids, links)
	if err != nil {
		return out, err
	}
	var invitations []models.Invitation
	if len(ids) > 0 {
		if err := a.DB.Where("household_person_id IN ? AND accepted_at IS NULL AND revoked_at IS NULL", ids).Find(&invitations).Error; err != nil {
			return out, err
		}
	}
	conversions := map[uint]conversionJSON{}
	for i := range invitations {
		conversions[*invitations[i].HouseholdPersonID] = conversionMetadata(&invitations[i])
	}
	if len(ids) > 0 {
		var requests []models.AccountRequest
		if err := openConversionRequests(a.DB).Where("household_person_id IN ?", ids).Find(&requests).Error; err != nil {
			return out, err
		}
		for _, req := range requests {
			state := conversionAwaitingApproval
			if req.Status == models.RequestApproved {
				state = conversionAwaitingConsent
			}
			conversions[*req.HouseholdPersonID] = conversionJSON{Email: req.Email, ExpiresAt: req.ExpiresAt, State: state}
		}
	}
	for i := range people {
		row, err := a.toHouseholdPersonJSON(&people[i], links[people[i].ID], viewer, batch)
		if err != nil {
			return out, err
		}
		if conversion, ok := conversions[people[i].ID]; ok {
			row.Conversion = &conversion
		}
		out.People = append(out.People, row)
	}
	userIDs := make([]uint, 0, len(accounts))
	for _, acct := range accounts {
		userIDs = append(userIDs, acct.UserID)
	}
	users := map[uint]models.User{}
	if len(userIDs) > 0 {
		var found []models.User
		if err := a.DB.Where("id IN ?", userIDs).Find(&found).Error; err != nil {
			return out, err
		}
		for _, u := range found {
			users[u.ID] = u
		}
	}
	for _, acct := range accounts {
		user, ok := users[acct.UserID]
		if !ok {
			continue
		}
		out.Accounts = append(out.Accounts, householdAccountJSON{
			UserID: acct.UserID, Name: user.PublicName(), Relation: acct.Relation, Status: user.Status,
		})
	}
	return out, nil
}

// createGuardianLink writes a pending link for a child or teen person
// and asks the admins to confirm it, all in tx (DEC-32).
func createGuardianLink(tx *gorm.DB, guardianID uint, person *models.HouseholdPerson) (*models.GuardianLink, error) {
	link := models.GuardianLink{GuardianUserID: guardianID, PersonID: &person.ID, Status: models.GuardianPending}
	if err := tx.Create(&link).Error; err != nil {
		return nil, err
	}
	return &link, notifyGuardianLinkPending(tx, &link, person)
}

// revokeGuardianLinks ends every live (pending or confirmed) link to a
// person in tx: a pending task is resolved withdrawn, and each revoked
// link is audited (DEC-32 rows are never purged).
func revokeGuardianLinks(tx *gorm.DB, c *gin.Context, actor uint, person *models.HouseholdPerson) error {
	var links []models.GuardianLink
	if err := tx.Where("person_id = ? AND status IN ?", person.ID,
		[]string{models.GuardianPending, models.GuardianConfirmed}).Find(&links).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, link := range links {
		res := tx.Model(&models.GuardianLink{}).Where("id = ? AND status = ?", link.ID, link.Status).
			Updates(map[string]any{"status": models.GuardianRevoked, "decided_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			continue
		}
		if link.Status == models.GuardianPending {
			if _, err := notify.Resolve(tx, notify.HouseholdLinkPending, link.ID, &actor, "withdrawn"); err != nil {
				return err
			}
		}
		if err := audit.Write(tx, audit.Entry{Actor: &actor, Action: audit.GuardianLinkRevoked,
			TargetType: "guardian_link", TargetID: link.ID,
			Meta: map[string]any{"guardianUserId": link.GuardianUserID, "personId": person.ID},
			IP:   c.ClientIP()}); err != nil {
			return err
		}
	}
	return nil
}

// apiGetMyHousehold returns the user's household, or {household: null}
// when they have none yet. It is read-only (Warden, PR 106): the household
// is created by PUT /me/household or POST /me/household/people through
// ensureHousehold.
func (a *App) apiGetMyHousehold(c *gin.Context) {
	u := apiUser(c)
	acct, found := householdAccountIn(a.DB, u.ID)
	if !found {
		c.JSON(http.StatusOK, gin.H{"household": nil})
		return
	}
	if !policy.CanManageHousehold(asPerson(u), householdRelOf(acct, true)) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return
	}
	var household models.Household
	if err := a.DB.First(&household, acct.HouseholdID).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load household")
		return
	}
	out, err := a.toHouseholdJSON(&household, u)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load your household")
		return
	}
	c.JSON(http.StatusOK, gin.H{"household": out})
}

func (a *App) apiUpdateMyHousehold(c *gin.Context) {
	household, ok := a.manageMyHousehold(c)
	if !ok {
		return
	}
	var body struct {
		Name    *string `json:"name"`
		Address *string `json:"address"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	updates := map[string]any{}
	if body.Name != nil {
		name, err := requiredName(*body.Name)
		if err != nil {
			fail(c, err)
			return
		}
		updates["name"] = name
	}
	if body.Address != nil {
		address, err := postalAddress(*body.Address)
		if err != nil {
			fail(c, err)
			return
		}
		updates["address"] = address
	}
	if len(updates) > 0 {
		// The household written is the one the locked seat names, which
		// may differ from the one loaded before the transaction.
		var householdID uint
		if err := a.DB.Transaction(func(tx *gorm.DB) error {
			fresh, seat, err := lockHouseholdSeat(tx, apiUser(c), errHouseholdForbidden)
			if err != nil {
				return err
			}
			householdID = seat.HouseholdID
			if err := lockHousehold(tx, householdID); err != nil {
				return err
			}
			if err := tx.Model(&models.Household{}).Where("id = ?", householdID).Updates(updates).Error; err != nil {
				return err
			}
			fields := []string{}
			for key := range updates {
				fields = append(fields, key)
			}
			sort.Strings(fields)
			return audit.Write(tx, audit.Entry{Actor: &fresh.ID, Action: audit.HouseholdUpdated, TargetType: "household", TargetID: householdID, Meta: map[string]any{"fields": fields}, IP: c.ClientIP()})
		}); err != nil {
			fail(c, err)
			return
		}
		var saved models.Household
		if err := a.DB.First(&saved, householdID).Error; err != nil {
			jsonError(c, http.StatusInternalServerError, "could not load household")
			return
		}
		household = &saved
	}
	out, err := a.toHouseholdJSON(household, apiUser(c))
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load your household")
		return
	}
	c.JSON(http.StatusOK, gin.H{"household": out})
}

// householdPersonInput is the add/edit body. Fields are pointers so an
// edit can tell "not provided" from "set to empty". Name is a fallback
// for clients that send one field: it splits into first and last.
type householdPersonInput struct {
	FirstName    *string `json:"firstName"`
	LastName     *string `json:"lastName"`
	Name         *string `json:"name"`
	Kind         *string `json:"kind"`
	BirthDate    *string `json:"birthDate"`
	CareNotes    *string `json:"careNotes"`
	PhotoConsent *bool   `json:"photoConsent"`
}

func validHouseholdKind(kind string) bool {
	return kind == models.HouseholdChild || kind == models.HouseholdTeen || kind == models.HouseholdAdult
}

func splitPersonName(name string) (string, string) {
	fields := strings.Fields(strings.TrimSpace(name))
	if len(fields) == 0 {
		return "", ""
	}
	if len(fields) == 1 {
		return fields[0], ""
	}
	return fields[0], strings.Join(fields[1:], " ")
}

// householdPersonFields validates the person fields a request names and
// returns them as column updates: first_name, last_name, kind, birth_date (nil
// clears it), care_notes, photo_consent. Create and update share it, so a
// person is held to the same rules either way; only the fields a request
// carries are returned.
func householdPersonFields(body householdPersonInput) (map[string]any, error) {
	if body.Name != nil && body.FirstName == nil && body.LastName == nil {
		first, last := splitPersonName(*body.Name)
		body.FirstName, body.LastName = &first, &last
	}
	fields := map[string]any{}
	if body.FirstName != nil {
		first, err := personNameField.clean(*body.FirstName)
		if first == "" {
			return nil, errFirstNameRequired
		}
		if err != nil {
			return nil, err
		}
		fields["first_name"] = first
	}
	if body.LastName != nil {
		last, err := optionalName(*body.LastName)
		if err != nil {
			return nil, err
		}
		fields["last_name"] = last
	}
	if body.Kind != nil {
		kind := strings.TrimSpace(*body.Kind)
		if !validHouseholdKind(kind) {
			return nil, errInvalidKind
		}
		fields["kind"] = kind
	}
	if body.BirthDate != nil {
		raw := strings.TrimSpace(*body.BirthDate)
		if raw == "" {
			fields["birth_date"] = (*time.Time)(nil)
		} else {
			parsed, err := accounts.ParseBirthDate(raw)
			if err != nil {
				return nil, errInvalidBirthDate
			}
			fields["birth_date"] = parsed
		}
	}
	if body.CareNotes != nil {
		notes, err := careNotesField.clean(*body.CareNotes)
		if err != nil {
			return nil, err
		}
		fields["care_notes"] = notes
	}
	if body.PhotoConsent != nil {
		fields["photo_consent"] = *body.PhotoConsent
	}
	return fields, nil
}

// householdAgeFits is the age rule for a household person: a birth date is
// never in the future, and an adult is at least 18.
func householdAgeFits(kind string, birth *time.Time, today time.Time) bool {
	age := accounts.AgeOn(*birth, today)
	return age >= 0 && !(kind == models.HouseholdAdult && age < 18)
}

func (a *App) parseHouseholdPersonInput(c *gin.Context, body householdPersonInput) (models.HouseholdPerson, bool) {
	fields, err := householdPersonFields(body)
	if err == nil {
		if _, named := fields["first_name"]; !named {
			err = errFirstNameRequired
		} else if _, ok := fields["kind"]; !ok {
			err = errInvalidKind
		}
	}
	if err != nil {
		fail(c, err)
		return models.HouseholdPerson{}, false
	}
	person := models.HouseholdPerson{FirstName: fields["first_name"].(string), Kind: fields["kind"].(string)}
	person.LastName, _ = fields["last_name"].(string)
	person.CareNotes, _ = fields["care_notes"].(string)
	person.PhotoConsent, _ = fields["photo_consent"].(bool)
	person.BirthDate, _ = fields["birth_date"].(*time.Time)
	if person.BirthDate != nil {
		today, err := churchClockIn(a.DB, time.Now().UTC())
		if err != nil {
			jsonError(c, http.StatusInternalServerError, "could not check the birth date")
			return models.HouseholdPerson{}, false
		}
		if !householdAgeFits(person.Kind, person.BirthDate, today) {
			fail(c, errHouseholdAgeMismatch)
			return models.HouseholdPerson{}, false
		}
	}
	return person, true
}

// apiAddHouseholdPerson adds someone to the user's household (created on
// first use). A child or teen also gets a pending guardian link and the
// admins a waiting task (DEC-32); an adult needs no approval.
func (a *App) apiAddHouseholdPerson(c *gin.Context) {
	if _, ok := a.manageMyHousehold(c); !ok {
		return
	}
	me := apiUser(c)
	var body householdPersonInput
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	person, ok := a.parseHouseholdPersonInput(c, body)
	if !ok {
		return
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		// User lock first, then the household the locked seat names.
		fresh, seat, err := lockHouseholdSeat(tx, me, errHouseholdForbidden)
		if err != nil {
			return err
		}
		if err := lockHousehold(tx, seat.HouseholdID); err != nil {
			return err
		}
		person.HouseholdID, person.CreatedBy = seat.HouseholdID, fresh.ID
		if err := tx.Create(&person).Error; err != nil {
			return err
		}
		meta := map[string]any{"personId": person.ID, "kind": person.Kind}
		if policy.NeedsGuardianLink(person.Kind) {
			link, err := createGuardianLink(tx, fresh.ID, &person)
			if err != nil {
				return err
			}
			meta["guardianLinkId"] = link.ID
		}
		return audit.Write(tx, audit.Entry{Actor: &fresh.ID, Action: audit.HouseholdPersonAdded,
			TargetType: "household", TargetID: seat.HouseholdID, Meta: meta, IP: c.ClientIP()})
	})
	if err != nil {
		fail(c, err)
		return
	}
	row, err := a.householdPersonRow(a.DB, &person, me)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load that person")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"person": row})
}

// loadMyHouseholdPerson loads a person in the user's household for an
// edit or removal: 404 when they have no household or the person is not
// in it, 403 when their account in it may not manage it (a teen account).
func (a *App) loadMyHouseholdPerson(c *gin.Context) (*models.Household, *models.HouseholdPerson, bool) {
	u := apiUser(c)
	id, _ := strconv.Atoi(c.Param("id"))
	acct, found := householdAccountIn(a.DB, u.ID)
	if !found {
		jsonError(c, http.StatusNotFound, "household person not found")
		return nil, nil, false
	}
	if !policy.CanManageHousehold(asPerson(u), householdRelOf(acct, true)) {
		jsonError(c, http.StatusForbidden, "forbidden")
		return nil, nil, false
	}
	var household models.Household
	if err := a.DB.First(&household, acct.HouseholdID).Error; err != nil {
		jsonError(c, http.StatusNotFound, "household person not found")
		return nil, nil, false
	}
	var person models.HouseholdPerson
	if err := a.DB.Where("id = ? AND household_id = ?", id, household.ID).First(&person).Error; err != nil {
		jsonError(c, http.StatusNotFound, "household person not found")
		return nil, nil, false
	}
	if person.ConvertedUserID != nil {
		fail(c, errPersonHasAccount)
		return nil, nil, false
	}
	return &household, &person, true
}

// apiUpdateHouseholdPerson edits a person in the user's household. A
// kind change keeps DEC-32 true: becoming a child or teen starts a
// pending guardian link, and becoming an adult revokes live links.
func (a *App) apiUpdateHouseholdPerson(c *gin.Context) {
	_, person, ok := a.loadMyHouseholdPerson(c)
	if !ok {
		return
	}
	me := apiUser(c)
	var body householdPersonInput
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	updates, err := householdPersonFields(body)
	if err != nil {
		fail(c, err)
		return
	}
	newKind := person.Kind
	if k, ok := updates["kind"].(string); ok {
		newKind = k
	}
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		// User lock first, then the household the locked seat names; the
		// person must be in it.
		_, seat, err := lockHouseholdSeat(tx, me, errHouseholdPersonGone)
		if err != nil {
			return err
		}
		if err := lockHousehold(tx, seat.HouseholdID); err != nil {
			return err
		}
		if err := tx.First(person, person.ID).Error; err != nil {
			return err
		}
		if person.HouseholdID != seat.HouseholdID {
			return errHouseholdPersonGone
		}
		if person.ConvertedUserID != nil {
			return errConversionUnavailable
		}
		material := (body.FirstName != nil && strings.TrimSpace(*body.FirstName) != person.FirstName) ||
			(body.LastName != nil && strings.TrimSpace(*body.LastName) != person.LastName) ||
			(body.Kind != nil && newKind != person.Kind) ||
			(body.BirthDate != nil && strings.TrimSpace(*body.BirthDate) != formatBirthDate(person.BirthDate))
		if material {
			staged, err := approvedConversionForPerson(tx, person.ID)
			if err != nil {
				return err
			}
			if staged {
				return errConversionConsent
			}
			if err := revokePersonConversion(tx, person.ID, me.ID, c.ClientIP()); err != nil {
				return err
			}
		}
		oldKind := person.Kind
		merged := *person
		if body.Kind != nil {
			merged.Kind = newKind
		} else {
			newKind = person.Kind
		}
		if birth, set := updates["birth_date"]; set {
			merged.BirthDate = birth.(*time.Time)
		}
		if merged.BirthDate != nil {
			today, err := churchClockIn(tx, time.Now().UTC())
			if err != nil {
				return err
			}
			if !householdAgeFits(merged.Kind, merged.BirthDate, today) {
				return householdRegistrationError{code: "household_age_mismatch", personID: person.ID}
			}
		}
		if merged.Kind == models.HouseholdAdult && merged.BirthDate == nil {
			var n int64
			if err := tx.Model(&models.Registration{}).Where("household_person_id = ? AND status IN ?", person.ID, models.CurrentRegStatuses).Count(&n).Error; err != nil {
				return err
			}
			if n > 0 {
				return householdRegistrationError{code: "household_birth_date_required", personID: person.ID}
			}
		}
		if len(updates) > 0 {
			if err := tx.Model(person).Updates(updates).Error; err != nil {
				return err
			}
		}
		if err := tx.First(person, person.ID).Error; err != nil {
			return err
		}
		switch {
		case policy.NeedsGuardianLink(oldKind) && !policy.NeedsGuardianLink(newKind):
			if err := revokeGuardianLinks(tx, c, me.ID, person); err != nil {
				return err
			}
		case !policy.NeedsGuardianLink(oldKind) && policy.NeedsGuardianLink(newKind):
			_, err := createGuardianLink(tx, me.ID, person)
			if err != nil {
				return err
			}
		}
		err = a.reconcileHouseholdPerson(tx, person, false, &me.ID, c.ClientIP())
		if err != nil {
			return err
		}
		fields := []string{}
		for key := range updates {
			fields = append(fields, key)
		}
		sort.Strings(fields)
		return audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.HouseholdPersonUpdated, TargetType: "household", TargetID: person.HouseholdID, Meta: map[string]any{"personId": person.ID, "fields": fields}, IP: c.ClientIP()})
	})
	if err != nil {
		fail(c, err)
		return
	}
	var fresh models.HouseholdPerson
	if err := a.DB.First(&fresh, person.ID).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load that person")
		return
	}
	row, err := a.householdPersonRow(a.DB, &fresh, me)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not load that person")
		return
	}
	c.JSON(http.StatusOK, gin.H{"person": row})
}

// apiDeleteHouseholdPerson removes a person from the user's household:
// a soft delete, with live guardian links revoked and any waiting admin
// task resolved withdrawn, audited in the same transaction.
func (a *App) apiDeleteHouseholdPerson(c *gin.Context) {
	_, person, ok := a.loadMyHouseholdPerson(c)
	if !ok {
		return
	}
	me := apiUser(c)
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		// User lock first, then the household the locked seat names; the
		// person must be in it.
		_, seat, err := lockHouseholdSeat(tx, me, errHouseholdPersonGone)
		if err != nil {
			return err
		}
		if err := lockHousehold(tx, seat.HouseholdID); err != nil {
			return err
		}
		if err := tx.First(person, person.ID).Error; err != nil {
			return err
		}
		if person.HouseholdID != seat.HouseholdID {
			return errHouseholdPersonGone
		}
		if person.ConvertedUserID != nil {
			return errConversionUnavailable
		}
		staged, err := approvedConversionForPerson(tx, person.ID)
		if err != nil {
			return err
		}
		if staged {
			return errConversionConsent
		}
		if err := revokePersonConversion(tx, person.ID, me.ID, c.ClientIP()); err != nil {
			return err
		}
		err = a.reconcileHouseholdPerson(tx, person, true, &me.ID, c.ClientIP())
		if err != nil {
			return err
		}
		if err := revokeGuardianLinks(tx, c, me.ID, person); err != nil {
			return err
		}
		if err := audit.Write(tx, audit.Entry{Actor: &me.ID, Action: audit.HouseholdPersonRemoved,
			TargetType: "household", TargetID: seat.HouseholdID,
			Meta: map[string]any{"personId": person.ID, "kind": person.Kind}, IP: c.ClientIP()}); err != nil {
			return err
		}
		return tx.Delete(person).Error
	})
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}
