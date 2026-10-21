package jam

import (
	"fmt"
	"net/http"
	"sync"
	"time"

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
	return ensureKit(store, name, sk, nil, nil)
}

// ensureStudioKitMeta is ensureStudioKit recording meta (provenance) on the new
// version. An unchanged definition still reuses the current version and leaves
// its provenance as it was. A non-nil track also becomes the kit's tracked
// source: in the same write as the new version, or on its own when unchanged.
func ensureStudioKitMeta(store Store, name string, sk studio.StudioKit, meta KitVersionMeta, track *KitSource) (KitRef, bool, error) {
	return ensureKit(store, name, sk, &meta, track)
}

// ensureKit validates sk, resolves its parent chain and checks the merged kit
// (prompt, notes), then, under kitMu, reuses the current version when its
// canonical JSON is identical, else pushes a new one (with meta when non-nil).
func ensureKit(store Store, name string, sk studio.StudioKit, meta *KitVersionMeta, track *KitSource) (KitRef, bool, error) {
	if err := sk.CheckName(name); err != nil {
		return KitRef{}, false, writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	if err := sk.Validate(); err != nil {
		return KitRef{}, false, writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	text, err := sk.ToJSON()
	if err != nil {
		return KitRef{}, false, writeErr(http.StatusBadRequest, "studio kit %q: %s", name, err.Error())
	}
	kitMu.Lock()
	defer kitMu.Unlock()
	// Resolve the chain under kitMu so a parent cannot move between the checks
	// and the push.
	chain, err := ResolveKitChain(store, name, sk)
	if err != nil {
		return KitRef{}, false, writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	merged := studio.Merge(chainKits(chain))
	if err := merged.CheckPrompt(); err != nil {
		return KitRef{}, false, writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	if err := merged.CheckNotes(); err != nil {
		return KitRef{}, false, writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	digest := studio.ChainDigest(chainKits(chain), harnessinstall.Default())
	if k, ok := store.GetKit(name); ok && k.Current != 0 {
		if cur, ok := store.KitConfig(name, k.Current); ok && cur == string(text) {
			if track != nil {
				if err := store.SetKitSource(name, track); err != nil {
					return KitRef{}, false, fmt.Errorf("studio kit %q: track: %w", name, err)
				}
			}
			return KitRef{ID: name, Version: k.Current, Digest: digest}, true, nil
		}
	}
	if len(chain) > 1 {
		parent := chain[len(chain)-2].Ref
		if meta == nil {
			meta = &KitVersionMeta{PushedAt: time.Now().UTC()}
		}
		m := *meta
		m.Parent = &KitParentRef{Name: parent.ID, Version: parent.Version}
		meta = &m
	}
	var version int
	if meta != nil {
		version, err = store.PushKitTracked(name, string(text), *meta, track)
	} else {
		version, err = store.PushKit(name, string(text))
	}
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
// studio.ChainDigest of its chain under h, the image key), the merged kit, its
// chain and h. Fails closed (no silent fallback) when name is not tag-safe, is
// absent, its stored config is not a valid studio kit, or a parent is missing
// or invalid.
func StudioKitDefinition(store Store, name string, h harnessinstall.Install) (KitDefinition, error) {
	ref, text, err := studioKitCurrent(store, name)
	if err != nil {
		return KitDefinition{}, err
	}
	def, err := kitDefinitionFromConfig(store, ref, text, h)
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
// for a raise under harness install h: the merged kit, its chain (resolved at
// the following parents' current versions), h, and the ref re-keyed to the
// image it builds under h (Digest = studio.ChainDigest). The supervisor
// resolves it once per raise; it serves the image key, the session context
// and an ErrKitNotReady PrepareKit. ok=false when the registry has no such
// (id, version).
func ResolveKitDefinition(store Store, ref KitRef, h harnessinstall.Install) (KitDefinition, bool, error) {
	text, ok := store.KitConfig(ref.ID, ref.Version)
	if !ok {
		return KitDefinition{}, false, nil
	}
	def, err := kitDefinitionFromConfig(store, ref, text, h)
	return def, err == nil, err
}

// kitDefinitionFromConfig parses the stored config of kit version ref,
// resolves its parent chain from store, and returns the effective definition:
// Kit = the merged chain, Chain = its links (the last is ref itself, at its
// stored version), Ref.Digest = studio.ChainDigest(chain, h). An unchained kit
// is a chain of one and keeps its BuildDigest. A missing or invalid parent
// fails closed.
func kitDefinitionFromConfig(store Store, ref KitRef, text string, h harnessinstall.Install) (KitDefinition, error) {
	sk, err := studio.ParseStudioKit([]byte(text))
	if err != nil {
		return KitDefinition{}, fmt.Errorf("resolve kit %s: %w", ref, err)
	}
	chain, err := ResolveKitChain(store, ref.ID, sk)
	if err != nil {
		return KitDefinition{}, fmt.Errorf("resolve kit %s: %w", ref, err)
	}
	chain[len(chain)-1].Ref.Version = ref.Version // self is this stored version
	kits := chainKits(chain)
	ref.Digest = studio.ChainDigest(kits, h)
	merged := sk // an unchained kit is exactly as parsed, not re-merged
	if len(chain) > 1 {
		merged = studio.Merge(kits)
	}
	return KitDefinition{Ref: ref, Kit: merged, Chain: chain, Harness: h}, nil
}
