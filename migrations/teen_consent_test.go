package migrations_test

import (
	"context"
	"strings"
	"testing"

	"fold/internal/testdb"
	"fold/migrations"
)

func TestTeenConsentMigrationAppendOnlyAndRollback(t *testing.T) {
	db := openSQL(t, testdb.Fresh(t, false))
	p, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := p.UpTo(ctx, 25); err != nil {
		t.Fatal(err)
	}
	exec := func(query string) {
		t.Helper()
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users(id,email,status) VALUES(1,'admin@example.com','active'),(2,'teen@example.com','disabled')`)
	if _, err := p.UpTo(ctx, 26); err != nil {
		t.Fatal(err)
	}
	var suspended bool
	if err := db.QueryRow(`SELECT administratively_disabled FROM users WHERE id=2`).Scan(&suspended); err != nil || !suspended {
		t.Fatalf("disabled backfill: %t %v", suspended, err)
	}
	exec(`INSERT INTO teen_consents(teen_user_id,scope,status,granted_by,granter_role,evidence_text) VALUES(2,'account','granted',1,'admin','Paper consent')`)
	for name, query := range map[string]string{
		"update":         `UPDATE teen_consents SET status='revoked'`,
		"delete":         `DELETE FROM teen_consents`,
		"scope":          `INSERT INTO teen_consents(teen_user_id,scope,status,granted_by,granter_role) VALUES(2,'all','revoked',1,'admin')`,
		"admin evidence": `INSERT INTO teen_consents(teen_user_id,scope,status,granted_by,granter_role) VALUES(2,'sms','granted',1,'admin')`,
		"teen FK":        `INSERT INTO teen_consents(teen_user_id,scope,status,granted_by,granter_role) VALUES(999,'sms','revoked',1,'admin')`,
	} {
		if _, err := db.Exec(query); err == nil {
			t.Errorf("%s allowed", name)
		}
	}
	if _, err := p.DownTo(ctx, 25); err == nil || !strings.Contains(err.Error(), "teen consent history exists") {
		t.Fatalf("down discarded history: %v", err)
	}
	// The test helper truncates tables between tests. The row trigger permits
	// that operation, and empty-database rollback removes the trigger itself.
	exec(`TRUNCATE teen_consents`)
	if _, err := p.DownTo(ctx, 25); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 26); err != nil {
		t.Fatal(err)
	}
}
