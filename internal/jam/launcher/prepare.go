package launcher

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/aethons-tools/cove/internal/assemble"
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
	// brokered coves). Three cases: an authored context (inline tree or stored
	// tar.gz) is streamed to `docker build -` and gated; else a declared Base.Image
	// is resolved+gated; an empty ref resolves to the blessed default.
	base, err := l.resolveStudioBase(def)
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

// resolveStudioBase resolves the gated FROM-base for a studio kit. An authored
// context (an inline tree or a stored tar.gz) is produced as context tar bytes,
// pre-flighted (fail-closed) by studio.ScanContextTar, then streamed to the
// backend's `docker build -` and gated — no host dir, no extraction. Otherwise
// the declared Base.Image is resolved+gated ("" → blessed default). The gate is
// ON in both cases.
func (l *Launcher) resolveStudioBase(def KitDefinition) (string, error) {
	kind, err := def.Kit.Base.Kind()
	if err != nil {
		return "", err
	}
	switch kind {
	case studio.BaseContextFiles, studio.BaseContextZip:
		tarBytes, err := def.Kit.Base.ContextTar()
		if err != nil {
			return "", err
		}
		if err := studio.ScanContextTar(tarBytes); err != nil {
			return "", err
		}
		return l.cfg.Ops.ResolveKitBaseTar(bytes.NewReader(tarBytes))
	default: // BaseImage or BaseDefault: a declared ref, or "" → blessed default.
		return l.cfg.Ops.ResolveKitBase(def.Kit.Base.Image)
	}
}

// lockRef returns the build lock for ref's image, held; the returned func
// releases it. It is keyed on the image tag (build-digest + assembly), not on
// id@version: one kit version raised under two harnesses is two images.
func (l *Launcher) lockRef(ref KitRef) func() {
	key := l.imageTag(ref)
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
