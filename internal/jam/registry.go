package jam

import (
	"errors"
	"fmt"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
)

// Status is a registry entity's lifecycle state. Removal is a tombstone: a
// removed entity keeps its id and still resolves (for history), but no name,
// login or account lookup finds it, and its name is free for a new entity.
type Status string

const (
	StatusLive    Status = "live"
	StatusRemoved Status = "removed"
)

// User is a person registered with Jam who talks to agents (as opposed to an
// operator, who configures Jam). Users are Jam-wide. Logins are admin
// OperatorIDs (OIDC sub, or "local"); OIDC binds browser identities.
type User struct {
	ID     ident.ID       `json:"id"`
	Name   string         `json:"name"`
	Status Status         `json:"status"`
	Logins []string       `json:"logins,omitempty"`
	OIDC   []OIDCIdentity `json:"oidc,omitempty"`
}

// Connection is one configured instance of an external service (a Linear
// workspace, a Discord bot). CredName names the credential Jam uses for it;
// the value never enters the registry.
type Connection struct {
	ID       ident.ID `json:"id"`
	Kind     string   `json:"kind"`
	Name     string   `json:"name"`
	CredName string   `json:"cred_name,omitempty"`
	Status   Status   `json:"status"`
}

// ConnectionKinds are the services a connection may be.
var ConnectionKinds = []string{"linear", "discord"}

// Account is an identity on a connection's service. ServiceUID is the
// service's immutable user id; Handle its mention handle; either may be
// learned later, but one is required. An account with no UserID is an
// external sender not (yet) linked to a Jam user.
type Account struct {
	ID           ident.ID `json:"id"`
	ConnectionID ident.ID `json:"connection_id"`
	ServiceUID   string   `json:"service_uid,omitempty"`
	Handle       string   `json:"handle,omitempty"`
	Label        string   `json:"label,omitempty"`
	UserID       ident.ID `json:"user_id,omitempty"`
	Status       Status   `json:"status"`
}

// Entry is what Resolve knows about any id: enough to render it.
type Entry struct {
	ID     ident.ID
	Kind   ident.Kind
	Name   string
	Status Status
}

// Label renders the entry for people: its name, marked when removed.
func (e Entry) Label() string {
	if e.Status == StatusRemoved {
		return e.Name + " (removed)"
	}
	return e.Name
}

var (
	ErrInvalidName        = errors.New("invalid name")
	ErrNameTaken          = errors.New("name already in use")
	ErrLoginTaken         = errors.New("login already bound to another user")
	ErrIdentityTaken      = errors.New("oidc identity already bound to another user")
	ErrUserNotFound       = errors.New("user not found")
	ErrConnectionNotFound = errors.New("connection not found")
	ErrAccountNotFound    = errors.New("account not found")
	ErrConnectionInUse    = errors.New("connection is still referenced")
	ErrRemoved            = errors.New("entity has been removed")
)

// ValidateEntityName checks a registry name. Names are labels addressed as
// "<kind>:<name>" (and "chat:<a>,<b>", "ticket:<connection>/<key>"), so they
// exclude whitespace, address and glob metacharacters, and anything that
// parses as an id — every edge accepts "a name or an id", which must not be
// ambiguous.
func ValidateEntityName(name string) error {
	if name == "" || len(name) > 64 {
		return fmt.Errorf("%w: %q must be 1-64 characters", ErrInvalidName, name)
	}
	if strings.ContainsAny(name, ":,*?[]\\/ \t\r\n") {
		return fmt.Errorf("%w: %q must not contain whitespace or any of : , * ? [ ] \\ /", ErrInvalidName, name)
	}
	if _, err := ident.Parse(name); err == nil {
		return fmt.Errorf("%w: %q looks like an id", ErrInvalidName, name)
	}
	return nil
}

// Directory is the registry's read side: resolve any id for display, and map
// names, logins and service identities to live entities.
type Directory interface {
	// Resolve finds any registry id, removed ones included.
	Resolve(id ident.ID) (Entry, bool)
	// LookupName finds the live entity of kind k named name (User and
	// Connection in this slice).
	LookupName(k ident.Kind, name string) (ident.ID, bool)
	UserByLogin(login string) (User, bool)
	UserByOIDC(issuer, subject string) (User, bool)
	AccountByUID(conn ident.ID, uid string) (Account, bool)
	AccountByHandle(conn ident.ID, handle string) (Account, bool)
}

// RegistryStore is the Store's registry: users, connections and accounts.
// Create* mint the id when it is empty. Get* include removed entities; List*
// return live ones sorted by name (accounts: by id). Returned values are copies.
type RegistryStore interface {
	Directory

	CreateUser(u User) (User, error)
	RenameUser(id ident.ID, name string) error
	SetUserLogins(id ident.ID, logins []string) error
	SetUserOIDC(id ident.ID, ids []OIDCIdentity) error
	// RemoveUser tombstones the user, frees their logins and OIDC bindings,
	// and unlinks their accounts.
	RemoveUser(id ident.ID) error
	GetUser(id ident.ID) (User, bool)
	ListUsers() []User

	CreateConnection(c Connection) (Connection, error)
	RenameConnection(id ident.ID, name string) error
	// RemoveConnection tombstones the connection; ErrConnectionInUse while any
	// account belongs to it.
	RemoveConnection(id ident.ID) error
	GetConnection(id ident.ID) (Connection, bool)
	ListConnections() []Connection

	// UpsertAccount finds the account by (ConnectionID, ServiceUID), else by
	// (ConnectionID, Handle), and fills in what it learned (a missing uid, a
	// new handle or label); otherwise it creates one. The service uid is
	// authoritative: a handle held by an account with another uid (or claimed
	// by the uid's account) moves to the uid's account, and the stale holder
	// gives it up. Two accounts are never merged.
	UpsertAccount(a Account) (Account, error)
	// LinkAccount links the account to a live user; userID "" unlinks.
	LinkAccount(id, userID ident.ID) error
	GetAccount(id ident.ID) (Account, bool)
	ListAccounts(conn ident.ID) []Account
}
