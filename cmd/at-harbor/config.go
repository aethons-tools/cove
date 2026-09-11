package main

import (
	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/secret"
	"gopkg.in/yaml.v3"
)

// credSpec is the YAML shape for one harbor credential: either a resolver
// command or a literal value (dev only).
type credSpec struct {
	Command []string `yaml:"command"`
	Value   string   `yaml:"value"`
}

// serveConfig is the on-disk config for `at-harbor serve`.
type serveConfig struct {
	Listen string `yaml:"listen"`
	TLS    struct {
		Cert string `yaml:"cert"`
		Key  string `yaml:"key"`
	} `yaml:"tls"`
	Store       string              `yaml:"store"`
	Broker      harbor.Config       `yaml:",inline"`
	Credentials map[string]credSpec `yaml:"credentials"`
}

// parseServeConfig parses the serve config YAML.
func parseServeConfig(data []byte) (serveConfig, error) {
	var c serveConfig
	if err := yaml.Unmarshal(data, &c); err != nil {
		return serveConfig{}, err
	}
	return c, nil
}

// credSpecs maps each configured credential to a secret.Spec (literal or command).
func (c serveConfig) credSpecs() map[string]secret.Spec {
	out := make(map[string]secret.Spec, len(c.Credentials))
	for name, cs := range c.Credentials {
		if cs.Value != "" {
			out[name] = secret.Spec{Value: cs.Value, Literal: true}
		} else {
			out[name] = secret.Spec{Command: cs.Command}
		}
	}
	return out
}
