package jam

import (
	"net/http"
	"slices"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/condition"
)

func TestReleaseConfigMark(t *testing.T) {
	st := appliedFull(t)
	tr := condition.New(condition.Options{})
	editGH(t, st, "edited in Jam")
	CheckConfigDrift(st, tr, nil)
	gh := ConfigKey{ConfigKindDestination, "gh"}
	if err := ReleaseConfigMark(st, gh, tr, nil, "alice"); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(st.ListConfigMarks(), func(m ConfigMark) bool { return m.Key() == gh }) {
		t.Fatal("mark kept")
	}
	if _, ok := findDestination(st, "gh"); !ok {
		t.Fatal("release removed the object")
	}
	if tr.IsOpen(ConfigDriftKey) {
		t.Fatal("release of the only drifted object left config.drift open")
	}
	// A repo without it now leaves it alone: unmanaged, not a delete.
	p := planOn(t, st, treeOf(t, without(fullTreeFiles, "destinations/gh.yml")))
	if opOf(p, ConfigKindDestination, "gh").Action != "" || !slices.Contains(p.Unmanaged, gh) {
		t.Fatalf("after release: %+v unmanaged=%v", opOf(p, ConfigKindDestination, "gh"), p.Unmanaged)
	}
	if err := ReleaseConfigMark(st, gh, tr, nil, "alice"); WriteStatus(err, 0) != http.StatusNotFound || err.Error() != "config release: destination gh is not managed" {
		t.Fatalf("second release: %v", err)
	}
	if err := ReleaseConfigMark(st, ConfigKey{"widget", "x"}, tr, nil, "alice"); WriteStatus(err, 0) != http.StatusBadRequest {
		t.Fatalf("unknown kind: %v", err)
	}
}
