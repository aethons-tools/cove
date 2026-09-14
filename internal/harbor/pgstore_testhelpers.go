//go:build integration

package harbor

import "context"

// TruncateAllForTest clears every control-plane table and resets the in-memory
// cache, so an integration test can start each case from an empty store while
// reusing the connection pool. Test-only: compiled only under the integration
// build tag.
func (s *PostgresStore) TruncateAllForTest(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `TRUNCATE actors, roles, kits, instances, destinations, projects`); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roles = map[string]map[string]Role{}
	s.actors = map[string]Actor{}
	s.dests = map[string]Destination{}
	s.kits = map[string]Kit{}
	s.instances = map[string]Instance{}
	s.projects = map[string]Project{}
	return nil
}
