package main

import (
	"fmt"
	"net"
	"reflect"
	"sort"
	"strings"

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
	Listen      string `yaml:"listen"`
	AdminListen string `yaml:"admin-listen"`
	TLS         struct {
		Cert string `yaml:"cert"`
		Key  string `yaml:"key"`
	} `yaml:"tls"`
	AdminTLS struct {
		Cert string `yaml:"cert"`
		Key  string `yaml:"key"`
	} `yaml:"admin-tls"`
	Store        string              `yaml:"store"`
	Credentials  map[string]credSpec `yaml:"credentials"`
	OperatorAuth struct {
		OIDC *struct {
			Issuer         string `yaml:"issuer"`
			Audience       string `yaml:"audience"`
			RequireScope   string `yaml:"require-scope"`
			DeviceClientID string `yaml:"device-client-id"`
			DeviceScope    string `yaml:"device-scope"`
		} `yaml:"oidc"`
	} `yaml:"operator-auth"`
}

// operatorLoginConfig builds the public device-flow client config harbor
// advertises at /admin/login-config, or nil when device login isn't configured
// (no device-client-id). Scope defaults to "openid".
func (c serveConfig) operatorLoginConfig() *harbor.OperatorLoginConfig {
	o := c.OperatorAuth.OIDC
	if o == nil || o.DeviceClientID == "" {
		return nil
	}
	scope := o.DeviceScope
	if scope == "" {
		scope = "openid"
	}
	return &harbor.OperatorLoginConfig{Issuer: o.Issuer, Audience: o.Audience, ClientID: o.DeviceClientID, Scope: scope}
}

// adminTLS resolves the admin listener's cert/key: admin-tls if set, else the
// top-level tls. ok is false when neither is configured.
func (c serveConfig) adminTLS() (cert, key string, ok bool) {
	if c.AdminTLS.Cert != "" && c.AdminTLS.Key != "" {
		return c.AdminTLS.Cert, c.AdminTLS.Key, true
	}
	if c.TLS.Cert != "" && c.TLS.Key != "" {
		return c.TLS.Cert, c.TLS.Key, true
	}
	return "", "", false
}

// adminUsesTLS reports whether to serve the admin API over TLS: off-loopback
// (required by validateAdminExposure) or an explicit admin-tls block (opt-in on
// loopback). Reusing the broker's tls: does not upgrade a loopback listener.
func (c serveConfig) adminUsesTLS() bool {
	if !isLoopbackAddr(c.AdminListen) {
		return true
	}
	return c.AdminTLS.Cert != "" && c.AdminTLS.Key != ""
}

// validateAdminExposure refuses an off-loopback admin listener that lacks TLS or
// OIDC — either would expose the admin API to token interception or no real auth.
func (c serveConfig) validateAdminExposure() error {
	if c.AdminListen == "" || isLoopbackAddr(c.AdminListen) {
		return nil
	}
	if _, _, ok := c.adminTLS(); !ok {
		return fmt.Errorf("admin-listen %q is off-loopback but no TLS is configured (set `tls` or `admin-tls`)", c.AdminListen)
	}
	if c.OperatorAuth.OIDC == nil {
		return fmt.Errorf("admin-listen %q is off-loopback but operator auth is loopback-only (configure `operator-auth.oidc`)", c.AdminListen)
	}
	return nil
}

// isLoopbackAddr reports whether a host:port listen address binds only loopback.
// Empty host, 0.0.0.0 and :: are all-interfaces (non-loopback).
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// serveConfigKeys is the set of recognized top-level YAML keys, derived from
// serveConfig's yaml tags so it can't drift as fields are added.
func serveConfigKeys() map[string]bool {
	keys := map[string]bool{}
	t := reflect.TypeOf(serveConfig{})
	for i := 0; i < t.NumField(); i++ {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ","); name != "" && name != "-" {
			keys[name] = true
		}
	}
	return keys
}

// unknownServeKeys returns the top-level keys in the harbor.yml that serveConfig
// does not recognize, sorted. The bootstrap config is parsed leniently (unknown
// keys are silently dropped), so a stray `destinations:` block — natural to write
// but now managed via the admin API — would otherwise vanish without a word.
func unknownServeKeys(data []byte) []string {
	var m map[string]any
	if yaml.Unmarshal(data, &m) != nil {
		return nil // a genuine parse error surfaces from parseServeConfig instead
	}
	known := serveConfigKeys()
	var out []string
	for k := range m {
		if !known[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
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
