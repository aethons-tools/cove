package blessgen

import (
	"strings"
	"testing"
)

// Walk collects digests from a newest-first registry listing, from the head down
// to AND INCLUDING the watermark; everything older than the watermark is dropped.
func TestWalkCollectsToWatermark(t *testing.T) {
	listing := []string{"sha256:new", "sha256:mid", "sha256:wm", "sha256:old1", "sha256:old2"}
	got, err := Walk(listing, "sha256:wm")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"sha256:new", "sha256:mid", "sha256:wm"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Walk = %v, want %v (newest-first, through watermark, older dropped)", got, want)
	}
}

// The newest image can itself be the watermark (a fresh breaking bump): the list
// is then exactly the watermark.
func TestWalkWatermarkIsHead(t *testing.T) {
	got, err := Walk([]string{"sha256:wm", "sha256:old"}, "sha256:wm")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != "sha256:wm" {
		t.Fatalf("Walk = %v, want [sha256:wm]", got)
	}
}

// A watermark absent from the listing must FAIL LOUD — a pruned or mistyped
// watermark must never silently shrink the trust set to an implicit prefix.
func TestWalkMissingWatermarkFailsLoud(t *testing.T) {
	if _, err := Walk([]string{"sha256:a", "sha256:b"}, "sha256:gone"); err == nil {
		t.Fatal("expected error when watermark is not in the listing")
	}
	if _, err := Walk(nil, "sha256:wm"); err == nil {
		t.Fatal("expected error when the listing is empty")
	}
	if _, err := Walk([]string{"sha256:a"}, ""); err == nil {
		t.Fatal("expected error when the watermark is empty")
	}
}

// Watermark reads the committed file's oldest (last) meaningful digest — the
// low-watermark — ignoring the header comment and blank lines.
func TestWatermark(t *testing.T) {
	content := "# header\n#\n# more\nsha256:newer\nsha256:oldest\n"
	got, err := Watermark(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "sha256:oldest" {
		t.Fatalf("Watermark = %q, want sha256:oldest", got)
	}
	if _, err := Watermark("# only comments\n\n"); err == nil {
		t.Fatal("expected error when the file has no digest")
	}
}

// Render round-trips: the produced file parses (via Watermark on a reversed view)
// and keeps the digests newest-first under a #-comment header.
func TestRenderIsParseableAndNewestFirst(t *testing.T) {
	digests := []string{"sha256:new", "sha256:mid", "sha256:wm"}
	out := Render(digests)

	// Every rendered digest line, in order, must match the input order.
	var lines []string
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		lines = append(lines, ln)
	}
	if strings.Join(lines, ",") != strings.Join(digests, ",") {
		t.Fatalf("rendered digests = %v, want %v", lines, digests)
	}
	if !strings.HasPrefix(out, "#") {
		t.Fatal("rendered file must open with a #-comment header")
	}
	// The oldest rendered line is the watermark the next build will read back.
	wm, err := Watermark(out)
	if err != nil || wm != "sha256:wm" {
		t.Fatalf("Watermark(Render(...)) = %q, %v; want sha256:wm", wm, err)
	}
}

// digestsNewestFirst keeps only released index manifests and orders them by
// publish time, newest first — dropping untagged per-arch child manifests AND the
// per-arch intermediate tags (sha-<sha>-<arch>) the publish step leaves behind.
func TestDigestsNewestFirstFiltersAndSorts(t *testing.T) {
	vs := []version{
		{Name: "sha256:mid", CreatedAt: "2026-07-10T00:00:00Z", Tags: []string{"1200-0710"}},
		{Name: "sha256:untagged", CreatedAt: "2026-07-12T00:00:00Z", Tags: nil},
		{Name: "sha256:archAMD", CreatedAt: "2026-07-16T00:00:00Z", Tags: []string{"sha-abc123-amd64"}},
		{Name: "sha256:archARM", CreatedAt: "2026-07-16T00:00:01Z", Tags: []string{"sha-abc123-arm64"}},
		{Name: "sha256:new", CreatedAt: "2026-07-15T00:00:00Z", Tags: []string{"1300-0715", "latest"}},
		{Name: "sha256:old", CreatedAt: "2026-07-01T00:00:00Z", Tags: []string{"1100-0701"}},
	}
	got := digestsNewestFirst(vs)
	want := []string{"sha256:new", "sha256:mid", "sha256:old"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("digestsNewestFirst = %v, want %v (index manifests only, newest-first)", got, want)
	}
}
