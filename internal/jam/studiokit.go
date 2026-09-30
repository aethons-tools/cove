package jam

import (
	"fmt"

	"github.com/aethons-tools/cove/internal/studio"
)

// EnsureStudioKit records sk in the kit registry under sk.Name and returns the
// reference a raise carries: {ID: name, Version: registry version, Digest:
// studio.BuildDigest(sk)}. Idempotent — an unchanged definition reuses the
// current version; any change bumps a new monotonic one. The registry stores the
// WHOLE definition (canonical JSON, prompt included); the ref's Digest is the
// BUILD-digest (build-affecting fields only), so a prompt-only edit bumps the
// version yet keeps the image key stable.
func EnsureStudioKit(store Store, sk studio.StudioKit) (KitRef, error) {
	if err := sk.Validate(); err != nil {
		return KitRef{}, err
	}
	text, err := sk.ToJSON()
	if err != nil {
		return KitRef{}, fmt.Errorf("studio kit %q: marshal: %w", sk.Name, err)
	}
	digest := studio.BuildDigest(sk)
	if k, ok := store.GetKit(sk.Name); ok && k.Current != 0 {
		if cur, ok := store.KitConfig(sk.Name, k.Current); ok && cur == string(text) {
			return KitRef{ID: sk.Name, Version: k.Current, Digest: digest}, nil
		}
	}
	version, err := store.PushKit(sk.Name, string(text))
	if err != nil {
		return KitRef{}, fmt.Errorf("studio kit %q: push: %w", sk.Name, err)
	}
	return KitRef{ID: sk.Name, Version: version, Digest: digest}, nil
}

// StudioKitRef resolves a registered studio kit's current version into a raise
// reference. Fails closed (no silent fallback) when name is not tag-safe, is
// absent, or its stored config is not a valid studio kit.
func StudioKitRef(store Store, name string) (KitRef, error) {
	if !studio.TagSafeName(name) {
		return KitRef{}, fmt.Errorf("kit name %q is not tag-safe", name)
	}
	text, ok := store.KitConfig(name, 0) // 0 = current
	if !ok {
		return KitRef{}, fmt.Errorf("studio kit %q not in registry", name)
	}
	sk, err := studio.ParseStudioKit([]byte(text))
	if err != nil {
		return KitRef{}, fmt.Errorf("studio kit %q: %w", name, err)
	}
	k, _ := store.GetKit(name)
	return KitRef{ID: name, Version: k.Current, Digest: studio.BuildDigest(sk)}, nil
}

// EnsureDefaultStudioKit seeds the built-in default studio kit into the registry
// (idempotent) and returns its ref — what role.Kit == "" raises.
func EnsureDefaultStudioKit(store Store) (KitRef, error) {
	return EnsureStudioKit(store, studio.DefaultStudioKit())
}

// ResolveKitDefinition fetches the full studio-kit definition a KitRef names —
// the chunky payload the supervisor resolves on an ErrKitNotReady miss before
// PrepareKit. ok=false when the registry has no such (id, version).
func ResolveKitDefinition(store Store, ref KitRef) (KitDefinition, bool, error) {
	text, ok := store.KitConfig(ref.ID, ref.Version)
	if !ok {
		return KitDefinition{}, false, nil
	}
	sk, err := studio.ParseStudioKit([]byte(text))
	if err != nil {
		return KitDefinition{}, false, fmt.Errorf("resolve kit %s: %w", ref, err)
	}
	return KitDefinition{Ref: ref, Kit: sk}, true, nil
}
