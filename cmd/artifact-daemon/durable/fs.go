package durable

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// FS stores objects as files under a root directory.
//
// It exists for two reasons: it is the whole store for a single-node install
// backed by an NFS mount or a PVC, and it lets every test in this package run
// the real code path with no network and no credentials.
type FS struct {
	root  string
	limit int64
}

var _ Store = (*FS)(nil)

// NewFS returns a store rooted at dir, creating it if needed.
//
// limit bounds a single object in bytes; zero or less is unbounded.
func NewFS(dir string, limit int64) (*FS, error) {
	if dir == "" {
		return nil, errors.New("durable: fs root is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("durable: create fs root: %w", err)
	}

	return &FS{root: dir, limit: limit}, nil
}

func (f *FS) path(key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}

	return filepath.Join(f.root, key), nil
}

// Stat reports the object file's attributes.
//
// There is no Version: a file has no generation, so a caller that needs to pin
// one write cannot do it on this backend.
func (f *FS) Stat(_ context.Context, key string) (Attributes, bool, error) {
	path, err := f.path(key)
	if err != nil {
		return Attributes{}, false, err
	}

	info, err := os.Stat(path)
	switch {
	case err == nil:
		return Attributes{Key: key, Size: info.Size()}, true, nil
	case os.IsNotExist(err):
		return Attributes{}, false, nil
	default:
		return Attributes{}, false, fmt.Errorf("durable: stat %s: %w", key, err)
	}
}

// Get opens the object file. A missing file is a miss, not an error.
func (f *FS) Get(_ context.Context, key string) (io.ReadCloser, bool, error) {
	path, err := f.path(key)
	if err != nil {
		return nil, false, err
	}

	file, err := os.Open(path)
	switch {
	case err == nil:
		return file, true, nil
	case os.IsNotExist(err):
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("durable: open %s: %w", key, err)
	}
}

// Put writes the object through a temporary file and renames it into place, so
// a crashed or over-limit upload never leaves a short file that a later Get
// would serve as if it were whole.
func (f *FS) Put(_ context.Context, key string, body io.Reader) error {
	path, err := f.path(key)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(f.root, ".put-*")
	if err != nil {
		return fmt.Errorf("durable: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	if _, err := io.Copy(tmp, LimitReader(body, f.limit)); err != nil {
		return fmt.Errorf("durable: write %s: %w", key, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("durable: close %s: %w", key, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("durable: rename %s: %w", key, err)
	}

	return nil
}

// Delete removes the object file. An absent file is not an error.
func (f *FS) Delete(_ context.Context, key string) error {
	path, err := f.path(key)
	if err != nil {
		return err
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("durable: remove %s: %w", key, err)
	}

	return nil
}

// List walks the root directory.
//
// Entries that are not objects are skipped rather than reported: the temporary
// files Put writes are siblings of the objects, and a reclaim pass that treated
// one as an orphan would delete an upload in flight.
func (f *FS) List(_ context.Context, fn func(Attributes) error) error {
	entries, err := os.ReadDir(f.root)
	if err != nil {
		return fmt.Errorf("durable: list: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || ValidateKey(entry.Name()) != nil {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			if os.IsNotExist(err) {
				// Swept between ReadDir and Info; it is simply gone.
				continue
			}

			return fmt.Errorf("durable: list %s: %w", entry.Name(), err)
		}

		if err := fn(Attributes{Key: entry.Name(), Size: info.Size()}); err != nil {
			return err
		}
	}

	return nil
}
