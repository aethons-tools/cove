// Package studio defines the StudioKit — the small, directly-authored kit a
// brokered ("studio") cove is built and raised from. A studio kit carries only
// a base, egress, secret demands, build args, and an orienting prompt.
package studio

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/kit"
	"gopkg.in/yaml.v3"
)

// Kind is the discriminator stored with every studio kit, distinguishing it
// in the jam kit registry.
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
	// Notes are leaves the kit ships into its sessions' context (kit/<name>).
	// Like Prompt, a raise-time input: outside the build digest.
	Notes []KitNote `yaml:"notes,omitempty" json:"notes,omitempty"`
	// MCPServers are MCP servers the cove's agent loads alongside its
	// guaranteed messaging server (COV-240). Build-affecting: they are baked
	// into the image at /etc/jam/mcp-servers.json (env references only — never
	// secret values; see kit.ValidateMCPServers).
	MCPServers map[string]kit.MCPServer `yaml:"mcp-servers,omitempty" json:"mcp-servers,omitempty"`
	// BuildContext is the tree hash of the kit's build context (its tree minus
	// kit.yml) when a Dockerfile sits next to kit.yml; "" otherwise. Set by
	// ParseTree, never serialized; build-affecting (BuildDigest).
	BuildContext string `yaml:"-" json:"-"`
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
	if err := kit.ValidateMCPServers("studio kit: mcp-servers", sk.MCPServers); err != nil {
		return err
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
