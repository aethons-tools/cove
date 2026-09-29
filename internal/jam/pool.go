package jam

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

// PoolAccount is one Anthropic subscription-OAuth account the broker draws from.
// AccessToken/RefreshToken are secrets: never log them.
type PoolAccount struct {
	Name         string    `json:"name"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// PoolStore persists account definitions and identity→account bindings. All
// methods are safe for concurrent use.
type PoolStore interface {
	Accounts() ([]PoolAccount, error)
	SetAccount(PoolAccount) error         // upsert by Name (the refresher rotates tokens here)
	Bindings() (map[string]string, error) // identityHash → accountName
	Bind(identityHash, accountName string) error
}

// Pool binds each identity to one account for life and returns that account's
// current access token. Selection spreads new identities across the least-loaded
// accounts. It never refreshes inline — that is the refresher's job.
type Pool struct {
	store PoolStore
	mu    sync.Mutex // serializes bind-or-select so a first-use never double-binds
}

// NewPool returns a Pool backed by store.
func NewPool(store PoolStore) *Pool { return &Pool{store: store} }

// TokenFor returns the current access token for the account bound to
// identityHash, binding a least-loaded account on first use.
func (p *Pool) TokenFor(identityHash string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	accts, err := p.store.Accounts()
	if err != nil {
		return "", err
	}
	if len(accts) == 0 {
		return "", fmt.Errorf("pool: no accounts configured")
	}
	byName := make(map[string]PoolAccount, len(accts))
	for _, a := range accts {
		byName[a.Name] = a
	}
	bindings, err := p.store.Bindings()
	if err != nil {
		return "", err
	}
	if name, ok := bindings[identityHash]; ok {
		a, ok := byName[name]
		if !ok {
			return "", fmt.Errorf("pool: bound account %q no longer exists", name)
		}
		return a.AccessToken, nil
	}
	chosen := leastLoaded(accts, bindings)
	if err := p.store.Bind(identityHash, chosen); err != nil {
		return "", err
	}
	return byName[chosen].AccessToken, nil
}

// leastLoaded returns the name of the account with the fewest bindings, breaking
// ties by name for determinism.
func leastLoaded(accts []PoolAccount, bindings map[string]string) string {
	counts := make(map[string]int, len(accts))
	for _, a := range accts {
		counts[a.Name] = 0
	}
	for _, name := range bindings {
		if _, ok := counts[name]; ok {
			counts[name]++
		}
	}
	names := make([]string, 0, len(accts))
	for _, a := range accts {
		names = append(names, a.Name)
	}
	sort.Strings(names)
	best := names[0]
	for _, n := range names {
		if counts[n] < counts[best] {
			best = n
		}
	}
	return best
}

// FilePoolStore is a JSON-file-backed PoolStore: atomic writes, 0600, mutexed.
type FilePoolStore struct {
	path string
	mu   sync.Mutex
}

type poolFile struct {
	Accounts []PoolAccount     `json:"accounts"`
	Bindings map[string]string `json:"bindings"`
}

// NewFilePoolStore opens (or creates, empty) the pool file at path.
func NewFilePoolStore(path string) (*FilePoolStore, error) {
	s := &FilePoolStore{path: path}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := s.write(poolFile{Bindings: map[string]string{}}); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return s, nil
}

func (s *FilePoolStore) read() (poolFile, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return poolFile{}, err
	}
	var pf poolFile
	if len(b) > 0 {
		if err := json.Unmarshal(b, &pf); err != nil {
			return poolFile{}, err
		}
	}
	if pf.Bindings == nil {
		pf.Bindings = map[string]string{}
	}
	return pf, nil
}

func (s *FilePoolStore) write(pf poolFile) error {
	b, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Accounts returns all account definitions.
func (s *FilePoolStore) Accounts() ([]PoolAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pf, err := s.read()
	return pf.Accounts, err
}

// SetAccount upserts a by Name.
func (s *FilePoolStore) SetAccount(a PoolAccount) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pf, err := s.read()
	if err != nil {
		return err
	}
	for i := range pf.Accounts {
		if pf.Accounts[i].Name == a.Name {
			pf.Accounts[i] = a
			return s.write(pf)
		}
	}
	pf.Accounts = append(pf.Accounts, a)
	return s.write(pf)
}

// Bindings returns a copy of the identity→account map.
func (s *FilePoolStore) Bindings() (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pf, err := s.read()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(pf.Bindings))
	for k, v := range pf.Bindings {
		out[k] = v
	}
	return out, nil
}

// Bind records identityHash → accountName (last write wins for an identity).
func (s *FilePoolStore) Bind(identityHash, accountName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pf, err := s.read()
	if err != nil {
		return err
	}
	pf.Bindings[identityHash] = accountName
	return s.write(pf)
}
