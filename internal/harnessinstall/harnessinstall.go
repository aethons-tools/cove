// Package harnessinstall generates the image's HARNESS LAYER: the build stage
// that installs a model-spec's agent CLI (at its exact version) and its
// plugins, between the kit-built base and the sealed hardening layer
// (FROM ${BASE} → harness → hardening). It is the build-time counterpart of
// internal/agentrun's runtime Harness, kept separate so the build side
// (internal/assemble, internal/studio's image digest, the Jam launcher) never
// imports the cove-side agent runner: both key on the model-spec's harness
// type, from the shared leaf internal/jam/modelspec.
//
// The install is model-spec-mediated, not a hardening concern: hardening stays
// last and never installs the harness. Placing the harness stage before
// hardening also means a CLI version bump rebuilds only the harness layer and
// what follows, not the kit base (Docker layer caching).
package harnessinstall

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
)

// payloadFS holds the harness layer's build-context files, per harness type
// (payload/<type>/...). They are SANDBOX PAYLOAD, staged into the build
// context — not configuration for this repo.
//
//go:embed all:payload
var payloadFS embed.FS

// PayloadFS returns the embedded harness payload (rooted at "payload"), hashed
// into at-cove's build identity (internal/install.AtCoveIdentity).
func PayloadFS() fs.FS { return payloadFS }

// ContextDir is the build-context directory the harness payload is staged in.
const ContextDir = "harness"

// Install is everything the harness layer bakes into an image — and so part of
// the image's identity (internal/studio.BuildDigest): the harness type, its
// exact CLI version, and its plugins. Plugins are sorted and de-duplicated by
// FromSpec so equal installs compare and hash equal.
type Install struct {
	Type    modelspec.HarnessType `json:"type"`
	Version string                `json:"version"`
	Plugins []string              `json:"plugins"`
}

// Default is the install of claude-default: the harness a full config.yml
// kit without a model-spec: block gets (assemble.HarnessFor), and the one a
// raise uses when its role delivers no spec.
func Default() Install {
	d := modelspec.Default("")
	return FromSpec(&d)
}

// FromSpec derives a model-spec's install. nil (no spec delivered) is Default.
// A spec whose version is not an exact X.Y.Z is certainly from before the
// version split (COV-242) and not yet migrated: it is read as Jam's one-time
// migration will leave it (modelspec.MigrateLegacy), so its image is the one it
// will run under. Any other spec is taken as written.
func FromSpec(s *modelspec.Spec) Install {
	if s == nil {
		return Default()
	}
	spec := *s
	if _, err := modelspec.ParseExactVersion(spec.Version); err != nil {
		spec, _ = modelspec.MigrateLegacy(spec)
	}
	in := Install{Type: spec.Type, Version: spec.Version, Plugins: []string{}}
	if spec.Claude != nil {
		in.Plugins = append(in.Plugins, spec.Claude.Plugins...)
	}
	slices.Sort(in.Plugins)
	in.Plugins = slices.Compact(in.Plugins)
	return in
}

// Validate refuses an install the harness layer cannot render safely: an
// unknown type, a version that is not an exact X.Y.Z, or a plugin id outside
// the shell-inert grammar or naming an unknown marketplace. Every value
// reaches a Dockerfile RUN line, so this is re-checked at build regardless of
// Jam's write-time validation.
func (in Install) Validate() error {
	switch in.Type {
	case modelspec.HarnessClaude:
	default:
		return fmt.Errorf("harness layer: unknown harness type %q (want %s)", in.Type, modelspec.HarnessClaude)
	}
	if _, err := modelspec.ParseExactVersion(in.Version); err != nil {
		return fmt.Errorf("harness layer: %w", err)
	}
	for _, p := range in.Plugins {
		if err := modelspec.CheckClaudePlugin(p); err != nil {
			return fmt.Errorf("harness layer: %w", err)
		}
	}
	return nil
}

// Stage stages in's payload into buildDir/ContextDir and returns the harness
// stage's Dockerfile text: it declares ARG BASE, starts `FROM ${BASE} AS
// harness`, and installs the harness. The sealed hardening Dockerfile that
// follows it builds `FROM harness`.
func Stage(buildDir string, in Install) (string, error) {
	if err := in.Validate(); err != nil {
		return "", err
	}
	dir := filepath.Join(buildDir, ContextDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := copyPayload(string(in.Type), dir); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, baselineFile), BaselineSettings(), 0o644); err != nil {
		return "", err
	}
	return header + claudeStage(in), nil
}

// header opens the harness stage. ARG BASE is declared before the first FROM
// so it applies to the base line; no default, so a bare `docker build` without
// BASE fails loud rather than silently building on the wrong floor.
const header = `# ---------------------------------------------------------------------------
# HARNESS LAYER — generated by at-cove assemble (internal/harnessinstall) from
# the model-spec: installs the agent CLI at its exact version and its plugins.
# It sits between the kit-built base and the sealed hardening layer below
# (FROM ${BASE} -> harness -> hardening), so a CLI bump rebuilds from here on.
# ---------------------------------------------------------------------------
# The base this image builds FROM. at-cove always injects --build-arg BASE (the
# resolved, provenance-gated base: a blessed cove-base-image by default, or the
# kit's image.base / built image/Dockerfile).
ARG BASE
# BASE is an at-cove-injected, digest-pinned ref, so "always tag" does not apply.
# hadolint ignore=DL3006
FROM ${BASE} AS harness
`

// copyPayload copies payload/<typ>/ into dst (scripts executable).
func copyPayload(typ, dst string) error {
	root := "payload/" + typ
	return fs.WalkDir(payloadFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := fs.ReadFile(payloadFS, p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o755)
	})
}
