package studio

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"unicode/utf8"

	"github.com/go-git/go-billy/v5"
	"gopkg.in/yaml.v3"

	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/textesc"
)

// ParseFiles is the one ingest pipeline from files to a kit: it builds files
// into a tree (kittree.Build: paths, modes, limits, kit.yml at the root) and
// parses the tree (ParseTree, which refuses the packed-context fields). It
// does not apply .dockerignore: callers that take files from outside (a push)
// run kittree.Ignore first. Errors are unprefixed; the caller
// locates them (a kit name, a directory). A refusal of the tree itself (a
// path, a limit, no kit.yml) is a *TreeError, so a caller can locate those
// at the kit and leave the kit.yml errors as they are.
func ParseFiles(files []kittree.File) (kittree.Tree, map[string][]byte, StudioKit, error) {
	tree, blobs, err := kittree.Build(files)
	if err != nil {
		return kittree.Tree{}, nil, StudioKit{}, &TreeError{err}
	}
	sk, err := ParseTree(tree, kittree.MapBlobs(blobs))
	if err != nil {
		return kittree.Tree{}, nil, StudioKit{}, err
	}
	return tree, blobs, sk, nil
}

// TreeError is ParseFiles refusing the files as a tree (kittree.Build): its
// text is the cause's.
type TreeError struct{ Err error }

func (e *TreeError) Error() string { return e.Err.Error() }
func (e *TreeError) Unwrap() error { return e.Err }

// ParseTree parses kit version tree t, its bytes from blobs: ParseKitFS over
// its billy view, with BuildContext set to t's context hash.
func ParseTree(t kittree.Tree, blobs kittree.Blobs) (StudioKit, error) {
	sk, err := ParseKitFS(kittree.NewFS(t, blobs))
	if err != nil {
		return StudioKit{}, err
	}
	sk.BuildContext = t.ContextHash()
	return sk, nil
}

// ParseKitFS parses the kit directory fsys: its kit.yml (RefuseContextFields,
// then ParseStudioKit),
// each note's file read from fsys into its body (relative to the root, inside
// it, at most maxNoteFileBytes in total), and the rule that base.image and a
// Dockerfile next to kit.yml exclude each other.
func ParseKitFS(fsys billy.Filesystem) (StudioKit, error) {
	data, err := readKitFile(fsys, kittree.KitFile, maxRootFile)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return StudioKit{}, errors.New("kit.yml missing at the root")
	case errors.Is(err, errTooLarge):
		return StudioKit{}, fmt.Errorf("%s: larger than %d bytes", kittree.KitFile, maxRootFile)
	case err != nil:
		return StudioKit{}, err
	}
	if err := RefuseContextFields(data); err != nil {
		return StudioKit{}, err
	}
	sk, err := ParseStudioKit(data)
	if err != nil {
		return StudioKit{}, err
	}
	budget := int64(maxNoteFileBytes)
	for i, n := range sk.Notes {
		if n.File == "" {
			continue
		}
		if n.Body != "" {
			return StudioKit{}, fmt.Errorf("note %q: set body or file, not both", n.Name)
		}
		p := path.Clean(n.File)
		if kittree.CheckPath(p) != nil {
			return StudioKit{}, fmt.Errorf("note %q: file must be a relative path inside the kit directory", n.Name)
		}
		b, err := readKitFile(fsys, p, budget)
		switch {
		case errors.Is(err, errTooLarge):
			return StudioKit{}, fmt.Errorf("notes: note files exceed the %d-byte total cap (at note %q)", maxNoteFileBytes, n.Name)
		case errors.Is(err, fs.ErrNotExist):
			// n.Name is %q-quoted (control and format characters escaped);
			// p passed CheckPath, and is escaped again in case that changes.
			return StudioKit{}, fmt.Errorf("note %q: file %s is not in the kit", n.Name, textesc.Escape(p, false))
		case err != nil:
			return StudioKit{}, err
		}
		budget -= int64(len(b))
		sk.Notes[i].Body, sk.Notes[i].File = string(b), ""
	}
	if _, err := fsys.Stat(kittree.Dockerfile); err == nil && sk.Base.Image != "" {
		return StudioKit{}, errors.New("studio kit: base.image and a Dockerfile next to kit.yml are mutually exclusive")
	}
	return sk, nil
}

// readKitFile reads p from fsys, errTooLarge past max bytes.
func readKitFile(fsys billy.Filesystem, p string, max int64) ([]byte, error) {
	f, err := fsys.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, fmt.Errorf("%s: cannot be read", p)
	}
	if int64(len(b)) > max {
		return nil, errTooLarge
	}
	return b, nil
}

// contextFields are the packed-context keys of base, as written and as stored.
var contextFields = []struct{ key, field string }{
	{"context", "base.context"},
	{"context-files", "base.context-files"},
	{"contextFiles", "base.context-files"},
	{"context-dir", "base.context-dir"},
}

// RefuseContextFields refuses a kit.yml that still uses the packed build
// context: base.context, base.context-files or base.context-dir, whether
// written as keys of base or reached through an alias or a merge key. A
// kit.yml that is not YAML passes; ParseStudioKit reports it.
func RefuseContextFields(kitYML []byte) error {
	if base := baseNode(kitYML); base != nil && base.Kind == yaml.MappingNode {
		for _, c := range contextFields {
			if MapValue(base, c.key) != nil {
				return contextFieldErr(c.field)
			}
		}
	}
	// Decoded leniently, the way the YAML decoder sees it: aliases and merge
	// keys resolved. A decode error leaves the fields it reached set; a field
	// present at all (even null, or an alias) has a non-zero Kind.
	var doc struct {
		Base struct {
			Packed  yaml.Node `yaml:"context"`
			Inline  yaml.Node `yaml:"context-files"`
			Stored  yaml.Node `yaml:"contextFiles"`
			HostDir yaml.Node `yaml:"context-dir"`
		} `yaml:"base"`
	}
	_ = yaml.Unmarshal(kitYML, &doc)
	switch b := doc.Base; {
	case b.Packed.Kind != 0:
		return contextFieldErr("base.context")
	case b.Inline.Kind != 0 || b.Stored.Kind != 0:
		return contextFieldErr("base.context-files")
	case b.HostDir.Kind != 0:
		return contextFieldErr("base.context-dir")
	}
	return nil
}

func contextFieldErr(field string) error {
	return fmt.Errorf("%s is no longer supported; put a Dockerfile next to kit.yml", field)
}

// RenderKitYAML is the kit.yml Jam writes for a definition it holds (the
// default kit, a JSON push's re-render, a migrated version): sk as YAML
// without a legacy name.
func RenderKitYAML(sk StudioKit) ([]byte, error) {
	sk.LegacyName = ""
	return yaml.Marshal(sk)
}

var (
	errNoBaseKit    = errors.New("kit.yml: no base.kit to rewrite")
	errBaseKitShape = errors.New("kit.yml: write base.kit as a one-line plain or quoted value, without escapes")
	errBaseKitValue = errors.New("kit.yml: the new base.kit value cannot be written in place")
)

// SetBaseKit returns kitYML with its base.kit value replaced by value (a
// tag-safe name, name@vN, or a ../path: all plain-safe YAML), keeping every
// other byte and the value's quote style. base.kit must be a one-line plain,
// single- or double-quoted scalar without escapes.
func SetBaseKit(kitYML []byte, value string) ([]byte, error) {
	base := baseNode(kitYML)
	if base == nil {
		return nil, errNoBaseKit
	}
	kit := MapValue(base, "kit")
	if kit == nil || kit.Kind != yaml.ScalarNode || kit.Value == "" || kit.ShortTag() == "!!null" {
		return nil, errNoBaseKit
	}
	var q string
	switch kit.Style {
	case 0:
	case yaml.DoubleQuotedStyle:
		q = `"`
	case yaml.SingleQuotedStyle:
		q = "'"
	default:
		return nil, errBaseKitShape
	}
	start, ok := byteOffset(kitYML, kit.Line, kit.Column)
	raw := q + kit.Value + q
	if !ok || !bytes.HasPrefix(kitYML[start:], []byte(raw)) {
		return nil, errBaseKitShape
	}
	out := make([]byte, 0, len(kitYML)-len(raw)+len(value)+2)
	out = append(out, kitYML[:start]...)
	out = append(out, q+value+q...)
	out = append(out, kitYML[start+len(raw):]...)
	// value must read back as itself in the kept quote style (a "#", ": " or
	// quote in it would change the document): check, never trust.
	if got := MapValue(baseNode(out), "kit"); got == nil || got.Kind != yaml.ScalarNode || got.Value != value || got.ShortTag() == "!!null" {
		return nil, errBaseKitValue
	}
	return out, nil
}

// baseNode is the value node of the document's top-level base key, or nil.
func baseNode(kitYML []byte) *yaml.Node {
	var doc yaml.Node
	if yaml.Unmarshal(kitYML, &doc) != nil || len(doc.Content) == 0 {
		return nil
	}
	return MapValue(doc.Content[0], "base")
}

// MapValue is YAML mapping node m's value for key, or nil (also when m is nil
// or not a mapping).
func MapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// byteOffset maps yaml.v3's 1-based line and column (the column counts
// characters) to a byte offset in b.
func byteOffset(b []byte, line, col int) (int, bool) {
	off := 0
	for l := 1; l < line; l++ {
		i := bytes.IndexByte(b[off:], '\n')
		if i < 0 {
			return 0, false
		}
		off += i + 1
	}
	for c := 1; c < col; c++ {
		if off >= len(b) || b[off] == '\n' {
			return 0, false
		}
		_, size := utf8.DecodeRune(b[off:])
		off += size
	}
	return off, true
}
