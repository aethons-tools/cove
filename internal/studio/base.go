package studio

import (
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Base names the image a kit builds FROM. Kit names a parent kit ("name"
// follows its current version, "name@v<N>" pins N); Image is a prebuilt,
// provenance-gated ref; both empty is the blessed default. A Dockerfile next
// to kit.yml is the kit's own build context (StudioKit.BuildContext), built
// FROM ${COVE_BASE_IMAGE}: the blessed base, or the parent's stage.
type Base struct {
	Kit   string `yaml:"kit,omitempty" json:"kit,omitempty"`
	Image string `yaml:"image,omitempty" json:"image,omitempty"`
}

// BaseKind is which base form a Base selects.
type BaseKind int

const (
	BaseDefault BaseKind = iota // blessed default
	BaseImage                   // a prebuilt gated ref
	BaseKit                     // a parent kit's stage (plus the kit's own Dockerfile stage, if any)
)

// Kind reports which base form is set.
func (b Base) Kind() (BaseKind, error) {
	switch {
	case b.Kit != "" && b.Image != "":
		return BaseDefault, fmt.Errorf("base: kit and image are mutually exclusive")
	case b.Kit != "":
		return BaseKit, nil
	case b.Image != "":
		return BaseImage, nil
	}
	return BaseDefault, nil
}

// HasParent reports whether the base names a parent kit.
func (b Base) HasParent() bool { return b.Kit != "" }

// errPathParent is what ParentRef returns for a path parent, which names no
// registry kit; only a git import resolves it.
var errPathParent = errors.New("path parents resolve only in a git import (kit import/track --git)")

// maxParentPath caps a path parent reference.
const maxParentPath = 200

// ParentPath returns the cleaned slash path when Kit is a path parent (starts
// with "."), and false otherwise. It does not judge the path; Validate does.
func (b Base) ParentPath() (string, bool) {
	if !strings.HasPrefix(b.Kit, ".") {
		return "", false
	}
	return path.Clean(b.Kit), true
}

// HasUnsafeChars reports whether s is invalid UTF-8 or holds whitespace, a
// quote, a backtick, or a control, format (Cf) or non-printable character.
func HasUnsafeChars(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if r <= 0x20 || r == 0x7f || r == '"' || r == '\'' || r == '`' || unicode.IsSpace(r) || unicode.IsControl(r) ||
			unicode.Is(unicode.Cf, r) || !unicode.IsPrint(r) {
			return true
		}
	}
	return false
}

// checkParentPath is the syntax check for a path parent: non-empty after the
// dot, no backslash, no space, quote, control or non-printable character (the
// source-dir rules), not absolute, within maxParentPath bytes.
func (b Base) checkParentPath() error {
	p, _ := b.ParentPath()
	if p == "." || strings.ContainsRune(b.Kit, '\\') || HasUnsafeChars(b.Kit) || path.IsAbs(p) || len(b.Kit) > maxParentPath {
		return errors.New("base.kit: not a valid parent path (a path parent must be a non-empty relative path of at most 200 bytes, with no backslash, space, quote or control character)")
	}
	return nil
}

// ParentRef parses Kit into the parent's registry name and pinned version
// (0 means follow the parent's current version).
func (b Base) ParentRef() (name string, version int, err error) {
	ref := b.Kit
	if strings.HasPrefix(ref, ".") {
		return "", 0, errPathParent
	}
	name = ref
	if i := strings.IndexByte(ref, '@'); i >= 0 {
		name = ref[:i]
		v, ok := strings.CutPrefix(ref[i+1:], "v")
		n, convErr := strconv.Atoi(v)
		if !ok || convErr != nil || n < 1 || strconv.Itoa(n) != v {
			return "", 0, fmt.Errorf("base.kit: %q: a pin must be name@v<N> with N >= 1", ref)
		}
		version = n
	}
	if !TagSafeName(name) {
		return "", 0, fmt.Errorf("base.kit: %q is not a valid kit name", ref)
	}
	return name, version, nil
}

// validate enforces the base invariants (one form, a well-formed parent).
// Called from StudioKit.Validate.
func (b Base) validate() error {
	if _, err := b.Kind(); err != nil {
		return err
	}
	if _, isPath := b.ParentPath(); isPath {
		return b.checkParentPath()
	}
	if b.HasParent() {
		if _, _, err := b.ParentRef(); err != nil {
			return err
		}
	}
	return nil
}
