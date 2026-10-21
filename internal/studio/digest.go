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
	return hashDigest(struct {
		Base       Base                     `json:"base"`
		Egress     []string                 `json:"egress"`
		BuildArgs  map[string]string        `json:"buildArgs"`
		MCPServers map[string]kit.MCPServer `json:"mcpServers,omitempty"`
		Harness    harnessinstall.Install   `json:"harness"`
	}{Base: sk.Base, Egress: normEgress(sk.Egress), BuildArgs: normBuildArgs(sk.BuildArgs), MCPServers: sk.MCPServers, Harness: normHarness(harness)})
}

// ChainDigest is BuildDigest for a resolved kit chain (root first, the kit
// itself last). A chain of one is exactly BuildDigest, so unchained kits keep
// their image tags. Longer chains hash every link's own build-affecting fields
// (base, egress, build args, MCP servers) in order plus the harness, so a
// change anywhere up the chain changes the child's digest. chain must hold at
// least one kit: an empty chain hashes to a meaningless digest.
func ChainDigest(chain []StudioKit, harness harnessinstall.Install) string {
	if len(chain) == 1 {
		return BuildDigest(chain[0], harness)
	}
	type link struct {
		Base       Base                     `json:"base"`
		Egress     []string                 `json:"egress"`
		BuildArgs  map[string]string        `json:"buildArgs"`
		MCPServers map[string]kit.MCPServer `json:"mcpServers,omitempty"`
	}
	links := make([]link, 0, len(chain))
	for _, sk := range chain {
		links = append(links, link{Base: sk.Base, Egress: normEgress(sk.Egress), BuildArgs: normBuildArgs(sk.BuildArgs), MCPServers: sk.MCPServers})
	}
	return hashDigest(struct {
		Chain   []link                 `json:"chain"`
		Harness harnessinstall.Install `json:"harness"`
	}{Chain: links, Harness: normHarness(harness)})
}

// normEgress sorts and compacts egress; nil becomes empty non-nil.
func normEgress(in []string) []string {
	egress := slices.Clone(in)
	slices.Sort(egress)
	egress = slices.Compact(egress)
	if len(egress) == 0 {
		return []string{}
	}
	return egress
}

// normBuildArgs maps nil to an empty map so nil and empty hash identically.
func normBuildArgs(in map[string]string) map[string]string {
	if in == nil {
		return map[string]string{}
	}
	return in
}

// normHarness sorts and compacts plugins (nil becomes empty non-nil). The
// harness is always hashed: adding it (COV-242) changed every kit's digest
// once, unavoidably.
func normHarness(h harnessinstall.Install) harnessinstall.Install {
	plugins := slices.Clone(h.Plugins)
	slices.Sort(plugins)
	plugins = slices.Compact(plugins)
	if plugins == nil {
		plugins = []string{}
	}
	h.Plugins = plugins
	return h
}

func hashDigest(payload any) string {
	b, _ := json.Marshal(payload) // JSON-safe fields only; never errors
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
