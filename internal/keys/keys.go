// Package keys manages cove's dedicated SSH keypair.
package keys

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/aethons-tools/cove/internal/runner"
)

// Ensure returns the path to <dir>/id_ed25519 and its public key bytes,
// generating the keypair with ssh-keygen on first use.
func Ensure(r runner.Runner, dir string) (string, []byte, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	priv := filepath.Join(dir, "id_ed25519")
	if _, err := os.Stat(priv); errors.Is(err, os.ErrNotExist) {
		if err := r.Run("ssh-keygen", "-t", "ed25519", "-N", "", "-C", "cove", "-f", priv); err != nil {
			return "", nil, err
		}
	} else if err != nil {
		return "", nil, err
	}
	pub, err := os.ReadFile(priv + ".pub")
	if err != nil {
		return "", nil, err
	}
	return priv, pub, nil
}
