package launcher

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/aethons-tools/cove/internal/assemble"
)

// PrepareKit builds a managed kit's image from its full definition: it assembles
// the build context and `docker build -t cove-kit:<id>-v<version>`, so a later
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

	buildDir := filepath.Join(l.cfg.BuildRoot, fmt.Sprintf("%s-v%d", ref.ID, ref.Version))
	if err := l.cfg.assemble(def, buildDir); err != nil {
		return KitStatus{State: KitPreparing, Err: err.Error()}, fmt.Errorf("prepare kit %s: assemble: %w", ref, err)
	}
	// Build on the substrate backend (context-pinned + BASE arg), so the image
	// lands in the same daemon Raise's RunEphemeral runs it from, and the
	// Dockerfile's FROM ${BASE} resolves. See backend.KitImageBuilder / COV-217.
	if _, err := l.cfg.Ops.BuildKitImage(buildDir, imageTag(ref), l.cfg.BaseImage, false); err != nil {
		return KitStatus{State: KitPreparing, Err: err.Error()}, fmt.Errorf("prepare kit %s: build: %w", ref, err)
	}
	l.cfg.Log.Info("prepared kit", "ref", ref.String(), "tag", imageTag(ref))
	return KitStatus{State: KitReady}, nil
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
	gitlabHost, _ := def.Config.GitLabHost() // "" for a non-GitLab kit
	return assemble.AssembleContext(buildDir, l.cfg.PublicKey, assemble.EgressFor(def.Config), gitlabHost)
}
