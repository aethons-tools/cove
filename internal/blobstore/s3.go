package blobstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Config is an S3-compatible bucket: AWS S3, MinIO (PathStyle), R2, GCS
// interop. The keys are resolved in memory by the caller and never logged.
type S3Config struct {
	Endpoint  string // http(s)://host[:port]
	Bucket    string
	Region    string
	AccessKey string
	SecretKey string
	PathStyle bool
	// Transport overrides the HTTP transport (tests: a TLS test server's).
	Transport http.RoundTripper
}

// S3 is a Store over an S3-compatible bucket.
type S3 struct {
	c      *minio.Client
	bucket string
}

var _ Store = (*S3)(nil)

// NewS3 returns a store over cfg's bucket. It makes no request; Check does.
func NewS3(cfg S3Config) (*S3, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("blobstore: endpoint must be http(s)://host[:port]")
	}
	lookup := minio.BucketLookupDNS
	if cfg.PathStyle {
		lookup = minio.BucketLookupPath
	}
	c, err := minio.New(u.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure:       u.Scheme == "https",
		Region:       cfg.Region,
		BucketLookup: lookup,
		Transport:    cfg.Transport,
	})
	if err != nil {
		return nil, fmt.Errorf("blobstore: %w", err)
	}
	return &S3{c: c, bucket: cfg.Bucket}, nil
}

func (s *S3) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := s.c.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	return err
}

func (s *S3) Open(ctx context.Context, key string) (Object, Info, error) {
	info, err := s.Stat(ctx, key)
	if err != nil {
		return nil, Info{}, err
	}
	obj, err := s.c.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, Info{}, notFound(err)
	}
	return obj, info, nil
}

func (s *S3) Stat(ctx context.Context, key string) (Info, error) {
	st, err := s.c.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return Info{}, notFound(err)
	}
	return Info{Size: st.Size, ContentType: st.ContentType, ETag: st.ETag, Modified: st.LastModified}, nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	return notFoundOK(s.c.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}))
}

func (s *S3) Presign(context.Context, string, string, time.Duration) (string, error) {
	return "", ErrUnsupported
}

func (s *S3) Check(ctx context.Context) error {
	ok, err := s.c.BucketExists(ctx, s.bucket)
	switch {
	case err != nil:
		return fmt.Errorf("blobstore: bucket %q: %w", s.bucket, err)
	case !ok:
		return fmt.Errorf("blobstore: bucket %q does not exist", s.bucket)
	}
	return nil
}

func notFound(err error) error {
	if r := minio.ToErrorResponse(err); r.Code == "NoSuchKey" || r.StatusCode == 404 {
		return ErrNotFound
	}
	return err
}

func notFoundOK(err error) error {
	if errors.Is(notFound(err), ErrNotFound) {
		return nil
	}
	return err
}
