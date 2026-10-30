package jam

import (
	"fmt"
	"sync"
)

// InstanceChanges is a Store whose instance writes signal subscribers when
// they change what a session's status card shows (docs/superpowers/specs/
// 2026-10-30-session-status-card-design.md): lifecycle, mission, open asks,
// alarms, exposures, kept processes. Heartbeats and cursor writes don't
// signal. Every other method passes through to the wrapped Store. Each
// subscriber's channel holds one pending signal, so a burst coalesces and a
// slow subscriber never blocks a write.
type InstanceChanges struct {
	Store
	mu   sync.Mutex
	last map[string]string // actor → its card fingerprint at the last write
	subs map[chan struct{}]struct{}
}

// NewInstanceChanges wraps st.
func NewInstanceChanges(st Store) *InstanceChanges {
	return &InstanceChanges{Store: st, last: map[string]string{}, subs: map[chan struct{}]struct{}{}}
}

// cardFingerprint is the part of i a status card shows.
func cardFingerprint(i Instance) string {
	return fmt.Sprintf("%s|%s|%s|%t|%d|%q|%d|%d|%d", i.Phase, i.Activity, i.Name, NeedsPerson(i), i.OpenAsks, i.Mission,
		len(i.Alarms), len(i.Exposures), i.KeptProcesses)
}

func (c *InstanceChanges) PutInstance(i Instance) error {
	if err := c.Store.PutInstance(i); err != nil {
		return err
	}
	c.note(i.ActorID, cardFingerprint(i))
	return nil
}

func (c *InstanceChanges) RemoveInstance(actorID string) error {
	if err := c.Store.RemoveInstance(actorID); err != nil {
		return err
	}
	c.note(actorID, "")
	return nil
}

// note records actor's fingerprint fp ("" = removed), signalling if it changed.
func (c *InstanceChanges) note(actor, fp string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.last[actor]; ok && old == fp {
		return
	}
	if fp == "" {
		delete(c.last, actor)
	} else {
		c.last[actor] = fp
	}
	for ch := range c.subs {
		select {
		case ch <- struct{}{}:
		default: // already pending: coalesce
		}
	}
}

// Subscribe returns a channel signalled after a card-visible instance change,
// and an idempotent cancel that unsubscribes it.
func (c *InstanceChanges) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	c.mu.Lock()
	c.subs[ch] = struct{}{}
	c.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			c.mu.Lock()
			delete(c.subs, ch)
			c.mu.Unlock()
		})
	}
}
