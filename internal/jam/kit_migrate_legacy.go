package jam

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"path"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/studio"
)

// The fixed reasons a pre-tree version is kept broken (Kit.Broken). They
// name no path and quote no content.
var (
	errLegacyBase64  = errors.New("its base.context is not valid base64")
	errLegacyTar     = errors.New("its base.context is not a readable tar.gz")
	errLegacyLink    = errors.New("its base.context holds a link or an irregular file")
	errLegacyUnsafe  = errors.New("its base.context holds an unsafe path")
	errLegacyTree    = errors.New("its base.context-files is not a tree of files")
	errLegacyTwice   = errors.New("its base holds more than one packed context")
	errLegacyAlias   = errors.New("its packed context is reached through an alias or a merge key")
	errLegacyIgnore  = errors.New("its context's .dockerignore cannot be applied")
	errLegacyKitYML  = errors.New("its context holds a kit.yml at the root")
	errLegacyLimits  = errors.New("its context does not fit a kit tree (a safe path for every file; at most 2000 files, 64 MiB, 4 MiB a file)")
	legacyMaxHeaders = 4 * kittree.MaxEntries // tar headers read, directories and duplicates included
	legacyMaxDepth   = 64                     // nesting of an inline context-files tree
)

// legacyKitFiles is a pre-tree version's files. kit.yml is its stored config
// with the packed-context fields taken out, re-rendered (studio.RenderKitYAML).
// Beside it go the files of its base.context (a base64 tar.gz) or
// base.contextFiles (an inline tree; the reserved dockerfile key is the
// Dockerfile, every file 0644), with the context's own .dockerignore applied
// (kittree.Ignore: the build never saw those files).
//
// A config that does not parse once those fields are out keeps its stripped
// text as kit.yml (the same YAML, re-serialized), so it stays exactly as
// invalid as it was and no packed base64 reaches the tree. Text that is not
// a YAML mapping has no context fields to take out and is kept verbatim. A
// context that cannot be read is one of the errLegacy reasons.
func legacyKitFiles(text string) ([]kittree.File, error) {
	var doc yaml.Node
	if yaml.Unmarshal([]byte(text), &doc) != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return []kittree.File{{Path: kittree.KitFile, Mode: 0o644, Data: []byte(text)}}, nil
	}
	b64, inline, err := takeLegacyContext(doc.Content[0])
	if err != nil {
		return nil, err
	}
	stripped, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, errLegacyTree
	}
	if studio.RefuseContextFields(stripped) != nil {
		return nil, errLegacyAlias
	}
	yml := stripped
	if sk, err := studio.ParseStudioKit(stripped); err == nil {
		if r, err := studio.RenderKitYAML(sk); err == nil && studio.RefuseContextFields(r) == nil {
			yml = r
		}
	}
	files := []kittree.File{{Path: kittree.KitFile, Mode: 0o644, Data: yml}}
	var ctx []kittree.File
	switch {
	case b64 != nil:
		ctx, err = legacyContextTar(b64.Value)
	case inline != nil:
		ctx, err = legacyContextTree(inline, "", 0)
	}
	if err != nil {
		return nil, err
	}
	for _, f := range ctx {
		if f.Path == kittree.KitFile {
			return nil, errLegacyKitYML
		}
	}
	files, err = kittree.Ignore(append(files, ctx...))
	if err != nil {
		return nil, errLegacyIgnore
	}
	return files, nil
}

// takeLegacyContext removes every packed-context key from every base mapping
// of top (base.context, base.contextFiles / context-files, base.context-dir)
// and returns the context it found: the base64 scalar or the inline tree. A
// base left empty is removed too. More than one context is errLegacyTwice.
func takeLegacyContext(top *yaml.Node) (b64, inline *yaml.Node, err error) {
	found := 0
	for i := 0; i+1 < len(top.Content); {
		if top.Content[i].Value != "base" || top.Content[i+1].Kind != yaml.MappingNode {
			i += 2
			continue
		}
		base := top.Content[i+1]
		for j := 0; j+1 < len(base.Content); {
			switch base.Content[j].Value {
			case "context":
				b64, found = base.Content[j+1], found+1
			case "contextFiles", "context-files":
				inline, found = base.Content[j+1], found+1
			case "context-dir":
			default:
				j += 2
				continue
			}
			base.Content = append(base.Content[:j], base.Content[j+2:]...)
		}
		if len(base.Content) == 0 {
			top.Content = append(top.Content[:i], top.Content[i+2:]...)
			continue
		}
		i += 2
	}
	if found > 1 {
		return nil, nil, errLegacyTwice
	}
	if (b64 != nil && b64.Kind != yaml.ScalarNode) || (inline != nil && inline.Kind == yaml.AliasNode) {
		return nil, nil, errLegacyAlias
	}
	return b64, inline, nil
}

// legacyContextTar reads a packed context with the old pre-flight's safety
// rules: no absolute or ".." names, no links or irregular entries, the tree
// limits, enforced as the stream is read. At most legacyMaxHeaders headers
// are read, and every file's bytes count against kittree.MaxTotal, a
// replaced duplicate's included, so a gzip bomb trips a cap before it fills
// memory. Directory entries and "./" prefixes are dropped, and a later
// duplicate replaces an earlier one (extraction keeps the last).
func legacyContextTar(b64 string) ([]kittree.File, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, errLegacyBase64
	}
	gr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, errLegacyTar
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	var out []kittree.File
	at := map[string]int{}
	var total int64
	for headers := 0; ; headers++ {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, errLegacyTar
		}
		if headers == legacyMaxHeaders {
			return nil, errLegacyLimits
		}
		if path.IsAbs(hdr.Name) || strings.Contains("/"+hdr.Name+"/", "/../") {
			return nil, errLegacyUnsafe
		}
		name := path.Clean(hdr.Name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
		default:
			return nil, errLegacyLink
		}
		if kittree.CheckPath(name) != nil {
			return nil, errLegacyUnsafe
		}
		data, err := io.ReadAll(io.LimitReader(tr, kittree.MaxFile+1))
		if err != nil {
			return nil, errLegacyTar
		}
		if int64(len(data)) > kittree.MaxFile {
			return nil, errLegacyLimits
		}
		if total += int64(len(data)); total > kittree.MaxTotal {
			return nil, errLegacyLimits
		}
		f := kittree.File{Path: name, Mode: kittree.NormMode(fs.FileMode(hdr.Mode)), Data: data}
		if i, dup := at[name]; dup {
			out[i] = f
			continue
		}
		if len(out) == kittree.MaxEntries {
			return nil, errLegacyLimits
		}
		at[name] = len(out)
		out = append(out, f)
	}
}

// legacyContextTree flattens an inline context-files tree: each key one safe
// path segment, a string a file, a mapping a directory.
func legacyContextTree(n *yaml.Node, prefix string, depth int) ([]kittree.File, error) {
	if n.Kind != yaml.MappingNode || depth > legacyMaxDepth {
		return nil, errLegacyTree
	}
	var out []kittree.File
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i].Value, n.Content[i+1]
		if strings.Contains(k, "/") || kittree.CheckPath(k) != nil {
			return nil, errLegacyUnsafe
		}
		p := path.Join(prefix, k)
		if prefix == "" && k == "dockerfile" {
			p = kittree.Dockerfile
		}
		switch v.Kind {
		case yaml.ScalarNode:
			out = append(out, kittree.File{Path: p, Mode: 0o644, Data: []byte(v.Value)})
		case yaml.MappingNode:
			sub, err := legacyContextTree(v, p, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, sub...)
		default:
			return nil, errLegacyTree
		}
		if len(out) > kittree.MaxEntries {
			return nil, errLegacyLimits
		}
	}
	return out, nil
}

// legacyKitTree is legacyKitFiles as a tree. Files that do not fit a tree
// are errLegacyLimits.
func legacyKitTree(text string) (kittree.Tree, map[string][]byte, error) {
	files, err := legacyKitFiles(text)
	if err != nil {
		return kittree.Tree{}, nil, err
	}
	tree, blobs, err := kittree.Build(files)
	if err != nil {
		return kittree.Tree{}, nil, errLegacyLimits
	}
	return tree, blobs, nil
}
