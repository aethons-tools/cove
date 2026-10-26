// Package scheduler holds the tracker vocabulary Jam's Requisitioner and its
// trackers share: lifecycle Roles, the Issue and Comment shapes, label
// classification (ClassifyLabels) and the brief a raised session receives
// (AssembleBrief).
package scheduler

import "strings"

// Role is a lifecycle role Jam transitions issues through. A tracker maps
// each role to this team's configured state name and then to a tracker state id.
type Role int

const (
	RoleReady Role = iota
	RoleInProgress
	RoleInReview
	RoleNeedsInput
	RoleBlocked
	RoleDone
)

// Issue is the subset of a tracker issue Jam needs.
type Issue struct {
	ID          string // tracker-internal id (for API calls)
	Identifier  string // human key, e.g. "AET-42"
	Title       string
	Description string
	Class       string // parsed from the class label; "" if none
	// DispatchLabeled is true when the issue carries a label matching the
	// tracker's dispatch-label prefix (default "dispatch:"). Jam's resident
	// dispatcher raises a cove only for labeled issues. Presence-only — the value after the prefix is unused.
	DispatchLabeled bool
}

// Comment is one entry in an issue's thread.
type Comment struct {
	Author string
	Body   string
}

// ClassifyLabels derives an issue's handler class (the value after
// classPrefix; the first match wins) and whether it is tagged for dispatch
// (any label under dispatchPrefix; presence-only). An empty dispatchPrefix
// means no gate: every issue is dispatchable.
func ClassifyLabels(labels []string, classPrefix, dispatchPrefix string) (class string, dispatchLabeled bool) {
	dispatchLabeled = dispatchPrefix == ""
	for _, name := range labels {
		if class == "" && strings.HasPrefix(name, classPrefix) {
			class = strings.TrimPrefix(name, classPrefix)
		}
		if dispatchPrefix != "" && strings.HasPrefix(name, dispatchPrefix) {
			dispatchLabeled = true
		}
	}
	return class, dispatchLabeled
}
