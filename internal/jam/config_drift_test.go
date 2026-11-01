package jam

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/condition"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// editGH changes destination gh in Jam: it drifts from its mark.
func editGH(t *testing.T, st *MemStore, note string) {
	t.Helper()
	d, ok := findDestination(st, "gh")
	if !ok {
		t.Fatal("no destination gh")
	}
	d.Note = note
	if err := st.AddDestination(d); err != nil {
		t.Fatal(err)
	}
}

func TestConfigMarkStates(t *testing.T) {
	if s, err := ConfigMarkStates(NewMemStore()); s != nil || err != nil {
		t.Fatalf("no marks: %v %v", s, err)
	}
	st := appliedFull(t)
	states, err := ConfigMarkStates(st)
	if err != nil || len(states) != len(st.ListConfigMarks()) {
		t.Fatalf("%d states for %d marks: %v", len(states), len(st.ListConfigMarks()), err)
	}
	for _, s := range states {
		if s.State != ConfigInSync {
			t.Fatalf("%s right after apply: %s", s.Key(), s.State)
		}
	}
	editGH(t, st, "edited in Jam")
	if err := st.SetJamContext(sessionctx.Layer{}); err != nil { // the Jam context is gone from the live Jam
		t.Fatal(err)
	}
	states, err = ConfigMarkStates(st)
	if err != nil {
		t.Fatal(err)
	}
	want := map[ConfigKey]string{{ConfigKindDestination, "gh"}: ConfigDrifted, {ConfigKindJamContext, "jam"}: ConfigMissing}
	for _, s := range states {
		w, ok := want[s.Key()]
		if !ok {
			w = ConfigInSync
		}
		if s.State != w {
			t.Errorf("%s: %s, want %s", s.Key(), s.State, w)
		}
	}
}

func TestCheckConfigDriftRaisesAndApplyClears(t *testing.T) {
	st := appliedFull(t)
	tr := condition.New(condition.Options{})
	CheckConfigDrift(st, tr, nil)
	if tr.IsOpen(ConfigDriftKey) {
		t.Fatal("raised with nothing drifted")
	}
	editGH(t, st, "edited in Jam")
	CheckConfigDrift(st, tr, nil)
	c, ok := tr.Get(ConfigDriftKey)
	if !ok || c.Severity != condition.Info || c.Summary != "1 managed object(s) differ from the config repo" ||
		c.Detail != "destination gh (drifted)" || c.Fix == "" {
		t.Fatalf("%+v %v", c, ok)
	}
	// An apply restores the repo's version and clears it in the same call.
	env := allCreds
	env.Conditions = tr
	if _, err := ApplyConfigTree(context.Background(), st, nil, treeOf(t, fullTreeFiles), env, dirOpts(), nil, "alice"); err != nil {
		t.Fatal(err)
	}
	if tr.IsOpen(ConfigDriftKey) {
		t.Fatal("the apply left config.drift open")
	}
}

// config write then apply (Jam wins): the plan has nothing to change, but
// its metadata-only write refreshes the stale mark, and the check clears.
func TestWriteAndApplyClearsConfigDrift(t *testing.T) {
	st := appliedFull(t)
	tr := condition.New(condition.Options{})
	editGH(t, st, "kept")
	CheckConfigDrift(st, tr, nil)
	if !tr.IsOpen(ConfigDriftKey) {
		t.Fatal("not raised")
	}
	exp, err := ExportConfigTree(st.ExportConfig(), st.ListConfigMarks(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	env := allCreds
	env.Conditions = tr
	res, err := ApplyConfigTree(context.Background(), st, nil, exportTree(t, exp), env, dirOpts(), nil, "alice")
	if err != nil || res.Applied {
		t.Fatalf("write-and-apply: %+v %v", res, err)
	}
	if tr.IsOpen(ConfigDriftKey) {
		t.Fatal("write-and-apply left config.drift open")
	}
}

// pushUnrenderableKit stores kit name with its files' bytes then lost (a
// store that cannot happen through the API): a Jam that cannot be rendered.
func pushUnrenderableKit(t *testing.T, st *MemStore, name string) {
	t.Helper()
	if _, err := st.PushKit(name, "kind: studio\nprompt: unrenderable "+name+"\n"); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	k := st.kits[name]
	for _, e := range st.kitTrees[k.Trees[k.Current]].Entries {
		delete(st.kitBlobs, e.SHA256)
	}
}

// A live Jam that cannot be rendered (a kit whose files are not stored)
// leaves the condition as it was: no crash, no flapping. A kit.yml that does
// not parse renders (as it is): one bad kit never blocks the check.
func TestCheckConfigDriftKeepsStateOnError(t *testing.T) {
	st := appliedFull(t)
	tr := condition.New(condition.Options{})
	editGH(t, st, "edited in Jam")
	CheckConfigDrift(st, tr, nil)
	if _, err := st.PushKit("odd", "listen: :443"); err != nil { // not a studio kit
		t.Fatal(err)
	}
	if _, err := ConfigMarkStates(st); err != nil {
		t.Fatalf("an unparseable kit does not render: %v", err)
	}
	pushUnrenderableKit(t, st, "bad")
	if _, err := ConfigMarkStates(st); err == nil {
		t.Fatal("a kit with no stored files rendered")
	}
	CheckConfigDrift(st, tr, nil)
	if !tr.IsOpen(ConfigDriftKey) {
		t.Fatal("an unrenderable Jam cleared config.drift")
	}
}

func TestRunConfigDriftCheckChecksAtStart(t *testing.T) {
	st := appliedFull(t)
	editGH(t, st, "edited in Jam")
	tr := condition.New(condition.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { RunConfigDriftCheck(ctx, st, tr, time.Hour, nil); close(done) }()
	for deadline := time.Now().Add(5 * time.Second); !tr.IsOpen(ConfigDriftKey) && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if !tr.IsOpen(ConfigDriftKey) {
		t.Fatal("RunConfigDriftCheck did not check at start")
	}
}

// An unrenderable Jam warns once per distinct error, not every minute, and
// warns again after a check that worked.
func TestCheckConfigDriftWarnsOncePerError(t *testing.T) {
	st := appliedFull(t)
	tr := condition.New(condition.Options{})
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	CheckConfigDrift(st, tr, log) // a good check resets what was last warned
	pushUnrenderableKit(t, st, "bad")
	CheckConfigDrift(st, tr, log)
	CheckConfigDrift(st, tr, log)
	if n := strings.Count(buf.String(), "level=WARN"); n != 1 {
		t.Fatalf("%d warnings for one repeated error:\n%s", n, buf.String())
	}
	if err := st.RemoveKit("bad"); err != nil {
		t.Fatal(err)
	}
	CheckConfigDrift(st, tr, log)
	pushUnrenderableKit(t, st, "bad")
	CheckConfigDrift(st, tr, log)
	if n := strings.Count(buf.String(), "level=WARN"); n != 2 {
		t.Fatalf("%d warnings, want 2 (the error came back):\n%s", n, buf.String())
	}
}
