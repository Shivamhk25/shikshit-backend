// Package storage signs links to S3-compatible object storage (MinIO in development, S3 or R2 in production) and
// checks uploaded objects. Clients move the bytes themselves with the signed links (decision 0020).
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ErrNotFound is returned for an object that does not exist (for example, an upload that never happened).
var ErrNotFound = errors.New("storage: object not found")

// Store is what the files service needs from object storage.
type Store interface {
	// PresignPut returns a link the client PUTs the file to, with the headers it must send.
	PresignPut(ctx context.Context, key, contentType string, ttl time.Duration) (string, map[string]string, error)
	// PresignGet returns a link to download the file, served with the given content type.
	PresignGet(ctx context.Context, key, contentType string, ttl time.Duration) (string, error)
	// Head returns the object's size and its first bytes (up to n), to check what was really uploaded.
	Head(ctx context.Context, key string, n int64) (int64, []byte, error)
	Remove(ctx context.Context, key string) error
}

// Config describes the bucket. Region must be set so signing needs no network round trip.
type Config struct {
	Endpoint  string // host[:port], no scheme
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string
	Secure    bool // HTTPS
}

// MinIO is a Store over any S3-compatible service, using the minio-go client.
type MinIO struct {
	client *minio.Client
	bucket string
}

// New returns a Store for the bucket. It does not contact the service.
func New(c Config) (*MinIO, error) {
	if c.Endpoint == "" || c.Bucket == "" || c.AccessKey == "" || c.SecretKey == "" {
		return nil, errors.New("storage: endpoint, bucket, access key and secret key are required")
	}
	if c.Region == "" {
		c.Region = "us-east-1"
	}
	cl, err := minio.New(c.Endpoint, &minio.Options{Creds: credentials.NewStaticV4(c.AccessKey, c.SecretKey, ""), Secure: c.Secure, Region: c.Region})
	if err != nil {
		return nil, err
	}
	return &MinIO{client: cl, bucket: c.Bucket}, nil
}

// PresignPut implements Store. The content type is part of the signature, so the client cannot upload as another type.
func (m *MinIO) PresignPut(ctx context.Context, key, contentType string, ttl time.Duration) (string, map[string]string, error) {
	u, err := m.client.PresignHeader(ctx, "PUT", m.bucket, key, ttl, nil, map[string][]string{"Content-Type": {contentType}})
	if err != nil {
		return "", nil, err
	}
	return u.String(), map[string]string{"Content-Type": contentType}, nil
}

// PresignGet implements Store. The response content type is pinned to the type the checks accepted, so a file can
// never be served as HTML from the storage address.
func (m *MinIO) PresignGet(ctx context.Context, key, contentType string, ttl time.Duration) (string, error) {
	u, err := m.client.PresignedGetObject(ctx, m.bucket, key, ttl, url.Values{"response-content-type": {contentType}})
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// Head implements Store.
func (m *MinIO) Head(ctx context.Context, key string, n int64) (int64, []byte, error) {
	info, err := m.client.StatObject(ctx, m.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return 0, nil, ErrNotFound
		}
		return 0, nil, err
	}
	opts := minio.GetObjectOptions{}
	if err := opts.SetRange(0, n-1); err != nil {
		return 0, nil, err
	}
	obj, err := m.client.GetObject(ctx, m.bucket, key, opts)
	if err != nil {
		return 0, nil, err
	}
	defer obj.Close()
	head, err := io.ReadAll(io.LimitReader(obj, n))
	if err != nil {
		return 0, nil, fmt.Errorf("storage: read head: %w", err)
	}
	return info.Size, head, nil
}

// Remove implements Store. Removing an object that is already gone is not an error.
func (m *MinIO) Remove(ctx context.Context, key string) error {
	return m.client.RemoveObject(ctx, m.bucket, key, minio.RemoveObjectOptions{})
}
