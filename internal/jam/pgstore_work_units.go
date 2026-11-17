package jam

import (
	"context"
	"fmt"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/jackc/pgx/v5"
)

// The units of work's records on Postgres (work_units.go): prepare → one
// transaction that rewrites the unit's rows → apply.

func (s *PostgresStore) StartUnitWorker(log ident.ID, w UnitWorker, issue UnitIssue) error {
	return s.putWorkUnitWith("StartUnitWorker", func() (WorkUnit, bool, error) { return s.prepareStartUnitWorker(log, w, issue) })
}

func (s *PostgresStore) StopUnitWorker(log, session ident.ID, at time.Time) error {
	return s.putWorkUnitWith("StopUnitWorker", func() (WorkUnit, bool, error) { return s.prepareStopUnitWorker(log, session, at) })
}

func (s *PostgresStore) ReportUnit(log ident.ID, state, pr string, at time.Time) error {
	return s.putWorkUnitWith("ReportUnit", func() (WorkUnit, bool, error) {
		u, err := s.prepareReportUnit(log, state, pr, at)
		return u, err == nil, err
	})
}

func (s *PostgresStore) SetUnitPR(log ident.ID, url string, st PRStatus) error {
	return s.putWorkUnitWith("SetUnitPR", func() (WorkUnit, bool, error) { return s.prepareSetUnitPR(log, url, st) })
}

func (s *PostgresStore) putWorkUnitWith(op string, prepare func() (WorkUnit, bool, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, changed, err := prepare()
	if err != nil || !changed {
		return err
	}
	if err := s.registryTx(op, func(ctx context.Context, tx pgx.Tx) error { return putWorkUnitTx(ctx, tx, u) }); err != nil {
		return err
	}
	s.applyPutWorkUnit(u)
	return nil
}

// putWorkUnitTx writes u whole: its row, then its workers and PRs replaced.
func putWorkUnitTx(ctx context.Context, tx pgx.Tx, u WorkUnit) error {
	if _, err := tx.Exec(ctx, `INSERT INTO work_units (channel_id, title, url, state, state_at) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (channel_id) DO UPDATE SET title = EXCLUDED.title, url = EXCLUDED.url, state = EXCLUDED.state, state_at = EXCLUDED.state_at`,
		u.Log, u.Issue.Title, u.Issue.URL, u.State, nullTime(u.StateAt)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM work_unit_workers WHERE channel_id = $1`, u.Log); err != nil {
		return err
	}
	for i, w := range u.Workers {
		if _, err := tx.Exec(ctx, `INSERT INTO work_unit_workers (channel_id, ord, session_id, label, started_at, stopped, stopped_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, u.Log, i, w.Session, w.Label, nullTime(w.Started), w.Stopped, nullTime(w.StoppedAt)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM work_unit_prs WHERE channel_id = $1`, u.Log); err != nil {
		return err
	}
	for i, p := range u.PRs {
		if _, err := tx.Exec(ctx, `INSERT INTO work_unit_prs (channel_id, ord, url, number, title, state, reported_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, u.Log, i, p.URL, p.Number, p.Title, p.State, p.ReportedAt); err != nil {
			return err
		}
	}
	return nil
}

// nullTime is t for a nullable timestamptz: NULL when zero.
func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// utcOf is a loaded nullable timestamptz: zero for NULL, else in UTC.
func utcOf(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

// loadWorkUnits fills the units of work's records. load's part; no lock.
func (s *PostgresStore) loadWorkUnits(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT channel_id, title, url, state, state_at FROM work_units`)
	if err != nil {
		return fmt.Errorf("pgstore: load work_units: %w", err)
	}
	for rows.Next() {
		var u WorkUnit
		var at *time.Time
		if err := rows.Scan(&u.Log, &u.Issue.Title, &u.Issue.URL, &u.State, &at); err != nil {
			rows.Close()
			return err
		}
		u.StateAt = utcOf(at)
		s.units[u.Log] = u
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = s.pool.Query(ctx, `SELECT channel_id, session_id, label, started_at, stopped, stopped_at FROM work_unit_workers ORDER BY channel_id, ord`)
	if err != nil {
		return fmt.Errorf("pgstore: load work_unit_workers: %w", err)
	}
	for rows.Next() {
		var ch ident.ID
		var w UnitWorker
		var started, stopped *time.Time
		if err := rows.Scan(&ch, &w.Session, &w.Label, &started, &w.Stopped, &stopped); err != nil {
			rows.Close()
			return err
		}
		w.Started, w.StoppedAt = utcOf(started), utcOf(stopped)
		u := s.units[ch]
		u.Workers = append(u.Workers, w)
		s.units[ch] = u
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = s.pool.Query(ctx, `SELECT channel_id, url, number, title, state, reported_at FROM work_unit_prs ORDER BY channel_id, ord`)
	if err != nil {
		return fmt.Errorf("pgstore: load work_unit_prs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ch ident.ID
		var p UnitPR
		if err := rows.Scan(&ch, &p.URL, &p.Number, &p.Title, &p.State, &p.ReportedAt); err != nil {
			return err
		}
		p.ReportedAt = p.ReportedAt.UTC()
		u := s.units[ch]
		u.PRs = append(u.PRs, p)
		s.units[ch] = u
	}
	return rows.Err()
}
