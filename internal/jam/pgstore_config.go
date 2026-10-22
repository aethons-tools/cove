package jam

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// ApplyConfig writes a config apply's whole set in one transaction; the cache
// changes only after commit.
func (s *PostgresStore) ApplyConfig(w ConfigWrite) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := checkConfigWrite(s.memState, w); err != nil {
		return err
	}
	if w.Empty() {
		return nil
	}
	w = prepareConfigWrite(s.memState, w)
	if err := s.registryTx("ApplyConfig", func(ctx context.Context, tx pgx.Tx) error {
		return writeConfigTx(ctx, tx, w)
	}); err != nil {
		return err
	}
	applyConfigWrite(s.memState, w)
	return nil
}

// writeConfigTx issues w's statements in apply order.
func writeConfigTx(ctx context.Context, tx pgx.Tx, w ConfigWrite) error {
	for _, c := range w.Connections {
		if err := putConnectionTx(ctx, tx, c); err != nil {
			return err
		}
	}
	for _, u := range w.Users {
		if err := putUserTx(ctx, tx, u, nil); err != nil {
			return err
		}
	}
	for _, a := range w.Accounts {
		if err := putAccountTx(ctx, tx, a); err != nil {
			return err
		}
	}
	for _, d := range w.Destinations {
		doc, err := json.Marshal(d)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO destinations (name, doc) VALUES ($1,$2)
			 ON CONFLICT (name) DO UPDATE SET doc = EXCLUDED.doc, version = destinations.version + 1, updated_at = now()`,
			d.Name, doc); err != nil {
			return err
		}
	}
	for _, n := range w.RemoveDestinations {
		if _, err := tx.Exec(ctx, `DELETE FROM destinations WHERE name = $1`, n); err != nil {
			return err
		}
	}
	for _, ms := range w.ModelSpecs {
		c := ms // prepared by prepareConfigWrite
		doc, err := json.Marshal(c)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO model_specs (name, doc) VALUES ($1,$2)
			 ON CONFLICT (name) DO UPDATE SET doc = EXCLUDED.doc, version = model_specs.version + 1, updated_at = now()`,
			c.Name, doc); err != nil {
			return err
		}
	}
	for _, n := range w.RemoveModelSpecs {
		if _, err := tx.Exec(ctx, `DELETE FROM model_specs WHERE name = $1`, n); err != nil {
			return err
		}
	}
	for _, k := range w.Kits {
		doc, err := json.Marshal(copyKit(k))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO kits (name, doc) VALUES ($1,$2)
			 ON CONFLICT (name) DO UPDATE SET doc = EXCLUDED.doc, version = kits.version + 1, updated_at = now()`,
			k.Name, doc); err != nil {
			return err
		}
	}
	for _, n := range w.RemoveKits {
		if _, err := tx.Exec(ctx, `DELETE FROM kits WHERE name = $1`, n); err != nil {
			return err
		}
	}
	for _, p := range w.Projects {
		if err := upsertProjectTx(ctx, tx, p); err != nil {
			return err
		}
	}
	for _, ms := range w.RemoveMemberships {
		if _, err := tx.Exec(ctx, `DELETE FROM memberships WHERE project_id = $1 AND user_id = $2`, ms.ProjectID, ms.UserID); err != nil {
			return err
		}
	}
	for _, ms := range w.Memberships {
		if err := putMembershipTx(ctx, tx, ms); err != nil {
			return err
		}
	}
	for _, c := range w.Channels {
		if err := putChannelTx(ctx, tx, c); err != nil {
			return err
		}
	}
	for _, r := range w.RemoveRoles {
		if _, err := tx.Exec(ctx, `DELETE FROM roles WHERE project_id = $1 AND name = $2`, r.ProjectID, r.Role.Name); err != nil {
			return err
		}
	}
	for _, r := range w.Roles {
		doc, err := json.Marshal(r.Role)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO roles (project_id, name, doc) VALUES ($1,$2,$3)
			 ON CONFLICT (project_id, name) DO UPDATE SET doc = EXCLUDED.doc, version = roles.version + 1, updated_at = now()`,
			r.ProjectID, r.Role.Name, doc); err != nil {
			return err
		}
	}
	if w.JamContext != nil {
		if w.JamContext.Empty() {
			if _, err := tx.Exec(ctx, `DELETE FROM jam_settings WHERE key = 'context'`); err != nil {
				return err
			}
		} else {
			doc, err := json.Marshal(*w.JamContext)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO jam_settings (key, doc) VALUES ('context', $1)
				 ON CONFLICT (key) DO UPDATE SET doc = EXCLUDED.doc, updated_at = now()`, doc); err != nil {
				return err
			}
		}
	}
	for _, mk := range w.Marks {
		doc, err := json.Marshal(mk)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO config_marks (kind, name, doc) VALUES ($1,$2,$3)
			 ON CONFLICT (kind, name) DO UPDATE SET doc = EXCLUDED.doc, updated_at = now()`,
			mk.Kind, mk.Name, doc); err != nil {
			return err
		}
	}
	for _, k := range w.RemoveMarks {
		if _, err := tx.Exec(ctx, `DELETE FROM config_marks WHERE kind = $1 AND name = $2`, k.Kind, k.Name); err != nil {
			return err
		}
	}
	return nil
}
