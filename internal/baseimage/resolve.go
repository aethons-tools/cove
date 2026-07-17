package baseimage

import (
	"fmt"
	"io"
)

// Docker is the minimal execution surface the resolver needs. The backend
// implements it (pinned to its docker context); the resolver stays I/O-free so
// its selection + gate logic is unit-tested against a fake.
type Docker interface {
	// Build builds the image whose context is contextDir (a kit image/ dir with a
	// Dockerfile) and returns the resulting image ID (sha256:…).
	Build(contextDir string) (string, error)
	// Layers returns ref's OCI rootfs diff_ids (pulling ref if needed).
	Layers(ref string) ([]string, error)
}

// Spec is the base-selection input for one build. Exactly the first non-empty of
// DockerfileDir / Base / DefaultRef is used (mutual exclusion of the first two is
// enforced earlier, in kit.ValidateImageSource).
type Spec struct {
	DockerfileDir   string // kit image/ dir to build as the base; wins if set
	Base            string // config.yml image.base; used if DockerfileDir == ""
	DefaultRef      string // cove-base-image@<blessed[0]>; used if both empty
	AllowUnverified bool   // --allow-unverified-base: downgrade a failed gate to a warning
}

// Resolve selects the base image the sealed hardening layer is applied FROM, runs
// the provenance gate against the blessed cove-base-image(s), and returns the ref
// to pass as the hardening build's BASE arg. A base that descends from no blessed
// image is rejected — unless spec.AllowUnverified, which downgrades the rejection
// to a loud warning on warn and proceeds.
func Resolve(d Docker, s Spec, blessedRefs []string, warn io.Writer) (string, error) {
	ref, userChosen, err := resolveRef(d, s)
	if err != nil {
		return "", err
	}
	// The default ref IS a blessed cove-base-image (we pin it), so it is trusted by
	// construction — no inspection needed. Only a kit-chosen base (image.base or an
	// image/Dockerfile) must prove its provenance.
	if !userChosen {
		return ref, nil
	}

	child, err := d.Layers(ref)
	if err != nil {
		return "", fmt.Errorf("read base image layers for %s: %w", ref, err)
	}
	var blessed [][]string
	for _, br := range blessedRefs {
		layers, err := d.Layers(br)
		if err != nil {
			continue // a blessed image we cannot fetch is skipped, not fatal
		}
		blessed = append(blessed, layers)
	}
	if Verify(child, blessed) {
		return ref, nil
	}

	if s.AllowUnverified {
		fmt.Fprintf(warn, "%s\nWARNING: base %s does not descend from any blessed cove-base-image.\n"+
			"The sealed hardening layer cannot verify its prerequisites (egress stack, agent\n"+
			"user, expected layout). Proceeding only because --allow-unverified-base was set.\n%s\n",
			warnRule, ref, warnRule)
		return ref, nil
	}
	return "", fmt.Errorf("base %s does not descend from any blessed cove-base-image; "+
		"the sealed hardening layer cannot trust its prerequisites. Pass --allow-unverified-base "+
		"to override (at your own risk)", ref)
}

const warnRule = "!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!"

// resolveRef picks the base ref per the design's precedence: a kit Dockerfile is
// built (its image ID becomes the base), else image.base, else the default. The
// bool reports whether the base was kit-chosen (and so must pass the gate); the
// default is blessed by construction.
func resolveRef(d Docker, s Spec) (string, bool, error) {
	switch {
	case s.DockerfileDir != "":
		id, err := d.Build(s.DockerfileDir)
		if err != nil {
			return "", false, fmt.Errorf("build kit base image (%s): %w", s.DockerfileDir, err)
		}
		return id, true, nil
	case s.Base != "":
		return s.Base, true, nil
	default:
		return s.DefaultRef, false, nil
	}
}
