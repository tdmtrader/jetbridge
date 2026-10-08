package durable

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/concourse/concourse/hangar/disk"
	"github.com/concourse/concourse/hangar/gcs"
	"github.com/concourse/concourse/hangar/objectstore"
)

// DefaultProbeTimeout bounds the startup identity probe of a disk cache.
const DefaultProbeTimeout = 5 * time.Second

// listPageSize is one listing page. It is the disk store's maximum, and GCS
// pages at most this many per RPC anyway.
const listPageSize = 1000

// Config selects the cache's backend and its dedicated namespace.
type Config struct {
	// Kind is "gcs" or "disk".
	Kind string

	// Bucket is the cache's GCS bucket, or its disk namespace. It is never
	// the output one.
	Bucket string

	// Endpoint is a GCS emulator override (empty means real GCS through
	// Application Default Credentials), or the disk store's HTTPS origin.
	Endpoint string

	// StoreID, TokenFile and CACert reach a disk store as its cache role.
	StoreID   string
	TokenFile string
	CACert    string

	// Timeout bounds one disk-store request.
	Timeout time.Duration

	// ProbeTimeout bounds the disk store's identity probe at Open, so an
	// unreachable store costs seconds at startup rather than Timeout. Zero
	// means DefaultProbeTimeout.
	ProbeTimeout time.Duration

	// Limit bounds a single object in bytes; zero or less is unbounded.
	Limit int64
}

// Validate is the configuration half: what can be refused before anything is
// dialled.
func (c Config) Validate() error {
	switch c.Kind {
	case "gcs":
	case "disk":
		if c.Endpoint == "" || c.StoreID == "" || c.TokenFile == "" {
			return errors.New("--durable-store=disk requires --durable-endpoint, --durable-store-id and --durable-token-file")
		}
	default:
		return fmt.Errorf("unknown --durable-store %q: want \"\", \"gcs\" or \"disk\"", c.Kind)
	}
	if c.Bucket == "" {
		return fmt.Errorf("--durable-bucket is required for --durable-store=%s", c.Kind)
	}
	return nil
}

// Open builds the cache's own clients over its own namespace.
//
// It constructs a fresh client and a fresh delete client every time and never
// accepts one: the output plane's client, and for disk its connection pool,
// are never shared with cache churn. The returned closer is the caller's.
func Open(ctx context.Context, c Config) (Store, func() error, error) {
	if err := c.Validate(); err != nil {
		return nil, nil, err
	}
	if c.Kind == "disk" {
		probe := c.ProbeTimeout
		if probe <= 0 {
			probe = DefaultProbeTimeout
		}
		client := disk.ClientConfig{Endpoint: c.Endpoint, StoreID: c.StoreID, TokenFile: c.TokenFile, CACert: c.CACert, Timeout: c.Timeout, ProbeTimeout: probe}
		objects, err := disk.NewClient(client)
		if err != nil {
			return nil, nil, fmt.Errorf("durable: disk cache client: %w", err)
		}
		deleter, err := disk.NewDeleteClient(client)
		if err != nil {
			return nil, nil, fmt.Errorf("durable: disk cache delete client: %w", err)
		}
		return New(objects, deleter, c.Bucket, c.Limit), func() error { return nil }, nil
	}

	objects, closeObjects, err := gcs.NewClient(ctx, c.Endpoint)
	if err != nil {
		return nil, nil, fmt.Errorf("durable: gcs cache client: %w", err)
	}
	deleter, closeDeleter, err := gcs.NewDeleteClient(ctx, c.Endpoint)
	if err != nil {
		_ = closeObjects()
		return nil, nil, fmt.Errorf("durable: gcs cache delete client: %w", err)
	}
	return New(objects, deleter, c.Bucket, c.Limit), func() error {
		return errors.Join(closeObjects(), closeDeleter())
	}, nil
}

// IsUnavailable reports whether an Open failed because the store could not be
// reached right now -- a transport failure or a probe that ran out of time --
// rather than because the configuration is wrong. Only the first is worth
// retrying; a refused credential, a store answering with another identity, or
// an unreadable token file is a configuration error.
func IsUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, objectstore.ErrUnauthorized) || errors.Is(err, objectstore.ErrConflict) {
		return false
	}
	return errors.Is(err, objectstore.ErrInfrastructure) || errors.Is(err, context.DeadlineExceeded)
}

// New wraps an object client as the cache. A nil deleter is a cache that can
// fill and read but never expire.
func New(objects objectstore.Client, deleter objectstore.DeleteClient, bucket string, limit int64) Store {
	return &objectStore{objects: objects, deleter: deleter, bucket: bucket, limit: limit}
}

type objectStore struct {
	objects objectstore.Client
	deleter objectstore.DeleteClient
	bucket  string
	limit   int64
}

func attributesOf(attrs objectstore.Attrs) Attributes {
	return Attributes{
		Key:     attrs.Key,
		Size:    attrs.Size,
		Updated: attrs.Created,
		Version: strconv.FormatInt(attrs.Generation, 10),
	}
}

func (s *objectStore) Stat(ctx context.Context, key string) (Attributes, bool, error) {
	if err := ValidateKey(key); err != nil {
		return Attributes{}, false, err
	}
	attrs, err := s.objects.StatCurrent(ctx, s.bucket, key)
	if errors.Is(err, objectstore.ErrNotFound) {
		return Attributes{}, false, nil
	}
	if err != nil {
		return Attributes{}, false, err
	}
	return attributesOf(attrs), true, nil
}

// Get reads the current generation, pinned: a stat then an exact open, so an
// object expired and recreated between the two is read as a miss rather than
// as a mixture. The attributes name the generation that was opened.
func (s *objectStore) Get(ctx context.Context, key string) (io.ReadCloser, Attributes, bool, error) {
	if err := ValidateKey(key); err != nil {
		return nil, Attributes{}, false, err
	}
	attrs, err := s.objects.StatCurrent(ctx, s.bucket, key)
	if errors.Is(err, objectstore.ErrNotFound) {
		return nil, Attributes{}, false, nil
	}
	if err != nil {
		return nil, Attributes{}, false, err
	}
	body, err := s.objects.OpenExact(ctx, s.bucket, key, attrs.Generation)
	if errors.Is(err, objectstore.ErrNotFound) {
		return nil, Attributes{}, false, nil
	}
	if err != nil {
		return nil, Attributes{}, false, err
	}
	return body, attributesOf(attrs), true, nil
}

// Put creates the object if it is absent.
//
// A key already present is success: keys are content-derived, so the bytes
// there are the bytes this call carries, and the immutable store would refuse
// to replace them anyway. The stat first only saves the upload; the
// create-if-absent precondition is what decides, and losing that race to a
// concurrent writer of the same key (ErrPreconditionFailed) is the same success.
func (s *objectStore) Put(ctx context.Context, key string, body io.Reader) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	if _, err := s.objects.StatCurrent(ctx, s.bucket, key); err == nil {
		return nil
	} else if !errors.Is(err, objectstore.ErrNotFound) {
		return err
	}
	_, err := s.objects.CreateAbsent(ctx, s.bucket, key, nil, LimitReader(body, s.limit))
	if errors.Is(err, objectstore.ErrPreconditionFailed) {
		return nil
	}
	if errors.Is(err, ErrTooLarge) {
		return ErrTooLarge
	}
	return err
}

// Delete removes the current generation: a stat for the generation, then a
// delete conditioned on exactly it. An absent key is not an error, and neither
// is a key recreated between the two -- that is a newer object, and keeping it
// is right.
func (s *objectStore) Delete(ctx context.Context, key string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	if s.deleter == nil {
		return errors.New("durable: this cache holds no delete capability")
	}
	attrs, err := s.objects.StatCurrent(ctx, s.bucket, key)
	if errors.Is(err, objectstore.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.deleteGeneration(ctx, key, attrs.Generation)
}

// DeleteVersion deletes exactly one generation, named by the caller from what
// it read. It never re-stats the key: a newer object there is kept.
func (s *objectStore) DeleteVersion(ctx context.Context, key, version string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	generation, err := strconv.ParseInt(version, 10, 64)
	if err != nil || generation <= 0 {
		return fmt.Errorf("durable: invalid version %q", version)
	}
	return s.deleteGeneration(ctx, key, generation)
}

func (s *objectStore) deleteGeneration(ctx context.Context, key string, generation int64) error {
	if s.deleter == nil {
		return errors.New("durable: this cache holds no delete capability")
	}
	err := s.deleter.DeleteExact(ctx, s.bucket, key, generation)
	if errors.Is(err, objectstore.ErrNotFound) || errors.Is(err, objectstore.ErrPreconditionFailed) {
		return nil
	}
	return err
}

// List walks the whole namespace a page at a time, resuming from the last
// (key, generation) seen rather than from a provider token.
func (s *objectStore) List(ctx context.Context, fn func(Attributes) error) error {
	request := objectstore.ListRequest{PageSize: listPageSize}
	for {
		page, err := s.objects.List(ctx, s.bucket, request)
		if err != nil {
			return err
		}
		for _, attrs := range page.Objects {
			if err := fn(attributesOf(attrs)); err != nil {
				return err
			}
		}
		if page.Done || len(page.Objects) == 0 {
			return nil
		}
		last := page.Objects[len(page.Objects)-1]
		request.After, request.AfterGeneration = last.Key, last.Generation
	}
}
