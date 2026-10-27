package launcher

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/jam"
)

// Studios lists the studio containers this manager holds, from docker's
// labels: Location is the container name, ActorID the ActorLabel value. The
// local manager (no name) has no inventory and returns nil.
func (l *Launcher) Studios(ctx context.Context) ([]jam.ManagedStudio, error) {
	if l.cfg.Manager == "" {
		return nil, nil
	}
	cl, ok := l.cfg.Ops.(backend.ContainerLister)
	if !ok {
		return nil, fmt.Errorf("backend cannot list containers (required for the studio inventory)")
	}
	byName, err := cl.ListContainers(ActorLabel, ManagerLabel+"="+l.cfg.Manager)
	if err != nil {
		return nil, err
	}
	out := make([]jam.ManagedStudio, 0, len(byName))
	for name, actor := range byName {
		out = append(out, jam.ManagedStudio{ActorID: actor, Location: name})
	}
	slices.SortFunc(out, func(a, b jam.ManagedStudio) int {
		if c := strings.Compare(a.ActorID, b.ActorID); c != 0 {
			return c
		}
		return strings.Compare(a.Location, b.Location)
	})
	return out, nil
}
