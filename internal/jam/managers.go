package jam

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

// ErrManagerExists reports that a manager name is already registered.
var ErrManagerExists = errors.New("manager already registered")

// LocalManager is the reserved name of Jam's in-process manager
// (fleet.LocalName); package jam cannot import fleet.
const LocalManager = "local"

var managerNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidManagerName reports whether name may be registered as a Studio
// Manager. "local" is reserved for Jam's own manager.
func ValidManagerName(name string) error {
	if name == LocalManager {
		return fmt.Errorf("manager name %q is reserved", name)
	}
	if !managerNameRE.MatchString(name) {
		return fmt.Errorf("invalid manager name %q", name)
	}
	return nil
}

// ManagerRecord is one registered Studio Manager; only its token's hash is
// stored.
type ManagerRecord struct {
	Name      string
	TokenHash string
	CreatedAt time.Time
}

// ManagerStore is the Store's manager registry.
type ManagerStore interface {
	// AddManager registers m: ErrManagerExists when the name is taken; an
	// error for an invalid name, an empty token hash, or a token hash another
	// manager already holds.
	AddManager(m ManagerRecord) error
	// ListManagers lists registrations by CreatedAt, then Name.
	ListManagers() []ManagerRecord
	// ManagerByTokenHash finds the registration holding that token hash.
	ManagerByTokenHash(hash string) (ManagerRecord, bool)
	// SetManagerToken replaces name's token hash (wraps ErrNotFound; errors on an
	// empty hash or one another manager holds).
	SetManagerToken(name, tokenHash string) error
	// RemoveManager deletes name's registration (wraps ErrNotFound).
	RemoveManager(name string) error
}

// ManagedStudio is one studio a manager runs: its actor and an opaque
// manager-side location.
type ManagedStudio struct {
	ActorID  string `json:"actor_id"`
	Location string `json:"location"`
}

// ManagerInventory is what a manager reports holding.
type ManagerInventory struct {
	Studios     []ManagedStudio
	StateOwners []string
}

// ManagerReconcile is Jam's comparison of an inventory with its records.
type ManagerReconcile struct {
	Lost         []string
	Strays       []ManagedStudio
	MissingState []string
}

// ManagerStatus is one manager's row in the admin listing.
type ManagerStatus struct {
	Name        string          `json:"name"`
	Local       bool            `json:"local,omitempty"`
	Connected   bool            `json:"connected"`
	Version     string          `json:"version,omitempty"`
	Capacity    int             `json:"capacity,omitempty"`
	Studios     int             `json:"studios"`
	ConnectedAt time.Time       `json:"connected_at,omitzero"`
	LastSeen    time.Time       `json:"last_seen,omitzero"`
	Strays      []ManagedStudio `json:"strays,omitempty"`
}

// ManagerToken is the one-time response to adding or rotating a manager.
type ManagerToken struct {
	Name  string `json:"name"`
	Token string `json:"token"`
}
