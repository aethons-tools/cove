// Package studio defines the StudioKit — the small, directly-authored kit a
// brokered ("studio") cove is built and raised from. It deliberately does NOT
// reuse kit.Config: a studio kit carries only a base, egress, secret demands,
// build args, and an orienting prompt.
package studio

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/kit"
	"gopkg.in/yaml.v3"
)

// Kind is the discriminator stored with every studio kit, distinguishing it
// from a full kit.Config entry sharing the jam kit registry.
const Kind = "studio"

// StudioKit is the parsed contents of a studio kit's config. Build-affecting
// fields (Base, Egress, BuildArgs) key the built image (see BuildDigest);
// raise-time fields (Secrets, Prompt) do not.
//
// A kit does not name itself: the registry key (the push's name) is its name.
type StudioKit struct {
	Kind string `yaml:"kind" json:"kind"`
	// LegacyName is the deprecated `name:` field. It is accepted on input so
	// kit files and registry rows from before names left the schema still
	// parse, checked against the pushed name (CheckName), and never stored.
	LegacyName string                      `yaml:"name,omitempty" json:"-"`
	Base       Base                        `yaml:"base,omitempty" json:"base,omitempty"`
	Egress     []string                    `yaml:"egress,omitempty" json:"egress,omitempty"`
	BuildArgs  map[string]string           `yaml:"build-args,omitempty" json:"build-args,omitempty"`
	Secrets    map[string]kit.SecretConfig `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	Prompt     string                      `yaml:"prompt,omitempty" json:"prompt,omitempty"`
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
	if err := sk.Base.validate(); err != nil {
		return fmt.Errorf("studio kit: %w", err)
	}
	// Build-args must never collide with a secret demand or a reserved secret
	// name — secrets reach the session at raise, never the build (argv/logs).
	for k := range sk.BuildArgs {
		if _, ok := sk.Secrets[k]; ok {
			return fmt.Errorf("studio kit: build-arg %q collides with a secret demand", k)
		}
		if kit.IsReservedSecretName(k) {
			return fmt.Errorf("studio kit: build-arg %q is a reserved secret name", k)
		}
	}
	return nil
}

// CheckPrompt enforces the kit prompt's budget: it is the session context's
// always-on kit core (sessionctx). An authoring rule, checked on push only —
// not in Validate, so a kit stored before the budget still parses and raises
// (Compile truncates it).
func (sk StudioKit) CheckPrompt() error {
	if n := len(strings.TrimSpace(sk.Prompt)); n > sessionctx.BudgetKit {
		return fmt.Errorf("studio kit: prompt is %d bytes; the kit core budget is %d — move detail out of the prompt", n, sessionctx.BudgetKit)
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
		return nil, fmt.Errorf("studio kit: base.context-dir must be resolved (packed) before serializing")
	}
	return json.Marshal(sk)
}

// CheckName validates the name a kit is pushed (registered) under: it must be
// tag-safe, and a deprecated in-file `name:` must agree with it, so a file
// written for one kit can't silently land on another.
func (sk StudioKit) CheckName(name string) error {
	if !TagSafeName(name) {
		return fmt.Errorf("kit name %q is not tag-safe (allowed: [A-Za-z0-9_.-])", name)
	}
	if sk.LegacyName != "" && sk.LegacyName != name {
		return fmt.Errorf("kit file names itself %q but is being pushed as %q; drop its name: field (kits are named by the registry)", sk.LegacyName, name)
	}
	return nil
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
