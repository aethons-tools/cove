package adminui

import (
	"errors"
	"net/url"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

// The users pages (intercom slice 1a-3b): the people agents talk to, with
// their logins, OIDC bindings and service accounts. Project membership is
// edited on the project page (members section).

func userURL(id ident.ID) string { return "/app/admin/users/" + url.PathEscape(string(id)) }

// userDetail is the user page payload. A removed user's page is read-only.
type userDetail struct {
	Title       string
	User        jam.UserView
	Removed     bool
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
		Removed:     u.Status == jam.StatusRemoved,
		LoginSpec:   strings.Join(u.Logins, "\n"),
		OIDCSpec:    lines(u.OIDC, jam.FormatOIDCSpec),
		Connections: store.ListConnections(),
	}, true
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
