// Package disk implements immutable objects on a single, exclusively owned
// filesystem. The index commits only after the referenced bytes are durable.
package disk

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/objectstore"
	bolt "go.etcd.io/bbolt"
)

const format = "hangar-disk-v1"

var objects = []byte("objects")
var settings = []byte("settings")
var blobOwners = []byte("blob-owners")

type record struct {
	Attrs    objectstore.Attrs
	Blob     string
	SHA256   string
	Deleting bool
}

// Store has one owner process. Only the storage server holds this type; remote
// clients receive interfaces without filesystem or unconditional-delete access.
type Store struct {
	root     string
	id       string
	maxBytes int64
	db       *bolt.DB
	life     sync.RWMutex
	mu       sync.Mutex
	closed   bool
	// Test-only crash seam. A subprocess exits here; production never sets it.
	checkpoint func(string)
}

// Initialize provisions an empty directory explicitly. Open never initializes
// a missing index: replacing the volume must not silently recreate its identity.
func Initialize(root, id string) error {
	if err := validateID(id); err != nil {
		return err
	}
	if !filepath.IsAbs(root) {
		return fmt.Errorf("disk root must be absolute")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("disk root must be a real directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	// ext filesystems may have a provider-created lost+found directory.
	for _, entry := range entries {
		if entry.Name() != "lost+found" {
			return fmt.Errorf("refusing to initialize nonempty disk root")
		}
	}
	index := filepath.Join(root, "index.db")
	f, err := os.OpenFile(index, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	db, err := bolt.Open(index, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucket(settings)
		if err != nil {
			return err
		}
		if err = b.Put([]byte("format"), []byte(format)); err != nil {
			return err
		}
		if err = b.Put([]byte("id"), []byte(id)); err != nil {
			return err
		}
		if _, err = tx.CreateBucket(objects); err != nil {
			return err
		}
		_, err = tx.CreateBucket(blobOwners)
		return err
	})
	err = errors.Join(err, db.Close())
	if err != nil {
		return err
	}
	if err := os.Mkdir(filepath.Join(root, "blobs"), 0700); err != nil {
		return err
	}
	return errors.Join(syncDir(root), syncDir(filepath.Dir(root)))
}

// Open refuses a missing, foreign, or corrupt store and completes interrupted
// deletions before serving requests. maxBytes bounds each admitted upload.
func Open(root, expectedID string, maxBytes int64) (*Store, error) {
	if err := validateID(expectedID); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(root) || maxBytes <= 0 || maxBytes == math.MaxInt64 {
		return nil, fmt.Errorf("disk requires an absolute root and positive bounded object limit")
	}
	for _, p := range []string{root, filepath.Join(root, "blobs"), filepath.Join(root, "index.db")} {
		info, err := os.Lstat(p)
		if err != nil {
			return nil, fmt.Errorf("disk is not initialized: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("disk paths must not be symlinks")
		}
		if p == filepath.Join(root, "index.db") && !info.Mode().IsRegular() {
			return nil, fmt.Errorf("disk index must be regular")
		}
	}
	db, err := bolt.Open(filepath.Join(root, "index.db"), 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open exclusive disk index: %w", err)
	}
	s := &Store{root: root, id: expectedID, maxBytes: maxBytes, db: db}
	err = db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(settings)
		if b == nil || tx.Bucket(objects) == nil || tx.Bucket(blobOwners) == nil || string(b.Get([]byte("format"))) != format {
			return fmt.Errorf("unsupported disk format")
		}
		if string(b.Get([]byte("id"))) != expectedID {
			return fmt.Errorf("%w: disk identity mismatch", hangar.ErrConflict)
		}
		return nil
	})
	if err == nil {
		err = s.recover()
	}
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	s.life.Lock()
	defer s.life.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.db.Close()
}

// ID is the identity read from the provisioned index, never an endpoint name.
func (s *Store) ID() string { return s.id }

func (s *Store) enter(ctx context.Context) (func(), error) {
	s.life.RLock()
	if s.closed {
		s.life.RUnlock()
		return nil, objectstore.ErrInfrastructure
	}
	if err := ctx.Err(); err != nil {
		s.life.RUnlock()
		return nil, err
	}
	return s.life.RUnlock, nil
}

func (s *Store) CreateAbsent(ctx context.Context, bucket, key string, metadata map[string]string, body io.Reader) (objectstore.Attrs, error) {
	done, err := s.enter(ctx)
	if err != nil {
		return objectstore.Attrs{}, err
	}
	defer done()
	indexKey, err := validateKey(bucket, key)
	if err != nil {
		return objectstore.Attrs{}, err
	}
	if body == nil {
		return objectstore.Attrs{}, hangar.ErrCorrupt
	}
	encoded, err := json.Marshal(metadata)
	if err != nil || len(encoded) > 64<<10 || len(metadata) > 128 {
		return objectstore.Attrs{}, hangar.ErrLimitExceeded
	}
	var copied map[string]string
	if err := json.Unmarshal(encoded, &copied); err != nil {
		return objectstore.Attrs{}, err
	}
	f, err := os.CreateTemp(filepath.Join(s.root, "blobs"), "blob-")
	if err != nil {
		return objectstore.Attrs{}, infra(err)
	}
	committed := false
	defer func() {
		_ = f.Close()
		if !committed {
			_ = os.Remove(f.Name())
		}
	}()
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(contextReader{ctx, body}, s.maxBytes+1))
	if err != nil {
		return objectstore.Attrs{}, infra(err)
	}
	if size > s.maxBytes {
		return objectstore.Attrs{}, hangar.ErrLimitExceeded
	}
	if err = f.Sync(); err != nil {
		return objectstore.Attrs{}, infra(err)
	}
	if err = f.Close(); err != nil {
		return objectstore.Attrs{}, infra(err)
	}
	if err = syncDir(filepath.Dir(f.Name())); err != nil {
		return objectstore.Attrs{}, infra(err)
	}
	s.point("blob-durable")
	s.mu.Lock()
	defer s.mu.Unlock()
	attrs := objectstore.Attrs{Key: key, Size: size, Metadata: copied, Created: time.Now().UTC(), Metageneration: 1}
	err = s.db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		b := tx.Bucket(objects)
		if old := b.Get(indexKey); old != nil {
			r, err := decode(old)
			if err != nil {
				return err
			}
			if r.Deleting {
				return objectstore.ErrInfrastructure
			}
			return objectstore.ErrPreconditionFailed
		}
		n, err := b.NextSequence()
		if err != nil {
			return err
		}
		if n > math.MaxInt64 {
			return fmt.Errorf("generation exhausted")
		}
		attrs.Generation = int64(n)
		r := record{Attrs: attrs, Blob: filepath.Base(f.Name()), SHA256: hex.EncodeToString(h.Sum(nil))}
		v, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if err := tx.Bucket(blobOwners).Put([]byte(r.Blob), indexKey); err != nil {
			return err
		}
		return b.Put(indexKey, v)
	})
	if err != nil {
		// A failed fsync may leave commit status ambiguous. Retain the durable
		// blob; recovery will decide from the index, never delete possible data.
		if !errors.Is(err, objectstore.ErrPreconditionFailed) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			committed = true
		}
		return objectstore.Attrs{}, infra(err)
	}
	committed = true
	s.point("object-committed")
	return attrs, nil
}

func (s *Store) StatCurrent(ctx context.Context, bucket, key string) (objectstore.Attrs, error) {
	return s.stat(ctx, bucket, key, 0)
}
func (s *Store) StatExact(ctx context.Context, bucket, key string, generation int64) (objectstore.Attrs, error) {
	if generation <= 0 {
		return objectstore.Attrs{}, objectstore.ErrPreconditionFailed
	}
	return s.stat(ctx, bucket, key, generation)
}
func (s *Store) stat(ctx context.Context, bucket, key string, generation int64) (objectstore.Attrs, error) {
	done, err := s.enter(ctx)
	if err != nil {
		return objectstore.Attrs{}, err
	}
	defer done()
	k, err := validateKey(bucket, key)
	if err != nil {
		return objectstore.Attrs{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.lookup(k, generation)
	if err != nil {
		return objectstore.Attrs{}, err
	}
	if err = s.checkBlob(r); err != nil {
		return objectstore.Attrs{}, err
	}
	return r.Attrs, nil
}

func (s *Store) OpenExact(ctx context.Context, bucket, key string, generation int64) (io.ReadCloser, error) {
	if generation <= 0 {
		return nil, objectstore.ErrPreconditionFailed
	}
	done, err := s.enter(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	k, err := validateKey(bucket, key)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.lookup(k, generation)
	if err != nil {
		return nil, err
	}
	if err = s.checkBlob(r); err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(s.root, "blobs", r.Blob))
	if err != nil {
		return nil, infra(err)
	}
	return &verifiedReader{file: f, ctx: ctx, hash: sha256.New(), expected: r.SHA256}, nil
}

func (s *Store) DeleteExact(ctx context.Context, bucket, key string, generation int64) error {
	if generation <= 0 {
		return objectstore.ErrPreconditionFailed
	}
	done, err := s.enter(ctx)
	if err != nil {
		return err
	}
	defer done()
	k, err := validateKey(bucket, key)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var r record
	err = s.db.Update(func(tx *bolt.Tx) error {
		v := tx.Bucket(objects).Get(k)
		if v == nil {
			return objectstore.ErrNotFound
		}
		var err error
		r, err = decode(v)
		if err != nil {
			return err
		}
		if r.Attrs.Generation != generation {
			return objectstore.ErrPreconditionFailed
		}
		r.Deleting = true
		v, err = json.Marshal(r)
		if err != nil {
			return err
		}
		return tx.Bucket(objects).Put(k, v)
	})
	if err != nil {
		return infra(err)
	}
	s.point("delete-recorded")
	return s.finishDelete(k, r)
}

func (s *Store) finishDelete(k []byte, r record) error {
	if err := os.Remove(filepath.Join(s.root, "blobs", r.Blob)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return infra(err)
	}
	if err := syncDir(filepath.Join(s.root, "blobs")); err != nil {
		return infra(err)
	}
	s.point("blob-removed")
	if err := s.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(blobOwners).Delete([]byte(r.Blob)); err != nil {
			return err
		}
		return tx.Bucket(objects).Delete(k)
	}); err != nil {
		return infra(err)
	}
	s.point("delete-committed")
	return nil
}

func (s *Store) List(ctx context.Context, bucket string, request objectstore.ListRequest) (objectstore.Page, error) {
	done, err := s.enter(ctx)
	if err != nil {
		return objectstore.Page{}, err
	}
	defer done()
	if err := validateID(bucket); err != nil {
		return objectstore.Page{}, err
	}
	if request.PageSize <= 0 || request.PageSize > 1000 || len(request.Prefix) > 1024 || len(request.After) > 1024 || strings.ContainsRune(request.Prefix, 0) || strings.ContainsRune(request.After, 0) {
		return objectstore.Page{}, hangar.ErrLimitExceeded
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	page := objectstore.Page{Objects: []objectstore.Attrs{}, Done: true}
	prefix := bucket + "\x00" + request.Prefix
	start := prefix
	if after := bucket + "\x00" + request.After; after > start {
		start = after
	}
	err = s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(objects).Cursor()
		for k, v := c.Seek([]byte(start)); k != nil && strings.HasPrefix(string(k), prefix); k, v = c.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			r, err := decode(v)
			if err != nil {
				return err
			}
			if r.Deleting {
				return objectstore.ErrInfrastructure
			}
			if r.Attrs.Key == request.After && (request.AfterGeneration == 0 || r.Attrs.Generation <= request.AfterGeneration) {
				continue
			}
			if len(page.Objects) == request.PageSize {
				page.Done = false
				break
			}
			if err := s.checkBlob(r); err != nil {
				return err
			}
			page.Objects = append(page.Objects, r.Attrs)
			page.LastKey = r.Attrs.Key
		}
		return nil
	})
	return page, infra(err)
}

func (s *Store) lookup(k []byte, generation int64) (record, error) {
	var r record
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(objects).Get(k)
		if v == nil {
			return objectstore.ErrNotFound
		}
		var err error
		r, err = decode(v)
		if err != nil {
			return err
		}
		if r.Deleting {
			return objectstore.ErrInfrastructure
		}
		if generation > 0 && r.Attrs.Generation != generation {
			return objectstore.ErrNotFound
		}
		return nil
	})
	return r, infra(err)
}

func (s *Store) checkBlob(r record) error {
	info, err := os.Lstat(filepath.Join(s.root, "blobs", r.Blob))
	if err != nil {
		return fmt.Errorf("%w: committed blob unavailable: %v", hangar.ErrCorrupt, err)
	}
	if !info.Mode().IsRegular() || info.Size() != r.Attrs.Size {
		return fmt.Errorf("%w: committed blob changed", hangar.ErrCorrupt)
	}
	return nil
}

func (s *Store) recover() error {
	// Validate both sides before treating an unindexed file as an orphan. A
	// damaged reverse index must not cause recovery to delete committed bytes.
	if err := s.db.View(func(tx *bolt.Tx) error {
		if err := tx.Bucket(objects).ForEach(func(k, v []byte) error {
			r, err := decode(v)
			if err != nil {
				return err
			}
			if !bytes.Equal(tx.Bucket(blobOwners).Get([]byte(r.Blob)), k) {
				return hangar.ErrCorrupt
			}
			return nil
		}); err != nil {
			return err
		}
		return tx.Bucket(blobOwners).ForEach(func(blob, owner []byte) error {
			r, err := decode(tx.Bucket(objects).Get(owner))
			if err != nil {
				return err
			}
			if r.Blob != string(blob) {
				return hangar.ErrCorrupt
			}
			return nil
		})
	}); err != nil {
		return err
	}
	// Free uncommitted uploads before any index write. A full disk must not need
	// a new temporary index just to discover bytes that can safely be removed.
	dir, err := os.Open(filepath.Join(s.root, "blobs"))
	if err != nil {
		return err
	}
	defer dir.Close()
	for {
		entries, readErr := dir.ReadDir(128)
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), "blob-") || !e.Type().IsRegular() {
				return fmt.Errorf("%w: unexpected disk entry", hangar.ErrCorrupt)
			}
			keep := false
			if err := s.db.View(func(tx *bolt.Tx) error {
				owner := tx.Bucket(blobOwners).Get([]byte(e.Name()))
				if owner == nil {
					return nil
				}
				r, err := decode(tx.Bucket(objects).Get(owner))
				if err != nil {
					return err
				}
				if r.Blob != e.Name() {
					return hangar.ErrCorrupt
				}
				keep = true
				return nil
			}); err != nil {
				return err
			}
			if !keep {
				if err := os.Remove(filepath.Join(s.root, "blobs", e.Name())); err != nil {
					return err
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err := syncDir(filepath.Join(s.root, "blobs")); err != nil {
		return err
	}
	// Examine the index with a bounded cursor, closing its read transaction
	// before completing any pending deletion in a write transaction.
	var after []byte
	for {
		var key []byte
		var r record
		err := s.db.View(func(tx *bolt.Tx) error {
			c := tx.Bucket(objects).Cursor()
			k, v := c.First()
			if after != nil {
				k, v = c.Seek(after)
				if bytes.Equal(k, after) {
					k, v = c.Next()
				}
			}
			if k == nil {
				return nil
			}
			key = append([]byte(nil), k...)
			var err error
			r, err = decode(v)
			if err != nil {
				return err
			}
			if !bytes.Equal(tx.Bucket(blobOwners).Get([]byte(r.Blob)), k) {
				return hangar.ErrCorrupt
			}
			return nil
		})
		if err != nil {
			return err
		}
		if key == nil {
			return nil
		}
		if r.Deleting {
			if err := s.finishDelete(key, r); err != nil {
				return err
			}
		} else if err := s.checkBlob(r); err != nil {
			return err
		}
		after = key
	}
}

func decode(v []byte) (record, error) {
	var r record
	if err := json.Unmarshal(v, &r); err != nil {
		return r, fmt.Errorf("%w: invalid disk record", hangar.ErrCorrupt)
	}
	if !strings.HasPrefix(r.Blob, "blob-") || filepath.Base(r.Blob) != r.Blob || r.Attrs.Generation <= 0 || r.Attrs.Metageneration != 1 || r.Attrs.Size < 0 || len(r.SHA256) != 64 {
		return r, hangar.ErrCorrupt
	}
	return r, nil
}
func validateID(id string) error {
	if err := hangar.Scope(id).Validate(); err != nil {
		return fmt.Errorf("invalid disk namespace or identity: %w", err)
	}
	return nil
}
func validateKey(bucket, key string) ([]byte, error) {
	if err := validateID(bucket); err != nil {
		return nil, err
	}
	if len(key) == 0 || len(key) > 1024 || !utf8.ValidString(key) || strings.ContainsAny(key, "\x00\r\n") {
		return nil, hangar.ErrCorrupt
	}
	return []byte(bucket + "\x00" + key), nil
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
func infra(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{objectstore.ErrNotFound, objectstore.ErrPreconditionFailed, objectstore.ErrInfrastructure, hangar.ErrCorrupt, hangar.ErrLimitExceeded, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, known) {
			return err
		}
	}
	return fmt.Errorf("%w: %v", objectstore.ErrInfrastructure, err)
}
func (s *Store) point(name string) {
	if s.checkpoint != nil {
		s.checkpoint(name)
	}
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
