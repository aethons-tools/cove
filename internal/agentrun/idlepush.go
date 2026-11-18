package agentrun

import (
	"sync"
	"time"
)

// idlePush holds the latest IdleTimeout and IdleDeadline Jam pushed; each push
// replaces that message's last one. Nothing reads them yet: the open episode
// (usage/jam/open-episode.adoc) will. Control's goroutine writes, any reads.
type idlePush struct {
	mu       sync.Mutex
	timeout  time.Duration // 0 = none
	deadline time.Time     // zero = none
}

func (p *idlePush) setTimeout(d time.Duration) {
	p.mu.Lock()
	p.timeout = d
	p.mu.Unlock()
}

func (p *idlePush) setDeadline(at time.Time) {
	p.mu.Lock()
	p.deadline = at
	p.mu.Unlock()
}

// get returns the latest timeout and deadline.
func (p *idlePush) get() (time.Duration, time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.timeout, p.deadline
}
