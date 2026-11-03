package jam

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"
)

// notesMD is operator notes as a person writes them: multi-line markdown.
const notesMD = "# Why\n\nAsk **ops** before changing.\n\n- one\n- two\n"

// notesTreeFiles is fullTreeFiles with operator-notes on every kind.
func notesTreeFiles(notes string) map[string]string {
	add := func(s string) string {
		return s + "operator-notes: |\n  " + strings.ReplaceAll(strings.TrimSuffix(notes, "\n"), "\n", "\n  ") + "\n"
	}
	out := maps.Clone(fullTreeFiles)
	for p, s := range out {
		if strings.HasSuffix(p, ".yml") {
			out[p] = add(s)
		}
	}
	out["jam.yml"] = add("display-name: A\n")
	return out
}

// jamNotesOf is every object's operator notes in st, by "kind name".
func jamNotesOf(t *testing.T, st *MemStore) map[string]string {
	t.Helper()
	out := map[string]string{"jam": st.GetJamNotes()}
	for _, d := range st.ListDestinations() {
		out["destination "+d.Name] = d.OperatorNotes
	}
	ms, _ := st.GetModelSpec("fast")
	out["model-spec fast"] = ms.OperatorNotes
	for _, u := range st.ListUsers() {
		out["user "+u.Name] = u.OperatorNotes
	}
	for _, c := range st.ListConnections() {
		out["connection "+c.Name] = c.OperatorNotes
	}
	p, _ := st.GetProject("app")
	out["project app"] = p.OperatorNotes
	r, _ := st.GetRole("app", "ic")
	out["role app/ic"] = r.OperatorNotes
	for _, k := range []string{"web-dev", "app"} {
		v, ok := LoadKitVersion(st, k, 0)
		if !ok {
			t.Fatalf("kit %s missing", k)
		}
		out["kit "+k] = v.Kit.OperatorNotes
	}
	return out
}

func TestOperatorNotesApplyEveryKind(t *testing.T) {
	st := NewMemStore()
	tr := treeOf(t, notesTreeFiles(notesMD))
	if len(tr.Refusals) > 0 {
		t.Fatal(tr.Refusals)
	}
	applyPlan(t, st, planOn(t, st, tr))
	got := jamNotesOf(t, st)
	if len(got) != 10 {
		t.Fatalf("want every object: %v", got)
	}
	for k, n := range got {
		if n != notesMD {
			t.Errorf("%s: notes %q", k, n)
		}
	}

	again := planOn(t, st, tr)
	for _, o := range again.Ops {
		if o.Action != ConfigUnchanged {
			t.Errorf("re-plan %s %s: %s", o.Kind, o.Name, o.Action)
		}
	}

	// Written back (as config write does), the notes come out as written and
	// the tree plans as a no-op.
	exp, err := ExportConfigTree(st.ExportConfig(), st.ListConfigMarks(), map[string]any{"display-name": "A"}, false)
	if err != nil {
		t.Fatal(err)
	}
	for p, s := range exp.Files {
		if strings.HasSuffix(p, "context.md") {
			continue
		}
		if !strings.Contains(s, "operator-notes: |\n  # Why\n") {
			t.Errorf("%s lost its notes:\n%s", p, s)
		}
	}
	if p := planOn(t, st, exportTree(t, exp)); len(p.Refusals) > 0 || p.Counts()[ConfigUnchanged] != len(p.Ops) {
		t.Fatalf("write-then-plan is not a no-op: %+v", p)
	}
}

func TestOperatorNotesEditIsAnUpdate(t *testing.T) {
	st := NewMemStore()
	applyPlan(t, st, planOn(t, st, treeOf(t, notesTreeFiles(notesMD))))
	p := planOn(t, st, treeOf(t, notesTreeFiles("changed\n")))
	for _, o := range p.Ops {
		if o.Kind != ConfigKindJamContext && o.Action != ConfigUpdate { // context.md has no notes
			t.Errorf("%s %s: %s, want update", o.Kind, o.Name, o.Action)
		}
	}
	applyPlan(t, st, p)
	for k, n := range jamNotesOf(t, st) {
		if n != "changed\n" {
			t.Errorf("%s: notes %q", k, n)
		}
	}

	// Notes dropped from the repo are cleared; the Jam's are a delete of its
	// notes object, not of jam.yml.
	p = planOn(t, st, treeOf(t, fullTreeFiles))
	if op := opOf(p, ConfigKindJamNotes, "jam"); op.Action != ConfigDelete {
		t.Fatalf("jam notes: %+v", op)
	}
	applyPlan(t, st, p)
	for k, n := range jamNotesOf(t, st) {
		if n != "" {
			t.Errorf("%s: notes %q not cleared", k, n)
		}
	}
	exp, err := ExportConfigTree(st.ExportConfig(), st.ListConfigMarks(), map[string]any{"display-name": "A"}, false)
	if err != nil || slices.Contains(exp.Remove, "jam.yml") || exp.Files["jam.yml"] != "display-name: A\n" {
		t.Fatalf("jam.yml: %v %v %q", err, exp.Remove, exp.Files["jam.yml"])
	}
}

func TestOperatorNotesAreNotAServeSetting(t *testing.T) {
	in := ConfigPlanInput{Tree: treeOf(t, map[string]string{"jam.yml": "display-name: A\noperator-notes: new\n"}), Env: ConfigEnv{
		CredExists: allCreds.CredExists, Serve: map[string]any{"display-name": "A", "operator-notes": "old"}}, Commit: ConfigDirCommit, Now: time.Now()}
	if sc := PlanConfig(in).ServeConfig; sc.State != "same" {
		t.Fatalf("%+v", sc)
	}
	tr := treeOf(t, map[string]string{"jam.yml": "operator-notes: [a]\n"})
	if !slices.Contains(tr.Refusals, "jam.yml: operator-notes must be a string") {
		t.Fatal(tr.Refusals)
	}
}

// A model spec's notes never reach the cove it is delivered to.
func TestModelSpecForStripsOperatorNotes(t *testing.T) {
	st := connectorStore(t, []Destination{legacyAnthropic}, map[string]Scope{"w": {Destinations: []string{"anthropic"}}})
	m := validSpec()
	m.Name, m.OperatorNotes = DefaultModelSpec, notesMD
	must(t, st.PutModelSpec(m))
	got, err := ModelSpecFor(st, actorWith("w"))
	if err != nil || got == nil || got.OperatorNotes != "" {
		t.Fatalf("%+v, %v", got, err)
	}
	if stored, _ := st.GetModelSpec(DefaultModelSpec); stored.OperatorNotes != notesMD {
		t.Fatal("the store lost the notes")
	}
}

// The console's write sets an object's notes and nothing else: written back,
// only the notes differ from the repo.
func TestSetOperatorNotes(t *testing.T) {
	st := NewMemStore()
	applyPlan(t, st, planOn(t, st, treeOf(t, fullTreeFiles)))
	for _, k := range []ConfigKey{{ConfigKindUser, "bob"}, {ConfigKindConnection, "acme"}, {ConfigKindDestination, "gh"},
		{ConfigKindModelSpec, "fast"}, {ConfigKindProject, "app"}, {ConfigKindRole, "app/ic"}} {
		if err := SetOperatorNotes(st, k, notesMD); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
	}
	p := planOn(t, st, treeOf(t, fullTreeFiles))
	updates := 0
	for _, o := range p.Ops {
		if o.Action == ConfigUpdate {
			updates++
			if !strings.Contains(o.Before, "operator-notes: |") || strings.Contains(o.After, "operator-notes") {
				t.Errorf("%s %s: before\n%s\nafter\n%s", o.Kind, o.Name, o.Before, o.After)
			}
		}
	}
	if updates != 6 {
		t.Fatalf("%d updates, want 6: %+v", updates, p.Ops)
	}
	for _, k := range []ConfigKey{{ConfigKindKit, "app"}, {ConfigKindJamNotes, "jam"}, {ConfigKindUser, "nobody"}, {ConfigKindRole, "app/nope"}} {
		if err := SetOperatorNotes(st, k, "x"); err == nil {
			t.Errorf("%s: want a refusal", k)
		}
	}
}
