package durable

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// GCS stores objects in a Google Cloud Storage bucket.
//
// This exists alongside the S3 backend rather than deferring to Cloud Storage's
// S3-compatible XML API, and the reason is authentication. Interop signs with
// SigV4, which needs an HMAC key — a long-lived secret tied to a service
// account, which somebody then has to mount and rotate. The native client uses
// Application Default Credentials, which on GKE is Workload Identity: no key
// exists to leak.
//
// Trading that away to save a backend would be a poor bargain, and the backend
// is small because the interface is four methods.
type GCS struct {
	client *storage.Client
	bucket string
	prefix string
	limit  int64
}

var _ Store = (*GCS)(nil)

// GCSConfig configures the Google Cloud Storage backend.
type GCSConfig struct {
	// Bucket is required.
	Bucket string

	// Prefix namespaces this deployment's objects inside the bucket, so one
	// bucket can serve several clusters — or several consumers.
	Prefix string

	// Endpoint overrides the API endpoint. Set it only to point at a fake;
	// leave empty for Google.
	Endpoint string

	// Limit bounds a single object in bytes; zero or less is unbounded.
	Limit int64
}

// NewGCS builds a Cloud Storage store.
//
// Credentials come from Application Default Credentials. There is deliberately
// no way to pass a key here: on GKE the right answer is Workload Identity, and
// off it the SDK already reads GOOGLE_APPLICATION_CREDENTIALS.
func NewGCS(ctx context.Context, cfg GCSConfig) (*GCS, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("durable: gcs bucket is required")
	}

	var opts []option.ClientOption
	if cfg.Endpoint != "" {
		// Only a fake is reached this way, and a fake has no credentials to
		// present. Asking for none keeps the client from hunting for ADC that
		// is not there and failing before it ever sends a request.
		opts = append(opts,
			option.WithEndpoint(cfg.Endpoint),
			option.WithoutAuthentication(),
		)
	}

	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("durable: gcs client: %w", err)
	}

	return &GCS{
		client: client,
		bucket: cfg.Bucket,
		prefix: strings.Trim(cfg.Prefix, "/"),
		limit:  cfg.Limit,
	}, nil
}

func (g *GCS) objectName(key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	if g.prefix == "" {
		return key, nil
	}

	return g.prefix + "/" + key, nil
}

func (g *GCS) object(key string) (*storage.ObjectHandle, error) {
	name, err := g.objectName(key)
	if err != nil {
		return nil, err
	}

	return g.client.Bucket(g.bucket).Object(name), nil
}

// Stat reads the object's attributes without transferring it.
//
// Generation is reported as Version: it names one particular write, so a caller
// that must read exactly the bytes it stat'd can pin it.
func (g *GCS) Stat(ctx context.Context, key string) (Attributes, bool, error) {
	obj, err := g.object(key)
	if err != nil {
		return Attributes{}, false, err
	}

	attrs, err := obj.Attrs(ctx)
	switch {
	case err == nil:
		return Attributes{
			Key:     key,
			Size:    attrs.Size,
			Updated: attrs.Updated,
			Version: strconv.FormatInt(attrs.Generation, 10),
		}, true, nil
	case errors.Is(err, storage.ErrObjectNotExist):
		return Attributes{}, false, nil
	default:
		return Attributes{}, false, fmt.Errorf("durable: attrs %s: %w", key, err)
	}
}

// Get opens the object body. A missing object is a miss, not an error.
func (g *GCS) Get(ctx context.Context, key string) (io.ReadCloser, bool, error) {
	obj, err := g.object(key)
	if err != nil {
		return nil, false, err
	}

	reader, err := obj.NewReader(ctx)
	switch {
	case err == nil:
		return reader, true, nil
	case errors.Is(err, storage.ErrObjectNotExist):
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("durable: get %s: %w", key, err)
	}
}

// Put streams the object up.
//
// No spooling: Cloud Storage's writer is a plain io.WriteCloser that chunks as
// it goes, so unlike the S3 path there is nothing here that needs a seekable
// body, and nothing that needs a temp file the size of the artifact.
func (g *GCS) Put(ctx context.Context, key string, body io.Reader) error {
	obj, err := g.object(key)
	if err != nil {
		return err
	}

	// The writer must be cancelled, not just closed, on a failed copy: Close
	// finalises whatever was written, which for a truncated body would publish
	// a short object.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	writer := obj.NewWriter(ctx)

	if _, err := io.Copy(writer, LimitReader(body, g.limit)); err != nil {
		cancel()
		return fmt.Errorf("durable: write %s: %w", key, err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("durable: close %s: %w", key, err)
	}

	return nil
}

// Delete removes the object. An absent object is not an error.
func (g *GCS) Delete(ctx context.Context, key string) error {
	obj, err := g.object(key)
	if err != nil {
		return err
	}

	if err := obj.Delete(ctx); err != nil && !errors.Is(err, storage.ErrObjectNotExist) {
		return fmt.Errorf("durable: delete %s: %w", key, err)
	}

	return nil
}

// List iterates the bucket under this store's prefix.
//
// Keys that do not round-trip are skipped: a bucket shared with another
// consumer holds objects that are not this store's to enumerate, and a reclaim
// pass must not mistake them for orphans.
func (g *GCS) List(ctx context.Context, fn func(Attributes) error) error {
	query := &storage.Query{}
	if g.prefix != "" {
		query.Prefix = g.prefix + "/"
	}

	it := g.client.Bucket(g.bucket).Objects(ctx, query)
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("durable: list: %w", err)
		}

		key := attrs.Name
		if g.prefix != "" {
			key = strings.TrimPrefix(key, g.prefix+"/")
		}
		if ValidateKey(key) != nil {
			continue
		}

		if err := fn(Attributes{
			Key:     key,
			Size:    attrs.Size,
			Updated: attrs.Updated,
			Version: strconv.FormatInt(attrs.Generation, 10),
		}); err != nil {
			return err
		}
	}
}
