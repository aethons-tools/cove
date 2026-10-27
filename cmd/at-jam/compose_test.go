package main

import (
	"os"
	"reflect"
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

func TestComposeJamWiring(t *testing.T) {
	c := loadCompose(t)
	jam := c.Services["jam"]
	if jam.Image != "${JAM_IMAGE:-cove-jam:dev}" {
		t.Errorf("image = %q", jam.Image)
	}
	if got := jam.DependsOn["postgres"].Condition; got != "service_healthy" {
		t.Errorf("postgres condition = %q, want service_healthy", got)
	}
	have := map[string]bool{}
	for _, v := range jam.Volumes {
		have[v] = true
	}
	for _, m := range []string{
		"./jam.container.yml:/etc/jam/jam.yml:ro",
		"./credentials.container.yml:/etc/jam/credentials.yml:ro",
		"${JAM_TLS_DIR:-./tls}:/etc/jam/tls:ro",
		"jamstate:/var/lib/jam",
	} {
		if !have[m] {
			t.Errorf("missing mount %q (have %v)", m, jam.Volumes)
		}
	}
	if _, ok := c.Volumes["jamstate"]; !ok {
		t.Error("jamstate volume not declared")
	}
}
