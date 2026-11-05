package studiomanager

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

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
// its optional interfaces stay visible to client.Run) gated by capacity and
// by a full disk.
type gated struct {
	*launcher.Launcher
	capacity int                // 0 = unlimited
	disk     func() (int, bool) // nil: never full; else the disk percent and whether raises are declined (diskPolicy.full)
	mu       sync.Mutex
	inflight map[string]int // actor id -> raises in flight
	kitWork  atomic.Int32   // raises, kit prepares and draft builds in flight
}

// Raise declines, without touching the launcher, when the disk is full or
// the studios it holds plus the raises in flight reach capacity; otherwise
// it raises. A raise past RunEphemeral is already listed, so an in-flight
// actor that is among the studios counts once.
func (g *gated) Raise(ctx context.Context, spec jam.RaiseSpec, creds jam.LaunchCreds) (string, error) {
	if g.disk != nil {
		if n, full := g.disk(); full {
			return "", fmt.Errorf("disk full (%d%%)", n)
		}
	}
	g.kitWork.Add(1)
	defer g.kitWork.Add(-1)
	if g.capacity > 0 {
		if err := g.admit(ctx, spec.ActorID); err != nil {
			return "", err
		}
		defer g.done(spec.ActorID)
	}
	return g.Launcher.Raise(ctx, spec, creds)
}

// PrepareKit prepares on the launcher, counted as kit work.
func (g *gated) PrepareKit(ctx context.Context, def jam.KitDefinition) (jam.KitStatus, error) {
	g.kitWork.Add(1)
	defer g.kitWork.Add(-1)
	return g.Launcher.PrepareKit(ctx, def)
}

// BuildKitDraft builds on the launcher, counted as kit work.
func (g *gated) BuildKitDraft(ctx context.Context, def jam.KitDefinition, tag string, log io.Writer) (string, error) {
	g.kitWork.Add(1)
	defer g.kitWork.Add(-1)
	return g.Launcher.BuildKitDraft(ctx, def, tag, log)
}

// kitBusy reports kit work in flight: the disk policy then keeps kit images.
func (g *gated) kitBusy() bool { return g.kitWork.Load() > 0 }

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
