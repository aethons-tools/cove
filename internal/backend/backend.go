// Package backend defines the VM-backend abstraction: provision a VM from an
// assembled build context, reach its sshd, query state, destroy it.
package backend

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/runner"
)

type State int

const (
	StateAbsent State = iota
	StateStopped
	StateRunning
)

type WorkspaceMode int

const (
	Isolated WorkspaceMode = iota
	Shared
)

// WorkspaceMount expresses how the workspace is realized. HostPath is set iff
// Mode == Shared.
type WorkspaceMount struct {
	Mode     WorkspaceMode
	HostPath string
}

// Endpoint is a reachable sshd address.
type Endpoint struct {
	Host string
	Port int
	User string
}

// CreateContext is everything a backend needs to provision a VM.
type CreateContext struct {
	Name      string
	BuildDir  string
	Kit       string // identity for the shared image tag; "" => derive from Name.
	Workspace WorkspaceMount
}

// Instance identifies a provisioned VM. Create returns it; the CLI records it in
// the kit's state file, and connect/destroy/status drive the backend from it
// (rather than from the kit config), so a live sandbox is independent of kit edits.
type Instance struct {
	Backend   string
	Container string // backend handle (docker container name)
	Image     string // built image reference (tag)
	Workspace WorkspaceMount
}

// Backend provisions and manages VMs of one technology. Dial/GetStatus take the
// container handle from a recorded Instance; Destroy takes the whole Instance so
// it can also clean up the image.
type Backend interface {
	Create(ctx CreateContext) (Instance, error)
	Dial(container string) (Endpoint, func(), error)
	// Destroy removes the instance's container and (for the interactive instance)
	// its image. When keepVolumes is true the named volumes survive — `recreate`
	// relies on this so the saved login on /agent-data persists. When false, a
	// real `destroy` also removes the instance's named volumes.
	Destroy(inst Instance, keepVolumes bool) error
	GetStatus(container string) (State, error)
}

// DispatchOps is the ephemeral-container surface `at-cove dispatch` needs, beyond
// the persistent Create/Destroy lifecycle. A Backend may implement it.
type DispatchOps interface {
	BuildImage(buildDir, tag string) error
	RunEphemeral(image, name, label string) (Instance, error) // fresh labeled --rm no-volume container; sshd published
	Dial(container string) (Endpoint, func(), error)
	RemoveContainer(name string) error // docker rm -f; no image/volume removal
	// ScavengeLabeled force-removes labeled containers whose age (relative to now)
	// exceeds olderThan. Returns the count removed.
	ScavengeLabeled(label string, olderThan time.Duration, now time.Time) (int, error)
}

// Factory constructs a Backend bound to a Runner.
type Factory func(r runner.Runner) Backend

var registry = map[string]Factory{}

// Register adds a backend factory under name (called from backend init()s).
func Register(name string, f Factory) { registry[name] = f }

// Get returns the factory for name, or an error listing supported backends.
func Get(name string) (Factory, error) {
	if f, ok := registry[name]; ok {
		return f, nil
	}
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return nil, fmt.Errorf("unknown backend %q; supported: %s", name, strings.Join(names, ", "))
}
