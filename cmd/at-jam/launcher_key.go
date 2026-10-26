package main

import (
	"os"

	"github.com/aethons-tools/cove/internal/keys"
	"github.com/aethons-tools/cove/internal/runner"
)

// launcherPublicKey returns the public half of the launcher's SSH identity
// (runtime.launcher.identity-file), which serve bakes into every studio image's
// authorized_keys, generating the keypair on first use.
func launcherPublicKey(identityFile string) ([]byte, error) {
	return keys.EnsureFile(runner.OS{}, identityFile)
}

// ensureKnownHostsDir creates the launcher's known-hosts directory (owner-only)
// when absent. The launcher's ssh records each studio's host key there with
// StrictHostKeyChecking=accept-new, which does not create the directory itself.
func ensureKnownHostsDir(dir string) error {
	return os.MkdirAll(dir, 0o700)
}
