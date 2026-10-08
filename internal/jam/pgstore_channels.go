package jam

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/jackc/pgx/v5"
)

// The channel registry on Postgres (channel_registry.go): the same
// prepare → one transaction → apply split as the rest of the registry.

func (s *PostgresStore) CreateChannel(c Channel) (Channel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.prepareCreateChannel(c)
	if err != nil {
		return Channel{}, err
	}
	if err := s.registryTx("CreateChannel", func(ctx context.Context, tx pgx.Tx) error {
		return putChannelTx(ctx, tx, c)
	}); err != nil {
		return Channel{}, err
	}
	s.applyPutChannel(c)
	return copyChannel(c), nil
}

func (s *PostgresStore) RenameChannel(id ident.ID, key, label string) error {
	return s.putChannelWith("RenameChannel", func() (Channel, error) { return s.prepareRenameChannel(id, key, label) })
}

func (s *PostgresStore) SetChannelBindings(id ident.ID, bs []Binding) error {
	return s.putChannelWith("SetChannelBindings", func() (Channel, error) { return s.prepareSetChannelBindings(id, bs) })
}

func (s *PostgresStore) ArchiveChannel(id ident.ID) error {
	return s.putChannelWith("ArchiveChannel", func() (Channel, error) { return s.prepareArchiveChannel(id) })
}

func (s *PostgresStore) ReopenChannel(id ident.ID) error {
	return s.putChannelWith("ReopenChannel", func() (Channel, error) { return s.prepareReopenChannel(id) })
}

func (s *PostgresStore) putChannelWith(op string, prepare func() (Channel, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := prepare()
	if err != nil {
		return err
	}
	if err := s.registryTx(op, func(ctx context.Context, tx pgx.Tx) error {
		return putChannelTx(ctx, tx, c)
	}); err != nil {
		return err
	}
	s.applyPutChannel(c)
	return nil
}

func (s *PostgresStore) JoinChannel(ch, p ident.ID, seq int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	join, err := s.prepareJoinChannel(ch, p)
	if err != nil || !join {
		return err
	}
	if err := s.registryTx("JoinChannel", func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO participants (id, kind) VALUES ($1,$2) ON CONFLICT (id) DO NOTHING`, p, participantKind(p)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO channel_members (channel_id, participant_id, joined_seq) VALUES ($1,$2,$3)
			 ON CONFLICT (channel_id, participant_id, joined_seq) DO UPDATE SET left_seq = NULL`,
			ch, p, seq)
		return err
	}); err != nil {
		return err
	}
	s.applyJoinChannel(ch, p, seq)
	return nil
}

func (s *PostgresStore) LeaveChannel(ch, p ident.ID, seq int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	leave, err := s.prepareLeaveChannel(ch, p)
	if err != nil || !leave {
		return err
	}
	if err := s.registryTx("LeaveChannel", func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE channel_members SET left_seq = GREATEST($3, joined_seq)
			 WHERE channel_id = $1 AND participant_id = $2 AND left_seq IS NULL`,
			ch, p, seq)
		return err
	}); err != nil {
		return err
	}
	s.applyLeaveChannel(ch, p, seq)
	return nil
}

func (s *PostgresStore) CommitChannelRead(p, ch ident.ID, seq int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.exec("CommitChannelRead",
		`INSERT INTO channel_reads (participant_id, channel_id, seq) VALUES ($1,$2,$3)
		 ON CONFLICT (participant_id, channel_id) DO UPDATE SET seq = GREATEST(channel_reads.seq, EXCLUDED.seq), updated_at = now()`,
		p, ch, seq); err != nil {
		return err
	}
	s.applyChannelRead(p, ch, seq)
	return nil
}

// participantKind is the participants.kind of a channel member: its id's
// kind, or a session for a grandfathered (pre-registry) session id — the only
// participants whose ids are not surrogate ids.
func participantKind(p ident.ID) string {
	if _, err := ident.Parse(string(p)); err == nil {
		return string(p.Kind())
	}
	return string(ident.Session)
}

// putChannelTx upserts c and replaces its bindings.
func putChannelTx(ctx context.Context, tx pgx.Tx, c Channel) error {
	if err := insertParticipantTx(ctx, tx, c.ID); err != nil {
		return err
	}
	doc, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO channels (id, project_id, kind, key, status, doc) VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (id) DO UPDATE SET key = EXCLUDED.key, status = EXCLUDED.status, doc = EXCLUDED.doc, updated_at = now()`,
		c.ID, c.ProjectID, string(c.Kind), c.Key, string(c.Status), doc); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM channel_bindings WHERE channel_id = $1`, c.ID); err != nil {
		return err
	}
	for _, b := range c.Bindings {
		if _, err := tx.Exec(ctx,
			`INSERT INTO channel_bindings (channel_id, connection_id, ref, mode, live) VALUES ($1,$2,$3,$4,$5)`,
			c.ID, b.ConnectionID, b.Ref, string(b.Mode), c.Status == StatusLive); err != nil {
			return err
		}
	}
	return nil
}

// loadChannels fills the channel registry and its memberships. load's part;
// no lock.
func (s *PostgresStore) loadChannels(ctx context.Context) error {
	if err := s.loadDocs(ctx, "channels", func(doc []byte) error {
		var c Channel
		if err := json.Unmarshal(doc, &c); err != nil {
			return err
		}
		s.channels[c.ID] = c
		return nil
	}); err != nil {
		return err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT channel_id, participant_id, joined_seq, left_seq FROM channel_members ORDER BY channel_id, joined_seq`)
	if err != nil {
		return fmt.Errorf("pgstore: load channel_members: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ch string
		var ms ChannelMember
		var left *int64
		if err := rows.Scan(&ch, &ms.ParticipantID, &ms.JoinedSeq, &left); err != nil {
			return err
		}
		if left != nil {
			ms.Left, ms.LeftSeq = true, *left
		}
		s.chanMembers[ident.ID(ch)] = append(s.chanMembers[ident.ID(ch)], ms)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return s.loadChannelReads(ctx)
}

// loadChannelReads fills the /me read cursors. load's part; no lock.
func (s *PostgresStore) loadChannelReads(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT participant_id, channel_id, seq FROM channel_reads`)
	if err != nil {
		return fmt.Errorf("pgstore: load channel_reads: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p, ch string
		var seq int64
		if err := rows.Scan(&p, &ch, &seq); err != nil {
			return err
		}
		s.applyChannelRead(ident.ID(p), ident.ID(ch), seq)
	}
	return rows.Err()
}
