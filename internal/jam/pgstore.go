package jam

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore is a jam.Store backed by Postgres (the durable source of
// truth) with an in-memory write-through cache (the embedded *memState) serving
// all reads. The serve process is the sole writer (Phase 1). Every mutation
// writes to Postgres and updates the cache only after the write commits
// (commit-then-cache); a failed write leaves the cache untouched. Reads are
// inherited from memState and never touch the DB.
type PostgresStore struct {
	*memState
	pool *pgxpool.Pool
	log  *slog.Logger
}

// migrateAdvisoryLock is an arbitrary constant key so concurrent starts of the
// same Jam serialize their migration step (belt-and-braces; Phase 1 is
// single-writer).
//
// The value spells "harbor" (Jam's former name) and deliberately survived the
// Harbor → Jam rename: changing it would let an old and a new binary migrate
// the same database concurrently during a rollout, since each would take a
// different lock. Do not change it. See docs/usage/jam/renamed-from-harbor.md.
const migrateAdvisoryLock = 0x686172626f72 // "harbor" — kept across the rename; see above

// NewPostgresStore connects, applies embedded migrations, loads all rows into
// the cache, and returns a ready store. It fails closed: any connect, migrate,
// or load error returns an error and no usable store.
func NewPostgresStore(ctx context.Context, dsn string, log *slog.Logger) (*PostgresStore, error) {
	if log == nil {
		log = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgstore: connect: %w", err)
	}
	s := &PostgresStore{memState: newMemState(), pool: pool, log: log}
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := s.load(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

// PostgresStore implements Store.
var _ Store = (*PostgresStore)(nil)

// Close releases the connection pool.
func (s *PostgresStore) Close() { s.pool.Close() }

// Pool returns the underlying connection pool so colocated subsystems (e.g. the
// Postgres message log) can share this store's database. The pool's lifecycle is
// owned by the store; callers must not close it.
func (s *PostgresStore) Pool() *pgxpool.Pool { return s.pool }

// migrate applies any embedded migrations not yet recorded in schema_migrations,
// under an advisory lock so concurrent starts don't race.
func (s *PostgresStore) migrate(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(migrateAdvisoryLock)); err != nil {
			return fmt.Errorf("pgstore: advisory lock: %w", err)
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version int PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
			return fmt.Errorf("pgstore: create schema_migrations: %w", err)
		}
		applied := map[int]bool{}
		rows, err := tx.Query(ctx, `SELECT version FROM schema_migrations`)
		if err != nil {
			return fmt.Errorf("pgstore: read schema_migrations: %w", err)
		}
		for rows.Next() {
			var v int
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return err
			}
			applied[v] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		entries, err := fs.ReadDir(migrationFiles, "migrations")
		if err != nil {
			return fmt.Errorf("pgstore: read migrations: %w", err)
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			ver, err := migrationVersion(name)
			if err != nil {
				return err
			}
			if applied[ver] {
				continue
			}
			sqlText, err := migrationFiles.ReadFile("migrations/" + name)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, string(sqlText)); err != nil {
				return fmt.Errorf("pgstore: apply migration %s: %w", name, err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, ver); err != nil {
				return fmt.Errorf("pgstore: record migration %d: %w", ver, err)
			}
			s.log.Info("pgstore: applied migration", "version", ver, "file", name)
		}
		return nil
	})
}

// migrationVersion parses the leading integer of a migration file name
// ("0001_init.sql" -> 1).
func migrationVersion(name string) (int, error) {
	i := strings.IndexByte(name, '_')
	if i <= 0 {
		return 0, fmt.Errorf("pgstore: bad migration name %q (want <number>_<desc>.sql)", name)
	}
	v, err := strconv.Atoi(name[:i])
	if err != nil {
		return 0, fmt.Errorf("pgstore: bad migration version in %q: %w", name, err)
	}
	return v, nil
}

// load reads every row into the cache. Called once at construction before the
// store is shared, so it populates the maps without locking.
func (s *PostgresStore) load(ctx context.Context) error {
	if err := s.loadDocs(ctx, "actors", func(doc []byte) error {
		var a Actor
		if err := json.Unmarshal(doc, &a); err != nil {
			return err
		}
		s.actors[a.TokenHash] = a
		return nil
	}); err != nil {
		return err
	}
	// roles carry their project in a column, not the doc.
	rows, err := s.pool.Query(ctx, `SELECT project, doc FROM roles`)
	if err != nil {
		return fmt.Errorf("pgstore: load roles: %w", err)
	}
	for rows.Next() {
		var project string
		var doc []byte
		if err := rows.Scan(&project, &doc); err != nil {
			rows.Close()
			return err
		}
		var r Role
		if err := json.Unmarshal(doc, &r); err != nil {
			rows.Close()
			return err
		}
		if s.roles[project] == nil {
			s.roles[project] = map[string]Role{}
		}
		s.roles[project][r.Name] = r
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if err := s.loadDocs(ctx, "kits", func(doc []byte) error {
		var k Kit
		if err := json.Unmarshal(doc, &k); err != nil {
			return err
		}
		s.kits[k.Name] = k
		return nil
	}); err != nil {
		return err
	}
	if err := s.loadDocs(ctx, "instances", func(doc []byte) error {
		var i Instance
		if err := json.Unmarshal(doc, &i); err != nil {
			return err
		}
		s.instances[i.ActorID] = i
		return nil
	}); err != nil {
		return err
	}
	if err := s.loadDocs(ctx, "destinations", func(doc []byte) error {
		var d Destination
		if err := json.Unmarshal(doc, &d); err != nil {
			return err
		}
		s.dests[d.Name] = d
		return nil
	}); err != nil {
		return err
	}
	if err := s.loadDocs(ctx, "projects", func(doc []byte) error {
		var p Project
		if err := json.Unmarshal(doc, &p); err != nil {
			return err
		}
		s.projects[p.Name] = p
		return nil
	}); err != nil {
		return err
	}
	// intercom-UI unread cursors: (participant, channel) → last-seen Seq.
	curs, err := s.pool.Query(ctx, `SELECT participant, channel, seq FROM intercom_unread_cursors`)
	if err != nil {
		return fmt.Errorf("pgstore: load intercom_unread_cursors: %w", err)
	}
	defer curs.Close()
	for curs.Next() {
		var participant, channel string
		var seq int64
		if err := curs.Scan(&participant, &channel, &seq); err != nil {
			return err
		}
		if s.unread[participant] == nil {
			s.unread[participant] = map[string]int64{}
		}
		s.unread[participant][channel] = seq
	}
	return curs.Err()
}

func (s *PostgresStore) loadDocs(ctx context.Context, table string, unmarshal func(doc []byte) error) error {
	rows, err := s.pool.Query(ctx, `SELECT doc FROM `+table)
	if err != nil {
		return fmt.Errorf("pgstore: load %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var doc []byte
		if err := rows.Scan(&doc); err != nil {
			return err
		}
		if err := unmarshal(doc); err != nil {
			return fmt.Errorf("pgstore: decode %s row: %w", table, err)
		}
	}
	return rows.Err()
}

// exec runs a single write statement (implicitly atomic) and wraps its error.
func (s *PostgresStore) exec(op, sql string, args ...any) error {
	if _, err := s.pool.Exec(context.Background(), sql, args...); err != nil {
		return fmt.Errorf("pgstore: %s: %w", op, err)
	}
	return nil
}

// execWithProjects runs one write statement in a transaction that first inserts
// the given project rows (the DefaultProject a write materializes), so the
// project and the row referencing it commit together or not at all.
func (s *PostgresStore) execWithProjects(op string, projects []Project, sql string, args ...any) error {
	if len(projects) == 0 {
		return s.exec(op, sql, args...)
	}
	ctx := context.Background()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, p := range projects {
			doc, err := json.Marshal(p)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO projects (name, doc) VALUES ($1,$2)`, p.Name, doc); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, sql, args...)
		return err
	})
	if err != nil {
		return fmt.Errorf("pgstore: %s: %w", op, err)
	}
	return nil
}

// createdProjects is the 0-or-1 project list a requireProject result asks the
// caller to persist.
func createdProjects(p Project, created bool) []Project {
	if created {
		return []Project{p}
	}
	return nil
}

// ImportConfig restores a config snapshot into an empty Postgres store in a
// single transaction: either every aggregate row is inserted or none is. It is
// fail-closed (checkImport) and never touches instances or unread cursors.
func (s *PostgresStore) ImportConfig(snap ConfigSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := checkImport(s.memState, snap); err != nil {
		return err
	}
	ctx := context.Background()
	snap = withReferencedProjects(snap)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Projects first: roles.project references projects.name.
		for _, p := range snap.Projects {
			doc, err := json.Marshal(p)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO projects (name, doc) VALUES ($1,$2)`, p.Name, doc); err != nil {
				return err
			}
		}
		for _, a := range snap.Actors {
			doc, err := json.Marshal(a)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO actors (token_hash, id, doc) VALUES ($1,$2,$3)`, a.TokenHash, a.ID, doc); err != nil {
				return err
			}
		}
		for project, rs := range snap.Roles {
			for _, r := range rs {
				doc, err := json.Marshal(r)
				if err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO roles (project, name, doc) VALUES ($1,$2,$3)`, project, r.Name, doc); err != nil {
					return err
				}
			}
		}
		for _, k := range snap.Kits {
			doc, err := json.Marshal(k)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO kits (name, doc) VALUES ($1,$2)`, k.Name, doc); err != nil {
				return err
			}
		}
		for _, d := range snap.Destinations {
			doc, err := json.Marshal(d)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO destinations (name, doc) VALUES ($1,$2)`, d.Name, doc); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("pgstore: ImportConfig: %w", err)
	}
	applyImport(s.memState, snap)
	return nil
}

// ---- mutators: Lock; validate/compute (from memState); write; apply cache ----

func (s *PostgresStore) AddActor(a Actor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.actorIDExists(a.ID) {
		return fmt.Errorf("actor %q already exists", a.ID)
	}
	created, err := s.grantProjects(a)
	if err != nil {
		return err
	}
	doc, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if err := s.execWithProjects("AddActor", created, `INSERT INTO actors (token_hash, id, doc) VALUES ($1,$2,$3)`, a.TokenHash, a.ID, doc); err != nil {
		return err
	}
	for _, p := range created {
		s.applyPutProject(p)
	}
	s.applyPutActor(a)
	return nil
}

func (s *PostgresStore) RemoveActor(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, _, ok := s.actorByID(id)
	if !ok {
		return actorNotFoundErr(id)
	}
	if err := s.exec("RemoveActor", `DELETE FROM actors WHERE token_hash = $1`, h); err != nil {
		return err
	}
	s.applyRemoveActorByID(id)
	return nil
}

func (s *PostgresStore) AddGrant(actorID string, g Grant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, a, ok := s.actorByID(actorID)
	if !ok {
		return actorNotFoundErr(actorID)
	}
	p, created, err := s.requireProject(g.Project)
	if err != nil {
		return err
	}
	updated := upsertGrant(a, g)
	if err := s.putActorDoc(h, updated, createdProjects(p, created)...); err != nil {
		return err
	}
	if created {
		s.applyPutProject(p)
	}
	s.applyPutActor(updated)
	return nil
}

func (s *PostgresStore) RemoveGrant(actorID, project, role string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if project == "" {
		project = DefaultProject
	}
	h, a, ok := s.actorByID(actorID)
	if !ok {
		return actorNotFoundErr(actorID)
	}
	updated, found := removeGrantFrom(a, project, role)
	if !found {
		return fmt.Errorf("actor %q has no grant %s/%s", actorID, project, role)
	}
	if err := s.putActorDoc(h, updated); err != nil {
		return err
	}
	s.applyPutActor(updated)
	return nil
}

// putActorDoc writes the actor's row (grants live in the doc); the token_hash
// key is immutable, so this is always an UPDATE of the existing row. Any
// projects given are inserted in the same transaction.
func (s *PostgresStore) putActorDoc(tokenHash string, a Actor, projects ...Project) error {
	doc, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return s.execWithProjects("putActor", projects, `UPDATE actors SET doc = $2, version = version + 1, updated_at = now() WHERE token_hash = $1`, tokenHash, doc)
}

func (s *PostgresStore) PutRole(project string, r Role) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Kit != "" && !s.kitExists(r.Kit) {
		return fmt.Errorf("kit %q not found", r.Kit)
	}
	if project == "" {
		project = DefaultProject
	}
	p, created, err := s.requireProject(project)
	if err != nil {
		return err
	}
	doc, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := s.execWithProjects("PutRole", createdProjects(p, created),
		`INSERT INTO roles (project, name, doc) VALUES ($1,$2,$3)
		 ON CONFLICT (project, name) DO UPDATE SET doc = EXCLUDED.doc, version = roles.version + 1, updated_at = now()`,
		project, r.Name, doc); err != nil {
		return err
	}
	if created {
		s.applyPutProject(p)
	}
	s.applyPutRole(project, r)
	return nil
}

func (s *PostgresStore) CreateProject(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCreateProject(name); err != nil {
		return err
	}
	p := Project{Name: name}
	doc, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := s.exec("CreateProject", `INSERT INTO projects (name, doc) VALUES ($1,$2)`, name, doc); err != nil {
		return err
	}
	s.applyPutProject(p)
	return nil
}

func (s *PostgresStore) RemoveProject(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkRemoveProject(name); err != nil {
		return err
	}
	if err := s.exec("RemoveProject", `DELETE FROM projects WHERE name = $1`, name); err != nil {
		return err
	}
	s.applyRemoveProject(name)
	return nil
}

func (s *PostgresStore) RemoveRole(project, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if project == "" {
		project = DefaultProject
	}
	if _, ok := s.roles[project][name]; !ok {
		return fmt.Errorf("role %q not found in project %q", name, project)
	}
	if err := s.exec("RemoveRole", `DELETE FROM roles WHERE project = $1 AND name = $2`, project, name); err != nil {
		return err
	}
	s.applyRemoveRole(project, name)
	return nil
}

func (s *PostgresStore) PushKit(name, config string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == "" || config == "" {
		return 0, fmt.Errorf("kit name and config are required")
	}
	k, ok := s.kits[name]
	if !ok {
		k = Kit{Name: name, Versions: map[int]string{}}
	} else {
		k = copyKit(k) // don't mutate the cached kit before commit
	}
	ver := nextKitVersion(k)
	k.Versions[ver] = config
	k.Current = ver
	doc, err := json.Marshal(k)
	if err != nil {
		return 0, err
	}
	if err := s.exec("PushKit",
		`INSERT INTO kits (name, doc) VALUES ($1,$2)
		 ON CONFLICT (name) DO UPDATE SET doc = EXCLUDED.doc, version = kits.version + 1, updated_at = now()`,
		name, doc); err != nil {
		return 0, err
	}
	s.kits[name] = k
	return ver, nil
}

func (s *PostgresStore) PinKit(name string, version int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.kits[name]
	if !ok {
		return fmt.Errorf("kit %q not found", name)
	}
	if _, ok := k.Versions[version]; !ok {
		return fmt.Errorf("kit %q has no version %d", name, version)
	}
	k = copyKit(k)
	k.Current = version
	doc, err := json.Marshal(k)
	if err != nil {
		return err
	}
	if err := s.exec("PinKit", `UPDATE kits SET doc = $2, version = version + 1, updated_at = now() WHERE name = $1`, name, doc); err != nil {
		return err
	}
	s.applyPinKit(name, version)
	return nil
}

func (s *PostgresStore) RemoveKit(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.kitExists(name) {
		return fmt.Errorf("kit %q not found", name)
	}
	if err := s.exec("RemoveKit", `DELETE FROM kits WHERE name = $1`, name); err != nil {
		return err
	}
	s.applyRemoveKit(name)
	return nil
}

func (s *PostgresStore) PutInstance(i Instance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i.ActorID == "" {
		return fmt.Errorf("instance actor id is required")
	}
	doc, err := json.Marshal(i)
	if err != nil {
		return err
	}
	if err := s.exec("PutInstance",
		`INSERT INTO instances (actor_id, doc) VALUES ($1,$2)
		 ON CONFLICT (actor_id) DO UPDATE SET doc = EXCLUDED.doc, version = instances.version + 1, updated_at = now()`,
		i.ActorID, doc); err != nil {
		return err
	}
	s.applyPutInstance(i)
	return nil
}

func (s *PostgresStore) RemoveInstance(actorID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.instances[actorID]; !ok {
		return fmt.Errorf("instance %q not found", actorID)
	}
	if err := s.exec("RemoveInstance", `DELETE FROM instances WHERE actor_id = $1`, actorID); err != nil {
		return err
	}
	s.applyRemoveInstance(actorID)
	return nil
}

func (s *PostgresStore) AdvanceCommitCursor(actorID, upToID string, upToSeq int64) (Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.instances[actorID]; !ok {
		return Instance{}, fmt.Errorf("instance %q not found", actorID)
	}
	// Advance only when forward on Seq (the ordering key, numeric — no locale
	// collation concern); jsonb_set both fields (the id echo travels with its
	// Seq). No-op UPDATE (0 rows) when upToSeq <= current CommitSeq is fine —
	// the cache helper below is the source of truth for the returned value and
	// also no-ops on a non-forward upToSeq.
	if err := s.exec("AdvanceCommitCursor",
		`UPDATE instances SET doc = jsonb_set(jsonb_set(doc, '{commit_cursor}', to_jsonb($2::text)), '{commit_seq}', to_jsonb($3::bigint)), version = version + 1, updated_at = now()
		 WHERE actor_id = $1 AND coalesce((doc->>'commit_seq')::bigint, 0) < $3`,
		actorID, upToID, upToSeq); err != nil {
		return Instance{}, err
	}
	i, _ := s.applyAdvanceCommitCursor(actorID, upToID, upToSeq)
	return i, nil
}

func (s *PostgresStore) CommitUnread(participant, channel string, seq int64) error {
	if participant == "" || channel == "" {
		return fmt.Errorf("participant and channel are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Forward-only in the DB (GREATEST), matching the cache helper below. A
	// backward/equal seq is a harmless no-op update (the row keeps its value).
	if err := s.exec("CommitUnread",
		`INSERT INTO intercom_unread_cursors (participant, channel, seq) VALUES ($1,$2,$3)
		 ON CONFLICT (participant, channel) DO UPDATE
		   SET seq = GREATEST(intercom_unread_cursors.seq, EXCLUDED.seq), updated_at = now()`,
		participant, channel, seq); err != nil {
		return err
	}
	s.applyCommitUnread(participant, channel, seq)
	return nil
}

func (s *PostgresStore) AddDestination(d Destination) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if err := s.exec("AddDestination",
		`INSERT INTO destinations (name, doc) VALUES ($1,$2)
		 ON CONFLICT (name) DO UPDATE SET doc = EXCLUDED.doc, version = destinations.version + 1, updated_at = now()`,
		d.Name, doc); err != nil {
		return err
	}
	s.applyPutDestination(d)
	return nil
}

func (s *PostgresStore) RemoveDestination(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.dests[name]; !ok {
		return fmt.Errorf("destination %q not found", name)
	}
	if err := s.exec("RemoveDestination", `DELETE FROM destinations WHERE name = $1`, name); err != nil {
		return err
	}
	s.applyRemoveDestination(name)
	return nil
}

func (s *PostgresStore) AddHuman(project string, h Human) error {
	if h.Name == "" {
		return fmt.Errorf("human name required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, _, err := s.requireProject(project)
	if err != nil {
		return err
	}
	return s.putProject(upsertHuman(copyProject(p), h))
}

func (s *PostgresStore) AddChannel(project string, c Channel) error {
	if c.Name == "" {
		return fmt.Errorf("channel name required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, _, err := s.requireProject(project)
	if err != nil {
		return err
	}
	return s.putProject(upsertChannel(copyProject(p), c))
}

func (s *PostgresStore) RemoveHuman(project, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[project]
	if !ok {
		return fmt.Errorf("project %q not found", project)
	}
	return s.putProject(removeHumanFrom(copyProject(p), name))
}

func (s *PostgresStore) RemoveChannel(project, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[project]
	if !ok {
		return fmt.Errorf("project %q not found", project)
	}
	return s.putProject(removeChannelFrom(copyProject(p), name))
}

func (s *PostgresStore) SetEscalationPolicy(project, category string, tiers []EscalationTier) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, _, err := s.requireProject(project)
	if err != nil {
		return err
	}
	return s.putProject(setEscalation(copyProject(p), category, tiers))
}

func (s *PostgresStore) SetChatService(project, service string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, _, err := s.requireProject(project)
	if err != nil {
		return err
	}
	return s.putProject(setChatService(copyProject(p), service))
}

// putProject upserts a project's row and, on success, updates the cache.
func (s *PostgresStore) putProject(p Project) error {
	doc, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := s.exec("putProject",
		`INSERT INTO projects (name, doc) VALUES ($1,$2)
		 ON CONFLICT (name) DO UPDATE SET doc = EXCLUDED.doc, version = projects.version + 1, updated_at = now()`,
		p.Name, doc); err != nil {
		return err
	}
	s.applyPutProject(p)
	return nil
}

// discard is an io.Writer that drops everything, for the nil-logger default.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
