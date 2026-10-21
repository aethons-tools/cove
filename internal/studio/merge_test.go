package studio

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/kit"
)

func noteNames(ns []KitNote) []string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, n.Name)
	}
	return out
}

func TestMerge(t *testing.T) {
	root := StudioKit{Kind: "studio", Egress: []string{"github.com"}, BuildArgs: map[string]string{"GO_VERSION": "1.26", "A": "root"},
		Secrets: map[string]kit.SecretConfig{"S": {Description: "root"}}, Prompt: "root prompt",
		Notes:      []KitNote{{Name: "a.md", Body: "ra"}, {Name: "b.md", Body: "rb"}},
		MCPServers: map[string]kit.MCPServer{"docs": {Type: "http", URL: "https://root"}}}
	mid := StudioKit{Kind: "studio", Base: Base{Kit: "root"}, Egress: []string{"registry.npmjs.org", "github.com"}, BuildArgs: map[string]string{"NODE_VERSION": "22"}}
	leaf := StudioKit{Kind: "studio", Base: Base{Kit: "mid"}, BuildArgs: map[string]string{"A": "leaf"},
		Secrets: map[string]kit.SecretConfig{"S": {Description: "leaf"}, "T": {}}, Prompt: "leaf prompt",
		Notes:      []KitNote{{Name: "b.md", Body: "lb"}, {Name: "c.md", Body: "lc"}},
		MCPServers: map[string]kit.MCPServer{"docs": {Type: "http", URL: "https://leaf"}}}
	m := Merge([]StudioKit{root, mid, leaf})
	if !slices.Equal(m.Egress, []string{"github.com", "registry.npmjs.org"}) {
		t.Errorf("egress %v", m.Egress)
	}
	if m.BuildArgs["A"] != "leaf" || m.BuildArgs["GO_VERSION"] != "1.26" || m.BuildArgs["NODE_VERSION"] != "22" {
		t.Errorf("build-args %v", m.BuildArgs)
	}
	if m.Secrets["S"].Description != "leaf" || len(m.Secrets) != 2 {
		t.Errorf("secrets %v", m.Secrets)
	}
	if m.Prompt != "root prompt\n\nleaf prompt" {
		t.Errorf("prompt %q", m.Prompt)
	}
	if got := noteNames(m.Notes); !slices.Equal(got, []string{"a.md", "b.md", "c.md"}) || m.Notes[1].Body != "lb" {
		t.Errorf("notes %v %q", noteNames(m.Notes), m.Notes[1].Body)
	}
	if m.MCPServers["docs"].URL != "https://leaf" {
		t.Errorf("mcp %v", m.MCPServers)
	}
	if m.Base.Kit != "" {
		t.Error("merged base keeps the parent ref")
	}
	if leaf.BuildArgs["GO_VERSION"] != "" || root.BuildArgs["A"] != "root" || len(leaf.Secrets) != 2 {
		t.Error("Merge mutated its inputs")
	}
}

func TestMergeEmptyPromptsSkippedAndSingle(t *testing.T) {
	a := StudioKit{Kind: "studio", Prompt: ""}
	b := StudioKit{Kind: "studio", Base: Base{Kit: "a"}, Prompt: "only"}
	if m := Merge([]StudioKit{a, b}); m.Prompt != "only" {
		t.Errorf("prompt %q", m.Prompt)
	}
	if m := Merge([]StudioKit{b}); m.Prompt != "only" {
		t.Errorf("single %q", m.Prompt)
	}
}

func TestMergeKeepsLastOwnBase(t *testing.T) {
	root := StudioKit{Kind: "studio", Base: Base{Image: "r"}}
	leaf := StudioKit{Kind: "studio", Base: Base{Kit: "root", Context: "ctx"}}
	m := Merge([]StudioKit{root, leaf})
	if m.Base.Context != "ctx" || m.Base.Image != "" || m.Base.Kit != "" {
		t.Errorf("base %+v", m.Base)
	}
}

func TestChainDigest(t *testing.T) {
	h := harnessinstall.Default()
	solo := StudioKit{Kind: "studio", Egress: []string{"github.com"}}
	if ChainDigest([]StudioKit{solo}, h) != BuildDigest(solo, h) {
		t.Fatal("a chain of one must equal BuildDigest")
	}
	child := StudioKit{Kind: "studio", Base: Base{Kit: "p"}}
	p1 := StudioKit{Kind: "studio", Egress: []string{"a.example"}}
	p2 := StudioKit{Kind: "studio", Egress: []string{"b.example"}}
	if ChainDigest([]StudioKit{p1, child}, h) == ChainDigest([]StudioKit{p2, child}, h) {
		t.Fatal("parent change must change the child digest")
	}
	withPrompt := p1
	withPrompt.Prompt = "x"
	if ChainDigest([]StudioKit{p1, child}, h) != ChainDigest([]StudioKit{withPrompt, child}, h) {
		t.Fatal("prompt is not build-relevant")
	}
	// order-normalised egress, and nil vs empty, hash identically
	e1 := StudioKit{Kind: "studio", Egress: []string{"b", "a", "a"}}
	e2 := StudioKit{Kind: "studio", Egress: []string{"a", "b"}}
	if ChainDigest([]StudioKit{e1, child}, h) != ChainDigest([]StudioKit{e2, child}, h) {
		t.Fatal("egress not normalised")
	}
	if ChainDigest([]StudioKit{p1, child}, h) == BuildDigest(child, h) {
		t.Fatal("chain digest must differ from the child alone")
	}
}

func TestBuildDigestUnchangedWithoutKit(t *testing.T) {
	// Kit is omitempty: an unchained kit's Base JSON is exactly as before.
	b, err := json.Marshal(StudioKit{Kind: "studio", Base: Base{Image: "r"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); strings.Contains(got, `"kit"`) {
		t.Errorf("kit leaked into json: %s", got)
	}
}
