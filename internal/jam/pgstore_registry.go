package jam

import "github.com/aethons-tools/cove/internal/ident"

func (s *PostgresStore) CreateUser(User) (User, error) { panic("pgstore: registry not implemented") }
func (s *PostgresStore) RenameUser(ident.ID, string) error {
	panic("pgstore: registry not implemented")
}
func (s *PostgresStore) SetUserLogins(ident.ID, []string) error {
	panic("pgstore: registry not implemented")
}
func (s *PostgresStore) SetUserOIDC(ident.ID, []OIDCIdentity) error {
	panic("pgstore: registry not implemented")
}
func (s *PostgresStore) RemoveUser(ident.ID) error { panic("pgstore: registry not implemented") }
func (s *PostgresStore) CreateConnection(Connection) (Connection, error) {
	panic("pgstore: registry not implemented")
}
func (s *PostgresStore) RenameConnection(ident.ID, string) error {
	panic("pgstore: registry not implemented")
}
func (s *PostgresStore) RemoveConnection(ident.ID) error { panic("pgstore: registry not implemented") }
func (s *PostgresStore) UpsertAccount(Account) (Account, error) {
	panic("pgstore: registry not implemented")
}
func (s *PostgresStore) LinkAccount(ident.ID, ident.ID) error {
	panic("pgstore: registry not implemented")
}
