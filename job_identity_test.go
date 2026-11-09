package main

import (
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"time"

	"fold/migrations"
)

func jsonKey(t *testing.T, payload any, field string) string {
	t.Helper()
	f, ok := reflect.TypeOf(payload).FieldByName(field)
	if !ok {
		t.Fatalf("%T has no field %s", payload, field)
	}
	key, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	return key
}

// The payload keys that raw SQL reads are the structs' JSON keys.
func TestJobPayloadKeysMatchTheSQLThatReadsThem(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload any
		field   string
		sqlKey  string
	}{
		{"message.send", messageSendJob{}, "MessageID", messageJobIDField},
		{"message.deliver_batch", messageBatchJob{}, "MessageID", messageJobIDField},
		{"push.deliver", pushJob{}, "MessageRecipientID", pushJobRecipientField},
		{"avatar.cleanup", avatarCleanupJob{}, "UserID", avatarCleanupUserField},
	} {
		if got := jsonKey(t, tc.payload, tc.field); got != tc.sqlKey {
			t.Errorf("%s: payload key %q, SQL reads %q", tc.name, got, tc.sqlKey)
		}
	}
}

// push.devices_off is queued only by the users trigger. Its kind name and
// payload keys are spelled there in SQL, and its attempts are the column
// default, so Go must say the same.
func TestDevicesOffTriggerMatchesItsJobType(t *testing.T) {
	var sql strings.Builder
	err := fs.WalkDir(migrations.Files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".sql") {
			return err
		}
		b, err := fs.ReadFile(migrations.Files, path)
		sql.Write(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	all := sql.String()
	if want := "VALUES ('" + kindPushDevicesOff + "',"; !strings.Contains(all, want) {
		t.Errorf("no migration queues %q: %s", kindPushDevicesOff, want)
	}
	if want := "'" + kindPushDevicesOff + ":' || NEW.id"; !strings.Contains(all, want) {
		t.Errorf("the trigger's unique key no longer starts %q", kindPushDevicesOff+":")
	}
	want := "jsonb_build_object('" + jsonKey(t, devicesOffJob{}, "UserID") + "', NEW.id, '" +
		jsonKey(t, devicesOffJob{}, "Devices") + "', removed, '" + jsonKey(t, devicesOffJob{}, "Key") + "',"
	if !strings.Contains(all, want) {
		t.Errorf("the trigger's payload no longer matches devicesOffJob: %s", want)
	}
	db := testApp(t).DB
	var def int
	if err := db.Raw(`SELECT column_default::int FROM information_schema.columns WHERE table_name = 'jobs' AND column_name = 'max_attempts'`).Scan(&def).Error; err != nil {
		t.Fatal(err)
	}
	if def != pushDevicesOffType.Attempts() {
		t.Errorf("jobs.max_attempts defaults to %d, pushDevicesOffType says %d", def, pushDevicesOffType.Attempts())
	}
}

// The announcement keys start with their kind name, as the daily and hourly
// keys do.
func TestMessageJobKeysStartWithTheirKindName(t *testing.T) {
	for kind, key := range map[string]string{
		kindMessageSend:  messageSendKey(7, time.Unix(1700000000, 0)),
		kindMessageBatch: messageBatchKeyPrefix(7, ""),
	} {
		if !strings.HasPrefix(key, kind+":7:") {
			t.Errorf("%s: key %q", kind, key)
		}
	}
}
