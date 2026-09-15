package dispatcher

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/dispatch/scheduler"
	"github.com/aethons-tools/cove/internal/harbor"
)

type fakeTracker struct {
	ready       []scheduler.Issue
	comments    []scheduler.Comment
	transitions []struct {
		id   string
		role scheduler.Role
	}
	transitionErr error
}

func (f *fakeTracker) ListReady(ctx context.Context) ([]scheduler.Issue, error) {
	return f.ready, nil
}
func (f *fakeTracker) Comments(ctx context.Context, id string) ([]scheduler.Comment, error) {
	return f.comments, nil
}
func (f *fakeTracker) Transition(ctx context.Context, id string, role scheduler.Role) error {
	f.transitions = append(f.transitions, struct {
		id   string
		role scheduler.Role
	}{id, role})
	return f.transitionErr
}

type fakeRaiser struct {
	specs []harbor.RaiseSpec
	err   error
}

func (f *fakeRaiser) Raise(ctx context.Context, spec harbor.RaiseSpec) (harbor.Instance, string, string, error) {
	f.specs = append(f.specs, spec)
	if f.err != nil {
		return harbor.Instance{}, "", "", f.err
	}
	return harbor.Instance{ActorID: spec.ActorID}, "tok", "sec", nil
}

type fakeRegistry struct{ insts []harbor.Instance }

func (f *fakeRegistry) GetInstance(actorID string) (harbor.Instance, bool) {
	for _, i := range f.insts {
		if i.ActorID == actorID {
			return i, true
		}
	}
	return harbor.Instance{}, false
}
func (f *fakeRegistry) ListInstances() []harbor.Instance { return f.insts }

func newTestDispatcher(t *fakeTracker, r *fakeRaiser, reg *fakeRegistry, max int) *Dispatcher {
	return New(t, r, reg, Config{Role: "worker", Project: "acme", MaxConcurrent: max}, nil)
}

func TestTickClaimsAndRaises(t *testing.T) {
	tr := &fakeTracker{ready: []scheduler.Issue{{ID: "id1", Identifier: "AET-1", Title: "do a thing", Description: "the desc", DispatchLabeled: true}}}
	r := &fakeRaiser{}
	d := newTestDispatcher(tr, r, &fakeRegistry{}, 5)
	d.tick(context.Background())

	if len(tr.transitions) != 1 || tr.transitions[0].id != "id1" || tr.transitions[0].role != scheduler.RoleInProgress {
		t.Fatalf("want one InProgress transition on id1, got %+v", tr.transitions)
	}
	if len(r.specs) != 1 {
		t.Fatalf("want 1 raise, got %d", len(r.specs))
	}
	s := r.specs[0]
	if s.ActorID != "cove-AET-1" || s.Role != "worker" || s.Project != "acme" || s.Unit != "AET-1" {
		t.Fatalf("raise spec = %+v", s)
	}
	if !strings.Contains(s.Prompt, "do a thing") || !strings.Contains(s.Prompt, "worker-result.json") {
		t.Fatalf("prompt missing brief or result-protocol:\n%s", s.Prompt)
	}
}

func TestTickSkipsUnlabeledIssues(t *testing.T) {
	// An issue with no dispatch label must never be claimed or raised.
	tr := &fakeTracker{ready: []scheduler.Issue{{ID: "id1", Identifier: "AET-1", DispatchLabeled: false}}}
	r := &fakeRaiser{}
	newTestDispatcher(tr, r, &fakeRegistry{}, 5).tick(context.Background())
	if len(tr.transitions) != 0 || len(r.specs) != 0 {
		t.Fatalf("unlabeled issue must be skipped: transitions=%v raises=%v", tr.transitions, r.specs)
	}
}

func TestTickRaisesOnlyLabeledAmongMixed(t *testing.T) {
	// Mixed batch: only the dispatch-labeled issue is raised.
	tr := &fakeTracker{ready: []scheduler.Issue{
		{ID: "id1", Identifier: "AET-1", DispatchLabeled: false},
		{ID: "id2", Identifier: "AET-2", DispatchLabeled: true},
	}}
	r := &fakeRaiser{}
	newTestDispatcher(tr, r, &fakeRegistry{}, 5).tick(context.Background())
	if len(r.specs) != 1 || r.specs[0].Unit != "AET-2" {
		t.Fatalf("want only AET-2 raised, got %+v", r.specs)
	}
}

func TestTickDedupsExistingInstance(t *testing.T) {
	tr := &fakeTracker{ready: []scheduler.Issue{{ID: "id1", Identifier: "AET-1", DispatchLabeled: true}}}
	r := &fakeRaiser{}
	reg := &fakeRegistry{insts: []harbor.Instance{{ActorID: "cove-AET-1", Phase: harbor.PhaseLive}}}
	newTestDispatcher(tr, r, reg, 5).tick(context.Background())
	if len(tr.transitions) != 0 || len(r.specs) != 0 {
		t.Fatalf("existing instance must be skipped: transitions=%v raises=%v", tr.transitions, r.specs)
	}
}

func TestTickRespectsCap(t *testing.T) {
	tr := &fakeTracker{ready: []scheduler.Issue{
		{ID: "id1", Identifier: "AET-1", DispatchLabeled: true}, {ID: "id2", Identifier: "AET-2", DispatchLabeled: true}, {ID: "id3", Identifier: "AET-3", DispatchLabeled: true},
	}}
	r := &fakeRaiser{}
	// 1 already live + cap 2 ⇒ exactly 1 new raise allowed this tick.
	reg := &fakeRegistry{insts: []harbor.Instance{{ActorID: "cove-OTHER", Phase: harbor.PhaseLive}}}
	newTestDispatcher(tr, r, reg, 2).tick(context.Background())
	if len(r.specs) != 1 {
		t.Fatalf("cap: want 1 raise, got %d", len(r.specs))
	}
}

func TestTickCapCountExcludesGone(t *testing.T) {
	tr := &fakeTracker{ready: []scheduler.Issue{{ID: "id1", Identifier: "AET-1", DispatchLabeled: true}}}
	r := &fakeRaiser{}
	// A gone instance must NOT count toward the cap.
	reg := &fakeRegistry{insts: []harbor.Instance{{ActorID: "cove-OLD", Phase: harbor.PhaseGone}}}
	newTestDispatcher(tr, r, reg, 1).tick(context.Background())
	if len(r.specs) != 1 {
		t.Fatalf("gone instance should not consume a slot; want 1 raise, got %d", len(r.specs))
	}
}

func TestTickRaiseFailureMovesToNeedsInput(t *testing.T) {
	tr := &fakeTracker{ready: []scheduler.Issue{{ID: "id1", Identifier: "AET-1", DispatchLabeled: true}}}
	r := &fakeRaiser{err: errors.New("launch boom")}
	newTestDispatcher(tr, r, &fakeRegistry{}, 5).tick(context.Background())
	// InProgress (claim) then NeedsInput (failure).
	if len(tr.transitions) != 2 ||
		tr.transitions[0].role != scheduler.RoleInProgress ||
		tr.transitions[1].role != scheduler.RoleNeedsInput {
		t.Fatalf("want [InProgress, NeedsInput], got %+v", tr.transitions)
	}
}

func TestTickClaimFailureSkipsRaise(t *testing.T) {
	tr := &fakeTracker{ready: []scheduler.Issue{{ID: "id1", Identifier: "AET-1", DispatchLabeled: true}}, transitionErr: errors.New("claim boom")}
	r := &fakeRaiser{}
	newTestDispatcher(tr, r, &fakeRegistry{}, 5).tick(context.Background())
	if len(r.specs) != 0 {
		t.Fatalf("claim failure must skip raise, got %d raises", len(r.specs))
	}
}
