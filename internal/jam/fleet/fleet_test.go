package fleet

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/aethons-tools/cove/internal/jam"
)

// fakeManager is a Manager (and jam.StateKeeper) that records what reaches it.
// Shared by the router tests.
type fakeManager struct {
	name       string
	avail      atomic.Bool
	raiseErr   error
	raiseCalls atomic.Int32
	availCalls atomic.Int32
	owners     []string
	asm        string // ImageTag result

	notReadyOnce atomic.Bool  // the next Raise reports jam.ErrKitNotReady
	prepareState jam.KitState // what PrepareKit reports

	mu       sync.Mutex
	tornDown []string
	probed   []string
	purged   []string
	raised   []string
	prepared []string
	paused   []string
}

func newFake(name string) *fakeManager {
	f := &fakeManager{name: name}
	f.avail.Store(true)
	return f
}

func (f *fakeManager) Name() string { return f.name }
func (f *fakeManager) Available() bool {
	f.availCalls.Add(1)
	return f.avail.Load()
}

func (f *fakeManager) rec(dst *[]string, s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	*dst = append(*dst, s)
}

func (f *fakeManager) get(src *[]string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(*src)
}

func (f *fakeManager) Raise(_ context.Context, spec jam.RaiseSpec, _ jam.LaunchCreds) (string, error) {
	f.raiseCalls.Add(1)
	if f.raiseErr != nil {
		return "", f.raiseErr
	}
	if f.notReadyOnce.CompareAndSwap(true, false) {
		return "", jam.ErrKitNotReady
	}
	f.rec(&f.raised, spec.Role)
	return f.name + "-loc", nil
}

func (f *fakeManager) Teardown(_ context.Context, inst jam.Instance) error {
	f.rec(&f.tornDown, inst.ActorID)
	return nil
}

func (f *fakeManager) Probe(_ context.Context, inst jam.Instance) (jam.Liveness, error) {
	f.rec(&f.probed, inst.ActorID)
	return jam.LivenessAlive, nil
}

func (f *fakeManager) Pause(_ context.Context, inst jam.Instance) error {
	f.rec(&f.paused, inst.ActorID)
	return nil
}
func (f *fakeManager) Unpause(context.Context, jam.Instance) error { return nil }
func (f *fakeManager) ApplyEgress(context.Context, jam.Instance, *jam.EgressPolicy) error {
	return nil
}

func (f *fakeManager) PrepareKit(_ context.Context, def jam.KitDefinition) (jam.KitStatus, error) {
	f.rec(&f.prepared, f.name)
	return jam.KitStatus{State: f.prepareState}, nil
}

func (f *fakeManager) PurgeState(_ context.Context, id string) error {
	f.rec(&f.purged, id)
	return nil
}

func (f *fakeManager) StateOwners(context.Context) ([]string, error) {
	return slices.Clone(f.owners), nil
}

func (f *fakeManager) ImageTag(jam.KitRef) string { return f.asm }
