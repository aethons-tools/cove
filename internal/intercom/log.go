package intercom

import (
	"strings"
	"sync"
)

// LegacyLog is an in-memory, append-only message log implementing Store, for tests
// and tooling. Not for production: nothing is persisted — Jam's message log is
// intercompg (Postgres).
type LegacyLog struct {
	mu      sync.Mutex
	msgs    []LegacySquawk
	nextSeq int64 // next Seq to assign, from 1
}

// NewLegacyMemLog returns an empty in-memory log.
func NewLegacyMemLog() *LegacyLog { return &LegacyLog{nextSeq: 1} }

// Close is a no-op; it exists to satisfy Store.
func (l *LegacyLog) Close() error { return nil }

// Append validates m, assigns an ID/At when unset and the next Seq, and stores
// it. Returns the stored message.
func (l *LegacyLog) Append(m LegacySquawk) (LegacySquawk, error) {
	m, err := PrepareLegacy(m)
	if err != nil {
		return LegacySquawk{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	m.Seq = l.nextSeq
	l.nextSeq++
	l.msgs = append(l.msgs, m)
	return m, nil
}

// snapshot returns a copy of the stored messages (deep enough that callers
// can't mutate stored To slices). Reads in read.go build on this.
func (l *LegacyLog) snapshot() []LegacySquawk {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]LegacySquawk, len(l.msgs))
	for i, m := range l.msgs {
		m.To = append([]Target(nil), m.To...)
		out[i] = m
	}
	return out
}

// SeenIDs returns ids with the given prefix, in append order.
func (l *LegacyLog) SeenIDs(prefix string) []string {
	var out []string
	for _, m := range l.snapshot() {
		if strings.HasPrefix(m.ID, prefix) {
			out = append(out, m.ID)
		}
	}
	return out
}

var _ LegacyStore = (*LegacyLog)(nil)
