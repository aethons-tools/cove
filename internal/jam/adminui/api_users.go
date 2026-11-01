package adminui

import (
	"cmp"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

// The users JSON API: the users list and a user's page with their writes —
// the same store and jam calls as the HTML pages (users.go).

func userRow(store jam.Store, u jam.User) UserRow {
	v := jam.NewUserView(store, u)
	row := UserRow{ID: string(u.ID), Name: u.Name, Logins: []string{}, Accounts: []AccountRow{}, Projects: []string{}}
	row.Logins = append(row.Logins, u.Logins...)
	row.Projects = append(row.Projects, v.Projects...)
	for _, a := range v.Accounts {
		row.Accounts = append(row.Accounts, AccountRow{ID: string(a.ID), Connection: a.Connection, ServiceUID: a.ServiceUID, Handle: a.Handle})
	}
	return row
}

func userDetailOf(store jam.Store, ref string) (UserDetail, bool) {
	d, ok := buildUserDetail(store, ref)
	if !ok {
		return UserDetail{}, false
	}
	u, _ := store.GetUser(d.User.ID)
	out := UserDetail{User: userRow(store, u), Removed: d.Removed, LoginSpec: d.LoginSpec, OIDCSpec: d.OIDCSpec, Connections: []ConnectionChoice{},
		AppTokens: appTokenRows(store, u.ID)}
	for _, c := range d.Connections {
		out.Connections = append(out.Connections, ConnectionChoice{Name: c.Name, Kind: c.Kind})
	}
	return out, true
}

// appTokenRows lists user's app tokens, newest first.
func appTokenRows(store jam.Store, user ident.ID) []AppTokenRow {
	toks := store.AppTokens(user)
	slices.SortFunc(toks, func(a, b jam.AppToken) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(b.ID, a.ID))
	})
	out := []AppTokenRow{}
	for _, t := range toks {
		out = append(out, AppTokenRow{ID: string(t.ID), Label: t.Label, Created: t.CreatedAt, LastUsed: t.LastUsedAt,
			Expires: t.ExpiresAt, Operator: t.Operator()})
	}
	return out
}

func apiUsers(api *http.ServeMux, src frameSource, log *slog.Logger) {
	store := src.store
	api.HandleFunc("GET "+apiPrefix+"users", func(w http.ResponseWriter, r *http.Request) {
		us := store.ListUsers()
		slices.SortFunc(us, func(a, b jam.User) int { return cmp.Compare(a.Name, b.Name) })
		out := UsersResponse{Users: []UserRow{}}
		for _, u := range us {
			out.Users = append(out.Users, userRow(store, u))
		}
		writeJSON(w, http.StatusOK, out)
	})
	api.HandleFunc("POST "+apiPrefix+"users", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Name, Logins, OIDC string }
		if !decodeJSON(w, r, &body) {
			return
		}
		ids, err := oidcFromForm(body.OIDC)
		if err != nil {
			apiFail(w, err, http.StatusBadRequest)
			return
		}
		u, err := store.CreateUser(jam.User{Name: strings.TrimSpace(body.Name), Logins: splitSpecLines(body.Logins), OIDC: ids})
		if err != nil {
			apiFail(w, err, http.StatusBadRequest)
			return
		}
		log.Info("ui user created", "operator", jam.OperatorID(r), "user", u.ID, "name", u.Name)
		writeJSON(w, http.StatusCreated, Created{ID: string(u.ID), Path: "config/users/" + url.PathEscape(string(u.ID))})
	})
	api.HandleFunc("GET "+apiPrefix+"users/{user}", func(w http.ResponseWriter, r *http.Request) {
		d, ok := userDetailOf(store, r.PathValue("user"))
		if !ok {
			writeAPIError(w, http.StatusNotFound, "not_found", "no such user")
			return
		}
		writeJSON(w, http.StatusOK, d)
	})

	// edit runs one write on the {user} (a POST's JSON body decoded into
	// fields) and answers with the user's page.
	type fields struct{ Name, Logins, OIDC, Connection, UID, Handle string }
	edit := func(what string, apply func(r *http.Request, id ident.ID, f fields) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var f fields
			if r.Method == http.MethodPost && !decodeJSON(w, r, &f) {
				return
			}
			id, err := jam.ResolveRegistryRef(store, ident.User, r.PathValue("user"))
			if err == nil {
				err = apply(r, id, f)
			}
			if err != nil {
				apiFail(w, err, http.StatusBadRequest)
				return
			}
			log.Info("ui user "+what, "operator", jam.OperatorID(r), "user", id)
			d, _ := userDetailOf(store, string(id))
			writeJSON(w, http.StatusOK, d)
		}
	}
	api.HandleFunc("POST "+apiPrefix+"users/{user}/name", edit("renamed", func(_ *http.Request, id ident.ID, f fields) error {
		return jam.RenameUserChecked(store, id, strings.TrimSpace(f.Name))
	}))
	api.HandleFunc("POST "+apiPrefix+"users/{user}/logins", edit("logins set", func(_ *http.Request, id ident.ID, f fields) error {
		return store.SetUserLogins(id, splitSpecLines(f.Logins))
	}))
	api.HandleFunc("POST "+apiPrefix+"users/{user}/oidc", edit("oidc set", func(_ *http.Request, id ident.ID, f fields) error {
		ids, err := oidcFromForm(f.OIDC)
		if err != nil {
			return err
		}
		return store.SetUserOIDC(id, ids)
	}))
	api.HandleFunc("POST "+apiPrefix+"users/{user}/accounts", edit("account added", func(_ *http.Request, id ident.ID, f fields) error {
		_, err := jam.UpsertAccountChecked(store, jam.AccountBody{Connection: strings.TrimSpace(f.Connection), ServiceUID: strings.TrimSpace(f.UID),
			Handle: strings.TrimSpace(f.Handle), User: string(id)})
		return err
	}))
	api.HandleFunc("DELETE "+apiPrefix+"users/{user}/accounts/{account}", edit("account unlinked", func(r *http.Request, user ident.ID, _ fields) error {
		acc, err := ident.Parse(r.PathValue("account"))
		if err != nil || acc.Kind() != ident.Account {
			return &jam.WriteError{Status: http.StatusNotFound, Msg: "no such account"}
		}
		if a, ok := store.GetAccount(acc); !ok || a.UserID != user {
			return &jam.WriteError{Status: http.StatusNotFound, Msg: "no such account on this user"}
		}
		return store.LinkAccount(acc, "")
	}))
	api.HandleFunc("DELETE "+apiPrefix+"users/{user}/app-tokens/{token}", func(w http.ResponseWriter, r *http.Request) {
		user, err := jam.ResolveRegistryRef(store, ident.User, r.PathValue("user"))
		tok, perr := ident.Parse(r.PathValue("token"))
		deleted := false
		if err == nil && perr == nil && tok.Kind() == ident.AppToken {
			if deleted, err = store.DeleteUserAppToken(user, tok); err != nil {
				apiFail(w, err, http.StatusInternalServerError)
				return
			}
		}
		if !deleted {
			writeAPIError(w, http.StatusNotFound, "not_found", "no such app token on this user")
			return
		}
		if src.appTokenRevoke != nil {
			src.appTokenRevoke(tok)
		}
		log.Info("ui app token revoked", "operator", jam.OperatorID(r), "user", user, "token", tok)
		w.WriteHeader(http.StatusNoContent)
	})
	api.HandleFunc("DELETE "+apiPrefix+"users/{user}", func(w http.ResponseWriter, r *http.Request) {
		id, err := jam.ResolveRegistryRef(store, ident.User, r.PathValue("user"))
		if err == nil {
			err = jam.RemoveUserChecked(store, id)
		}
		if err != nil {
			apiFail(w, err, http.StatusBadRequest)
			return
		}
		log.Info("ui user removed", "operator", jam.OperatorID(r), "user", id)
		w.WriteHeader(http.StatusNoContent)
	})
}
