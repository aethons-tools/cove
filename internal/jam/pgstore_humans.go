package jam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// rosterSchemaVersion marks, in jam_settings 'roster_schema', that the stored
// project docs' roster humans have been migrated into the registry.
const rosterSchemaVersion = 1

// errHumansMigratedElsewhere: another Jam process ran the humans migration
// between this load and its migration transaction.
var errHumansMigratedElsewhere = errors.New("pgstore: humans migrated by another process")

// migrateHumans runs the one-time humans → users migration at load (spec §6,
// plan 1a-3a): plan from the loaded cache, write in one transaction under the
// migration advisory lock (re-checking the marker there), then apply. load's
// part; no lock.
func (s *PostgresStore) migrateHumans(ctx context.Context) error {
	done, err := rosterSchemaDone(ctx, s.pool)
	if err != nil || done {
		return err
	}
	plan := s.planHumanMigration()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(migrateAdvisoryLock)); err != nil {
			return err
		}
		if done, err := rosterSchemaDone(ctx, tx); err != nil {
			return err
		} else if done {
			return errHumansMigratedElsewhere
		}
		if err := writeHumanPlanTx(ctx, tx, plan); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO jam_settings (key, doc) VALUES ('roster_schema', $1)
			 ON CONFLICT (key) DO UPDATE SET doc = EXCLUDED.doc`,
			fmt.Sprint(rosterSchemaVersion))
		return err
	})
	if errors.Is(err, errHumansMigratedElsewhere) {
		return err
	}
	if err != nil {
		return fmt.Errorf("pgstore: migrate roster humans: %w", err)
	}
	s.applyHumanPlan(plan)
	r := plan.report
	if r.Users+r.Memberships+r.Accounts > 0 || len(r.Notes) > 0 {
		s.log.Info("pgstore: migrated roster humans to users", "users", r.Users, "memberships", r.Memberships, "accounts", r.Accounts, "notes", r.Notes)
	}
	return nil
}

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func rosterSchemaDone(ctx context.Context, q queryRower) (bool, error) {
	var v int
	switch err := q.QueryRow(ctx, `SELECT (doc #>> '{}')::int FROM jam_settings WHERE key = 'roster_schema'`).Scan(&v); {
	case errors.Is(err, pgx.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("pgstore: read roster_schema: %w", err)
	}
	return v >= rosterSchemaVersion, nil
}

// writeHumanPlanTx writes a humans plan: registry rows, aliases, and the
// project, role, actor and instance docs it rewrote.
func writeHumanPlanTx(ctx context.Context, tx pgx.Tx, p humanPlan) error {
	for _, c := range p.connections {
		if err := putConnectionTx(ctx, tx, c); err != nil {
			return err
		}
	}
	for _, u := range p.users {
		if err := putUserTx(ctx, tx, u, nil); err != nil {
			return err
		}
	}
	for _, pr := range p.projects {
		if err := upsertProjectTx(ctx, tx, pr); err != nil {
			return err
		}
	}
	for _, a := range p.accounts {
		if err := putAccountTx(ctx, tx, a); err != nil {
			return err
		}
	}
	for _, ms := range p.memberships {
		if err := putMembershipTx(ctx, tx, ms); err != nil {
			return err
		}
	}
	for _, al := range p.aliases {
		if _, err := tx.Exec(ctx,
			`INSERT INTO legacy_human_aliases (project_name, human_name, user_id) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`,
			al.Project, al.Name, al.User); err != nil {
			return err
		}
	}
	for _, rw := range p.roles {
		doc, err := json.Marshal(rw.Role)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE roles SET doc = $3, version = version + 1, updated_at = now() WHERE project = $1 AND name = $2`,
			rw.Project, rw.Role.Name, doc); err != nil {
			return err
		}
	}
	for _, a := range p.actors {
		doc, err := json.Marshal(a)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE actors SET doc = $2, version = version + 1, updated_at = now() WHERE token_hash = $1`, a.TokenHash, doc); err != nil {
			return err
		}
	}
	for _, inst := range p.instances {
		doc, err := json.Marshal(inst)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE instances SET doc = $2, version = version + 1, updated_at = now() WHERE actor_id = $1`, inst.ActorID, doc); err != nil {
			return err
		}
	}
	return nil
}
