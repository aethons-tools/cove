package jam

// sessionRecord is the part of a session's Instance that is its record in
// Jam, kept when its studio is replaced under the same id: its inbox cursor,
// focus, ticket report, alarms and escalation category. Recovery's re-raise
// restores it, and so does the re-raise of a standing session torn down by a
// restart, an upgrade or a hand teardown (docs/modules/ROOT/pages/usage/jam/standing-state.adoc#session-record).
type sessionRecord struct {
	commitSeq          int64
	commitCursor       string
	focus              string
	report             *TicketReport
	alarms             []Alarm
	escalationCategory string
}

// recordOf is inst's record.
func recordOf(inst Instance) sessionRecord {
	return sessionRecord{
		commitSeq: inst.CommitSeq, commitCursor: inst.CommitCursor,
		focus: inst.Focus, report: inst.Report, alarms: inst.Alarms, escalationCategory: inst.EscalationCategory,
	}
}

// restore writes r onto i.
func (r sessionRecord) restore(i *Instance) {
	i.CommitSeq, i.CommitCursor = r.commitSeq, r.commitCursor
	i.Focus, i.Report, i.Alarms, i.EscalationCategory = r.focus, r.report, r.alarms, r.escalationCategory
}

// keepRecord keeps the record of actorID's instance, when it is a standing
// session's, for its next raise (Teardown, right before removing it).
func (s *Supervisor) keepRecord(actorID string) {
	inst, ok := s.store.GetInstance(actorID)
	if !ok || inst.SessionKind != SessionKindStanding {
		return
	}
	s.recordsMu.Lock()
	defer s.recordsMu.Unlock()
	if s.keptRecords == nil {
		s.keptRecords = map[string]sessionRecord{}
	}
	s.keptRecords[actorID] = recordOf(inst)
}

// keptRecord is the record kept for spec's session: only a standing raise
// that isn't a recovery's (which restores its own) takes one.
func (s *Supervisor) keptRecord(spec RaiseSpec) (sessionRecord, bool) {
	if spec.SessionKind != SessionKindStanding || spec.recovering {
		return sessionRecord{}, false
	}
	s.recordsMu.Lock()
	defer s.recordsMu.Unlock()
	r, ok := s.keptRecords[spec.ActorID]
	return r, ok
}

// ForgetRecord drops the record kept for actorID, so its next raise starts
// empty: the standing reconciler's reset and dismissal, and a raise that
// restored it.
func (s *Supervisor) ForgetRecord(actorID string) {
	s.recordsMu.Lock()
	defer s.recordsMu.Unlock()
	delete(s.keptRecords, actorID)
}
