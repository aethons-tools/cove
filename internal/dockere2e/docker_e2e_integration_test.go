//go:build integration

// Package dockere2e holds the docker-in-sandbox end-to-end test (COV-122): it drives
// the REAL at-cove pipeline to install + boot a docker:true kit under Sysbox on a live
// colima VM and asserts the booted sandbox's behavior that the hermetic unit tests
// (COV-116/117/118/121/125) can only assert as text. Behind the `integration` build
// tag and gated by COVE_DOCKER_E2E, so it never runs in the hermetic `just test`.
//
// Prerequisites (see RUNBOOK.md; design §H): colima running with Sysbox installed and
// the sysbox-runc runtime registered (docs/usage/docker-in-sandbox.md), `at-cove` on
// PATH, and the active docker context pointing at that colima VM. Set COVE_DOCKER_E2E=1
// to enable. Run via `just integration-docker`.
package dockere2e_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const container = "atcove-dockere2e" // naming.Container for the plain (no-class) instance of kit "dockere2e"

func kitDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// This test lives in internal/dockere2e; the fixture kit is under testdata/.
	return filepath.Join(wd, "testdata", "dockerkit")
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

// atcove shells `at-cove <args...>` from the repo root, streaming output; it fails the
// test on a non-zero exit.
func atcove(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("at-cove", args...)
	cmd.Dir = repoRoot(t)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("at-cove %s: %v", strings.Join(args, " "), err)
	}
}

// dexec runs a shell command as root inside the booted sandbox and returns its
// combined output and error (nil on exit 0).
func dexec(t *testing.T, script string) (string, error) {
	t.Helper()
	out, err := exec.Command("docker", "exec", container, "sh", "-c", script).CombinedOutput()
	return string(out), err
}

// dexecAs runs a shell command inside the booted sandbox as user (e.g. "root",
// "agent") and returns its combined output and error (nil on exit 0).
func dexecAs(t *testing.T, user, script string) (string, error) {
	t.Helper()
	out, err := exec.Command("docker", "exec", "-u", user, container, "sh", "-c", script).CombinedOutput()
	return string(out), err
}

// domainLines returns the non-comment, non-blank lines of a squid list.
func domainLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// mustExec is dexec that fails the test unless the command exits 0.
func mustExec(t *testing.T, script string) string {
	t.Helper()
	out, err := dexec(t, script)
	if err != nil {
		t.Fatalf("in-sandbox %q failed: %v\n%s", script, err, out)
	}
	return out
}

// waitFor polls `script` (exit 0 == ready) inside the sandbox until it succeeds or the
// deadline passes.
func waitFor(t *testing.T, what, script string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := dexec(t, script); err == nil {
			return
		}
		time.Sleep(2 * time.Second)
	}
	out, _ := dexec(t, script)
	t.Fatalf("timed out after %s waiting for %s; last output:\n%s", timeout, what, out)
}

func TestDockerInSandboxE2E(t *testing.T) {
	if os.Getenv("COVE_DOCKER_E2E") == "" {
		t.Skip("set COVE_DOCKER_E2E=1 (needs a colima VM with Sysbox) to run the docker-in-sandbox e2e")
	}
	kit := kitDir(t)

	// Compile the kit (build + provenance-gate the image, write install.json) then
	// boot the sandbox under sysbox-runc. Always tear it (and its volumes) down.
	atcove(t, "install", "--project-dir", kit)
	atcove(t, "create", "--project-dir", kit)
	t.Cleanup(func() {
		cmd := exec.Command("at-cove", "destroy", "--project-dir", kit)
		cmd.Dir = repoRoot(t)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		_ = cmd.Run()
	})

	// The inner rootful dockerd comes up after the egress lock; give systemd a moment.
	waitFor(t, "inner dockerd", "docker info >/dev/null 2>&1", 90*time.Second)

	t.Run("systemd is PID 1", func(t *testing.T) {
		if got := strings.TrimSpace(mustExec(t, "ps -o comm= -p 1")); got != "systemd" {
			t.Fatalf("PID 1 = %q, want systemd", got)
		}
	})

	t.Run("egress lock ordered before docker.service", func(t *testing.T) {
		// The nftables drop (cove-egress) is ordered before docker.service via the
		// sealed drop-in, and is active by the time dockerd starts.
		after := mustExec(t, "systemctl show -p After --value docker.service")
		if !strings.Contains(after, "cove-egress.service") {
			t.Fatalf("docker.service must be ordered After cove-egress.service; got After=%s", after)
		}
		if got := strings.TrimSpace(mustExec(t, "systemctl is-active cove-egress.service")); got != "active" {
			t.Fatalf("cove-egress.service is-active = %q, want active", got)
		}
	})

	t.Run("squid up with a live pid file (COV-125)", func(t *testing.T) {
		// squid is the only permitted egress and must be reconfigurable per session:
		// apply-session-domains runs `squid -k reconfigure`, which needs /run/squid.pid.
		waitFor(t, "squid listening", "squid -k check", 30*time.Second)
		mustExec(t, "test -s /run/squid.pid")
		mustExec(t, "squid -k reconfigure")
	})

	t.Run("nft lock present and survived docker's own setup", func(t *testing.T) {
		rs := mustExec(t, "nft list ruleset")
		if !strings.Contains(rs, `skuid "proxy"`) {
			t.Fatalf("output-chain skuid-proxy egress lock missing after docker setup; ruleset:\n%s", rs)
		}
		if !strings.Contains(rs, "hook forward") || !strings.Contains(rs, "policy drop") {
			t.Fatalf("COV-121 forward-chain drop missing after docker setup; ruleset:\n%s", rs)
		}
	})

	t.Run("inner docker run/build/compose through squid", func(t *testing.T) {
		mustExec(t, "docker run --rm hello-world >/dev/null")
		mustExec(t, "printf 'FROM hello-world\\nLABEL cove.e2e=1\\n' > /tmp/Dockerfile.e2e && docker build -q -t cove-e2e-build -f /tmp/Dockerfile.e2e /tmp >/dev/null")
		compose := "mkdir -p /tmp/e2e-compose && " +
			"printf 'services:\\n  hi:\\n    image: hello-world\\n' > /tmp/e2e-compose/compose.yaml && " +
			"docker compose -f /tmp/e2e-compose/compose.yaml up --abort-on-container-exit >/dev/null 2>&1 && " +
			"docker compose -f /tmp/e2e-compose/compose.yaml down >/dev/null 2>&1"
		mustExec(t, compose)
	})

	t.Run("un-proxied nested-container egress is dropped", func(t *testing.T) {
		// The container's own (un-proxied) egress must be dropped by the forward
		// chain: the image pull rides squid, but curl to the outside without proxy
		// env must fail.
		if out, err := dexec(t, "docker run --rm curlimages/curl -m 8 -sS https://example.com >/dev/null"); err == nil {
			t.Fatalf("un-proxied nested egress to example.com should be dropped, but it succeeded:\n%s", out)
		}
	})

	t.Run("role egress replaces the kit list within the ceiling", func(t *testing.T) {
		// Jam applies a role's egress with `docker exec -u root` running the
		// sealed apply-role-egress.sh; the fixture kit's image.allowed-domains
		// (registry-1/auth/index.docker.io, .cloudfront.docker.com) is the ceiling.
		// Runs after the inner-docker subtests (which pull through the kit list)
		// and restores the kit default at the end.
		const (
			helper  = "/usr/local/lib/cove/apply-role-egress.sh"
			kitFile = "/etc/squid/allowed_domains.kit.txt"
		)
		t.Cleanup(func() {
			// Restore the kit default (the ceiling is exactly the kit's list).
			if out, err := dexecAs(t, "root", "grep -v '^#' /etc/squid/egress_ceiling.txt | grep . | "+helper); err != nil {
				t.Logf("restore kit egress default failed: %v\n%s", err, out)
			}
		})

		// The lists and the ceiling are root-owned and not writable by the agent.
		perms := mustExec(t, "stat -c '%U %a %n' /etc/squid/allowed_domains.txt /etc/squid/allowed_domains.infra.txt "+kitFile+" /etc/squid/allowed_domains.session.txt /etc/squid/egress_ceiling.txt")
		for _, line := range strings.Split(strings.TrimSpace(perms), "\n") {
			if !strings.HasPrefix(line, "root 644 ") {
				t.Fatalf("squid lists must be root-owned 0644; got:\n%s", perms)
			}
		}

		// Within the ceiling: the helper exits 0 (a real `squid -k reconfigure`)
		// and the active policy list now holds exactly the role's domain.
		if out, err := dexecAs(t, "root", "printf 'auth.docker.io\\n' | "+helper); err != nil {
			t.Fatalf("apply-role-egress within the ceiling failed: %v\n%s", err, out)
		}
		kit := mustExec(t, "cat "+kitFile)
		if got := strings.Join(domainLines(kit), ","); got != "auth.docker.io" {
			t.Fatalf("kit list after role egress = %q, want auth.docker.io:\n%s", got, kit)
		}

		// squid now denies a ceiling domain the role dropped. The 403 is squid's
		// own CONNECT refusal, so no real egress is needed (an allowed CONNECT
		// would need the internet, so it is not asserted). Poll: reconfigure is async.
		denied := "test \"$(curl -s -o /dev/null -w '%{http_connect}' -m 8 --proxy http://127.0.0.1:3128 https://registry-1.docker.io/v2/)\" = 403"
		waitFor(t, "squid to deny a dropped ceiling domain", denied, 30*time.Second)

		// Outside the ceiling: non-zero exit, and the kit list is unchanged.
		if out, err := dexecAs(t, "root", "printf 'example.com\\n' | "+helper); err == nil {
			t.Fatalf("a domain outside the ceiling must be refused, but the helper succeeded:\n%s", out)
		} else if !strings.Contains(out, "example.com is outside the kit's egress ceiling") {
			t.Fatalf("refusal must name the domain; got:\n%s", out)
		}
		if got := mustExec(t, "cat "+kitFile); got != kit {
			t.Fatalf("kit list changed after a refused apply:\n%s", got)
		}

		// The agent user cannot run it to change its own egress.
		if out, err := dexecAs(t, "agent", "printf 'index.docker.io\\n' | "+helper); err == nil {
			t.Fatalf("apply-role-egress as the agent user must fail, but it succeeded:\n%s", out)
		}
		if got := mustExec(t, "cat "+kitFile); got != kit {
			t.Fatalf("kit list changed after an agent-user run:\n%s", got)
		}
	})

	t.Run("docker cache volume survives recreate", func(t *testing.T) {
		// hello-world was pulled above; it lives in the persistent -docker volume
		// mounted at /var/lib/docker, so it must survive a recreate.
		if strings.TrimSpace(mustExec(t, "docker images -q hello-world")) == "" {
			t.Fatal("precondition: hello-world image should be present before recreate")
		}
		atcove(t, "recreate", "--project-dir", kit)
		waitFor(t, "inner dockerd after recreate", "docker info >/dev/null 2>&1", 90*time.Second)
		if strings.TrimSpace(mustExec(t, "docker images -q hello-world")) == "" {
			t.Fatal("hello-world image did not survive recreate — the -docker cache volume was not reused")
		}
	})
}
