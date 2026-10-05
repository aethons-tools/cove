package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"github.com/aethons-tools/cove/internal/kit"
)

// BuildDigest is the content key of the image a studio kit builds. It hashes the
// BUILD-AFFECTING fields only — base, egress (order-normalized), build args,
// mcp-servers (baked into the image) — so a raise-time edit (prompt, secret
// demands) reuses the cached image, while any build input change yields a
// fresh image. Deterministic: sorted egress + JSON
// with sorted map keys. Nil and empty slices/maps are normalized to empty
// non-nil so they hash identically.
func BuildDigest(sk StudioKit) string {
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
	// its pre-COV-240 digest — adding the field rebuilt no existing image.
	payload := struct {
		Base       Base                     `json:"base"`
		Egress     []string                 `json:"egress"`
		BuildArgs  map[string]string        `json:"buildArgs"`
		MCPServers map[string]kit.MCPServer `json:"mcpServers,omitempty"`
	}{Base: sk.Base, Egress: egress, BuildArgs: buildArgs, MCPServers: sk.MCPServers}
	b, _ := json.Marshal(payload) // struct of JSON-safe fields; never errors
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
