package main

import (
	"strings"
	"testing"
)

func TestLoadServeConfigs(t *testing.T) {
	files := map[string]string{
		"jam.yml":       "display-name: A\nruntime:\n  listen: \":7\"\n",
		"jam.local.yml": "listen: \":443\"\nadmin-listen: 127.0.0.1:8081\n",
		"bad.yml":       "listen: \":1\"\n",
	}
	read := func(p string) ([]byte, error) { return []byte(files[p]), nil }
	merged, portable, err := loadServeConfigs([]string{"jam.yml", "jam.local.yml"}, read)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := parseServeConfig(merged)
	if err != nil || cfg.Listen != ":443" || cfg.DisplayName != "A" || cfg.Runtime.Listen != ":7" {
		t.Fatalf("cfg %+v err %v", cfg, err)
	}
	if _, ok := portable["listen"]; ok || portable["display-name"] != "A" {
		t.Fatalf("portable %v", portable)
	}
	if _, _, err := loadServeConfigs([]string{"bad.yml", "jam.local.yml"}, read); err == nil ||
		err.Error() != "bad.yml: listen is a host key; put it in jam.local.yml" {
		t.Fatalf("host key in the first file: %v", err)
	}
	if m, _, err := loadServeConfigs([]string{"bad.yml"}, read); err != nil || !strings.Contains(string(m), "listen") {
		t.Fatalf("one --config behaves as today: %v", err)
	}
}

func TestLoadServeConfigsScalarText(t *testing.T) {
	files := map[string]string{
		"one.yml":   "state-dir: 0777\ndisplay-name: 1.10\n",
		"jam.yml":   "display-name: 1.10\nruntime:\n  listen: \":7\"\n",
		"local.yml": "state-dir: 0777\n",
		"typo.yml":  "display-name: A\nlisten: [1]\n",
	}
	read := func(p string) ([]byte, error) { return []byte(files[p]), nil }
	m, _, err := loadServeConfigs([]string{"one.yml"}, read)
	if err != nil || string(m) != files["one.yml"] {
		t.Fatalf("single file must pass through: %q %v", m, err)
	}
	cfg, err := parseServeConfig(m)
	if err != nil || cfg.StateDir != "0777" || cfg.DisplayName != "1.10" {
		t.Fatalf("cfg %+v %v", cfg, err)
	}
	m, _, err = loadServeConfigs([]string{"jam.yml", "local.yml"}, read)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = parseServeConfig(m)
	if err != nil || cfg.StateDir != "0777" || cfg.DisplayName != "1.10" || cfg.Runtime.Listen != ":7" {
		t.Fatalf("merged %q cfg %+v %v", m, cfg, err)
	}
	_, want := parseServeConfig([]byte(files["typo.yml"]))
	m, _, err = loadServeConfigs([]string{"typo.yml"}, read)
	if err != nil {
		t.Fatal(err)
	}
	if _, got := parseServeConfig(m); want == nil || got == nil || got.Error() != want.Error() {
		t.Fatalf("error differs: %v vs %v", got, want)
	}
}
