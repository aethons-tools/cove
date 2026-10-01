package studio

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Caps for the untrusted base64 `context` tar.gz (hard, fail-closed — see the
// spec). The cheap encoded-size check runs at register; the decompressed-size /
// entry-count checks run in ScanContextTar at prepare. Package vars (not consts)
// so tests can exercise the caps with small inputs; treat as constants in
// production.
var (
	maxEncodedZip      = 1 << 20  // 1 MiB of base64 in the kit definition
	maxDecompressedZip = 64 << 20 // 64 MiB uncompressed
	maxZipEntries      = 2000
)

// Base names the image a studio kit builds FROM, exactly one of four ways
// (all empty ⇒ the blessed default base):
//   - Image: a prebuilt, provenance-gated image ref.
//   - ContextFiles: a simple context authored inline as a tree — the reserved
//     key "dockerfile" is the (required) Dockerfile; every other key is a path
//     segment whose value is a file (string) or a subdirectory (nested tree).
//   - Context: base64 of a tar.gz build context (may include binaries) whose
//     root holds a Dockerfile. It is streamed to `docker build -`, which
//     extracts it and preserves unix file modes (so an executable script keeps
//     its +x bit).
//
// A context Dockerfile should `FROM ${COVE_BASE_IMAGE}` so the built base
// descends from the blessed cove-base-image (the provenance gate is ON).
type Base struct {
	Image        string      `yaml:"image,omitempty" json:"image,omitempty"`
	ContextFiles ContextTree `yaml:"context-files,omitempty" json:"contextFiles,omitempty"`
	Context      string      `yaml:"context,omitempty" json:"context,omitempty"` // base64 tar.gz
	// ContextDir is a CLIENT-ONLY authoring convenience: a host directory that
	// `at-jam kit push` packs into Context (a tar.gz) before pushing, then clears.
	// It is never serialized (json:"-"), so it never reaches the registry, the
	// server, or the build-digest — the server can't read the operator's disk.
	ContextDir string `yaml:"context-dir,omitempty" json:"-"`
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
	BaseContextZip                   // a base64 tar.gz context
	BaseContextDir                   // a host dir (client-only; packed into a tar.gz at push)
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
	if b.ContextDir != "" {
		kind, n = BaseContextDir, n+1
	}
	if n > 1 {
		return BaseDefault, fmt.Errorf("base: set exactly one of image / context-files / context / context-dir")
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
		// The reserved `dockerfile` is materialized to `Dockerfile`; a sibling
		// `Dockerfile` key would collide at that path with undefined precedence.
		if _, clash := b.ContextFiles["Dockerfile"]; clash {
			return fmt.Errorf("base.context-files: use the reserved lowercase `dockerfile` key; a top-level `Dockerfile` collides with it")
		}
		return validateContextKeys(b.ContextFiles)
	case BaseContextZip:
		return b.validateContextCheap()
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

// validateContextCheap runs the register-time checks on the base64 `context`:
// valid base64, a readable gzip+tar stream, and within the encoded-size cap. The
// heavier decompressed-size / entry / name-safety checks run at prepare in
// ScanContextTar.
func (b Base) validateContextCheap() error {
	if len(b.Context) > maxEncodedZip {
		return fmt.Errorf("base.context: encoded context is %d bytes, exceeds the %d-byte cap", len(b.Context), maxEncodedZip)
	}
	raw, err := base64.StdEncoding.DecodeString(b.Context)
	if err != nil {
		return fmt.Errorf("base.context: invalid base64: %w", err)
	}
	gr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("base.context: not a readable gzip: %w", err)
	}
	defer gr.Close()
	// Read one tar header to confirm it is a readable tar. An empty (header-only
	// EOF) archive is tolerated here; the required root Dockerfile and the caps
	// are enforced at prepare by ScanContextTar.
	tr := tar.NewReader(io.LimitReader(gr, int64(maxDecompressedZip)+1))
	if _, err := tr.Next(); err != nil && err != io.EOF {
		return fmt.Errorf("base.context: not a readable tar: %w", err)
	}
	return nil
}

// ContextTar produces the context tar.gz bytes to stream to `docker build -`.
// For a BaseContextZip it decodes the stored base64; for a BaseContextFiles it
// builds a deterministic in-memory tar.gz from the inline tree (the reserved
// `dockerfile` key → a root Dockerfile, every other file 0644, dirs implied).
// It errors for the image/default forms, which carry no build context.
func (b Base) ContextTar() ([]byte, error) {
	kind, err := b.Kind()
	if err != nil {
		return nil, err
	}
	switch kind {
	case BaseContextZip:
		raw, err := base64.StdEncoding.DecodeString(b.Context)
		if err != nil {
			return nil, fmt.Errorf("base.context: invalid base64: %w", err)
		}
		return raw, nil
	case BaseContextFiles:
		return tarFromTree(b.ContextFiles)
	default:
		return nil, fmt.Errorf("base: ContextTar is only for context-files/context (got kind %d)", kind)
	}
}

// tarEntry is one regular file packed into a deterministic tar.gz.
type tarEntry struct {
	name string // forward-slash relative path
	mode int64  // unix permission bits
	data []byte
}

// tarFromTree flattens an inline context tree into a deterministic tar.gz: the
// reserved `dockerfile` becomes the root Dockerfile, every other file keeps its
// single-segment/nested path, all at mode 0644 (YAML carries no mode info).
func tarFromTree(t ContextTree) ([]byte, error) {
	var entries []tarEntry
	var walk func(prefix string, tree ContextTree)
	walk = func(prefix string, tree ContextTree) {
		for name, node := range tree {
			p := name
			if prefix != "" {
				p = prefix + "/" + name
			}
			switch {
			case node.File != nil:
				if name == "dockerfile" && prefix == "" {
					p = "Dockerfile"
				}
				entries = append(entries, tarEntry{name: p, mode: 0o644, data: []byte(*node.File)})
			case node.Dir != nil:
				walk(p, node.Dir)
			}
		}
	}
	walk("", t)
	return buildTarGz(entries)
}

// buildTarGz writes entries into a deterministic gzip-compressed tar: entries are
// sorted by name and every metadata field that could leak host state or perturb
// the bytes (ModTime, Uid/Gid, Uname/Gname, the gzip header mtime) is zeroed,
// while the unix permission bits are preserved so docker restores the exec bit.
func buildTarGz(entries []tarEntry) ([]byte, error) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	gw.ModTime = time.Time{} // zero → gzip writes no timestamp
	tw := tar.NewWriter(gw)
	// time.Unix(0,0): a fixed, USTAR-representable mtime (epoch) so the archive is
	// byte-stable and leaks no host timestamp, while staying in the simple tar
	// format (a zero time.Time{} is year 1, which would force PAX mtime records).
	epoch := time.Unix(0, 0).UTC()
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Mode:     e.mode & 0o777,
			Size:     int64(len(e.data)),
			Typeflag: tar.TypeReg,
			ModTime:  epoch,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(e.data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
