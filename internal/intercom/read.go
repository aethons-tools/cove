package intercom

import "time"

// Filter selects messages for List. Zero fields are unbounded.
type Filter struct {
	Project string
	Since   time.Time // inclusive lower bound; zero = unbounded
	Until   time.Time // exclusive upper bound; zero = unbounded
}

// ReadInbox returns, in append order, the messages addressed to t (t ∈ To).
func (l *Log) ReadInbox(t Target) []Squawk {
	var out []Squawk
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
func (l *Log) ReadThread(rootID string) []Squawk {
	var out []Squawk
	for _, m := range l.snapshot() {
		if m.ID == rootID || m.ReplyTo == rootID {
			out = append(out, m)
		}
	}
	return out
}

// List returns messages matching f, in append order.
func (l *Log) List(f Filter) []Squawk {
	var out []Squawk
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

// ListSince returns messages with Seq > afterSeq, in append order, capped at
// limit (afterSeq <= 0 = from start).
func (l *Log) ListSince(afterSeq int64, limit int) []Squawk {
	var out []Squawk
	for _, m := range l.snapshot() {
		if m.Seq <= afterSeq {
			continue
		}
		out = append(out, m)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

// ReadInboxSince returns messages addressed to t with Seq > afterSeq, capped
// at limit (afterSeq <= 0 = from start).
func (l *Log) ReadInboxSince(t Target, afterSeq int64, limit int) []Squawk {
	var out []Squawk
	for _, m := range l.snapshot() {
		if m.Seq <= afterSeq {
			continue
		}
		for _, r := range m.To {
			if r == t {
				out = append(out, m)
				break
			}
		}
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

// ReadInboxBefore returns messages addressed to t with Seq < beforeSeq, the
// `limit` nearest below beforeSeq, in append (ascending) order (limit <= 0 =
// unbounded). beforeSeq <= 0 means "from the end" (the last `limit`).
func (l *Log) ReadInboxBefore(t Target, beforeSeq int64, limit int) []Squawk {
	var out []Squawk
	for _, m := range l.snapshot() {
		if beforeSeq > 0 && m.Seq >= beforeSeq {
			continue
		}
		for _, r := range m.To {
			if r == t {
				out = append(out, m)
				break
			}
		}
	}
	// keep the last `limit` (nearest below beforeSeq), preserving ascending order.
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// SeqOf returns the append-order Seq assigned to the message with the given
// id, or (0, false) if no such message exists.
func (l *Log) SeqOf(id string) (int64, bool) {
	for _, m := range l.snapshot() {
		if m.ID == id {
			return m.Seq, true
		}
	}
	return 0, false
}

// TailSeq returns the last message's Seq, or (0, false) if the log is empty.
func (l *Log) TailSeq() (int64, bool) {
	ms := l.snapshot()
	if len(ms) == 0 {
		return 0, false
	}
	return ms[len(ms)-1].Seq, true
}
