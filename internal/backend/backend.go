// Package backend defines the VM-backend abstraction: provision a VM from an
// assembled build context, reach its sshd, query state, destroy it.
package backend

import (
	"fmt"
	"sort"
	"strings"

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
	Workspace WorkspaceMount
}

// Backend provisions and manages VMs of one technology.
type Backend interface {
	Create(ctx CreateContext) error
	Dial(name string) (Endpoint, func(), error)
	Destroy(name string) error
	GetStatus(name string) (State, error)
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
