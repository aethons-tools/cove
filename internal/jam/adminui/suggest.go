package adminui

import (
	"encoding/json"
	"net/http"
	"slices"

	"github.com/aethons-tools/cove/internal/jam"
)

// WithCredentialNames supplies the configured credential names the type-ahead
// offers for credential fields. Names are references, never values.
func WithCredentialNames(names ...string) Option {
	return func(o *options) { o.credNames = append(o.credNames, names...) }
}

// suggestKinds maps a type-ahead kind to its suggestions; project scopes the
// kinds that live in a project (blank = default).
func suggestKinds(store jam.Store, credNames []string) map[string]func(project string) []string {
	return map[string]func(string) []string{
		"projects": func(string) []string { return projectChoices(store) },
		"roles": func(p string) []string {
			var out []string
			for _, r := range store.ListRoles(orDefaultProject(p)) {
				out = append(out, r.Name)
			}
			return out
		},
		"kits": func(string) []string {
			var out []string
			for _, k := range store.ListKits() {
				out = append(out, k.Name)
			}
			return out
		},
		"destinations": func(string) []string {
			var out []string
			for _, d := range store.ListDestinations() {
				out = append(out, d.Name)
			}
			return out
		},
		"credentials": func(string) []string { return credNames },
		// targets: a project's addressable roster, plus the per-kind globs.
		"targets": func(p string) []string {
			out := []string{"human:*", "channel:*"}
			if r, ok := store.GetRoster(orDefaultProject(p)); ok {
				for _, h := range r.Humans {
					out = append(out, "human:"+h.Name)
				}
				for _, c := range r.Channels {
					out = append(out, "channel:"+c.Name)
				}
			}
			return out
		},
		// participants: anyone a squawk can be from or to, across projects.
		"participants": func(string) []string {
			var out []string
			for _, name := range store.ListProjects() {
				if r, ok := store.GetRoster(name); ok {
					for _, h := range r.Humans {
						out = append(out, "human:"+h.Name)
					}
					for _, c := range r.Channels {
						out = append(out, "channel:"+c.Name)
					}
				}
			}
			for _, i := range store.ListInstances() {
				out = append(out, "actor:"+i.ActorID)
			}
			return out
		},
		"services": func(string) []string {
			return slices.DeleteFunc(slices.Clone(chatServices), func(s string) bool { return s == "" })
		},
	}
}

func registerSuggest(mux *http.ServeMux, store jam.Store, credNames []string) {
	kinds := suggestKinds(store, credNames)
	mux.HandleFunc("GET /ui/suggest", func(w http.ResponseWriter, r *http.Request) {
		f, ok := kinds[r.URL.Query().Get("kind")]
		if !ok {
			http.Error(w, "unknown suggestion kind", http.StatusBadRequest)
			return
		}
		out := slices.Compact(slices.Sorted(slices.Values(f(r.URL.Query().Get("project")))))
		if out == nil {
			out = []string{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
}
