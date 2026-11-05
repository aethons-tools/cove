package managers

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/condition"
	"github.com/aethons-tools/cove/internal/jam/managers/client"
	"github.com/aethons-tools/cove/internal/jam/managers/managerpb"
	"github.com/aethons-tools/cove/internal/jam/managers/wire"
)

// varHost reports whatever disk use the test last set.
type varHost struct {
	mu   sync.Mutex
	used uint64
}

func (h *varHost) set(used uint64) { h.mu.Lock(); h.used = used; h.mu.Unlock() }
func (h *varHost) HostStats(context.Context) (jam.HostStats, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return jam.HostStats{At: time.Unix(100, 0), MemTotal: 8, MemUsed: 2, DiskTotal: 100, DiskUsed: h.used}, nil
}

func diskCond(tr *condition.Tracker, name string) (condition.Condition, bool) {
	for _, c := range tr.Open() {
		if c.Key == "manager.disk:"+name {
			return c, true
		}
	}
	return condition.Condition{}, false
}

func TestHubKeepsStatusReports(t *testing.T) {
	e := newHubEnv(t, nil, nil)
	tok := e.add(t, "m1")
	h := &varHost{used: 40}
	acts := []jam.ManagerActivity{{ID: "a1", Kind: "prepare-kit"}}
	_, stop := runClient(t, e.cc, client.Config{Name: "m1", Token: tok, Host: h, StatusEvery: 10 * time.Millisecond,
		Activities: func() []jam.ManagerActivity { return acts }})
	eventually(t, "host reported", func() bool { st := e.status(t, "m1"); return st.Host != nil && len(st.Activities) == 1 })
	if st := e.status(t, "m1"); st.Host.DiskUsed != 40 || st.Host.MemTotal != 8 || !st.Host.At.Equal(time.Unix(100, 0)) {
		t.Fatalf("host = %+v", st.Host)
	}
	stop()
	eventually(t, "disconnected", func() bool { return !e.status(t, "m1").Connected })
	st := e.status(t, "m1")
	if st.Host == nil || st.Host.DiskUsed != 40 {
		t.Fatalf("host after disconnect = %+v, want the last report kept", st.Host)
	}
	if st.Activities != nil {
		t.Fatalf("activities after disconnect = %v, want dropped", st.Activities)
	}
	// A reconnect keeps the last host until the manager reports again.
	runClient(t, e.cc, client.Config{Name: "m1", Token: tok})
	eventually(t, "reconnected", func() bool { return e.status(t, "m1").Connected })
	if st := e.status(t, "m1"); st.Host == nil || st.Host.DiskUsed != 40 {
		t.Fatalf("host after reconnect = %+v", st.Host)
	}
}

func TestHubDiskCondition(t *testing.T) {
	tr := condition.New(condition.Options{})
	e := newHubEnv(t, func(c *Config) { c.Conditions = tr }, nil)
	tok := e.add(t, "m1")
	h := &varHost{used: 50}
	_, stop := runClient(t, e.cc, client.Config{Name: "m1", Token: tok, Host: h, StatusEvery: 10 * time.Millisecond})
	eventually(t, "host reported", func() bool { return e.status(t, "m1").Host != nil })
	if _, open := diskCond(tr, "m1"); open {
		t.Fatal("condition open at 50%")
	}
	h.set(90)
	eventually(t, "warning at 90%", func() bool { c, ok := diskCond(tr, "m1"); return ok && c.Severity == condition.Warning })
	c, _ := diskCond(tr, "m1")
	if c.Summary != "manager m1 disk 90% full (90B of 100B used)" || c.Fix == "" {
		t.Fatalf("condition = %+v", c)
	}
	h.set(95)
	eventually(t, "critical at 95%", func() bool { c, ok := diskCond(tr, "m1"); return ok && c.Severity == condition.Critical })
	h.set(87)
	eventually(t, "cleared below 88%", func() bool { _, ok := diskCond(tr, "m1"); return !ok })

	// Left open while the manager is disconnected; cleared on revoke.
	h.set(97)
	eventually(t, "critical again", func() bool { _, ok := diskCond(tr, "m1"); return ok })
	stop()
	eventually(t, "disconnected", func() bool { return !e.status(t, "m1").Connected })
	if _, ok := diskCond(tr, "m1"); !ok {
		t.Fatal("condition cleared on disconnect")
	}
	if err := e.hub.Revoke("m1", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := diskCond(tr, "m1"); ok {
		t.Fatal("condition still open after revoke")
	}
}

// The disk condition has hysteresis: it opens at 90% (critical at 95%) but
// resolves only below 88%, so a disk hovering around 90% does not flap.
func TestDiskConditionHysteresis(t *testing.T) {
	tr := condition.New(condition.Options{})
	h := &Hub{cfg: Config{Conditions: tr}}
	steps := []struct {
		pct  uint64
		want condition.Severity // "" = not open
	}{
		{50, ""},
		{89, ""}, // between the marks, closed stays closed
		{90, condition.Warning},
		{89, condition.Warning}, // between the marks, open stays open
		{88, condition.Warning},
		{95, condition.Critical},
		{94, condition.Warning}, // critical drops back to warning below 95
		{96, condition.Critical},
		{89, condition.Warning},
		{87, ""}, // resolved below 88
		{89, ""},
	}
	for i, s := range steps {
		h.diskCondition("m1", &jam.HostStats{DiskTotal: 100, DiskUsed: s.pct})
		c, open := diskCond(tr, "m1")
		switch {
		case s.want == "" && open:
			t.Fatalf("step %d (%d%%): open %s, want closed", i, s.pct, c.Severity)
		case s.want != "" && (!open || c.Severity != s.want):
			t.Fatalf("step %d (%d%%): open=%v %s, want %s", i, s.pct, open, c.Severity, s.want)
		}
	}
}

// Start resolves disk conditions of managers no longer registered (revoked
// while a previous Jam ran, or a condition left from a lost store).
func TestHubStartClearsDiskConditionsOfUnknownManagers(t *testing.T) {
	tr := condition.New(condition.Options{})
	tr.Raise(condition.Condition{Key: "manager.disk:gone", Severity: condition.Warning, Summary: "x"})
	tr.Raise(condition.Condition{Key: "manager.disk:m1", Severity: condition.Warning, Summary: "x"})
	newHubEnv(t, func(c *Config) { c.Conditions = tr }, func(s jam.Store) {
		if err := s.AddManager(jam.ManagerRecord{Name: "m1", TokenHash: "h"}); err != nil {
			t.Fatal(err)
		}
	})
	if _, ok := diskCond(tr, "gone"); ok {
		t.Fatal("condition of an unregistered manager kept")
	}
	if _, ok := diskCond(tr, "m1"); !ok {
		t.Fatal("condition of a registered manager cleared")
	}
}

// A malformed status report is ignored; the stream stays up.
func TestHubIgnoresBadStatus(t *testing.T) {
	e := newHubEnv(t, nil, nil)
	tok := e.add(t, "m1")
	s := rawStream(t, e.cc, tok)
	if err := s.Send(&managerpb.ManagerUp{Msg: &managerpb.ManagerUp_Hello{Hello: &managerpb.Hello{Name: "m1", Version: testVersion}}}); err != nil {
		t.Fatal(err)
	}
	if m, err := s.Recv(); err != nil || m.GetWelcome() == nil {
		t.Fatalf("first message = %v, %v", m, err)
	}
	up := func(f *managerpb.Frame) {
		if err := s.Send(&managerpb.ManagerUp{Msg: &managerpb.ManagerUp_Frame{Frame: f}}); err != nil {
			t.Fatal(err)
		}
	}
	up(&managerpb.Frame{Op: wire.OpStatus, Data: []byte("{not json")})
	up(&managerpb.Frame{Op: wire.OpStatus, Data: []byte(`{"Host":{"disk_total":10,"disk_used":1}}`)})
	eventually(t, "good report kept", func() bool { h := e.status(t, "m1").Host; return h != nil && h.DiskTotal == 10 })
	if !e.status(t, "m1").Connected {
		t.Fatal("bad status ended the stream")
	}
}
