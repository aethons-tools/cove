package colima

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/aethons-tools/cove/internal/backend"
)

// Compile-time proof colima delivers session egress (COV-39 §5). One concrete
// backend serves both the ephemeral (work/dispatch) and persistent (chat) paths.
var _ backend.SessionEgress = (*Colima)(nil)

// Compile-time proof colima delivers a harbor role's egress policy at raise.
var _ backend.RoleEgress = (*Colima)(nil)

// sessionDomainsHelper is the sealed, root-only helper the hardening layer bakes
// into every image (COV-39 §5, S2). It reads domains on stdin, rewrites
// /etc/squid/allowed_domains.session.txt, and runs `squid -k reconfigure`.
const sessionDomainsHelper = "/usr/local/lib/cove/apply-session-domains.sh"

// roleEgressHelper is the sealed, root-only helper that replaces the active
// egress policy list (allowed_domains.kit.txt) with a role's domains, refusing
// any outside the kit's baked egress_ceiling.txt, then reconfigures squid.
const roleEgressHelper = "/usr/local/lib/cove/apply-role-egress.sh"

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
	return c.r.RunStdin(domainsStdin(domains), "docker", helperArgs(container, sessionDomainsHelper)...)
}

// ApplyRoleEgress delivers a harbor role's egress policy to a raised container by
// `docker exec`ing the sealed apply-role-egress.sh as root, domains on stdin one
// per line (never argv). The helper enforces the kit's ceiling and fails —
// changing nothing — on a domain outside it; that error surfaces here.
func (c *Colima) ApplyRoleEgress(container string, domains []string) error {
	return c.runRoleEgress(domainsStdin(domains), helperArgs(container, roleEgressHelper))
}

// ResetRoleEgress restores a running container's kit-default egress by execing
// the sealed apply-role-egress.sh as root with --kit-default (its only argument)
// and empty stdin. The helper copies the baked ceiling back into the active list.
func (c *Colima) ResetRoleEgress(container string) error {
	return c.runRoleEgress(strings.NewReader(""), append(helperArgs(container, roleEgressHelper), "--kit-default"))
}

// runRoleEgress runs one privileged apply-role-egress.sh exec.
func (c *Colima) runRoleEgress(stdin io.Reader, args []string) error {
	if err := c.preflight(); err != nil {
		return err
	}
	// Capture the helper's output rather than inheriting harbor's own stdio: its
	// rejection message names the offending domain, so it belongs in the error.
	var out bytes.Buffer
	if err := c.r.RunIO(stdin, &out, &out, "docker", args...); err != nil {
		if msg := strings.TrimSpace(out.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

// domainsStdin renders domains one host per line (empty domains → empty stdin).
func domainsStdin(domains []string) io.Reader {
	var b strings.Builder
	for _, d := range domains {
		b.WriteString(d)
		b.WriteByte('\n')
	}
	return strings.NewReader(b.String())
}

// helperArgs is the privileged exec of a sealed domains helper: -i keeps stdin
// open for the helper; -u root because the delivery op is privileged (the agent
// user can't write the squid lists or reconfigure squid).
func helperArgs(container, helper string) []string {
	return dargs("exec", "-i", "-u", "root", container, helper)
}
