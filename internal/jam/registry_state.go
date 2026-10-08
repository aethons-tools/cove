package jam

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	"github.com/aethons-tools/cove/internal/ident"
)

// ---- registry reads (RLock; promoted to the embedding Store) ----

func (m *memState) Resolve(id ident.ID) (Entry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	switch id.Kind() {
	case ident.Project:
		if p, ok := m.projectByID(id); ok {
			st := p.Status
			if st == "" {
				st = StatusLive
			}
			return Entry{ID: id, Kind: ident.Project, Name: p.Name, Status: st}, true
		}
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
	case ident.Channel:
		if c, ok := m.channels[id]; ok {
			return Entry{ID: id, Kind: ident.Channel, Name: c.Label, Status: c.Status}, true
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
	case ident.Project:
		if p, ok := m.projects[name]; ok && p.ID != "" {
			return p.ID, true
		}
	case ident.User:
		if u, ok := m.liveUserNamed(name); ok {
			return u.ID, true
		}
	case ident.Connection:
		if c, ok := m.liveConnectionNamed(name); ok {
			return c.ID, true
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
	_, p := m.projectByID(id)
	_, ch := m.channels[id]
	return u || c || a || p || ch
}

// projectByID finds a project record by its id. Caller holds mu.
func (m *memState) projectByID(id ident.ID) (Project, bool) {
	for _, p := range m.projects {
		if p.ID == id {
			return p, true
		}
	}
	p, ok := m.removedProjects[id]
	return p, ok
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

func (m *memState) GetConnection(id ident.ID) (Connection, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.connections[id]
	return c, ok
}

func (m *memState) ListConnections() []Connection {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Connection
	for _, c := range m.connections {
		if c.Status == StatusLive {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b Connection) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

func (m *memState) GetAccount(id ident.ID) (Account, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.accounts[id]
	return a, ok
}

func (m *memState) ListAccounts(conn ident.ID) []Account {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Account
	for _, a := range m.accounts {
		if a.ConnectionID == conn && a.Status == StatusLive {
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(a, b Account) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

func (m *memState) AccountByUID(conn ident.ID, uid string) (Account, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.accountBy(conn, func(a Account) bool { return uid != "" && a.ServiceUID == uid })
}

func (m *memState) AccountByHandle(conn ident.ID, handle string) (Account, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.accountBy(conn, func(a Account) bool { return handle != "" && a.Handle == handle })
}

// accountBy is the lock-free live-account finder on one connection.
func (m *memState) accountBy(conn ident.ID, match func(Account) bool) (Account, bool) {
	for _, a := range m.accounts {
		if a.ConnectionID == conn && a.Status == StatusLive && match(a) {
			return a, true
		}
	}
	return Account{}, false
}

func (m *memState) liveConnectionNamed(name string) (Connection, bool) {
	for _, c := range m.connections {
		if c.Status == StatusLive && c.Name == name {
			return c, true
		}
	}
	return Connection{}, false
}

func (m *memState) liveConnection(id ident.ID) (Connection, error) {
	c, ok := m.connections[id]
	if !ok {
		return Connection{}, fmt.Errorf("%w: %s", ErrConnectionNotFound, id)
	}
	if c.Status != StatusLive {
		return Connection{}, fmt.Errorf("%w: connection %s", ErrRemoved, id)
	}
	return c, nil
}

func (m *memState) checkConnectionName(name string, self ident.ID) error {
	if err := ValidateEntityName(name); err != nil {
		return err
	}
	if other, ok := m.liveConnectionNamed(name); ok && other.ID != self {
		return fmt.Errorf("%w: connection %q", ErrNameTaken, name)
	}
	return nil
}

func (m *memState) prepareCreateConnection(c Connection) (Connection, error) {
	if !slices.Contains(ConnectionKinds, c.Kind) {
		return Connection{}, fmt.Errorf("connection kind %q is not one of %v", c.Kind, ConnectionKinds)
	}
	id, err := m.newEntityID(c.ID, ident.Connection)
	if err != nil {
		return Connection{}, err
	}
	if err := m.checkConnectionName(c.Name, id); err != nil {
		return Connection{}, err
	}
	c.ID, c.Status = id, StatusLive
	return c, nil
}

func (m *memState) prepareRenameConnection(id ident.ID, name string) (Connection, error) {
	c, err := m.liveConnection(id)
	if err != nil {
		return Connection{}, err
	}
	if err := m.checkConnectionName(name, id); err != nil {
		return Connection{}, err
	}
	c.Name = name
	return c, nil
}

func (m *memState) prepareRemoveConnection(id ident.ID) (Connection, error) {
	c, err := m.liveConnection(id)
	if err != nil {
		return Connection{}, err
	}
	if _, used := m.accountBy(id, func(Account) bool { return true }); used {
		return Connection{}, fmt.Errorf("%w: connection %q has accounts", ErrConnectionInUse, c.Name)
	}
	for _, p := range m.projects {
		if p.ChatService == string(id) {
			return Connection{}, fmt.Errorf("%w: connection %q is project %q's chat service", ErrConnectionInUse, c.Name, p.Name)
		}
	}
	if ch, bound := m.boundConnection(id); bound {
		return Connection{}, fmt.Errorf("%w: connection %q binds %s %q", ErrConnectionInUse, c.Name, ch.Kind, ch.Key)
	}
	c.Status = StatusRemoved
	return c, nil
}

// prepareUpsertAccount implements UpsertAccount's find-or-create (see
// RegistryStore). It returns the account to write and, when the incoming
// handle was held by a different account, that stale holder with the handle
// cleared (write it first: live handles are unique). A cleared holder keeps
// its old handle as its label so it stays renderable.
func (m *memState) prepareUpsertAccount(in Account) (Account, []Account, error) {
	if _, err := m.liveConnection(in.ConnectionID); err != nil {
		return Account{}, nil, err
	}
	if in.ServiceUID == "" && in.Handle == "" {
		return Account{}, nil, fmt.Errorf("an account needs a service uid or a handle")
	}
	byUID, hasUID := m.accountBy(in.ConnectionID, func(a Account) bool { return in.ServiceUID != "" && a.ServiceUID == in.ServiceUID })
	byHandle, hasHandle := m.accountBy(in.ConnectionID, func(a Account) bool { return in.Handle != "" && a.Handle == in.Handle })
	// The uid is authoritative: the handle's holder is this same account only
	// when it is the uid's account, or when the uid is unclaimed and the
	// holder has no uid of its own to contradict it.
	sameHolder := hasHandle && (hasUID && byHandle.ID == byUID.ID ||
		!hasUID && (in.ServiceUID == "" || byHandle.ServiceUID == ""))
	var displaced []Account
	if hasHandle && !sameHolder {
		stale := byHandle
		stale.Label = accountName(stale)
		stale.Handle = ""
		displaced = append(displaced, stale)
	}
	if hasUID || sameHolder {
		a := byUID
		if !hasUID {
			a = byHandle
		}
		if in.ServiceUID != "" {
			a.ServiceUID = in.ServiceUID
		}
		if in.Handle != "" {
			a.Handle = in.Handle
		}
		if in.Label != "" {
			a.Label = in.Label
		}
		return a, displaced, nil
	}
	id, err := m.newEntityID(in.ID, ident.Account)
	if err != nil {
		return Account{}, nil, err
	}
	if in.UserID != "" {
		if _, err := m.liveUser(in.UserID); err != nil {
			return Account{}, nil, err
		}
	}
	in.ID, in.Status = id, StatusLive
	return in, displaced, nil
}

func (m *memState) prepareLinkAccount(id, userID ident.ID) (Account, error) {
	a, ok := m.accounts[id]
	if !ok || a.Status != StatusLive {
		return Account{}, fmt.Errorf("%w: %s", ErrAccountNotFound, id)
	}
	if userID != "" {
		if _, err := m.liveUser(userID); err != nil {
			return Account{}, err
		}
	}
	a.UserID = userID
	return a, nil
}

func (m *memState) applyPutConnection(c Connection) { m.connections[c.ID] = c }

// ---- memberships ----

func (m *memState) IsMember(project, user ident.ID) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.members[project][user]
	return ok
}

func (m *memState) GetMembership(project, user ident.ID) (Membership, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ms, ok := m.members[project][user]
	return copyMembership(ms), ok
}

func (m *memState) ListMembers(project ident.ID) []ident.ID {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return slices.Sorted(maps.Keys(m.members[project]))
}

func (m *memState) ListMemberships(user ident.ID) []ident.ID {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []ident.ID
	for project, users := range m.members {
		if _, ok := users[user]; ok {
			out = append(out, project)
		}
	}
	slices.Sort(out)
	return out
}

func (m *memState) LegacyHumanAlias(project, name string) (ident.ID, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.aliases[project][name]
	return id, ok
}

// prepareAddMember validates an AddMember: the project exists and the user is
// live. It returns the membership to write (the existing one, delivery kept)
// and whether it is new.
func (m *memState) prepareAddMember(project, user ident.ID) (Membership, bool, error) {
	if err := m.checkMember(project, user); err != nil {
		return Membership{}, false, err
	}
	if ms, ok := m.members[project][user]; ok {
		return copyMembership(ms), false, nil
	}
	return Membership{ProjectID: project, UserID: user}, true, nil
}

// preparePutMembership validates a PutMembership and returns the value to write.
func (m *memState) preparePutMembership(ms Membership) (Membership, error) {
	if err := m.checkMember(ms.ProjectID, ms.UserID); err != nil {
		return Membership{}, err
	}
	for _, d := range ms.Delivery {
		if d.Service == "" || d.Address == "" {
			return Membership{}, fmt.Errorf("a membership delivery needs a service and an address")
		}
	}
	ms = copyMembership(ms)
	for i := range ms.Delivery {
		ms.Delivery[i].UserID = "" // the service account is an Account, not delivery
	}
	return ms, nil
}

func (m *memState) checkMember(project, user ident.ID) error {
	if _, ok := m.liveProjectByID(project); !ok {
		return fmt.Errorf("%w: %s", ErrProjectNotFound, project)
	}
	_, err := m.liveUser(user)
	return err
}

func (m *memState) prepareRemoveMember(project, user ident.ID) error {
	if _, ok := m.members[project][user]; !ok {
		return fmt.Errorf("%w: %s in %s", ErrMembershipNotFound, user, project)
	}
	return nil
}

func (m *memState) applyPutMembership(ms Membership) {
	if m.members[ms.ProjectID] == nil {
		m.members[ms.ProjectID] = map[ident.ID]Membership{}
	}
	m.members[ms.ProjectID][ms.UserID] = copyMembership(ms)
}

func (m *memState) applyRemoveMember(project, user ident.ID) {
	delete(m.members[project], user)
	if len(m.members[project]) == 0 {
		delete(m.members, project)
	}
}

// applyDropMemberships ends every membership of user (RemoveUser).
func (m *memState) applyDropMemberships(user ident.ID) {
	for project := range m.members {
		m.applyRemoveMember(project, user)
	}
}

func copyMembership(ms Membership) Membership {
	ms.Delivery = slices.Clone(ms.Delivery)
	return ms
}
