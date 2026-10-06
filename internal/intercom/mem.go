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
		return Squawk{}, fmt.Errorf("intercom: duplicate id %q", m.ID)
	}
	m.Seq = l.nextSeq
	l.nextSeq++
	l.msgs = append(l.msgs, m)
	aud := slices.Clone(audience)
	slices.Sort(aud)
	l.deliveries[m.Seq] = slices.Compact(aud)
	return m, nil
}

// filter returns the squawks keep selects, ascending; before/after bound the
// seq (0 = unbounded) and limit keeps the first (after) or last (before) n.
func (l *Log) filter(keep func(Squawk) bool, afterSeq, beforeSeq int64, limit int) []Squawk {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Squawk
	for _, m := range l.msgs {
		if m.Seq <= afterSeq || (beforeSeq > 0 && m.Seq >= beforeSeq) || !keep(m) {
			continue
		}
		out = append(out, m)
	}
	if limit > 0 && len(out) > limit {
		if beforeSeq != 0 || afterSeq < 0 {
			out = out[len(out)-limit:]
		} else {
			out = out[:limit]
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
	return l.filter(l.delivered(p), afterSeq, 0, limit)
}

func (l *Log) InboxBefore(p ident.ID, beforeSeq int64, limit int) []Squawk {
	return l.filter(l.delivered(p), -1, beforeSeq, limit)
}

func (l *Log) ChannelSince(ch ident.ID, afterSeq int64, limit int) []Squawk {
	return l.filter(l.inChannel(ch), afterSeq, 0, limit)
}

func (l *Log) ChannelBefore(ch ident.ID, beforeSeq int64, limit int) []Squawk {
	return l.filter(l.inChannel(ch), -1, beforeSeq, limit)
}

func (l *Log) ListSince(afterSeq int64, limit int) []Squawk {
	return l.filter(func(Squawk) bool { return true }, afterSeq, 0, limit)
}

func (l *Log) ReadThread(rootID string) []Squawk {
	return l.filter(func(m Squawk) bool { return m.ID == rootID || m.ReplyTo == rootID }, 0, 0, 0)
}

func (l *Log) InboxChannels(p ident.ID) []ChannelStat {
	last := map[ident.ID]int64{}
	for _, m := range l.filter(l.delivered(p), 0, 0, 0) {
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
