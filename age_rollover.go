package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"fold/internal/accounts"
	"fold/internal/audit"
	"fold/internal/notify"
	"fold/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// rolloverAges takes an instant, then uses the church's calendar date for
// every age check. Each person commits separately so a restart repeats only
// unfinished work. User type and the durable child milestone audit prevent
// repeated changes and notices, even after notification retention runs.
func (a *App) rolloverAges(now time.Time) (int, error) {
	today := now.In(churchZone(a.DB))
	var userIDs []uint
	if err := a.DB.Model(&models.User{}).Where("account_type = ? AND birth_date IS NOT NULL AND birth_date <= ?", models.AccountTeen,
		today.AddDate(-18, 0, 0).Format("2006-01-02")).Order("id").Pluck("id", &userIDs).Error; err != nil {
		return 0, err
	}
	changed := 0
	for _, id := range userIDs {
		var mailed []uint
		var done bool
		err := a.DB.Transaction(func(tx *gorm.DB) error {
			var user models.User
			if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&user, id).Error; err != nil {
				return err
			}
			var err error
			done, mailed, err = a.rolloverTeen(tx, &user, today, now)
			return err
		})
		if err != nil {
			return changed, err
		}
		if done {
			changed++
		}
		a.deliverNotificationEmails(mailed)
	}
	var people []models.HouseholdPerson
	if err := a.DB.Where("kind = ? AND converted_user_id IS NULL AND birth_date IS NOT NULL AND birth_date <= ?", models.HouseholdChild,
		today.AddDate(-13, 0, 0).Format("2006-01-02")).
		Where("NOT EXISTS (SELECT 1 FROM audit_logs WHERE action = ? AND target_type = 'household_person' AND target_id = household_people.id)", audit.HouseholdChildTurned13).
		Order("household_id, id").Find(&people).Error; err != nil {
		return changed, err
	}
	for _, person := range people {
		var mailed []uint
		var done bool
		err := a.DB.Transaction(func(tx *gorm.DB) error {
			if err := lockHousehold(tx, person.HouseholdID); err != nil {
				return err
			}
			if err := tx.First(&person, person.ID).Error; err != nil {
				return err
			}
			var err error
			done, mailed, err = flagHouseholdChild13(tx, &person, today)
			return err
		})
		if err != nil {
			return changed, err
		}
		if done {
			changed++
		}
		a.deliverNotificationEmails(mailed)
	}
	return changed, nil
}

func (a *App) rolloverTeen(tx *gorm.DB, user *models.User, today, now time.Time) (bool, []uint, error) {
	if user.AccountType != models.AccountTeen || user.BirthDate == nil || accounts.AgeOn(*user.BirthDate, today) < 18 {
		return false, nil, nil
	}
	oldStatus := user.Status
	status := oldStatus
	if !user.AdministrativelyDisabled && (status == models.UserPendingConsent || status == models.UserDisabled) {
		status = models.UserActive
		if user.PasswordHash == nil {
			status = models.UserInvited
		}
	}
	// Restoration must see the new adult type while the account is still
	// disabled, so it can check access without counting their places twice.
	if err := tx.Model(user).Update("account_type", models.AccountAdult).Error; err != nil {
		return false, nil, err
	}
	var mailed []uint
	if oldStatus == models.UserDisabled && status != models.UserDisabled {
		ids, err := restoreMemberships(tx, user.ID, 0, "")
		if err != nil {
			return false, nil, err
		}
		mailed = append(mailed, ids...)
		ids, err = a.restoreRegistrations(tx, user.ID, status, 0, "", now)
		if err != nil {
			return false, nil, err
		}
		mailed = append(mailed, ids...)
	}
	if status != oldStatus {
		updates := map[string]any{"status": status}
		if oldStatus == models.UserDisabled {
			updates["session_version"] = gorm.Expr("session_version + 1")
		}
		if err := tx.Model(user).Updates(updates).Error; err != nil {
			return false, nil, err
		}
	}
	if err := audit.Write(tx, audit.Entry{Action: audit.UserTurned18, TargetType: "user", TargetID: user.ID,
		Meta: map[string]any{"previousStatus": oldStatus, "status": status, "churchDate": today.Format("2006-01-02")}}); err != nil {
		return false, nil, err
	}
	ids, err := notify.Send(tx, notify.Msg{Kind: notify.AccountTurned18, To: []uint{user.ID}, SubjectType: "user", SubjectID: user.ID,
		Link: "/profile", DedupeKey: fmt.Sprintf("account.turned_18:%d", user.ID)})
	mailed = append(mailed, ids...)
	return true, mailed, err
}

func flagHouseholdChild13(tx *gorm.DB, person *models.HouseholdPerson, today time.Time) (bool, []uint, error) {
	if person.Kind != models.HouseholdChild || person.ConvertedUserID != nil || person.BirthDate == nil || accounts.AgeOn(*person.BirthDate, today) < 13 {
		return false, nil, nil
	}
	var count int64
	if err := tx.Model(&models.AuditLog{}).Where("action = ? AND target_type = 'household_person' AND target_id = ?", audit.HouseholdChildTurned13, person.ID).Count(&count).Error; err != nil {
		return false, nil, err
	}
	if count > 0 {
		return false, nil, nil
	}
	var guardians []uint
	if err := tx.Model(&models.GuardianLink{}).
		Joins("JOIN users ON users.id = guardian_links.guardian_user_id AND users.deleted_at IS NULL AND users.status = ? AND users.account_type = ?", models.UserActive, models.AccountAdult).
		Where("person_id = ? AND guardian_links.status = ?", person.ID, models.GuardianConfirmed).Pluck("guardian_user_id", &guardians).Error; err != nil {
		return false, nil, err
	}
	if len(guardians) == 0 {
		return false, nil, nil
	}
	if err := audit.Write(tx, audit.Entry{Action: audit.HouseholdChildTurned13, TargetType: "household_person", TargetID: person.ID,
		Meta: map[string]any{"householdId": person.HouseholdID, "churchDate": today.Format("2006-01-02")}}); err != nil {
		return false, nil, err
	}
	ids, err := notify.Send(tx, notify.Msg{Kind: notify.HouseholdChildTurned13, To: guardians, SubjectType: "household_person", SubjectID: person.ID,
		Link: "/profile", Vars: notify.Vars{Person: person.FullName()}, DedupeKey: fmt.Sprintf("household.child_turned_13:%d", person.ID)})
	return true, ids, err
}

func nextAgeRollover(now time.Time, zone *time.Location) time.Time {
	local := now.In(zone)
	return time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, zone)
}

func (a *App) startAgeRollover(ctx context.Context) {
	go func() {
		for {
			if n, err := a.rolloverAges(time.Now().UTC()); err != nil {
				log.Printf("age rollover: %v", err)
			} else if n > 0 {
				log.Printf("age rollover: updated %d people", n)
			}
			now := time.Now()
			timer := time.NewTimer(nextAgeRollover(now, churchZone(a.DB)).Sub(now))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}
