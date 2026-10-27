// Package naming is the single source of truth for the runtime docker
// resource names Jam's launcher derives: a studio's container
// (atcove-cove-<id>, CoveContainer; atcove.<manager>.cove-<id> on a remote
// Studio Manager, CoveContainerOn) and the volumes named after it. Every name
// starts with the product marker `atcove`, so it never collides with an
// unrelated docker object and every resource of one studio sorts together.
// Local names continue `atcove-`, a manager's `atcove.`: the two never meet,
// and since a manager name holds no '.', no manager's names meet another's.
// Pure (no os/time), so it is hermetically testable.
package naming

import (
	"strings"
)

// prefix is the product marker on every runtime resource name, so a cove
// object is never confused with (or colliding with) an unrelated docker object.
const prefix = "atcove"

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

// CoveContainer names a managed cove's container from its actor id: atcove-cove-<id>,
// with any docker-unsafe characters replaced by '-' so the name is always valid.
func CoveContainer(actorID string) string { return CoveContainerOn("", actorID) }

// CoveContainerOn names the container on a Studio Manager: CoveContainer's
// atcove-cove-<id> for the legacy unnamed manager (""), atcove.<manager>.cove-<id>
// for a named one, so managers sharing a docker daemon never reuse each other's
// state. The '.' separators keep the manager part unambiguous (manager names
// are [a-z0-9-], never '.'), so no manager/id pair names another's container.
func CoveContainerOn(manager, actorID string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			return r
		default:
			return '-'
		}
	}, actorID)
	if manager == "" {
		return prefix + "-cove-" + safe
	}
	return prefix + "." + manager + ".cove-" + safe
}
