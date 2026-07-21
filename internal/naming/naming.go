// Package naming is the single source of truth for every runtime docker
// resource name cove derives — image, container, volumes, and the ephemeral
// worker container (COV-77). Every name shares the product prefix `atcove-` and
// the structure `atcove-{kit}-{class}-{type}`, so a name can never collide with
// an unrelated docker object and every resource of one instance sorts together.
//
// It is deliberately pure (no os/time): the impure inputs a worker name needs
// (pid, nanotime) are passed in by the caller, keeping the whole package
// hermetically testable. It does NOT own the secret-bucket key (the bare kit
// name) or the interactive session label — those are not docker resources.
package naming

import "fmt"

// prefix is the product marker on every runtime resource name, so an at-cove
// object is never confused with (or colliding with) an unrelated docker object.
const prefix = "atcove"

// instance is the shared base for an instance's runtime resources:
// atcove-{kit} for a plain (no-collaborator) instance, atcove-{kit}-{class} for
// a collaborator instance. Container is this base; the volumes append a type.
func instance(kit, class string) string {
	if class == "" {
		return prefix + "-" + kit
	}
	return prefix + "-" + kit + "-" + class
}

// Image is the docker image tag for a kit. It is kit-scoped and carries NO
// class: every collaborator of a kit shares one built image (COV-77).
func Image(kit string) string { return prefix + "-" + kit }

// Container is the docker container name for an instance: atcove-{kit} for a
// plain instance, atcove-{kit}-{class} for a collaborator.
func Container(kit, class string) string { return instance(kit, class) }

// WorkspaceVolume names the /home/agent/workspace volume for an instance, given
// that instance's container (base) name.
func WorkspaceVolume(container string) string { return container + "-workspace" }

// AgentDataVolume names the /agent-data volume for an instance, given that
// instance's container (base) name. The suffix is -agent-data (matching the
// mount), not the historical -state.
func AgentDataVolume(container string) string { return container + "-agent-data" }

// WorkerContainer names an ephemeral `at-cove work` container. It carries the
// atcove- prefix like every other resource, plus a pid+nanotime suffix so
// concurrent dispatches of one kit (even from separate processes) never collide.
func WorkerContainer(kit string, pid int, nano int64) string {
	return fmt.Sprintf("%s-work-%s-%d-%d", prefix, kit, pid, nano)
}
