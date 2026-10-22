package main

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/aethons-tools/cove/internal/jam"
)

// configPaths is a repeatable --config flag.
type configPaths []string

func (p *configPaths) String() string { return strings.Join(*p, ",") }

func (p *configPaths) Set(v string) error {
	*p = append(*p, v)
	return nil
}

// loadServeConfigs reads, host-key-checks (first of two or more) and merges
// paths in order; merged is YAML for parseServeConfig/unknownServeKeys. A
// single path passes its bytes through unchanged, so scalar text and error
// positions are exactly as before; several paths merge as yaml.Node trees so
// scalar text and style survive the merge.
func loadServeConfigs(paths []string, read func(string) ([]byte, error)) (merged []byte, portable map[string]any, err error) {
	raws := make([][]byte, len(paths))
	for i, p := range paths {
		if raws[i], err = read(p); err != nil {
			return nil, nil, err
		}
	}
	if len(paths) == 1 {
		var doc map[string]any
		if yaml.Unmarshal(raws[0], &doc) != nil {
			// parseServeConfig reports the error with its own position.
			return raws[0], nil, nil
		}
		return raws[0], jam.PortableServeConfig(doc), nil
	}
	docs := make([]map[string]any, len(paths))
	var root *yaml.Node
	for i, p := range paths {
		if err := yaml.Unmarshal(raws[i], &docs[i]); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", p, err)
		}
		if docs[i] == nil {
			docs[i] = map[string]any{}
		}
		var n yaml.Node
		if err := yaml.Unmarshal(raws[i], &n); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", p, err)
		}
		m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if n.Kind == yaml.DocumentNode && len(n.Content) == 1 && n.Content[0].Kind == yaml.MappingNode {
			m = n.Content[0]
		}
		if root == nil {
			root = m
		} else {
			mergeNodes(root, m)
		}
	}
	if r := jam.HostKeyRefusals(paths[0], docs[0]); len(r) > 0 {
		return nil, nil, errors.New(r[0])
	}
	if merged, err = yaml.Marshal(root); err != nil {
		return nil, nil, err
	}
	return merged, jam.PortableServeConfig(jam.MergeServeConfigs(docs...)), nil
}

// mergeNodes merges mapping src into dst: mappings merge by key, anything
// else replaces.
func mergeNodes(dst, src *yaml.Node) {
	for i := 0; i+1 < len(src.Content); i += 2 {
		k, v := src.Content[i], src.Content[i+1]
		found := false
		for j := 0; j+1 < len(dst.Content); j += 2 {
			if dst.Content[j].Value != k.Value {
				continue
			}
			found = true
			if dv := dst.Content[j+1]; dv.Kind == yaml.MappingNode && v.Kind == yaml.MappingNode {
				mergeNodes(dv, v)
			} else {
				dst.Content[j+1] = v
			}
			break
		}
		if !found {
			dst.Content = append(dst.Content, k, v)
		}
	}
}
