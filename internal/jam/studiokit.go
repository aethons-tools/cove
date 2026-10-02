package jam

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/aethons-tools/cove/internal/studio"
)

// EnsureStudioKit records sk in the kit registry under name and returns the
// reference a raise carries: {ID: name, Version: registry version, Digest:
// studio.BuildDigest(sk)}. Idempotent — an unchanged definition reuses the
// current version; any change bumps a new monotonic one. The registry stores the
// WHOLE definition (canonical JSON, prompt included); the ref's Digest is the
// BUILD-digest (build-affecting fields only), so a prompt-only edit bumps the
// version yet keeps the image key stable.
func EnsureStudioKit(store Store, name string, sk studio.StudioKit) (KitRef, error) {
	ref, _, err := ensureStudioKit(store, name, sk)
	return ref, err
}

// kitMu makes ensureStudioKit's compare-with-current and push one step.
var kitMu sync.Mutex

func ensureStudioKit(store Store, name string, sk studio.StudioKit) (KitRef, bool, error) {
	if err := sk.CheckName(name); err != nil {
		return KitRef{}, false, writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	if err := sk.Validate(); err != nil {
		return KitRef{}, false, writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	if err := sk.CheckPrompt(); err != nil {
		return KitRef{}, false, writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	text, err := sk.ToJSON()
	if err != nil {
		return KitRef{}, false, writeErr(http.StatusBadRequest, "studio kit %q: %s", name, err.Error())
	}
	digest := studio.BuildDigest(sk)
	kitMu.Lock()
	defer kitMu.Unlock()
	if k, ok := store.GetKit(name); ok && k.Current != 0 {
		if cur, ok := store.KitConfig(name, k.Current); ok && cur == string(text) {
			return KitRef{ID: name, Version: k.Current, Digest: digest}, true, nil
		}
	}
	version, err := store.PushKit(name, string(text))
	if err != nil {
		return KitRef{}, false, fmt.Errorf("studio kit %q: push: %w", name, err)
	}
	return KitRef{ID: name, Version: version, Digest: digest}, false, nil
}

// PushStudioKit parses studio-kit YAML (or stored JSON) and records it under
// name — the one push path for the JSON admin API and the UI. It returns the
// resulting current version and whether the definition was unchanged (no new
// version). An invalid kit or name is a 400 WriteError.
func PushStudioKit(store Store, name, config string) (version int, unchanged bool, err error) {
	sk, err := studio.ParseStudioKit([]byte(config))
	if err != nil {
		return 0, false, writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	ref, unchanged, err := ensureStudioKit(store, name, sk)
	return ref.Version, unchanged, err
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
	return EnsureStudioKit(store, studio.DefaultStudioKitID, studio.DefaultStudioKit())
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
