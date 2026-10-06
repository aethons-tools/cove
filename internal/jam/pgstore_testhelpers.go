//go:build integration

package jam

import (
	"context"

	"github.com/aethons-tools/cove/internal/ident"
)

// TruncateAllForTest clears every control-plane table and resets the in-memory
// cache, so an integration test can start each case from an empty store while
// reusing the connection pool. Test-only: compiled only under the integration
// build tag.
func (s *PostgresStore) TruncateAllForTest(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `TRUNCATE actors, roles, kits, instances, destinations, model_specs, projects, intercom_unread_cursors, legacy_human_aliases, memberships, accounts, connections, user_oidc, user_logins, users, participants`); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roles = map[string]map[string]Role{}
	s.actors = map[string]Actor{}
	s.dests = map[string]Destination{}
	s.specs = map[string]ModelSpec{}
	s.kits = map[string]Kit{}
	s.instances = map[string]Instance{}
	s.projects = map[string]Project{}
	s.unread = map[string]map[string]int64{}
	s.users = map[ident.ID]User{}
	s.connections = map[ident.ID]Connection{}
	s.accounts = map[ident.ID]Account{}
	s.members = map[ident.ID]map[ident.ID]Membership{}
	s.aliases = map[string]map[string]ident.ID{}
	return nil
}
