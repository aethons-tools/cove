package blobstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sync"
	"time"
)

// Mem is an in-memory Store, for tests.
type Mem struct {
	mu   sync.Mutex
	objs map[string]memObj
}

type memObj struct {
	data []byte
	info Info
}

// NewMem returns an empty in-memory store.
func NewMem() *Mem { return &Mem{objs: map[string]memObj{}} }

var _ Store = (*Mem)(nil)

func (m *Mem) Put(ctx context.Context, key string, r io.Reader, _ int64, contentType string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objs[key] = memObj{data: data, info: Info{Size: int64(len(data)), ContentType: contentType, ETag: hex.EncodeToString(sum[:8]), Modified: time.Now()}}
	return nil
}

type memReader struct{ *bytes.Reader }

func (memReader) Close() error { return nil }

func (m *Mem) Open(_ context.Context, key string) (Object, Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objs[key]
	if !ok {
		return nil, Info{}, ErrNotFound
	}
	return memReader{bytes.NewReader(o.data)}, o.info, nil
}

func (m *Mem) Stat(_ context.Context, key string) (Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objs[key]
	if !ok {
		return Info{}, ErrNotFound
	}
	return o.info, nil
}

func (m *Mem) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objs, key)
	return nil
}

func (m *Mem) Presign(context.Context, string, string, time.Duration) (string, error) {
	return "", ErrUnsupported
}

func (m *Mem) Check(context.Context) error { return nil }

// Len is how many objects are stored (tests).
func (m *Mem) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.objs)
}
