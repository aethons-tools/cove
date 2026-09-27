package assemble

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
}

// apply-sshenv.sh bridges the image's Docker ENV into /etc/environment (pam_env →
// SSH sessions): PATH is intrinsic (with ~/.local/bin prepended); the vars named
// in COVE_SSHENV transfer their live values; then the sealed runtime env
// (CLAUDE_CONFIG_DIR + egress proxy) is written last so it always wins.
func TestApplySshEnv(t *testing.T) {
	requireBash(t)
	dir := t.TempDir()
	envFile := filepath.Join(dir, "environment")

	cmd := exec.Command("bash", "hardening/image-files/usr/local/lib/cove/apply-sshenv.sh")
	cmd.Env = []string{
		"PATH=/usr/local/go/bin:/usr/bin:/bin", // the image's live PATH (go already on it)
		"COVE_SSHENV=GOROOT:GOPATH",
		"GOROOT=/usr/local/go",
		"GOPATH=/home/agent/go",
		"FOO=nope", // set but NOT named in COVE_SSHENV → must not transfer
		"COVE_ENV_FILE=" + envFile,
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("apply-sshenv.sh failed: %v\n%s", err, out)
	}
	got := read(t, envFile)

	// PATH intrinsic: ~/.local/bin prepended to the image's live PATH (go included).
	if !strings.Contains(got, "PATH=/home/agent/.local/bin:/usr/local/go/bin:/usr/bin:/bin\n") {
		t.Fatalf("PATH must prepend ~/.local/bin to the live PATH; got:\n%s", got)
	}
	// COVE_SSHENV-named vars transfer with their live values.
	if !strings.Contains(got, "GOROOT=/usr/local/go\n") || !strings.Contains(got, "GOPATH=/home/agent/go\n") {
		t.Fatalf("COVE_SSHENV vars must transfer; got:\n%s", got)
	}
	// A var not named in COVE_SSHENV must NOT leak in.
	if strings.Contains(got, "FOO=") {
		t.Fatalf("only COVE_SSHENV-named vars transfer; got:\n%s", got)
	}
	// Sealed runtime env is written (last, so it wins) and is never image-settable.
	for _, want := range []string{"CLAUDE_CONFIG_DIR=/agent-data\n", "http_proxy=http://127.0.0.1:3128\n", "NO_PROXY=localhost,127.0.0.1,::1,.internal\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("sealed env must contain %q; got:\n%s", want, got)
		}
	}
}

// roleEgressEnv is a hermetic stage for apply-role-egress.sh: a fake `id` and a
// fake `squid` (recording its args to squid.log; exit code from squid.exit) on
// PATH, and the kit/session/ceiling files in a temp dir.
type roleEgressEnv struct {
	dir, kit, session, ceiling, squidLog, squidExit, uid string
}

const (
	roleEgressKitSeed     = "# kit header\nkit-default.example\n"
	roleEgressSessionSeed = "# session header\nsession.example\n"
)

func newRoleEgressEnv(t *testing.T, ceiling string) roleEgressEnv {
	t.Helper()
	requireBash(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	e := roleEgressEnv{
		dir:       dir,
		kit:       filepath.Join(dir, "allowed_domains.kit.txt"),
		session:   filepath.Join(dir, "allowed_domains.session.txt"),
		ceiling:   filepath.Join(dir, "egress_ceiling.txt"),
		squidLog:  filepath.Join(dir, "squid.log"),
		squidExit: filepath.Join(dir, "squid.exit"),
		uid:       filepath.Join(dir, "uid"),
	}
	writeFile(t, filepath.Join(bin, "id"), "#!/bin/sh\ncat "+e.uid+"\n", 0o755)
	writeFile(t, filepath.Join(bin, "squid"), "#!/bin/sh\necho \"$@\" >> "+e.squidLog+"\nexit \"$(cat "+e.squidExit+")\"\n", 0o755)
	writeFile(t, e.uid, "0\n", 0o644)
	writeFile(t, e.squidExit, "0\n", 0o644)
	writeFile(t, e.kit, roleEgressKitSeed, 0o644)
	writeFile(t, e.session, roleEgressSessionSeed, 0o644)
	writeFile(t, e.ceiling, ceiling, 0o644)
	return e
}

func writeFile(t *testing.T, p, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// run feeds stdin to the helper; it returns the exit code and combined output.
func (e roleEgressEnv) run(t *testing.T, stdin string) (int, string) {
	t.Helper()
	cmd := exec.Command("bash", "hardening/image-files/usr/local/lib/cove/apply-role-egress.sh")
	cmd.Env = []string{
		"PATH=" + filepath.Join(e.dir, "bin") + ":/usr/bin:/bin",
		"COVE_KIT_DOMAINS_FILE=" + e.kit,
		"COVE_SESSION_DOMAINS_FILE=" + e.session,
		"COVE_EGRESS_CEILING_FILE=" + e.ceiling,
	}
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), string(out)
	}
	if err != nil {
		t.Fatalf("run apply-role-egress.sh: %v", err)
	}
	return 0, string(out)
}

func (e roleEgressEnv) squidCalls(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(e.squidLog)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// assertUnchanged fails unless the kit/session files and squid are untouched.
func (e roleEgressEnv) assertUnchanged(t *testing.T) {
	t.Helper()
	if got := read(t, e.kit); got != roleEgressKitSeed {
		t.Errorf("kit file changed:\n%s", got)
	}
	if got := read(t, e.session); got != roleEgressSessionSeed {
		t.Errorf("session file changed:\n%s", got)
	}
	if got := e.squidCalls(t); got != "" {
		t.Errorf("squid must not be called; got %q", got)
	}
}

const roleEgressCeiling = "# ceiling header\n\n.x.com\nexact.org\npkg.go.dev\n"

// Within the ceiling: the kit (active policy) file is replaced, the session delta
// is cleared, and squid is reconfigured.
func TestApplyRoleEgress_WithinCeiling(t *testing.T) {
	e := newRoleEgressEnv(t, roleEgressCeiling)
	code, out := e.run(t, "PKG.go.dev\n\nexact.org\n")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	kitFile := read(t, e.kit)
	if !strings.HasPrefix(kitFile, "#") || strings.Join(domainLines(kitFile), ",") != "pkg.go.dev,exact.org" {
		t.Fatalf("kit file = %q, want header + [pkg.go.dev exact.org] (lowercased)", kitFile)
	}
	sess := read(t, e.session)
	if !strings.HasPrefix(sess, "#") || len(domainLines(sess)) != 0 {
		t.Fatalf("session file must be cleared to its header:\n%s", sess)
	}
	if got := strings.TrimSpace(e.squidCalls(t)); got != "-k reconfigure" {
		t.Fatalf("squid calls = %q, want -k reconfigure", got)
	}
}

// A leading-dot ceiling entry covers the apex, subdomains and sub-wildcards; an
// exact entry covers only itself (never the wildcard).
func TestApplyRoleEgress_CeilingMatch(t *testing.T) {
	for _, tc := range []struct {
		domain string
		ok     bool
	}{
		{"x.com", true},
		{"a.x.com", true},
		{".a.x.com", true},
		{".x.com", true},
		{"exact.org", true},
		{".exact.org", false}, // exact never covers the wildcard
		{"a.exact.org", false},
		{"notx.com", false}, // suffix match is label-aligned
		{"other.net", false},
	} {
		t.Run(tc.domain, func(t *testing.T) {
			e := newRoleEgressEnv(t, roleEgressCeiling)
			code, out := e.run(t, tc.domain+"\n")
			if tc.ok && code != 0 {
				t.Fatalf("%s should be within the ceiling; exit %d:\n%s", tc.domain, code, out)
			}
			if !tc.ok {
				if code != 3 {
					t.Fatalf("%s should be outside the ceiling (exit 3); exit %d:\n%s", tc.domain, code, out)
				}
				e.assertUnchanged(t)
			}
		})
	}
}

// One outside-ceiling domain rejects the whole list before anything is written.
func TestApplyRoleEgress_OutsideCeilingChangesNothing(t *testing.T) {
	e := newRoleEgressEnv(t, roleEgressCeiling)
	code, out := e.run(t, "pkg.go.dev\nevil.example\n")
	if code != 3 {
		t.Fatalf("exit %d, want 3:\n%s", code, out)
	}
	if !strings.Contains(out, "apply-role-egress: evil.example is outside the kit's egress ceiling") {
		t.Fatalf("error must name the domain:\n%s", out)
	}
	e.assertUnchanged(t)
}

// Bad syntax (scheme, glob, single label, port, path, whitespace) exits 2 unchanged.
func TestApplyRoleEgress_BadSyntax(t *testing.T) {
	for _, bad := range []string{"https://x.com", "*.x.com", "x", "x.com:443", "x.com/p", "a x.com", "-a.x.com", "a..x.com", "x.com."} {
		t.Run(bad, func(t *testing.T) {
			e := newRoleEgressEnv(t, roleEgressCeiling)
			code, out := e.run(t, "pkg.go.dev\n"+bad+"\n")
			if code != 2 {
				t.Fatalf("%q: exit %d, want 2:\n%s", bad, code, out)
			}
			e.assertUnchanged(t)
		})
	}
}

// Empty stdin is a set-but-empty policy: nothing beyond the sealed base + infra.
func TestApplyRoleEgress_EmptyPolicy(t *testing.T) {
	e := newRoleEgressEnv(t, roleEgressCeiling)
	code, out := e.run(t, "")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	kitFile := read(t, e.kit)
	if !strings.HasPrefix(kitFile, "#") || len(domainLines(kitFile)) != 0 {
		t.Fatalf("kit file must be header-only:\n%s", kitFile)
	}
	if strings.TrimSpace(e.squidCalls(t)) != "-k reconfigure" {
		t.Fatalf("squid must be reconfigured; calls %q", e.squidCalls(t))
	}
}

// The helper is root-only: the agent user cannot run it to change its own egress.
func TestApplyRoleEgress_NonRootRefused(t *testing.T) {
	e := newRoleEgressEnv(t, roleEgressCeiling)
	writeFile(t, e.uid, "1000\n", 0o644)
	code, out := e.run(t, "pkg.go.dev\n")
	if code == 0 {
		t.Fatalf("non-root run must fail:\n%s", out)
	}
	e.assertUnchanged(t)
}

// A failed squid reconfigure fails loudly.
func TestApplyRoleEgress_ReconfigureFails(t *testing.T) {
	e := newRoleEgressEnv(t, roleEgressCeiling)
	writeFile(t, e.squidExit, "1\n", 0o644)
	code, out := e.run(t, "pkg.go.dev\n")
	if code == 0 {
		t.Fatalf("a failed reconfigure must exit non-zero:\n%s", out)
	}
}

// The written kit file is world-readable (squid reads it) — never a 0600 mktemp.
func TestApplyRoleEgress_FileMode(t *testing.T) {
	e := newRoleEgressEnv(t, roleEgressCeiling)
	if code, out := e.run(t, "pkg.go.dev\n"); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, p := range []string{e.kit, e.session} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o644 {
			t.Errorf("%s mode = %v, want 0644", filepath.Base(p), fi.Mode().Perm())
		}
	}
}
