package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"fold/models"
)

func TestChurchZoneIn(t *testing.T) {
	for _, tt := range []struct {
		name, value, want string
		wantErr           bool
	}{
		{name: "missing", want: "UTC"},
		{name: "empty", value: " ", want: "UTC"},
		{name: "configured", value: "America/Boise", want: "America/Boise"},
		{name: "server local", value: "Local", wantErr: true},
		{name: "invalid", value: "Not/AZone", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			app := testApp(t)
			if tt.name != "missing" {
				if err := app.DB.Create(&models.Setting{Key: settingTimezone, Value: tt.value}).Error; err != nil {
					t.Fatal(err)
				}
			}
			// An empty value is unset, as a missing row is.
			loc, err := churchZoneIn(app.DB)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("church zone = %v; want an error", loc)
				}
				// Only the helper for pages and notices that must render anyway guesses.
				if got := churchZoneOrUTC(app.DB); got != time.UTC {
					t.Fatalf("churchZoneOrUTC = %v; want UTC", got)
				}
				return
			}
			if err != nil || loc.String() != tt.want || churchZoneOrUTC(app.DB).String() != tt.want {
				t.Fatalf("church zone = %v, %v; want %q", loc, err, tt.want)
			}
		})
	}
}

func TestChurchZoneInReturnsReadFailure(t *testing.T) {
	app := testApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db := app.DB.WithContext(ctx)
	if _, err := churchZoneIn(db); !errors.Is(err, context.Canceled) {
		t.Fatalf("time zone read failure = %v; want context cancellation", err)
	}
	if got, err := churchClockIn(db, time.Now()); !errors.Is(err, context.Canceled) || !got.IsZero() {
		t.Fatalf("church clock on a failed read = %v, %v; want zero and cancellation", got, err)
	}
	if got := churchZoneOrUTC(db); got != time.UTC {
		t.Fatalf("pages that must render show %v on a failed read; want UTC", got)
	}
}

func TestUserZone(t *testing.T) {
	boise, _ := time.LoadLocation("America/Boise")
	for _, tt := range []struct {
		tz     string
		church *time.Location
		want   string
	}{
		{"Asia/Tokyo", boise, "Asia/Tokyo"},
		{"", boise, "America/Boise"},
		{"Local", boise, "America/Boise"},
		{"Not/AZone", boise, "America/Boise"},
		{"", nil, "UTC"},
	} {
		if got := userZone(tt.tz, tt.church).String(); got != tt.want {
			t.Errorf("userZone(%q, %v) = %s; want %s", tt.tz, tt.church, got, tt.want)
		}
	}
	if got := eventZone("Local").String(); got != "UTC" {
		t.Errorf("eventZone(Local) = %s; want UTC", got)
	}
}

func TestChurchZoneLenient(t *testing.T) {
	app := testApp(t)
	if err := app.DB.Create(&models.Setting{Key: settingTimezone, Value: "Not/AZone"}).Error; err != nil {
		t.Fatal(err)
	}
	if loc, err := churchZoneLenient(app.DB); err != nil || loc != time.UTC {
		t.Fatalf("damaged value = %v, %v; want UTC, nil", loc, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := churchZoneLenient(app.DB.WithContext(ctx)); !errors.Is(err, context.Canceled) {
		t.Fatalf("failed read = %v; want it returned", err)
	}
}
