package adminui

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

// The users pages (intercom slice 1a-3b): the people agents talk to, with
// their logins, OIDC bindings and service accounts. Project membership is
// edited on the project page (members section).

func userURL(id ident.ID) string { return "/ui/users/" + url.PathEscape(string(id)) }

// userDetail is the user page payload.
type userDetail struct {
	Title       string
	User        jam.UserView
	LoginSpec   string
	OIDCSpec    string
	Connections []jam.Connection
	NotFound    bool
	NotFoundFor string
}

func buildUserDetail(store jam.Store, ref string) (userDetail, bool) {
	id, err := jam.ResolveRegistryRef(store, ident.User, ref)
	if err != nil {
		return userDetail{}, false
	}
	u, _ := store.GetUser(id)
	return userDetail{
		Title:       "Users",
		User:        jam.NewUserView(store, u),
		LoginSpec:   strings.Join(u.Logins, "\n"),
		OIDCSpec:    lines(u.OIDC, jam.FormatOIDCSpec),
		Connections: store.ListConnections(),
	}, true
}

func usersData(store jam.Store) map[string]any {
	var users []jam.UserView
	for _, u := range store.ListUsers() {
		users = append(users, jam.NewUserView(store, u))
	}
	return map[string]any{"Title": "Users", "Users": users}
}

func oidcFromForm(s string) ([]jam.OIDCIdentity, error) {
	var out []jam.OIDCIdentity
	for _, l := range splitSpecLines(s) {
		id, err := jam.ParseOIDCSpec(l)
		if err != nil {
			return nil, badRequest("oidc " + `"` + l + `": ` + err.Error())
		}
		out = append(out, id)
	}
	return out, nil
}

// registryErr turns a registry error into a status-carrying write error.
func registryErr(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*jam.WriteError](err); ok {
		return err
	}
	return &jam.WriteError{Status: jam.RegistryErrStatus(err), Msg: err.Error()}
}

func registerUsers(mux *http.ServeMux, store jam.Store, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	mux.HandleFunc("GET /ui/users", func(w http.ResponseWriter, r *http.Request) {
		render(w, "users", usersData(store))
	})
	mux.HandleFunc("GET /ui/users/{user}", func(w http.ResponseWriter, r *http.Request) {
		d, ok := buildUserDetail(store, r.PathValue("user"))
		if !ok {
			renderStatus(w, http.StatusNotFound, "user", userDetail{Title: "Users", NotFound: true, NotFoundFor: r.PathValue("user")})
			return
		}
		render(w, "user", d)
	})

	// form wraps a write: guard, parse, apply, log; apply's error carries the
	// status (a WriteError or a registry error).
	form := func(w http.ResponseWriter, r *http.Request, what string, apply func() error) bool {
		if !guardWrite(w, r) {
			return false
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return false
		}
		if err := registryErr(apply()); err != nil {
			renderError(w, jam.WriteStatus(err, http.StatusBadRequest), err.Error())
			return false
		}
		log.Info("ui user "+what, "operator", jam.OperatorID(r), "user", r.PathValue("user"))
		return true
	}
	// edit applies a write to the {user} and answers with the re-rendered body.
	edit := func(what string, apply func(r *http.Request, id ident.ID) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var id ident.ID
			if !form(w, r, what, func() error {
				var err error
				if id, err = jam.ResolveRegistryRef(store, ident.User, r.PathValue("user")); err != nil {
					return err
				}
				return apply(r, id)
			}) {
				return
			}
			d, _ := buildUserDetail(store, string(id))
			renderFragment(w, "user", "user-body", d)
		}
	}

	mux.HandleFunc("POST /ui/users", func(w http.ResponseWriter, r *http.Request) {
		var u jam.User
		if !form(w, r, "created", func() error {
			ids, err := oidcFromForm(r.FormValue("oidc"))
			if err != nil {
				return err
			}
			u, err = store.CreateUser(jam.User{Name: strings.TrimSpace(r.FormValue("name")), Logins: splitSpecLines(r.FormValue("logins")), OIDC: ids})
			return err
		}) {
			return
		}
		w.Header().Set("HX-Redirect", userURL(u.ID))
		renderFragment(w, "users", "users-table", usersData(store))
	})
	mux.HandleFunc("POST /ui/users/{user}/name", edit("renamed", func(r *http.Request, id ident.ID) error {
		return jam.RenameUserChecked(store, id, strings.TrimSpace(r.FormValue("name")))
	}))
	mux.HandleFunc("POST /ui/users/{user}/logins", edit("logins set", func(r *http.Request, id ident.ID) error {
		return store.SetUserLogins(id, splitSpecLines(r.FormValue("logins")))
	}))
	mux.HandleFunc("POST /ui/users/{user}/oidc", edit("oidc set", func(r *http.Request, id ident.ID) error {
		ids, err := oidcFromForm(r.FormValue("oidc"))
		if err != nil {
			return err
		}
		return store.SetUserOIDC(id, ids)
	}))
	mux.HandleFunc("POST /ui/users/{user}/accounts", edit("account added", func(r *http.Request, id ident.ID) error {
		_, err := jam.UpsertAccountChecked(store, jam.AccountBody{
			Connection: strings.TrimSpace(r.FormValue("connection")),
			ServiceUID: strings.TrimSpace(r.FormValue("uid")),
			Handle:     strings.TrimSpace(r.FormValue("handle")),
			Label:      strings.TrimSpace(r.FormValue("label")),
			User:       string(id),
		})
		return err
	}))
	// Unlinking answers with the page of the user named by ?user=.
	mux.HandleFunc("DELETE /ui/accounts/{account}/user", func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("user", r.URL.Query().Get("user"))
		edit("account unlinked", func(r *http.Request, _ ident.ID) error {
			acc, err := ident.Parse(r.PathValue("account"))
			if err != nil || acc.Kind() != ident.Account {
				return &jam.WriteError{Status: http.StatusNotFound, Msg: "no such account"}
			}
			return store.LinkAccount(acc, "")
		})(w, r)
	})
	mux.HandleFunc("DELETE /ui/users/{user}", func(w http.ResponseWriter, r *http.Request) {
		if !form(w, r, "removed", func() error {
			id, err := jam.ResolveRegistryRef(store, ident.User, r.PathValue("user"))
			if err != nil {
				return err
			}
			return jam.RemoveUserChecked(store, id)
		}) {
			return
		}
		w.Header().Set("HX-Redirect", "/ui/users")
		w.WriteHeader(http.StatusOK)
	})
}
