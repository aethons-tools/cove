package colimacfg

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var opts = Options{SysboxVersion: MinSysboxVersion}

// freshConfig mirrors the shape of colima's generated default config: comments,
// an empty `docker: {}` and an empty `provision: []`.
const freshConfig = `# Number of CPUs to be allocated to the virtual machine.
cpu: 2

# Docker daemon configuration that maps directly to daemon.json.
docker: {}

# Initial provisioning scripts to run.
provision: []

disk: 100
`

// parsed is the decoded shape the assertions inspect.
type parsed struct {
	Docker    map[string]any `yaml:"docker"`
	Provision []struct {
		Mode   string `yaml:"mode"`
		Script string `yaml:"script"`
	} `yaml:"provision"`
	CPU  int `yaml:"cpu"`
	Disk int `yaml:"disk"`
}

func decode(t *testing.T, b []byte) parsed {
	t.Helper()
	var p parsed
	if err := yaml.Unmarshal(b, &p); err != nil {
		t.Fatalf("output is not valid YAML: %v\n%s", err, b)
	}
	return p
}

func runtimePath(p parsed) any {
	rt, _ := p.Docker["runtimes"].(map[string]any)
	sb, _ := rt["sysbox-runc"].(map[string]any)
	return sb["path"]
}

func apply(t *testing.T, in string, o Options) (string, []Change) {
	t.Helper()
	out, ch, err := Apply([]byte(in), o)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return string(out), ch
}

func TestApplyFreshConfig(t *testing.T) {
	out, ch := apply(t, freshConfig, opts)
	if len(ch) != 2 {
		t.Fatalf("changes = %+v, want runtime + hook", ch)
	}
	p := decode(t, []byte(out))
	if runtimePath(p) != SysboxRuntimePath {
		t.Fatalf("runtime path = %v\n%s", runtimePath(p), out)
	}
	if len(p.Provision) != 1 || p.Provision[0].Mode != "system" || p.Provision[0].Script != HookScript(MinSysboxVersion) {
		t.Fatalf("provision = %+v", p.Provision)
	}
	if p.CPU != 2 || p.Disk != 100 {
		t.Fatalf("other keys lost: %+v", p)
	}
	for _, c := range []string{"# Number of CPUs", "# Docker daemon configuration", "# Initial provisioning"} {
		if !strings.Contains(out, c) {
			t.Fatalf("comment %q lost:\n%s", c, out)
		}
	}
	if !strings.Contains(out, "script: |") {
		t.Fatalf("script must render as a literal block:\n%s", out)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	once, _ := apply(t, freshConfig, opts)
	twice, ch := apply(t, once, opts)
	if len(ch) != 0 || twice != once {
		t.Fatalf("second Apply changed things: %+v\n%s", ch, twice)
	}
}

func TestApplyNoOpReturnsInputBytes(t *testing.T) {
	// Already set up, but formatted unusually (4-space indent, quoted path):
	// a no-op must not reformat it.
	in, _ := apply(t, freshConfig, opts)
	in = strings.Replace(in, "path: /usr/bin/sysbox-runc", `path: "/usr/bin/sysbox-runc"`, 1)
	out, ch := apply(t, in, opts)
	if len(ch) != 0 || out != in {
		t.Fatalf("no-op must return input bytes: %+v\n%s", ch, out)
	}
}

func TestApplyReplacesOldVersionHook(t *testing.T) {
	old, _ := apply(t, freshConfig, opts)
	out, ch := apply(t, old, Options{SysboxVersion: "0.8.0"})
	if len(ch) != 1 || !strings.Contains(ch[0].What, "updated") {
		t.Fatalf("changes = %+v", ch)
	}
	p := decode(t, []byte(out))
	if len(p.Provision) != 1 || !strings.Contains(p.Provision[0].Script, `ver="0.8.0"`) {
		t.Fatalf("provision = %+v", p.Provision)
	}
}

func TestApplyKeepsUserHooksAndDockerKeys(t *testing.T) {
	in := `docker:
  features:
    buildkit: true
  runtimes:
    crun:
      path: /usr/bin/crun
provision:
  - mode: user
    script: echo first
  - mode: system
    script: echo second
`
	out, _ := apply(t, in, opts)
	p := decode(t, []byte(out))
	if f, _ := p.Docker["features"].(map[string]any); f["buildkit"] != true {
		t.Fatalf("docker.features lost:\n%s", out)
	}
	if rt, _ := p.Docker["runtimes"].(map[string]any); rt["crun"] == nil {
		t.Fatalf("other runtime lost:\n%s", out)
	}
	if len(p.Provision) != 3 || p.Provision[0].Script != "echo first" || p.Provision[1].Script != "echo second" || !strings.Contains(p.Provision[2].Script, Marker) {
		t.Fatalf("hook order/content wrong: %+v", p.Provision)
	}
}

func TestApplyEmptyFile(t *testing.T) {
	for _, in := range []string{"", "# only a comment\n", "---\n"} {
		out, ch := apply(t, in, opts)
		p := decode(t, []byte(out))
		if len(ch) != 2 || runtimePath(p) != SysboxRuntimePath || len(p.Provision) != 1 {
			t.Fatalf("empty file %q: %+v\n%s", in, ch, out)
		}
	}
}

func TestApplyOverwritesDifferentRuntimePath(t *testing.T) {
	out, ch := apply(t, "docker:\n  runtimes:\n    sysbox-runc:\n      path: /opt/sysbox-runc\n", opts)
	if runtimePath(decode(t, []byte(out))) != SysboxRuntimePath {
		t.Fatalf("path not overwritten:\n%s", out)
	}
	if len(ch) == 0 || !strings.Contains(ch[0].What, "/opt/sysbox-runc") {
		t.Fatalf("overwrite must be reported: %+v", ch)
	}
}

func TestApplyDedupesMarkedHooks(t *testing.T) {
	once, _ := apply(t, freshConfig, opts)
	p := decode(t, []byte(once))
	var doc yaml.Node // re-encode with a second marked entry appended
	if err := yaml.Unmarshal([]byte(once), &doc); err != nil {
		t.Fatal(err)
	}
	prov := value(doc.Content[0], "provision")
	prov.Content = append(prov.Content, hookNode(p.Provision[0].Script))
	b, _ := yaml.Marshal(&doc)
	out, ch := apply(t, string(b), opts)
	if got := decode(t, []byte(out)); len(got.Provision) != 1 {
		t.Fatalf("duplicates kept: %+v", got.Provision)
	}
	if len(ch) != 1 || !strings.Contains(ch[0].What, "duplicate") {
		t.Fatalf("changes = %+v", ch)
	}
}

func TestApplyRejectsNonMappings(t *testing.T) {
	for _, in := range []string{"docker: true\n", "docker:\n  runtimes: [a]\n", "provision: nope\n", "- a\n"} {
		if _, _, err := Apply([]byte(in), opts); err == nil {
			t.Errorf("Apply(%q) must error", in)
		}
	}
}

func TestValidateVersion(t *testing.T) {
	for _, v := range []string{"0.7.1", "0.7.2", "0.8.0", "1.0.0"} {
		if err := ValidateVersion(v); err != nil {
			t.Errorf("ValidateVersion(%q) = %v", v, err)
		}
	}
	for _, v := range []string{"0.6.9", "0.7.0", "latest", "1.2", "v0.7.1", ""} {
		if err := ValidateVersion(v); err == nil {
			t.Errorf("ValidateVersion(%q) must error", v)
		}
	}
}
