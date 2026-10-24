package jam

import (
	"cmp"
	"fmt"
	"slices"
	"time"
)

// Setup snapshot parts: what Jam delivers to a studio and records, masked.
const (
	SetupPartConnector = "connector" // GET /connector (and the raise)
	SetupPartContext   = "context"   // GET /context (and the raise)
	SetupPartEgress    = "egress"    // a successful ApplyEgress (and the raise)
	SetupPartKit       = "kit"       // the raise: kit ref, chain, image tag
)

var setupParts = []string{SetupPartConnector, SetupPartContext, SetupPartEgress, SetupPartKit}

// MaxSetupBody caps a snapshot body (RecordSetupPart truncates to it).
const MaxSetupBody = 64 << 10

// SetupSnapshot is the latest delivery of one part to one studio: when, its
// fingerprint, and the already-masked rendered body. Runtime state: never
// exported, dropped with the instance.
type SetupSnapshot struct {
	ActorID     string    `json:"actor_id"`
	Part        string    `json:"part"`
	At          time.Time `json:"at"`
	Fingerprint string    `json:"fingerprint"`
	Body        string    `json:"body"`
}

// SetupSnapshotStore is the Store's per-instance setup snapshots.
type SetupSnapshotStore interface {
	// PutSetupSnapshot upserts by (ActorID, Part). ErrNotFound when the
	// instance does not exist; an unknown part or an over-cap body is refused.
	PutSetupSnapshot(s SetupSnapshot) error
	// SetupSnapshots lists an instance's snapshots, sorted by part.
	SetupSnapshots(actorID string) []SetupSnapshot
}

// prepareSetupSnapshot validates s. Caller holds mu.
func (m *memState) prepareSetupSnapshot(s SetupSnapshot) error {
	if !slices.Contains(setupParts, s.Part) {
		return fmt.Errorf("setup snapshot: unknown part %q", s.Part)
	}
	if _, ok := m.instances[s.ActorID]; !ok {
		return fmt.Errorf("setup snapshot for instance %q: %w", s.ActorID, ErrNotFound)
	}
	if len(s.Body) > MaxSetupBody {
		return fmt.Errorf("setup snapshot: body is %d bytes (max %d)", len(s.Body), MaxSetupBody)
	}
	return nil
}

func (m *memState) applyPutSetupSnapshot(s SetupSnapshot) {
	if m.setup[s.ActorID] == nil {
		m.setup[s.ActorID] = map[string]SetupSnapshot{}
	}
	m.setup[s.ActorID][s.Part] = s
}

func (m *memState) SetupSnapshots(actorID string) []SetupSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []SetupSnapshot
	for _, s := range m.setup[actorID] {
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b SetupSnapshot) int { return cmp.Compare(a.Part, b.Part) })
	return out
}

func (fs *MemStore) PutSetupSnapshot(s SetupSnapshot) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if err := fs.prepareSetupSnapshot(s); err != nil {
		return err
	}
	fs.applyPutSetupSnapshot(s)
	return nil
}
