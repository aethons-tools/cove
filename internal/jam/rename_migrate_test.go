package jam

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/legacynames"
)

type fakeRenamer struct {
	containers []string // RetireLegacyContainers' answer
	owned      []string // the ids RetireLegacyContainers was given
	retired    []string
	also       []string
	moved      []string
	err        error
	calls      []string // method names, in order
}

func (f *fakeRenamer) RetireLegacyContainers(_ context.Context, ids []string) ([]string, error) {
	f.owned = ids
	f.calls = append(f.calls, "containers")
	return f.containers, nil
}
func (f *fakeRenamer) RetireLegacyStudio(_ context.Context, id string) error {
	f.retired = append(f.retired, id)
	f.calls = append(f.calls, "studio")
	return nil
}
func (f *fakeRenamer) MigrateStateVolumes(_ context.Context, also []string) ([]string, error) {
	f.also = also
	f.calls = append(f.calls, "volumes")
	return f.moved, f.err
}
func (f *fakeRenamer) StudioLocation(id string) string { return "jam-studio-" + id }

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestMigrateStudioNamesRetiresAndRewrites(t *testing.T) {
	st := NewMemStore()
	put := func(i Instance) {
		if err := st.PutInstance(i); err != nil {
			t.Fatal(err)
		}
	}
	put(Instance{ActorID: "s1", SessionKind: SessionKindStanding, Phase: PhaseLive, Location: legacynames.Container("s1"), ImageTag: "old-tag", KitDraftImage: "old-draft"})
	put(Instance{ActorID: "e1", Phase: PhaseIdled, Location: legacynames.Container("e1"), ImageTag: "old-tag"})
	put(Instance{ActorID: "g1", Phase: PhaseGone, Location: legacynames.Container("g1")})
	put(Instance{ActorID: "n1", Phase: PhaseLive, Location: "jam-studio-n1", ImageTag: "new-tag"})
	put(Instance{ActorID: "r1", Phase: PhaseLive, Manager: "remote-a", Location: legacynames.Container("r1"), ImageTag: "remote-tag"})
	mustCreateProject(t, st, "acme")
	pid, _ := ProjectIDOf(st, "acme")
	if err := st.PutStandingSession(pid, "r", "idle", "s2"); err != nil {
		t.Fatal(err)
	}
	r := &fakeRenamer{}
	rep, err := MigrateStudioNames(context.Background(), st, r, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	if r.calls[0] != "containers" || r.calls[len(r.calls)-1] != "volumes" {
		t.Fatalf("calls = %v: every pre-rename container goes first, the volumes last", r.calls)
	}
	if !slices.Equal(r.owned, []string{"e1", "n1", "s1", "s2"}) {
		t.Fatalf("owned ids = %v (local, not gone; standing sessions too)", r.owned)
	}
	slices.Sort(r.retired)
	if !slices.Equal(r.retired, []string{"e1", "s1"}) {
		t.Fatalf("retired = %v (gone, already-renamed and remote instances are skipped)", r.retired)
	}
	if !slices.Equal(r.also, []string{"s1", "s2"}) {
		t.Fatalf("standing ids = %v", r.also)
	}
	s1, _ := st.GetInstance("s1")
	if s1.Location != "jam-studio-s1" || s1.ImageTag != "" || s1.KitDraftImage != "" || s1.Phase != PhaseLost {
		t.Fatalf("s1 = %+v", s1)
	}
	if n1, _ := st.GetInstance("n1"); n1.ImageTag != "new-tag" || n1.Phase != PhaseLive {
		t.Fatalf("an already-renamed instance changed: %+v", n1)
	}
	if r1, _ := st.GetInstance("r1"); r1.ImageTag != "remote-tag" || r1.Phase != PhaseLive {
		t.Fatalf("another manager's instance changed: %+v", r1)
	}
	if len(rep.Retired) != 2 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestMigrateStudioNamesIsIdempotent(t *testing.T) {
	st := NewMemStore()
	_ = st.PutInstance(Instance{ActorID: "s1", Phase: PhaseLive, Location: legacynames.Container("s1")})
	if _, err := MigrateStudioNames(context.Background(), st, &fakeRenamer{}, nil); err != nil {
		t.Fatal(err)
	}
	r := &fakeRenamer{}
	if _, err := MigrateStudioNames(context.Background(), st, r, nil); err != nil || len(r.retired) != 0 {
		t.Fatalf("second run retired %v, %v", r.retired, err)
	}
}

func TestMigrateStudioNamesFailsClosedOnAVolumeError(t *testing.T) {
	st := NewMemStore()
	r := &fakeRenamer{err: errText("migration: volume a could not be copied to b; left in place")}
	if _, err := MigrateStudioNames(context.Background(), st, r, nil); err == nil || err.Error() != "migration: volume a could not be copied to b; left in place" {
		t.Fatalf("err = %v", err)
	}
}

type errText string

func (e errText) Error() string { return string(e) }

// After the migration, the supervisor's first reconcile finishes the retired
// studio's teardown through the normal path.
func TestMigratedStudioIsTornDownByReconcile(t *testing.T) {
	st := NewMemStore()
	_ = st.PutInstance(Instance{ActorID: "s1", Phase: PhaseLive, Location: legacynames.Container("s1")})
	if _, err := MigrateStudioNames(context.Background(), st, &fakeRenamer{}, nil); err != nil {
		t.Fatal(err)
	}
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup := NewSupervisor(st, fl, "holder-A", time.Minute, time.Minute, time.Now, discardLog())
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(fl.tornDown, "s1") {
		t.Fatalf("tornDown = %v", fl.tornDown)
	}
	if _, ok := st.GetInstance("s1"); ok {
		t.Fatal("the retired instance must be removed once torn down")
	}
}
