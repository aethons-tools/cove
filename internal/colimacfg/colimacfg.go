// Package colimacfg edits a colima config (colima.yaml) so its VM can run
// docker:true sandboxes: a provision hook that installs Sysbox on every boot and
// the sysbox-runc runtime registered through colima's docker: passthrough (colima
// regenerates /etc/docker/daemon.json each start, so that is the durable seam).
// It is pure bytes-in/bytes-out; the at-jam colima command owns the file I/O.
package colimacfg

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// MinSysboxVersion is the oldest Sysbox CE proven to run a docker:true sandbox;
// 0.6.x predates time-namespace support and fails container create.
const MinSysboxVersion = "0.7.1"

// SysboxRuntimePath is where the Sysbox .deb installs sysbox-runc in the VM.
const SysboxRuntimePath = "/usr/bin/sysbox-runc"

// Marker identifies the provision hook this package owns, so a re-run replaces
// it rather than appending a duplicate, and leaves the operator's hooks alone.
const Marker = "# managed by at-jam colima setup-docker — re-run it to change; edits here are overwritten"

// Options parameterizes Apply.
type Options struct {
	// SysboxVersion is the Sysbox CE release the hook installs (MAJOR.MINOR.PATCH, ≥ MinSysboxVersion).
	SysboxVersion string
}

// Change is one human-readable edit Apply made.
type Change struct{ What string }

var semver = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)

// ValidateVersion rejects a version that isn't MAJOR.MINOR.PATCH or is older
// than MinSysboxVersion.
func ValidateVersion(v string) error {
	got, ok := parseVersion(v)
	if !ok {
		return fmt.Errorf("sysbox version %q: want MAJOR.MINOR.PATCH (e.g. %s)", v, MinSysboxVersion)
	}
	floor, _ := parseVersion(MinSysboxVersion)
	for i := range got {
		if got[i] != floor[i] {
			if got[i] < floor[i] {
				return fmt.Errorf("sysbox version %s is older than the %s floor (0.6.x lacks time-namespace support)", v, MinSysboxVersion)
			}
			return nil
		}
	}
	return nil
}

func parseVersion(v string) ([3]int, bool) {
	m := semver.FindStringSubmatch(v)
	if m == nil {
		return [3]int{}, false
	}
	var out [3]int
	for i := range out {
		out[i], _ = strconv.Atoi(m[i+1])
	}
	return out, true
}

// HookScript renders the provision hook's script for a Sysbox version. It is
// idempotent on the binary, so it installs once and is a no-op on later boots.
func HookScript(version string) string {
	return `#!/usr/bin/env bash
` + Marker + `
set -euo pipefail
command -v sysbox-runc >/dev/null 2>&1 && exit 0
arch="$(dpkg --print-architecture)"
ver="` + version + `"
apt-get update && apt-get install -y jq
curl -fsSL -o /tmp/sysbox.deb \
  "https://github.com/nestybox/sysbox/releases/download/v${ver}/sysbox-ce_${ver}.linux_${arch}.deb"
apt-get install -y /tmp/sysbox.deb
`
}

// Apply returns in with the Sysbox provision hook and the sysbox-runc runtime
// set, plus the changes it made. When nothing needs changing it returns in
// unchanged (byte for byte) and no changes, so a no-op run never reformats the
// file. Comments survive a changing run; indentation and quoting are normalized.
func Apply(in []byte, o Options) ([]byte, []Change, error) {
	if err := ValidateVersion(o.SysboxVersion); err != nil {
		return nil, nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(in, &doc); err != nil {
		return nil, nil, fmt.Errorf("parse colima config: %w", err)
	}
	if doc.Kind == 0 { // empty file
		doc = yaml.Node{Kind: yaml.DocumentNode}
	}
	if len(doc.Content) == 0 || isNull(doc.Content[0]) { // comment-only, or a bare `---`
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("colima config: top level is not a mapping")
	}
	var changes []Change
	c, err := ensureRuntime(root)
	if err != nil {
		return nil, nil, err
	}
	changes = append(changes, c...)
	c, err = ensureHook(root, o.SysboxVersion)
	if err != nil {
		return nil, nil, err
	}
	changes = append(changes, c...)
	if len(changes) == 0 {
		return in, nil, nil
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, nil, fmt.Errorf("encode colima config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, nil, fmt.Errorf("encode colima config: %w", err)
	}
	return buf.Bytes(), changes, nil
}

// ensureRuntime sets docker.runtimes.sysbox-runc.path, creating the docker and
// runtimes mappings as needed and leaving every other key alone.
func ensureRuntime(root *yaml.Node) ([]Change, error) {
	docker, err := mapping(root, "docker")
	if err != nil {
		return nil, err
	}
	runtimes, err := mapping(docker, "runtimes")
	if err != nil {
		return nil, fmt.Errorf("docker.%w", err)
	}
	sysbox, err := mapping(runtimes, "sysbox-runc")
	if err != nil {
		return nil, fmt.Errorf("docker.runtimes.%w", err)
	}
	path := value(sysbox, "path")
	switch {
	case path == nil:
		setScalar(sysbox, "path", SysboxRuntimePath)
		return []Change{{What: "registered docker.runtimes.sysbox-runc (path " + SysboxRuntimePath + ")"}}, nil
	case path.Kind != yaml.ScalarNode || path.Value != SysboxRuntimePath:
		old := path.Value
		*path = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: SysboxRuntimePath}
		return []Change{{What: fmt.Sprintf("changed docker.runtimes.sysbox-runc path %q → %q", old, SysboxRuntimePath)}}, nil
	}
	return nil, nil
}

// ensureHook adds or replaces the one managed provision entry, keeping the
// operator's other hooks and their order. A managed entry is a marked one or a
// legacy hand-written one (it installs Sysbox from the nestybox/sysbox release
// URL); the first is adopted in place and any further ones are dropped.
func ensureHook(root *yaml.Node, version string) ([]Change, error) {
	prov := value(root, "provision")
	if prov == nil || isNull(prov) {
		prov = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		set(root, "provision", prov)
	}
	if prov.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("colima config: provision is not a list")
	}
	want := HookScript(version)
	var changes []Change
	var kept []*yaml.Node
	found := false
	for _, e := range prov.Content {
		marked := isMarked(e)
		if !marked && !isLegacy(e) {
			kept = append(kept, e)
			continue
		}
		if found {
			changes = append(changes, Change{What: "removed a duplicate Sysbox provision hook"})
			continue
		}
		found = true
		switch {
		case !marked:
			*e = *hookNode(want)
			changes = append(changes, Change{What: "replaced a hand-written Sysbox provision hook with the managed one (Sysbox " + version + ")"})
		case value(e, "mode") == nil || value(e, "mode").Value != "system" || value(e, "script").Value != want:
			*e = *hookNode(want)
			changes = append(changes, Change{What: "updated the Sysbox provision hook (Sysbox " + version + ")"})
		}
		kept = append(kept, e)
	}
	if !found {
		kept = append(kept, hookNode(want))
		changes = append(changes, Change{What: "added the Sysbox provision hook (Sysbox " + version + ")"})
	}
	prov.Content = kept
	if prov.Style == yaml.FlowStyle { // `provision: []` — render the entries as a block list
		prov.Style = 0
	}
	return changes, nil
}

func isMarked(e *yaml.Node) bool { return scriptContains(e, Marker) }

// isLegacy matches a hand-written hook that installs Sysbox: every manual recipe
// downloads the release from github.com/nestybox/sysbox.
func isLegacy(e *yaml.Node) bool { return scriptContains(e, "nestybox/sysbox") }

func scriptContains(e *yaml.Node, sub string) bool {
	if e.Kind != yaml.MappingNode {
		return false
	}
	s := value(e, "script")
	return s != nil && s.Kind == yaml.ScalarNode && strings.Contains(s.Value, sub)
}

func hookNode(script string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setScalar(n, "mode", "system")
	set(n, "script", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: script, Style: yaml.LiteralStyle})
	return n
}

// mapping returns m[key] as a mapping, creating it when absent or null (colima's
// fresh config writes `docker: {}`). A present non-mapping value is an error.
func mapping(m *yaml.Node, key string) (*yaml.Node, error) {
	v := value(m, key)
	if v == nil || isNull(v) {
		n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		set(m, key, n)
		return n, nil
	}
	if v.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s is not a mapping in the colima config", key)
	}
	if v.Style == yaml.FlowStyle { // `docker: {}` — render the new keys as a block
		v.Style = 0
	}
	return v, nil
}

func isNull(n *yaml.Node) bool { return n.Kind == yaml.ScalarNode && n.Tag == "!!null" }

func value(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func set(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			// Keep the value's comments when replacing a null/{} placeholder.
			v.LineComment, v.FootComment = m.Content[i+1].LineComment, m.Content[i+1].FootComment
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
}

func setScalar(m *yaml.Node, key, v string) {
	set(m, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v})
}
