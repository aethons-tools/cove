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
		"hardening/image-files/etc/systemd/system/cove-egress.service",
		"hardening/image-files/etc/systemd/system/squid.service.d/cove-egress.conf",
		"hardening/image-files/etc/ssh/sshd_config.d/cove.conf",
		"hardening/image-files/etc/claude-code/managed-settings.json",
		// Agent-instruction docs are hardening-owned (moved from overridable in
		// 63984c6) so a kit override cannot shadow them.
		"hardening/image-files/home/agent/.init-agent-data/SANDBOX.md",
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

// TestManagedSettingsNoForcedLoginMethod guards that managed settings do NOT
// force a login method: auth is env-driven, so interactive `connect` selects
// subscription OAuth explicitly (`claude auth login --claudeai`) while a
// dispatched `work` agent uses an injected ANTHROPIC_API_KEY. Forcing claudeai
// here would block (or contradict) the worker's API key.
func TestManagedSettingsNoForcedLoginMethod(t *testing.T) {
	b, err := fs.ReadFile(hardeningFS, "hardening/image-files/etc/claude-code/managed-settings.json")
	if err != nil {
		t.Fatalf("managed-settings.json not embedded: %v", err)
	}
	if strings.Contains(string(b), "forceLoginMethod") {
		t.Errorf("managed-settings.json must NOT force a login method (env-driven auth; a forced claudeai blocks the worker API key); got:\n%s", b)
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

// TestEntrypointRefreshesReferenceSet guards the every-boot refresh (COV-113): a
// rebuilt image's updated skills/reference docs/CLAUDE tree must reach an existing
// (already-seeded) sandbox, while runtime-owned seed files stay untouched. The
// first-boot full seed is unconditional-once; the refresh re-mirrors only the
// image-owned reference set on every boot.
func TestEntrypointRefreshesReferenceSet(t *testing.T) {
	b, err := fs.ReadFile(hardeningFS, "hardening/image-files/usr/local/bin/entrypoint.sh")
	if err != nil {
		t.Fatalf("entrypoint.sh not embedded: %v", err)
	}
	s := string(b)

	// (a) mirrors skills and reference (prune semantics: rm -rf + cp) and overwrites
	// the three doc files, every boot.
	if !strings.Contains(s, "for d in skills reference; do") {
		t.Errorf("entrypoint must re-mirror the skills and reference subtrees every boot; got:\n%s", s)
	}
	if !strings.Contains(s, `rm -rf "/agent-data/$d"`) {
		t.Errorf("entrypoint must prune (rm -rf) the reference subtrees so removed/renamed entries don't linger; got:\n%s", s)
	}
	if !strings.Contains(s, "for f in CLAUDE.md PROGRESSIVE_DISCLOSURE.md SANDBOX.md; do") {
		t.Errorf("entrypoint must overwrite the three CLAUDE doc files by name every boot; got:\n%s", s)
	}

	// (b) does NOT refresh the runtime-owned seed files. The refresh block names the
	// reference set explicitly and must never operate on a runtime-owned entry — so
	// inspect only the executable lines (a comment may legitimately name them to
	// document what is deliberately skipped).
	refresh := s[strings.Index(s, "Every boot:"):]
	var code strings.Builder
	for _, line := range strings.Split(refresh, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			code.WriteString(line)
			code.WriteString("\n")
		}
	}
	for _, forbidden := range []string{".claude.json", "settings.json", "plugins", "COLLABORATOR.md"} {
		if strings.Contains(code.String(), forbidden) {
			t.Errorf("every-boot refresh must NOT touch runtime-owned %q; got:\n%s", forbidden, code.String())
		}
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
