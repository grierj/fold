package main

import (
	"fmt"

	"fold/internal/notify"
	"fold/internal/policy"
	"fold/models"

	"gorm.io/gorm"
)

// notifyAccountRequested asks every admin to decide an account
// request (a task, DEC-4). Only a clean adult request carries the
// inline Approve (DEC-44); teens and flagged requests are Review-only.
// The row names the requester for admins but never their contact
// details, and the email variant names nobody at all.
func notifyAccountRequested(tx *gorm.DB, req *models.AccountRequest, flags models.RequestFlags) error {
	source := "Self-signup"
	if req.Source == models.RequestGuestUpgrade {
		source = "Guest asking for an account"
	}
	var actor *uint
	if req.RequestedBy != nil && (req.Source == models.RequestHouseholdConversion || req.Source == models.RequestMemberInvite) {
		by, err := nameOf(tx, *req.RequestedBy)
		if err != nil {
			return err
		}
		source = "Invited by " + by
		if req.Source == models.RequestHouseholdConversion {
			source = "Household account invited by " + by
		}
		actor = req.RequestedBy
	}
	eligible := policy.InlineApproveAccountOK(req.AccountType, flags.HasWarnings())
	admins, err := activeAdminIDs(tx)
	if err != nil {
		return err
	}
	_, err = notify.Send(tx, notify.Msg{
		Kind: notify.AccountRequested, To: admins, Actor: actor,
		SubjectType: "account_request", SubjectID: req.ID, Link: approvalsLink,
		Vars:            notify.Vars{Person: req.Name, Change: source, SubjectKind: requestSubjectKind(req.AccountType)},
		Data:            map[string]any{"requestId": req.ID},
		DedupeKey:       fmt.Sprintf("account.requested:%d", req.ID),
		SuppressActions: !eligible,
		BellOnly:        flags.Duplicate || flags.Honeypot,
	})
	return err
}

// notifyGuardianLinkPending asks every admin to confirm a guardian link
// to a child or teen household person (a task, DEC-32, M1.9). The rows
// go to admins only, so they name the guardian and the person; the email
// variant names nobody. Resolved on confirm and reject (and withdrawn
// when the person is removed before a decision).
func notifyGuardianLinkPending(tx *gorm.DB, link *models.GuardianLink, person *models.HouseholdPerson) error {
	guardian, err := nameOf(tx, link.GuardianUserID)
	if err != nil {
		return err
	}
	vars := notify.Vars{Actor: guardian}
	data := map[string]any{"guardianLinkId": link.ID}
	if person != nil {
		vars.Person = person.FullName()
		if vars.SubjectKind, err = subjectKindOfHousehold(tx, person.ID); err != nil {
			return err
		}
		data["personId"] = person.ID
	} else if link.TeenUserID != nil {
		// M1.10 uses account-targeted guardian links for parent consent.
		teen, err := nameOf(tx, *link.TeenUserID)
		if err != nil {
			return err
		}
		vars.Person, vars.Change = teen, "teen_account"
		if vars.SubjectKind, err = subjectKindOfUser(tx, *link.TeenUserID); err != nil {
			return err
		}
		data["teenUserId"] = *link.TeenUserID
	}
	admins, err := activeAdminIDs(tx)
	if err != nil {
		return err
	}
	if person == nil {
		// An admin who is the teen is never badged on their own bell.
		vars.SubjectKind = badgeForRecipients(vars.SubjectKind, link.TeenUserID, admins)
	}
	_, err = notify.Send(tx, notify.Msg{
		Kind: notify.HouseholdLinkPending, To: admins, Actor: &link.GuardianUserID,
		SubjectType: "guardian_link", SubjectID: link.ID, Link: approvalsLink,
		Vars:      vars,
		Data:      data,
		DedupeKey: fmt.Sprintf("household.link_pending:%d", link.ID),
	})
	return err
}

// requestSubjectKind is the badge for an account request: a teen's request is
// badged for the admins who review it, who see every request (DEC-44). The
// requester has no account yet, so they are never a recipient.
func requestSubjectKind(accountType string) string {
	if accountType == models.AccountTeen {
		return notify.SubjectTeen
	}
	return ""
}
