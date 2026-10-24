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

// kitMu makes a kit write's compare-with-current and push one step: an
// ensureStudioKit push, a kit-set import, and the rm/pin guards.
var kitMu sync.Mutex

func ensureStudioKit(store Store, name string, sk studio.StudioKit) (KitRef, bool, error) {
	return ensureKit(store, name, sk)
}

// validateKit is the push-time check of sk as kit name, shared by a push and
// a kit-set import: the name, the kit's own validity, no path parent (an
// import rewrites those first), the canonical JSON, the parent chain resolved
// from store, and the merged kit's prompt and notes budgets. It returns the
// canonical JSON and the chain. Every failure is a 400 WriteError. The caller
// holds kitMu, so a parent cannot move between the checks and the write.
func validateKit(store Store, name string, sk studio.StudioKit) (string, []KitLink, error) {
	if err := sk.CheckName(name); err != nil {
		return "", nil, writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	if err := sk.Validate(); err != nil {
		return "", nil, writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	if _, isPath := sk.Base.ParentPath(); isPath {
		return "", nil, writeErr(http.StatusBadRequest, "path parents resolve only in a git import (kit import/track --git)")
	}
	text, err := sk.ToJSON()
	if err != nil {
		return "", nil, writeErr(http.StatusBadRequest, "studio kit %q: %s", name, err.Error())
	}
	chain, err := ResolveKitChain(store, name, sk)
	if err != nil {
		return "", nil, writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	merged := studio.Merge(chainKits(chain))
	if err := merged.CheckPrompt(); err != nil {
		return "", nil, writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	if err := merged.CheckNotes(); err != nil {
		return "", nil, writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	return string(text), chain, nil
}

// chainParent is the parent a kit version is resolved against: the
// next-to-last link of its chain; nil for an unchained kit.
func chainParent(chain []KitLink) *KitParentRef {
	if len(chain) < 2 {
		return nil
	}
	p := chain[len(chain)-2].Ref
	return &KitParentRef{Name: p.ID, Version: p.Version}
}

// ensureKit, under kitMu, validates sk (validateKit), then reuses the current
// version when its canonical JSON is identical, else pushes a new one
// (recording the parent it was resolved against).
func ensureKit(store Store, name string, sk studio.StudioKit) (KitRef, bool, error) {
	kitMu.Lock()
	defer kitMu.Unlock()
	text, chain, err := validateKit(store, name, sk)
	if err != nil {
		return KitRef{}, false, err
	}
	digest := studio.ChainDigest(chainKits(chain), harnessinstall.Default())
	if cur, ok := LoadKitVersion(store, name, 0); ok && cur.Version != 0 && string(cur.KitYML) == text {
		return KitRef{ID: name, Version: cur.Version, Digest: digest}, true, nil
	}
	var version int
	if parent := chainParent(chain); parent != nil {
		version, err = store.PushKitMeta(name, text, KitVersionMeta{PushedAt: time.Now().UTC(), Parent: parent})
	} else {
		version, err = store.PushKit(name, text)
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
	ref, v, err := studioKitCurrent(store, name)
	if err != nil {
		return KitDefinition{}, err
	}
	def, err := kitDefinitionFromVersion(store, ref, v, h)
	if err != nil {
		return KitDefinition{}, fmt.Errorf("studio kit %q: %w", name, err)
	}
	return def, nil
}

// studioKitCurrent is StudioKitDefinition's registry lookup, without the chain:
// the current version's ref (no digest) and the version (LoadKitVersion).
func studioKitCurrent(store Store, name string) (KitRef, KitVersion, error) {
	if !studio.TagSafeName(name) {
		return KitRef{}, KitVersion{}, fmt.Errorf("kit name %q is not tag-safe", name)
	}
	v, ok := LoadKitVersion(store, name, 0)
	if !ok || v.Version == 0 {
		return KitRef{}, KitVersion{}, fmt.Errorf("studio kit %q not in registry", name)
	}
	return KitRef{ID: name, Version: v.Version}, v, nil
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
	v, ok := LoadKitVersion(store, ref.ID, ref.Version)
	if !ok {
		return KitDefinition{}, false, nil
	}
	def, err := kitDefinitionFromVersion(store, ref, v, h)
	return def, err == nil, err
}

// kitDefinitionFromVersion is the effective definition of stored version v
// (ref): its chain resolved from store, Kit = the merged chain, Chain = its
// links (the last is ref itself, at its stored version, carrying v's files),
// Ref.Digest = studio.ChainDigest(chain, h). An unchained kit is a chain of
// one and keeps its BuildDigest. A version that does not parse, or a missing
// or invalid parent, fails closed.
func kitDefinitionFromVersion(store Store, ref KitRef, v KitVersion, h harnessinstall.Install) (KitDefinition, error) {
	if v.Err != nil {
		return KitDefinition{}, fmt.Errorf("resolve kit %s: %w", ref, v.Err)
	}
	sk := v.Kit
	chain, err := ResolveKitChain(store, ref.ID, sk)
	if err != nil {
		return KitDefinition{}, fmt.Errorf("resolve kit %s: %w", ref, err)
	}
	self := &chain[len(chain)-1]
	self.Ref.Version, self.Files = ref.Version, v.Files // self is this stored version
	kits := chainKits(chain)
	ref.Digest = studio.ChainDigest(kits, h)
	merged := sk // an unchained kit is exactly as parsed, not re-merged
	if len(chain) > 1 {
		merged = studio.Merge(kits)
	}
	return KitDefinition{Ref: ref, Kit: merged, Chain: chain, Harness: h}, nil
}
