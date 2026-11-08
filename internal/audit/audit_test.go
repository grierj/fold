package audit

import (
	"os"
	"strconv"
	"testing"

	"fold/internal/testdb"
	"fold/models"

	"gorm.io/gorm"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

func signedEntries(n int) []SignedEntry {
	out := make([]SignedEntry, n)
	for i := range out {
		out[i] = SignedEntry{Action: ContactReasonKept, TargetType: "contact_request", TargetID: int64(i) - 500, Meta: map[string]any{"n": i}, IP: "192.0.2.1"}
	}
	return out
}

// Exact signed targets, including both ends of the int64 range, survive the
// batch document.
func TestWriteAllStoresExactSignedTargets(t *testing.T) {
	db := testdb.Shared(t)
	ids := []int64{-9223372036854775808, -1, 0, 9223372036854775807}
	var entries []SignedEntry
	for _, id := range ids {
		entries = append(entries, SignedEntry{Action: AddReasonRemoved, TargetType: "add_request", TargetID: id,
			Meta: map[string]any{"erasedUserId": 7, "reviewVersion": 1}, IP: "192.0.2.9"})
	}
	if err := WriteAll(db, entries); err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := db.Raw("SELECT target_id::text FROM audit_logs WHERE action = ? ORDER BY id", AddReasonRemoved).Scan(&got).Error; err != nil {
		t.Fatal(err)
	}
	if len(got) != len(ids) {
		t.Fatalf("stored %d rows, want %d", len(got), len(ids))
	}
	for i, id := range ids {
		if got[i] != strconv.FormatInt(id, 10) {
			t.Errorf("row %d target = %s, want %d", i, got[i], id)
		}
	}
	var meta string
	if err := db.Raw("SELECT meta_json::text FROM audit_logs WHERE action = ? ORDER BY id LIMIT 1", AddReasonRemoved).Scan(&meta).Error; err != nil {
		t.Fatal(err)
	}
	if meta != `{"erasedUserId": 7, "reviewVersion": 1}` {
		t.Errorf("meta = %s", meta)
	}
}

// The batch boundary: 999, 1,000 and 1,001 rows all arrive, in order, once.
func TestWriteAllBatchBoundary(t *testing.T) {
	db := testdb.Shared(t)
	for _, n := range []int{0, 1, WriteBatchSize - 1, WriteBatchSize, WriteBatchSize + 1, 2*WriteBatchSize + 1} {
		if err := db.Exec("DELETE FROM audit_logs").Error; err != nil {
			t.Fatal(err)
		}
		if err := WriteAll(db, signedEntries(n)); err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		var rows []struct {
			ID       uint
			TargetID int64
		}
		if err := db.Raw("SELECT id, target_id FROM audit_logs ORDER BY id").Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) != n {
			t.Fatalf("n=%d: stored %d", n, len(rows))
		}
		for i, r := range rows {
			if r.TargetID != int64(i)-500 {
				t.Fatalf("n=%d: row %d has target %d, want %d (entry order lost)", n, i, r.TargetID, i-500)
			}
		}
	}
}

// A failure in the second batch is returned, and because the caller's
// transaction rolls back on it, no row of the first batch remains.
func TestWriteAllFailureInSecondBatchLeavesNothing(t *testing.T) {
	db := testdb.Shared(t)
	entries := signedEntries(WriteBatchSize + 5)
	// jsonb refuses \u0000, so this one row makes the second statement fail.
	entries[WriteBatchSize+2].Action = "bad\x00action"
	err := db.Transaction(func(tx *gorm.DB) error { return WriteAll(tx, entries) })
	if err == nil {
		t.Fatal("WriteAll accepted a NUL byte")
	}
	var n int64
	if err := db.Model(&models.AuditLog{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d rows remain after the failed transaction", n)
	}
}

// Write and WriteAll record the same shape for a positive target.
func TestWriteAllMatchesWrite(t *testing.T) {
	db := testdb.Shared(t)
	if err := Write(db, Entry{Action: ContactReasonKept, TargetType: "contact_request", TargetID: 42, Meta: map[string]any{"k": "v"}, IP: "192.0.2.3"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteAll(db, []SignedEntry{{Action: ContactReasonKept, TargetType: "contact_request", TargetID: 42, Meta: map[string]any{"k": "v"}, IP: "192.0.2.3"}}); err != nil {
		t.Fatal(err)
	}
	var rows []models.AuditLog
	if err := db.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("%d rows", len(rows))
	}
	a, b := rows[0], rows[1]
	if a.Action != b.Action || a.TargetType != b.TargetType || a.TargetID != b.TargetID || a.IP != b.IP || a.ActorID != nil || b.ActorID != nil {
		t.Errorf("rows differ: %+v vs %+v", a, b)
	}
	var same bool
	if err := db.Raw("SELECT (SELECT meta_json FROM audit_logs WHERE id = ?) = (SELECT meta_json FROM audit_logs WHERE id = ?)", a.ID, b.ID).Scan(&same).Error; err != nil || !same {
		t.Errorf("metadata differs (%v)", err)
	}
}
