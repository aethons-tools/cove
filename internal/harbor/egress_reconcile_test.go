package harbor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestEgressFingerprint(t *testing.T) {
	for _, tc := range []struct {
		p    *EgressPolicy
		want string
	}{
		{nil, "kit"},
		{&EgressPolicy{Domains: []string{}}, "none"},
		{&EgressPolicy{}, "none"},
		{&EgressPolicy{Domains: []string{".b.org", "a.com"}}, "d:.b.org,a.com"},
	} {
		if got := EgressFingerprint(tc.p); got != tc.want {
			t.Errorf("EgressFingerprint(%+v) = %q, want %q", tc.p, got, tc.want)
		}
	}
}

// egressKit is a supervisor over a store holding the kit-default "guest" role and
// a policed "fenced" role, with its log captured.
func egressKit(t *testing.T) (*Supervisor, Store, *fakeLauncher, *bytes.Buffer) {
	t.Helper()
	f := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, f)
	var logs bytes.Buffer
	sup.log = slog.New(slog.NewJSONHandler(&logs, nil))
	setRoleEgress(t, store, "fenced", &EgressPolicy{Domains: []string{".b.org", "a.com"}})
	return sup, store, f, &logs
}

func setRoleEgress(t *testing.T, store Store, role string, p *EgressPolicy) {
	t.Helper()
	if err := store.PutRole("default", Role{Name: role, Scope: Scope{Destinations: []string{"anthropic"}, TTL: time.Hour, Egress: p}}); err != nil {
		t.Fatal(err)
	}
}

func raiseOrFail(t *testing.T, sup *Supervisor, id, role string) {
	t.Helper()
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: id, Role: role}); err != nil {
		t.Fatal(err)
	}
}

func reconcileOrFail(t *testing.T, sup *Supervisor) {
	t.Helper()
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// Raise records the fingerprint of the policy it raised under.
func TestRaiseRecordsEgressFingerprint(t *testing.T) {
	sup, store, _, _ := egressKit(t)
	raiseOrFail(t, sup, "k1", "guest")
	raiseOrFail(t, sup, "f1", "fenced")
	if got, _ := store.GetInstance("k1"); got.Egress != "kit" {
		t.Fatalf("kit-default cove egress = %q, want kit", got.Egress)
	}
	if got, _ := store.GetInstance("f1"); got.Egress != "d:.b.org,a.com" {
		t.Fatalf("policed cove egress = %q", got.Egress)
	}
}

// No drift: nothing applied.
func TestReconcileEgressNoDrift(t *testing.T) {
	sup, _, f, _ := egressKit(t)
	raiseOrFail(t, sup, "k1", "guest")
	raiseOrFail(t, sup, "f1", "fenced")
	reconcileOrFail(t, sup)
	if len(f.egressed) != 0 {
		t.Fatalf("no drift must apply nothing; applied %d", len(f.egressed))
	}
}

// A policy set on a kit-default cove's role is applied and recorded; the log
// carries the count, never the list.
func TestReconcileEgressAppliesSetPolicy(t *testing.T) {
	sup, store, f, logs := egressKit(t)
	raiseOrFail(t, sup, "k1", "guest")
	setRoleEgress(t, store, "guest", &EgressPolicy{Domains: []string{"pkg.go.dev", "x.org"}})
	reconcileOrFail(t, sup)
	if len(f.egressed) != 1 || f.egressed[0] == nil || strings.Join(f.egressed[0].Domains, ",") != "pkg.go.dev,x.org" {
		t.Fatalf("applied %+v, want the role's policy once", f.egressed)
	}
	if got, _ := store.GetInstance("k1"); got.Egress != "d:pkg.go.dev,x.org" || got.EgressFailures != 0 {
		t.Fatalf("recorded egress = %q failures %d", got.Egress, got.EgressFailures)
	}
	out := logs.String()
	if !strings.Contains(out, `"domains":2`) || strings.Contains(out, "pkg.go.dev") {
		t.Fatalf("log must carry the count, never the list: %s", out)
	}
	reconcileOrFail(t, sup)
	if len(f.egressed) != 1 {
		t.Fatalf("a recorded policy must not be re-applied; applied %d", len(f.egressed))
	}
}

// Clearing a policed role's policy resets the cove to the kit default.
func TestReconcileEgressClearedResetsToKit(t *testing.T) {
	sup, store, f, _ := egressKit(t)
	raiseOrFail(t, sup, "f1", "fenced")
	setRoleEgress(t, store, "fenced", nil)
	reconcileOrFail(t, sup)
	if len(f.egressed) != 1 || f.egressed[0] != nil {
		t.Fatalf("applied %+v, want one ApplyEgress(nil)", f.egressed)
	}
	if got, _ := store.GetInstance("f1"); got.Egress != "kit" {
		t.Fatalf("recorded egress = %q, want kit", got.Egress)
	}
}

// A pre-feature instance (Egress "") is applied once, then left alone.
func TestReconcileEgressLegacyAppliedOnce(t *testing.T) {
	sup, store, f, _ := egressKit(t)
	raiseOrFail(t, sup, "k1", "guest")
	inst, _ := store.GetInstance("k1")
	inst.Egress = ""
	if err := store.PutInstance(inst); err != nil {
		t.Fatal(err)
	}
	reconcileOrFail(t, sup)
	reconcileOrFail(t, sup)
	if len(f.egressed) != 1 || f.egressed[0] != nil {
		t.Fatalf("applied %+v, want exactly one ApplyEgress(nil)", f.egressed)
	}
	if got, _ := store.GetInstance("k1"); got.Egress != "kit" {
		t.Fatalf("recorded egress = %q, want kit", got.Egress)
	}
}

// A missing role means the kit default.
func TestReconcileEgressMissingRoleIsKit(t *testing.T) {
	sup, store, f, _ := egressKit(t)
	if err := store.PutInstance(Instance{ActorID: "o1", Project: "default", Role: "nope", Phase: PhaseLive, Egress: "none",
		Lease: Lease{Holder: "holder-A", Expiry: time.Unix(2000, 0).UTC()}}); err != nil {
		t.Fatal(err)
	}
	reconcileOrFail(t, sup)
	if len(f.egressed) != 1 || f.egressed[0] != nil {
		t.Fatalf("applied %+v, want ApplyEgress(nil)", f.egressed)
	}
	if got, _ := store.GetInstance("o1"); got.Egress != "kit" {
		t.Fatalf("recorded egress = %q, want kit", got.Egress)
	}
}

// Failures count 1, 2; a success in between resets; the third consecutive
// failure tears the cove down.
func TestReconcileEgressFailuresThenTeardown(t *testing.T) {
	sup, store, f, logs := egressKit(t)
	raiseOrFail(t, sup, "k1", "guest")
	setRoleEgress(t, store, "guest", &EgressPolicy{Domains: []string{"x.org"}})
	f.egressErr = errors.New("exec failed")

	failures := func() int {
		t.Helper()
		got, ok := store.GetInstance("k1")
		if !ok {
			t.Fatal("instance gone")
		}
		return got.EgressFailures
	}
	reconcileOrFail(t, sup)
	if n := failures(); n != 1 {
		t.Fatalf("failures = %d, want 1", n)
	}
	reconcileOrFail(t, sup)
	if n := failures(); n != 2 {
		t.Fatalf("failures = %d, want 2", n)
	}
	if !strings.Contains(logs.String(), `"failures":2`) {
		t.Fatalf("warning must carry the failure count: %s", logs.String())
	}
	// A success resets the count (then the role changes again).
	f.egressErr = nil
	reconcileOrFail(t, sup)
	if n := failures(); n != 0 {
		t.Fatalf("failures after success = %d, want 0", n)
	}
	setRoleEgress(t, store, "guest", &EgressPolicy{Domains: []string{"y.org"}})
	f.egressErr = errors.New("exec failed")
	reconcileOrFail(t, sup)
	reconcileOrFail(t, sup)
	if n := failures(); n != 2 {
		t.Fatalf("failures = %d, want 2", n)
	}
	if len(f.tornDown) != 0 {
		t.Fatal("torn down before the third consecutive failure")
	}
	reconcileOrFail(t, sup)
	if _, ok := store.GetInstance("k1"); ok {
		t.Fatal("third consecutive failure must tear the cove down")
	}
	if len(f.tornDown) != 1 {
		t.Fatalf("teardown calls = %d, want 1", len(f.tornDown))
	}
	if !strings.Contains(logs.String(), "torn down: egress re-apply failed") {
		t.Fatalf("teardown must be logged: %s", logs.String())
	}
	if strings.Contains(logs.String(), "y.org") {
		t.Fatalf("log must not carry the domain list: %s", logs.String())
	}
}

// Someone else's unexpired lease: not ours to re-apply.
func TestReconcileEgressSkipsOthersLease(t *testing.T) {
	sup, store, f, _ := egressKit(t)
	if err := store.PutInstance(Instance{ActorID: "o1", Project: "default", Role: "fenced", Phase: PhaseLive, Egress: "kit",
		Lease: Lease{Holder: "holder-B", Expiry: time.Unix(2000, 0).UTC()}}); err != nil {
		t.Fatal(err)
	}
	reconcileOrFail(t, sup)
	if len(f.egressed) != 0 {
		t.Fatalf("applied under someone else's lease: %+v", f.egressed)
	}
}

// Reconcile skips an Idled cove (it can't be exec'd into while paused).
func TestReconcileEgressSkipsIdled(t *testing.T) {
	sup, store, f, _ := egressKit(t)
	raiseOrFail(t, sup, "k1", "guest")
	if err := sup.Idle(context.Background(), "k1"); err != nil {
		t.Fatal(err)
	}
	setRoleEgress(t, store, "guest", &EgressPolicy{Domains: []string{"x.org"}})
	reconcileOrFail(t, sup)
	if len(f.egressed) != 0 {
		t.Fatalf("applied to a paused cove: %+v", f.egressed)
	}
}

// Resume applies drift after unpausing and before the cove is Live again.
func TestResumeAppliesEgressDrift(t *testing.T) {
	sup, store, f, _ := egressKit(t)
	raiseOrFail(t, sup, "k1", "guest")
	if err := sup.Idle(context.Background(), "k1"); err != nil {
		t.Fatal(err)
	}
	setRoleEgress(t, store, "guest", &EgressPolicy{Domains: []string{"x.org"}})
	if err := sup.Resume(context.Background(), "k1"); err != nil {
		t.Fatal(err)
	}
	if len(f.resumed) != 1 || len(f.egressed) != 1 || f.egressed[0] == nil {
		t.Fatalf("resumed %v applied %+v; want unpause then the role's policy", f.resumed, f.egressed)
	}
	got, _ := store.GetInstance("k1")
	if got.Phase != PhaseLive || got.Egress != "d:x.org" {
		t.Fatalf("phase %s egress %q; want live under d:x.org", got.Phase, got.Egress)
	}
}

// Resume without drift applies nothing.
func TestResumeNoDriftAppliesNothing(t *testing.T) {
	sup, _, f, _ := egressKit(t)
	raiseOrFail(t, sup, "k1", "guest")
	if err := sup.Idle(context.Background(), "k1"); err != nil {
		t.Fatal(err)
	}
	if err := sup.Resume(context.Background(), "k1"); err != nil {
		t.Fatal(err)
	}
	if len(f.egressed) != 0 {
		t.Fatalf("applied %+v without drift", f.egressed)
	}
}

// A resume whose re-apply fails tears the cove down at once and errors: the
// agent is never woken under a stale policy.
func TestResumeEgressFailureTearsDown(t *testing.T) {
	sup, store, f, logs := egressKit(t)
	raiseOrFail(t, sup, "k1", "guest")
	if err := sup.Idle(context.Background(), "k1"); err != nil {
		t.Fatal(err)
	}
	setRoleEgress(t, store, "guest", &EgressPolicy{Domains: []string{"x.org"}})
	f.egressErr = errors.New("exec failed")
	err := sup.Resume(context.Background(), "k1")
	if err == nil || !strings.Contains(err.Error(), "exec failed") {
		t.Fatalf("err = %v; want the apply failure", err)
	}
	if _, ok := store.GetInstance("k1"); ok {
		t.Fatal("a failed resume re-apply must tear the cove down")
	}
	if len(f.tornDown) != 1 {
		t.Fatalf("teardown calls = %d, want 1", len(f.tornDown))
	}
	if !strings.Contains(logs.String(), "torn down: egress re-apply failed") {
		t.Fatalf("teardown must be logged: %s", logs.String())
	}
}
