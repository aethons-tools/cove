package jam

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// raisingUpgrader is a StandingUpgrader that does what the reconciler does —
// a plain Teardown, then a Raise under the same id — or fails with err.
type raisingUpgrader struct {
	sup   *Supervisor
	calls int
	err   error
}

func (u *raisingUpgrader) UpgradeStanding(ctx context.Context, project, role, name string) error {
	u.calls++
	if u.err != nil {
		return u.err
	}
	id := StandingActorID(project, role, name)
	if err := u.sup.Teardown(ctx, id); err != nil {
		return err
	}
	_, _, _, err := u.sup.Raise(ctx, RaiseSpec{ActorID: id, Project: project, Role: role, Name: name, SessionKind: SessionKindStanding})
	return err
}

const upgradePath = "/admin/roles/default/dev/standing/bot/upgrade"

// upgradeKit: role default/dev (kit "web") declaring standing session "bot",
// raised on image asm1, behind an admin handler; the upgrader is wired.
func upgradeKit(t *testing.T) (http.Handler, Store, *fakeLauncher, *raisingUpgrader, string) {
	t.Helper()
	fl := &fakeLauncher{liveness: LivenessAlive, asm: "asm1"}
	sup, store := imageKit(t, fl)
	if err := AddStanding(store, "default", "dev", StandingSession{Name: "bot", Prompt: "p"}); err != nil {
		t.Fatal(err)
	}
	id := StandingActorID("default", "dev", "bot")
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: id, Project: "default", Role: "dev", Name: "bot", SessionKind: SessionKindStanding}); err != nil {
		t.Fatal(err)
	}
	up := &raisingUpgrader{sup: sup}
	sup.SetStandingUpgrader(up)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewAdminHandler(store, sup, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil, nil)
	return h, store, fl, up, id
}

// setState sets the standing instance's phase and activity.
func setState(t *testing.T, store Store, id string, p Phase, a Activity) {
	t.Helper()
	inst, ok := store.GetInstance(id)
	if !ok {
		t.Fatalf("%s has no instance", id)
	}
	inst.Phase, inst.Activity = p, a
	if err := store.PutInstance(inst); err != nil {
		t.Fatal(err)
	}
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

// An idled (or waiting) session on a stale image is torn down — state kept,
// no purge — and re-raised within the call; 200 reports the new tag.
func TestAdminStandingUpgradeIdle(t *testing.T) {
	for _, st := range []struct {
		p Phase
		a Activity
	}{{PhaseIdled, ActivityWaiting}, {PhaseLive, ActivityWaiting}} {
		h, store, fl, up, id := upgradeKit(t)
		setState(t, store, id, st.p, st.a)
		fl.asm = "asm2" // a Jam-side rebuild: the session's image is stale
		code, res, body := upgrade(t, h, upgradePath)
		if code != http.StatusOK || !res.Upgraded || res.Pending {
			t.Fatalf("%s/%s: upgrade = %d %s", st.p, st.a, code, body)
		}
		inst, ok := store.GetInstance(id)
		if !ok || !strings.HasSuffix(inst.ImageTag, "-asm2") || res.Image != inst.ImageTag {
			t.Fatalf("%s/%s: re-raised on %q (result %q), want the asm2 image", st.p, st.a, inst.ImageTag, res.Image)
		}
		if up.calls != 1 || len(fl.tornDown) != 1 || len(fl.raised) != 2 {
			t.Fatalf("%s/%s: calls=%d torn=%v raised=%v; want one teardown and a second raise", st.p, st.a, up.calls, fl.tornDown, fl.raised)
		}
		if len(fl.purged) != 0 {
			t.Fatalf("%s/%s: upgrade must never purge state; purged %v", st.p, st.a, fl.purged)
		}
	}
}

// A session mid-episode (running, holding, raising) is refused 409 naming
// its state; --force upgrades it anyway.
func TestAdminStandingUpgradeBusy(t *testing.T) {
	for _, st := range []struct {
		p    Phase
		a    Activity
		want string
	}{
		{PhaseLive, ActivityRunning, "running"},
		{PhaseLive, ActivityHolding, "holding"},
		{PhaseRaising, "", "raising"},
	} {
		h, store, fl, up, id := upgradeKit(t)
		setState(t, store, id, st.p, st.a)
		fl.asm = "asm2"
		code, _, body := upgrade(t, h, upgradePath)
		if code != http.StatusConflict || !strings.Contains(body, st.want) {
			t.Fatalf("%s/%s: upgrade = %d %q, want 409 naming %q", st.p, st.a, code, body, st.want)
		}
		if up.calls != 0 {
			t.Fatalf("%s/%s: a refused upgrade must not reach the reconciler", st.p, st.a)
		}
		code, res, body := upgrade(t, h, upgradePath+"?force=true")
		if code != http.StatusOK || !res.Upgraded || up.calls != 1 {
			t.Fatalf("%s/%s: forced upgrade = %d %s", st.p, st.a, code, body)
		}
	}
}

// A session already on the current image is left running (200, not
// upgraded) — unless forced, which restarts it on the same image.
func TestAdminStandingUpgradeAlreadyCurrent(t *testing.T) {
	h, store, fl, up, id := upgradeKit(t)
	setState(t, store, id, PhaseLive, ActivityRunning) // even busy: nothing to do
	code, res, body := upgrade(t, h, upgradePath)
	if code != http.StatusOK || res.Upgraded || res.Pending || !strings.HasSuffix(res.Image, "-asm1") || !strings.Contains(res.Reason, "already current") {
		t.Fatalf("already current = %d %s", code, body)
	}
	if up.calls != 0 || len(fl.tornDown) != 0 {
		t.Fatalf("already current must not restart; calls=%d torn=%v", up.calls, fl.tornDown)
	}
	code, res, body = upgrade(t, h, upgradePath+"?force=true")
	if code != http.StatusOK || !res.Upgraded || up.calls != 1 {
		t.Fatalf("forced = %d %s", code, body)
	}
}

// A name with no studio is raised now; a re-raise that doesn't complete is 202
// pending; unknown name/role is 404; no reconciler is 503; an actor id held by
// another cove is 409.
func TestAdminStandingUpgradeEdges(t *testing.T) {
	h, store, fl, up, id := upgradeKit(t)
	if err := up.sup.Teardown(context.Background(), id); err != nil { // down (state kept)
		t.Fatal(err)
	}
	code, res, body := upgrade(t, h, upgradePath)
	if code != http.StatusOK || !res.Upgraded || up.calls != 1 {
		t.Fatalf("no instance = %d %s; want a raise", code, body)
	}
	if _, ok := store.GetInstance(id); !ok {
		t.Fatal("no instance: upgrade must raise it")
	}

	fl.asm = "asm2"
	setState(t, store, id, PhaseIdled, ActivityWaiting)
	up.err = errors.New("grant denied")
	code, res, body = upgrade(t, h, upgradePath)
	if code != http.StatusAccepted || !res.Pending || !strings.Contains(res.Reason, "grant denied") {
		t.Fatalf("failed re-raise = %d %s; want 202 pending", code, body)
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
