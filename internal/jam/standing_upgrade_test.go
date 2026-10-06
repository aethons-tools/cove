package jam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// fakeUpgrader is a StandingUpgrader queue: it records each QueueUpgrade
// (name → force), reports state, and fails queueing with err.
type fakeUpgrader struct {
	queued map[string]bool
	state  map[string]string
	err    error
}

func newFakeUpgrader() *fakeUpgrader {
	return &fakeUpgrader{queued: map[string]bool{}, state: map[string]string{}}
}

func (f *fakeUpgrader) QueueUpgrade(project, role, name string, force bool) error {
	if f.err != nil {
		return f.err
	}
	f.queued[name] = f.queued[name] || force
	if f.state[name] == "" {
		f.state[name] = UpgradeQueued
	}
	return nil
}

func (f *fakeUpgrader) UpgradeState(_, _, name string) string { return f.state[name] }

const upgradePath = "/admin/roles/default/dev/standing/bot/upgrade"

// upgradeKit: role default/dev (kit "web") declaring standing session "bot",
// raised on image asm1, behind an admin handler; the upgrader is wired.
func upgradeKit(t *testing.T) (http.Handler, Store, *Supervisor, *fakeLauncher, *fakeUpgrader, string) {
	t.Helper()
	fl := &fakeLauncher{liveness: LivenessAlive, asm: "asm1"}
	sup, store := imageKit(t, fl)
	if err := AddStanding(store, "default", "dev", StandingSession{Name: "bot", Prompt: "p"}); err != nil {
		t.Fatal(err)
	}
	id := SeedStandingSession(store, "default", "dev", "bot")
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: id, Project: "default", Role: "dev", Name: "bot", SessionKind: SessionKindStanding}); err != nil {
		t.Fatal(err)
	}
	up := newFakeUpgrader()
	sup.SetStandingUpgrader(up)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewAdminHandler(store, sup, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil, nil)
	return h, store, sup, fl, up, id
}

func upgrade(t *testing.T, h http.Handler, path string) (int, StandingUpgradeResult, string) {
	t.Helper()
	rec := doReq(t, h, "POST", path, nil)
	var res StandingUpgradeResult
	if rec.Code == http.StatusOK || rec.Code == http.StatusAccepted {
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("body %q: %v", rec.Body.String(), err)
		}
	}
	return rec.Code, res, rec.Body.String()
}

// The request only records intent (COV-251): a stale session — busy or not —
// is queued with the reconciler and answered 202 {pending, state:queued} at
// once; nothing is torn down or raised in the request.
func TestAdminStandingUpgradeQueues(t *testing.T) {
	for _, a := range []Activity{ActivityWaiting, ActivityRunning} {
		h, store, _, fl, up, id := upgradeKit(t)
		inst, _ := store.GetInstance(id)
		inst.Activity = a
		if err := store.PutInstance(inst); err != nil {
			t.Fatal(err)
		}
		fl.asm = "asm2" // stale
		code, res, body := upgrade(t, h, upgradePath)
		if code != http.StatusAccepted || !res.Pending || res.State != UpgradeQueued {
			t.Fatalf("%s: upgrade = %d %s, want 202 queued", a, code, body)
		}
		if force, ok := up.queued["bot"]; !ok || force {
			t.Fatalf("%s: queued = %v, want bot unforced", a, up.queued)
		}
		if len(fl.tornDown) != 0 || len(fl.raised) != 1 || len(fl.purged) != 0 {
			t.Fatalf("%s: the request must not tear down, raise or purge; torn=%v raised=%v purged=%v", a, fl.tornDown, fl.raised, fl.purged)
		}
	}
}

// An already-current session is 200 "already current" and not queued; force
// queues it anyway. A name with no studio is queued (the reconciler raises it).
func TestAdminStandingUpgradeAlreadyCurrentAndDown(t *testing.T) {
	h, _, sup, _, up, id := upgradeKit(t)
	code, res, body := upgrade(t, h, upgradePath)
	if code != http.StatusOK || res.Pending || res.Reason != "already current" || !strings.HasSuffix(res.Image, "-asm1") {
		t.Fatalf("already current = %d %s", code, body)
	}
	if len(up.queued) != 0 {
		t.Fatalf("already current must not queue; queued %v", up.queued)
	}
	if code, res, body := upgrade(t, h, upgradePath+"?force=true"); code != http.StatusAccepted || !res.Pending || !up.queued["bot"] {
		t.Fatalf("forced = %d %s (queued %v)", code, body, up.queued)
	}

	h, _, sup, _, up, id = upgradeKit(t)
	if err := sup.Teardown(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if code, _, body := upgrade(t, h, upgradePath); code != http.StatusAccepted || len(up.queued) != 1 {
		t.Fatalf("no studio = %d %s, want queued", code, body)
	}
}

// Error mapping: only an accepted upgrade is 2xx. A pending reset is 409, a
// name dismissed meanwhile 404, any other queue failure 500; unknown
// name/role 404; no reconciler 503; an actor id held by another cove 409.
func TestAdminStandingUpgradeErrors(t *testing.T) {
	h, store, _, fl, up, id := upgradeKit(t)
	fl.asm = "asm2"
	for _, c := range []struct {
		err  error
		want int
	}{
		{fmt.Errorf("%w: x", ErrStandingResetPending), http.StatusConflict},
		{fmt.Errorf("%w: x", ErrStandingNotDeclared), http.StatusNotFound},
		{errors.New("boom"), http.StatusInternalServerError},
	} {
		up.err = c.err
		if code, _, body := upgrade(t, h, upgradePath); code != c.want {
			t.Fatalf("queue error %v = %d %s, want %d", c.err, code, body, c.want)
		}
	}
	up.err = nil
	for _, p := range []string{"/admin/roles/default/dev/standing/nobody/upgrade", "/admin/roles/default/ghost/standing/bot/upgrade"} {
		if code, _, _ := upgrade(t, h, p); code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404", p, code)
		}
	}
	inst, _ := store.GetInstance(id)
	inst.SessionKind = ""
	if err := store.PutInstance(inst); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := upgrade(t, h, upgradePath+"?force=true"); code != http.StatusConflict {
		t.Fatalf("held id = %d, want 409", code)
	}

	h2, store2, _, _ := newTestAdminWithSupervisorAndLauncher(t)
	if err := store2.PutRole("default", Role{Name: "dev", Allocation: RoleAllocation{Standing: []StandingSession{{Name: "bot", Prompt: "p"}}}}); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := upgrade(t, h2, upgradePath); code != http.StatusServiceUnavailable {
		t.Fatalf("no reconciler = %d, want 503", code)
	}
}

// The standing list carries each name's pending upgrade state.
func TestAdminStandingListShowsUpgrade(t *testing.T) {
	h, _, _, _, up, _ := upgradeKit(t)
	up.state["bot"] = UpgradeWaitingIdle + ": live, activity running"
	rec := doReq(t, h, "GET", "/admin/roles/default/dev/standing", nil)
	var list []StandingStatus
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list) != 1 ||
		list[0].Name != "bot" || list[0].Prompt != "p" || list[0].Upgrade != up.state["bot"] {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
}

// UpgradeBusy: idle is idled, or live and waiting/blocked/done.
func TestUpgradeBusy(t *testing.T) {
	for _, c := range []struct {
		p    Phase
		a    Activity
		busy bool
	}{
		{PhaseIdled, ActivityWaiting, false},
		{PhaseLive, ActivityWaiting, false},
		{PhaseLive, ActivityBlocked, false},
		{PhaseLive, ActivityDone, false},
		{PhaseLive, ActivityRunning, true},
		{PhaseLive, ActivityHolding, true},
		{PhaseLive, "", true},
		{PhaseRaising, "", true},
	} {
		if got := UpgradeBusy(Instance{Phase: c.p, Activity: c.a}); (got != "") != c.busy {
			t.Errorf("%s/%s busy = %q, want busy=%v", c.p, c.a, got, c.busy)
		}
	}
}

// PrepareImage prepares exactly the image a raise would run now (its Key
// matches CurrentImage's) through the launcher's PrepareKit, reporting its
// status; a role that raises no kit is ready as is.
func TestSupervisorPrepareImage(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive, asm: "asm1"}
	sup, store := imageKit(t, fl)
	cur, st, err := sup.PrepareImage(context.Background(), "default", "dev")
	want, _ := sup.CurrentImage("default", "dev")
	if err != nil || st.State != KitPreparing || cur.Key() != want.Key() || want.Key() == "" || fl.prepareCalls != 1 || fl.preparedDef.Ref != want.Kit {
		t.Fatalf("PrepareImage = %+v %+v %v (want key %q, prepares %d)", cur, st, err, want.Key(), fl.prepareCalls)
	}
	fl.prepareState = KitReady
	if _, st, _ := sup.PrepareImage(context.Background(), "default", "dev"); st.State != KitReady {
		t.Fatalf("ready = %+v", st)
	}
	if err := store.PutRole("default", Role{Name: "bare"}); err != nil {
		t.Fatal(err)
	}
	if cur, st, err := sup.PrepareImage(context.Background(), "default", "bare"); err != nil || st.State != KitReady || cur.HasKit || fl.prepareCalls != 2 {
		t.Fatalf("no kit = %+v %+v %v", cur, st, err)
	}
	if _, _, err := sup.PrepareImage(context.Background(), "default", "ghost"); err == nil {
		t.Fatal("unknown role must error")
	}
}
