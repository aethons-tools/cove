package msglog

import "time"

// Filter selects messages for List. Zero fields are unbounded.
type Filter struct {
	Project string
	Since   time.Time // inclusive lower bound; zero = unbounded
	Until   time.Time // exclusive upper bound; zero = unbounded
}

// ReadInbox returns, in append order, the messages addressed to t (t ∈ To).
func (l *Log) ReadInbox(t Target) []Message {
	var out []Message
	for _, m := range l.snapshot() {
		for _, r := range m.To {
			if r == t {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

// ReadThread returns the root message (ID == rootID) followed by its direct
// replies (ReplyTo == rootID), in append order. Deep (multi-level) threads are
// deferred.
func (l *Log) ReadThread(rootID string) []Message {
	var out []Message
	for _, m := range l.snapshot() {
		if m.ID == rootID || m.ReplyTo == rootID {
			out = append(out, m)
		}
	}
	return out
}

// List returns messages matching f, in append order.
func (l *Log) List(f Filter) []Message {
	var out []Message
	for _, m := range l.snapshot() {
		if f.Project != "" && m.Project != f.Project {
			continue
		}
		if !f.Since.IsZero() && m.At.Before(f.Since) {
			continue
		}
		if !f.Until.IsZero() && !m.At.Before(f.Until) {
			continue
		}
		out = append(out, m)
	}
	return out
}
