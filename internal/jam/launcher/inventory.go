package launcher

import (
	"fmt"

	"github.com/aethons-tools/cove/internal/runner"
)

// Inventory answers whether this launcher has already prepared a kit — the
// launcher's own source of truth for "do I have this?", consulted on the hot
// path of a Raise (a miss returns ErrKitNotReady so the supervisor prepares it).
// Eviction is invisible: a miss simply re-prepares.
type Inventory interface {
	Has(KitRef) (bool, error)
}

// imageTag is the docker tag a prepared Colima kit carries, e.g.
// "cove-kit:managed-v2". It is derived purely from the (id,version) content key,
// so it is the same tag PrepareKit builds and Raise runs.
func imageTag(r KitRef) string { return fmt.Sprintf("cove-kit:%s-v%d", r.ID, r.Version) }

// colimaInventory is the Colima Inventory: it asks the local docker daemon
// whether the kit's tagged image exists.
type colimaInventory struct{ r runner.Runner }

func newColimaInventory(r runner.Runner) *colimaInventory { return &colimaInventory{r: r} }

// Has reports whether the (id,version) image exists locally. `docker image
// inspect` exits non-zero when the image is absent, which we map to not-ready
// (false, nil) rather than an error — a miss is a normal, expected outcome that
// drives PrepareKit, not a fault the caller must handle.
func (c *colimaInventory) Has(ref KitRef) (bool, error) {
	if _, err := c.r.Output("docker", "image", "inspect", imageTag(ref)); err != nil {
		return false, nil
	}
	return true, nil
}
