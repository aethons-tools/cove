package colima

import (
	"bytes"

	"github.com/aethons-tools/cove/internal/backend"
)

// Compile-time proof colima installs Jam's SSH user CA at raise.
var _ backend.SSHCA = (*Colima)(nil)

// sshCAHelper is the sealed, root-only helper that validates one ssh-ed25519
// line on stdin and installs it as /etc/ssh/jam_user_ca.pub (sshd's
// TrustedUserCAKeys).
const sshCAHelper = "/usr/local/lib/cove/install-ssh-ca.sh"

// InstallSSHUserCA installs Jam's SSH user-CA public key in container via the
// sealed helper, as root, the key on stdin (never argv).
func (c *Colima) InstallSSHUserCA(container string, pub []byte) error {
	return c.runHelper(bytes.NewReader(pub), c.helperArgs(container, sshCAHelper))
}
