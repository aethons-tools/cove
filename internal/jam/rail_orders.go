package jam

import (
	"fmt"
	"slices"

	"github.com/aethons-tools/cove/internal/ident"
)

// RailSection is a rail section a person can order
// (docs/modules/ROOT/pages/usage/jam/inbox-rail.adoc#order).
type RailSection string

const (
	RailMe     RailSection = "me"     // conversation ids
	RailAgents RailSection = "agents" // session ids
)

// ValidRailSection reports whether s has a saved order.
func ValidRailSection(s RailSection) bool { return s == RailMe || s == RailAgents }

// RailOrderStore is the Store's rail_orders table: each person's saved order
// of a rail section, shared by every client they use. Kept when the person
// is removed, like their read cursors.
type RailOrderStore interface {
	// RailOrder is user's saved order of section (nil: none saved).
	RailOrder(user ident.ID, section RailSection) []string
	// PutRailOrder replaces user's order of section; an error for a section
	// with no order.
	PutRailOrder(user ident.ID, section RailSection, ids []string) error
}

func checkRailSection(s RailSection) error {
	if !ValidRailSection(s) {
		return fmt.Errorf("rail order: section %q has no saved order", s)
	}
	return nil
}

func (m *memState) RailOrder(user ident.ID, section RailSection) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return slices.Clone(m.railOrders[user][section])
}

// putRailOrderLocked is PutRailOrder's state change; the caller holds mu.
func (m *memState) putRailOrderLocked(user ident.ID, section RailSection, ids []string) {
	if m.railOrders[user] == nil {
		m.railOrders[user] = map[RailSection][]string{}
	}
	m.railOrders[user][section] = append([]string{}, ids...)
}

func (fs *MemStore) PutRailOrder(user ident.ID, section RailSection, ids []string) error {
	if err := checkRailSection(section); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.putRailOrderLocked(user, section, ids)
	return nil
}
