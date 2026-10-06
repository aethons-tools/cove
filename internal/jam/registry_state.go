package jam

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/aethons-tools/cove/internal/ident"
)

// ---- registry reads (RLock; promoted to the embedding Store) ----

func (m *memState) Resolve(id ident.ID) (Entry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	switch id.Kind() {
	case ident.User:
		if u, ok := m.users[id]; ok {
			return Entry{ID: id, Kind: ident.User, Name: u.Name, Status: u.Status}, true
		}
	case ident.Connection:
		if c, ok := m.connections[id]; ok {
			return Entry{ID: id, Kind: ident.Connection, Name: c.Name, Status: c.Status}, true
		}
	case ident.Account:
		if a, ok := m.accounts[id]; ok {
			return Entry{ID: id, Kind: ident.Account, Name: accountName(a), Status: a.Status}, true
		}
	}
	return Entry{}, false
}

// accountName is an account's display name: its label, else its handle, else
// its service uid.
func accountName(a Account) string {
	return cmp.Or(a.Label, a.Handle, a.ServiceUID)
}

func (m *memState) LookupName(k ident.Kind, name string) (ident.ID, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	switch k {
	case ident.User:
		if u, ok := m.liveUserNamed(name); ok {
			return u.ID, true
		}
	}
	return "", false
}

func (m *memState) UserByLogin(login string) (User, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, u := range m.users {
		if u.Status == StatusLive && slices.Contains(u.Logins, login) {
			return copyUser(u), true
		}
	}
	return User{}, false
}

func (m *memState) UserByOIDC(issuer, subject string) (User, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	want := OIDCIdentity{Issuer: issuer, Subject: subject}
	for _, u := range m.users {
		if u.Status == StatusLive && slices.Contains(u.OIDC, want) {
			return copyUser(u), true
		}
	}
	return User{}, false
}

func (m *memState) GetUser(id ident.ID) (User, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	u, ok := m.users[id]
	return copyUser(u), ok
}

func (m *memState) ListUsers() []User {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []User
	for _, u := range m.users {
		if u.Status == StatusLive {
			out = append(out, copyUser(u))
		}
	}
	slices.SortFunc(out, func(a, b User) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// ---- lock-free helpers (caller holds mu) ----

func (m *memState) liveUserNamed(name string) (User, bool) {
	for _, u := range m.users {
		if u.Status == StatusLive && u.Name == name {
			return u, true
		}
	}
	return User{}, false
}

// liveUser returns the user for a mutation: ErrUserNotFound when absent,
// ErrRemoved when tombstoned.
func (m *memState) liveUser(id ident.ID) (User, error) {
	u, ok := m.users[id]
	if !ok {
		return User{}, fmt.Errorf("%w: %s", ErrUserNotFound, id)
	}
	if u.Status != StatusLive {
		return User{}, fmt.Errorf("%w: user %s", ErrRemoved, id)
	}
	return u, nil
}

// newEntityID returns id when set (validated as kind k and unused), else a
// fresh id of kind k.
func (m *memState) newEntityID(id ident.ID, k ident.Kind) (ident.ID, error) {
	if id == "" {
		return ident.New(k), nil
	}
	if _, err := ident.Parse(string(id)); err != nil || id.Kind() != k {
		return "", fmt.Errorf("id %q is not a valid %s id", id, k)
	}
	if m.idExists(id) {
		return "", fmt.Errorf("id %q already exists", id)
	}
	return id, nil
}

// idExists reports whether any registry entity (removed included) has id.
func (m *memState) idExists(id ident.ID) bool {
	_, u := m.users[id]
	_, c := m.connections[id]
	_, a := m.accounts[id]
	return u || c || a
}

func (m *memState) checkUserName(name string, self ident.ID) error {
	if err := ValidateEntityName(name); err != nil {
		return err
	}
	if other, ok := m.liveUserNamed(name); ok && other.ID != self {
		return fmt.Errorf("%w: user %q", ErrNameTaken, name)
	}
	return nil
}

func (m *memState) checkLogins(logins []string, self ident.ID) error {
	for i, l := range logins {
		if l == "" {
			return fmt.Errorf("login must be non-empty")
		}
		if slices.Contains(logins[:i], l) {
			return fmt.Errorf("login %q is listed twice", l)
		}
		for _, u := range m.users {
			if u.ID != self && u.Status == StatusLive && slices.Contains(u.Logins, l) {
				return fmt.Errorf("%w: %q", ErrLoginTaken, l)
			}
		}
	}
	return nil
}

func (m *memState) checkOIDC(ids []OIDCIdentity, self ident.ID) error {
	if err := ValidateIdentity(ids); err != nil {
		return err
	}
	for i, id := range ids {
		if slices.Contains(ids[:i], id) {
			return fmt.Errorf("oidc identity %s:%s is listed twice", id.Issuer, id.Subject)
		}
		for _, u := range m.users {
			if u.ID != self && u.Status == StatusLive && slices.Contains(u.OIDC, id) {
				return fmt.Errorf("%w: %s:%s", ErrIdentityTaken, id.Issuer, id.Subject)
			}
		}
	}
	return nil
}

// ---- user prepares: validate and compute the value to persist ----

func (m *memState) prepareCreateUser(u User) (User, error) {
	id, err := m.newEntityID(u.ID, ident.User)
	if err != nil {
		return User{}, err
	}
	if err := m.checkUserName(u.Name, id); err != nil {
		return User{}, err
	}
	if err := m.checkLogins(u.Logins, id); err != nil {
		return User{}, err
	}
	if err := m.checkOIDC(u.OIDC, id); err != nil {
		return User{}, err
	}
	u = copyUser(u)
	u.ID, u.Status = id, StatusLive
	return u, nil
}

func (m *memState) prepareRenameUser(id ident.ID, name string) (User, error) {
	u, err := m.liveUser(id)
	if err != nil {
		return User{}, err
	}
	if err := m.checkUserName(name, id); err != nil {
		return User{}, err
	}
	u = copyUser(u)
	u.Name = name
	return u, nil
}

func (m *memState) prepareSetUserLogins(id ident.ID, logins []string) (User, error) {
	u, err := m.liveUser(id)
	if err != nil {
		return User{}, err
	}
	if err := m.checkLogins(logins, id); err != nil {
		return User{}, err
	}
	u = copyUser(u)
	u.Logins = slices.Clone(logins)
	return u, nil
}

func (m *memState) prepareSetUserOIDC(id ident.ID, ids []OIDCIdentity) (User, error) {
	u, err := m.liveUser(id)
	if err != nil {
		return User{}, err
	}
	if err := m.checkOIDC(ids, id); err != nil {
		return User{}, err
	}
	u = copyUser(u)
	u.OIDC = slices.Clone(ids)
	return u, nil
}

// prepareRemoveUser returns the tombstoned user (logins and OIDC cleared) and
// the accounts it unlinks.
func (m *memState) prepareRemoveUser(id ident.ID) (User, []Account, error) {
	u, err := m.liveUser(id)
	if err != nil {
		return User{}, nil, err
	}
	u = copyUser(u)
	u.Status, u.Logins, u.OIDC = StatusRemoved, nil, nil
	var unlinked []Account
	for _, a := range m.accounts {
		if a.UserID == id {
			a.UserID = ""
			unlinked = append(unlinked, a)
		}
	}
	return u, unlinked, nil
}

// ---- apply (caller holds mu.Lock) ----

func (m *memState) applyPutUser(u User)       { m.users[u.ID] = copyUser(u) }
func (m *memState) applyPutAccount(a Account) { m.accounts[a.ID] = a }

func copyUser(u User) User {
	u.Logins = slices.Clone(u.Logins)
	u.OIDC = slices.Clone(u.OIDC)
	return u
}

func (m *memState) GetConnection(id ident.ID) (Connection, bool)  { return Connection{}, false }
func (m *memState) ListConnections() []Connection                 { return nil }
func (m *memState) GetAccount(id ident.ID) (Account, bool)        { return Account{}, false }
func (m *memState) ListAccounts(conn ident.ID) []Account          { return nil }
func (m *memState) AccountByUID(ident.ID, string) (Account, bool) { return Account{}, false }
func (m *memState) AccountByHandle(ident.ID, string) (Account, bool) {
	return Account{}, false
}
