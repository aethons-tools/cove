package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// kitChainServer fakes the admin API for a chained kit (my-app on web-dev on
// default), a pinned one, and the kit list.
func kitChainServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/kits", func(w http.ResponseWriter, r *http.Request) {
		chainJSON(w, []map[string]any{
			{"name": "web-dev", "current": 5, "versions": 5, "parent": "default@v1"},
			{"name": "my-app", "current": 2, "versions": 2, "parent": "web-dev"},
			{"name": "solo", "current": 1, "versions": 1},
		})
	})
	mux.HandleFunc("GET /admin/kits/my-app", func(w http.ResponseWriter, r *http.Request) {
		chainJSON(w, map[string]any{"name": "my-app", "version": 2,
			"config":    `{"kind":"studio","base":{"kit":"web-dev"},"prompt":"own prompt","egress":["own.example.com"]}`,
			"effective": "kind: studio\nprompt: merged prompt\negress:\n    - parent.example.com\n    - own.example.com\n",
			"parent":    map[string]any{"name": "web-dev", "version": 3},
			"chain": []map[string]any{
				{"name": "default", "version": 1, "follow": false},
				{"name": "web-dev", "version": 5, "follow": true}}})
	})
	mux.HandleFunc("GET /admin/kits/pinned", func(w http.ResponseWriter, r *http.Request) {
		chainJSON(w, map[string]any{"name": "pinned", "version": 1,
			"config":    `{"kind":"studio","base":{"kit":"web-dev@v3"},"prompt":"p"}`,
			"effective": "kind: studio\nprompt: merged\n",
			"parent":    map[string]any{"name": "web-dev", "version": 3},
			"chain":     []map[string]any{{"name": "web-dev", "version": 3, "follow": false}}})
	})
	mux.HandleFunc("GET /admin/kits/solo", func(w http.ResponseWriter, r *http.Request) {
		chainJSON(w, map[string]any{"name": "solo", "version": 1, "config": `{"kind":"studio","prompt":"hi"}`,
			"effective": "kind: studio\nprompt: hi\n"})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestKitShowChainedEffective(t *testing.T) {
	ts := kitChainServer(t)
	code, out, errb := runKit(t, ts, "show", "my-app")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errb)
	}
	want := "# source: local\n# built on: default@v1 → web-dev (follows, now v5; pushed against v3)\nkind: studio\n"
	if !strings.HasPrefix(out, want) || !strings.Contains(out, "prompt: merged prompt") {
		t.Errorf("show:\n%s", out)
	}
	if strings.Contains(out, "own prompt") {
		t.Error("default show printed the own YAML")
	}
	// The ceiling lines use the merged egress.
	if !strings.Contains(out, "parent.example.com") || !strings.Contains(out, "egress ceiling:") {
		t.Errorf("ceiling not from the merged egress:\n%s", out)
	}
}

func TestKitShowOwn(t *testing.T) {
	ts := kitChainServer(t)
	code, out, errb := runKit(t, ts, "show", "my-app", "--own")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errb)
	}
	if !strings.HasPrefix(out, "# source: local\n# built on: default@v1 → web-dev (follows, now v5; pushed against v3)\nkind: studio\nbase:\n    kit: web-dev\n") ||
		!strings.Contains(out, "prompt: own prompt") || strings.Contains(out, "merged prompt") {
		t.Errorf("show --own:\n%s", out)
	}
}

func TestKitShowPinnedParent(t *testing.T) {
	ts := kitChainServer(t)
	_, out, _ := runKit(t, ts, "show", "pinned")
	if !strings.HasPrefix(out, "# source: local\n# built on: web-dev@v3 (pinned)\n") {
		t.Errorf("show:\n%s", out)
	}
}

func TestKitShowUnchainedUnchanged(t *testing.T) {
	ts := kitChainServer(t)
	_, eff, _ := runKit(t, ts, "show", "solo")
	_, own, _ := runKit(t, ts, "show", "solo", "--own")
	if eff != own || strings.Contains(eff, "built on") || !strings.HasPrefix(eff, "# source: local\nkind: studio\n") {
		t.Errorf("effective:\n%s\nown:\n%s", eff, own)
	}
}

func TestKitListParentColumn(t *testing.T) {
	ts := kitChainServer(t)
	code, out, errb := runKit(t, ts, "list")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errb)
	}
	want := "web-dev\tcurrent=v5\tversions=5\tparent=default@v1\n" +
		"my-app\tcurrent=v2\tversions=2\tparent=web-dev\n" +
		"solo\tcurrent=v1\tversions=1\n"
	if out != want {
		t.Errorf("list:\n%q\nwant\n%q", out, want)
	}
}

func chainJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
