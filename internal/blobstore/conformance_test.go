package blobstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/aethons-tools/cove/internal/ident"
)

func newFakeS3(t *testing.T, bucket string) S3Config {
	t.Helper()
	backend := s3mem.New()
	if err := backend.CreateBucket("jam"); err != nil {
		t.Fatal(err)
	}
	// TLS, as production should be: over plain http minio-go signs a streamed
	// upload chunk by chunk, which the emulator doesn't decode.
	srv := httptest.NewTLSServer(gofakes3.New(backend).Server())
	t.Cleanup(srv.Close)
	return S3Config{Endpoint: srv.URL, Bucket: bucket, Region: "us-east-1", AccessKey: "k", SecretKey: "s", PathStyle: true, Transport: srv.Client().Transport}
}

func TestConformance(t *testing.T) {
	stores := map[string]func(t *testing.T) Store{
		"mem": func(*testing.T) Store { return NewMem() },
		"s3": func(t *testing.T) Store {
			s, err := NewS3(newFakeS3(t, "jam"))
			if err != nil {
				t.Fatal(err)
			}
			return s
		},
	}
	for name, open := range stores {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s := open(t)
			if err := s.Check(ctx); err != nil {
				t.Fatalf("Check: %v", err)
			}
			body := []byte("hello, attachments")
			if err := s.Put(ctx, "blobs/a", bytes.NewReader(body), -1, "text/plain"); err != nil {
				t.Fatal(err)
			}
			obj, info, err := s.Open(ctx, "blobs/a")
			if err != nil {
				t.Fatal(err)
			}
			if info.Size != int64(len(body)) || info.ContentType != "text/plain" {
				t.Fatalf("info = %+v", info)
			}
			if _, err := obj.Seek(7, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			rest, _ := io.ReadAll(obj)
			obj.Close()
			if string(rest) != "attachments" {
				t.Fatalf("after seek: %q", rest)
			}
			if st, err := s.Stat(ctx, "blobs/a"); err != nil || st.Size != int64(len(body)) {
				t.Fatalf("Stat = %+v, %v", st, err)
			}
			if err := s.Delete(ctx, "blobs/a"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.Open(ctx, "blobs/a"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("open deleted: %v", err)
			}
			if _, err := s.Stat(ctx, "blobs/a"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("stat deleted: %v", err)
			}
			if err := s.Delete(ctx, "blobs/missing"); err != nil {
				t.Fatalf("delete missing: %v", err)
			}
			if _, err := s.Presign(ctx, "GET", "blobs/a", 0); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("Presign: %v", err)
			}
			big := strings.Repeat("x", 6<<20) // over a single-part upload
			if err := s.Put(ctx, "blobs/big", strings.NewReader(big), -1, "application/octet-stream"); err != nil {
				t.Fatalf("big put: %v", err)
			}
			if st, _ := s.Stat(ctx, "blobs/big"); st.Size != int64(len(big)) {
				t.Fatalf("big size = %d", st.Size)
			}
		})
	}
}

func TestS3CheckMissingBucket(t *testing.T) {
	s, err := NewS3(newFakeS3(t, "nope"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Check(context.Background()); err == nil {
		t.Fatal("Check on a missing bucket should fail")
	}
}

func TestBlobKind(t *testing.T) {
	id := ident.New(ident.Blob)
	if p, err := ident.Parse(string(id)); err != nil || p.Kind() != ident.Blob || !strings.HasPrefix(string(id), "blb_") {
		t.Fatalf("blob id %q: %v", id, err)
	}
}
