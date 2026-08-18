// Package update drives the embedded install.sh to update the on-PATH at-cove
// binaries to a GitHub release (COV-128). It deliberately *reuses* install.sh's
// resolve → download → verify (checksums.txt) → replace flow rather than
// reimplementing any of it in Go: the checksum verification the installer
// performs is a security boundary, so `at-cove update` must never replace a
// binary by a path that skips it.
//
// The package splits a pure, testable plan (Target/UpToDate/Env — argv/env and
// the already-current decision) from execution (WriteScript/ResolveLatest/Run,
// which shell out via a runner.Runner). Callers embed install.sh (see the module
// root package) and hand its bytes to WriteScript.
package update

import (
	"fmt"
	"os"
	"strings"

	"github.com/aethons-tools/cove/internal/runner"
)

// Target resolves the pinned release tag for an update: the --version flag wins,
// else install.sh's own COVE_VERSION env knob. An empty result means "no pin —
// resolve the latest release" (see ResolveLatest).
func Target(versionFlag, coveVersionEnv string) string {
	if versionFlag != "" {
		return versionFlag
	}
	return coveVersionEnv
}

// UpToDate reports whether the running version already matches target, so the
// update can no-op without downloading or replacing anything. An empty target
// (unresolved latest) is never up to date.
func UpToDate(current, target string) bool {
	target = strings.TrimSpace(target)
	return target != "" && strings.TrimSpace(current) == target
}

// Env is the extra environment for the install.sh child. A non-empty target pins
// COVE_VERSION so the exact release is fetched (and a resolved-latest install is
// not re-resolved by the script). The other knobs — BINDIR, COVE_SYSTEM,
// COVE_REPO — need no explicit entry: they are inherited from the process
// environment by the Runner, exactly as a direct `install.sh` invocation reads
// them.
func Env(target string) []string {
	if target == "" {
		return nil
	}
	return []string{"COVE_VERSION=" + target}
}

// WriteScript materializes the embedded install.sh to a private temp file and
// returns its path plus a cleanup func. The file is 0600 (only the current user
// can read the script we are about to run through bash); the returned cleanup
// removes it and is safe to call more than once.
func WriteScript(script []byte) (path string, cleanup func(), err error) {
	f, err := os.CreateTemp("", "at-cove-install-*.sh")
	if err != nil {
		return "", func() {}, err
	}
	path = f.Name()
	cleanup = func() { _ = os.Remove(path) }
	if _, err := f.Write(script); err != nil {
		f.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}

// ResolveLatest returns the latest release tag by sourcing install.sh in lib
// mode (COVE_INSTALL_LIB=1 suppresses its main) and calling the script's own
// resolve_version — reusing the installer's resolution (gh-or-curl fork, private
// repo aware) rather than reimplementing it in Go. It shells out via the Runner
// and hits the network; keep it out of --dry-run paths.
func ResolveLatest(r runner.Runner, scriptPath string) (string, error) {
	// The script path rides as a positional ($1) rather than being interpolated
	// into the -c program, so a path with shell metacharacters can never break
	// out. COVE_REPO/COVE_VERSION flow through the inherited env, as in install.sh.
	out, err := r.OutputEnv([]string{"COVE_INSTALL_LIB=1"}, "bash", "-c", `source "$1"; resolve_version`, "bash", scriptPath)
	if err != nil {
		return "", err
	}
	tag := strings.TrimSpace(out)
	if tag == "" {
		return "", fmt.Errorf("install.sh resolved an empty release tag")
	}
	return tag, nil
}

// Run executes the embedded installer (its fetch → verify checksums.txt →
// replace flow) as `bash <scriptPath>` with the pinning env, streaming the
// script's own progress to the user's terminal via the Runner.
func Run(r runner.Runner, scriptPath string, env []string) error {
	return r.RunEnv(env, "bash", scriptPath)
}
