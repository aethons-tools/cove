package studiomanager

import (
	"context"
	"fmt"
	"sync"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/launcher"
	"github.com/aethons-tools/cove/internal/jam/managers/wire"
)

// client.Run discovers these by type assertion; losing one would silently
// make Jam's requests for it fail as unsupported.
var (
	_ jam.Launcher          = (*gated)(nil)
	_ jam.StateKeeper       = (*gated)(nil)
	_ jam.ImageTagger       = (*gated)(nil)
	_ jam.KitDraftBuilder   = (*gated)(nil)
	_ wire.TeardownCapturer = (*gated)(nil)
)

// gated is what a manager serves: the Colima launcher (embedded concretely so
// its optional interfaces stay visible to client.Run) gated by capacity.
type gated struct {
	*launcher.Launcher
	capacity int // 0 = unlimited
	mu       sync.Mutex
	inflight map[string]int // actor id -> raises in flight
}

// Raise declines, without touching the launcher, when the studios it holds
// plus the raises in flight reach capacity; otherwise it raises. A raise past
// RunEphemeral is already listed, so an in-flight actor that is among the
// studios counts once.
func (g *gated) Raise(ctx context.Context, spec jam.RaiseSpec, creds jam.LaunchCreds) (string, error) {
	if g.capacity > 0 {
		if err := g.admit(ctx, spec.ActorID); err != nil {
			return "", err
		}
		defer g.done(spec.ActorID)
	}
	return g.Launcher.Raise(ctx, spec, creds)
}

// admit counts one more raise in flight for actor, or says why not.
func (g *gated) admit(ctx context.Context, actor string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	studios, err := g.Studios(ctx)
	if err != nil {
		return fmt.Errorf("capacity check: %w", err)
	}
	listed := make(map[string]bool, len(studios))
	for _, s := range studios {
		listed[s.ActorID] = true
	}
	n := len(studios)
	for a := range g.inflight {
		if !listed[a] {
			n++
		}
	}
	if n >= g.capacity {
		return fmt.Errorf("at capacity (%d/%d studios)", n, g.capacity)
	}
	if g.inflight == nil {
		g.inflight = map[string]int{}
	}
	g.inflight[actor]++
	return nil
}

// done ends one of actor's raises in flight.
func (g *gated) done(actor string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inflight[actor]--; g.inflight[actor] <= 0 {
		delete(g.inflight, actor)
	}
}
