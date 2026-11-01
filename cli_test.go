package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fold/internal/push"
	"fold/internal/testdb"
	"fold/migrations"
	"net"
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

func TestMigrateDownOneRefusesAdvancedMembershipTenure(t *testing.T) {
	name := testdb.Fresh(t, false)
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
	if _, err := p.UpTo(ctx, 36); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO users(id,email,status,role) VALUES(1,'cli-tenure@example.com','active','member')`,
		`INSERT INTO groups(id,name,leader_id,status,visibility) VALUES(10,'CLI tenure group',1,'active','public')`,
		`INSERT INTO memberships(id,user_id,group_id,role,status) VALUES(50,1,10,'member','active')`,
		`UPDATE memberships SET status='removed' WHERE id=50`,
		`UPDATE memberships SET status='active' WHERE id=50`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	url, err := testdb.URL(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", url)

	var out, errOut bytes.Buffer
	if code := run([]string{"migrate", "down-one"}, &out, &errOut); code != 1 {
		t.Fatalf("down-one: code %d, stdout %q, stderr %q", code, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "fold: migrate down-one:") || !strings.Contains(errOut.String(), "advanced membership tenure exists; cannot downgrade safely") {
		t.Fatalf("down-one stderr = %q", errOut.String())
	}
	var tenure int64
	if err := db.QueryRow(`SELECT tenure FROM memberships WHERE id=50`).Scan(&tenure); err != nil || tenure != 2 {
		t.Fatalf("preserved tenure=%d err=%v, want 2", tenure, err)
	}
	var trigger, function bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='memberships'::regclass AND tgname='memberships_tenure_guard' AND NOT tgisinternal)`).Scan(&trigger); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT to_regprocedure('guard_membership_tenure()') IS NOT NULL`).Scan(&function); err != nil {
		t.Fatal(err)
	}
	version, err := p.GetDBVersion(ctx)
	if err != nil || version != 36 || !trigger || !function {
		t.Fatalf("preserved version=%d trigger=%v function=%v err=%v", version, trigger, function, err)
	}
	if _, err := db.Exec(`UPDATE memberships SET tenure=9 WHERE id=50`); err == nil {
		t.Fatal("down-one failure left tenure trigger inactive")
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
	if !strings.Contains(out.String(), "SKIP  push.samsungosp.com") || len(dialed) != len(push.DefaultHosts) {
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
