package launcher

import "fmt"

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

// imageChecker is the sliver of the substrate backend the inventory needs: does a
// tagged image exist on this substrate? Satisfied by backend.KitImageBuilder.
type imageChecker interface {
	HasKitImage(tag string) (bool, error)
}

// backendInventory is the default Inventory: it asks the substrate backend
// whether the kit's tagged image exists. Going through the backend (not a bare
// docker runner) means the inventory queries the SAME daemon the backend builds
// and runs on — on Colima the pinned `--context colima`, never the host's default
// (e.g. Docker Desktop). That daemon agreement is the whole point (COV-217).
type backendInventory struct{ ops imageChecker }

// Has reports whether the (id,version) image exists on the substrate.
func (b backendInventory) Has(ref KitRef) (bool, error) { return b.ops.HasKitImage(imageTag(ref)) }
