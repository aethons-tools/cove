package jam

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// Alarm limits (turn-end spec §1).
const (
	MaxAlarms     = 20
	maxAlarmNote  = 1000
	maxAlarmAhead = 366 * 24 * time.Hour
)

// Alarm errors the /alarms handler maps to 400 and 404.
var (
	ErrAlarmLimit  = errors.New("alarm limit reached (20 per session)")
	ErrNoSuchAlarm = errors.New("no such alarm")
)

var alarmName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Alarm is one of a session's named wake conditions (the alarm_* tools): a
// one-shot RFC 3339 time or a cron schedule in the role's time zone. See
// docs/usage/jam/turn-end.md#alarms.
type Alarm struct {
	Name     string    `json:"name"`
	Schedule string    `json:"schedule"`
	Note     string    `json:"note,omitempty"`
	NextAt   time.Time `json:"next_at,omitzero"`  // zero once a one-shot has fired
	FiredAt  time.Time `json:"fired_at,omitzero"` // fired, awaiting the session's next run
}

// OneShot reports whether the schedule is a single RFC 3339 time.
func (a Alarm) OneShot() bool {
	_, err := time.Parse(time.RFC3339, a.Schedule)
	return err == nil
}

// ValidateAlarmName accepts lowercase letters, digits, '-' and '_' (≤ 64).
func ValidateAlarmName(name string) error {
	if !alarmName.MatchString(name) {
		return fmt.Errorf("alarm name must match %s", alarmName)
	}
	return nil
}

// ParseSchedule validates s and returns its first fire time after now: a
// one-shot RFC 3339 time (future, within 366 days) or a 5-field cron
// expression (or descriptor) evaluated in loc, at most once a minute.
func ParseSchedule(s string, loc *time.Location, now time.Time) (time.Time, error) {
	if at, err := time.Parse(time.RFC3339, s); err == nil {
		if !at.After(now) || at.Sub(now) > maxAlarmAhead {
			return time.Time{}, fmt.Errorf("an at time must be in the future and within 366 days")
		}
		return at, nil
	}
	sched, err := parseCron(s)
	if err != nil {
		return time.Time{}, err
	}
	first := sched.Next(now.In(loc))
	if sched.Next(first).Sub(first) < time.Minute {
		return time.Time{}, fmt.Errorf("a recurring alarm may fire at most once a minute")
	}
	return first, nil
}

// NextAfter is a cron alarm's next fire time after now (no catch-up); zero for
// a one-shot.
func NextAfter(a Alarm, loc *time.Location, now time.Time) time.Time {
	if a.OneShot() {
		return time.Time{}
	}
	sched, err := parseCron(a.Schedule)
	if err != nil {
		return time.Time{}
	}
	return sched.Next(now.In(loc))
}

func parseCron(s string) (cron.Schedule, error) {
	const want = "schedule must be an RFC 3339 time or a 5-field cron expression"
	if len(strings.Fields(s)) == 6 {
		return nil, errors.New(want)
	}
	sched, err := cron.ParseStandard(s)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", want, err)
	}
	return sched, nil
}
