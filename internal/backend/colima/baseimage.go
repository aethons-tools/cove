package colima

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/basedigest"
	"github.com/aethons-tools/cove/internal/baseimage"
)

// dockerImg adapts colima's context-pinned docker to baseimage.Docker so the
// resolver's selection + provenance logic stays backend-agnostic and unit-tested.
// baseArg is the blessed base injected into a kit image/Dockerfile's
// JAM_STUDIO_BASE_IMAGE build arg (see Build).
type dockerImg struct {
	c       *Colima
	baseArg string
}

func (d dockerImg) Build(contextDir string) (string, error) {
	// Standard build args for a kit/studio base Dockerfile:
	//   JAM_STUDIO_BASE_IMAGE — the blessed base ref to build FROM (write
	//     `FROM ${JAM_STUDIO_BASE_IMAGE}`). The ARG default is only for a bare
	//     manual `docker build`.
	//   JAM_STUDIO_TARGET_ARCH — "amd64" | "arm64", the build target arch.
	// Jam passes these two and nothing else.
	// -q: emit only the built image ID (a bare `sha256:<hex>`). -t: a
	// temporary tag (buildingTag), so the image is never dangling.
	tmp, err := buildingTag()
	if err != nil {
		return "", err
	}
	out, err := d.c.r.Output("docker", d.c.dargs(
		"build", "-q",
		"--build-arg", "JAM_STUDIO_BASE_IMAGE="+d.baseArg,
		"--build-arg", "JAM_STUDIO_TARGET_ARCH="+runtime.GOARCH,
		"-t", tmp,
		contextDir,
	)...)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return id, nil
	}
	// A bare local image ID is not usable in `FROM ${BASE}`: `FROM sha256:<hex>`
	// is misparsed as the repo/tag `docker.io/library/sha256:<hex>`, and there is
	// no way to reference the local build cache by digest in a FROM at all. So tag
	// the just-built image under a stable, content-derived local tag and hand that
	// back — `FROM <tag>` resolves it from the local daemon (never a registry fetch,
	// since the tag exists locally), and `docker inspect` (the gate) accepts it too.
	return d.c.retag(id, tmp)
}

// buildingTag is a fresh temporary tag for a base build. A `docker build -q`
// without -t leaves its image dangling until it is tagged, and a
// dangling-image prune (the disk policy's, this manager's or another's on a
// shared VM) could remove it in between; built under this tag, it never is.
func buildingTag() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("temporary build tag: %w", err)
	}
	return "jam-kit-base:building-" + hex.EncodeToString(b[:]), nil
}

// retag tags a built base id under its stable content-derived tag and then
// removes the temporary tag tmp (an untag: the image keeps the stable one).
// A failed untag only leaves an extra tag behind.
func (c *Colima) retag(id, tmp string) (string, error) {
	tag := "jam-kit-base:" + strings.TrimPrefix(id, "sha256:")
	if err := c.r.Run("docker", c.dargs("tag", id, tag)...); err != nil {
		return "", err
	}
	_ = c.r.Run("docker", c.dargs("image", "rm", tmp)...)
	return tag, nil
}

func (d dockerImg) Layers(ref string) ([]string, error) {
	out, err := d.c.quietRunner().Output("docker", d.c.dargs("inspect", "--format", "{{json .RootFS.Layers}}", ref)...)
	if err != nil {
		// Not present locally: pull once, then retry. (A locally-built image ID is
		// already present, so it never reaches the pull.)
		if perr := d.c.r.Run("docker", d.c.dargs("pull", ref)...); perr != nil {
			return nil, err
		}
		out, err = d.c.r.Output("docker", d.c.dargs("inspect", "--format", "{{json .RootFS.Layers}}", ref)...)
		if err != nil {
			return nil, err
		}
	}
	return baseimage.ParseLayers(out)
}

// embeddedBlessedLayers is the gate's build-time layer lookup for a blessed ref
// (baseimage.Spec.BlessedLayers): the diff_ids embedded for the arch this
// backend builds studio images for — runtime.GOARCH, the JAM_STUDIO_TARGET_ARCH
// passed to every base build (colima's VM arch is the host's). A ref without
// embedded layers falls back to dockerImg.Layers. A var so tests stay hermetic
// whether or not this build embedded layers.
var embeddedBlessedLayers = func(ref string) ([]string, bool) {
	return basedigest.BlessedLayers(ref, runtime.GOARCH)
}

// resolveBase selects and verifies the base ref for a build, returning the value
// to pass as the hardening build's BASE arg. The gate runs only for a kit-chosen
// base; the default is a blessed jam-studio-base by construction.
func (c *Colima) resolveBase(spec backend.BaseSpec) (string, error) {
	s := baseimage.Spec{
		Base:            spec.Base,
		DefaultRef:      basedigest.DefaultRef(),
		AllowUnverified: spec.AllowUnverified,
		BlessedLayers:   embeddedBlessedLayers,
	}
	if spec.KitDir != "" {
		imageDir := filepath.Join(spec.KitDir, "image")
		if _, err := os.Stat(filepath.Join(imageDir, "Dockerfile")); err == nil {
			s.DockerfileDir = imageDir
		}
	}
	return baseimage.Resolve(dockerImg{c: c, baseArg: s.DefaultRef}, s, basedigest.BlessedRefs(), os.Stderr)
}

// streamDockerImg is a baseimage.Docker whose Build streams a studio kit's build
// context (a tar, gzip auto-detected) to `docker build -` on stdin instead of
// building a context directory. Reusing baseimage.Resolve keeps the provenance
// gate — layer read, blessed-set comparison, the self-explaining reject
// diagnostic — identical to every other base, specialized only in how the image
// is built. Layers delegates to the embedded dockerImg (tag/inspect are unchanged).
type streamDockerImg struct {
	dockerImg
	ctx io.Reader
}

// Build streams the stored context tar to `docker build -q … -` under a
// temporary tag (buildingTag), reading the built image id from stdout, then
// tags it under a stable local tag so it is
// usable in `FROM ${BASE}` and inspectable by the gate (see dockerImg.Build for
// why the bare sha256 id cannot be used directly).
func (d streamDockerImg) Build(_ string) (string, error) {
	tmp, err := buildingTag()
	if err != nil {
		return "", err
	}
	var out, errBuf bytes.Buffer
	if err := d.c.r.RunIO(d.ctx, &out, &errBuf, "docker", d.c.dargs(
		"build", "-q",
		"--build-arg", "JAM_STUDIO_BASE_IMAGE="+d.baseArg,
		"--build-arg", "JAM_STUDIO_TARGET_ARCH="+runtime.GOARCH,
		"-t", tmp,
		"-",
	)...); err != nil {
		// Surface docker's own build diagnostic (the failing Dockerfile line, etc.);
		// a studio context build carries no secrets in its output.
		if msg := strings.TrimSpace(errBuf.String()); msg != "" {
			if len(msg) > 4000 {
				msg = msg[len(msg)-4000:]
			}
			return "", fmt.Errorf("docker build - (streamed context): %w\n%s", err, msg)
		}
		return "", fmt.Errorf("docker build - (streamed context): %w", err)
	}
	id := strings.TrimSpace(out.String())
	if id == "" {
		return "", fmt.Errorf("docker build - (streamed context): no image id on stdout")
	}
	return d.c.retag(id, tmp)
}

// resolveBaseTar streams ctxTar to `docker build -`, tags the result, and runs
// the provenance gate (ON — no --allow-unverified for brokered coves): an
// unblessed base is a loud error with no tag returned.
func (c *Colima) resolveBaseTar(ctxTar io.Reader) (string, error) {
	base := basedigest.DefaultRef()
	d := streamDockerImg{dockerImg: dockerImg{c: c, baseArg: base}, ctx: ctxTar}
	// DockerfileDir non-empty selects the build+gate path in baseimage.Resolve; the
	// value is ignored by streamDockerImg.Build (the context comes from stdin).
	return baseimage.Resolve(d, baseimage.Spec{DockerfileDir: "-", DefaultRef: base, BlessedLayers: embeddedBlessedLayers}, basedigest.BlessedRefs(), os.Stderr)
}

// resolveBaseTarOn builds a kit-chain stage: ctxTar streams to `docker build -`
// with parentRef (the previous stage) as JAM_STUDIO_BASE_IMAGE, and the result
// runs the same provenance gate against the blessed refs (gate ON — the parent
// descends from a blessed base, so a child built on it must too). Only after the gate passes is the gated image tagged stageTag, so a
// rejected stage leaves no stage tag for HasKitBase to find.
func (c *Colima) resolveBaseTarOn(ctxTar io.Reader, parentRef, stageTag string) (string, error) {
	if parentRef == "" || stageTag == "" {
		return "", fmt.Errorf("kit stage build: parent ref and stage tag are required")
	}
	d := streamDockerImg{dockerImg: dockerImg{c: c, baseArg: parentRef}, ctx: ctxTar}
	ref, err := baseimage.Resolve(d, baseimage.Spec{DockerfileDir: "-", DefaultRef: parentRef, BlessedLayers: embeddedBlessedLayers}, basedigest.BlessedRefs(), os.Stderr)
	if err != nil {
		return "", err
	}
	if err := c.r.Run("docker", c.dargs("tag", ref, stageTag)...); err != nil {
		return "", err
	}
	return stageTag, nil
}
