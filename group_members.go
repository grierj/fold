package main

import (
	"gorm.io/gorm"

	"fold/models"
)

// listedUsers is the account-only membership join: not deleted or disabled.
// Registration queries use listedRegistrations for their two subject types.
func listedUsers(table string) string {
	return "JOIN users ON users.id = " + table + ".user_id AND users.deleted_at IS NULL AND users.status <> 'disabled'"
}

// A group's members and leaders are its active memberships whose accounts
// still exist and aren't disabled. soleLedGroups (api_handoff.go) states the
// leader rule in SQL and TestSoleLedGroupsMatchLeaderCount keeps the two alike.
func listedLeaders(db *gorm.DB, groupID uint) *gorm.DB {
	return db.Model(&models.Membership{}).Joins(listedUsers("memberships")).
		Where("memberships.group_id = ? AND memberships.role = ? AND memberships.status = ?",
			groupID, models.MembershipLeader, models.MembershipActive)
}

// leaderCount counts a group's listed leaders. A failed read is an error, never
// a count.
func leaderCount(db *gorm.DB, groupID uint) (int, error) {
	var n int64
	err := listedLeaders(db, groupID).Count(&n).Error
	return int(n), err
}

// leaderIDs is a group's listed leaders, by user id.
func leaderIDs(db *gorm.DB, groupID uint) ([]uint, error) {
	var ids []uint
	err := listedLeaders(db, groupID).Order("memberships.user_id").Pluck("memberships.user_id", &ids).Error
	return ids, err
}

// memberCount counts a group's listed active members. A failed read is an
// error: GroupHasRoom never sees a made-up zero.
func memberCount(db *gorm.DB, groupID uint) (int, error) {
	var n int64
	err := db.Model(&models.Membership{}).Joins(listedUsers("memberships")).
		Where("memberships.group_id = ? AND memberships.status = ?", groupID, models.MembershipActive).Count(&n).Error
	return int(n), err
}
