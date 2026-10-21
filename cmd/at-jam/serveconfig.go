package main

import (
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
// paths in order; merged is YAML for parseServeConfig/unknownServeKeys.
func loadServeConfigs(paths []string, read func(string) ([]byte, error)) (merged []byte, portable map[string]any, err error) {
	docs := make([]map[string]any, 0, len(paths))
	for _, p := range paths {
		data, err := read(p)
		if err != nil {
			return nil, nil, err
		}
		doc := map[string]any{}
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", p, err)
		}
		if doc == nil {
			doc = map[string]any{}
		}
		docs = append(docs, doc)
	}
	if len(docs) > 1 {
		if r := jam.HostKeyRefusals(paths[0], docs[0]); len(r) > 0 {
			return nil, nil, fmt.Errorf("%s", r[0])
		}
	}
	all := jam.MergeServeConfigs(docs...)
	merged, err = yaml.Marshal(all)
	if err != nil {
		return nil, nil, err
	}
	return merged, jam.PortableServeConfig(all), nil
}
