package jam

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

// appliedFull is a MemStore with fullTreeFiles applied (user alice is named
// in project app's escalation; bob in its infra category).
func appliedFull(t *testing.T) *MemStore {
	t.Helper()
	st := NewMemStore()
	applyPlan(t, st, planOn(t, st, treeOf(t, fullTreeFiles), false))
	return st
}

// without copies files minus the given paths.
func without(files map[string]string, paths ...string) map[string]string {
	out := maps.Clone(files)
	for _, p := range paths {
		delete(out, p)
	}
	return out
}

// liveSessionOf puts a live session s1 of project/role.
func liveSessionOf(project, role string) func(t *testing.T, st *MemStore) {
	return func(t *testing.T, st *MemStore) {
		t.Helper()
		p, ok := st.GetProject(project)
		if !ok {
			t.Fatalf("no project %s", project)
		}
		if err := st.PutInstance(Instance{ActorID: "s1", Project: string(p.ID), Role: role, Phase: PhaseLive}); err != nil {
			t.Fatal(err)
		}
	}
}

// personalSessionOf puts a live personal session s2 owned by user.
func personalSessionOf(user string) func(t *testing.T, st *MemStore) {
	return func(t *testing.T, st *MemStore) {
		t.Helper()
		u := userNamed(t, st, user)
		p, _ := st.GetProject("app")
		if err := st.PutInstance(Instance{ActorID: "s2", Project: string(p.ID), Role: "ic", Phase: PhaseLive,
			SessionKind: SessionKindPersonal, OwnerID: u.ID, Owner: u.Name}); err != nil {
			t.Fatal(err)
		}
	}
}

func userNamed(t *testing.T, st *MemStore, name string) User {
	t.Helper()
	u, ok := st.liveUserNamed(name)
	if !ok {
		t.Fatalf("no user %s", name)
	}
	return u
}

func TestPlanDeletesReverseOrder(t *testing.T) {
	st := appliedFull(t)
	p := planOn(t, st, treeOf(t, nil), false)
	if len(p.Refusals) > 0 {
		t.Fatal(p.Refusals)
	}
	var kinds []string
	for _, o := range p.Ops {
		if o.Action == ConfigDelete {
			kinds = append(kinds, o.Kind)
		}
	}
	want := []string{ConfigKindRole, ConfigKindProject, ConfigKindKit, ConfigKindKit, ConfigKindModelSpec,
		ConfigKindDestination, ConfigKindConnection, ConfigKindUser, ConfigKindUser, ConfigKindJamContext}
	if !slices.Equal(kinds, want) {
		t.Fatalf("delete order %v", kinds)
	}
	if kits := p.write.RemoveKits; !slices.Equal(kits, []string{"app", "web-dev"}) {
		t.Fatalf("children first: %v", kits)
	}
	applyPlan(t, st, p)
	if len(st.ListConfigMarks()) != 0 || len(st.ListKits()) != 0 {
		t.Fatal("not deleted")
	}
	if _, ok := st.GetProject("app"); ok {
		t.Fatal("project app still live")
	}
	if _, ok := st.liveUserNamed("bob"); ok {
		t.Fatal("user bob still live")
	}
	if again := planOn(t, st, treeOf(t, nil), false); len(again.Ops) != 0 || len(again.Refusals) != 0 {
		t.Fatalf("re-plan after deletes: %+v", again)
	}
}

func TestPlanDeleteGuards(t *testing.T) {
	for name, tc := range map[string]struct {
		setup  func(t *testing.T, st *MemStore)
		remove []string          // repo paths dropped from fullTreeFiles
		edit   map[string]string // repo files rewritten
		want   string
	}{
		"kit bound by a kept role": {remove: []string{"kits/app/kit.yml"}, want: "cannot delete kit app: role app/ic binds it"},
		"kit a kept kit builds on": {remove: []string{"kits/web-dev/kit.yml"},
			edit: map[string]string{"kits/app/kit.yml": "kind: studio\nbase: {kit: web-dev}\n"},
			want: "cannot delete kit web-dev: kit app builds on it"},
		"model-spec a role binds": {remove: []string{"model-specs/fast.yml"}, want: "cannot delete model-spec fast: "},
		"role with a live session": {setup: liveSessionOf("app", "ic"), remove: []string{"projects/app/roles/ic.yml"},
			want: "cannot delete role app/ic: live session s1 uses it"},
		"user in escalation": {remove: []string{"users/alice.yml"}, want: "cannot delete user alice: project app's escalation names them"},
		"user owning a session": {setup: personalSessionOf("bob"), remove: []string{"users/bob.yml"},
			want: "cannot delete user bob: they own the live personal session s2"},
		"project with a live session": {setup: liveSessionOf("app", "ic"), remove: []string{"projects/app/project.yml", "projects/app/context.md", "projects/app/roles/ic.yml"},
			want: "cannot delete project app: "},
		"connection a room binds": {remove: []string{"connections/acme.yml"}, want: "cannot delete connection acme: "},
	} {
		t.Run(name, func(t *testing.T) {
			st := appliedFull(t)
			if tc.setup != nil {
				tc.setup(t, st)
			}
			files := without(fullTreeFiles, tc.remove...)
			maps.Copy(files, tc.edit)
			p := planOn(t, st, treeOf(t, files), false)
			if !slices.ContainsFunc(p.Refusals, func(s string) bool { return strings.HasPrefix(s, tc.want) }) {
				t.Fatalf("refusals %v, want prefix %q", p.Refusals, tc.want)
			}
		})
	}
}

func TestPlanUserDeleteRefusedByUnmanagedMembership(t *testing.T) {
	st := appliedFull(t)
	if err := st.CreateProject("side"); err != nil {
		t.Fatal(err)
	}
	side, _ := st.GetProject("side")
	bob := userNamed(t, st, "bob")
	if err := st.AddMember(side.ID, bob.ID); err != nil {
		t.Fatal(err)
	}
	// Drop bob from the repo and from everything managed that names him
	// (app's members and its infra escalation), so the only guard left is
	// his membership of the unmanaged project side.
	files := without(fullTreeFiles, "users/bob.yml")
	proj := files["projects/app/project.yml"]
	proj = strings.Replace(proj, `{user: bob, delivery: [{service: discord, address: "111"}]}, `, "", 1)
	proj = strings.Replace(proj, "targets: [user:bob]", "targets: [user:alice]", 1)
	if strings.Contains(proj, "bob") {
		t.Fatalf("fixture still names bob:\n%s", proj)
	}
	files["projects/app/project.yml"] = proj
	p := planOn(t, st, treeOf(t, files), false)
	if !slices.Contains(p.Refusals, "cannot delete user bob: project side lists them as a member") {
		t.Fatal(p.Refusals)
	}
	if o := opOf(p, ConfigKindUser, "bob"); o.Action != "" {
		t.Fatalf("refused delete still planned: %+v", o)
	}
	if _, ok := st.GetMembership(side.ID, bob.ID); !ok {
		t.Fatal("planning touched the store")
	}
	if !slices.Contains(p.Unmanaged, ConfigKey{ConfigKindProject, "side"}) {
		t.Fatalf("side is unmanaged: %v", p.Unmanaged)
	}
	// Once bob leaves side, the delete goes through.
	if err := st.RemoveMember(side.ID, bob.ID); err != nil {
		t.Fatal(err)
	}
	p = planOn(t, st, treeOf(t, files), false)
	if len(p.Refusals) > 0 || opOf(p, ConfigKindUser, "bob").Action != ConfigDelete {
		t.Fatalf("%v %+v", p.Refusals, opOf(p, ConfigKindUser, "bob"))
	}
}

func TestPlanDeleteDrift(t *testing.T) {
	st := appliedFull(t)
	d, _ := findDestination(st, "gh")
	d.Note = "edited"
	_ = st.AddDestination(d)
	tr := treeOf(t, without(fullTreeFiles, "destinations/gh.yml"))
	p := planOn(t, st, tr, false)
	if !slices.Contains(p.Refusals, "destination gh drifted (edited in Jam since config @dir); re-run with --force or config write it first") {
		t.Fatal(p.Refusals)
	}
	forced := planOn(t, st, tr, true)
	if o := opOf(forced, ConfigKindDestination, "gh"); len(forced.Refusals) > 0 || o.Action != ConfigDelete || !o.Drift {
		t.Fatalf("forced %+v %v", o, forced.Refusals)
	}
}

func TestPlanForgetsMarkOfVanishedObject(t *testing.T) {
	st := appliedFull(t)
	_ = st.RemoveDestination("gh")
	p := planOn(t, st, treeOf(t, without(fullTreeFiles, "destinations/gh.yml")), false)
	if opOf(p, ConfigKindDestination, "gh").Action != "" || !slices.Contains(p.write.RemoveMarks, ConfigKey{ConfigKindDestination, "gh"}) {
		t.Fatalf("%+v", p)
	}
	if !slices.Equal(p.Forget, []ConfigKey{{ConfigKindDestination, "gh"}}) || p.Counts()[ConfigForget] != 1 || ConfigChanges(p) != 1 {
		t.Fatalf("forget %v counts %v", p.Forget, p.Counts())
	}
	q := p
	q.Forget = nil
	if planDigest(q) == p.Digest {
		t.Fatal("the digest ignores Forget")
	}
}

func TestPlanUnmanagedUntouched(t *testing.T) {
	st := appliedFull(t)
	_, _ = st.CreateUser(User{Name: "carol"})
	p := planOn(t, st, treeOf(t, fullTreeFiles), false)
	if !slices.Contains(p.Unmanaged, ConfigKey{ConfigKindUser, "carol"}) || opOf(p, ConfigKindUser, "carol").Action != "" {
		t.Fatalf("%+v", p)
	}
}
