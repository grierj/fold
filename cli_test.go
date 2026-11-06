package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fold/internal/push"
	"fold/internal/testdb"
	"fold/migrations"
	"io/fs"
	"net"
	"sort"
	"strings"
	"testing"
)

func TestServeWithoutDatabaseURLExplainsDevDatabase(t *testing.T) {
	t.Setenv("APP_ENV", "dev")
	t.Setenv("DATABASE_URL", "")
	var stderr bytes.Buffer
	// No arguments means serve.
	if code := run(nil, &bytes.Buffer{}, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "deploy/docker-compose.dev.yml") {
		t.Fatalf("stderr = %q, want the start-the-dev-database hint", stderr.String())
	}
}

func TestVersionNeedsNoEnvironment(t *testing.T) {
	for _, k := range []string{"APP_ENV", "SESSION_SECRET", "DATABASE_URL"} {
		t.Setenv(k, "")
	}
	var out bytes.Buffer
	if code := run([]string{"version"}, &out, &out); code != 0 || !strings.HasPrefix(out.String(), "fold ") {
		t.Fatalf("version: code %d, output %q", code, out.String())
	}
	out.Reset()
	if code := run([]string{"version", "--json"}, &out, &out); code != 0 || !json.Valid(out.Bytes()) {
		t.Fatalf("version --json: code %d, output %q", code, out.String())
	}
}

func TestMigrateStatusNeedsOnlyDatabaseURL(t *testing.T) {
	testdb.Shared(t) // make sure DATABASE_URL works before touching env
	url, err := testdb.URL(testdb.Fresh(t, false))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_ENV", "")
	t.Setenv("SESSION_SECRET", "")
	t.Setenv("DATABASE_URL", url)

	var out, errOut bytes.Buffer
	if code := run([]string{"migrate", "status"}, &out, &errOut); code != 0 {
		t.Fatalf("status: code %d, stderr %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "pending") || !strings.Contains(out.String(), "00001_baseline.sql") {
		t.Fatalf("status output = %q", out.String())
	}
	out.Reset()
	if code := run([]string{"migrate", "up"}, &out, &errOut); code != 0 {
		t.Fatalf("up: code %d, stderr %q", code, errOut.String())
	}
	out.Reset()
	if code := run([]string{"migrate", "status", "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("status --json: code %d, stderr %q", code, errOut.String())
	}
	var rows []struct{ State string }
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) == 0 || rows[0].State != "applied" {
		t.Fatalf("status --json = %q (%v)", out.String(), err)
	}
}

// The baseline Down destroys the schema, so `fold migrate down-one` on a
// database that holds a user must roll back every later migration, then fail
// at the baseline, exit nonzero and change nothing more. The file list comes
// from migrations.Files, so a later migration needs no edit here.
func TestMigrateDownOneRefusesBaselineWithData(t *testing.T) {
	name := testdb.Fresh(t, true)
	db, err := testdb.OpenSQL(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	p, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO users(id,email,status,role) VALUES(1,'cli-down@example.com','active','member')`); err != nil {
		t.Fatal(err)
	}
	url, err := testdb.URL(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_ENV", "")
	t.Setenv("SESSION_SECRET", "")
	t.Setenv("DATABASE_URL", url)

	files, err := fs.Glob(migrations.Files, "*.sql")
	sort.Strings(files)
	if err != nil || len(files) == 0 || files[0] != "00001_baseline.sql" {
		t.Fatalf("migration files = %v (%v)", files, err)
	}

	var out, errOut bytes.Buffer
	// Every file after the baseline rolls back, newest first.
	for i := len(files) - 1; i >= 1; i-- {
		out.Reset()
		errOut.Reset()
		if code := run([]string{"migrate", "down-one"}, &out, &errOut); code != 0 {
			t.Fatalf("down-one of %s: code %d, stdout %q, stderr %q", files[i], code, out.String(), errOut.String())
		}
		if !strings.Contains(out.String(), "rolled back") || !strings.Contains(out.String(), files[i]) {
			t.Fatalf("down-one of %s: stdout %q, want it named as rolled back", files[i], out.String())
		}
	}
	// Additive migrations may create tables that their Down removes. Compare
	// the refused baseline Down with the schema after those rollbacks.
	var tablesBefore int
	if err := db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE'`).Scan(&tablesBefore); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if code := run([]string{"migrate", "down-one"}, &out, &errOut); code != 1 {
		t.Fatalf("down-one: code %d, stdout %q, stderr %q", code, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "fold: migrate down-one:") || !strings.Contains(errOut.String(), "baseline down would destroy data; refusing") {
		t.Fatalf("down-one stderr = %q", errOut.String())
	}
	var email string
	if err := db.QueryRow(`SELECT email FROM users WHERE id=1`).Scan(&email); err != nil || email != "cli-down@example.com" {
		t.Fatalf("the refused down-one changed the user row: %q %v", email, err)
	}
	var tablesAfter int
	if err := db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE'`).Scan(&tablesAfter); err != nil || tablesAfter != tablesBefore {
		t.Fatalf("tables after the refusal = %d (%v), want %d", tablesAfter, err, tablesBefore)
	}
	if version, err := p.GetDBVersion(ctx); err != nil || version != 1 {
		t.Fatalf("version after the refusal = %d, %v, want 1", version, err)
	}
	// The database still works: status reports the baseline applied and every
	// later migration pending.
	out.Reset()
	errOut.Reset()
	if code := run([]string{"migrate", "status", "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("status after the refusal: code %d, stderr %q", code, errOut.String())
	}
	var rows []struct{ State string }
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != len(files) || rows[0].State != "applied" {
		t.Fatalf("status --json = %q (%v), want %d rows with the first applied", out.String(), err, len(files))
	}
	for i, r := range rows[1:] {
		if r.State != "pending" {
			t.Fatalf("status row %d (%s) = %q, want pending", i+1, files[i+1], r.State)
		}
	}
	// Up brings every migration back, and status shows them all applied.
	out.Reset()
	errOut.Reset()
	if code := run([]string{"migrate", "up"}, &out, &errOut); code != 0 {
		t.Fatalf("up after the refusal: code %d, stdout %q, stderr %q", code, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := run([]string{"migrate", "status", "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("status after up: code %d, stderr %q", code, errOut.String())
	}
	rows = nil
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != len(files) {
		t.Fatalf("status --json after up = %q (%v), want %d rows", out.String(), err, len(files))
	}
	for i, r := range rows {
		if r.State != "applied" {
			t.Fatalf("status row %d (%s) after up = %q, want applied", i, files[i], r.State)
		}
	}
}

func TestPushKeysShowAndRotate(t *testing.T) {
	testdb.Shared(t)
	name := testdb.Fresh(t, true)
	url, err := testdb.URL(name)
	if err != nil {
		t.Fatal(err)
	}
	db, err := testdb.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s, err := db.DB(); err == nil {
			s.Close()
		}
	})
	t.Setenv("APP_ENV", "")
	t.Setenv("SESSION_SECRET", "")
	t.Setenv("DATABASE_URL", url)
	var out, errOut bytes.Buffer
	// No keys until the server first starts.
	if code := run([]string{"push", "keys", "show"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "no web push keys yet") {
		t.Fatalf("show before boot: %d %q", code, errOut.String())
	}
	secret := "a session secret that is long enough for prod"
	if _, err := ensureVAPIDKeys(db, []byte(secret)); err != nil {
		t.Fatal(err)
	}
	public, _, _ := readVAPID(db)
	out.Reset()
	if code := run([]string{"push", "keys", "show"}, &out, &errOut); code != 0 || out.String() != public+"\n" {
		t.Fatalf("show: %d %q", code, out.String())
	}
	out.Reset()
	if code := run([]string{"push", "keys", "show", "--json"}, &out, &errOut); code != 0 || out.String() != `{"publicKey":"`+public+`"}`+"\n" {
		t.Fatalf("show --json: %d %q", code, out.String())
	}
	// Rotate needs SESSION_SECRET outside dev.
	errOut.Reset()
	if code := run([]string{"push", "keys", "rotate"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "SESSION_SECRET must be set") {
		t.Fatalf("rotate without a secret: %d %q", code, errOut.String())
	}
	t.Setenv("SESSION_SECRET", secret)
	if err := db.Exec(`INSERT INTO users (id, email, name, role, status) VALUES (1, 'pat@example.com', 'Pat', 'member', 'active')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO push_subscriptions (user_id, endpoint, p256dh, auth) VALUES (1, 'https://fcm.googleapis.com/a', 'k', 'a'), (1, 'https://fcm.googleapis.com/b', 'k', 'a')`).Error; err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := run([]string{"push", "keys", "rotate"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "removed 2 devices") {
		t.Fatalf("rotate: %d %q %q", code, out.String(), errOut.String())
	}
	rotated, enc, _ := readVAPID(db)
	if rotated == public {
		t.Fatal("rotate kept the key")
	}
	if _, err := openVAPIDKey([]byte(secret), enc); err != nil {
		t.Fatalf("the rotated key doesn't open with SESSION_SECRET: %v", err)
	}
	out.Reset()
	if code := run([]string{"push", "keys", "rotate", "--json"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), `"devicesRemoved":0`) {
		t.Fatalf("rotate --json: %d %q", code, out.String())
	}
	if code := run([]string{"push", "keys", "spin"}, &out, &errOut); code != 2 {
		t.Fatalf("unknown push command: %d", code)
	}
	if code := run([]string{"push", "keys", "show", "--force"}, &out, &errOut); code != 2 {
		t.Fatalf("show --force: %d", code)
	}
	// A SESSION_SECRET that isn't the server's is refused, and nothing changes.
	if err := db.Exec(`INSERT INTO push_subscriptions (user_id, endpoint, p256dh, auth) VALUES (1, 'https://fcm.googleapis.com/c', 'k', 'a')`).Error; err != nil {
		t.Fatal(err)
	}
	before, beforeEnc, _ := readVAPID(db)
	other := "some other secret that is long enough for prod"
	t.Setenv("SESSION_SECRET", other)
	out.Reset()
	errOut.Reset()
	if code := run([]string{"push", "keys", "rotate"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "isn't the one the server uses") {
		t.Fatalf("rotate with another secret: %d %q", code, errOut.String())
	}
	if p, e, _ := readVAPID(db); p != before || e != beforeEnc {
		t.Fatal("a refused rotate changed the keys")
	}
	var devices int64
	db.Raw(`SELECT count(*) FROM push_subscriptions`).Scan(&devices)
	if devices != 1 {
		t.Fatalf("a refused rotate removed devices: %d left", devices)
	}
	// --force rotates with it anyway.
	out.Reset()
	if code := run([]string{"push", "keys", "rotate", "--force"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "removed 1 devices") {
		t.Fatalf("rotate --force: %d %q %q", code, out.String(), errOut.String())
	}
	_, enc, _ = readVAPID(db)
	if _, err := openVAPIDKey([]byte(other), enc); err != nil {
		t.Fatalf("--force didn't rotate with the given secret: %v", err)
	}
}

func TestPushCheckNeedsNothingAndReportsEachHost(t *testing.T) {
	for _, k := range []string{"APP_ENV", "SESSION_SECRET", "DATABASE_URL"} {
		t.Setenv(k, "")
	}
	t.Setenv("FOLD_PUSH_HOSTS", "push.example.org")
	var dialed []string
	pushCheckDial = func(_ context.Context, _, addr string) (net.Conn, error) {
		dialed = append(dialed, addr)
		if addr == "push.example.org:443" {
			return nil, errors.New("no route to host")
		}
		a, b := net.Pipe()
		_ = b.Close()
		return a, nil
	}
	t.Cleanup(func() { pushCheckDial = nil })
	var out, errOut bytes.Buffer
	if code := run([]string{"push", "check"}, &out, &errOut); code != 1 {
		t.Fatalf("check: %d", code)
	}
	if !strings.Contains(out.String(), "OK    fcm.googleapis.com:443") || !strings.Contains(out.String(), "OK    web.push.apple.com:443") ||
		!strings.Contains(out.String(), "FAIL  push.example.org:443  no route to host") || !strings.Contains(errOut.String(), "Allow outbound https") {
		t.Fatalf("check output %q %q", out.String(), errOut.String())
	}
	if strings.Contains(out.String(), "samsung") || len(dialed) != len(push.DefaultHosts)+1 {
		t.Fatalf("dialed %v", dialed)
	}
	t.Setenv("FOLD_PUSH_HOSTS", "")
	out.Reset()
	if code := run([]string{"push", "check", "--json"}, &out, &errOut); code != 0 || !json.Valid(out.Bytes()) || !strings.Contains(out.String(), `"ok":true`) {
		t.Fatalf("check --json: %d %q", code, out.String())
	}
	t.Setenv("FOLD_PUSH_HOSTS", "10.0.0.1")
	if code := run([]string{"push", "check"}, &out, &errOut); code != 1 {
		t.Fatalf("a bad FOLD_PUSH_HOSTS: %d", code)
	}
}
