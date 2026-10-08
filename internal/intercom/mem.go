package intercom

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/aethons-tools/cove/internal/ident"
)

// Log is an in-memory channel log, for tests and tooling (serve's is
// intercompg). It continues an optional legacy log, as the Postgres log
// continues its legacy tables.
type Log struct {
	mu         sync.Mutex
	legacy     LegacyStore
	cutover    int64
	nextSeq    int64
	msgs       []Squawk
	deliveries map[int64][]ident.ID // seq → audience
}

// NewMemLog returns an empty channel log continuing legacy (nil: none).
func NewMemLog(legacy LegacyStore) *Log {
	l := &Log{legacy: legacy, cutover: 1, deliveries: map[int64][]ident.ID{}}
	if legacy != nil {
		if tail, ok := legacy.TailSeq(); ok {
			l.cutover = tail + 1
		}
	}
	l.nextSeq = l.cutover
	return l
}

var _ Store = (*Log)(nil)

func (l *Log) Close() error { return nil }

func (l *Log) CutoverSeq() int64 { return l.cutover }

func (l *Log) Append(m Squawk, audience []ident.ID) (Squawk, error) {
	m, err := Prepare(m)
	if err != nil {
		return Squawk{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.seqOf(m.ID); ok {
		return Squawk{}, fmt.Errorf("%w: %q", ErrDuplicateID, m.ID)
	}
	m.Seq = l.nextSeq
	l.nextSeq++
	l.msgs = append(l.msgs, m)
	aud := slices.Clone(audience)
	slices.Sort(aud)
	l.deliveries[m.Seq] = slices.Compact(aud)
	return m, nil
}

// since returns the squawks keep selects after a seq, ascending, the first
// limit (<= 0: all).
func (l *Log) since(keep func(Squawk) bool, afterSeq int64, limit int) []Squawk {
	out := l.matching(keep, func(seq int64) bool { return seq > afterSeq })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// before returns the limit squawks keep selects nearest below a seq (<= 0:
// the end), ascending.
func (l *Log) before(keep func(Squawk) bool, beforeSeq int64, limit int) []Squawk {
	out := l.matching(keep, func(seq int64) bool { return beforeSeq <= 0 || seq < beforeSeq })
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func (l *Log) matching(keep func(Squawk) bool, inRange func(int64) bool) []Squawk {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Squawk
	for _, m := range l.msgs {
		if inRange(m.Seq) && keep(m) {
			out = append(out, m)
		}
	}
	return out
}

func (l *Log) delivered(p ident.ID) func(Squawk) bool {
	return func(m Squawk) bool { return slices.Contains(l.deliveries[m.Seq], p) }
}

func (l *Log) inChannel(ch ident.ID) func(Squawk) bool {
	return func(m Squawk) bool { return m.Channel == ch }
}

func (l *Log) InboxSince(p ident.ID, afterSeq int64, limit int) []Squawk {
	return l.since(l.delivered(p), afterSeq, limit)
}

func (l *Log) InboxBefore(p ident.ID, beforeSeq int64, limit int) []Squawk {
	return l.before(l.delivered(p), beforeSeq, limit)
}

func (l *Log) ChannelSince(ch ident.ID, afterSeq int64, limit int) []Squawk {
	return l.since(l.inChannel(ch), afterSeq, limit)
}

func (l *Log) ChannelBefore(ch ident.ID, beforeSeq int64, limit int) []Squawk {
	return l.before(l.inChannel(ch), beforeSeq, limit)
}

func (l *Log) ListSince(afterSeq int64, limit int) []Squawk {
	return l.since(func(Squawk) bool { return true }, afterSeq, limit)
}

func (l *Log) ReadThread(rootID string) []Squawk {
	return l.since(func(m Squawk) bool { return m.ID == rootID || m.ReplyTo == rootID }, 0, 0)
}

func (l *Log) InboxChannels(p ident.ID) []ChannelStat {
	last := map[ident.ID]int64{}
	for _, m := range l.since(l.delivered(p), 0, 0) {
		last[m.Channel] = m.Seq
	}
	var out []ChannelStat
	for ch, seq := range last {
		out = append(out, ChannelStat{Channel: ch, LastSeq: seq})
	}
	slices.SortFunc(out, func(a, b ChannelStat) int { return strings.Compare(string(a.Channel), string(b.Channel)) })
	return out
}

func (l *Log) SeqOf(id string) (int64, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seqOf(id)
}

// seqOf resolves id in either log. Caller holds mu.
func (l *Log) seqOf(id string) (int64, bool) {
	for _, m := range l.msgs {
		if m.ID == id {
			return m.Seq, true
		}
	}
	if l.legacy != nil {
		return l.legacy.SeqOf(id)
	}
	return 0, false
}

func (l *Log) SeenIDs(prefix string) []string {
	var out []string
	if l.legacy != nil {
		out = l.legacy.SeenIDs(prefix)
	}
	for _, m := range l.ListSince(0, 0) {
		if strings.HasPrefix(m.ID, prefix) {
			out = append(out, m.ID)
		}
	}
	return out
}

func (l *Log) TailSeq() (int64, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n := len(l.msgs); n > 0 {
		return l.msgs[n-1].Seq, true
	}
	if l.legacy != nil {
		return l.legacy.TailSeq()
	}
	return 0, false
}
