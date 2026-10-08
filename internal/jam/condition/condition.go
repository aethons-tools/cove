// Package condition tracks operator-attention conditions: named, severity-tagged
// problems Jam itself detects (a lapsed brokered credential, a failing pool
// refresh, …). It owns the model, the in-memory Tracker with async
// write-through persistence, the Prometheus exposition and the admin JSON
// view. It is a leaf package: producers in internal/jam call into it.
// See docs/usage/jam/monitoring.md.
package condition

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Severity is how urgently a condition needs an operator.
type Severity string

const (
	Critical Severity = "critical" // agents are failing now
	Warning  Severity = "warning"  // degraded, or will fail
	Info     Severity = "info"     // shown, never pushed
)

// Field caps (bytes).
const (
	MaxKey     = 200
	MaxSummary = 200
	MaxDetail  = 2048
	MaxFix     = 500
)

// Condition is one problem that is (or was) true. Text fields name things
// only — never secret values, tokens or raw upstream bodies.
type Condition struct {
	Key        string     `json:"key"`
	Severity   Severity   `json:"severity"`
	Summary    string     `json:"summary"`
	Detail     string     `json:"detail,omitempty"`
	Fix        string     `json:"fix,omitempty"`
	Since      time.Time  `json:"since"`
	LastSeen   time.Time  `json:"last_seen"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

// Kind is the key's prefix (before the first ':').
func (c Condition) Kind() string { k, _, _ := strings.Cut(c.Key, ":"); return k }

// IsOpen reports whether the condition has not been resolved.
func (c Condition) IsOpen() bool { return c.ResolvedAt == nil }

var keyRE = regexp.MustCompile(`^[a-z0-9._-]+:[a-z0-9._:/-]+$`)

var keyUnsafe = regexp.MustCompile(`[^a-z0-9._:/-]+`)

// Key builds a valid condition key from a kind and a subject (a name):
// lowercased, runs of other characters become '-', capped at MaxKey.
func Key(kind, subject string) string {
	k := kind + ":" + keyUnsafe.ReplaceAllString(strings.ToLower(subject), "-")
	if len(k) > MaxKey {
		k = k[:MaxKey]
	}
	return k
}

func validKey(k string) bool { return len(k) <= MaxKey && keyRE.MatchString(k) }

func validSeverity(s Severity) bool { return s == Critical || s == Warning || s == Info }

// clip shortens s to at most n bytes on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func sevRank(s Severity) int {
	switch s {
	case Critical:
		return 0
	case Warning:
		return 1
	}
	return 2
}
