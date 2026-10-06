package jam

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/jackc/pgx/v5"
)

// ---- registry mutators: Lock; prepare via memState; SQL (one tx); apply ----

func (s *PostgresStore) CreateUser(u User) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.prepareCreateUser(u)
	if err != nil {
		return User{}, err
	}
	if err := s.registryTx("CreateUser", func(ctx context.Context, tx pgx.Tx) error {
		return putUserTx(ctx, tx, u, nil)
	}); err != nil {
		return User{}, err
	}
	s.applyPutUser(u)
	return copyUser(u), nil
}

func (s *PostgresStore) RenameUser(id ident.ID, name string) error {
	return s.putUserWith("RenameUser", func() (User, error) { return s.prepareRenameUser(id, name) })
}

func (s *PostgresStore) SetUserLogins(id ident.ID, logins []string) error {
	return s.putUserWith("SetUserLogins", func() (User, error) { return s.prepareSetUserLogins(id, logins) })
}

func (s *PostgresStore) SetUserOIDC(id ident.ID, ids []OIDCIdentity) error {
	return s.putUserWith("SetUserOIDC", func() (User, error) { return s.prepareSetUserOIDC(id, ids) })
}

func (s *PostgresStore) RemoveUser(id ident.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, unlinked, err := s.prepareRemoveUser(id)
	if err != nil {
		return err
	}
	if err := s.registryTx("RemoveUser", func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM memberships WHERE user_id = $1`, id); err != nil {
			return err
		}
		return putUserTx(ctx, tx, u, unlinked)
	}); err != nil {
		return err
	}
	s.applyPutUser(u)
	for _, a := range unlinked {
		s.applyPutAccount(a)
	}
	s.applyDropMemberships(id)
	return nil
}

func (s *PostgresStore) putUserWith(op string, prepare func() (User, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := prepare()
	if err != nil {
		return err
	}
	if err := s.registryTx(op, func(ctx context.Context, tx pgx.Tx) error {
		return putUserTx(ctx, tx, u, nil)
	}); err != nil {
		return err
	}
	s.applyPutUser(u)
	return nil
}

func (s *PostgresStore) CreateConnection(c Connection) (Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.prepareCreateConnection(c)
	if err != nil {
		return Connection{}, err
	}
	if err := s.registryTx("CreateConnection", func(ctx context.Context, tx pgx.Tx) error {
		return putConnectionTx(ctx, tx, c)
	}); err != nil {
		return Connection{}, err
	}
	s.applyPutConnection(c)
	return c, nil
}

func (s *PostgresStore) RenameConnection(id ident.ID, name string) error {
	return s.putConnectionWith("RenameConnection", func() (Connection, error) { return s.prepareRenameConnection(id, name) })
}

func (s *PostgresStore) SetConnectionCred(id ident.ID, cred string) error {
	return s.putConnectionWith("SetConnectionCred", func() (Connection, error) { return s.prepareSetConnectionCred(id, cred) })
}

func (s *PostgresStore) RemoveConnection(id ident.ID) error {
	return s.putConnectionWith("RemoveConnection", func() (Connection, error) { return s.prepareRemoveConnection(id) })
}

func (s *PostgresStore) putConnectionWith(op string, prepare func() (Connection, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := prepare()
	if err != nil {
		return err
	}
	if err := s.registryTx(op, func(ctx context.Context, tx pgx.Tx) error {
		return putConnectionTx(ctx, tx, c)
	}); err != nil {
		return err
	}
	s.applyPutConnection(c)
	return nil
}

func (s *PostgresStore) UpsertAccount(a Account) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, displaced, err := s.prepareUpsertAccount(a)
	if err != nil {
		return Account{}, err
	}
	if err := s.registryTx("UpsertAccount", func(ctx context.Context, tx pgx.Tx) error {
		// The stale holder gives up the handle first: live handles are unique.
		for _, d := range displaced {
			if err := putAccountTx(ctx, tx, d); err != nil {
				return err
			}
		}
		return putAccountTx(ctx, tx, a)
	}); err != nil {
		return Account{}, err
	}
	for _, d := range displaced {
		s.applyPutAccount(d)
	}
	s.applyPutAccount(a)
	return a, nil
}

func (s *PostgresStore) LinkAccount(id, userID ident.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.prepareLinkAccount(id, userID)
	if err != nil {
		return err
	}
	if err := s.registryTx("LinkAccount", func(ctx context.Context, tx pgx.Tx) error {
		return putAccountTx(ctx, tx, a)
	}); err != nil {
		return err
	}
	s.applyPutAccount(a)
	return nil
}

// ---- SQL ----

func (s *PostgresStore) registryTx(op string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	ctx := context.Background()
	if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return fn(ctx, tx) }); err != nil {
		return fmt.Errorf("pgstore: %s: %w", op, err)
	}
	return nil
}

func insertParticipantTx(ctx context.Context, tx pgx.Tx, id ident.ID) error {
	_, err := tx.Exec(ctx, `INSERT INTO participants (id, kind) VALUES ($1,$2) ON CONFLICT (id) DO NOTHING`, id, string(id.Kind()))
	return err
}

// putUserTx upserts u, replaces its login and OIDC rows (a removed user has
// none), and writes the accounts the change unlinked.
func putUserTx(ctx context.Context, tx pgx.Tx, u User, unlinked []Account) error {
	if err := insertParticipantTx(ctx, tx, u.ID); err != nil {
		return err
	}
	doc, err := json.Marshal(u)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO users (id, name, status, doc) VALUES ($1,$2,$3,$4)
		 ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, status = EXCLUDED.status, doc = EXCLUDED.doc, updated_at = now()`,
		u.ID, u.Name, string(u.Status), doc); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_logins WHERE user_id = $1`, u.ID); err != nil {
		return err
	}
	for _, l := range u.Logins {
		if _, err := tx.Exec(ctx, `INSERT INTO user_logins (login, user_id) VALUES ($1,$2)`, l, u.ID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_oidc WHERE user_id = $1`, u.ID); err != nil {
		return err
	}
	for _, o := range u.OIDC {
		if _, err := tx.Exec(ctx, `INSERT INTO user_oidc (issuer, subject, user_id) VALUES ($1,$2,$3)`, o.Issuer, o.Subject, u.ID); err != nil {
			return err
		}
	}
	for _, a := range unlinked {
		if err := putAccountTx(ctx, tx, a); err != nil {
			return err
		}
	}
	return nil
}

func putConnectionTx(ctx context.Context, tx pgx.Tx, c Connection) error {
	if err := insertParticipantTx(ctx, tx, c.ID); err != nil {
		return err
	}
	doc, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO connections (id, kind, name, status, doc) VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, status = EXCLUDED.status, doc = EXCLUDED.doc, updated_at = now()`,
		c.ID, c.Kind, c.Name, string(c.Status), doc)
	return err
}

func putAccountTx(ctx context.Context, tx pgx.Tx, a Account) error {
	if err := insertParticipantTx(ctx, tx, a.ID); err != nil {
		return err
	}
	doc, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO accounts (id, connection_id, service_uid, handle, user_id, status, doc)
		 VALUES ($1,$2,NULLIF($3,''),NULLIF($4,''),NULLIF($5,''),$6,$7)
		 ON CONFLICT (id) DO UPDATE SET service_uid = EXCLUDED.service_uid, handle = EXCLUDED.handle,
		   user_id = EXCLUDED.user_id, status = EXCLUDED.status, doc = EXCLUDED.doc, updated_at = now()`,
		a.ID, a.ConnectionID, a.ServiceUID, a.Handle, string(a.UserID), string(a.Status), doc)
	return err
}

// loadRegistry fills the registry maps from their docs (load's registry part).
func (s *PostgresStore) loadRegistry(ctx context.Context) error {
	if err := s.loadDocs(ctx, "users", func(doc []byte) error {
		var u User
		if err := json.Unmarshal(doc, &u); err != nil {
			return err
		}
		s.users[u.ID] = u
		return nil
	}); err != nil {
		return err
	}
	if err := s.loadDocs(ctx, "connections", func(doc []byte) error {
		var c Connection
		if err := json.Unmarshal(doc, &c); err != nil {
			return err
		}
		s.connections[c.ID] = c
		return nil
	}); err != nil {
		return err
	}
	return s.loadDocs(ctx, "accounts", func(doc []byte) error {
		var a Account
		if err := json.Unmarshal(doc, &a); err != nil {
			return err
		}
		s.accounts[a.ID] = a
		return nil
	})
}

// ---- projects and memberships ----

func (s *PostgresStore) AddMember(project, user ident.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ms, added, err := s.prepareAddMember(project, user)
	if err != nil || !added {
		return err
	}
	if err := s.registryTx("AddMember", func(ctx context.Context, tx pgx.Tx) error {
		return putMembershipTx(ctx, tx, ms)
	}); err != nil {
		return err
	}
	s.applyPutMembership(ms)
	return nil
}

func (s *PostgresStore) PutMembership(ms Membership) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ms, err := s.preparePutMembership(ms)
	if err != nil {
		return err
	}
	if err := s.registryTx("PutMembership", func(ctx context.Context, tx pgx.Tx) error {
		return putMembershipTx(ctx, tx, ms)
	}); err != nil {
		return err
	}
	s.applyPutMembership(ms)
	return nil
}

func putMembershipTx(ctx context.Context, tx pgx.Tx, ms Membership) error {
	delivery, err := json.Marshal(ms.Delivery)
	if err != nil {
		return err
	}
	if ms.Delivery == nil {
		delivery = []byte("[]")
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO memberships (project_id, user_id, delivery) VALUES ($1,$2,$3)
		 ON CONFLICT (project_id, user_id) DO UPDATE SET delivery = EXCLUDED.delivery`,
		ms.ProjectID, ms.UserID, delivery)
	return err
}

func (s *PostgresStore) RemoveMember(project, user ident.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.prepareRemoveMember(project, user); err != nil {
		return err
	}
	if err := s.registryTx("RemoveMember", func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM memberships WHERE project_id = $1 AND user_id = $2`, project, user)
		return err
	}); err != nil {
		return err
	}
	s.applyRemoveMember(project, user)
	return nil
}

// insertProjectTx inserts a new project row with its id (and the id's
// participants row).
func insertProjectTx(ctx context.Context, tx pgx.Tx, p Project) error {
	p.Roster.Humans = nil // the registry holds them (roster_view.go)
	if err := insertParticipantTx(ctx, tx, p.ID); err != nil {
		return err
	}
	doc, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO projects (name, id, doc) VALUES ($1,$2,$3)`, p.Name, p.ID, doc)
	return err
}

// upsertProjectTx writes a project row by name, keeping its id column in step
// with the doc.
func upsertProjectTx(ctx context.Context, tx pgx.Tx, p Project) error {
	p.Roster.Humans = nil // the registry holds them (roster_view.go)
	if err := insertParticipantTx(ctx, tx, p.ID); err != nil {
		return err
	}
	doc, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO projects (name, id, doc) VALUES ($1,$2,$3)
		 ON CONFLICT (name) DO UPDATE SET id = EXCLUDED.id, doc = EXCLUDED.doc, version = projects.version + 1, updated_at = now()`,
		p.Name, p.ID, doc)
	return err
}

// ensureProjectIDs gives every loaded project whose doc lacks an id one and
// persists it, so later loads see the same id. The row is locked and an id
// already in its column wins (a concurrent start backfilled it, or an older
// binary rewrote the doc without it), so every process converges on one id.
// It then loads the memberships, which reference those ids. load's project
// part; no lock.
func (s *PostgresStore) ensureProjectIDs(ctx context.Context) error {
	for name, p := range s.projects {
		if p.ID != "" {
			continue
		}
		if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			var have *string
			if err := tx.QueryRow(ctx, `SELECT id FROM projects WHERE name = $1 FOR UPDATE`, name).Scan(&have); err != nil {
				return err
			}
			if have != nil {
				p.ID = ident.ID(*have)
			} else {
				p.ID = newProject(name).ID
				if err := insertParticipantTx(ctx, tx, p.ID); err != nil {
					return err
				}
			}
			doc, err := json.Marshal(p)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `UPDATE projects SET id = $1, doc = $2 WHERE name = $3`, p.ID, doc, name)
			return err
		}); err != nil {
			return fmt.Errorf("pgstore: backfill project id for %q: %w", name, err)
		}
		s.projects[name] = p
	}
	rows, err := s.pool.Query(ctx, `SELECT project_id, user_id, delivery FROM memberships`)
	if err != nil {
		return fmt.Errorf("pgstore: load memberships: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var project, user string
		var delivery []byte
		if err := rows.Scan(&project, &user, &delivery); err != nil {
			return err
		}
		ms := Membership{ProjectID: ident.ID(project), UserID: ident.ID(user)}
		if err := json.Unmarshal(delivery, &ms.Delivery); err != nil {
			return fmt.Errorf("pgstore: decode membership delivery: %w", err)
		}
		if len(ms.Delivery) == 0 {
			ms.Delivery = nil
		}
		s.applyPutMembership(ms)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := s.loadLegacyAliases(ctx); err != nil {
		return err
	}
	if err := s.loadStandingSessions(ctx); err != nil {
		return err
	}
	return s.loadChannels(ctx)
}

// loadLegacyAliases fills the frozen legacy_human_aliases map. load's part; no lock.
func (s *PostgresStore) loadLegacyAliases(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT project_name, human_name, user_id FROM legacy_human_aliases`)
	if err != nil {
		return fmt.Errorf("pgstore: load legacy_human_aliases: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var project, name, user string
		if err := rows.Scan(&project, &name, &user); err != nil {
			return err
		}
		if s.aliases[project] == nil {
			s.aliases[project] = map[string]ident.ID{}
		}
		s.aliases[project][name] = ident.ID(user)
	}
	return rows.Err()
}

func (s *PostgresStore) PutStandingSession(project ident.ID, role, name, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.preparePutStandingSession(project, role, name, sessionID); err != nil {
		return err
	}
	if err := s.registryTx("PutStandingSession", func(ctx context.Context, tx pgx.Tx) error {
		return putStandingSessionTx(ctx, tx, project, role, name, sessionID)
	}); err != nil {
		return err
	}
	s.applyPutStandingSession(project, role, name, sessionID)
	return nil
}

func putStandingSessionTx(ctx context.Context, tx pgx.Tx, project ident.ID, role, name, sessionID string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO standing_sessions (project_id, role, name, session_id) VALUES ($1,$2,$3,$4)
		 ON CONFLICT (project_id, role, name) DO UPDATE SET session_id = EXCLUDED.session_id`,
		project, role, name, sessionID)
	return err
}

func (s *PostgresStore) RemoveStandingSession(project ident.ID, role, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.registryTx("RemoveStandingSession", func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM standing_sessions WHERE project_id = $1 AND role = $2 AND name = $3`, project, role, name)
		return err
	}); err != nil {
		return err
	}
	s.applyRemoveStandingSession(project, role, name)
	return nil
}

// loadStandingSessions fills the standing-session map. load's part; no lock.
func (s *PostgresStore) loadStandingSessions(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT project_id, role, name, session_id FROM standing_sessions`)
	if err != nil {
		return fmt.Errorf("pgstore: load standing_sessions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var project, role, name, id string
		if err := rows.Scan(&project, &role, &name, &id); err != nil {
			return err
		}
		s.applyPutStandingSession(ident.ID(project), role, name, id)
	}
	return rows.Err()
}
