package jam

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/studio"
)

// EnsureStudioKit records sk in the kit registry under name and returns the
// reference a raise carries: {ID: name, Version: registry version, Digest:
// studio.BuildDigest(sk, harnessinstall.Default())} — the image under
// claude-default's harness; a raise re-keys it to its role's model-spec
// (ResolveKitDefinition). Idempotent — an unchanged definition reuses the
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
	if err := sk.CheckNotes(); err != nil {
		return KitRef{}, false, writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	text, err := sk.ToJSON()
	if err != nil {
		return KitRef{}, false, writeErr(http.StatusBadRequest, "studio kit %q: %s", name, err.Error())
	}
	digest := studio.BuildDigest(sk, harnessinstall.Default())
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

// StudioKitDefinition resolves a registered studio kit's current version into
// the definition a raise under harness install h carries: its Ref (Digest =
// studio.BuildDigest(kit, h), the image key), the parsed kit and h. The kit is
// parsed and hashed once. Fails closed (no silent fallback) when name is not
// tag-safe, is absent, or its stored config is not a valid studio kit.
func StudioKitDefinition(store Store, name string, h harnessinstall.Install) (KitDefinition, error) {
	ref, text, err := studioKitCurrent(store, name)
	if err != nil {
		return KitDefinition{}, err
	}
	def, err := kitDefinitionFromConfig(ref, text, h)
	if err != nil {
		return KitDefinition{}, fmt.Errorf("studio kit %q: %w", name, err)
	}
	return def, nil
}

// studioKitCurrent is StudioKitDefinition's registry lookup, without the parse:
// the current version's ref (no digest) and stored config.
func studioKitCurrent(store Store, name string) (KitRef, string, error) {
	if !studio.TagSafeName(name) {
		return KitRef{}, "", fmt.Errorf("kit name %q is not tag-safe", name)
	}
	k, ok := store.GetKit(name)
	if !ok || k.Current == 0 {
		return KitRef{}, "", fmt.Errorf("studio kit %q not in registry", name)
	}
	text, ok := store.KitConfig(name, k.Current)
	if !ok {
		return KitRef{}, "", fmt.Errorf("studio kit %q not in registry", name)
	}
	return KitRef{ID: name, Version: k.Current}, text, nil
}

// EnsureDefaultStudioKit seeds the built-in default studio kit into the registry
// (idempotent) and returns its ref — what role.Kit == "" raises.
func EnsureDefaultStudioKit(store Store) (KitRef, error) {
	return EnsureStudioKit(store, studio.DefaultStudioKitID, studio.DefaultStudioKit())
}

// ResolveKitDefinition fetches the full studio-kit definition a KitRef names,
// for a raise under harness install h: the parsed kit, h, and the ref re-keyed
// to the image it builds under h (Digest = studio.BuildDigest(kit, h)). The
// supervisor resolves it once per raise; it serves the image key, the session
// context and an ErrKitNotReady PrepareKit. ok=false when the registry has no
// such (id, version).
func ResolveKitDefinition(store Store, ref KitRef, h harnessinstall.Install) (KitDefinition, bool, error) {
	text, ok := store.KitConfig(ref.ID, ref.Version)
	if !ok {
		return KitDefinition{}, false, nil
	}
	def, err := kitDefinitionFromConfig(ref, text, h)
	return def, err == nil, err
}

// kitDefinitionFromConfig parses a stored studio-kit config and keys ref on the
// image it builds under h (Digest = studio.BuildDigest(kit, h)).
func kitDefinitionFromConfig(ref KitRef, text string, h harnessinstall.Install) (KitDefinition, error) {
	sk, err := studio.ParseStudioKit([]byte(text))
	if err != nil {
		return KitDefinition{}, fmt.Errorf("resolve kit %s: %w", ref, err)
	}
	ref.Digest = studio.BuildDigest(sk, h)
	return KitDefinition{Ref: ref, Kit: sk, Harness: h}, nil
}
