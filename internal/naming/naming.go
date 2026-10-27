// Package naming is the single source of truth for the runtime docker
// resource names Jam's launcher derives: a studio's container
// (jam-studio-<id>, StudioContainer) and the volumes named after it; every
// name starts `jam-studio-`, so one studio's resources sort together and never
// collide with an unrelated docker object. Pure (no os/time), so it is
// hermetically testable.
package naming

import (
	"strings"
)

// WorkspaceVolume names the /home/agent/workspace volume for an instance, given
// that instance's container (base) name.
func WorkspaceVolume(container string) string { return container + "-workspace" }

// AgentDataVolume names the /agent-data volume for an instance, given that
// instance's container (base) name. The suffix is -agent-data (matching the
// mount), not the historical -state.
func AgentDataVolume(container string) string { return container + "-agent-data" }

// DockerVolume names the persistent /var/lib/docker cache volume for a
// docker:true instance (COV-117), given that instance's container (base) name.
// The suffix is -docker, so it sorts with the instance's other volumes and is
// removed on destroy alongside them.
func DockerVolume(container string) string { return container + "-docker" }

// SafeID is actorID with every docker-unsafe character replaced by '-'.
func SafeID(actorID string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			return r
		default:
			return '-'
		}
	}, actorID)
}

// StudioContainer names a managed studio's container from its actor id,
// docker-safe (SafeID).
func StudioContainer(actorID string) string { return "jam-studio-" + SafeID(actorID) }
