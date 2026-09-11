package harbor

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Store records enrolled identities (by token hash) and the destination table.
type Store interface {
	Add(id Identity) error
	Lookup(tokenHash string) (Identity, bool)
	Remove(id string) error
	ListIdentities() []Identity

	AddDestination(d Destination) error
	RemoveDestination(name string) error
	ListDestinations() []Destination
	Match(reqPath string) (Destination, bool)
}

// storeFile is the on-disk JSON shape (format v2).
type storeFile struct {
	Identities   map[string]Identity    `json:"identities"`   // keyed by TokenHash
	Destinations map[string]Destination `json:"destinations"` // keyed by Name
}

// FileStore is a JSON-file-backed Store. Single-node MVP; the serve process is the
// sole writer, so there is no cross-process contention.
type FileStore struct {
	path  string
	mu    sync.Mutex
	ids   map[string]Identity
	dests map[string]Destination
}

// NewFileStore loads (or initializes) the store at path. A legacy v1 file (a bare
// map[tokenHash]Identity) is migrated into the identities collection.
func NewFileStore(path string) (*FileStore, error) {
	fs := &FileStore{path: path, ids: map[string]Identity{}, dests: map[string]Destination{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fs, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return fs, nil
	}
	var v2 storeFile
	if err := json.Unmarshal(data, &v2); err != nil {
		return nil, fmt.Errorf("load store %s: %w", path, err)
	}
	if v2.Identities == nil && v2.Destinations == nil {
		// v1 migration: the whole file is a map[tokenHash]Identity.
		var legacy map[string]Identity
		if err := json.Unmarshal(data, &legacy); err != nil {
			return nil, fmt.Errorf("load store %s (legacy): %w", path, err)
		}
		fs.ids = legacy
		return fs, nil
	}
	if v2.Identities != nil {
		fs.ids = v2.Identities
	}
	if v2.Destinations != nil {
		fs.dests = v2.Destinations
	}
	return fs, nil
}

// save persists both collections (v2). Caller holds fs.mu.
func (fs *FileStore) save() error {
	data, err := json.MarshalIndent(storeFile{Identities: fs.ids, Destinations: fs.dests}, "", "  ")
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

func (fs *FileStore) ListIdentities() []Identity {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	out := make([]Identity, 0, len(fs.ids))
	for _, id := range fs.ids {
		out = append(out, id)
	}
	return out
}

func (fs *FileStore) AddDestination(d Destination) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.dests[d.Name] = d
	return fs.save()
}

func (fs *FileStore) RemoveDestination(name string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if _, ok := fs.dests[name]; !ok {
		return fmt.Errorf("destination %q not found", name)
	}
	delete(fs.dests, name)
	return fs.save()
}

func (fs *FileStore) ListDestinations() []Destination {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	out := make([]Destination, 0, len(fs.dests))
	for _, d := range fs.dests {
		out = append(out, d)
	}
	return out
}

// Match resolves the destination whose Route prefixes reqPath (longest wins),
// reusing Config.Match over a snapshot of the current table.
func (fs *FileStore) Match(reqPath string) (Destination, bool) {
	return Config{Destinations: fs.ListDestinations()}.Match(reqPath)
}
