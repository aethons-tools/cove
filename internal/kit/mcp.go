package kit

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
)

// MCPReservedName is the MCP server name the cove's agent harness always
// registers itself (the Jam messaging tools, `cove-master mcp`). A kit may
// never declare or override it.
const MCPReservedName = "messaging"

// MCPServersImagePath is where a kit's mcp-servers are baked into the cove
// image (JSON: name → MCPServer) for the agent harness to read. Cove-owned —
// deliberately not under /etc/claude-code.
const MCPServersImagePath = "/etc/cove/mcp-servers.json"

// MCPServer is one kit-declared MCP server (a kit's mcp-servers: map value).
// Its JSON form is Claude Code's --mcp-config server entry shape, so the
// harness passes it through as-is. Values may reference environment variables
// (${VAR}), which Claude Code expands at agent start; header values MUST be
// references (see ValidateMCPServers) so no secret ever lives in kit config,
// an image, or a generated config file.
type MCPServer struct {
	Type    string            `yaml:"type" json:"type"`                           // "http" | "stdio"
	URL     string            `yaml:"url,omitempty" json:"url,omitempty"`         // http only
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"` // http only; values are ${VAR} references
	Command string            `yaml:"command,omitempty" json:"command,omitempty"` // stdio only
	Args    []string          `yaml:"args,omitempty" json:"args,omitempty"`       // stdio only
}

var (
	mcpNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	// mcpHeaderRe: a whole-value ${VAR} reference, optionally after ONE fixed
	// scheme token ("Bearer ${VAR}", "Basic ${VAR}"). No default syntax
	// (${VAR:-literal}) — that would smuggle a literal back in.
	mcpHeaderRe = regexp.MustCompile(`^(?:[A-Za-z][A-Za-z0-9-]* )?\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)
)

// ValidateMCPServers checks a kit's mcp-servers map. field prefixes errors
// (e.g. "studio kit: mcp-servers"). Header values are never echoed in errors —
// a rejected literal is likely a secret.
func ValidateMCPServers(field string, servers map[string]MCPServer) error {
	for _, name := range slices.Sorted(maps.Keys(servers)) {
		s := servers[name]
		at := fmt.Sprintf("%s[%q]", field, name)
		switch {
		case name == MCPReservedName:
			return fmt.Errorf("%s: %q is a reserved name (the cove's own messaging server); pick another name", at, name)
		case !mcpNameRe.MatchString(name):
			return fmt.Errorf("%s: server name must be non-empty [A-Za-z0-9_-]", at)
		}
		switch s.Type {
		case "http":
			if s.URL == "" {
				return fmt.Errorf("%s: type http requires url", at)
			}
			if s.Command != "" || len(s.Args) > 0 {
				return fmt.Errorf("%s: type http takes url (+headers), not command/args", at)
			}
			for _, h := range slices.Sorted(maps.Keys(s.Headers)) {
				if !mcpHeaderRe.MatchString(s.Headers[h]) {
					return fmt.Errorf("%s: headers[%q]: value must be an env reference ${VAR} (optionally after one scheme word, e.g. \"Bearer ${VAR}\") — never a literal (value not shown)", at, h)
				}
			}
		case "stdio":
			if s.Command == "" {
				return fmt.Errorf("%s: type stdio requires command", at)
			}
			if s.URL != "" {
				return fmt.Errorf("%s: type stdio takes command (+args), not url", at)
			}
			if len(s.Headers) > 0 {
				return fmt.Errorf("%s: type stdio takes command (+args), not headers", at)
			}
		default:
			return fmt.Errorf("%s: type must be http or stdio, got %q", at, s.Type)
		}
	}
	return nil
}
