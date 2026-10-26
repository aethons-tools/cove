package colima

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/aethons-tools/cove/internal/backend"
)

// Compile-time proof colima delivers a Jam role's egress policy at raise.
var _ backend.RoleEgress = (*Colima)(nil)

// roleEgressHelper is the sealed, root-only helper that replaces the active
// egress policy list (allowed_domains.kit.txt) with a role's domains, refusing
// any outside the kit's baked egress_ceiling.txt, then reconfigures squid.
const roleEgressHelper = "/usr/local/lib/cove/apply-role-egress.sh"

// ApplyRoleEgress delivers a Jam role's egress policy to a raised container by
// `docker exec`ing the sealed apply-role-egress.sh as root, domains on stdin one
// per line (never argv). The helper enforces the kit's ceiling and fails —
// changing nothing — on a domain outside it; that error surfaces here.
func (c *Colima) ApplyRoleEgress(container string, domains []string) error {
	return c.runHelper(domainsStdin(domains), c.helperArgs(container, roleEgressHelper))
}

// ResetRoleEgress restores a running container's kit-default egress by execing
// the sealed apply-role-egress.sh as root with --kit-default (its only argument)
// and empty stdin. The helper copies the baked ceiling back into the active list.
func (c *Colima) ResetRoleEgress(container string) error {
	return c.runHelper(strings.NewReader(""), append(c.helperArgs(container, roleEgressHelper), "--kit-default"))
}

// runHelper runs one privileged sealed-helper exec (apply-role-egress.sh,
// install-ssh-ca.sh), capturing its output into any error.
func (c *Colima) runHelper(stdin io.Reader, args []string) error {
	if err := c.preflight(); err != nil {
		return err
	}
	// Capture the helper's output rather than inheriting Jam's own stdio: its
	// rejection message names what it refused, so it belongs in the error.
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

// helperArgs is the privileged exec of a sealed helper: -i keeps stdin open for
// the helper; -u root because the delivery op is privileged (the agent user
// can't write the squid lists, reconfigure squid, or write /etc/ssh).
func (c *Colima) helperArgs(container, helper string) []string {
	return c.dargs("exec", "-i", "-u", "root", container, helper)
}
