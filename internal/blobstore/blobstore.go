// Package blobstore stores attachment bytes (intercom attachments; see
// docs/superpowers/specs/2026-10-21-intercom-attachments-design.md) in an
// S3-compatible bucket, or in memory for tests. It knows keys and bytes
// only: who may read a blob is the caller's (internal/jam) business.
package blobstore

import (
	"context"
	"errors"
	"io"
	"time"
)

// Info describes a stored object.
type Info struct {
	Size        int64
	ContentType string
	ETag        string
	Modified    time.Time
}

// Object is an open object: seekable, so a handler can serve byte ranges.
type Object interface {
	io.ReadSeekCloser
}

// Store is a byte store keyed by string.
type Store interface {
	// Put stores r under key; size is -1 when unknown (streamed).
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// Open opens key for reading (ErrNotFound).
	Open(ctx context.Context, key string) (Object, Info, error)
	// Stat describes key (ErrNotFound).
	Stat(ctx context.Context, key string) (Info, error)
	// Delete removes key; a missing key is no error.
	Delete(ctx context.Context, key string) error
	// Presign returns a short-lived URL for method on key (direct upload or
	// download, later); ErrUnsupported until that is switched on.
	Presign(ctx context.Context, method, key string, ttl time.Duration) (string, error)
	// Check reports whether the store is reachable and its bucket exists.
	Check(ctx context.Context) error
}

// ErrNotFound is a missing key; ErrUnsupported an operation this store
// doesn't offer.
var (
	ErrNotFound    = errors.New("blobstore: not found")
	ErrUnsupported = errors.New("blobstore: unsupported")
)
