package blobstore

import (
	"errors"
	"testing"

	"github.com/minio/minio-go/v7"
)

// A streamed upload buffers one part, not minio-go's ~537 MiB default.
func TestPutOptionsPartSize(t *testing.T) {
	if got := putOptions("x/y").PartSize; got != streamPartSize || got > 32<<20 {
		t.Fatalf("part size = %d", got)
	}
}

// Only a missing key is ErrNotFound; a missing bucket is a real error.
func TestNotFoundMapsKeysOnly(t *testing.T) {
	if !errors.Is(notFound(minio.ErrorResponse{Code: "NoSuchKey", StatusCode: 404}), ErrNotFound) {
		t.Fatal("NoSuchKey should be ErrNotFound")
	}
	if errors.Is(notFound(minio.ErrorResponse{Code: "NoSuchBucket", StatusCode: 404}), ErrNotFound) {
		t.Fatal("NoSuchBucket must not read as a missing object")
	}
}
