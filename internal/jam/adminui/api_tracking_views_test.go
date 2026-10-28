package adminui_test

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/condition"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// The tracked source links its repo and commit (7 chars); each managed object
// carries its state — drifted when edited in Jam, missing when gone (no
// page then) — and the newest apply.
func TestTrackingAPIStates(t *testing.T) {
	h, store := managedUI(t)
	trackManaged(t, store)
	for _, d := range store.ListDestinations() {
		if d.Name == "gh" {
			d.Note = "edited in Jam"
			if err := store.AddDestination(d); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := store.SetJamContext(sessionctx.Layer{}); err != nil { // the managed Jam context goes missing
		t.Fatal(err)
	}
	var tr adminui.Tracking
	apiDo(t, h, "GET", "/app/admin/api/tracking", nil, &tr)
	want := adminui.TrackingSource{Label: "https://github.com/acme/jam@main", Repo: "https://github.com/acme/jam", RepoLink: "https://github.com/acme/jam", Ref: "main",
		Commit: "abc1234", CommitLink: "https://github.com/acme/jam/commit/" + managedSHA, Applied: "1970-01-01 00:16 UTC"}
	if tr.Source == nil || *tr.Source != want {
		t.Errorf("source = %+v, want %+v", tr.Source, want)
	}
	rows := map[string]adminui.ManagedRow{}
	for _, r := range tr.Rows {
		rows[r.Kind+" "+r.Name] = r
	}
	if r := rows["destination gh"]; r.State != jam.ConfigDrifted || r.Path != "config/destinations/gh" || r.Commit != "abc1234" || r.Applied != "1970-01-01 00:16 UTC" {
		t.Errorf("gh = %+v", r)
	}
	var ctx adminui.ManagedRow
	for _, r := range tr.Rows {
		if r.Kind == jam.ConfigKindJamContext {
			ctx = r
		}
	}
	if ctx.State != jam.ConfigMissing || ctx.Path != "" {
		t.Errorf("jam context = %+v", ctx)
	}
	if r := rows["role app/ic"]; r.State != jam.ConfigInSync || r.Path != "config/projects/app/agents/ic" {
		t.Errorf("role = %+v", r)
	}
	if tr.Drifted != 2 || tr.StatesErr || tr.LastCommit != "abc1234" || tr.LastApplied != "1970-01-01 00:16 UTC" || tr.CanGit {
		t.Errorf("tracking = %+v", tr)
	}
}

// A managed connection has no page of its own: its row carries no path.
func TestTrackingAPIConnectionNoPath(t *testing.T) {
	store := newStore(t)
	applyManaged(t, store, map[string]string{"connections/disc.yml": "kind: connection\nversion: 1\nservice: discord\ncred: tok\n"})
	var tr adminui.Tracking
	apiDo(t, uiHandler(t, store), "GET", "/app/admin/api/tracking", nil, &tr)
	if len(tr.Rows) != 1 || tr.Rows[0].Kind != jam.ConfigKindConnection || tr.Rows[0].Path != "" || tr.Rows[0].State != jam.ConfigInSync {
		t.Errorf("rows = %+v", tr.Rows)
	}
}

// A stored source that does not re-parse (a hand-made URL with userinfo) is
// "invalid": no credential reaches the response; its commit is still short.
func TestTrackingAPIInvalidSource(t *testing.T) {
	h, store := managedUI(t)
	if err := store.SetConfigSource(&jam.ConfigSource{Repo: "https://user:hunter2@github.com/acme/jam", Ref: "main", Commit: managedSHA}); err != nil {
		t.Fatal(err)
	}
	var tr adminui.Tracking
	rec := apiDo(t, h, "GET", "/app/admin/api/tracking", nil, &tr)
	if strings.Contains(rec.Body.String(), "hunter2") || tr.Source == nil || tr.Source.Label != "invalid" || tr.Source.Repo != "invalid" || tr.Source.RepoLink != "" ||
		tr.Source.Commit != "abc1234" || tr.Source.Applied != "" {
		t.Errorf("invalid source = %+v", tr.Source)
	}
}

// A drifted object is a refusal in the plan, offering Force; the forced plan
// updates it, and applying it clears config.drift.
func TestTrackingAPIForceWithDrift(t *testing.T) {
	conds := condition.New(condition.Options{})
	f := &cfgTreeFetcher{t: t, sha: managedSHA, files: map[string]string{"users/bob.yml": "kind: user\nversion: 1\nlogins: [bob@repo]\n"}}
	h, store := configUI(t, f, conds)
	applyManaged(t, store, map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"})
	trackManaged(t, store)
	bob, _ := jam.ResolveRegistryRef(store, ident.User, "bob")
	if err := store.SetUserLogins(bob, []string{"bob@jam"}); err != nil {
		t.Fatal(err)
	}
	jam.CheckConfigDrift(store, conds, nil)
	if !conds.IsOpen(jam.ConfigDriftKey) {
		t.Fatal("drift not raised")
	}
	var plan adminui.Plan
	apiDo(t, h, "POST", "/app/admin/api/tracking/diff", map[string]bool{"force": false}, &plan)
	if !plan.HasDrift || plan.Forced || !strings.Contains(strings.Join(plan.Refusals, "\n"), "user bob drifted (edited in Jam since config @abc1234)") ||
		plan.Commit != "abc1234" || plan.Source != "https://github.com/acme/jam@main" {
		t.Fatalf("drifted plan = %+v", plan)
	}
	if len(plan.Ops) != 1 || !plan.Ops[0].Drift || plan.Ops[0].Path != "config/users/"+string(bob) || len(plan.Ops[0].Lines) == 0 {
		t.Errorf("drifted op = %+v", plan.Ops)
	}
	apiDo(t, h, "POST", "/app/admin/api/tracking/diff", map[string]bool{"force": true}, &plan)
	if !plan.Forced || len(plan.Refusals) != 0 || plan.Changes != 1 || plan.Ops[0].Action != jam.ConfigUpdate {
		t.Fatalf("forced plan = %+v", plan)
	}
	var res adminui.TrackingResult
	if rec := apiDo(t, h, "POST", "/app/admin/api/tracking/apply", map[string]any{"plan": plan.Digest, "force": true}, &res); rec.Code != 200 || !strings.Contains(res.Message, "1 updated") {
		t.Fatalf("forced apply = %d %+v", rec.Code, res)
	}
	if u, _ := store.GetUser(bob); len(u.Logins) != 1 || u.Logins[0] != "bob@repo" {
		t.Errorf("logins %v", u.Logins)
	}
	if conds.IsOpen(jam.ConfigDriftKey) {
		t.Error("the apply left config.drift open")
	}
}

// A plan with nothing to change counts the unchanged; a repo copy of an
// unmanaged object that differs is refused and diffed, flagged unmanaged.
func TestTrackingAPIPlanUnchangedAndUnmanaged(t *testing.T) {
	f := &cfgTreeFetcher{t: t, sha: managedSHA, files: map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"}}
	h, store := configUI(t, f, nil)
	applyManaged(t, store, map[string]string{"users/bob.yml": "kind: user\nversion: 1\n"})
	trackManaged(t, store)
	var plan adminui.Plan
	apiDo(t, h, "POST", "/app/admin/api/tracking/diff", map[string]bool{}, &plan)
	if plan.Changes != 0 || plan.Unchanged != 1 || len(plan.Ops) != 0 || plan.HasDrift {
		t.Errorf("unchanged plan = %+v", plan)
	}

	f2 := &cfgTreeFetcher{t: t, sha: managedSHA, files: map[string]string{"users/bob.yml": "kind: user\nversion: 1\nlogins:\n- repo@x\n"}}
	h2, store2 := configUI(t, f2, nil)
	if _, err := store2.CreateUser(jam.User{ID: ident.New(ident.User), Name: "bob", Status: jam.StatusLive, Logins: []string{"jam@x"}}); err != nil {
		t.Fatal(err)
	}
	trackManaged(t, store2)
	apiDo(t, h2, "POST", "/app/admin/api/tracking/diff", map[string]bool{}, &plan)
	if len(plan.Ops) != 1 || !plan.Ops[0].Unmanaged || plan.Ops[0].Path == "" || !strings.Contains(strings.Join(plan.Refusals, "\n"), "user bob exists and is not managed") {
		t.Fatalf("unmanaged plan = %+v", plan)
	}
	if ops := diffOps(plan.Ops[0].Lines); !strings.Contains(ops, "-   - jam@x") || !strings.Contains(ops, "+   - repo@x") {
		t.Errorf("unmanaged diff = %q", ops)
	}
}
