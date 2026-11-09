package main

import (
	"fold/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// lockUsersByID takes NO KEY UPDATE on the users with these ids in one
// statement, in ascending id order, which is the order every multi-user path
// in the lock hierarchy uses (contact gate, then users by id, then
// households). Callers pass the ids they have already discovered; an id with
// no row is simply absent from the map. includeDeleted also locks soft-deleted
// accounts, which the conversion paths need to see (erasure tombstones keep
// their rows); the admin household and teen lifecycle paths pass false.
func lockUsersByID(tx *gorm.DB, ids []uint, includeDeleted bool) (map[uint]*models.User, error) {
	q := tx
	if includeDeleted {
		q = q.Unscoped()
	}
	var rows []models.User
	if err := q.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).Where("id IN ?", ids).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	locked := make(map[uint]*models.User, len(rows))
	for i := range rows {
		locked[rows[i].ID] = &rows[i]
	}
	return locked, nil
}
