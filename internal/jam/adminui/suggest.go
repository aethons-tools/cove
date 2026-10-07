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
		"model-specs": func(string) []string {
			var out []string
			for _, m := range store.ListModelSpecs() {
				out = append(out, m.Name)
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
			out := []string{"user:*", "channel:*"}
			if pr, ok := store.GetProject(orDefaultProject(p)); ok {
				for _, m := range jam.MembersOf(store, pr.ID) {
					out = append(out, "user:"+m.User.Name)
				}
				for _, c := range store.ListChannels(pr.ID, jam.SourceRoom) {
					out = append(out, "channel:"+c.Key)
				}
			}
			return out
		},
		// participants: who a squawk can be from and where it can be — users,
		// sessions and channels by id (the channel log), and the legacy log's
		// kind:ref forms.
		"participants": func(string) []string {
			var out []string
			for _, u := range store.ListUsers() {
				out = append(out, string(u.ID))
			}
			for _, i := range store.ListInstances() {
				out = append(out, i.ActorID, "actor:"+i.ActorID)
			}
			for _, name := range store.ListProjects() {
				p, _ := store.GetProject(name)
				for _, k := range []jam.SourceKind{jam.SourceTicket, jam.SourceRoom, jam.SourceChat} {
					for _, c := range store.ListChannels(p.ID, k) {
						out = append(out, string(c.ID))
					}
				}
				for _, m := range jam.MembersOf(store, p.ID) {
					out = append(out, "human:"+m.User.Name)
				}
				for _, c := range store.ListChannels(p.ID, jam.SourceRoom) {
					out = append(out, "channel:"+c.Key)
				}
			}
			return out
		},
		"users": func(string) []string {
			var out []string
			for _, u := range store.ListUsers() {
				out = append(out, u.Name)
			}
			return out
		},
		"services": func(string) []string {
			return slices.Clone(jam.ChatKinds)
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
