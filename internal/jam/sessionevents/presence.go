package sessionevents

import (
	"encoding/json"
	"sync"
	"time"
)

// Session statuses derived from the event stream. They summarize what a
// session is doing without any of an event's content: the tool name is the
// only detail taken from an event.
const (
	StatusThinking = "thinking"
	StatusRunning  = "running" // a tool; Status.Tool names it
	StatusWriting  = "writing"
	StatusIdle     = "idle" // the turn finished
)

// Status is a session's latest derived status.
type Status struct {
	State string
	Tool  string    // StatusRunning only
	At    time.Time // when this status was first observed
}

// DeriveStatus maps one event to the status it puts its session in, or
// ok=false when it doesn't change what the session is doing (a gap, a hook or
// progress event, or an unparseable line).
func DeriveStatus(ev Event) (Status, bool) {
	if ev.Kind != KindEvent {
		return Status{}, false
	}
	switch ev.Index.Type {
	case "result":
		return Status{State: StatusIdle}, true
	case "user":
		return Status{State: StatusThinking}, true
	case "system":
		if ev.Index.Subtype == "init" {
			return Status{State: StatusThinking}, true
		}
		return Status{}, false
	case "assistant":
		if ev.Index.ToolName != "" {
			return Status{State: StatusRunning, Tool: ev.Index.ToolName}, true
		}
		var env struct {
			Message struct {
				Content []struct {
					Type string `json:"type"`
				} `json:"content"`
			} `json:"message"`
		}
		_ = json.Unmarshal(ev.Raw, &env)
		for _, b := range env.Message.Content {
			if b.Type == "text" {
				return Status{State: StatusWriting}, true
			}
		}
		return Status{State: StatusThinking}, true
	}
	return Status{}, false
}

// Presence keeps each actor's latest derived status, and how many system/init
// events it has seen (a status card's sparkline marks them), in memory, and
// signals subscribers when either changes. Feed it every published event via
// Hub.Observe. After a restart it knows nothing until each session's next
// status-bearing event. Each subscriber's channel holds one pending signal, so
// a burst of changes coalesces and a slow subscriber never blocks ingest.
type Presence struct {
	now   func() time.Time
	mu    sync.Mutex
	last  map[string]Status
	inits map[string]int
	subs  map[chan struct{}]struct{}
}

// NewPresence returns an empty Presence; now defaults to time.Now.
func NewPresence(now func() time.Time) *Presence {
	if now == nil {
		now = time.Now
	}
	return &Presence{now: now, last: map[string]Status{}, inits: map[string]int{}, subs: map[chan struct{}]struct{}{}}
}

// Observe records ev's status for its actor (and counts it if it is an init),
// signalling on a change or an init.
func (p *Presence) Observe(ev Event) {
	s, ok := DeriveStatus(ev)
	if !ok {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	isInit := ev.Index.Type == "system" && ev.Index.Subtype == "init"
	if isInit {
		p.inits[ev.ActorID]++
	}
	if old, ok := p.last[ev.ActorID]; ok && old.State == s.State && old.Tool == s.Tool {
		if isInit {
			p.signal()
		}
		return
	}
	s.At = p.now()
	p.last[ev.ActorID] = s
	p.signal()
}

// signal wakes every subscriber; p.mu is held.
func (p *Presence) signal() {
	for ch := range p.subs {
		select {
		case ch <- struct{}{}:
		default: // already pending: coalesce
		}
	}
}

// Status returns actorID's latest status, or ok=false if none is known.
func (p *Presence) Status(actorID string) (Status, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.last[actorID]
	return s, ok
}

// Inits is how many system/init events actorID has had since this Presence
// started.
func (p *Presence) Inits(actorID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inits[actorID]
}

// Subscribe returns a channel signalled after a status change or an init, and an
// idempotent cancel that unsubscribes it.
func (p *Presence) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	p.mu.Lock()
	p.subs[ch] = struct{}{}
	p.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			p.mu.Lock()
			delete(p.subs, ch)
			p.mu.Unlock()
		})
	}
}
