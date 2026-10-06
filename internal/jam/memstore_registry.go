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

func (fs *MemStore) CreateConnection(Connection) (Connection, error) { panic("task 4") }
func (fs *MemStore) RenameConnection(ident.ID, string) error         { panic("task 4") }
func (fs *MemStore) RemoveConnection(ident.ID) error                 { panic("task 4") }
func (fs *MemStore) UpsertAccount(Account) (Account, error)          { panic("task 4") }
func (fs *MemStore) LinkAccount(ident.ID, ident.ID) error            { panic("task 4") }
