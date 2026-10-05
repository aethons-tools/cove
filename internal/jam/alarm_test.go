package jam

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC) // a Monday

func TestParseScheduleOneShot(t *testing.T) {
	next, err := ParseSchedule("2026-10-05T14:30:00Z", time.UTC, t0)
	if err != nil || !next.Equal(time.Date(2026, 10, 5, 14, 30, 0, 0, time.UTC)) {
		t.Fatalf("next=%v err=%v", next, err)
	}
	for _, bad := range []string{"2026-10-05T11:00:00Z", "2028-01-01T00:00:00Z"} { // past; > 366d
		if _, err := ParseSchedule(bad, time.UTC, t0); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestParseScheduleCronInZone(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	next, err := ParseSchedule("0 9 * * *", ny, t0) // 12:00Z = 08:00 EDT
	if err != nil || !next.Equal(time.Date(2026, 10, 5, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("next=%v err=%v; want 09:00 New York = 13:00Z", next, err)
	}
	if _, err := ParseSchedule("@daily", time.UTC, t0); err != nil {
		t.Fatalf("@daily: %v", err)
	}
}

func TestParseScheduleRejects(t *testing.T) {
	for _, bad := range []string{"", "soon", "@every 10s", "* * * * * *", "61 * * * *"} {
		if _, err := ParseSchedule(bad, time.UTC, t0); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := ParseSchedule("@every 5m", time.UTC, t0); err != nil {
		t.Errorf("@every 5m: %v", err)
	}
}

func TestNextAfterNoCatchUp(t *testing.T) {
	a := Alarm{Name: "h", Schedule: "0 * * * *"}
	if got := NextAfter(a, time.UTC, t0.Add(5*time.Hour+time.Minute)); !got.Equal(t0.Add(6 * time.Hour)) {
		t.Fatalf("next = %v, want the first match after now", got)
	}
	if got := NextAfter(Alarm{Schedule: "2026-10-05T14:30:00Z"}, time.UTC, t0); !got.IsZero() {
		t.Fatalf("one-shot next = %v, want zero", got)
	}
}

func TestValidateAlarmName(t *testing.T) {
	for _, ok := range []string{"pr-watch", "nightly_1", "a"} {
		if err := ValidateAlarmName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "PR", "-x", "a b", "a/b", string(make([]byte, 65))} {
		if err := ValidateAlarmName(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
