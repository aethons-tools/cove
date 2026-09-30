package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
)

// BuildDigest is the content key of the image a studio kit builds. It hashes the
// BUILD-AFFECTING fields only — base, egress (order-normalized), build args — so
// a raise-time edit (prompt, secret demands) reuses the cached image, while any
// build input change yields a fresh image. Deterministic: sorted egress + JSON
// with sorted map keys.
func BuildDigest(sk StudioKit) string {
	egress := slices.Clone(sk.Egress)
	slices.Sort(egress)
	egress = slices.Compact(egress)
	payload := struct {
		Base      Base              `json:"base"`
		Egress    []string          `json:"egress"`
		BuildArgs map[string]string `json:"buildArgs"`
	}{Base: sk.Base, Egress: egress, BuildArgs: sk.BuildArgs}
	b, _ := json.Marshal(payload) // struct of JSON-safe fields; never errors
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
