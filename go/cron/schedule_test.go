package cron

import (
	"testing"
	"time"
)

func TestEveryFiveMinutesUTC(t *testing.T) {
	after := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	next, err := NextRunAt("0 */5 * * * *", "UTC", after)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("got %v, want %v", next, want)
	}
}

func TestDaily9amRespectsTimezoneOffset(t *testing.T) {
	// 9am in Asia/Ho_Chi_Minh (UTC+7) is 2am UTC.
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	next, err := NextRunAt("0 0 9 * * *", "Asia/Ho_Chi_Minh", after)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("got %v, want %v", next, want)
	}
}

func TestRejectsBadExpression(t *testing.T) {
	if ValidateSchedule("not a cron expr", "UTC") == nil {
		t.Fatal("expected an error")
	}
}

func TestRejectsBadTimezone(t *testing.T) {
	if ValidateSchedule("0 */5 * * * *", "Not/A_Zone") == nil {
		t.Fatal("expected an error")
	}
}
