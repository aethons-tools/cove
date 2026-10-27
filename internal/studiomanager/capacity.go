package studiomanager

import (
	"context"
	"fmt"
	"sync"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/launcher"
)

// gated is what a manager serves: the Colima launcher (embedded concretely so
// its optional interfaces stay visible to client.Run) gated by capacity.
type gated struct {
	*launcher.Launcher
	capacity int // 0 = unlimited
	mu       sync.Mutex
	inflight int
}

// Raise declines, without touching the launcher, when the studios it holds
// plus the raises in flight reach capacity; otherwise it raises. A studio
// whose container already exists while its raise is still in flight counts
// twice, which errs towards declining.
func (g *gated) Raise(ctx context.Context, spec jam.RaiseSpec, creds jam.LaunchCreds) (string, error) {
	if g.capacity > 0 {
		if err := g.admit(ctx); err != nil {
			return "", err
		}
		defer func() {
			g.mu.Lock()
			g.inflight--
			g.mu.Unlock()
		}()
	}
	return g.Launcher.Raise(ctx, spec, creds)
}

// admit counts one more raise in flight, or says why not.
func (g *gated) admit(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	studios, err := g.Studios(ctx)
	if err != nil {
		return fmt.Errorf("capacity check: %w", err)
	}
	if n := len(studios) + g.inflight; n >= g.capacity {
		return fmt.Errorf("at capacity (%d/%d studios)", n, g.capacity)
	}
	g.inflight++
	return nil
}
