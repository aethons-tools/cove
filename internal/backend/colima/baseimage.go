package colima

import (
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
// COVE_BASE_IMAGE build arg (see Build).
type dockerImg struct {
	c       *Colima
	baseArg string
}

func (d dockerImg) Build(contextDir string) (string, error) {
	// Standard build args for a kit/studio base Dockerfile:
	//   COVE_BASE_IMAGE / AT_JAM_STUDIO_BASE_IMAGE — the blessed base ref to build
	//     FROM (write `FROM ${COVE_BASE_IMAGE}`); the two are aliases so a kit can
	//     use the at-cove or the jam-studio name. The ARG defaults are only for a
	//     bare manual `docker build`.
	//   AT_JAM_STUDIO_TARGET_ARCH — "amd64" | "arm64", the build target arch.
	// -q: emit only the built image ID (a bare `sha256:<hex>`).
	out, err := d.c.r.Output("docker", dargs(
		"build", "-q",
		"--build-arg", "COVE_BASE_IMAGE="+d.baseArg,
		"--build-arg", "AT_JAM_STUDIO_BASE_IMAGE="+d.baseArg,
		"--build-arg", "AT_JAM_STUDIO_TARGET_ARCH="+runtime.GOARCH,
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
	tag := "cove-kit-base:" + strings.TrimPrefix(id, "sha256:")
	if err := d.c.r.Run("docker", dargs("tag", id, tag)...); err != nil {
		return "", err
	}
	return tag, nil
}

func (d dockerImg) Layers(ref string) ([]string, error) {
	out, err := d.c.r.Output("docker", dargs("inspect", "--format", "{{json .RootFS.Layers}}", ref)...)
	if err != nil {
		// Not present locally: pull once, then retry. (A locally-built image ID is
		// already present, so it never reaches the pull.)
		if perr := d.c.r.Run("docker", dargs("pull", ref)...); perr != nil {
			return nil, err
		}
		out, err = d.c.r.Output("docker", dargs("inspect", "--format", "{{json .RootFS.Layers}}", ref)...)
		if err != nil {
			return nil, err
		}
	}
	return baseimage.ParseLayers(out)
}

// resolveBase selects and verifies the base ref for a build, returning the value
// to pass as the hardening build's BASE arg. The gate runs only for a kit-chosen
// base; the default is a blessed cove-base-image by construction.
func (c *Colima) resolveBase(spec backend.BaseSpec) (string, error) {
	s := baseimage.Spec{
		Base:            spec.Base,
		DefaultRef:      basedigest.DefaultRef(),
		AllowUnverified: spec.AllowUnverified,
	}
	switch {
	case spec.DockerfileDir != "":
		// A studio kit's materialized Dockerfile context: the Dockerfile is at the
		// dir root (no image/ subdir convention). Takes precedence over KitDir.
		s.DockerfileDir = spec.DockerfileDir
	case spec.KitDir != "":
		imageDir := filepath.Join(spec.KitDir, "image")
		if _, err := os.Stat(filepath.Join(imageDir, "Dockerfile")); err == nil {
			s.DockerfileDir = imageDir
		}
	}
	return baseimage.Resolve(dockerImg{c: c, baseArg: s.DefaultRef}, s, basedigest.BlessedRefs(), os.Stderr)
}
