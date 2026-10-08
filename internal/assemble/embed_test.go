package assemble

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbedsContainKeyFiles(t *testing.T) {
	for _, p := range []string{
		"hardening/Dockerfile",
		"hardening/image-files/etc/nftables.conf",
		"hardening/image-files/etc/squid/squid.conf",
		"hardening/image-files/etc/squid/allowed_domains.session.txt",
		"hardening/image-files/usr/local/lib/cove/apply-session-domains.sh",
		"hardening/image-files/usr/local/lib/cove/apply-role-egress.sh",
		"hardening/image-files/etc/systemd/system/cove-egress.service",
		"hardening/image-files/etc/systemd/system/squid.service.d/cove-egress.conf",
		"hardening/image-files/etc/ssh/sshd_config.d/cove.conf",
		"hardening/image-files/usr/local/lib/cove/seed-agent-data.sh",
	} {
		if _, err := fs.Stat(hardeningFS, p); err != nil {
			t.Errorf("hardeningFS missing %s: %v", p, err)
		}
	}
	// The replaceable user-settings default now ships in cove-base-image (COV-34),
	// not the sealed embed.
	if _, err := os.Stat(baseInitAgentData("settings.json")); err != nil {
		t.Errorf("cove-base-image missing settings.json default: %v", err)
	}
}

// baseInitAgentData resolves a file the cove-base-image seeds into
// /home/agent/.init-agent-data (the overridable startup defaults, COV-34),
// relative to this package's directory.
func baseInitAgentData(name string) string {
	return filepath.Join("..", "..", "images", "cove-base-image", "image-files", "home", "agent", ".init-agent-data", name)
}

// TestNftablesForwardChainContainsNestedEgress guards the always-on forward drop
// (COV-121). A nested container's traffic is masqueraded and *forwarded*, so it
// never traverses the output chain's skuid rule — without a forward-chain drop it
// escapes the egress lock (confirmed in the COV-120 spike). A forward chain with
// `policy drop` that only accepts established/related return traffic contains
// nested-container egress behind the same lock, while leaving same-network
// (L2-bridged) container-to-container and the daemon's proxied pulls
// (loopback/output) unaffected. The output chain must remain unchanged.
func TestNftablesForwardChainContainsNestedEgress(t *testing.T) {
	b, err := fs.ReadFile(hardeningFS, "hardening/image-files/etc/nftables.conf")
	if err != nil {
		t.Fatalf("nftables.conf not embedded: %v", err)
	}
	s := string(b)

	// The agent direct-egress lock (output chain) must be preserved unchanged.
	if !strings.Contains(s, "hook output") ||
		!strings.Contains(s, `meta skuid "proxy" tcp dport { 80, 443 } accept`) {
		t.Errorf("nftables.conf must keep the output-chain skuid-proxy egress lock; got:\n%s", s)
	}

	// The forward chain must default-drop and allow only established/related.
	i := strings.Index(s, "chain forward {")
	if i < 0 {
		t.Fatalf("nftables.conf must define a forward chain to contain nested-container egress; got:\n%s", s)
	}
	body := s[i:]
	if j := strings.Index(body, "}"); j >= 0 {
		body = body[:j]
	}
	if !strings.Contains(body, "hook forward") {
		t.Errorf("forward chain must hook forward; got:\n%s", body)
	}
	if !strings.Contains(body, "policy drop") {
		t.Errorf("forward chain must default-drop forwarded traffic; got:\n%s", body)
	}
	if !strings.Contains(body, "ct state established,related accept") {
		t.Errorf("forward chain must accept established,related return traffic; got:\n%s", body)
	}
}

// TestEntrypointExecsSystemdForDocker guards the COV-118 PID-1 branch: a docker:true
// sandbox (COVE_DOCKER=1) hands off to systemd (exec /sbin/init), which raises the
// egress lock and starts sshd + the inner rootful dockerd as ordered units; a
// non-docker sandbox keeps the script path (exec sshd), unchanged. The handoff must
// precede the non-docker tail so the docker path never runs the script bring-up.
func TestEntrypointExecsSystemdForDocker(t *testing.T) {
	b, err := fs.ReadFile(hardeningFS, "hardening/image-files/usr/local/bin/entrypoint.sh")
	if err != nil {
		t.Fatalf("entrypoint.sh not embedded: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, `[ "${COVE_DOCKER:-}" = "1" ]`) {
		t.Errorf("entrypoint must branch on COVE_DOCKER; got:\n%s", s)
	}
	initIdx := strings.Index(s, "exec /sbin/init")
	sshdIdx := strings.Index(s, "exec /usr/sbin/sshd -D")
	if initIdx < 0 {
		t.Errorf("entrypoint must exec systemd (/sbin/init) on the docker path; got:\n%s", s)
	}
	if sshdIdx < 0 {
		t.Errorf("entrypoint must still exec sshd on the non-docker path; got:\n%s", s)
	}
	if initIdx >= 0 && sshdIdx >= 0 && initIdx > sshdIdx {
		t.Errorf("the systemd branch must precede the non-docker sshd exec; got:\n%s", s)
	}
}

// TestCoveEgressUnitAndOrdering guards the systemd egress-first invariant (COV-118)
// and the squid-ownership fix: cove-egress raises the nftables drop-all (oneshot); the
// egress proxy is the base image's DISTRO squid.service (it supervises squid and keeps
// /run/squid.pid live for per-session `squid -k reconfigure`, COV-39) — we do NOT ship
// a second squid unit (that collided with squid.service and corrupted its pid file). A
// sealed squid.service.d drop-in orders squid After cove-egress; docker.service
// Requires+After BOTH so the inner dockerd cannot start before the lock is fully up.
func TestCoveEgressUnitAndOrdering(t *testing.T) {
	u, err := fs.ReadFile(hardeningFS, "hardening/image-files/etc/systemd/system/cove-egress.service")
	if err != nil {
		t.Fatalf("cove-egress.service not embedded: %v", err)
	}
	us := string(u)
	for _, want := range []string{"DefaultDependencies=no", "nft -f /etc/nftables.conf"} {
		if !strings.Contains(us, want) {
			t.Errorf("cove-egress.service must contain %q; got:\n%s", want, us)
		}
	}
	if strings.Contains(us, "ExecStart=/usr/sbin/squid") {
		t.Errorf("cove-egress.service must not start squid (the distro squid.service owns it); got:\n%s", us)
	}
	if !strings.Contains(us, "Before=") || !strings.Contains(us, "docker.service") {
		t.Errorf("cove-egress.service must be ordered Before docker.service; got:\n%s", us)
	}

	// We must NOT ship our own squid unit — it collides with the base image's distro
	// squid.service ("Squid is already running") and corrupts /run/squid.pid.
	if _, err := fs.ReadFile(hardeningFS, "hardening/image-files/etc/systemd/system/cove-squid.service"); err == nil {
		t.Error("cove-squid.service must not be shipped: it collides with the distro squid.service")
	}

	// The sealed drop-in orders the distro squid.service After + Requires the nft lock.
	sq, err := fs.ReadFile(hardeningFS, "hardening/image-files/etc/systemd/system/squid.service.d/cove-egress.conf")
	if err != nil {
		t.Fatalf("squid.service.d/cove-egress.conf not embedded: %v", err)
	}
	sqs := string(sq)
	for _, want := range []string{"After=cove-egress.service", "Requires=cove-egress.service"} {
		if !strings.Contains(sqs, want) {
			t.Errorf("squid.service drop-in must contain %q (squid up only after the nft lock); got:\n%s", want, sqs)
		}
	}

	d, err := fs.ReadFile(hardeningFS, "hardening/image-files/etc/systemd/system/docker.service.d/cove-egress.conf")
	if err != nil {
		t.Fatalf("docker.service drop-in not embedded: %v", err)
	}
	ds := string(d)
	if !strings.Contains(ds, "After=") || !strings.Contains(ds, "Requires=") {
		t.Errorf("docker.service drop-in must After+Requires the egress units; got:\n%s", ds)
	}
	for _, want := range []string{"cove-egress.service", "squid.service"} {
		if !strings.Contains(ds, want) {
			t.Errorf("docker.service drop-in must depend on %q (squid up before dockerd); got:\n%s", want, ds)
		}
	}

	df, err := fs.ReadFile(hardeningFS, "hardening/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"systemctl enable cove-egress.service", "squid.service"} {
		if !strings.Contains(string(df), want) {
			t.Errorf("hardening Dockerfile must enable %q so it runs at boot; got:\n%s", want, df)
		}
	}
}

// TestInnerDockerDaemonJSON guards the sealed inner-dockerd config (COV-118): it is
// valid JSON, routes the daemon (containerd pulls + buildkit) through squid, caps the
// build cache, and carries no key dockerd's strict parser would reject.
func TestInnerDockerDaemonJSON(t *testing.T) {
	b, err := fs.ReadFile(hardeningFS, "hardening/image-files/etc/docker/daemon.json")
	if err != nil {
		t.Fatalf("daemon.json not embedded: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("daemon.json must be valid JSON: %v", err)
	}
	// dockerd's parser rejects unknown top-level keys — guard the tempting "comment".
	if _, bad := cfg["comment"]; bad {
		t.Errorf("daemon.json must not carry a 'comment' key (dockerd rejects unknown keys)")
	}
	if _, ok := cfg["proxies"]; !ok {
		t.Errorf("daemon.json must set proxies so pulls route through squid; got:\n%s", b)
	}
	if _, ok := cfg["builder"]; !ok {
		t.Errorf("daemon.json must set a builder gc cap; got:\n%s", b)
	}
	if !strings.Contains(string(b), "127.0.0.1:3128") {
		t.Errorf("daemon.json must point the daemon proxy at squid (127.0.0.1:3128); got:\n%s", b)
	}
}

// TestAllowlistPermitsClaudeAI guards that the egress allowlist permits the
// claude.ai OAuth login endpoint (not covered by .claude.com — different TLD).
func TestAllowlistPermitsClaudeAI(t *testing.T) {
	b, err := fs.ReadFile(hardeningFS, "hardening/image-files/etc/squid/allowed_domains.txt")
	if err != nil {
		t.Fatalf("allowed_domains.txt not embedded: %v", err)
	}
	found := false
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "claude.ai" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("allowed_domains.txt must permit claude.ai for OAuth login; got:\n%s", b)
	}
}

// TestGitConfigForcesHTTPS guards that the system gitconfig rewrites SSH/git
// remotes to HTTPS, the only egress the sandbox permits (port 22 and git:// are
// dropped by the nftables rule).
func TestGitConfigForcesHTTPS(t *testing.T) {
	b, err := fs.ReadFile(hardeningFS, "hardening/image-files/etc/gitconfig")
	if err != nil {
		t.Fatalf("gitconfig not embedded: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, `[url "https://github.com/"]`) {
		t.Errorf("gitconfig must rewrite to https://github.com/; got:\n%s", s)
	}
	if !strings.Contains(s, "insteadOf = git@github.com:") {
		t.Errorf("gitconfig must rewrite git@github.com: to HTTPS; got:\n%s", s)
	}
}

// TestGitCredentialHelperWired guards that the token credential helper is shipped
// and referenced from the gitconfig (scoped to github.com over HTTPS).
func TestGitCredentialHelperWired(t *testing.T) {
	helper, err := fs.ReadFile(hardeningFS, "hardening/image-files/usr/local/bin/cove-git-credential.sh")
	if err != nil {
		t.Fatalf("credential helper not embedded: %v", err)
	}
	if !strings.Contains(string(helper), "GITHUB_TOKEN") {
		t.Errorf("credential helper must read GITHUB_TOKEN; got:\n%s", helper)
	}
	if !strings.Contains(string(helper), "GITLAB_TOKEN") {
		t.Errorf("credential helper must read GITLAB_TOKEN for GitLab kits; got:\n%s", helper)
	}
	cfg, err := fs.ReadFile(hardeningFS, "hardening/image-files/etc/gitconfig")
	if err != nil {
		t.Fatal(err)
	}
	s := string(cfg)
	if !strings.Contains(s, `[credential "https://github.com"]`) {
		t.Errorf("gitconfig must scope the helper to github.com; got:\n%s", s)
	}
	if !strings.Contains(s, "helper = /usr/local/bin/cove-git-credential.sh") {
		t.Errorf("gitconfig must reference the credential helper; got:\n%s", s)
	}
}

// TestGitCredentialHelperYieldsToken runs real `git credential fill` against the
// shipped gitconfig + helper to prove the token is supplied for github.com and
// withheld when GITHUB_TOKEN is unset. Skips if git is unavailable.
func TestGitCredentialHelperYieldsToken(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()

	helperSrc, err := fs.ReadFile(hardeningFS, "hardening/image-files/usr/local/bin/cove-git-credential.sh")
	if err != nil {
		t.Fatal(err)
	}
	helperPath := filepath.Join(dir, "cove-git-credential.sh")
	if err := os.WriteFile(helperPath, helperSrc, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgSrc, err := fs.ReadFile(hardeningFS, "hardening/image-files/etc/gitconfig")
	if err != nil {
		t.Fatal(err)
	}
	// Point the (absolute) helper path at the materialized copy for the test.
	cfg := strings.ReplaceAll(string(cfgSrc), "/usr/local/bin/cove-git-credential.sh", helperPath)
	cfgPath := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	fill := func(token string) string {
		cmd := exec.Command("git", "credential", "fill")
		// Minimal env (do NOT inherit a stray GITHUB_TOKEN); keep PATH for the
		// helper's `/usr/bin/env bash` shebang.
		env := []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + dir,
			"GIT_CONFIG_SYSTEM=" + cfgPath,
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_TERMINAL_PROMPT=0",
		}
		if token != "" {
			env = append(env, "GITHUB_TOKEN="+token)
		}
		cmd.Env = env
		cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
		out, _ := cmd.CombinedOutput() // non-zero exit is expected when no token
		return string(out)
	}

	if got := fill("tok_ABC123"); !strings.Contains(got, "password=tok_ABC123") ||
		!strings.Contains(got, "username=x-access-token") {
		t.Fatalf("helper did not supply the token for github.com:\n%s", got)
	}
	if got := fill(""); strings.Contains(got, "password=") {
		t.Fatalf("helper must withhold credentials when GITHUB_TOKEN is unset:\n%s", got)
	}
}

// TestGitCredentialHelperYieldsGitLabToken drives the shipped helper directly with
// a GitLab credential request (git passes the host on stdin) to prove it supplies
// GITLAB_TOKEN as oauth2 for the kit's GitLab host, and withholds it when the token
// is unset or the host is not the kit's GITLAB_HOST. Skips if bash is unavailable.
func TestGitCredentialHelperYieldsGitLabToken(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	src, err := fs.ReadFile(hardeningFS, "hardening/image-files/usr/local/bin/cove-git-credential.sh")
	if err != nil {
		t.Fatal(err)
	}
	helperPath := filepath.Join(dir, "cove-git-credential.sh")
	if err := os.WriteFile(helperPath, src, 0o755); err != nil {
		t.Fatal(err)
	}

	// get runs the helper with the given host on stdin and the given extra env,
	// returning its stdout (the credential answer, empty when withheld).
	get := func(host string, env ...string) string {
		cmd := exec.Command("bash", helperPath, "get")
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
		cmd.Stdin = strings.NewReader("protocol=https\nhost=" + host + "\n\n")
		out, _ := cmd.Output()
		return string(out)
	}

	host := "gitlab.example.com"
	if got := get(host, "GITLAB_HOST="+host, "GITLAB_TOKEN=glpat-XYZ"); !strings.Contains(got, "password=glpat-XYZ") ||
		!strings.Contains(got, "username=oauth2") {
		t.Fatalf("helper did not supply the GitLab token for %s:\n%s", host, got)
	}
	if got := get(host, "GITLAB_HOST="+host); strings.Contains(got, "password=") {
		t.Fatalf("helper must withhold when GITLAB_TOKEN is unset:\n%s", got)
	}
	if got := get("gitlab.other.example", "GITLAB_HOST="+host, "GITLAB_TOKEN=glpat-XYZ"); strings.Contains(got, "password=") {
		t.Fatalf("helper must not offer the token to a host that is not GITLAB_HOST:\n%s", got)
	}
}

// TestClaudeJSONPrunedAndBlended guards that the managed .claude.json holds only
// startup-experience overrides (install/identity fields are pruned, to be supplied
// by the real install) and that the Dockerfile blends the install's ~/.claude.json
// under it via jq.
func TestClaudeJSONPrunedAndBlended(t *testing.T) {
	cj, err := os.ReadFile(baseInitAgentData(".claude.json"))
	if err != nil {
		t.Fatalf(".claude.json default not found in cove-base-image: %v", err)
	}
	s := string(cj)
	for _, want := range []string{"hasCompletedOnboarding", "hasTrustDialogAccepted"} {
		if !strings.Contains(s, want) {
			t.Errorf("managed .claude.json should keep %q; got:\n%s", want, s)
		}
	}
	for _, gone := range []string{"machineID", "userID", "firstStartTime", "migrationVersion"} {
		if strings.Contains(s, gone) {
			t.Errorf("managed .claude.json must be pruned of install/identity field %q; got:\n%s", gone, s)
		}
	}

	df, err := fs.ReadFile(hardeningFS, "hardening/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	d := string(df)
	if !strings.Contains(d, "jq -s") || !strings.Contains(d, "/home/agent/.claude.json") {
		t.Errorf("Dockerfile must blend ~/.claude.json with jq; got:\n%s", d)
	}
}

// TestEntrypointStartsSSHD guards that the container's main process is sshd (the
// whole connect design reaches the VM over SSH) and that the state-volume seed
// is restart-safe (guarded by a marker, not an unconditional copy that crashes
// under `set -e` on the second boot).
func TestEntrypointStartsSSHD(t *testing.T) {
	b, err := fs.ReadFile(hardeningFS, "hardening/image-files/usr/local/bin/entrypoint.sh")
	if err != nil {
		t.Fatalf("entrypoint.sh not embedded: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, "exec /usr/sbin/sshd -D") {
		t.Errorf("entrypoint must exec sshd as the main process; got:\n%s", s)
	}
	if strings.Contains(s, "-i bash -c") {
		t.Errorf("entrypoint must not drop to an interactive bash instead of sshd")
	}
	if !strings.Contains(s, "/agent-data/.seeded") {
		t.Errorf("entrypoint must guard the state-volume seed with a marker")
	}
}

// TestEntrypointSeedsGenerically guards the sealed seeding mechanism (COV-246):
// the entrypoint hands the image's seed to seed-agent-data.sh and chowns the
// volume, before the docker/systemd handoff so both boot paths seed. What is
// seeded and refreshed is the image's business (its .refresh manifest), so the
// sealed entrypoint must not hard-code any agent file name.
func TestEntrypointSeedsGenerically(t *testing.T) {
	b, err := fs.ReadFile(hardeningFS, "hardening/image-files/usr/local/bin/entrypoint.sh")
	if err != nil {
		t.Fatalf("entrypoint.sh not embedded: %v", err)
	}
	s := string(b)
	call := "/usr/local/lib/cove/seed-agent-data.sh /home/agent/.init-agent-data /agent-data"
	if !strings.Contains(s, call) || !strings.Contains(s, "chown -R agent:agent /agent-data") {
		t.Fatalf("entrypoint must run %q and chown the volume; got:\n%s", call, s)
	}
	if strings.Index(s, call) > strings.Index(s, `[ "${COVE_DOCKER:-}" = "1" ]`) {
		t.Error("seeding must run before the COVE_DOCKER branch")
	}
	var code strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			code.WriteString(line + "\n")
		}
	}
	for _, name := range []string{"CLAUDE.md", "SANDBOX.md", "PROGRESSIVE_DISCLOSURE.md", "COLLABORATOR.md", "skills", "reference", ".claude.json", "settings.json"} {
		if strings.Contains(code.String(), name) {
			t.Errorf("the sealed entrypoint must not hard-code %q; got:\n%s", name, code.String())
		}
	}
}

// TestHardeningShipsNoAgentDocs guards decision B3 (COV-246): the agent docs
// and skills seeded into /agent-data are the kit base's (cove-base-image), so a
// kit can override them; the sealed layer ships none of them, nor the unused
// /etc/claude-code/mcp.json (agentrun generates the MCP config since COV-240).
func TestHardeningShipsNoAgentDocs(t *testing.T) {
	err := fs.WalkDir(hardeningFS, "hardening/image-files", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case strings.Contains(p, ".init-agent-data"):
			t.Errorf("hardening must not seed agent data: %s", p)
		case strings.HasSuffix(p, "SKILL.md"), !d.IsDir() && strings.HasSuffix(p, ".md"):
			t.Errorf("hardening must not ship agent docs or skills: %s", p)
		case p == "hardening/image-files/etc/claude-code/mcp.json":
			t.Errorf("the unused baked MCP config must be gone: %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestBaseImageSeedsAgentDocs guards where the agent docs live now: the base
// image's seed carries CLAUDE.md and every file it imports, the reference docs
// and skills, a .refresh manifest preserving the pre-COV-246 every-boot set,
// and its Dockerfile copies them agent-owned with docs_audit.py executable.
func TestBaseImageSeedsAgentDocs(t *testing.T) {
	claude, err := os.ReadFile(baseInitAgentData("CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range []string{"@PROGRESSIVE_DISCLOSURE.md", "@SANDBOX.md", "@COLLABORATOR.md"} {
		if !strings.Contains(string(claude), imp) {
			t.Errorf("CLAUDE.md must import %s:\n%s", imp, claude)
		}
		if _, err := os.Stat(baseInitAgentData(strings.TrimPrefix(imp, "@"))); err != nil {
			t.Errorf("CLAUDE.md import %s must resolve in the seed: %v", imp, err)
		}
	}
	for _, f := range []string{
		"reference/sandbox-kit-changes.md", "reference/sandbox-hardening-limits.md", "reference/progressive-disclosure.md",
		"skills/board-execute/SKILL.md", "skills/docs-audit/SKILL.md", "skills/docs-navigate/SKILL.md",
	} {
		if _, err := os.Stat(baseInitAgentData(f)); err != nil {
			t.Errorf("base seed missing %s: %v", f, err)
		}
	}
	if fi, err := os.Stat(baseInitAgentData("skills/docs-audit/scripts/docs_audit.py")); err != nil || fi.Mode()&0o111 == 0 {
		t.Errorf("docs_audit.py must be executable in the checkout: %v %v", fi, err)
	}
	m, err := os.ReadFile(baseInitAgentData(".refresh"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []string
	for _, line := range strings.Split(string(m), "\n") {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "#") {
			entries = append(entries, l)
		}
	}
	if got, want := strings.Join(entries, " "), "CLAUDE.md PROGRESSIVE_DISCLOSURE.md SANDBOX.md reference skills"; got != want {
		t.Errorf(".refresh = %q, want %q (runtime-owned files must never refresh)", got, want)
	}
	df, err := os.ReadFile(filepath.Join("..", "..", "images", "cove-base-image", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	d := string(df)
	copyAt, chownAt := strings.Index(d, "COPY image-files/. /."), strings.Index(d, "chown -R agent:agent /home/agent")
	if copyAt < 0 || chownAt < copyAt {
		t.Errorf("base Dockerfile must COPY the seed then chown /home/agent to agent:\n%s", d)
	}
	if !strings.Contains(d, "chmod 0755 /home/agent/.init-agent-data/skills/docs-audit/scripts/docs_audit.py") {
		t.Errorf("base Dockerfile must keep docs_audit.py executable:\n%s", d)
	}
}

// TestWorkspaceDirOwnedByAgent guards that the image creates /home/agent/workspace
// owned by the agent user. An isolated workspace is a freshly-created backend
// named volume mounted there; Docker initializes an empty volume's ownership from
// the image directory at the mount path. Without an agent-owned dir in the image,
// the fresh volume comes up root-owned and the first `chat` session's
// `at-task clone-workspace` — which runs as the agent user — fails with
// "/home/agent/workspace/.git: Permission denied" (the /agent-data volume avoids
// this only because the entrypoint chowns it at boot).
func TestWorkspaceDirOwnedByAgent(t *testing.T) {
	df, err := fs.ReadFile(hardeningFS, "hardening/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	d := string(df)
	if !strings.Contains(d, "mkdir -p /home/agent/workspace") {
		t.Errorf("Dockerfile must create /home/agent/workspace so the isolated volume mounts writable; got:\n%s", d)
	}
	if !strings.Contains(d, "chown agent:agent /home/agent/workspace") {
		t.Errorf("Dockerfile must chown /home/agent/workspace to agent so the fresh volume is agent-owned; got:\n%s", d)
	}
}

// TestConfigDirReachesEnvironment guards that CLAUDE_CONFIG_DIR points at the
// persistent volume for every ssh session, so the OAuth login and the agent
// session agree on where credentials live. It is sealed-layer-owned (the
// COVE_SSHENV redesign): apply-sshenv.sh writes it into /etc/environment, and the
// hardening Dockerfile runs that script. It is deliberately NOT an image ENV (as
// ENV it would misdirect the build-time claude install).
func TestConfigDirReachesEnvironment(t *testing.T) {
	script, err := fs.ReadFile(hardeningFS, "hardening/image-files/usr/local/lib/cove/apply-sshenv.sh")
	if err != nil {
		t.Fatalf("apply-sshenv.sh not embedded: %v", err)
	}
	if !strings.Contains(string(script), "CLAUDE_CONFIG_DIR=/agent-data") {
		t.Errorf("apply-sshenv.sh must set CLAUDE_CONFIG_DIR=/agent-data; got:\n%s", script)
	}
	df, err := fs.ReadFile(hardeningFS, "hardening/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(df), "apply-sshenv.sh") {
		t.Errorf("Dockerfile must run apply-sshenv.sh to populate /etc/environment; got:\n%s", df)
	}
}

// A shared workspace's shadow-dir volumes mount empty and root-owned; the
// entrypoint chowns each declared mountpoint to agent so uv/npm can write it on
// first boot, in the common prologue so both the systemd and sshd paths get it
// (COV-132).
func TestEntrypointChownsShadowDirs(t *testing.T) {
	b, err := fs.ReadFile(hardeningFS, "hardening/image-files/usr/local/bin/entrypoint.sh")
	if err != nil {
		t.Fatalf("entrypoint.sh not embedded: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		"${COVE_SHADOW_DIRS:-}",
		`case "$d" in /*|*/../*|../*|*/..|..) continue ;; esac`,
		"set -f", // no globbing: an entry must never be pathname-expanded (COV-132 #2)
		// chown only a real mountpoint (the fresh volume), never host content
		// showing through the shared bind (COV-132 #3).
		`if mountpoint -q "$p"; then chown agent:agent "$p"; fi`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("entrypoint must chown shadow-dirs; missing %q:\n%s", want, s)
		}
	}
	// The chown must precede the docker/systemd handoff so both paths run it.
	if strings.Index(s, "COVE_SHADOW_DIRS") > strings.Index(s, `[ "${COVE_DOCKER:-}" = "1" ]`) {
		t.Error("shadow-dir chown must run before the COVE_DOCKER branch")
	}
}

// SANDBOX.md is the plain at-cove sandbox guide. A Jam session gets the
// sandbox rules from its compiled context, so the file opens with a short guard
// telling a Jam session (CORE.md present) to ignore the rest; nothing in the
// sealed layer rewrites this kit-overridable file (COV-246). The body after the
// guard carries only the local kit path.
func TestSandboxMDIsPlainAtCoveWithJamGuard(t *testing.T) {
	b, err := os.ReadFile(baseInitAgentData("SANDBOX.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	head, body, ok := strings.Cut(s, "\n\n# ")
	if !ok {
		t.Fatalf("SANDBOX.md must open with a guard paragraph before its heading:\n%s", s)
	}
	for _, want := range []string{"/agent-data/context/CORE.md", "Jam", "session context", "ignore"} {
		if !strings.Contains(head, want) {
			t.Errorf("the guard must mention %q:\n%s", want, head)
		}
	}
	if len(head) > 300 {
		t.Errorf("the guard is the only Jam-side cost; keep it short (%d bytes)", len(head))
	}
	for _, want := range []string{".at-cove/config.yml", "at-cove recreate", "http://127.0.0.1:3128", "/agent-data/reference/sandbox-kit-changes.md", "/agent-data/reference/sandbox-hardening-limits.md"} {
		if !strings.Contains(body, want) {
			t.Errorf("SANDBOX.md missing %q:\n%s", want, s)
		}
	}
	for _, gone := range []string{"Jam", "/agent-data/context"} {
		if strings.Contains(body, gone) {
			t.Errorf("only the guard may mention Jam (%q):\n%s", gone, body)
		}
	}
}

func TestCollaboratorDefaultIsEmpty(t *testing.T) {
	b, err := os.ReadFile(baseInitAgentData("COLLABORATOR.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != "" {
		t.Fatalf("default COLLABORATOR.md must be empty, got %q", b)
	}
}
