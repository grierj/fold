package groups_test

import (
	"context"
	"testing"

	"fold/internal/groups"
	"fold/internal/policy"
	"fold/internal/testenv"
)

// A removed person's relation says so, and policy then refuses their rejoin.
func TestRelOfSeesRemoval(t *testing.T) {
	env := testenv.New(t)
	for _, q := range []string{
		`INSERT INTO people (id, uid, name, email, status, created_at, updated_at) VALUES (1, 'a', 'A', 'a@x.org', 'active', 0, 0), (2, 'b', 'B', 'b@x.org', 'active', 0, 0), (3, 'c', 'C', 'c@x.org', 'disabled', 0, 0)`,
		`INSERT INTO groups (id, uid, name, visibility, status, created_at, updated_at) VALUES (1, 'g1', 'G1', 'public', 'active', 0, 0)`,
		`INSERT INTO memberships (group_id, person_id, role, state, created_at, updated_at) VALUES (1, 1, 'leader', 'active', 0, 0), (1, 2, 'member', 'removed', 0, 0), (1, 3, 'leader', 'active', 0, 0)`,
	} {
		if _, err := env.Writer.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	g := policy.Group{Status: policy.GroupActive, Visibility: policy.Public}
	removed, _ := groups.RelOf(ctx, env.Writer, 1, 2)
	if !removed.Removed || removed.Member || policy.CanJoinGroup(&policy.Person{ID: 2}, g, removed) {
		t.Fatalf("a removed person may rejoin: %+v", removed)
	}
	lead, _ := groups.RelOf(ctx, env.Writer, 1, 1)
	if !lead.Member || !lead.Leader {
		t.Fatalf("leader: %+v", lead)
	}
	disabled, _ := groups.RelOf(ctx, env.Writer, 1, 3)
	if disabled.Member || disabled.Leader {
		t.Fatalf("a disabled leader still counts: %+v", disabled)
	}
	stranger, _ := groups.RelOf(ctx, env.Writer, 1, 99)
	if stranger != (policy.Rel{}) {
		t.Fatalf("stranger: %+v", stranger)
	}
}
