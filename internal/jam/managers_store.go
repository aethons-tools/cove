package jam

import (
	"cmp"
	"fmt"
	"slices"
)

// prepareAddManager validates m. Caller holds mu.
func (m *memState) prepareAddManager(r ManagerRecord) error {
	if err := ValidManagerName(r.Name); err != nil {
		return err
	}
	if r.TokenHash == "" {
		return fmt.Errorf("manager %q: token hash is required", r.Name)
	}
	if _, ok := m.managers[r.Name]; ok {
		return fmt.Errorf("manager %q: %w", r.Name, ErrManagerExists)
	}
	for _, o := range m.managers {
		if o.TokenHash == r.TokenHash {
			return fmt.Errorf("manager %q: token hash already in use", r.Name)
		}
	}
	return nil
}

// prepareSetManagerToken returns name's record with the new hash. Caller holds mu.
func (m *memState) prepareSetManagerToken(name, hash string) (ManagerRecord, error) {
	r, ok := m.managers[name]
	if !ok {
		return r, fmt.Errorf("manager %q: %w", name, ErrNotFound)
	}
	if hash == "" {
		return r, fmt.Errorf("manager %q: token hash is required", name)
	}
	for _, o := range m.managers {
		if o.Name != name && o.TokenHash == hash {
			return r, fmt.Errorf("manager %q: token hash already in use", name)
		}
	}
	r.TokenHash = hash
	return r, nil
}

func (m *memState) prepareRemoveManager(name string) error {
	if _, ok := m.managers[name]; !ok {
		return fmt.Errorf("manager %q: %w", name, ErrNotFound)
	}
	return nil
}

func (m *memState) applyPutManager(r ManagerRecord) { m.managers[r.Name] = r }

func (m *memState) applyRemoveManager(name string) { delete(m.managers, name) }

func (m *memState) ListManagers() []ManagerRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ManagerRecord, 0, len(m.managers))
	for _, r := range m.managers {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b ManagerRecord) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.Name, b.Name))
	})
	return out
}

func (m *memState) ManagerByTokenHash(hash string) (ManagerRecord, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, r := range m.managers {
		if r.TokenHash == hash {
			return r, true
		}
	}
	return ManagerRecord{}, false
}

func (fs *MemStore) AddManager(r ManagerRecord) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if err := fs.prepareAddManager(r); err != nil {
		return err
	}
	fs.applyPutManager(r)
	return nil
}

func (fs *MemStore) SetManagerToken(name, tokenHash string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	r, err := fs.prepareSetManagerToken(name, tokenHash)
	if err != nil {
		return err
	}
	fs.applyPutManager(r)
	return nil
}

func (fs *MemStore) RemoveManager(name string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if err := fs.prepareRemoveManager(name); err != nil {
		return err
	}
	fs.applyRemoveManager(name)
	return nil
}
