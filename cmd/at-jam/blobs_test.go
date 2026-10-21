package main

import (
	"net/http/httptest"
	"testing"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/aethons-tools/cove/internal/secret"
)

// resolveBlobs builds the bucket's store with keys from the credentials and
// checks the bucket: a missing one refuses to start; no block is off.
func TestResolveBlobs(t *testing.T) {
	backend := s3mem.New()
	if err := backend.CreateBucket("jam"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gofakes3.New(backend).Server())
	defer srv.Close()
	specs := map[string]secret.Spec{"bk": {Name: "bk", Literal: true, Value: "key"}, "bs": {Name: "bs", Literal: true, Value: "secret"}}
	cfg := serveConfig{Blobs: &blobsConfig{Endpoint: srv.URL, Bucket: "jam", Region: "us-east-1", PathStyle: true, AccessKeyCred: "bk", SecretKeyCred: "bs"}}
	if s, err := resolveBlobs(cfg, specs); err != nil || s == nil {
		t.Fatalf("resolveBlobs = %v, %v", s, err)
	}
	cfg.Blobs.Bucket = "missing"
	if _, err := resolveBlobs(cfg, specs); err == nil {
		t.Fatal("a missing bucket must refuse")
	}
	if s, err := resolveBlobs(serveConfig{}, nil); s != nil || err != nil {
		t.Fatalf("no blobs block = %v, %v", s, err)
	}
}
