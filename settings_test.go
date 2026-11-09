package main

import "testing"

// settingOnSQL and boolSettingIn(key, false) are one rule, in SQL and in Go.
func TestSettingOnSQLAgreesWithTheGoReader(t *testing.T) {
	a := testApp(t)
	for _, v := range []string{"true", "false", " true ", "yes", "", "TRUE", "1"} {
		if err := setSettingIn(a.DB, settingRoomsEnabled, v); err != nil {
			t.Fatal(err)
		}
		var sql bool
		if err := a.DB.Raw("SELECT "+settingOnSQL, settingRoomsEnabled).Scan(&sql).Error; err != nil {
			t.Fatal(err)
		}
		if goOn := boolSettingIn(a.DB, settingRoomsEnabled, false); sql != goOn {
			t.Errorf("value %q: SQL %v, Go %v", v, sql, goOn)
		}
	}
	a.DB.Exec("DELETE FROM settings WHERE key = ?", settingRoomsEnabled)
	var sql bool
	if err := a.DB.Raw("SELECT "+settingOnSQL, settingRoomsEnabled).Scan(&sql).Error; err != nil {
		t.Fatal(err)
	}
	if sql || boolSettingIn(a.DB, settingRoomsEnabled, false) {
		t.Error("a missing row is off in both")
	}
}
