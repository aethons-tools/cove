package studio

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Caps for the untrusted base64 `context` zip (hard, fail-closed — see the spec).
// The cheap encoded-size check runs at register; the decompressed-size /
// entry-count checks run at materialize. Package vars (not consts) so tests can
// exercise the caps with small inputs; treat as constants in production.
var (
	maxEncodedZip      = 1 << 20  // 1 MiB of base64 in the kit definition
	maxDecompressedZip = 64 << 20 // 64 MiB extracted
	maxZipEntries      = 2000
)

// Base names the image a studio kit builds FROM, exactly one of four ways
// (all empty ⇒ the blessed default base):
//   - Image: a prebuilt, provenance-gated image ref.
//   - ContextFiles: a simple context authored inline as a tree — the reserved
//     key "dockerfile" is the (required) Dockerfile; every other key is a path
//     segment whose value is a file (string) or a subdirectory (nested tree).
//   - Context: base64 of a zip build context (may include binaries) whose root
//     holds a Dockerfile.
//
// A context Dockerfile should `FROM ${COVE_BASE_IMAGE}` so the built base
// descends from the blessed cove-base-image (the provenance gate is ON).
type Base struct {
	Image        string      `yaml:"image,omitempty" json:"image,omitempty"`
	ContextFiles ContextTree `yaml:"context-files,omitempty" json:"contextFiles,omitempty"`
	Context      string      `yaml:"context,omitempty" json:"context,omitempty"` // base64 zip
}

// ContextTree is a directory of authored context entries keyed by a single path
// segment. Marshaling is deterministic (sorted keys), so it is hashable for the
// build-digest.
type ContextTree map[string]ContextNode

// ContextNode is either a file (File non-nil, its content) or a subdirectory
// (Dir non-nil). Exactly one is set.
type ContextNode struct {
	File *string
	Dir  ContextTree
}

// UnmarshalYAML maps a scalar to a file and a mapping to a subdirectory.
func (n *ContextNode) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		var s string
		if err := value.Decode(&s); err != nil {
			return err
		}
		n.File = &s
		return nil
	case yaml.MappingNode:
		var t ContextTree
		if err := value.Decode(&t); err != nil {
			return err
		}
		n.Dir = t
		return nil
	default:
		return fmt.Errorf("context entry must be a file (string) or a directory (map), got yaml kind %d", value.Kind)
	}
}

// MarshalJSON emits a file as its string and a directory as an object, so a
// ContextTree round-trips deterministically (sorted keys) for the digest.
func (n ContextNode) MarshalJSON() ([]byte, error) {
	if n.File != nil {
		return json.Marshal(*n.File)
	}
	return json.Marshal(n.Dir)
}

// MarshalYAML renders a file as its string and a directory as a nested map (used
// by `kit show`).
func (n ContextNode) MarshalYAML() (any, error) {
	if n.File != nil {
		return *n.File, nil
	}
	return n.Dir, nil
}

// BaseKind is which of the four base forms a Base selects.
type BaseKind int

const (
	BaseDefault      BaseKind = iota // all empty → the blessed default base
	BaseImage                        // a prebuilt gated ref
	BaseContextFiles                 // an inline tree
	BaseContextZip                   // a base64 zip context
)

// Kind reports which base form is set, erroring if more than one is.
func (b Base) Kind() (BaseKind, error) {
	kind, n := BaseDefault, 0
	if b.Image != "" {
		kind, n = BaseImage, n+1
	}
	if len(b.ContextFiles) > 0 {
		kind, n = BaseContextFiles, n+1
	}
	if b.Context != "" {
		kind, n = BaseContextZip, n+1
	}
	if n > 1 {
		return BaseDefault, fmt.Errorf("base: set exactly one of image / context-files / context")
	}
	return kind, nil
}

// validate enforces the base invariants (exactly-one form, the context-files
// shape, and the cheap zip checks). Called from StudioKit.Validate.
func (b Base) validate() error {
	kind, err := b.Kind()
	if err != nil {
		return err
	}
	switch kind {
	case BaseContextFiles:
		df, ok := b.ContextFiles["dockerfile"]
		if !ok || df.File == nil {
			return fmt.Errorf("base.context-files: a top-level `dockerfile` file is required")
		}
		return validateContextKeys(b.ContextFiles)
	case BaseContextZip:
		return b.validateZipCheap()
	}
	return nil
}

// validateContextKeys enforces that every key in the tree is a single, safe path
// segment (non-empty, no "/", not "." or ".."), recursively.
func validateContextKeys(t ContextTree) error {
	for k, node := range t {
		if k == "" || k == "." || k == ".." || strings.ContainsRune(k, '/') || strings.ContainsRune(k, '\\') {
			return fmt.Errorf("base.context-files: key %q must be a single path segment (no /, \\, or ..)", k)
		}
		if node.Dir != nil {
			if err := validateContextKeys(node.Dir); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateZipCheap runs the register-time checks on the base64 `context`: valid
// base64, a readable zip, and within the encoded-size cap. The heavier
// decompressed-size / entry / zip-slip checks run at materialize.
func (b Base) validateZipCheap() error {
	if len(b.Context) > maxEncodedZip {
		return fmt.Errorf("base.context: encoded zip is %d bytes, exceeds the %d-byte cap", len(b.Context), maxEncodedZip)
	}
	raw, err := base64.StdEncoding.DecodeString(b.Context)
	if err != nil {
		return fmt.Errorf("base.context: invalid base64: %w", err)
	}
	if _, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw))); err != nil {
		return fmt.Errorf("base.context: not a readable zip: %w", err)
	}
	return nil
}
