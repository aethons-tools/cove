package jam

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
)

// pinFixture is a placing supervisor over managers names, with a seeded
// standing session impl/spider.
func pinFixture(t *testing.T, names ...string) (*Supervisor, Store, *placingFake, ident.ID, string) {
	t.Helper()
	p := newPlacingFake(names...)
	sup, store, _ := supTestKit(t, p)
	pid, ok := ProjectIDOf(store, "default")
	if !ok {
		t.Fatal("no default project")
	}
	sid := string(ident.New(ident.Session))
	if err := store.PutStandingSession(pid, "guest", "spider", sid); err != nil {
		t.Fatal(err)
	}
	return sup, store, p, pid, sid
}

func raiseStanding(sup *Supervisor, sid string, pl Placement) (Instance, error) {
	inst, _, _, err := sup.Raise(context.Background(), RaiseSpec{
		ActorID: sid, Role: "guest", Name: "spider", SessionKind: SessionKindStanding, Placement: pl,
	})
	return inst, err
}

func TestStandingFirstRaisePinsPlacedManager(t *testing.T) {
	sup, store, p, pid, sid := pinFixture(t, "a", "b")
	p.avail["a"] = false
	inst, err := raiseStanding(sup, sid, Placement{})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Manager != "b" {
		t.Fatalf("manager = %q, want b", inst.Manager)
	}
	if got := store.StandingSessionManager(pid, "guest", "spider"); got != "b" {
		t.Fatalf("pin = %q, want b", got)
	}
}

func TestStandingRaiseGoesOnlyToPin(t *testing.T) {
	sup, store, p, pid, sid := pinFixture(t, "a", "b")
	_ = store.SetStandingSessionManager(pid, "guest", "spider", "a")
	p.avail["a"] = false
	_, err := raiseStanding(sup, sid, Placement{Timeout: 0})
	var nme *NoManagerError
	if !errors.As(err, &nme) {
		t.Fatalf("err = %v, want NoManagerError", err)
	}
	if len(p.by["b"].raised) != 0 || len(p.by["a"].raised) != 0 {
		t.Fatalf("raised a=%v b=%v, want none", p.by["a"].raised, p.by["b"].raised)
	}
}

func TestStandingRaiseExcludedPinFailsBeforeEnroll(t *testing.T) {
	sup, store, p, pid, sid := pinFixture(t, "a", "b")
	_ = store.SetStandingSessionManager(pid, "guest", "spider", "a")
	_, err := raiseStanding(sup, sid, Placement{Managers: []string{"b"}})
	if !errors.Is(err, ErrPinExcluded) {
		t.Fatalf("err = %v, want ErrPinExcluded", err)
	}
	for _, a := range store.ListActors() {
		if a.ID == sid {
			t.Fatal("actor was enrolled")
		}
	}
	if len(p.by["a"].raised)+len(p.by["b"].raised) != 0 {
		t.Fatal("a raise happened")
	}
}

func TestStandingPinToUnconfiguredManagerIsCleared(t *testing.T) {
	p := newPlacingFake("a")
	var buf bytes.Buffer
	sup, store, _ := supTestKit(t, p)
	sup.log = slog.New(slog.NewTextHandler(&buf, nil))
	pid, _ := ProjectIDOf(store, "default")
	sid := string(ident.New(ident.Session))
	_ = store.PutStandingSession(pid, "guest", "spider", sid)
	_ = store.SetStandingSessionManager(pid, "guest", "spider", "gone")
	inst, err := raiseStanding(sup, sid, Placement{})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Manager != "a" || store.StandingSessionManager(pid, "guest", "spider") != "a" {
		t.Fatalf("manager %q pin %q, want a/a", inst.Manager, store.StandingSessionManager(pid, "guest", "spider"))
	}
	if !strings.Contains(buf.String(), "placing fresh, state not carried over") {
		t.Fatalf("no warning logged: %s", buf.String())
	}
}

func TestStandingResetClearsPin(t *testing.T) {
	_, store, _, pid, _ := pinFixture(t, "a")
	_ = store.SetStandingSessionManager(pid, "guest", "spider", "a")
	if err := store.RemoveStandingSession(pid, "guest", "spider"); err != nil {
		t.Fatal(err)
	}
	if got := store.StandingSessionManager(pid, "guest", "spider"); got != "" {
		t.Fatalf("pin = %q after reset", got)
	}
}

func TestPlaceholderLauncherIgnoresPin(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	pid, _ := ProjectIDOf(store, "default")
	sid := string(ident.New(ident.Session))
	_ = store.PutStandingSession(pid, "guest", "spider", sid)
	_ = store.SetStandingSessionManager(pid, "guest", "spider", "local")
	if _, err := raiseStanding(sup, sid, Placement{Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if got := store.StandingSessionManager(pid, "guest", "spider"); got != "local" {
		t.Fatalf("pin = %q, want untouched local", got)
	}
}

func TestPersonalSessionNotPinned(t *testing.T) {
	sup, store, _, pid, _ := pinFixture(t, "a")
	_, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "p1", Role: "guest", Name: "spider", SessionKind: SessionKindPersonal})
	if err != nil {
		t.Fatal(err)
	}
	if got := store.StandingSessionManager(pid, "guest", "spider"); got != "" {
		t.Fatalf("pin = %q, want none", got)
	}
}

func TestStandingPinSurvivesConfigExportImport(t *testing.T) {
	_, src, _, pid, sid := pinFixture(t, "a")
	if err := src.SetStandingSessionManager(pid, "guest", "spider", "mac-1"); err != nil {
		t.Fatal(err)
	}
	snap := src.ExportConfig()
	dst := NewMemStore()
	if err := dst.ImportConfig(snap); err != nil {
		t.Fatal(err)
	}
	dpid, _ := ProjectIDOf(dst, "default")
	if got, ok := dst.StandingSessionID(dpid, "guest", "spider"); !ok || got != sid {
		t.Fatalf("imported session = %q, %v", got, ok)
	}
	if got := dst.StandingSessionManager(dpid, "guest", "spider"); got != "mac-1" {
		t.Fatalf("imported pin = %q, want mac-1", got)
	}
}
