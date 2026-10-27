package jam

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"testing"
)

// tagPlacing is a placingFake whose managers name images differently
// (each fakeLauncher's asm) and that lists its available managers in order.
type tagPlacing struct {
	*placingFake
	avail []string
}

func (p tagPlacing) AvailableNames() []string { return p.avail }

func perManagerKit(t *testing.T) (*Supervisor, Store, tagPlacing) {
	t.Helper()
	pf := newPlacingFake("m1", "local")
	pf.by["m1"].asm = "asmM1"
	pf.by["local"].asm = "asmLocal"
	tp := tagPlacing{placingFake: pf, avail: []string{"m1", "local"}}
	sup, store := imageKit(t, tp.fakeLauncher)
	sup.launcher = tp
	return sup, store, tp
}

func TestCurrentImagePerManager(t *testing.T) {
	sup, _, _ := perManagerKit(t)
	m1, err := sup.CurrentImage("default", "dev", "m1")
	if err != nil {
		t.Fatal(err)
	}
	loc, err := sup.CurrentImage("default", "dev", LocalManager)
	if err != nil {
		t.Fatal(err)
	}
	if m1.Tag == "" || loc.Tag == "" || m1.Tag == loc.Tag {
		t.Fatalf("tags m1=%q local=%q, want distinct non-empty", m1.Tag, loc.Tag)
	}
}

func raisedOn(t *testing.T, store Store, id, manager, tag string) {
	t.Helper()
	if err := store.PutInstance(Instance{ActorID: id, Project: "default", Role: "dev", Phase: PhaseLive, Manager: manager, ImageTag: tag}); err != nil {
		t.Fatal(err)
	}
}

func TestImageStatusUsesInstanceManager(t *testing.T) {
	sup, store, _ := perManagerKit(t)
	m1, _ := sup.CurrentImage("default", "dev", "m1")
	raisedOn(t, store, "w1", "m1", m1.Tag)
	if got := summaryImage(t, store, sup, "w1"); got != "ok" {
		t.Fatalf("status = %q, want ok (tag matches m1's current)", got)
	}
}

func TestImageStatusLegacyInstanceUsesLocal(t *testing.T) {
	sup, store, _ := perManagerKit(t)
	loc, _ := sup.CurrentImage("default", "dev", LocalManager)
	raisedOn(t, store, "w1", "", loc.Tag)
	if got := summaryImage(t, store, sup, "w1"); got != "ok" {
		t.Fatalf("status = %q, want ok (legacy instance compares with local)", got)
	}
}

func TestRoleImagesOneRowPerConnectedManager(t *testing.T) {
	sup, _, tp := perManagerKit(t)
	rows, err := sup.RoleImages("default", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Manager != "m1" || rows[1].Manager != "local" {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Tag != tp.by["m1"].ImageTag(rows[0].Kit) || rows[1].Tag != tp.by["local"].ImageTag(rows[1].Kit) || !rows[0].HasKit || rows[0].Tag == rows[1].Tag {
		t.Fatalf("rows = %+v", rows)
	}
	// A manager that is not connected is absent (m2 is registered but not available).
	if slices.ContainsFunc(rows, func(r RoleImage) bool { return r.Manager == "m2" }) {
		t.Fatal("disconnected m2 listed")
	}
}

func TestRoleImagesWithoutRouter(t *testing.T) {
	sup, _ := imageKit(t, &fakeLauncher{liveness: LivenessAlive, asm: "a"})
	rows, err := sup.RoleImages("default", "dev")
	if err != nil || len(rows) != 1 || rows[0].Manager != "" || rows[0].Tag == "" {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
}

func TestRoleImagesRoute(t *testing.T) {
	sup, store, _ := perManagerKit(t)
	h := NewAdminHandler(store, sup, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	rec := doReq(t, h, "GET", "/admin/roles/default/dev/images", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Images []RoleImage `json:"images"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Images) != 2 || out.Images[0].Manager != "m1" || out.Images[0].Kit.ID != "web" {
		t.Fatalf("body = %s (%v)", rec.Body, err)
	}
	if rec := doReq(t, h, "GET", "/admin/roles/default/nope/images", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing role: code %d", rec.Code)
	}
}
