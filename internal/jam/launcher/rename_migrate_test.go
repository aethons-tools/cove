package launcher

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/basedigest"
	"github.com/aethons-tools/cove/internal/legacynames"
	"github.com/aethons-tools/cove/internal/naming"
	"github.com/aethons-tools/cove/internal/runner"
)

// volFake is a stateful docker for the migration: volumes with labels and a
// content marker, containers by name (→ image), local images.
type volFake struct {
	*fakeOps
	vols       map[string]map[string]string // name → labels
	data       map[string]string            // name → content
	containers map[string]string            // name → image
	mounts     map[string][]string          // container → the volumes it mounts
	imageErr   map[string]bool              // image → HasKitImage errors
	labelled   map[string]bool              // containers carrying the pre-rename label
	copyFail   map[string]bool              // from volume → copy fails
	rmFail     map[string]bool              // volume → removal fails
	copyImages []string                     // the image of each copy, in order
}

func newVolFake() *volFake {
	return &volFake{fakeOps: &fakeOps{hasImage: map[string]bool{basedigest.DefaultRef(): true}},
		vols: map[string]map[string]string{}, data: map[string]string{},
		containers: map[string]string{}, mounts: map[string][]string{}, imageErr: map[string]bool{}, labelled: map[string]bool{}, copyFail: map[string]bool{}, rmFail: map[string]bool{}}
}

func (f *volFake) addVol(name, content string, labels ...string) {
	l := map[string]string{}
	for _, kv := range labels {
		k, v, _ := strings.Cut(kv, "=")
		l[k] = v
	}
	f.vols[name], f.data[name] = l, content
}
func (f *volFake) CreateVolume(name string, labels ...string) error {
	if _, ok := f.vols[name]; !ok {
		f.addVol(name, "", labels...)
	}
	return nil
}
func (f *volFake) RemoveVolumes(names ...string) error {
	for _, n := range names {
		if f.rmFail[n] {
			return errors.New("volume in use")
		}
		delete(f.vols, n)
		delete(f.data, n)
	}
	return nil
}
func (f *volFake) ListVolumes(key string, match ...string) (map[string]string, error) {
	out := map[string]string{}
next:
	for n, l := range f.vols {
		v, ok := l[key]
		if !ok {
			continue
		}
		for _, m := range match {
			k, want, _ := strings.Cut(m, "=")
			if l[k] != want {
				continue next
			}
		}
		out[n] = v
	}
	return out, nil
}
func (f *volFake) VolumeExists(name string) (bool, error) { _, ok := f.vols[name]; return ok, nil }
func (f *volFake) CopyVolume(image, from, to string) error {
	if !f.hasImage[image] {
		return errors.New("copy image not on the host: " + image)
	}
	f.copyImages = append(f.copyImages, image)
	if f.copyFail[from] {
		f.data[to] = "partial"
		return errors.New("no space left on device")
	}
	f.data[to] = f.data[from]
	return nil
}
func (f *volFake) ListContainers(filter string) (map[string]backend.ContainerInfo, error) {
	out := map[string]backend.ContainerInfo{}
	for n, img := range f.containers {
		switch {
		case filter == "label="+legacynames.Label && f.labelled[n],
			strings.HasPrefix(filter, "name=") && strings.Contains(n, strings.TrimPrefix(filter, "name=")):
			out[n] = backend.ContainerInfo{Image: img, Mounts: f.mounts[n]}
		}
	}
	return out, nil
}
func (f *volFake) HasKitImage(tag string) (bool, error) {
	if f.imageErr[tag] {
		return false, errors.New("daemon hiccup")
	}
	return f.hasImage[tag], nil
}
func (f *volFake) GetStatus(c string) (backend.State, error) {
	if _, ok := f.containers[c]; ok {
		return backend.StateRunning, nil
	}
	return backend.StateAbsent, nil
}
func (f *volFake) RemoveContainer(name string) error { delete(f.containers, name); return nil }

// marker is the incomplete-copy marker of new volume nw copied from old.
func (f *volFake) marker(nw, old string) {
	f.addVol(migrationMarker(nw), "", MigrationLabel+"="+MigrationIncomplete, MigratedFromLabel+"="+old)
}

func migLauncher(t *testing.T, f *volFake) *Launcher {
	return New(Config{Ops: f, Runner: &runner.Fake{}, JamID: "j1", KnownHostsDir: t.TempDir()})
}

func oldVols(id string) (agent, ws string) {
	c := legacynames.Container(id)
	return naming.AgentDataVolume(c), naming.WorkspaceVolume(c)
}
func newVols(id string) (agent, ws string) {
	c := naming.StudioContainer(id)
	return naming.AgentDataVolume(c), naming.WorkspaceVolume(c)
}

func failedText(old, nw string) string {
	return "migration: volume " + old + " could not be copied to " + nw + "; left in place"
}

func TestMigrateStateVolumesCopiesThenRemovesOld(t *testing.T) {
	f := newVolFake()
	oa, ow := oldVols("s1")
	f.addVol(oa, "agent-data-bytes", legacynames.StateLabel+"=s1", legacynames.JamLabel+"=j1")
	f.addVol(ow, "workspace-bytes", legacynames.StateLabel+"=s1", legacynames.JamLabel+"=j1")
	f.addVol("other-jam-vol", "x", legacynames.StateLabel+"=s9", legacynames.JamLabel+"=j2") // another Jam's: untouched
	done, err := migLauncher(t, f).MigrateStateVolumes(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	na, nw := newVols("s1")
	if f.data[na] != "agent-data-bytes" || f.data[nw] != "workspace-bytes" {
		t.Fatalf("copied data = %q / %q", f.data[na], f.data[nw])
	}
	if f.vols[na][StateLabel] != "s1" || f.vols[na][JamLabel] != "j1" || f.vols[na][MigratedFromLabel] != oa {
		t.Fatalf("new labels = %v", f.vols[na])
	}
	for _, v := range []string{oa, ow, migrationMarker(na), migrationMarker(nw)} {
		if _, ok := f.vols[v]; ok {
			t.Fatalf("%s must be gone after a verified copy (vols %v)", v, slices.Sorted(maps.Keys(f.vols)))
		}
	}
	if _, ok := f.vols["other-jam-vol"]; !ok {
		t.Fatal("another Jam's volume must be untouched")
	}
	if len(done) != 2 || !slices.Equal(f.copyImages, []string{basedigest.DefaultRef(), basedigest.DefaultRef()}) {
		t.Fatalf("done = %v, copy images %v", done, f.copyImages)
	}
	again, err := migLauncher(t, f).MigrateStateVolumes(context.Background(), []string{"s1"})
	if err != nil || len(again) != 0 {
		t.Fatalf("second run = %v, %v", again, err)
	}
}

func TestMigrateStateVolumesFailedCopyLeavesOldAndFails(t *testing.T) {
	f := newVolFake()
	oa, _ := oldVols("s1")
	f.addVol(oa, "precious", legacynames.StateLabel+"=s1", legacynames.JamLabel+"=j1")
	f.copyFail[oa] = true
	_, err := migLauncher(t, f).MigrateStateVolumes(context.Background(), nil)
	na, _ := newVols("s1")
	if err == nil || err.Error() != failedText(oa, na) {
		t.Fatalf("err = %v, want %q", err, failedText(oa, na))
	}
	if f.data[oa] != "precious" {
		t.Fatal("old volume must be left in place")
	}
	if _, ok := f.vols[na]; ok {
		t.Fatal("the partial new volume must be removed")
	}
	// The next start retries and, the fault gone, completes.
	f.copyFail[oa] = false
	if _, err := migLauncher(t, f).MigrateStateVolumes(context.Background(), nil); err != nil || f.data[na] != "precious" {
		t.Fatalf("retry: %v, new = %q", err, f.data[na])
	}
}

// A failed partial-copy cleanup keeps the marker, so the next start still
// knows the new volume for a partial copy and redoes it.
func TestMigrateStateVolumesKeepsTheMarkerWhenCleanupFails(t *testing.T) {
	f := newVolFake()
	oa, _ := oldVols("s1")
	na, _ := newVols("s1")
	f.addVol(oa, "precious", legacynames.StateLabel+"=s1", legacynames.JamLabel+"=j1")
	f.copyFail[oa], f.rmFail[na] = true, true
	if _, err := migLauncher(t, f).MigrateStateVolumes(context.Background(), nil); err == nil || err.Error() != failedText(oa, na) {
		t.Fatalf("err = %v", err)
	}
	if f.vols[migrationMarker(na)][MigrationLabel] != MigrationIncomplete {
		t.Fatal("the incomplete marker must stay while the partial copy does")
	}
	f.copyFail[oa], f.rmFail[na] = false, false
	if _, err := migLauncher(t, f).MigrateStateVolumes(context.Background(), nil); err != nil || f.data[na] != "precious" {
		t.Fatalf("retry: %v, new = %q", err, f.data[na])
	}
}

func TestMigrateStateVolumesRetriesAPartialCopy(t *testing.T) {
	f := newVolFake()
	oa, _ := oldVols("s1")
	na, _ := newVols("s1")
	f.addVol(oa, "precious", legacynames.StateLabel+"=s1", legacynames.JamLabel+"=j1")
	// Left by a start that crashed mid-copy: the new volume and its marker.
	f.addVol(na, "partial", StateLabel+"=s1", JamLabel+"=j1", MigratedFromLabel+"="+oa)
	f.marker(na, oa)
	if _, err := migLauncher(t, f).MigrateStateVolumes(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if f.data[na] != "precious" {
		t.Fatalf("new = %q; a partial copy must be redone", f.data[na])
	}
	if _, ok := f.vols[migrationMarker(na)]; ok {
		t.Fatal("the marker must be cleared after the verified copy")
	}
}

// Rollback, then re-upgrade: the old binary recreated an old-named volume
// while the migrated one holds the session's data. The migrated volume
// (migrated-from label, no incomplete marker) is never deleted or
// overwritten; serve start fails closed with both left in place.
func TestMigrateStateVolumesNeverDeletesACompletedCopyAfterRollback(t *testing.T) {
	f := newVolFake()
	oa, _ := oldVols("s1")
	na, _ := newVols("s1")
	f.addVol(oa, "precious", legacynames.StateLabel+"=s1", legacynames.JamLabel+"=j1")
	if _, err := migLauncher(t, f).MigrateStateVolumes(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	f.data[na] = "work-done-after-the-upgrade"
	f.addVol(oa, "rollback-era", legacynames.StateLabel+"=s1", legacynames.JamLabel+"=j1") // the rolled-back Jam's raise
	_, err := migLauncher(t, f).MigrateStateVolumes(context.Background(), nil)
	if err == nil || err.Error() != failedText(oa, na) {
		t.Fatalf("err = %v, want %q", err, failedText(oa, na))
	}
	if f.data[na] != "work-done-after-the-upgrade" || f.data[oa] != "rollback-era" {
		t.Fatalf("neither volume may change: new %q, old %q", f.data[na], f.data[oa])
	}
}

// A marker naming another old volume does not make the new one partial.
func TestMigrateStateVolumesIgnoresAMarkerForAnotherSource(t *testing.T) {
	f := newVolFake()
	oa, _ := oldVols("s1")
	na, _ := newVols("s1")
	f.addVol(oa, "precious", legacynames.StateLabel+"=s1", legacynames.JamLabel+"=j1")
	f.addVol(na, "live", StateLabel+"=s1", JamLabel+"=j1", MigratedFromLabel+"="+oa)
	f.marker(na, "some-other-volume")
	if _, err := migLauncher(t, f).MigrateStateVolumes(context.Background(), nil); err == nil {
		t.Fatal("must refuse")
	}
	if f.data[na] != "live" {
		t.Fatalf("new = %q", f.data[na])
	}
}

func TestMigrateStateVolumesRefusesAForeignNewVolume(t *testing.T) {
	f := newVolFake()
	oa, _ := oldVols("s1")
	na, _ := newVols("s1")
	f.addVol(oa, "precious", legacynames.StateLabel+"=s1", legacynames.JamLabel+"=j1")
	f.addVol(na, "someone-elses", StateLabel+"=s1", JamLabel+"=j1") // not made by the migration
	if _, err := migLauncher(t, f).MigrateStateVolumes(context.Background(), nil); err == nil || err.Error() != failedText(oa, na) {
		t.Fatalf("must refuse to overwrite a new-named volume it did not create: %v", err)
	}
	if f.data[na] != "someone-elses" || f.data[oa] != "precious" {
		t.Fatal("neither volume may change")
	}
}

func TestMigrateStateVolumesFindsUnlabelledStandingVolumes(t *testing.T) {
	f := newVolFake()
	oa, ow := oldVols("standing-p-r-n")
	f.addVol(oa, "a") // created before state volumes were labelled
	f.addVol(ow, "w")
	if _, err := migLauncher(t, f).MigrateStateVolumes(context.Background(), []string{"standing-p-r-n"}); err != nil {
		t.Fatal(err)
	}
	na, nw := newVols("standing-p-r-n")
	if f.data[na] != "a" || f.data[nw] != "w" {
		t.Fatalf("vols = %v", slices.Sorted(maps.Keys(f.vols)))
	}
}

// No image that can run the copy is on the host: a fixed, actionable error
// before anything is created; nothing is pulled.
func TestMigrateStateVolumesNeedsALocalImage(t *testing.T) {
	f := newVolFake()
	f.hasImage = map[string]bool{}
	oa, _ := oldVols("s1")
	f.addVol(oa, "precious", legacynames.StateLabel+"=s1", legacynames.JamLabel+"=j1")
	l := New(Config{Ops: f, Runner: &runner.Fake{}, JamID: "j1", KnownHostsDir: t.TempDir(), DockerContext: "colima-jam"})
	_, err := l.MigrateStateVolumes(context.Background(), nil)
	want := "migration: no image on this host can copy the state volumes (nothing was moved); " +
		"pull the jam base — docker --context colima-jam pull " + basedigest.DefaultRef() + " — and start jam serve again"
	if err == nil || err.Error() != want || !errors.Is(err, ErrNoCopyImage) {
		t.Fatalf("err = %v, want %q", err, want)
	}
	// Without a docker-context the command is docker's default context.
	_, err = migLauncher(t, f).MigrateStateVolumes(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "— docker pull "+basedigest.DefaultRef()+" —") || strings.Contains(err.Error(), "alpine") {
		t.Fatalf("err = %v", err)
	}
	if len(f.vols) != 1 || f.data[oa] != "precious" {
		t.Fatalf("nothing may be created or moved: %v", slices.Sorted(maps.Keys(f.vols)))
	}
}

// Without the jam base on the host, the pre-rename base (same digests after
// the re-cut) or a retired studio's image runs the copy.
func TestMigrateStateVolumesUsesAnImageAlreadyOnTheHost(t *testing.T) {
	f := newVolFake()
	oldBase := legacynames.BaseImageRepo + "@" + basedigest.Blessed()[0]
	f.hasImage = map[string]bool{oldBase: true}
	oa, _ := oldVols("s1")
	f.addVol(oa, "precious", legacynames.StateLabel+"=s1", legacynames.JamLabel+"=j1")
	if _, err := migLauncher(t, f).MigrateStateVolumes(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.copyImages, []string{oldBase}) {
		t.Fatalf("copy images = %v", f.copyImages)
	}

	// A retired studio's image goes first, even with the jam base local; an
	// image check that errors is skipped, not taken for absence of all.
	g := newVolFake()
	g.hasImage = map[string]bool{"kit-image@sha256:1": true, "kit-image@sha256:2": true, basedigest.DefaultRef(): true}
	g.imageErr["kit-image@sha256:1"] = true
	for i, id := range []string{"s2", "s3"} {
		c := legacynames.Container(id)
		g.containers[c], g.labelled[c] = "kit-image@sha256:"+string(rune('1'+i)), true
	}
	ob, _ := oldVols("s2")
	g.addVol(ob, "data", legacynames.StateLabel+"=s2", legacynames.JamLabel+"=j1")
	l := migLauncher(t, g)
	if _, err := l.RetireLegacyContainers(context.Background(), []string{"s2", "s3"}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.MigrateStateVolumes(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(g.copyImages, []string{"kit-image@sha256:2"}) {
		t.Fatalf("copy images = %v", g.copyImages)
	}
}

func TestRetireLegacyStudioRemovesContainerCacheAndKnownHosts(t *testing.T) {
	f := newVolFake()
	old := legacynames.Container("s1")
	f.containers[old] = "img"
	f.addVol(naming.DockerVolume(old), "cache")
	l := migLauncher(t, f)
	kh := filepath.Join(l.cfg.KnownHostsDir, old)
	if err := os.WriteFile(kh, []byte("pin"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.RetireLegacyStudio(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.containers[old]; ok {
		t.Fatal("old container must be removed")
	}
	if _, ok := f.vols[naming.DockerVolume(old)]; ok {
		t.Fatal("old -docker cache must be removed")
	}
	if _, err := os.Stat(kh); !os.IsNotExist(err) {
		t.Fatal("old known_hosts pin must be removed")
	}
	if err := l.RetireLegacyStudio(context.Background(), "s1"); err != nil {
		t.Fatalf("retiring again (nothing left) must succeed: %v", err)
	}
	if l.StudioLocation("s1") != naming.StudioContainer("s1") {
		t.Fatal("StudioLocation is the new container name")
	}
}

// Pre-rename containers are found by the old label and by the old name
// prefix, but only this Jam's are retired: the legacy name of one of its
// instances, or one mounting a state volume labelled with this Jam's id.
// Another Jam's, on the same docker host, is left running and named in one
// warning.
func TestRetireLegacyContainersRetiresOnlyThisJams(t *testing.T) {
	f := newVolFake()
	ours, mounting := legacynames.Container("s1"), "renamed-by-hand"
	theirs, theirsLabelled := legacynames.Container("x9"), "their-renamed"
	f.containers[ours] = "img-a"
	f.containers[mounting], f.labelled[mounting] = "img-b", true
	oa, _ := oldVols("s7")
	f.addVol(oa, "ours", legacynames.StateLabel+"=s7", legacynames.JamLabel+"=j1")
	f.mounts[mounting] = []string{oa}
	f.containers[theirs] = "img-c"
	ox, _ := oldVols("x9")
	f.addVol(ox, "theirs", legacynames.StateLabel+"=x9", legacynames.JamLabel+"=j2")
	f.mounts[theirs] = []string{ox}
	f.containers[theirsLabelled], f.labelled[theirsLabelled] = "img-d", true
	f.containers[naming.StudioContainer("n1")] = "jam-img"
	f.containers["unrelated"] = "x"
	f.addVol(naming.DockerVolume(ours), "cache")
	var logs strings.Builder
	l := New(Config{Ops: f, Runner: &runner.Fake{}, JamID: "j1", KnownHostsDir: t.TempDir(),
		Log: slog.New(slog.NewTextHandler(&logs, nil))})
	got, err := l.RetireLegacyContainers(context.Background(), []string{"s1", "n1"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{ours, mounting}) {
		t.Fatalf("retired = %v", got)
	}
	left := slices.Sorted(maps.Keys(f.containers))
	if !slices.Equal(left, slices.Sorted(slices.Values([]string{naming.StudioContainer("n1"), theirs, theirsLabelled, "unrelated"}))) {
		t.Fatalf("containers left = %v", left)
	}
	if _, ok := f.vols[naming.DockerVolume(ours)]; ok {
		t.Fatal("its -docker cache must go too")
	}
	if !strings.Contains(logs.String(), "pre-rename containers not owned by this Jam left running") ||
		!strings.Contains(logs.String(), theirs) || !strings.Contains(logs.String(), theirsLabelled) {
		t.Fatalf("log = %s", logs.String())
	}
	if again, err := l.RetireLegacyContainers(context.Background(), []string{"s1", "n1"}); err != nil || len(again) != 0 {
		t.Fatalf("second run = %v, %v", again, err)
	}
}

// Each copy is logged, by name, before it runs.
func TestMigrateStateVolumesLogsEachCopy(t *testing.T) {
	f := newVolFake()
	oa, _ := oldVols("s1")
	na, _ := newVols("s1")
	f.addVol(oa, "secret-bytes", legacynames.StateLabel+"=s1", legacynames.JamLabel+"=j1")
	var logs strings.Builder
	l := New(Config{Ops: f, Runner: &runner.Fake{}, JamID: "j1", KnownHostsDir: t.TempDir(),
		Log: slog.New(slog.NewTextHandler(&logs, nil))})
	if _, err := l.MigrateStateVolumes(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "migration: copying volume "+oa+" → "+na) || strings.Contains(logs.String(), "secret-bytes") {
		t.Fatalf("log = %s", logs.String())
	}
}

// The old volume cannot be removed after a verified copy: serve start fails
// closed with the fixed text, both kept; the next start does not redo or
// touch the completed copy.
func TestMigrateStateVolumesFailedOldRemovalFailsClosed(t *testing.T) {
	f := newVolFake()
	oa, _ := oldVols("s1")
	na, _ := newVols("s1")
	f.addVol(oa, "precious", legacynames.StateLabel+"=s1", legacynames.JamLabel+"=j1")
	f.rmFail[oa] = true
	if _, err := migLauncher(t, f).MigrateStateVolumes(context.Background(), nil); err == nil || err.Error() != failedText(oa, na) {
		t.Fatalf("err = %v, want %q", err, failedText(oa, na))
	}
	if f.data[oa] != "precious" || f.data[na] != "precious" {
		t.Fatalf("both volumes must be kept: old %q new %q", f.data[oa], f.data[na])
	}
	if _, ok := f.vols[migrationMarker(na)]; ok {
		t.Fatal("the marker is cleared once the copy verified")
	}
	f.data[na] = "used"
	if _, err := migLauncher(t, f).MigrateStateVolumes(context.Background(), nil); err == nil || err.Error() != failedText(oa, na) {
		t.Fatalf("retry err = %v, want %q", err, failedText(oa, na))
	}
	if f.data[na] != "used" || f.data[oa] != "precious" || len(f.copyImages) != 1 {
		t.Fatalf("a completed copy is never redone: new %q old %q copies %v", f.data[na], f.data[oa], f.copyImages)
	}
}
