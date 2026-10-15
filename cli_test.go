package main

import (
	"bytes"
	"encoding/json"
	"fold/internal/testdb"
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
