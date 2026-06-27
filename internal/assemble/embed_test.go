package assemble

import (
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
		"hardening/image-files/etc/ssh/sshd_config.d/cove.conf",
		"hardening/image-files/etc/claude-code/managed-settings.json",
	} {
		if _, err := fs.Stat(hardeningFS, p); err != nil {
			t.Errorf("hardeningFS missing %s: %v", p, err)
		}
	}
	if _, err := fs.Stat(overridableFS, "overridable/image-files/home/agent/.init-agent-data/CLAUDE.md"); err != nil {
		t.Errorf("overridableFS missing CLAUDE.md: %v", err)
	}
}

// TestManagedSettingsForceOAuth guards the policy that the non-overridable
// managed settings require subscription/OAuth login (forceLoginMethod=claudeai),
// which is what makes remote control work and blocks API-key fallback. Only
// managed settings enforce this, so the key must live in the hardening layer.
func TestManagedSettingsForceOAuth(t *testing.T) {
	b, err := fs.ReadFile(hardeningFS, "hardening/image-files/etc/claude-code/managed-settings.json")
	if err != nil {
		t.Fatalf("managed-settings.json not embedded: %v", err)
	}
	if !strings.Contains(string(b), `"forceLoginMethod": "claudeai"`) {
		t.Errorf("managed-settings.json must set forceLoginMethod=claudeai; got:\n%s", b)
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

// TestDockerfileSetsConfigDir guards that CLAUDE_CONFIG_DIR points at the
// persistent volume for every ssh session (via /etc/environment), so the OAuth
// login and the agent session agree on where credentials live.
func TestDockerfileSetsConfigDir(t *testing.T) {
	b, err := fs.ReadFile(hardeningFS, "hardening/Dockerfile")
	if err != nil {
		t.Fatalf("Dockerfile not embedded: %v", err)
	}
	if !strings.Contains(string(b), "CLAUDE_CONFIG_DIR=/agent-data") {
		t.Errorf("Dockerfile must put CLAUDE_CONFIG_DIR=/agent-data in /etc/environment; got:\n%s", b)
	}
}
