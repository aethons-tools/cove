package sessionevents

import (
	"slices"
	"sort"
	"strconv"
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
	// LastInput is that call's input (cache reads and writes included):
	// what keep-warm compares with a role's cache.threshold. 0 = unknown, as
	// Context.
	LastInput int64
	// Resets counts the compactions and Clears seen, so a caller can tell
	// whether one happened since it last looked.
	Resets int
	// ContextWindow is ContextModel's window, from the latest result that
	// reported it; 0 = unknown.
	ContextWindow int64
	contextAt     time.Time
	windows       map[string]int64
	// sessionCost is each claude session's total_cost_usd per result, in
	// ObservedAt order. claude reports it cumulatively per session, carried
	// across --continue and into a resumed session's new stream, so only the
	// growth since the previous result in time is new spend. Results can be
	// folded out of time order (an older stream's late events), so each is
	// placed between its neighbours rather than taken against the last one
	// folded.
	sessionCost map[string][]costPoint
	episodes    map[episode]bool
	lastResult  time.Time
	lastKey     string // the claude session of the latest result
}

// costPoint is one result's cumulative cost.
type costPoint struct {
	at   time.Time
	cost float64
}

// costStep is the new spend a result with cumulative cost reports after
// one with prev.
func costStep(prev, cost float64) float64 {
	if cost < prev { // claude's total restarted (its saved state was lost)
		return cost
	}
	return cost - prev
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
	if t.sessionCost == nil {
		t.sessionCost = map[string][]costPoint{}
	}
	key := ev.Index.ClaudeSessionID
	if key == "" { // no session id: assume cumulative over the episode only
		key = ep.stream + "/" + strconv.FormatUint(uint64(ep.turn), 10)
	}
	pts := t.sessionCost[key]
	i := sort.Search(len(pts), func(i int) bool { return pts[i].at.After(ev.ObservedAt) })
	var prev float64
	if i > 0 {
		prev = pts[i-1].cost
	}
	turn := costStep(prev, ev.Index.CostUSD)
	t.CostUSD += turn
	if i < len(pts) { // a late result: the next one's step is now from this one
		next := costStep(ev.Index.CostUSD, pts[i].cost)
		t.CostUSD += next - costStep(prev, pts[i].cost)
		if i == len(pts)-1 && key == t.lastKey {
			t.TurnCostUSD = next
		}
	}
	t.sessionCost[key] = slices.Insert(pts, i, costPoint{ev.ObservedAt, ev.Index.CostUSD})
	if !ev.ObservedAt.Before(t.lastResult) { // the latest turn
		t.lastResult, t.lastKey, t.TurnCostUSD = ev.ObservedAt, key, turn
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
	if c.Reset {
		t.Resets++
	}
	if (c.Tokens > 0 || c.Reset) && !ev.ObservedAt.Before(t.contextAt) {
		t.contextAt, t.Context, t.LastInput = ev.ObservedAt, c.Tokens, c.Input
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
	// Oldest first, so a cold fold appends each result after the one before
	// it in time.
	slices.SortStableFunc(streams, func(x, y StreamInfo) int { return x.FirstAt.Compare(y.FirstAt) })
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
