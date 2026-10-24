package sessionevents

import (
	"sync"
	"time"
)

// Totals are a session's aggregates, derived from its events — numbers only,
// never their content, so they may be shown wherever the session is (the
// /app/user session view), unlike the events themselves.
type Totals struct {
	Turns                     uint32 // results seen: prompts answered
	Episodes                  uint32 // claude processes: max turn
	ToolCalls                 int
	Errors                    int // results that were errors
	InputTokens, OutputTokens int64
	CostUSD                   float64   // total, across episodes
	TurnCostUSD               float64   // the latest episode's cost
	LastAt                    time.Time // the last event observed
	// episodeCost is the latest total_cost_usd per episode. claude
	// reports it cumulatively per process, so only the last one counts.
	episodeCost map[episode]float64
	lastResult  time.Time
}

// episode is one claude process: a turn number within a stream.
type episode struct {
	stream string
	turn   uint32
}

// Add folds ev into t.
func (t *Totals) Add(ev Event) {
	if ev.Kind != KindEvent {
		return
	}
	if ev.ObservedAt.After(t.LastAt) {
		t.LastAt = ev.ObservedAt
	}
	if ev.Turn > t.Episodes {
		t.Episodes = ev.Turn
	}
	if ev.Index.Type == "assistant" && ev.Index.ToolName != "" {
		t.ToolCalls++
	}
	if ev.Index.Type != "result" {
		return
	}
	t.Turns++
	if ev.Index.IsError {
		t.Errors++
	}
	t.InputTokens += ev.Index.InputTokens
	t.OutputTokens += ev.Index.OutputTokens
	if t.episodeCost == nil {
		t.episodeCost = map[episode]float64{}
	}
	ep := episode{ev.StreamID, ev.Turn}
	t.CostUSD += ev.Index.CostUSD - t.episodeCost[ep]
	t.episodeCost[ep] = ev.Index.CostUSD
	if !ev.ObservedAt.Before(t.lastResult) { // the latest result's episode
		t.lastResult, t.TurnCostUSD = ev.ObservedAt, ev.Index.CostUSD
	}
}

// TotalsCache folds each actor's events (every stream) into Totals, reading
// only what is new since the last call.
type TotalsCache struct {
	store Store
	mu    sync.Mutex
	byAct map[string]*actorTotals
}

type actorTotals struct {
	tot  Totals
	seen map[string]uint64 // stream → last seq folded
}

// NewTotalsCache returns a cache over store.
func NewTotalsCache(store Store) *TotalsCache {
	return &TotalsCache{store: store, byAct: map[string]*actorTotals{}}
}

// Of is actorID's totals, brought up to date.
func (c *TotalsCache) Of(actorID string) (Totals, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.byAct[actorID]
	if a == nil {
		a = &actorTotals{seen: map[string]uint64{}}
		c.byAct[actorID] = a
	}
	streams, err := c.store.Streams(actorID)
	if err != nil {
		return a.tot, err
	}
	for _, st := range streams {
		if st.LastSeq <= a.seen[st.StreamID] {
			continue
		}
		evs, err := c.store.List(Filter{ActorID: actorID, StreamID: st.StreamID, AfterSeq: a.seen[st.StreamID]})
		if err != nil {
			return a.tot, err
		}
		for _, ev := range evs {
			a.tot.Add(ev)
			a.seen[st.StreamID] = ev.Seq
		}
	}
	return a.tot, nil
}
