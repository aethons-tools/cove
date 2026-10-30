package main

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type composeFile struct {
	Services map[string]struct {
		Image     string   `yaml:"image"`
		Profiles  []string `yaml:"profiles"`
		Ports     []string `yaml:"ports"`
		Volumes   []string `yaml:"volumes"`
		DependsOn map[string]struct {
			Condition string `yaml:"condition"`
		} `yaml:"depends_on"`
	} `yaml:"services"`
	Volumes map[string]any `yaml:"volumes"`
}

func loadCompose(t *testing.T) composeFile {
	t.Helper()
	raw, err := os.ReadFile("../../dev/docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	var c composeFile
	if err := yaml.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Services["jam"]; !ok {
		t.Fatal("no jam service in dev/docker-compose.yml")
	}
	return c
}

func TestComposeJamServiceIsOptIn(t *testing.T) {
	c := loadCompose(t)
	if got := c.Services["jam"].Profiles; !reflect.DeepEqual(got, []string{"jam"}) {
		t.Errorf("jam profiles = %v, want [jam]", got)
	}
	for _, name := range []string{"postgres", "garage"} {
		if p := c.Services[name].Profiles; len(p) != 0 {
			t.Errorf("%s must not be profile-gated, has %v", name, p)
		}
	}
}

func TestComposeJamPorts(t *testing.T) {
	want := []string{"443:8443", "127.0.0.1:8081:8081"}
	if got := loadCompose(t).Services["jam"].Ports; !reflect.DeepEqual(got, want) {
		t.Errorf("jam ports = %v, want %v", got, want)
	}
}

// Every file the container example config names under /etc/jam is inside one
// of the jam service's mounts, read-only (only the state volume is writable),
// so the config and the compose file can't drift apart.
func TestComposeJamMountsCoverTheConfig(t *testing.T) {
	c := loadCompose(t)
	raw, err := os.ReadFile("../../dev/examples/jam.container.yml.example")
	if err != nil {
		t.Fatal(err)
	}
	var targets []string
	for _, v := range c.Services["jam"].Volumes {
		// source:target[:mode], read from the right: a source may carry a
		// ${VAR:-default} with its own colon.
		parts := strings.Split(v, ":")
		if len(parts) < 2 {
			t.Fatalf("volume %q has no target", v)
		}
		mode := ""
		if last := parts[len(parts)-1]; last == "ro" || last == "rw" {
			mode, parts = last, parts[:len(parts)-1]
		}
		target := parts[len(parts)-1]
		// TODO: the server rewrites the Anthropic cert pool in the secrets
		// dir; move it out and make /etc/jam/secrets read-only again.
		if mode != "ro" && target != "/var/lib/jam" && target != "/etc/jam/secrets" {
			t.Errorf("mount %q is writable; only the state volume and secrets dir may be", v)
		}
		targets = append(targets, target)
	}
	paths := regexp.MustCompile(`(?m)^[^#\n]*?(/etc/jam/[^\s"']+)`).FindAllStringSubmatch(string(raw), -1)
	if len(paths) == 0 {
		t.Fatal("the example names no /etc/jam path")
	}
	for _, m := range append(paths, []string{"", "/etc/jam/jam.yml"}) {
		p, covered := m[1], false
		for _, tg := range targets {
			if p == tg || strings.HasPrefix(p, strings.TrimSuffix(tg, "/")+"/") {
				covered = true
			}
		}
		if !covered {
			t.Errorf("the config names %s, which no jam mount (%v) provides", p, targets)
		}
	}
}
