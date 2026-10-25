package launcher

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sync"

	"github.com/aethons-tools/cove/internal/assemble"
	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/basedigest"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/studio"
)

// PrepareKit builds a studio kit's image from its full definition: it assembles
// the build context and `docker build -t cove-kit:<build-digest>-<asm>`, so a later
// Raise carrying only the KitRef finds the tagged image. It is the response to a
// Raise that returned ErrKitNotReady.
//
// It is idempotent — a PrepareKit for an already-present tag is a no-op success —
// and de-duped: concurrent PrepareKit calls for the same image serialize on a
// per-image-tag lock, so only the first builds and the rest short-circuit on the now
// present image. Colima builds synchronously, so success returns {State:
// KitReady}; the KitStatus/KitPreparing return exists so a future remote launcher
// can report an in-progress build without changing this contract.
func (l *Launcher) PrepareKit(ctx context.Context, def KitDefinition) (KitStatus, error) {
	ref := def.Ref

	// Serialize prepares of the same image: the winner builds, the rest wait and
	// then see the image present. Different images never contend.
	unlock := l.lockRef(ref)
	defer unlock()

	// Idempotent: already prepared → no-op success. Checked under the lock so a
	// queued duplicate observes the winner's freshly-built image.
	if ok, err := l.inv.Has(ref); err != nil {
		return KitStatus{State: KitPreparing, Err: err.Error()}, fmt.Errorf("prepare kit %s: inventory: %w", ref, err)
	} else if ok {
		return KitStatus{State: KitReady}, nil
	}

	buildDir := filepath.Join(l.cfg.BuildRoot, ref.Digest)
	if err := l.cfg.assemble(def, buildDir); err != nil {
		return KitStatus{State: KitPreparing, Err: err.Error()}, fmt.Errorf("prepare kit %s: assemble: %w", ref, err)
	}
	// Resolve the FROM-base on the substrate (gate ON — no --allow-unverified for
	// brokered coves). Three cases: a kit tree with a Dockerfile is streamed to
	// `docker build -` and gated; else a declared Base.Image is resolved+gated;
	// an empty ref resolves to the blessed default.
	base, err := l.resolveStudioBase(l.cfg.Ops, def, "")
	if err != nil {
		return KitStatus{State: KitPreparing, Err: err.Error()}, fmt.Errorf("prepare kit %s: resolve base: %w", ref, err)
	}
	// Build on the substrate backend (context-pinned + BASE arg + kit build-args),
	// so the image lands in the same daemon Raise's RunEphemeral runs it from, and
	// the Dockerfile's FROM ${BASE} resolves. See backend.KitImageBuilder / COV-217.
	if _, err := l.cfg.Ops.BuildKitImage(buildDir, l.imageTag(ref), base, def.Kit.BuildArgs, false); err != nil {
		return KitStatus{State: KitPreparing, Err: err.Error()}, fmt.Errorf("prepare kit %s: build: %w", ref, err)
	}
	_, excluded := studioEgress(def.Kit, l.cfg.JamHost)
	l.cfg.Log.Info("prepared studio kit", "ref", ref.String(), "tag", l.imageTag(ref), "asm", shortAsm(l.asm), "ceiling_excludes", excluded,
		"harness", string(def.Harness.Type), "harness_version", def.Harness.Version, "plugins", len(def.Harness.Plugins))
	return KitStatus{State: KitReady}, nil
}

// resolveStudioBase resolves the gated FROM-base for a studio kit on ops. An
// unchained kit (len(def.Chain) <= 1) resolves its own base by resolveOwnBase. A
// chain resolves its root that way, then builds each later link's own context as a
// stage FROM the previous ref (resolveChainStage); a link with no own context
// (studio.BaseKit) leaves the ref unchanged. The gate is ON for every stage
// built. The returned ref is the one BuildKitImage builds FROM, so the harness
// and hardening layers are applied exactly once, on top of the whole chain.
// top, when set, is a kit draft build's own stage tag: the chain's last link
// (an unchained kit's own base included) builds under it instead of its kit
// version's stage tag (BuildKitDraft).
func (l *Launcher) resolveStudioBase(ops backend.KitImageBuilder, def KitDefinition, top string) (string, error) {
	if len(def.Chain) <= 1 {
		// Guard on the chain's own link, not def.Kit.Base: Merge clears
		// Base.Kit, so the merged kit cannot show a named parent.
		own := jam.KitLink{Ref: def.Ref, Kit: def.Kit}
		if len(def.Chain) == 1 {
			own = def.Chain[0]
		}
		return l.resolveOwnBase(ops, own, top)
	}
	root := def.Chain[0]
	ref, err := l.resolveOwnBase(ops, root, "")
	if err != nil {
		return "", fmt.Errorf("kit %s: %w", root.Ref, err)
	}
	links := def.Chain[1:]
	for i, link := range links {
		stage := ""
		if i == len(links)-1 {
			stage = top
		}
		if ref, err = l.resolveChainStage(ops, link, ref, stage); err != nil {
			return "", fmt.Errorf("kit %s: %w", link.Ref, err)
		}
	}
	return ref, nil
}

// resolveOwnBase resolves a parentless link's base on ops. A kit whose tree holds a
// Dockerfile streams its directory (minus kit.yml) to the backend's `docker
// build -` FROM the blessed base, gated — no host dir, no extraction.
// Otherwise the declared Base.Image is resolved+gated ("" → blessed default).
// A base naming a parent kit is refused (fail closed): only a resolved chain
// may build it, on its parent's stage, never on the blessed base alone.
// top, when set, is a kit draft build's own stage tag: the Dockerfile's stage
// is built FROM the blessed default, gated the same, and tagged top
// (ResolveKitBaseTarOn), so the image builds FROM the draft's stage tag and
// not the content-id ref a kit version's base resolves to.
func (l *Launcher) resolveOwnBase(ops backend.KitImageBuilder, link jam.KitLink, top string) (string, error) {
	b := link.Kit.Base
	if b.HasParent() {
		return "", fmt.Errorf("base names parent kit %q, which the kit chain does not resolve", b.Kit)
	}
	if link.Kit.BuildContext != "" {
		r, err := contextReader(link)
		if err != nil {
			return "", err
		}
		if top != "" {
			return ops.ResolveKitBaseTarOn(r, basedigest.DefaultRef(), top)
		}
		return ops.ResolveKitBaseTar(r)
	}
	return ops.ResolveKitBase(b.Image)
}

// resolveChainStage resolves one non-root chain link on parentRef (the previous
// stage), on ops. A link with no build context is the parent's stage as-is. Otherwise
// its stage is cached under kitStageTag(l.asm, parentRef, link.Kit): a present
// stage tag was applied only after that stage's gate passed, so it is reused;
// a missing one is built by streaming the link's context (its tree minus
// kit.yml) FROM parentRef, gated against the blessed base, and tagged. The
// stage lock keeps two concurrent prepares sharing a parent from building the
// same stage twice. A draft's own stage (top set) is never looked up or
// cached under a kit version's stage tag: it is built, gated, and tagged top.
func (l *Launcher) resolveChainStage(ops backend.KitImageBuilder, link jam.KitLink, parentRef, top string) (string, error) {
	if !link.Kit.Base.HasParent() {
		return "", fmt.Errorf("a non-root chain link must name its parent kit")
	}
	if link.Kit.BuildContext == "" {
		return parentRef, nil
	}
	if top != "" {
		r, err := contextReader(link)
		if err != nil {
			return "", err
		}
		return ops.ResolveKitBaseTarOn(r, parentRef, top)
	}
	stage, err := kitStageTag(l.asm, parentRef, link.Kit)
	if err != nil {
		return "", err
	}
	unlock := l.lockKey(stage)
	defer unlock()
	if ok, err := ops.HasKitBase(stage); err != nil {
		return "", fmt.Errorf("stage %s: %w", stage, err)
	} else if ok {
		return stage, nil
	}
	r, err := contextReader(link)
	if err != nil {
		return "", err
	}
	return ops.ResolveKitBaseTarOn(r, parentRef, stage)
}

// contextReader is link's build context, its tree minus kit.yml, as a
// deterministic tar (kittree.ContextTar), buffered (a tree is at most 64 MiB).
// A link with a build context but no files loaded fails closed.
func contextReader(link jam.KitLink) (io.Reader, error) {
	if link.Files == nil {
		return nil, fmt.Errorf("kit %s has a Dockerfile but its files are not loaded", link.Ref)
	}
	var buf bytes.Buffer
	if err := kittree.ContextTar(&buf, link.Files); err != nil {
		return nil, err
	}
	return &buf, nil
}

// kitStageTag is the local tag a chain stage is cached under:
// cove-kit-base:<first 32 hex of sha256(asm, NUL, parentRef, NUL, JSON(own
// base)[, NUL, build-context hash])>. The build-context hash is appended only
// when the kit has one (a stage is only built for a kit with one), and an
// edit to kit.yml alone (prompt, notes) reuses the stage while an edit to
// any other file of the tree makes a new one. asm is the launcher's assembly
// fingerprint (it covers the blessed base ref), so a blessed-set rotation
// orphans stages gated against the old set and they are rebuilt and re-gated.
// Otherwise it is deterministic in the parent stage and the link's own Base
// and build context, so siblings share their parent's stage, a pinned parent's
// stage is stable across the parent's later pushes, and a following parent's
// new context yields new stages below it. 32 hex chars never collide with the
// 64-hex image-id tags under the same repository.
func kitStageTag(asm, parentRef string, sk studio.StudioKit) (string, error) {
	js, err := json.Marshal(sk.Base)
	if err != nil {
		return "", fmt.Errorf("stage digest: %w", err)
	}
	h := sha256.New()
	h.Write([]byte(asm))
	h.Write([]byte{0})
	h.Write([]byte(parentRef))
	h.Write([]byte{0})
	h.Write(js)
	if sk.BuildContext != "" {
		h.Write([]byte{0})
		h.Write([]byte(sk.BuildContext))
	}
	return "cove-kit-base:" + hex.EncodeToString(h.Sum(nil))[:32], nil
}

// lockRef returns the build lock for ref's image, held; the returned func
// releases it. It is keyed on the image tag (build-digest + assembly), not on
// id@version: one kit version raised under two harnesses is two images.
func (l *Launcher) lockRef(ref KitRef) func() { return l.lockKey(l.imageTag(ref)) }

// lockKey returns the build lock named key, held; the returned func releases
// it. Image tags and chain-stage tags share the map without colliding (distinct
// repositories).
func (l *Launcher) lockKey(key string) func() {
	l.mu.Lock()
	if l.inflight == nil {
		l.inflight = map[string]*sync.Mutex{}
	}
	m := l.inflight[key]
	if m == nil {
		m = &sync.Mutex{}
		l.inflight[key] = m
	}
	l.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// defaultAssemble is the real assembler: it stages the harness layer (the
// definition's Harness: the raising role's model-spec CLI version + plugins),
// the sealed hardening layer, the injected binaries, the kit's egress lists and
// MCP servers, and the launcher's public key into buildDir. Everything comes from the KitDefinition (data) plus resources
// compiled into this binary — no source kit directory — so the build is a
// data-only transfer that a remote substrate could run too. Wired unless a test
// injects a seam.
func (l *Launcher) defaultAssemble(def KitDefinition, buildDir string) error {
	eg, _ := studioEgress(def.Kit, l.cfg.JamHost)
	return assemble.AssembleContext(buildDir, l.cfg.PublicKey, eg, "", def.Kit.MCPServers, def.Harness)
}
