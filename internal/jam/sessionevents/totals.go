package sessionevents

import (
	"strings"
	"sync"
	"time"
)

// Totals are a session's aggregates, derived from its events — numbers only,
// never their content, so they may be shown wherever the session is (the
// /app/user session view), unlike the events themselves.
type Totals struct {
	Turns                     uint32 // results seen: prompts answered
	Episodes                  uint32 // claude processes (distinct stream+turn)
	ToolCalls                 int
	Errors                    int // results that were errors
	InputTokens, OutputTokens int64
	CostUSD                   float64   // total, across episodes
	TurnCostUSD               float64   // the latest turn's cost (its result's delta)
	LastAt                    time.Time // the last event observed
	// Context is the main thread's context size after its latest model call;
	// 0 = unknown (none yet, or reset by a compaction or Clear since).
	Context      int64
	ContextModel string // the model of that call
	// ContextWindow is ContextModel's window, from the latest result that
	// reported it; 0 = unknown.
	ContextWindow int64
	contextAt     time.Time
	windows       map[string]int64
	// episodeCost is the latest total_cost_usd per episode. claude
	// reports it cumulatively per process, so only the last one counts.
	episodeCost map[episode]float64
	episodes    map[episode]bool
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
	ep := episode{ev.StreamID, ev.Turn}
	if !t.episodes[ep] {
		if t.episodes == nil {
			t.episodes = map[episode]bool{}
		}
		t.episodes[ep] = true
		t.Episodes++
	}
	if ev.Index.Type == "assistant" && ev.Index.ToolName != "" {
		t.ToolCalls++
	}
	t.addContext(ev)
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
	turn := ev.Index.CostUSD - t.episodeCost[ep] // a result's cost is cumulative over its episode
	t.CostUSD += turn
	t.episodeCost[ep] = ev.Index.CostUSD
	if !ev.ObservedAt.Before(t.lastResult) { // the latest turn
		t.lastResult, t.TurnCostUSD = ev.ObservedAt, turn
	}
}

// addContext folds ev's context update, if any, into t. Streams are folded
// one after another, so the latest by observation time wins.
func (t *Totals) addContext(ev Event) {
	c, ok := deriveContext(ev)
	if !ok {
		return
	}
	for m, w := range c.Windows {
		if t.windows == nil {
			t.windows = map[string]int64{}
		}
		t.windows[m] = w
	}
	if (c.Tokens > 0 || c.Reset) && !ev.ObservedAt.Before(t.contextAt) {
		t.contextAt, t.Context = ev.ObservedAt, c.Tokens
		if c.Tokens > 0 {
			t.ContextModel = c.Model
		}
	}
	t.ContextWindow = windowOf(t.windows, t.ContextModel)
}

// windowOf is model's window in windows. A result may key a model with a
// variant suffix (e.g. "claude-x[1m]") that the message's model omits.
func windowOf(windows map[string]int64, model string) int64 {
	if w, ok := windows[model]; ok || model == "" {
		return w
	}
	for m, w := range windows {
		if base, _, ok := strings.Cut(m, "["); ok && base == model {
			return w
		}
	}
	return 0
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
