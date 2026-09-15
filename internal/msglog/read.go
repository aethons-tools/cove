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

// ListSince returns messages with id > afterID, in append order, capped at limit.
func (l *Log) ListSince(afterID string, limit int) []Message {
	var out []Message
	for _, m := range l.snapshot() {
		if m.ID <= afterID {
			continue
		}
		out = append(out, m)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

// ReadInboxSince returns messages addressed to t with id > afterID, capped at limit.
func (l *Log) ReadInboxSince(t Target, afterID string, limit int) []Message {
	var out []Message
	for _, m := range l.snapshot() {
		if m.ID <= afterID {
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

// ReadInboxBefore returns messages addressed to t with id < beforeID, the
// `limit` nearest below beforeID, in append (ascending) order (limit <= 0 =
// unbounded). beforeID == "" means "from the end" (the last `limit`).
func (l *Log) ReadInboxBefore(t Target, beforeID string, limit int) []Message {
	var out []Message
	for _, m := range l.snapshot() {
		if beforeID != "" && m.ID >= beforeID {
			continue
		}
		for _, r := range m.To {
			if r == t {
				out = append(out, m)
				break
			}
		}
	}
	// keep the last `limit` (nearest below beforeID), preserving ascending order.
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// TailID returns the last message's id, or ("", false) if the log is empty.
func (l *Log) TailID() (string, bool) {
	ms := l.snapshot()
	if len(ms) == 0 {
		return "", false
	}
	return ms[len(ms)-1].ID, true
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
