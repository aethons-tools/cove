package colima

import (
	"strings"

	"github.com/aethons-tools/cove/internal/backend"
)

// Compile-time proof colima delivers session egress (COV-39 §5). One concrete
// backend serves both the ephemeral (work/dispatch) and persistent (chat) paths.
var _ backend.SessionEgress = (*Colima)(nil)

// sessionDomainsHelper is the sealed, root-only helper the hardening layer bakes
// into every image (COV-39 §5, S2). It reads domains on stdin, rewrites
// /etc/squid/allowed_domains.session.txt, and runs `squid -k reconfigure`.
const sessionDomainsHelper = "/usr/local/lib/cove/apply-session-domains.sh"

// ApplySessionEgress delivers a session's per-class egress delta to the running
// container by `docker exec`ing the sealed helper as root, with the resolved
// domains piped on stdin — one host per line. The helper (not this op) writes the
// session allow-list and reloads squid.
//
// Domains never touch argv: they flow only on stdin, so a domain can't leak into
// process listings and the workload (SSH as the non-root agent user) has no path
// to invoke this — `docker exec` and the session file + reconfigure are root-only.
// An empty domains still execs the helper with empty stdin, clearing the session
// file so egress reverts to the baked sealed + kit lists (COV-39 §5).
func (c *Colima) ApplySessionEgress(container string, domains []string) error {
	if err := c.preflight(); err != nil {
		return err
	}
	// One host per line; empty domains yields empty stdin (a header-only clear).
	var stdin strings.Builder
	for _, d := range domains {
		stdin.WriteString(d)
		stdin.WriteByte('\n')
	}
	// -i keeps stdin open for the helper; -u root because the delivery op is
	// privileged (the agent user can't write the session file or reconfigure squid).
	return c.r.RunStdin(strings.NewReader(stdin.String()), "docker",
		dargs("exec", "-i", "-u", "root", container, sessionDomainsHelper)...)
}
