package jam

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/robfig/cron/v3"
)

// Alarm limits (turn-end spec §1).
const (
	MaxAlarms     = 20
	maxAlarmNote  = 1000
	maxAlarmAhead = 366 * 24 * time.Hour
	maxGateBytes  = 4096
	// GateTimeout is how long cove-master lets a gate run; GateGrace is how
	// much longer Jam waits for its result before calling the gate failed.
	GateTimeout = 60 * time.Second
	GateGrace   = 30 * time.Second
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
	// Gate is an optional shell command cove-master runs when the alarm comes
	// due; only its verdict decides whether the alarm fires.
	Gate       string       `json:"gate,omitempty"`
	GateRun    *GateRun     `json:"gate_run,omitempty"`    // a gate run in flight
	LastGate   *GateOutcome `json:"last_gate,omitempty"`   // the latest gate result
	FireKind   string       `json:"fire_kind,omitempty"`   // WakeAlarm | WakeGateFailed, set with FiredAt
	FireDetail string       `json:"fire_detail,omitempty"` // the gate's output, or why it failed
}

// GateRun is a gate run in flight.
type GateRun struct {
	RunID     string    `json:"run_id"`
	StartedAt time.Time `json:"started_at"`
}

// GateOutcome is a gate run's result.
type GateOutcome struct {
	At       time.Time `json:"at"`
	Exit     int       `json:"exit"`
	TimedOut bool      `json:"timed_out,omitempty"`
	NoResult bool      `json:"no_result,omitempty"` // the cove never answered within GateTimeout+GateGrace
	Output   string    `json:"output,omitempty"`
}

// Gate verdicts (GateOutcome.Verdict).
const (
	GatePass   = "pass"    // exit 0: wake with the output
	GateFailed = "failed"  // couldn't answer: wake with the cause
	GateNotYet = "not-yet" // any other exit: keep sleeping
)

// Verdict classifies the outcome: exit 0 passes; a timeout, no result, a
// command the shell couldn't find (127) or run (126), or a kill by a signal
// (128+n) failed; anything else is not yet.
func (o GateOutcome) Verdict() string {
	switch {
	case o.TimedOut || o.NoResult || o.Exit == 126 || o.Exit == 127 || o.Exit > 128:
		return GateFailed
	case o.Exit == 0:
		return GatePass
	}
	return GateNotYet
}

// maxGateOutput caps a gate's output as Jam stores it (the cove caps it too;
// Jam does not trust that).
const maxGateOutput = 4096

// CleanGateOutput makes a cove-reported gate output safe to store: valid
// UTF-8 with no NUL (Postgres jsonb rejects it), capped on a rune boundary,
// marked when cut.
func CleanGateOutput(b []byte, truncated bool) string {
	const marker = "\n[output truncated]"
	s := strings.ReplaceAll(strings.ToValidUTF8(string(b), "\uFFFD"), "\x00", "\uFFFD")
	s, had := strings.CutSuffix(s, marker)
	truncated = truncated || had
	if len(s) > maxGateOutput {
		cut := maxGateOutput
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s, truncated = s[:cut], true
	}
	if truncated {
		s += marker
	}
	return s
}

// gateFailure says why a failed gate couldn't answer, with its output.
func gateFailure(o GateOutcome) string {
	var why string
	switch {
	case o.NoResult:
		why = fmt.Sprintf("no result from the cove within %s", GateTimeout+GateGrace)
	case o.TimedOut:
		why = fmt.Sprintf("timed out after %s", GateTimeout)
	case o.Exit == 127:
		why = "exit 127 (command not found)"
	case o.Exit > 128:
		why = fmt.Sprintf("killed by signal %d", o.Exit-128)
	default:
		why = "exit 126 (not executable)"
	}
	if o.Output != "" {
		why += ":\n" + o.Output
	}
	return why
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
