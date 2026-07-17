package baseimage

import "testing"

func TestParseLayers(t *testing.T) {
	// docker inspect --format '{{json .RootFS.Layers}}' output.
	got, err := ParseLayers(`["sha256:aaa","sha256:bbb","sha256:ccc"]` + "\n")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"sha256:aaa", "sha256:bbb", "sha256:ccc"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("layer %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseLayersRejectsGarbage(t *testing.T) {
	if _, err := ParseLayers("not json"); err == nil {
		t.Fatal("expected an error parsing non-JSON inspect output")
	}
}

func TestDescendsFrom(t *testing.T) {
	base := []string{"sha256:a", "sha256:b", "sha256:c"}
	tests := []struct {
		name   string
		child  []string
		parent []string
		want   bool
	}{
		{"exact match (identity)", base, base, true},
		{"child adds layers on top", []string{"sha256:a", "sha256:b", "sha256:c", "sha256:d"}, base, true},
		{"unrelated base fails", []string{"sha256:x", "sha256:y"}, base, false},
		{"shares prefix but diverges", []string{"sha256:a", "sha256:b", "sha256:ZZZ"}, base, false},
		{"parent longer than child fails", base, []string{"sha256:a", "sha256:b", "sha256:c", "sha256:d"}, false},
		{"empty parent never matches", base, nil, false},
		{"empty child never matches", nil, base, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DescendsFrom(tt.child, tt.parent); got != tt.want {
				t.Fatalf("DescendsFrom(%v, %v) = %v, want %v", tt.child, tt.parent, got, tt.want)
			}
		})
	}
}

// A resolved base is verified if it descends from ANY blessed image (the rolling
// set — COV-36/COV-37).
func TestVerifyAnyBlessed(t *testing.T) {
	child := []string{"sha256:a", "sha256:b", "sha256:kit1", "sha256:kit2"}
	old := []string{"sha256:a", "sha256:OLD"}   // a stale blessed image
	current := []string{"sha256:a", "sha256:b"} // the image the kit built on
	if !Verify(child, [][]string{old, current}) {
		t.Fatal("child descends from the current blessed image; must verify")
	}
	if Verify(child, [][]string{old}) {
		t.Fatal("child does not descend from the only (stale) blessed image; must fail")
	}
	if Verify(child, nil) {
		t.Fatal("no blessed images → nothing can verify")
	}
}
