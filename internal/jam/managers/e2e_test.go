package managers

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/managers/client"
)

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// e2eEnv is a real Supervisor driving the Router, whose only manager is the
// remote m1, served by the hub; the Supervisor is also the hub's Reconciler.
type e2eEnv struct {
	*hubEnv
	sup   *jam.Supervisor
	clock *time.Time
	token string
}

func newE2E(t *testing.T, log *slog.Logger) *e2eEnv {
	t.Helper()
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	clk := time.Unix(1000, 0).UTC()
	env := &e2eEnv{clock: &clk}
	env.hubEnv = newHubEnv(t, func(c *Config) {
		c.Log = log
		if err := c.Store.PutRole("default", jam.Role{Name: "guest", Scope: jam.Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
			t.Fatal(err)
		}
		env.sup = jam.NewSupervisor(c.Store, c.Router, "holder-A", time.Minute, 30*time.Second,
			func() time.Time { return *env.clock }, log)
		c.Reconciler = env.sup
	}, nil)
	env.token = env.add(t, "m1")
	return env
}

// connect runs a client for m1 with l and waits until it is available.
func (e *e2eEnv) connect(t *testing.T, l *testLauncher, inv []jam.ManagedStudio) (errc chan error, stop func()) {
	t.Helper()
	errc, stop = runClient(t, e.cc, client.Config{
		Name: "m1", Token: e.token, Capacity: 2, Launcher: l,
		Inventory: func(context.Context) ([]jam.ManagedStudio, error) { return inv, nil },
	})
	eventually(t, "m1 connected", func() bool { return e.available("m1") })
	return errc, stop
}

func TestE2ERaiseTeardownViaSupervisor(t *testing.T) {
	e := newE2E(t, nil)
	l := &testLauncher{loc: "box-7"}
	_, stop := e.connect(t, l, nil)

	inst, _, _, err := e.sup.Raise(context.Background(), jam.RaiseSpec{ActorID: "w1", Role: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Manager != "m1" || inst.Location != "box-7" {
		t.Fatalf("instance = manager %q location %q", inst.Manager, inst.Location)
	}
	if err := e.sup.Teardown(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
	if got := l.gotTorn(); len(got) != 1 || got[0] != "w1" {
		t.Fatalf("torn = %v", got)
	}

	if _, _, _, err := e.sup.Raise(context.Background(), jam.RaiseSpec{ActorID: "w2", Role: "guest"}); err != nil {
		t.Fatal(err)
	}
	stop()
	eventually(t, "m1 disconnected", func() bool { return !e.available("m1") })
	*e.clock = e.clock.Add(10 * time.Minute) // w2's lease has expired
	if err := e.sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, ok := e.store.GetInstance("w2")
	if !ok {
		t.Fatal("instance on a disconnected manager was removed")
	}
	if got.Phase != jam.PhaseLive {
		t.Fatalf("phase = %s, want live (Unknown is never reaped)", got.Phase)
	}
}

func TestE2ESecretsNotLogged(t *testing.T) {
	buf := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := newE2E(t, log)
	l := &testLauncher{loc: "box-1"}
	e.connect(t, l, nil)

	if _, _, _, err := e.sup.Raise(context.Background(), jam.RaiseSpec{ActorID: "w1", Role: "guest"}); err != nil {
		t.Fatal(err)
	}
	creds := l.gotCreds()
	if len(creds) != 1 || creds[0].LaunchSecret == "" || creds[0].IdentityToken == "" {
		t.Fatalf("creds received = %d (secret set %v)", len(creds), len(creds) == 1 && creds[0].LaunchSecret != "")
	}
	out := buf.String()
	if out == "" {
		t.Fatal("nothing logged: the check would be vacuous")
	}
	if strings.Contains(out, creds[0].LaunchSecret) || strings.Contains(out, creds[0].IdentityToken) {
		t.Fatal("a launch secret or identity token reached the log")
	}
	if strings.Contains(out, e.token) {
		t.Fatal("the manager token reached the log")
	}
}

func TestE2EDraftBuildLogStreams(t *testing.T) {
	e := newE2E(t, nil)
	e.connect(t, &testLauncher{loc: "box-1"}, nil)
	var w syncBuffer
	id, err := e.router.BuildKitDraft(context.Background(), jam.KitDefinition{Ref: jam.KitRef{ID: "k"}}, "draft-tag", &w)
	if err != nil || id != "img-draft" {
		t.Fatalf("BuildKitDraft = %q, %v", id, err)
	}
	if got := w.String(); got != "step 1\nstep 2\n" {
		t.Fatalf("log = %q", got)
	}
}

func TestE2EReconnectReconciles(t *testing.T) {
	e := newE2E(t, nil)
	_, stop := e.connect(t, &testLauncher{loc: "box-1"}, nil)
	if _, _, _, err := e.sup.Raise(context.Background(), jam.RaiseSpec{ActorID: "w1", Role: "guest"}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := e.sup.Raise(context.Background(), jam.RaiseSpec{ActorID: "w2", Role: "guest"}); err != nil {
		t.Fatal(err)
	}
	stop()
	eventually(t, "m1 disconnected", func() bool { return !e.available("m1") })

	// The manager comes back holding only w2.
	e.connect(t, &testLauncher{loc: "box-1"}, []jam.ManagedStudio{{ActorID: "w2", Location: "box-1"}})
	if got, _ := e.store.GetInstance("w1"); got.Phase != jam.PhaseLost {
		t.Fatalf("w1 phase = %s, want lost", got.Phase)
	}
	if got, _ := e.store.GetInstance("w2"); got.Phase != jam.PhaseLive {
		t.Fatalf("w2 phase = %s, want live", got.Phase)
	}
}

// TestE2EForcedRevokeForgetsStudios: a forced revoke marks the manager's
// studios Lost, so one Reconcile removes their records and revokes their
// identities (the Router has no manager to tear them down on).
func TestE2EForcedRevokeForgetsStudios(t *testing.T) {
	e := newE2E(t, nil)
	e.connect(t, &testLauncher{loc: "box-7"}, nil)
	if _, _, _, err := e.sup.Raise(context.Background(), jam.RaiseSpec{ActorID: "w1", Role: "guest"}); err != nil {
		t.Fatal(err)
	}
	if !hasActor(e.store, "w1") {
		t.Fatal("w1 has no identity after raise")
	}
	if err := e.hub.Revoke("m1", true); err != nil {
		t.Fatal(err)
	}
	if got, ok := e.store.GetInstance("w1"); !ok || got.Phase != jam.PhaseLost {
		t.Fatalf("after forced revoke: present=%v phase=%s, want lost", ok, got.Phase)
	}
	if err := e.sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, ok := e.store.GetInstance("w1"); ok {
		t.Fatalf("w1 still recorded after a reconcile: phase=%s manager=%s", got.Phase, got.Manager)
	}
	if hasActor(e.store, "w1") {
		t.Fatal("w1's identity survived the forced revoke")
	}
}

// TestE2EEgressChangeWhileDisconnected: an egress edit while the manager is
// away is not a failed re-apply; the studio survives and gets the new policy
// once the manager is back.
func TestE2EEgressChangeWhileDisconnected(t *testing.T) {
	e := newE2E(t, nil)
	_, stop := e.connect(t, &testLauncher{loc: "box-7"}, nil)
	if _, _, _, err := e.sup.Raise(context.Background(), jam.RaiseSpec{ActorID: "w1", Role: "guest"}); err != nil {
		t.Fatal(err)
	}
	stop()
	eventually(t, "m1 disconnected", func() bool { return !e.available("m1") })
	if err := e.store.PutRole("default", jam.Role{Name: "guest", Scope: jam.Scope{Destinations: []string{"anthropic"}, TTL: time.Hour, Egress: &jam.EgressPolicy{Domains: []string{"example.com"}}}}); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		*e.clock = e.clock.Add(30 * time.Second)
		if err := e.sup.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	got, ok := e.store.GetInstance("w1")
	if !ok || got.Phase != jam.PhaseLive || got.EgressFailures != 0 {
		t.Fatalf("after 5 disconnected ticks: present=%v phase=%s failures=%d, want live with 0", ok, got.Phase, got.EgressFailures)
	}

	l2 := &testLauncher{loc: "box-7"}
	e.connect(t, l2, []jam.ManagedStudio{{ActorID: "w1", Location: "box-7"}})
	*e.clock = e.clock.Add(30 * time.Second)
	if err := e.sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ps := l2.gotEgress(); len(ps) != 1 || ps[0] == nil || len(ps[0].Domains) != 1 || ps[0].Domains[0] != "example.com" {
		t.Fatalf("egress applied after reconnect = %v", ps)
	}
	if got, _ := e.store.GetInstance("w1"); got.Phase != jam.PhaseLive || got.Egress != jam.EgressFingerprint(&jam.EgressPolicy{Domains: []string{"example.com"}}) {
		t.Fatalf("after reconnect: phase=%s egress=%q", got.Phase, got.Egress)
	}
}

func hasActor(s jam.Store, id string) bool {
	for _, a := range s.ListActors() {
		if a.ID == id {
			return true
		}
	}
	return false
}
