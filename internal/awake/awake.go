// Package awake lets cove ask the host OS to stay awake for the duration of a
// session, so the machine does not idle-sleep while an agent is working.
package awake

// Inhibitor asks the host OS to stay awake until the returned release func is
// called. A nil error means the assertion is held and release tears it down;
// a non-nil error means no assertion is held (the caller decides what to do).
type Inhibitor interface {
	Inhibit() (release func(), err error)
}
