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
