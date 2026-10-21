package jam

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

var allCreds = ConfigEnv{CredExists: func(string) bool { return true }}

var planNow = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

const ghDest = "kind: destination\nversion: 1\nroute: /gh/\nupstream: https://api.github.com\n"

// fullTreeFiles holds every kind once (two users).
var fullTreeFiles = map[string]string{
	"users/bob.yml":        "kind: user\nversion: 1\nlogins: [bob@example.com]\n",
	"users/alice.yml":      "kind: user\nversion: 1\n",
	"connections/acme.yml": "kind: connection\nversion: 1\nservice: discord\ncred: acme-bot\n",
	"destinations/gh.yml":  ghDest,
	"model-specs/fast.yml": "kind: model-spec\nversion: 1\ntype: claude\nharness-version: 2.1.3\nprincipal: {credential: anthropic}\nclaude: {provider: anthropic}\n",
	"kits/web-dev/kit.yml": "kind: studio\negress: [registry.npmjs.org]\n",
	"kits/app/kit.yml":     "kind: studio\nbase: {kit: ../web-dev}\n",
	"projects/app/project.yml": `kind: project
version: 1
chat-service: acme
members: [{user: bob, delivery: [{service: discord, address: "111"}]}, {user: alice}]
escalation: [{targets: [user:alice], timeout: 30m}]
escalation-by-category:
  infra: [{targets: [user:bob]}]
rooms:
  - key: ops
    label: Operations
    bindings: [{connection: acme, ref: "c-ops"}]
`,
	"projects/app/context.md": "Ship it.\n",
	"projects/app/roles/ic.yml": `kind: role
version: 1
kit: app
model-spec: fast
scope: {destinations: [gh], ttl: 1h}
allocation:
  standing: [{name: triage, prompt: Triage the queue.}]
context: {core: Be brief.}
`,
	"context.md": "Be kind.\n",
}

// unnormalizedTreeFiles is written the way a person might: an unnormalized
// egress domain, a 90m duration, a room label equal to its key, an explicit
// mode both, a delivery, and context cores without a trailing newline.
var unnormalizedTreeFiles = map[string]string{
	"users/bob.yml":        "kind: user\nversion: 1\n",
	"connections/acme.yml": "kind: connection\nversion: 1\nservice: discord\n",
	"destinations/gh.yml":  ghDest + "note: GitHub\n",
	"model-specs/fast.yml": "kind: model-spec\nversion: 1\ntype: claude\nharness-version: 2.1.3\nprincipal: {credential: anthropic}\nclaude: {provider: anthropic}\n",
	"kits/web-dev/kit.yml": "kind: studio\n",
	"kits/app/kit.yml":     "kind: studio\nbase: {kit: ../web-dev}\n",
	"projects/app/project.yml": `kind: project
version: 1
members: [{user: bob, delivery: [{service: discord, address: "111"}]}]
escalation: [{targets: [user:bob], timeout: 90m}]
rooms: [{key: ops, label: ops, bindings: [{connection: acme, ref: c1, mode: both}]}]
`,
	"projects/app/context.md": "Ship it.",
	"projects/app/roles/ic.yml": `kind: role
version: 1
kit: app
scope: {ttl: 90m, egress: [GitHub.com]}
allocation: {idle-after: 90m}
`,
	"context.md": "Be kind.",
}

func treeOf(t *testing.T, files map[string]string) *ConfigTree {
	t.Helper()
	tr, err := LoadConfigTree(writeTree(t, files))
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func planOn(t *testing.T, st *MemStore, tr *ConfigTree, force bool) ConfigPlan {
	t.Helper()
	return PlanConfig(ConfigPlanInput{
		Tree: tr, Snapshot: st.ExportConfig(), Instances: st.ListInstances(), Marks: st.ListConfigMarks(),
		Env: allCreds, Source: gitsource.Source{Repo: ConfigDirRepo}, Commit: ConfigDirCommit, Force: force, Now: planNow,
	})
}

func applyPlan(t *testing.T, st *MemStore, p ConfigPlan) {
	t.Helper()
	if len(p.Refusals) > 0 {
		t.Fatalf("refusals: %v", p.Refusals)
	}
	if err := st.ApplyConfig(p.write); err != nil {
		t.Fatal(err)
	}
}

func opOf(p ConfigPlan, kind, name string) ConfigOp {
	for _, o := range p.Ops {
		if o.Kind == kind && o.Name == name {
			return o
		}
	}
	return ConfigOp{}
}

func TestPlanCreatesInOrder(t *testing.T) {
	tr := treeOf(t, fullTreeFiles) // every kind once; role app/ic binds kit app (../web-dev) and model-spec fast
	p := planOn(t, NewMemStore(), tr, false)
	if len(p.Refusals) > 0 {
		t.Fatal(p.Refusals)
	}
	var order []string
	for _, o := range p.Ops {
		order = append(order, o.Kind)
	}
	want := []string{ConfigKindUser, ConfigKindUser, ConfigKindConnection, ConfigKindDestination, ConfigKindModelSpec,
		ConfigKindKit, ConfigKindKit, ConfigKindProject, ConfigKindRole, ConfigKindJamContext}
	if !slices.Equal(order, want) {
		t.Fatalf("order %v", order)
	}
	if p.write.Kits[1].Versions[1] == "" || !strings.Contains(p.write.Kits[1].Versions[1], `"web-dev@v1"`) {
		t.Fatalf("app not pinned to its parent: %+v", p.write.Kits)
	}
	if m := p.write.Kits[0].Meta[1]; m.Source == nil || m.Source.Repo != ConfigDirRepo || m.Source.Dir != "kits/web-dev" {
		t.Fatalf("provenance %+v", m)
	}
	if len(p.write.Marks) != len(p.Ops) || p.write.Marks[0].Commit != ConfigDirCommit {
		t.Fatalf("marks %+v", p.write.Marks)
	}
	st := NewMemStore()
	applyPlan(t, st, planOn(t, st, tr, false))
	if again := planOn(t, st, tr, false); again.Counts()[ConfigUnchanged] != len(again.Ops) || len(again.Refusals) > 0 {
		t.Fatalf("full tree re-plan: %+v", again)
	}
}

func TestPlanTable(t *testing.T) {
	st := NewMemStore()
	tr := treeOf(t, map[string]string{"destinations/gh.yml": ghDest})
	applyPlan(t, st, planOn(t, st, tr, false))

	if o := opOf(planOn(t, st, tr, false), ConfigKindDestination, "gh"); o.Action != ConfigUnchanged {
		t.Fatalf("re-plan %+v", o)
	}
	changed := treeOf(t, map[string]string{"destinations/gh.yml": ghDest + "note: hi\n"})
	if o := opOf(planOn(t, st, changed, false), ConfigKindDestination, "gh"); o.Action != ConfigUpdate || o.Drift {
		t.Fatalf("update %+v", o)
	}
	d, _ := findDestination(st, "gh")
	d.Note = "edited in Jam"
	_ = st.AddDestination(d)
	p := planOn(t, st, changed, false)
	if o := opOf(p, ConfigKindDestination, "gh"); !o.Drift || !slices.Contains(p.Refusals,
		"destination gh drifted (edited in Jam since config @dir); re-run with --force or config write it first") {
		t.Fatalf("drift %+v %v", o, p.Refusals)
	}
	if p := planOn(t, st, changed, true); len(p.Refusals) > 0 || !opOf(p, ConfigKindDestination, "gh").Drift {
		t.Fatalf("forced %+v", p)
	}
	_ = st.AddDestination(Destination{Name: "other", Route: "/o/", Upstream: "https://o.example"})
	same := treeOf(t, map[string]string{"destinations/gh.yml": ghDest + "note: edited in Jam\n",
		"destinations/other.yml": "kind: destination\nversion: 1\nroute: /o/\nupstream: https://o.example\n"})
	if o := opOf(planOn(t, st, same, false), ConfigKindDestination, "other"); o.Action != ConfigAdopt {
		t.Fatalf("adopt %+v", o)
	}
	differs := treeOf(t, map[string]string{"destinations/other.yml": "kind: destination\nversion: 1\nroute: /x/\nupstream: https://o.example\n"})
	if p := planOn(t, st, differs, false); !slices.Contains(p.Refusals,
		"destination other exists and is not managed; config write --all it into the repo, or rename") {
		t.Fatalf("refusal %v", p.Refusals)
	}
	if p := planOn(t, st, treeOf(t, nil), false); !slices.Contains(p.Unmanaged, ConfigKey{ConfigKindDestination, "other"}) {
		t.Fatalf("unmanaged %v", p.Unmanaged)
	}
}

func TestPlanReferencesResolveAgainstPlannedState(t *testing.T) {
	tr := treeOf(t, map[string]string{
		"projects/app/project.yml":  "kind: project\nversion: 1\nmembers: [{user: ghost}]\nescalation: [{targets: [user:bob]}]\n",
		"projects/app/roles/ic.yml": "kind: role\nversion: 1\nkit: web-dev\nmodel-spec: nope\n",
		"users/bob.yml":             "kind: user\nversion: 1\n",
	})
	p := planOn(t, NewMemStore(), tr, false)
	for _, want := range []string{
		"projects/app/project.yml: member ghost is not a user in the repo or Jam",
		"projects/app/roles/ic.yml: kit web-dev not found (not in the repo or Jam)",
	} {
		if !slices.Contains(p.Refusals, want) {
			t.Errorf("missing %q in %v", want, p.Refusals)
		}
	}
	if slices.ContainsFunc(p.Refusals, func(s string) bool { return strings.Contains(s, "user:bob") }) {
		t.Fatal("bob is in the repo: escalation must resolve")
	}
}

func TestPlanSeededDefaultModelSpec(t *testing.T) {
	st := NewMemStore()
	// EnsureDefaultModelSpec seeds from the anthropic destination's credential.
	if err := st.AddDestination(Destination{Name: "anthropic", Route: "/anthropic/", Upstream: "https://api.anthropic.com", CredName: "anthropic"}); err != nil {
		t.Fatal(err)
	}
	if created, err := EnsureDefaultModelSpec(st, false); err != nil || !created {
		t.Fatal(created, err)
	}
	tr := treeOf(t, map[string]string{"model-specs/" + DefaultModelSpec + ".yml": "kind: model-spec\nversion: 1\ntype: claude\nharness-version: 2.1.99\nprincipal: {credential: anthropic}\nclaude: {provider: anthropic}\n"})
	p := planOn(t, st, tr, false)
	if len(p.Refusals) > 0 || opOf(p, ConfigKindModelSpec, DefaultModelSpec).Action != ConfigUpdate {
		t.Fatalf("%+v", p)
	}
	applyPlan(t, st, p)
	if ms, _ := st.GetModelSpec(DefaultModelSpec); ms.Version != "2.1.99" {
		t.Fatalf("adopted spec not written: %+v", ms)
	}
}

func TestPlanReapplyIsUnchanged(t *testing.T) {
	st := NewMemStore()
	tr := treeOf(t, unnormalizedTreeFiles) // egress GitHub.com, ttl 90m, room label == key, mode both, a delivery, core without "\n"
	if len(tr.Refusals) > 0 {
		t.Fatal(tr.Refusals)
	}
	first := planOn(t, st, tr, false)
	kinds := map[string]bool{}
	for _, o := range first.Ops {
		kinds[o.Kind] = true
	}
	if len(kinds) != 8 {
		t.Fatalf("fixture must cover every kind: %v", kinds)
	}
	applyPlan(t, st, first)
	again := planOn(t, st, tr, false)
	if len(again.Ops) != len(first.Ops) || len(again.Refusals) > 0 {
		t.Fatalf("re-plan %+v", again)
	}
	for _, o := range again.Ops {
		if o.Action != ConfigUnchanged || o.Drift {
			t.Errorf("%s %s: %s drift=%v\nbefore:\n%s\nafter:\n%s", o.Kind, o.Name, o.Action, o.Drift, o.Before, o.After)
		}
	}
	exp, _ := ExportConfigTree(st.ExportConfig(), st.ListConfigMarks(), nil, false)
	if p := planOn(t, st, treeOf(t, exp.Files), false); len(p.Refusals) > 0 || p.Counts()[ConfigUnchanged] != len(p.Ops) || len(p.Ops) != len(first.Ops) {
		t.Fatalf("write-then-plan is not a no-op: %+v", p)
	}
}

func TestPlanPinsRefused(t *testing.T) {
	p := planOn(t, NewMemStore(), treeOf(t, map[string]string{"projects/app/roles/ic.yml": "kind: role\nversion: 1\nkit: web@v2\n"}), false)
	if !slices.Contains(p.Refusals, "projects/app/roles/ic.yml: kit web@v2 pins a version; versions are per-Jam — name the kit") {
		t.Fatal(p.Refusals)
	}
}

func TestPlanHostKeyInRepoJamYML(t *testing.T) {
	p := planOn(t, NewMemStore(), treeOf(t, map[string]string{"jam.yml": "listen: \":443\"\n"}), false)
	if !slices.Contains(p.Refusals, "jam.yml: listen is a host key; put it in jam.local.yml") {
		t.Fatal(p.Refusals)
	}
}

func TestPlanServeConfigDiff(t *testing.T) {
	in := ConfigPlanInput{Tree: treeOf(t, map[string]string{"jam.yml": "display-name: A\n"}), Env: ConfigEnv{
		CredExists: allCreds.CredExists, Serve: map[string]any{"display-name": "B"}}, Commit: ConfigDirCommit, Now: time.Now()}
	if sc := PlanConfig(in).ServeConfig; sc.State != "differs" || !slices.Equal(sc.Keys, []string{"display-name"}) {
		t.Fatalf("%+v", sc)
	}
	in.Env.Serve = map[string]any{"display-name": "A"}
	if sc := PlanConfig(in).ServeConfig; sc.State != "same" {
		t.Fatalf("%+v", sc)
	}
	in.Env.Serve = nil
	if sc := PlanConfig(in).ServeConfig; sc.State != "unknown" {
		t.Fatalf("%+v", sc)
	}
	in.Env.Serve, in.Tree = map[string]any{}, treeOf(t, nil)
	if sc := PlanConfig(in).ServeConfig; sc.State != "absent" {
		t.Fatalf("%+v", sc)
	}
}

func TestPlanRefusalWritesNothing(t *testing.T) {
	tr := treeOf(t, map[string]string{"users/bob.yml": "kind: user\nversion: 1\n", "kits/app/kit.yml": "kind: studio\nbase: {kit: ../../x}\n"})
	p := planOn(t, NewMemStore(), tr, false)
	if len(p.Refusals) == 0 {
		t.Fatal("expected a refusal")
	}
	// Task 8's ApplyConfigTree refuses a plan with refusals before ApplyConfig; this pins the input it must refuse.
}

func TestPlanDigestStable(t *testing.T) {
	tr := treeOf(t, fullTreeFiles)
	a, b := planOn(t, NewMemStore(), tr, false), planOn(t, NewMemStore(), tr, false)
	if a.Digest == "" || a.Digest != b.Digest {
		t.Fatalf("%q vs %q", a.Digest, b.Digest)
	}
}

func TestPlanConnectionServiceCannotChange(t *testing.T) {
	st := NewMemStore()
	applyPlan(t, st, planOn(t, st, treeOf(t, map[string]string{"connections/acme.yml": "kind: connection\nversion: 1\nservice: linear\n"}), false))
	p := planOn(t, st, treeOf(t, map[string]string{"connections/acme.yml": "kind: connection\nversion: 1\nservice: discord\n"}), false)
	if !slices.Contains(p.Refusals, "connections/acme.yml: service cannot change (linear → discord); remove the file, apply, then add it back") {
		t.Fatal(p.Refusals)
	}
}

func TestPlanProjectUpdateKeepsDeliveryBindingAndArchivesRooms(t *testing.T) {
	st := NewMemStore()
	base := map[string]string{
		"users/bob.yml":        "kind: user\nversion: 1\n",
		"connections/acme.yml": "kind: connection\nversion: 1\nservice: discord\n",
		"projects/app/project.yml": `kind: project
version: 1
members: [{user: bob, delivery: [{service: discord, address: "111"}]}]
rooms: [{key: ops, bindings: [{connection: acme, ref: c1}]}, {key: old}]
`,
	}
	applyPlan(t, st, planOn(t, st, treeOf(t, base), false))
	proj, _ := st.GetProject("app")
	bob, _ := st.liveUserNamed("bob")
	ms, _ := st.GetMembership(proj.ID, bob.ID)
	ms.Delivery[0].UserID = "999" // only a migration binds one; PutMembership clears it
	st.mu.Lock()
	st.applyPutMembership(ms)
	st.mu.Unlock()
	base["projects/app/project.yml"] = `kind: project
version: 1
members: [{user: bob, delivery: [{service: discord, address: "111"}]}]
rooms: [{key: ops, label: Ops, bindings: [{connection: acme, ref: c1}]}]
`
	p := planOn(t, st, treeOf(t, base), false)
	if o := opOf(p, ConfigKindProject, "app"); o.Action != ConfigUpdate {
		t.Fatalf("%+v %v", o, p.Refusals)
	}
	if len(p.write.Memberships) != 0 {
		t.Fatalf("an unchanged delivery is not rewritten: %+v", p.write.Memberships)
	}
	applyPlan(t, st, p)
	if ms, _ := st.GetMembership(proj.ID, bob.ID); ms.Delivery[0].UserID != "999" {
		t.Fatalf("delivery binding lost: %+v", ms)
	}
	rooms := st.ListChannels(proj.ID, SourceRoom)
	if len(rooms) != 1 || rooms[0].Key != "ops" || rooms[0].Label != "Ops" {
		t.Fatalf("rooms %+v", rooms)
	}
}

func TestCheckStandingDecl(t *testing.T) {
	have := []StandingSession{{Name: "a", Prompt: "p"}}
	for _, c := range []struct {
		s    StandingSession
		want string
	}{
		{StandingSession{Prompt: "p"}, "name and prompt are required"},
		{StandingSession{Name: "x/y", Prompt: "p"}, `standing session name "x/y" must not contain /`},
		{StandingSession{Name: "a", Prompt: "p"}, `standing session "a" is declared twice`},
	} {
		if err := checkStandingDecl(have, c.s); err == nil || err.Error() != c.want {
			t.Errorf("%+v: %v, want %q", c.s, err, c.want)
		}
	}
	if err := checkStandingDecl(have, StandingSession{Name: "b", Prompt: "p"}); err != nil {
		t.Fatal(err)
	}
}

// roomTree is project app (with connection acme) holding the given rooms YAML.
func roomTree(rooms string) map[string]string {
	return map[string]string{
		"connections/acme.yml":     "kind: connection\nversion: 1\nservice: discord\n",
		"projects/app/project.yml": "kind: project\nversion: 1\nrooms:\n" + rooms,
	}
}

func TestPlanRoomRenameKeepsItsBinding(t *testing.T) {
	st := NewMemStore()
	applyPlan(t, st, planOn(t, st, treeOf(t, roomTree("  - {key: ops, bindings: [{connection: acme, ref: c1}]}\n")), false))
	renamed := treeOf(t, roomTree("  - {key: operations, bindings: [{connection: acme, ref: c1}]}\n"))
	p := planOn(t, st, renamed, false)
	applyPlan(t, st, p)
	proj, _ := st.GetProject("app")
	if rooms := st.ListChannels(proj.ID, SourceRoom); len(rooms) != 1 || rooms[0].Key != "operations" || len(rooms[0].Bindings) != 1 {
		t.Fatalf("rooms %+v", rooms)
	}
	if again := planOn(t, st, renamed, false); again.Counts()[ConfigUnchanged] != len(again.Ops) {
		t.Fatalf("re-plan %+v", again.Ops)
	}
}

func TestPlanBindingMovesBetweenRooms(t *testing.T) {
	st := NewMemStore()
	applyPlan(t, st, planOn(t, st, treeOf(t, roomTree("  - {key: b}\n  - {key: z, bindings: [{connection: acme, ref: c1}]}\n")), false))
	moved := treeOf(t, roomTree("  - {key: b, bindings: [{connection: acme, ref: c1}]}\n  - {key: z}\n"))
	p := planOn(t, st, moved, false)
	applyPlan(t, st, p)
	proj, _ := st.GetProject("app")
	rooms := st.ListChannels(proj.ID, SourceRoom)
	if len(rooms) != 2 || len(rooms[0].Bindings) != 1 || len(rooms[1].Bindings) != 0 {
		t.Fatalf("rooms %+v", rooms)
	}
	// Bindings release before they are taken: a store checking the ingress
	// uniqueness per write (Postgres) never sees c1 on two live rooms.
	held := map[ident.ID]bool{}
	for _, c := range p.write.Channels {
		has := slices.ContainsFunc(c.Bindings, func(b Binding) bool { return b.Ref == "c1" }) && c.Status == StatusLive
		delete(held, c.ID)
		if has {
			held[c.ID] = true
		}
		if len(held) > 1 {
			t.Fatalf("c1 held twice mid-write: %+v", p.write.Channels)
		}
	}
}

func TestPlanRoomBindingOrderIsNotAChange(t *testing.T) {
	st := NewMemStore()
	files := roomTree("  - {key: ops, bindings: [{connection: acme, ref: c2}, {connection: acme, ref: c1}]}\n")
	applyPlan(t, st, planOn(t, st, treeOf(t, files), false))
	files["projects/app/project.yml"] = "kind: project\nversion: 1\nrooms:\n  - {key: ops, bindings: [{connection: acme, ref: c1}, {connection: acme, ref: c2}]}\n"
	p := planOn(t, st, treeOf(t, files), false)
	if o := opOf(p, ConfigKindProject, "app"); o.Action != ConfigUnchanged || len(p.write.Channels) != 0 {
		t.Fatalf("%+v %+v", o, p.write.Channels)
	}
}
