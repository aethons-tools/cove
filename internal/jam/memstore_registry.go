package jam

import "github.com/aethons-tools/cove/internal/ident"

// ---- registry mutators: Lock; prepare via memState; apply ----

func (fs *MemStore) CreateUser(u User) (User, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	u, err := fs.prepareCreateUser(u)
	if err != nil {
		return User{}, err
	}
	fs.applyPutUser(u)
	return copyUser(u), nil
}

func (fs *MemStore) RenameUser(id ident.ID, name string) error {
	return fs.putUserWith(func() (User, error) { return fs.prepareRenameUser(id, name) })
}

func (fs *MemStore) SetUserLogins(id ident.ID, logins []string) error {
	return fs.putUserWith(func() (User, error) { return fs.prepareSetUserLogins(id, logins) })
}

func (fs *MemStore) SetUserOIDC(id ident.ID, ids []OIDCIdentity) error {
	return fs.putUserWith(func() (User, error) { return fs.prepareSetUserOIDC(id, ids) })
}

func (fs *MemStore) RemoveUser(id ident.ID) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	u, unlinked, err := fs.prepareRemoveUser(id)
	if err != nil {
		return err
	}
	fs.applyPutUser(u)
	for _, a := range unlinked {
		fs.applyPutAccount(a)
	}
	fs.applyDropMemberships(id)
	return nil
}

func (fs *MemStore) putUserWith(prepare func() (User, error)) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	u, err := prepare()
	if err != nil {
		return err
	}
	fs.applyPutUser(u)
	return nil
}

func (fs *MemStore) CreateConnection(c Connection) (Connection, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	c, err := fs.prepareCreateConnection(c)
	if err != nil {
		return Connection{}, err
	}
	fs.applyPutConnection(c)
	return c, nil
}

func (fs *MemStore) RenameConnection(id ident.ID, name string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	c, err := fs.prepareRenameConnection(id, name)
	if err != nil {
		return err
	}
	fs.applyPutConnection(c)
	return nil
}

func (fs *MemStore) RemoveConnection(id ident.ID) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	c, err := fs.prepareRemoveConnection(id)
	if err != nil {
		return err
	}
	fs.applyPutConnection(c)
	return nil
}

func (fs *MemStore) UpsertAccount(a Account) (Account, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	a, displaced, err := fs.prepareUpsertAccount(a)
	if err != nil {
		return Account{}, err
	}
	for _, d := range displaced {
		fs.applyPutAccount(d)
	}
	fs.applyPutAccount(a)
	return a, nil
}

func (fs *MemStore) LinkAccount(id, userID ident.ID) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	a, err := fs.prepareLinkAccount(id, userID)
	if err != nil {
		return err
	}
	fs.applyPutAccount(a)
	return nil
}

func (fs *MemStore) AddMember(project, user ident.ID) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if _, err := fs.prepareAddMember(project, user); err != nil {
		return err
	}
	fs.applyAddMember(project, user)
	return nil
}

func (fs *MemStore) RemoveMember(project, user ident.ID) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if err := fs.prepareRemoveMember(project, user); err != nil {
		return err
	}
	fs.applyRemoveMember(project, user)
	return nil
}
