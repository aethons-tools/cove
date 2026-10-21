package jam

import (
	"reflect"
	"slices"
	"testing"
)

func TestMergeServeConfigs(t *testing.T) {
	repo := map[string]any{"display-name": "A", "ui-origins": []any{"x"}, "pool": map[string]any{"cred-name": "anthropic", "refresh-interval": "5m"}}
	local := map[string]any{"listen": ":443", "ui-origins": []any{"y"}, "pool": map[string]any{"store": "/var/pool.json"}}
	got := MergeServeConfigs(repo, local)
	want := map[string]any{"display-name": "A", "listen": ":443", "ui-origins": []any{"y"},
		"pool": map[string]any{"cred-name": "anthropic", "refresh-interval": "5m", "store": "/var/pool.json"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	repo["pool"].(map[string]any)["cred-name"] = "mutated"
	if got["pool"].(map[string]any)["cred-name"] != "anthropic" {
		t.Fatal("merge aliases its input")
	}
}

func TestHostKeysAndPortable(t *testing.T) {
	doc := map[string]any{"listen": ":1", "display-name": "A", "runtime": map[string]any{"launcher": map[string]any{}, "listen": ":2"},
		"pool": map[string]any{"store": "/p", "cred-name": "c"}, "previews": map[string]any{"tls": map[string]any{"cert": "c"}, "domain": "d.example"}}
	if got := ServeHostKeysIn(doc); !slices.Equal(got, []string{"listen", "runtime.launcher", "pool.store", "previews.tls"}) {
		t.Fatalf("host keys %v", got)
	}
	if r := HostKeyRefusals("jam.yml", doc); r[0] != "jam.yml: listen is a host key; put it in jam.local.yml" {
		t.Fatalf("refusal %v", r)
	}
	p := PortableServeConfig(doc)
	want := map[string]any{"display-name": "A", "runtime": map[string]any{"listen": ":2"}, "pool": map[string]any{"cred-name": "c"},
		"previews": map[string]any{"domain": "d.example"}}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("portable %v", p)
	}
}

func TestDiffServeConfig(t *testing.T) {
	a := map[string]any{"display-name": "A", "kits": map[string]any{"git": []any{"x"}}, "pool": map[string]any{"refresh-interval": "5m"}}
	b := map[string]any{"display-name": "A", "kits": map[string]any{"git": []any{"y"}}, "metrics": map[string]any{"token-cred": "m"}}
	if got := DiffServeConfig(a, b); !slices.Equal(got, []string{"kits.git", "metrics.token-cred", "pool.refresh-interval"}) {
		t.Fatalf("diff %v", got)
	}
	if DiffServeConfig(a, a) != nil {
		t.Fatal("same must be nil")
	}
}
