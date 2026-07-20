package colima

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/runner"
)

// The privileged delivery op must exec the sealed helper (as root, context-pinned)
// with the resolved domains piped on stdin — one per line, never on argv.
func TestApplySessionEgressExecsHelperWithStdin(t *testing.T) {
	f := &runner.Fake{}
	c := New(f).(*Colima)

	var egress backend.SessionEgress = c // compile-time: the op lives behind the seam
	if err := egress.ApplySessionEgress("sbx-1", []string{"registry.example.com", "github.com"}); err != nil {
		t.Fatalf("ApplySessionEgress: %v", err)
	}

	// The last recorded call is the exec (preflight's `docker info` runs first).
	call := f.Calls[len(f.Calls)-1]
	if call.Name != "docker" {
		t.Fatalf("exec via %q; want docker", call.Name)
	}
	got := strings.Join(call.Args, " ")
	for _, want := range []string{
		"--context colima",
		"exec -i",
		"-u root",
		"sbx-1",
		"/usr/local/lib/cove/apply-session-domains.sh",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("exec args missing %q:\n%s", want, got)
		}
	}

	// Domains flow on stdin (one per line), never as argv.
	for _, d := range []string{"registry.example.com", "github.com"} {
		for _, a := range call.Args {
			if a == d {
				t.Errorf("domain %q leaked onto argv:\n%s", d, got)
			}
		}
	}
	if call.Stdin != "registry.example.com\ngithub.com\n" {
		t.Errorf("stdin = %q; want the two domains, one per line", call.Stdin)
	}
}

// An empty list still execs the helper — with empty stdin, so the helper writes a
// header-only session file and egress reverts to the baked (sealed + kit) lists.
func TestApplySessionEgressEmptyClears(t *testing.T) {
	f := &runner.Fake{}
	c := New(f).(*Colima)

	if err := c.ApplySessionEgress("sbx-1", nil); err != nil {
		t.Fatalf("ApplySessionEgress: %v", err)
	}

	call := f.Calls[len(f.Calls)-1]
	if !strings.Contains(strings.Join(call.Args, " "), "/usr/local/lib/cove/apply-session-domains.sh") {
		t.Fatalf("empty list must still exec the helper to clear:\n%v", call.Args)
	}
	if call.Stdin != "" {
		t.Errorf("stdin = %q; want empty (header-only clear)", call.Stdin)
	}
}
