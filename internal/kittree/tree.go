// Package kittree is a kit version's files as a content-addressed tree: the
// model, its hash and limits, reading one in from a directory or a push tar,
// writing a push body and a build context, a read-only billy view, and the
// version diff. It holds no store: blobs come in through a Blobs lookup.
package kittree

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// KitFile is the kit definition at a tree's root.
const KitFile = "kit.yml"

// Dockerfile, next to kit.yml, makes the tree a build context.
const Dockerfile = "Dockerfile"

// The limits of one tree (spec §1). Vars so tests can shrink them; treat them
// as constants.
var (
	MaxEntries       = 2000
	MaxTotal   int64 = 64 << 20
	MaxFile    int64 = 4 << 20
)

// Entry is one file of a tree. Size is not part of the hash.
type Entry struct {
	Path   string      `json:"path"`
	Mode   fs.FileMode `json:"mode"`   // 0o644 or 0o755
	SHA256 string      `json:"sha256"` // hex sha256 of the content
	Size   int64       `json:"size"`
}

// Tree is a version's files, sorted by path.
type Tree struct {
	Entries []Entry `json:"entries"`
}

// File is one file with its content, on its way into a tree.
type File struct {
	Path string
	Mode fs.FileMode
	Data []byte
}

// Blobs looks a blob's bytes up by its hex sha256.
type Blobs func(sha string) ([]byte, bool)

// MapBlobs is a Blobs over m.
func MapBlobs(m map[string][]byte) Blobs {
	return func(sha string) ([]byte, bool) {
		b, ok := m[sha]
		return b, ok
	}
}

// NormMode keeps the two modes git keeps: any executable bit makes 0755.
func NormMode(m fs.FileMode) fs.FileMode {
	if m&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

var errUnsafePath = errors.New("a file has an unsafe path (empty, absolute, a . or .. segment, invalid UTF-8, or a control or format character)")

// CheckPath reports whether p is a clean, slash-separated, relative path of
// valid UTF-8 with no control or format character and no . or .. segment.
// Its error never names p.
func CheckPath(p string) error {
	if p == "" || p == "." || !utf8.ValidString(p) || !fs.ValidPath(p) {
		return errUnsafePath
	}
	for _, r := range p {
		if r != ' ' && (!unicode.IsGraphic(r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)) {
			return errUnsafePath
		}
	}
	return nil
}

// SizeText renders a limit: whole MiB as "N MiB", else "N bytes".
func SizeText(n int64) string {
	if n >= 1<<20 && n%(1<<20) == 0 {
		return strconv.FormatInt(n>>20, 10) + " MiB"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}

func tooManyFiles(n int) error { return fmt.Errorf("%d files (limit %d)", n, MaxEntries) }

func fileTooLarge(p string) error {
	return fmt.Errorf("%s: more than %s (limit %s per file)", p, SizeText(MaxFile), SizeText(MaxFile))
}

func treeTooLarge() error {
	return fmt.Errorf("more than %s in total (limit %s)", SizeText(MaxTotal), SizeText(MaxTotal))
}

// Build validates files as one tree, normalizing modes, and returns it with
// its blobs by sha256. Identical content shares one blob.
func Build(files []File) (Tree, map[string][]byte, error) {
	if len(files) > MaxEntries {
		return Tree{}, nil, tooManyFiles(len(files))
	}
	t := Tree{Entries: make([]Entry, 0, len(files))}
	blobs := make(map[string][]byte, len(files))
	var total int64
	for _, f := range files {
		if err := CheckPath(f.Path); err != nil {
			return Tree{}, nil, err
		}
		n := int64(len(f.Data))
		if n > MaxFile {
			return Tree{}, nil, fileTooLarge(f.Path)
		}
		if total += n; total > MaxTotal {
			return Tree{}, nil, treeTooLarge()
		}
		sum := sha256.Sum256(f.Data)
		sha := hex.EncodeToString(sum[:])
		blobs[sha] = f.Data
		t.Entries = append(t.Entries, Entry{Path: f.Path, Mode: NormMode(f.Mode), SHA256: sha, Size: n})
	}
	slices.SortFunc(t.Entries, func(a, b Entry) int { return strings.Compare(a.Path, b.Path) })
	for i := 1; i < len(t.Entries); i++ {
		if t.Entries[i].Path == t.Entries[i-1].Path {
			return Tree{}, nil, fmt.Errorf("%s: appears twice", t.Entries[i].Path)
		}
	}
	if err := t.Validate(); err != nil {
		return Tree{}, nil, err
	}
	return t, blobs, nil
}

// Hash is the tree's identity: sha256 over the entries in path order, each
// "path NUL mode NUL blob-sha256 LF", mode in octal without a leading zero.
func (t Tree) Hash() string {
	h := sha256.New()
	for _, e := range t.Entries {
		h.Write([]byte(e.Path))
		h.Write([]byte{0})
		h.Write([]byte(strconv.FormatUint(uint64(e.Mode.Perm()), 8)))
		h.Write([]byte{0})
		h.Write([]byte(e.SHA256))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Lookup finds the entry at p.
func (t Tree) Lookup(p string) (Entry, bool) {
	i, ok := slices.BinarySearchFunc(t.Entries, p, func(e Entry, p string) int { return strings.Compare(e.Path, p) })
	if !ok {
		return Entry{}, false
	}
	return t.Entries[i], true
}

// Without is t less the entry at p.
func (t Tree) Without(p string) Tree {
	return Tree{Entries: slices.DeleteFunc(slices.Clone(t.Entries), func(e Entry) bool { return e.Path == p })}
}

// ContextHash is the hash of the build context (the tree minus kit.yml) when
// a Dockerfile sits next to kit.yml; "" when none does.
func (t Tree) ContextHash() string {
	if _, ok := t.Lookup(Dockerfile); !ok {
		return ""
	}
	return t.Without(KitFile).Hash()
}

// Size is the total of the entries' sizes.
func (t Tree) Size() int64 {
	var n int64
	for _, e := range t.Entries {
		n += e.Size
	}
	return n
}

// isSHA256Hex reports whether s is a sha256 in canonical form: 64 lowercase
// hex digits, as Build writes it.
func isSHA256Hex(s string) bool {
	if len(s) != 2*sha256.Size {
		return false
	}
	for i := range len(s) {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Validate checks a tree read back from storage or a backup: sorted unique
// safe paths, the two modes, lowercase hex sums, no file where another file's
// directory is, kit.yml at the root, and the limits.
func (t Tree) Validate() error {
	if len(t.Entries) > MaxEntries {
		return tooManyFiles(len(t.Entries))
	}
	var total int64
	for i, e := range t.Entries {
		if err := CheckPath(e.Path); err != nil {
			return err
		}
		if i > 0 && t.Entries[i-1].Path >= e.Path {
			return fmt.Errorf("%s: entries out of order or repeated", e.Path)
		}
		if e.Mode != 0o644 && e.Mode != 0o755 {
			return fmt.Errorf("%s: mode %o (want 644 or 755)", e.Path, e.Mode)
		}
		if !isSHA256Hex(e.SHA256) {
			return fmt.Errorf("%s: not a sha256", e.Path)
		}
		if e.Size < 0 || e.Size > MaxFile {
			return fileTooLarge(e.Path)
		}
		if total += e.Size; total > MaxTotal {
			return treeTooLarge()
		}
		for d := path.Dir(e.Path); d != "."; d = path.Dir(d) {
			if _, clash := t.Lookup(d); clash {
				return fmt.Errorf("%s: is both a file and a directory", d)
			}
		}
	}
	if _, ok := t.Lookup(KitFile); !ok {
		return errors.New("kit.yml missing at the root")
	}
	return nil
}
