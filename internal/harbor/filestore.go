package harbor

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Store records enrolled identities keyed by token hash.
type Store interface {
	Add(id Identity) error
	Lookup(tokenHash string) (Identity, bool)
	Remove(id string) error
}

// FileStore is a JSON-file-backed Store. Single-node MVP; a real DB is slice #2.
type FileStore struct {
	path string
	mu   sync.Mutex
	ids  map[string]Identity // keyed by TokenHash
}

// NewFileStore loads (or initializes) the store at path.
func NewFileStore(path string) (*FileStore, error) {
	fs := &FileStore{path: path, ids: map[string]Identity{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fs, nil
		}
		return nil, err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &fs.ids); err != nil {
			return nil, fmt.Errorf("load identity store %s: %w", path, err)
		}
	}
	return fs, nil
}

func (fs *FileStore) save() error {
	data, err := json.MarshalIndent(fs.ids, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(fs.path, data, 0o600)
}

func (fs *FileStore) Add(id Identity) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.ids[id.TokenHash] = id
	return fs.save()
}

func (fs *FileStore) Lookup(tokenHash string) (Identity, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	id, ok := fs.ids[tokenHash]
	return id, ok
}

func (fs *FileStore) Remove(id string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for h, rec := range fs.ids {
		if rec.ID == id {
			delete(fs.ids, h)
			return fs.save()
		}
	}
	return fmt.Errorf("identity %q not found", id)
}
