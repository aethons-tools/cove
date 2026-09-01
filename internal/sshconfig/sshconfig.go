// Package sshconfig renders OpenSSH client config for reaching an at-cove
// sandbox — a ProxyCommand-based Host block whose alias stays valid across a
// `recreate` even though the container's ephemeral ssh port rotates.
package sshconfig

import (
	"fmt"
	"strings"
)

// HostParams are the inputs to a sandbox Host block.
type HostParams struct {
	Alias          string // Host alias, e.g. "cove-<container>"
	ProxyCommand   string // full ProxyCommand line (at-cove ssh-proxy ...)
	User           string // "agent"
	IdentityFile   string // <configDir>/id_ed25519
	KnownHostsFile string // <configDir>/known_hosts.d/<container>
}

// RenderHostBlock renders an OpenSSH client-config Host block. HostName is the
// alias itself: ProxyCommand supplies the transport, so HostName serves only as
// the known_hosts key — a stable alias keeps the host-key pin stable even though
// the real port rotates each boot. accept-new + the per-sandbox known_hosts file
// re-pin the regenerated key after a recreate without a MITM prompt.
func RenderHostBlock(p HostParams) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Host %s\n", p.Alias)
	fmt.Fprintf(&b, "    HostName %s\n", p.Alias)
	fmt.Fprintf(&b, "    User %s\n", p.User)
	fmt.Fprintf(&b, "    IdentityFile %s\n", p.IdentityFile)
	fmt.Fprintf(&b, "    IdentitiesOnly yes\n")
	fmt.Fprintf(&b, "    UserKnownHostsFile %s\n", p.KnownHostsFile)
	fmt.Fprintf(&b, "    StrictHostKeyChecking accept-new\n")
	fmt.Fprintf(&b, "    ProxyCommand %s\n", p.ProxyCommand)
	return b.String()
}

// GitRemoteURL is the git-over-SSH remote for the sandbox workspace, addressed
// through the Host alias so it rides the same ProxyCommand.
func GitRemoteURL(alias string) string {
	return alias + ":/home/agent/workspace"
}
