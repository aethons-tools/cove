// Package studio defines the StudioKit — the small, directly-authored kit a
// brokered ("studio") cove is built and raised from. It deliberately does NOT
// reuse kit.Config: a studio kit carries only a base, egress, secret demands,
// build args, and an orienting prompt.
package studio

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/aethons-tools/cove/internal/kit"
	"gopkg.in/yaml.v3"
)

// Kind is the discriminator stored with every studio kit, distinguishing it
// from a full kit.Config entry sharing the jam kit registry.
const Kind = "studio"

// StudioKit is the parsed contents of a studio kit's config. Build-affecting
// fields (Base, Egress, BuildArgs) key the built image (see BuildDigest);
// raise-time fields (Secrets, Prompt) do not.
type StudioKit struct {
	Kind      string                      `yaml:"kind" json:"kind"`
	Name      string                      `yaml:"name" json:"name"`
	Base      Base                        `yaml:"base,omitempty" json:"base,omitempty"`
	Egress    []string                    `yaml:"egress,omitempty" json:"egress,omitempty"`
	BuildArgs map[string]string           `yaml:"build-args,omitempty" json:"build-args,omitempty"`
	Secrets   map[string]kit.SecretConfig `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	Prompt    string                      `yaml:"prompt,omitempty" json:"prompt,omitempty"`
}

// ParseStudioKit unmarshals and validates studio-kit YAML. Unknown fields are
// rejected (KnownFields) to catch typos — including a full-kit field wrongly
// placed on a studio kit.
func ParseStudioKit(data []byte) (StudioKit, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var sk StudioKit
	if err := dec.Decode(&sk); err != nil {
		return StudioKit{}, fmt.Errorf("studio kit: %w", err)
	}
	if err := sk.Validate(); err != nil {
		return StudioKit{}, err
	}
	return sk, nil
}

// Validate enforces the studio-kit invariants.
func (sk StudioKit) Validate() error {
	if sk.Kind != Kind {
		return fmt.Errorf("studio kit: kind must be %q, got %q", Kind, sk.Kind)
	}
	if sk.Name == "" {
		return fmt.Errorf("studio kit: name is required")
	}
	if !TagSafeName(sk.Name) {
		return fmt.Errorf("studio kit: name %q is not tag-safe (allowed: [A-Za-z0-9_.-])", sk.Name)
	}
	if err := sk.Base.validate(); err != nil {
		return fmt.Errorf("studio kit %q: %w", sk.Name, err)
	}
	// Build-args must never collide with a secret demand or a reserved secret
	// name — secrets reach the session at raise, never the build (argv/logs).
	for k := range sk.BuildArgs {
		if _, ok := sk.Secrets[k]; ok {
			return fmt.Errorf("studio kit %q: build-arg %q collides with a secret demand", sk.Name, k)
		}
		if kit.IsReservedSecretName(k) {
			return fmt.Errorf("studio kit %q: build-arg %q is a reserved secret name", sk.Name, k)
		}
	}
	return nil
}

// ToJSON is the deterministic canonical form stored in the kit registry and
// hashed. encoding/json emits struct fields in declaration order and map keys
// sorted, so identical kits yield identical bytes.
func (sk StudioKit) ToJSON() ([]byte, error) {
	if sk.Kind == "" {
		sk.Kind = Kind
	}
	// base.context-dir is a client-only authoring field packed into base.context
	// at push. It must be resolved (see (*StudioKit).ResolveContextDir) before a
	// kit is serialized for storage — the server can't read the operator's disk.
	if sk.Base.ContextDir != "" {
		return nil, fmt.Errorf("studio kit %q: base.context-dir must be resolved (packed) before serializing", sk.Name)
	}
	return json.Marshal(sk)
}

// TagSafeName reports whether name may be a docker-tag / registry-id component.
func TagSafeName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}
