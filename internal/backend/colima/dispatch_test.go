package colima

import (
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/runner"
)

func TestRunEphemeralArgs(t *testing.T) {
	f := &runner.Fake{}
	c := New(f).(*Colima)
	// No digest (legacy manifest): the ephemeral run falls back to the tag.
	inst, err := c.RunEphemeral("img:tag", "", "disp-1", "at-cove.work", nil)
	if err != nil {
		t.Fatalf("RunEphemeral: %v", err)
	}
	if inst.Container != "disp-1" || inst.Image != "img:tag" {
		t.Fatalf("instance = %+v", inst)
	}
	got := strings.Join(f.Calls[len(f.Calls)-1].Args, " ")
	for _, want := range []string{"run -d", "--name disp-1", "--rm", "--label at-cove.work", "-p 127.0.0.1::2222", "img:tag"} {
		if !strings.Contains(got, want) {
			t.Errorf("run args missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "-v ") {
		t.Errorf("ephemeral run must not mount a volume:\n%s", got)
	}
	// No image.dns configured (nil): no --dns flag, so the container inherits
	// Docker's default resolver (COV-106).
	if strings.Contains(got, "--dns") {
		t.Errorf("ephemeral run with no image.dns must not pin a resolver:\n%s", got)
	}
}

// TestRunEphemeralDNS: a configured image.dns pins the container's resolvers,
// one --dns pair per IP, on the ephemeral dispatch run (COV-106).
func TestRunEphemeralDNS(t *testing.T) {
	f := &runner.Fake{}
	c := New(f).(*Colima)
	if _, err := c.RunEphemeral("img:tag", "", "disp-1", "at-cove.work", []string{"10.0.0.53", "10.0.0.54"}); err != nil {
		t.Fatalf("RunEphemeral: %v", err)
	}
	got := strings.Join(f.Calls[len(f.Calls)-1].Args, " ")
	for _, want := range []string{"--dns 10.0.0.53", "--dns 10.0.0.54"} {
		if !strings.Contains(got, want) {
			t.Errorf("run args missing %q:\n%s", want, got)
		}
	}
}

// TestRunEphemeralPinsDigest: like the persistent create path, the ephemeral
// dispatch run pins the built-image digest when one was captured, running it
// instead of the mutable tag while still recording the tag for display (COV-78).
func TestRunEphemeralPinsDigest(t *testing.T) {
	f := &runner.Fake{}
	c := New(f).(*Colima)
	inst, err := c.RunEphemeral("img:tag", "sha256:cafe", "disp-1", "at-cove.work", nil)
	if err != nil {
		t.Fatalf("RunEphemeral: %v", err)
	}
	if inst.Image != "img:tag" || inst.ImageDigest != "sha256:cafe" {
		t.Fatalf("instance must keep the tag and record the digest: %+v", inst)
	}
	got := strings.Join(f.Calls[len(f.Calls)-1].Args, " ")
	if !strings.Contains(got, "sha256:cafe") || strings.Contains(got, "img:tag") {
		t.Errorf("ephemeral run must pin the digest, not the tag:\n%s", got)
	}
}

func TestScavengeLabeledRemovesOldOnly(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	// runner.Fake.Outputs is a slice consumed in call order (not keyed by
	// command), so results must be queued in the exact sequence Output is
	// invoked: first `ps -aq --filter label=...`, then one `inspect` per id in
	// the order strings.Fields yields them ("old" then "fresh").
	f := &runner.Fake{
		Outputs: []runner.FakeResult{
			{Stdout: "old\nfresh\n"},
			{Stdout: now.Add(-2 * time.Hour).Format(time.RFC3339Nano)},
			{Stdout: now.Add(-1 * time.Minute).Format(time.RFC3339Nano)},
		},
	}
	c := New(f).(*Colima)
	n, err := c.ScavengeLabeled("at-cove.work", 30*time.Minute, now)
	if err != nil {
		t.Fatalf("ScavengeLabeled: %v", err)
	}
	if n != 1 {
		t.Fatalf("removed %d; want 1 (only the 2h-old one)", n)
	}
	joined := ""
	for _, c := range f.Calls {
		joined += strings.Join(c.Args, " ") + "\n"
	}
	if !strings.Contains(joined, "rm -f old") || strings.Contains(joined, "rm -f fresh") {
		t.Fatalf("should rm old, not fresh:\n%s", joined)
	}
}
