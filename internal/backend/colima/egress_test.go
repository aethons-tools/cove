package colima

import (
	"errors"
	"io"
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

// The role-egress op execs the sealed apply-role-egress.sh as root with the
// role's domains on stdin — one per line, never on argv.
func TestApplyRoleEgressExecsHelperWithStdin(t *testing.T) {
	f := &runner.Fake{}
	c := New(f).(*Colima)

	var egress backend.RoleEgress = c
	if err := egress.ApplyRoleEgress("cove-1", []string{".example.com", "pkg.go.dev"}); err != nil {
		t.Fatalf("ApplyRoleEgress: %v", err)
	}
	call := f.Calls[len(f.Calls)-1]
	if call.Name != "docker" {
		t.Fatalf("exec via %q; want docker", call.Name)
	}
	got := strings.Join(call.Args, " ")
	if !strings.Contains(got, "exec -i -u root cove-1 /usr/local/lib/cove/apply-role-egress.sh") {
		t.Errorf("exec args = %s; want docker exec -i -u root cove-1 <helper>", got)
	}
	for _, d := range []string{".example.com", "pkg.go.dev"} {
		if strings.Contains(got, d) {
			t.Errorf("domain %q leaked onto argv:\n%s", d, got)
		}
	}
	if call.Stdin != ".example.com\npkg.go.dev\n" {
		t.Errorf("stdin = %q; want the two domains, one per line", call.Stdin)
	}
}

// An empty policy still runs the helper, with empty stdin (nothing beyond base + infra).
func TestApplyRoleEgressEmptyPolicy(t *testing.T) {
	f := &runner.Fake{}
	c := New(f).(*Colima)
	if err := c.ApplyRoleEgress("cove-1", nil); err != nil {
		t.Fatalf("ApplyRoleEgress: %v", err)
	}
	call := f.Calls[len(f.Calls)-1]
	if !strings.Contains(strings.Join(call.Args, " "), "/usr/local/lib/cove/apply-role-egress.sh") {
		t.Fatalf("empty policy must still exec the helper:\n%v", call.Args)
	}
	if call.Stdin != "" {
		t.Errorf("stdin = %q; want empty", call.Stdin)
	}
}

// stderrRunner is a Fake whose RunIO writes a canned stderr and fails, standing
// in for the helper rejecting a domain outside the ceiling.
type stderrRunner struct {
	*runner.Fake
	stderr string
}

func (s stderrRunner) RunIO(stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	_ = s.Fake.RunIO(stdin, stdout, stderr, name, args...)
	if name == "docker" && len(args) > 2 && args[2] == "exec" {
		if stderr != nil {
			_, _ = io.WriteString(stderr, s.stderr)
		}
		return errors.New("exit status 3")
	}
	return nil
}

// The helper's rejection (naming the domain) is carried in the returned error
// rather than spilled onto Jam's own stdout/stderr.
func TestApplyRoleEgressErrorNamesDomain(t *testing.T) {
	r := stderrRunner{Fake: &runner.Fake{}, stderr: "apply-role-egress: evil.example is outside the kit's egress ceiling\n"}
	c := New(r).(*Colima)
	err := c.ApplyRoleEgress("cove-1", []string{"evil.example"})
	if err == nil || !strings.Contains(err.Error(), "evil.example is outside the kit's egress ceiling") {
		t.Fatalf("err = %v; want it to carry the helper's message", err)
	}
}

// The reset op execs the sealed helper as root in --kit-default mode with empty
// stdin: the flag is the only argument, no domains anywhere.
func TestResetRoleEgressExecsKitDefault(t *testing.T) {
	f := &runner.Fake{}
	c := New(f).(*Colima)

	var egress backend.RoleEgress = c
	if err := egress.ResetRoleEgress("cove-1"); err != nil {
		t.Fatalf("ResetRoleEgress: %v", err)
	}
	call := f.Calls[len(f.Calls)-1]
	if call.Name != "docker" {
		t.Fatalf("exec via %q; want docker", call.Name)
	}
	got := strings.Join(call.Args, " ")
	if !strings.HasSuffix(got, "exec -i -u root cove-1 /usr/local/lib/cove/apply-role-egress.sh --kit-default") {
		t.Errorf("exec args = %s; want docker exec -i -u root cove-1 <helper> --kit-default", got)
	}
	if call.Stdin != "" {
		t.Errorf("stdin = %q; want empty", call.Stdin)
	}
}

// A failed reset carries the helper's output in the error.
func TestResetRoleEgressErrorCarriesHelperOutput(t *testing.T) {
	r := stderrRunner{Fake: &runner.Fake{}, stderr: "apply-role-egress: egress ceiling /etc/squid/egress_ceiling.txt is missing\n"}
	c := New(r).(*Colima)
	err := c.ResetRoleEgress("cove-1")
	if err == nil || !strings.Contains(err.Error(), "egress ceiling /etc/squid/egress_ceiling.txt is missing") {
		t.Fatalf("err = %v; want it to carry the helper's message", err)
	}
}
