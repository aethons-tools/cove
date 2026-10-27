// Package keys manages the SSH keypair a Studio Manager uses to reach the
// studios it raises (the colima.identity-file of jam-studio-manager's config):
// the public half is baked into each studio image's authorized_keys.
package keys

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/aethons-tools/cove/internal/runner"
)

// EnsureFile returns the public key of the ed25519 keypair whose private half
// is priv (read from priv + ".pub"), generating the pair with ssh-keygen when
// priv does not exist yet. priv's directory is created 0700 if missing.
// ssh-keygen runs quietly (-q: no randomart on stdout) with an empty stdin, so
// an overwrite prompt (priv appeared after the check) reads EOF and fails
// rather than hanging on the terminal.
func EnsureFile(r runner.Runner, priv string) ([]byte, error) {
	if err := os.MkdirAll(filepath.Dir(priv), 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(priv); errors.Is(err, os.ErrNotExist) {
		if err := r.RunStdin(strings.NewReader(""), "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "jam-launcher", "-f", priv); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return os.ReadFile(priv + ".pub")
}
