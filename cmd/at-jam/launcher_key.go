package main

import (
	"github.com/aethons-tools/cove/internal/keys"
	"github.com/aethons-tools/cove/internal/runner"
)

// launcherPublicKey returns the public half of the launcher's SSH identity
// (runtime.launcher.identity-file), which serve bakes into every studio image's
// authorized_keys, generating the keypair on first use.
func launcherPublicKey(identityFile string) ([]byte, error) {
	return keys.EnsureFile(runner.OS{}, identityFile)
}
