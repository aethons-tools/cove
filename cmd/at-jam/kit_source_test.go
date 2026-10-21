package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const commit40 = "0123456789abcdef0123456789abcdef01234567"

// kitSourceServer fakes the admin API's kit-source routes.
func kitSourceServer(t *testing.T, history bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
	mux.HandleFunc("POST /admin/kits/import", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Name, Source string }
		_ = json.NewDecoder(r.Body).Decode(&b)
		if strings.HasSuffix(b.Source, "@set") {
			writeJSON(w, map[string]any{"name": "app", "version": 1, "commit": commit40, "also": []map[string]any{
				{"name": "web-dev", "version": 2, "commit": commit40}}})
			return
		}
		if strings.HasSuffix(b.Source, "@same") {
			writeJSON(w, map[string]any{"name": "web", "version": 2, "unchanged": true, "commit": commit40})
			return
		}
		writeJSON(w, map[string]any{"name": "web", "version": 3, "commit": commit40})
	})
	mux.HandleFunc("POST /admin/kits/app/sync", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"name": "app", "version": 2, "commit": commit40, "also": []map[string]any{
			{"name": "base", "version": 1, "unchanged": true, "commit": commit40},
			{"name": "web-dev", "version": 2, "commit": commit40}}})
	})
	mux.HandleFunc("PUT /admin/kits/web/source", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"name": "web", "version": 1, "commit": commit40})
	})
	mux.HandleFunc("DELETE /admin/kits/web/source", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /admin/kits/web/sync", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"name": "web", "version": 4, "commit": commit40})
	})
	mux.HandleFunc("POST /admin/kits/loose/sync", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "kit has no tracked source (use kit track)", http.StatusConflict)
	})
	mux.HandleFunc("GET /admin/kits/web", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"name": "web", "version": 2, "config": `{"kind":"studio","prompt":"hi"}`,
			"source":   map[string]any{"repo": "https://g.example.com/a/kits", "dir": "web", "ref": "main", "commit": commit40},
			"tracking": map[string]any{"repo": "https://g.example.com/a/kits", "dir": "web", "ref": "main", "commit": commit40}})
	})
	mux.HandleFunc("GET /admin/kits/local", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"name": "local", "version": 1, "config": `{"kind":"studio","prompt":"hi"}`})
	})
	mux.HandleFunc("GET /admin/kits/web/history", func(w http.ResponseWriter, r *http.Request) {
		if !history {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, []map[string]any{
			{"version": 1, "pushed_at": "2026-10-01T10:00:00Z"},
			{"version": 2, "pushed_at": "2026-10-02T10:00:00Z", "current": true,
				"source": map[string]any{"repo": "https://g.example.com/a/kits", "dir": "web", "ref": "main", "commit": commit40}},
		})
	})
	mux.HandleFunc("GET /admin/kits/web/versions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []int{1, 2})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func runKit(t *testing.T, ts *httptest.Server, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	full := append([]string{"kit", args[0], "--admin-url", ts.URL}, args[1:]...)
	code := run(full, func(string) string { return "" }, &out, &errb)
	return code, out.String(), errb.String()
}

func TestKitSourceVerbs(t *testing.T) {
	ts := kitSourceServer(t, true)
	src := "https://g.example.com/a/kits//web@main"
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"import", "--git", src}, "imported web v3 from " + src + " (0123456789ab)\n"},
		{[]string{"import", "--git", "https://g.example.com/a/kits@same", "--name", "web"}, "web unchanged (current v2, 0123456789ab)\n"},
		{[]string{"track", "web", "--git", src}, "imported web v1 from " + src + " (0123456789ab)\ntracking " + src + "\n"},
		{[]string{"untrack", "web"}, "web no longer tracks a source\n"},
		{[]string{"sync", "web"}, "synced web v4 (0123456789ab)\n"},
	} {
		code, out, errb := runKit(t, ts, tc.args...)
		if code != 0 || out != tc.want {
			t.Errorf("%v: code=%d out=%q stderr=%q, want %q", tc.args, code, out, errb, tc.want)
		}
	}
}

// A set import or sync prints one indented line per ancestor, root first,
// before the kit's own line.
func TestKitSourceSetOutput(t *testing.T) {
	ts := kitSourceServer(t, true)
	src := "https://g.example.com/a/kits//app@set"
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"import", "--git", src}, "  web-dev v2 (with app)\nimported app v1 from " + src + " (0123456789ab)\n"},
		{[]string{"sync", "app"}, "  base unchanged (v1)\n  web-dev v2 (with app)\nsynced app v2 (0123456789ab)\n"},
	} {
		code, out, errb := runKit(t, ts, tc.args...)
		if code != 0 || out != tc.want {
			t.Errorf("%v: code=%d out=%q stderr=%q, want %q", tc.args, code, out, errb, tc.want)
		}
	}
}

func TestKitSourceVerbErrors(t *testing.T) {
	ts := kitSourceServer(t, true)
	for _, args := range [][]string{
		{"import"}, {"track", "web"}, {"track", "--git", "x@y"}, {"untrack"}, {"sync"},
	} {
		if code, _, errb := runKit(t, ts, args...); code != 2 || errb == "" {
			t.Errorf("%v: code=%d stderr=%q, want 2 with message", args, code, errb)
		}
	}
	code, out, errb := runKit(t, ts, "sync", "loose")
	if code != 1 || out != "" || !strings.Contains(errb, "no tracked source") {
		t.Errorf("sync untracked: code=%d out=%q stderr=%q", code, out, errb)
	}
}

func TestKitVersionsTable(t *testing.T) {
	ts := kitSourceServer(t, true)
	code, out, errb := runKit(t, ts, "versions", "web")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errb)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || strings.Join(strings.Fields(lines[0]), " ") != "VERSION CURRENT PUSHED SOURCE COMMIT" {
		t.Fatalf("table:\n%s", out)
	}
	if got, want := strings.Join(strings.Fields(lines[1]), " "), "v1 2026-10-01T10:00:00Z local -"; got != want {
		t.Errorf("row1 = %q, want %q", got, want)
	}
	if got, want := strings.Join(strings.Fields(lines[2]), " "), "v2 * 2026-10-02T10:00:00Z https://g.example.com/a/kits//web@main 0123456789ab"; got != want {
		t.Errorf("row2 = %q, want %q", got, want)
	}

	// An older Jam without /history falls back to the plain version list.
	old := kitSourceServer(t, false)
	if code, out, _ = runKit(t, old, "versions", "web"); code != 0 || out != "v1\nv2\n" {
		t.Errorf("fallback: code=%d out=%q", code, out)
	}
}

func TestKitShowSourceHeader(t *testing.T) {
	ts := kitSourceServer(t, true)
	code, out, errb := runKit(t, ts, "show", "web")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errb)
	}
	want := "# source: https://g.example.com/a/kits//web@main (0123456789ab)\n" +
		"# tracking: https://g.example.com/a/kits//web@main (last synced 0123456789ab)\n"
	if !strings.HasPrefix(out, want) || !strings.Contains(out, "kind: studio") {
		t.Errorf("show:\n%s", out)
	}
	code, out, _ = runKit(t, ts, "show", "local")
	if code != 0 || !strings.HasPrefix(out, "# source: local\nkind: studio") || strings.Contains(out, "# tracking") {
		t.Errorf("show local (code %d):\n%s", code, out)
	}
}
