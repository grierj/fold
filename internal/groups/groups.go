// Package groups reads groups and memberships for pages and commands.
package groups

import (
	"context"
	"database/sql"
	"errors"

	"fold/internal/db"
	"fold/internal/policy"
)

// RelOf is a person's relation to a group, read from the membership row
// itself so a removal is seen (DEC-35). A membership counts as active only
// while its person is active and its group is not deleted, the same three
// conditions as the active_memberships view.
func RelOf(ctx context.Context, q db.Querier, groupID, personID int64) (policy.Rel, error) {
	var role, state, personStatus, groupStatus string
	err := q.QueryRowContext(ctx, `SELECT m.role, m.state, p.status, g.status FROM memberships m
		JOIN people p ON p.id = m.person_id JOIN groups g ON g.id = m.group_id
		WHERE m.group_id = ? AND m.person_id = ?`, groupID, personID).Scan(&role, &state, &personStatus, &groupStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return policy.Rel{}, nil
	}
	if err != nil {
		return policy.Rel{}, err
	}
	active := state == "active" && personStatus == "active" && groupStatus != policy.GroupDeleted
	return policy.Rel{Member: active, Leader: active && role == "leader", Removed: state == "removed"}, nil
}
