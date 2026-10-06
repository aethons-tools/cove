package jam

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
)

// The registry's admin plane (intercom slice 1a-3b): users, project members,
// accounts and connections. A {user} path parameter is a name or a usr_ id;
// {account} an acc_ id; a connection a name or a con_ id.

// UserBody creates a user.
type UserBody struct {
	Name   string         `json:"name"`
	Logins []string       `json:"logins,omitempty"`
	OIDC   []OIDCIdentity `json:"oidc,omitempty"`
}

// RenameBody renames an entity.
type RenameBody struct {
	Name string `json:"name"`
}

// LoginsBody replaces a user's logins.
type LoginsBody struct {
	Logins []string `json:"logins"`
}

// OIDCBody replaces a user's OIDC bindings.
type OIDCBody struct {
	OIDC []OIDCIdentity `json:"oidc"`
}

// MemberBody adds a project member, or replaces their delivery.
type MemberBody struct {
	Delivery []DeliveryProfile `json:"delivery,omitempty"`
}

// AccountBody upserts an account on a connection, optionally linking it.
type AccountBody struct {
	Connection string `json:"connection"`
	ServiceUID string `json:"service_uid,omitempty"`
	Handle     string `json:"handle,omitempty"`
	Label      string `json:"label,omitempty"`
	User       string `json:"user,omitempty"`
}

// ConnectionBody creates a connection.
type ConnectionBody struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Cred string `json:"cred,omitempty"`
}

// CredBody names a connection's credential.
type CredBody struct {
	Cred string `json:"cred"`
}

// LinkBody links an account to a user.
type LinkBody struct {
	User string `json:"user"`
}

// UserView is a user with their accounts and the projects they belong to.
type UserView struct {
	User
	Accounts []AccountView `json:"accounts,omitempty"`
	Projects []string      `json:"projects,omitempty"`
}

// AccountView is an account with its connection's and user's names.
type AccountView struct {
	Account
	Connection string `json:"connection"`
	User       string `json:"user,omitempty"`
}

// MemberView is one project member.
type MemberView struct {
	UserID   ident.ID          `json:"user_id"`
	User     string            `json:"user"`
	Delivery []DeliveryProfile `json:"delivery,omitempty"`
}

// ErrUserBusy refuses renaming or removing a user who owns a live personal
// session.
var ErrUserBusy = errors.New("user owns a live personal session")

// RegistryErrStatus maps registry and project errors to HTTP statuses.
func RegistryErrStatus(err error) int {
	for _, nf := range []error{ErrUserNotFound, ErrConnectionNotFound, ErrAccountNotFound, ErrMembershipNotFound, ErrProjectNotFound, ErrChannelNotFound} {
		if errors.Is(err, nf) {
			return http.StatusNotFound
		}
	}
	for _, c := range []error{ErrNameTaken, ErrRemoved, ErrConnectionInUse, ErrProjectInUse, ErrUserBusy, ErrChannelExists, ErrBindingTaken} {
		if errors.Is(err, c) {
			return http.StatusConflict
		}
	}
	return http.StatusBadRequest
}

// ResolveRegistryRef finds the registry id ref names: an id of kind k as given, else
// the live entity of kind k with that name.
func ResolveRegistryRef(store Store, k ident.Kind, ref string) (ident.ID, error) {
	if id, err := ident.Parse(ref); err == nil && id.Kind() == k {
		if _, ok := store.Resolve(id); ok {
			return id, nil
		}
	} else if id, ok := store.LookupName(k, ref); ok {
		return id, nil
	}
	switch k {
	case ident.User:
		return "", fmt.Errorf("%w: %q", ErrUserNotFound, ref)
	case ident.Connection:
		return "", fmt.Errorf("%w: %q", ErrConnectionNotFound, ref)
	case ident.Account:
		return "", fmt.Errorf("%w: %q", ErrAccountNotFound, ref)
	}
	return "", fmt.Errorf("%w: %q", ErrProjectNotFound, ref)
}

func NewUserView(store Store, u User) UserView {
	v := UserView{User: u}
	for _, c := range store.ListConnections() {
		for _, a := range store.ListAccounts(c.ID) {
			if a.UserID == u.ID {
				v.Accounts = append(v.Accounts, AccountView{Account: a, Connection: c.Name, User: u.Name})
			}
		}
	}
	for _, pid := range store.ListMemberships(u.ID) {
		if e, ok := store.Resolve(pid); ok {
			v.Projects = append(v.Projects, e.Name)
		}
	}
	slices.Sort(v.Projects)
	return v
}

func NewAccountView(store Store, a Account) AccountView {
	v := AccountView{Account: a}
	if c, ok := store.GetConnection(a.ConnectionID); ok {
		v.Connection = c.Name
	}
	if a.UserID != "" {
		if e, ok := store.Resolve(a.UserID); ok {
			v.User = e.Label()
		}
	}
	return v
}

// ownedPersonalSession is a live personal session owned by u. Its nags and
// keep/release replies still travel the name-keyed log (until slice 2), so
// renaming or removing u would orphan the session.
func ownedPersonalSession(store Store, u User) (string, bool) {
	for _, inst := range store.ListInstances() {
		if inst.OwnerID == u.ID || (inst.OwnerID == "" && inst.Owner != "" && inst.Owner == u.Name) {
			return inst.ActorID, true
		}
	}
	return "", false
}

// RenameUserChecked renames a user, refusing (ErrUserBusy) while they own a
// live personal session: its owner is still recorded by name.
func RenameUserChecked(store Store, id ident.ID, name string) error {
	if err := checkNotOwningSession(store, id); err != nil {
		return err
	}
	return store.RenameUser(id, name)
}

// RemoveUserChecked removes (tombstones) a user, refusing (ErrUserBusy) while
// they own a live personal session.
func RemoveUserChecked(store Store, id ident.ID) error {
	if err := checkNotOwningSession(store, id); err != nil {
		return err
	}
	return store.RemoveUser(id)
}

func checkNotOwningSession(store Store, id ident.ID) error {
	if u, _ := store.GetUser(id); u.Status == StatusLive {
		if sid, owns := ownedPersonalSession(store, u); owns {
			return fmt.Errorf("%w: user %q owns the live personal session %s; release it first", ErrUserBusy, u.Name, sid)
		}
	}
	return nil
}

// UpsertAccountChecked upserts the account b describes and links it to b.User
// when set. The user is resolved and checked live before anything is
// written, so a refusal leaves no account behind.
func UpsertAccountChecked(store Store, b AccountBody) (Account, error) {
	conn, err := ResolveRegistryRef(store, ident.Connection, b.Connection)
	if err != nil {
		return Account{}, err
	}
	var uid ident.ID
	if b.User != "" {
		if uid, err = ResolveRegistryRef(store, ident.User, b.User); err != nil {
			return Account{}, err
		}
		if u, _ := store.GetUser(uid); u.Status != StatusLive {
			return Account{}, fmt.Errorf("%w: user %s", ErrRemoved, uid)
		}
	}
	a, err := store.UpsertAccount(Account{ConnectionID: conn, ServiceUID: b.ServiceUID, Handle: b.Handle, Label: b.Label})
	if err != nil {
		return Account{}, err
	}
	if uid != "" {
		if err := store.LinkAccount(a.ID, uid); err != nil {
			return Account{}, err
		}
		a, _ = store.GetAccount(a.ID)
	}
	return a, nil
}

func registerUsers(mux *http.ServeMux, store Store, log *slog.Logger) {
	fail := func(w http.ResponseWriter, err error) { http.Error(w, err.Error(), RegistryErrStatus(err)) }
	user := func(w http.ResponseWriter, r *http.Request) (ident.ID, bool) {
		id, err := ResolveRegistryRef(store, ident.User, r.PathValue("user"))
		if err != nil {
			fail(w, err)
			return "", false
		}
		return id, true
	}

	mux.HandleFunc("GET /admin/users", func(w http.ResponseWriter, r *http.Request) {
		out := []UserView{}
		for _, u := range store.ListUsers() {
			out = append(out, NewUserView(store, u))
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST /admin/users", func(w http.ResponseWriter, r *http.Request) {
		var b UserBody
		if !decode(w, r, &b) {
			return
		}
		u, err := store.CreateUser(User{Name: b.Name, Logins: b.Logins, OIDC: b.OIDC})
		if err != nil {
			fail(w, err)
			return
		}
		log.Info("admin user created", "operator", OperatorID(r), "user", u.ID, "name", u.Name)
		writeJSON(w, http.StatusCreated, NewUserView(store, u))
	})
	mux.HandleFunc("GET /admin/users/{user}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := user(w, r)
		if !ok {
			return
		}
		u, _ := store.GetUser(id)
		writeJSON(w, http.StatusOK, NewUserView(store, u))
	})
	put := func(op string, apply func(id ident.ID, r *http.Request) (bool, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			id, ok := user(w, r)
			if !ok {
				return
			}
			if decoded, err := apply(id, r); !decoded {
				return
			} else if err != nil {
				fail(w, err)
				return
			}
			log.Info("admin user "+op, "operator", OperatorID(r), "user", id)
			w.WriteHeader(http.StatusNoContent)
		}
	}
	mux.HandleFunc("PUT /admin/users/{user}/name", func(w http.ResponseWriter, r *http.Request) {
		put("renamed", func(id ident.ID, r *http.Request) (bool, error) {
			var b RenameBody
			if !decode(w, r, &b) {
				return false, nil
			}
			return true, RenameUserChecked(store, id, b.Name)
		})(w, r)
	})
	mux.HandleFunc("PUT /admin/users/{user}/logins", func(w http.ResponseWriter, r *http.Request) {
		put("logins set", func(id ident.ID, r *http.Request) (bool, error) {
			var b LoginsBody
			if !decode(w, r, &b) {
				return false, nil
			}
			return true, store.SetUserLogins(id, b.Logins)
		})(w, r)
	})
	mux.HandleFunc("PUT /admin/users/{user}/oidc", func(w http.ResponseWriter, r *http.Request) {
		put("oidc set", func(id ident.ID, r *http.Request) (bool, error) {
			var b OIDCBody
			if !decode(w, r, &b) {
				return false, nil
			}
			return true, store.SetUserOIDC(id, b.OIDC)
		})(w, r)
	})
	mux.HandleFunc("DELETE /admin/users/{user}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := user(w, r)
		if !ok {
			return
		}
		if err := RemoveUserChecked(store, id); err != nil {
			fail(w, err)
			return
		}
		log.Info("admin user removed", "operator", OperatorID(r), "user", id)
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /admin/projects/{project}/members", func(w http.ResponseWriter, r *http.Request) {
		p, ok := store.GetProject(r.PathValue("project"))
		if !ok {
			fail(w, fmt.Errorf("%w: %q", ErrProjectNotFound, r.PathValue("project")))
			return
		}
		out := []MemberView{}
		for _, uid := range store.ListMembers(p.ID) {
			ms, _ := store.GetMembership(p.ID, uid)
			e, _ := store.Resolve(uid)
			out = append(out, MemberView{UserID: uid, User: e.Label(), Delivery: ms.Delivery})
		}
		slices.SortFunc(out, func(a, b MemberView) int { return strings.Compare(a.User, b.User) })
		writeJSON(w, http.StatusOK, out)
	})
	member := func(w http.ResponseWriter, r *http.Request) (Project, ident.ID, bool) {
		p, ok := store.GetProject(r.PathValue("project"))
		if !ok {
			fail(w, fmt.Errorf("%w: %q", ErrProjectNotFound, r.PathValue("project")))
			return Project{}, "", false
		}
		id, ok := user(w, r)
		return p, id, ok
	}
	mux.HandleFunc("PUT /admin/projects/{project}/members/{user}", func(w http.ResponseWriter, r *http.Request) {
		p, id, ok := member(w, r)
		if !ok {
			return
		}
		var b MemberBody
		if !decode(w, r, &b) {
			return
		}
		if err := store.PutMembership(Membership{ProjectID: p.ID, UserID: id, Delivery: b.Delivery}); err != nil {
			fail(w, err)
			return
		}
		log.Info("admin member set", "operator", OperatorID(r), "project", p.Name, "user", id)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /admin/projects/{project}/members/{user}", func(w http.ResponseWriter, r *http.Request) {
		p, id, ok := member(w, r)
		if !ok {
			return
		}
		if err := store.RemoveMember(p.ID, id); err != nil {
			fail(w, err)
			return
		}
		log.Info("admin member removed", "operator", OperatorID(r), "project", p.Name, "user", id)
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /admin/connections", func(w http.ResponseWriter, r *http.Request) {
		out := store.ListConnections()
		if out == nil {
			out = []Connection{}
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST /admin/connections", func(w http.ResponseWriter, r *http.Request) {
		var b ConnectionBody
		if !decode(w, r, &b) {
			return
		}
		c, err := store.CreateConnection(Connection{Kind: b.Kind, Name: b.Name, CredName: b.Cred})
		if err != nil {
			fail(w, err)
			return
		}
		log.Info("admin connection created", "operator", OperatorID(r), "connection", c.ID, "kind", c.Kind, "name", c.Name, "cred", c.CredName)
		writeJSON(w, http.StatusCreated, c)
	})
	conn := func(w http.ResponseWriter, r *http.Request) (ident.ID, bool) {
		id, err := ResolveRegistryRef(store, ident.Connection, r.PathValue("connection"))
		if err != nil {
			fail(w, err)
			return "", false
		}
		return id, true
	}
	connWrite := func(op string, apply func(id ident.ID, r *http.Request) (bool, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			id, ok := conn(w, r)
			if !ok {
				return
			}
			if decoded, err := apply(id, r); !decoded {
				return
			} else if err != nil {
				fail(w, err)
				return
			}
			log.Info("admin connection "+op, "operator", OperatorID(r), "connection", id)
			w.WriteHeader(http.StatusNoContent)
		}
	}
	mux.HandleFunc("PUT /admin/connections/{connection}/name", func(w http.ResponseWriter, r *http.Request) {
		connWrite("renamed", func(id ident.ID, r *http.Request) (bool, error) {
			var b RenameBody
			if !decode(w, r, &b) {
				return false, nil
			}
			return true, store.RenameConnection(id, b.Name)
		})(w, r)
	})
	mux.HandleFunc("PUT /admin/connections/{connection}/cred", func(w http.ResponseWriter, r *http.Request) {
		connWrite("cred set", func(id ident.ID, r *http.Request) (bool, error) {
			var b CredBody
			if !decode(w, r, &b) {
				return false, nil
			}
			return true, store.SetConnectionCred(id, b.Cred)
		})(w, r)
	})
	mux.HandleFunc("DELETE /admin/connections/{connection}", connWrite("removed", func(id ident.ID, r *http.Request) (bool, error) {
		return true, store.RemoveConnection(id)
	}))
	mux.HandleFunc("GET /admin/accounts", func(w http.ResponseWriter, r *http.Request) {
		conns := store.ListConnections()
		if ref := r.URL.Query().Get("connection"); ref != "" {
			id, err := ResolveRegistryRef(store, ident.Connection, ref)
			if err != nil {
				fail(w, err)
				return
			}
			c, _ := store.GetConnection(id)
			conns = []Connection{c}
		}
		out := []AccountView{}
		for _, c := range conns {
			for _, a := range store.ListAccounts(c.ID) {
				out = append(out, NewAccountView(store, a))
			}
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST /admin/accounts", func(w http.ResponseWriter, r *http.Request) {
		var b AccountBody
		if !decode(w, r, &b) {
			return
		}
		a, err := UpsertAccountChecked(store, b)
		if err != nil {
			fail(w, err)
			return
		}
		log.Info("admin account upserted", "operator", OperatorID(r), "account", a.ID, "connection", a.ConnectionID, "user", a.UserID)
		writeJSON(w, http.StatusCreated, NewAccountView(store, a))
	})
	link := func(w http.ResponseWriter, r *http.Request, userRef string) {
		acc, err := ident.Parse(r.PathValue("account"))
		if err != nil || acc.Kind() != ident.Account {
			fail(w, fmt.Errorf("%w: %q", ErrAccountNotFound, r.PathValue("account")))
			return
		}
		var uid ident.ID
		if userRef != "" {
			if uid, err = ResolveRegistryRef(store, ident.User, userRef); err != nil {
				fail(w, err)
				return
			}
		}
		if err := store.LinkAccount(acc, uid); err != nil {
			fail(w, err)
			return
		}
		log.Info("admin account link", "operator", OperatorID(r), "account", acc, "user", uid)
		w.WriteHeader(http.StatusNoContent)
	}
	mux.HandleFunc("PUT /admin/accounts/{account}/user", func(w http.ResponseWriter, r *http.Request) {
		var b LinkBody
		if !decode(w, r, &b) {
			return
		}
		if b.User == "" {
			http.Error(w, "user is required (DELETE unlinks)", http.StatusBadRequest)
			return
		}
		link(w, r, b.User)
	})
	mux.HandleFunc("DELETE /admin/accounts/{account}/user", func(w http.ResponseWriter, r *http.Request) {
		link(w, r, "")
	})
}
