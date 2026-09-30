package launcher

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/aethons-tools/cove/internal/assemble"
	"github.com/aethons-tools/cove/internal/studio"
)

// PrepareKit builds a studio kit's image from its full definition: it assembles
// the build context and `docker build -t cove-kit:<build-digest>`, so a later
// Raise carrying only the KitRef finds the tagged image. It is the response to a
// Raise that returned ErrKitNotReady.
//
// It is idempotent — a PrepareKit for an already-present tag is a no-op success —
// and de-duped: concurrent PrepareKit calls for the same ref serialize on a
// per-ref lock, so only the first builds and the rest short-circuit on the now
// present image. Colima builds synchronously, so success returns {State:
// KitReady}; the KitStatus/KitPreparing return exists so a future remote launcher
// can report an in-progress build without changing this contract.
func (l *Launcher) PrepareKit(ctx context.Context, def KitDefinition) (KitStatus, error) {
	ref := def.Ref

	// Serialize prepares of the same ref: the winner builds, the rest wait and
	// then see the image present. Different refs never contend.
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
	// brokered coves). Three cases: an authored Dockerfile+context is materialized
	// and built into a gated base; else a declared Base.Image is resolved+gated; an
	// empty ref resolves to the blessed default.
	base, err := l.resolveStudioBase(def, ref)
	if err != nil {
		return KitStatus{State: KitPreparing, Err: err.Error()}, fmt.Errorf("prepare kit %s: resolve base: %w", ref, err)
	}
	// Build on the substrate backend (context-pinned + BASE arg + kit build-args),
	// so the image lands in the same daemon Raise's RunEphemeral runs it from, and
	// the Dockerfile's FROM ${BASE} resolves. See backend.KitImageBuilder / COV-217.
	if _, err := l.cfg.Ops.BuildKitImage(buildDir, imageTag(ref), base, def.Kit.BuildArgs, false); err != nil {
		return KitStatus{State: KitPreparing, Err: err.Error()}, fmt.Errorf("prepare kit %s: build: %w", ref, err)
	}
	_, excluded := studioEgress(def.Kit, l.cfg.JamHost)
	l.cfg.Log.Info("prepared studio kit", "ref", ref.String(), "tag", imageTag(ref), "ceiling_excludes", excluded)
	return KitStatus{State: KitReady}, nil
}

// resolveStudioBase resolves the gated FROM-base for a studio kit. An authored
// Dockerfile+context is materialized (by value, no host-dir dependency) into a
// per-ref base dir and built+gated via the backend; otherwise the declared
// Base.Image is resolved+gated ("" → blessed default). The gate is ON in both cases.
func (l *Launcher) resolveStudioBase(def KitDefinition, ref KitRef) (string, error) {
	kind, err := def.Kit.Base.Kind()
	if err != nil {
		return "", err
	}
	switch kind {
	case studio.BaseContextFiles, studio.BaseContextZip:
		// Materialize the context (inline tree or zip) by value into a per-ref dir,
		// then build+gate it as the FROM-base (COV-223 path).
		baseDir := filepath.Join(l.cfg.BuildRoot, ref.Digest+"-base")
		if err := def.Kit.Base.MaterializeInto(baseDir); err != nil {
			return "", err
		}
		return l.cfg.Ops.ResolveKitBaseDockerfile(baseDir)
	default: // BaseImage or BaseDefault: a declared ref, or "" → blessed default.
		return l.cfg.Ops.ResolveKitBase(def.Kit.Base.Image)
	}
}

// lockRef returns the per-ref build lock, held; the returned func releases it.
func (l *Launcher) lockRef(ref KitRef) func() {
	key := ref.String()
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

// defaultAssemble is the real assembler: it stages the sealed hardening layer,
// the injected binaries, the kit's egress lists and the launcher's public key
// into buildDir. Everything comes from the KitDefinition (data) plus resources
// compiled into this binary — no source kit directory — so the build is a
// data-only transfer that a remote substrate could run too. Wired unless a test
// injects a seam.
func (l *Launcher) defaultAssemble(def KitDefinition, buildDir string) error {
	eg, _ := studioEgress(def.Kit, l.cfg.JamHost)
	return assemble.AssembleContext(buildDir, l.cfg.PublicKey, eg, "")
}
