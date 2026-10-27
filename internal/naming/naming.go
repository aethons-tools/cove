// Package naming is the single source of truth for the runtime docker
// resource names Jam's launcher derives: a studio's container
// (jam-studio-<id>, StudioContainer; jam-studio.<manager>.<id> on a remote
// Studio Manager, StudioContainerOn) and the volumes named after it. Every
// name starts `jam-studio`, so one studio's resources sort together and never
// collide with an unrelated docker object. Local names continue `jam-studio-`,
// a manager's `jam-studio.`: the two never meet, and since a manager name
// holds no '.', no manager's names meet another's. Pure (no os/time), so it is
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

// StudioContainerOn names the container on a Studio Manager: StudioContainer's
// jam-studio-<id> for the legacy unnamed manager (""), jam-studio.<manager>.<id>
// for a named one, so managers sharing a docker daemon never reuse each other's
// state. The '.' separators keep the manager part unambiguous (manager names
// are [a-z0-9-], never '.'), so no manager/id pair names another's container.
func StudioContainerOn(manager, actorID string) string {
	if manager == "" {
		return StudioContainer(actorID)
	}
	return "jam-studio." + manager + "." + SafeID(actorID)
}
