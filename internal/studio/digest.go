package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/kit"
)

// BuildDigest is the content key of the image a studio kit builds under a
// harness install. It hashes the BUILD-AFFECTING fields only — base, egress
// (order-normalized), build args, mcp-servers (baked into the image) and the
// harness layer (type, exact CLI version, plugins: the role's model-spec,
// COV-242) — so a raise-time edit (prompt, secret demands) reuses the cached
// image, while any build input change yields a fresh image. Deterministic: sorted egress + JSON
// with sorted map keys. Nil and empty slices/maps are normalized to empty
// non-nil so they hash identically.
func BuildDigest(sk StudioKit, harness harnessinstall.Install) string {
	egress := slices.Clone(sk.Egress)
	slices.Sort(egress)
	egress = slices.Compact(egress)
	// Normalize nil egress to empty non-nil slice for consistent hashing
	if len(egress) == 0 {
		egress = []string{}
	}
	// Normalize nil BuildArgs to empty non-nil map for consistent hashing
	buildArgs := sk.BuildArgs
	if buildArgs == nil {
		buildArgs = map[string]string{}
	}
	// MCPServers (baked into the image) is omitempty so a kit without any keeps
	// its pre-COV-240 digest. The harness is always hashed: adding it (COV-242)
	// changed every kit's digest once, unavoidably — the harness install moved
	// out of the sealed layer into a model-spec-driven one.
	plugins := slices.Clone(harness.Plugins)
	slices.Sort(plugins)
	plugins = slices.Compact(plugins)
	if plugins == nil {
		plugins = []string{}
	}
	harness.Plugins = plugins
	payload := struct {
		Base       Base                     `json:"base"`
		Egress     []string                 `json:"egress"`
		BuildArgs  map[string]string        `json:"buildArgs"`
		MCPServers map[string]kit.MCPServer `json:"mcpServers,omitempty"`
		Harness    harnessinstall.Install   `json:"harness"`
	}{Base: sk.Base, Egress: egress, BuildArgs: buildArgs, MCPServers: sk.MCPServers, Harness: harness}
	b, _ := json.Marshal(payload) // struct of JSON-safe fields; never errors
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
