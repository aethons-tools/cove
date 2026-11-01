package jam

import (
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/studio"
	"gopkg.in/yaml.v3"
)

// chainAdmin is an admin handler over a store holding web-dev v1, my-app and
// other-kit following it, and pinned-kit pinned to web-dev@v1.
func chainAdmin(t *testing.T) (http.Handler, Store) {
	t.Helper()
	h, st := newTestAdmin(t)
	mustEnsure(t, st, "web-dev", chainKit("", "base"))
	mustEnsure(t, st, "my-app", chainKit("web-dev", "app"))
	mustEnsure(t, st, "other-kit", chainKit("web-dev", "other"))
	mustEnsure(t, st, "pinned-kit", chainKit("web-dev@v1", "pinned"))
	return h, st
}

// rmKit removes kit name through the guarded store function the console's
// workspace writes use, answering its error's HTTP status (204 on success).
func rmKit(st Store, name string) (int, string) {
	if err := RemoveKitChecked(st, slog.New(slog.DiscardHandler), name); err != nil {
		return WriteStatus(err, http.StatusNotFound), err.Error()
	}
	return http.StatusNoContent, ""
}

// pinKit pins kit name to version through PinKitChecked, answering its
// error's HTTP status (204 on success).
func pinKit(st Store, name string, version int) (int, string) {
	if err := PinKitChecked(st, name, version); err != nil {
		return WriteStatus(err, http.StatusNotFound), err.Error()
	}
	return http.StatusNoContent, ""
}

func TestAdminKitRmGuardedByChildren(t *testing.T) {
	_, st := chainAdmin(t)
	code, body := rmKit(st, "web-dev")
	if code != http.StatusConflict {
		t.Fatalf("rm parent = %d %s", code, body)
	}
	if got, want := body, "kit web-dev is the parent of my-app, other-kit, pinned-kit"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if _, ok := st.GetKit("web-dev"); !ok {
		t.Error("kit removed despite 409")
	}
	// Leaves can go, then the parent.
	for _, n := range []string{"my-app", "other-kit", "pinned-kit", "web-dev"} {
		if code, body := rmKit(st, n); code != http.StatusNoContent {
			t.Fatalf("rm %s = %d %s", n, code, body)
		}
	}
}

func TestAdminKitRmRoleGuardUnchanged(t *testing.T) {
	_, st := chainAdmin(t)
	mustCreateProject(t, st, "acme")
	if err := st.PutRole("acme", Role{Name: "impl", Kit: "my-app"}); err != nil {
		t.Fatal(err)
	}
	if code, body := rmKit(st, "my-app"); code != http.StatusConflict || !strings.Contains(body, "is referenced by role acme/impl") {
		t.Fatalf("rm = %d %s", code, body)
	}
}

func TestAdminKitPinGuardedByFollowers(t *testing.T) {
	_, st := chainAdmin(t)
	mustEnsure(t, st, "web-dev", chainKit("", "base2")) // v2
	code, body := pinKit(st, "web-dev", 1)
	if code != http.StatusConflict {
		t.Fatalf("pin = %d %s", code, body)
	}
	want := "kit web-dev is followed by my-app, other-kit (pin those to a web-dev version, or point them elsewhere)"
	if body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
	if k, _ := st.GetKit("web-dev"); k.Current != 2 {
		t.Errorf("current = %d, want unchanged 2", k.Current)
	}
}

func TestAdminKitPinAllowedWithOnlyPinnedChildren(t *testing.T) {
	_, st := newTestAdmin(t)
	mustEnsure(t, st, "web-dev", chainKit("", "base"))
	mustEnsure(t, st, "web-dev", chainKit("", "base2"))
	mustEnsure(t, st, "pinned-kit", chainKit("web-dev@v1", "p"))
	if code, body := pinKit(st, "web-dev", 1); code != http.StatusNoContent {
		t.Fatalf("pin = %d %s", code, body)
	}
	// An unreferenced kit pins freely, and a missing one is still 404.
	mustEnsure(t, st, "solo", chainKit("", "s"))
	if code, _ := pinKit(st, "nope", 1); code != http.StatusNotFound {
		t.Errorf("pin missing = %d", code)
	}
}

func TestAdminKitGetChainFields(t *testing.T) {
	h, st := chainAdmin(t)
	mustEnsure(t, st, "web-dev", chainKit("", "base2")) // v2; my-app was pushed against v1
	var res KitConfigResult
	getJSON(t, h, "/admin/kits/my-app", &res)
	if len(res.Chain) != 1 || res.Chain[0] != (KitChainLink{Name: "web-dev", Version: 2, Follow: true}) {
		t.Errorf("chain = %+v", res.Chain)
	}
	if res.Parent == nil || *res.Parent != (KitParentRef{Name: "web-dev", Version: 1}) {
		t.Errorf("parent = %+v", res.Parent)
	}
	if !strings.Contains(res.Effective, "base2") || !strings.Contains(res.Effective, "app") {
		t.Errorf("effective = %q", res.Effective)
	}
	if strings.Contains(res.Effective, "web-dev") {
		t.Errorf("effective still names the parent: %q", res.Effective)
	}
	if _, err := studio.ParseStudioKit([]byte(res.Effective)); err != nil {
		t.Errorf("effective does not parse: %v", err)
	}
	var parent KitConfigResult
	getJSON(t, h, "/admin/kits/web-dev", &parent)
	if got := strings.Join(parent.UsedByKits, ","); got != "my-app,other-kit,pinned-kit" {
		t.Errorf("used_by_kits = %v", parent.UsedByKits)
	}
	if len(parent.Chain) != 0 || parent.Effective == "" {
		t.Errorf("root: chain=%v effective=%q", parent.Chain, parent.Effective)
	}
}

func TestAdminKitListParentColumn(t *testing.T) {
	h, _ := chainAdmin(t)
	var out []KitSummary
	getJSON(t, h, "/admin/kits", &out)
	got := map[string]string{}
	for _, k := range out {
		got[k.Name] = k.Parent
	}
	want := map[string]string{"web-dev": "", "my-app": "web-dev", "other-kit": "web-dev", "pinned-kit": "web-dev@v1"}
	for n, w := range want {
		if got[n] != w {
			t.Errorf("parent[%s] = %q, want %q", n, got[n], w)
		}
	}
}

func TestAdminKitPinBadVersionIs404(t *testing.T) {
	_, st := chainAdmin(t)
	if code, body := pinKit(st, "web-dev", 99); code != http.StatusNotFound {
		t.Fatalf("pin v99 = %d %s, want 404 (not the follow 409)", code, body)
	}
}

func TestAdminKitGetChainError(t *testing.T) {
	h, st := chainAdmin(t)
	if err := st.RemoveKit("web-dev"); err != nil { // bypass the guard: break my-app's chain
		t.Fatal(err)
	}
	var res KitConfigResult
	getJSON(t, h, "/admin/kits/my-app", &res)
	if !strings.Contains(res.ChainError, "web-dev") || res.Effective != "" || len(res.Chain) != 0 {
		t.Errorf("chain_error=%q effective=%q chain=%v", res.ChainError, res.Effective, res.Chain)
	}
}

func TestAdminKitEffectiveUnchainedIsOwn(t *testing.T) {
	h, st := newTestAdmin(t)
	sk := chainKit("", "p")
	sk.Egress = []string{"z.example.com", "a.example.com", "z.example.com"}
	mustEnsure(t, st, "solo", sk)
	var res KitConfigResult
	getJSON(t, h, "/admin/kits/solo", &res)
	own, err := studio.ParseStudioKit([]byte(res.Config))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := yaml.Marshal(own)
	if res.Effective != string(want) || !strings.Contains(res.Effective, "z.example.com\n    - a.example.com\n    - z.example.com") {
		t.Errorf("effective = %q\nwant own %q", res.Effective, want)
	}
}

// The list reads each current kit.yml for its parent without parsing it (no
// parse-cache entry); a pre-tree version's text and a broken version read as
// before.
func TestAdminKitListReadsKitYMLOnly(t *testing.T) {
	h, st := newTestAdmin(t)
	tr := pushTree(t, st, "tree", "kit.yml", "kind: studio\nbase: {kit: web}\nprompt: list reads only kit.yml\n")
	if err := st.PutKitDocs([]Kit{
		{Name: "old", Current: 1, Versions: map[int]string{1: "kind: studio\nbase: {kit: web@v2}\n"}},
		{Name: "gone", Current: 1, Broken: map[int]string{1: "its base.context is not valid base64"}},
	}, KitObjects{}); err != nil {
		t.Fatal(err)
	}
	var out []KitSummary
	getJSON(t, h, "/admin/kits", &out)
	got := map[string]string{}
	for _, k := range out {
		got[k.Name] = k.Parent
	}
	if got["tree"] != "web" || got["old"] != "web@v2" || got["gone"] != "" {
		t.Fatalf("parents = %v", got)
	}
	if _, ok := kitParses.get(tr.Hash()); ok {
		t.Fatal("the list parsed the kit")
	}
}
