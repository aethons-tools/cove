package agentrun

import (
	"sync"
	"time"
)

// idlePush holds the idle timeout Jam last pushed and the deadline in effect
// (usage/jam/open-episode.adoc): the episode loop arms the deadline when a turn
// ends (its end plus the timeout) and drops it when one starts; Jam's
// IdleDeadline push replaces it and signals the loop. Control's goroutine
// pushes, Run's goroutine arms and reads.
type idlePush struct {
	mu       sync.Mutex
	timeout  time.Duration // 0 = none
	deadline time.Time     // zero = none
	sig      chan struct{} // cap 1; signalled on every IdleDeadline push
}

func newIdlePush() *idlePush { return &idlePush{sig: make(chan struct{}, 1)} }

func (p *idlePush) setTimeout(d time.Duration) {
	p.mu.Lock()
	p.timeout = d
	p.mu.Unlock()
}

// setDeadline is Jam's IdleDeadline push: it replaces the deadline and
// signals.
func (p *idlePush) setDeadline(at time.Time) {
	p.arm(at)
	select {
	case p.sig <- struct{}{}:
	default:
	}
}

// arm sets the deadline without signalling (the episode loop's own change).
func (p *idlePush) arm(at time.Time) {
	p.mu.Lock()
	p.deadline = at
	p.mu.Unlock()
}

// armFrom arms the deadline at end plus the last pushed timeout; with no
// timeout it is dropped. It returns the deadline armed.
func (p *idlePush) armFrom(end time.Time) time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deadline = time.Time{}
	if p.timeout > 0 {
		p.deadline = end.Add(p.timeout)
	}
	return p.deadline
}

func (p *idlePush) signal() <-chan struct{} { return p.sig }

// get returns the latest timeout and deadline.
func (p *idlePush) get() (time.Duration, time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.timeout, p.deadline
}
