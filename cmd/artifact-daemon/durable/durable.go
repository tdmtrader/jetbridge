// Package durable is a long-term home for artifacts whose node-local copy is
// allowed to disappear.
//
// The artifact daemon stores everything on a node's hostPath and sweeps it at
// a TTL. That is right for step outputs, which are addressed by per-build
// handles nobody will ever ask for again. It is wrong for resource caches,
// which are addressed by a stable identity (rc-<id>, derived from the resource
// type, version and params) that recurs on every build — losing one costs a
// re-download of something that has not changed.
//
// So this package gives the daemon a second tier: node-independent, no TTL,
// holding only the artifacts whose key means something tomorrow.
//
// # Fail-open
//
// Every operation here is allowed to fail, and no failure may reach a build.
// A CI artifact is derivable by definition — re-run the step that produced it —
// so a miss, a timeout, an expired credential or a corrupt object must all
// degrade to "not here", never to an error the daemon propagates.
//
// This is the one property to preserve through any future change. The
// predecessor of this package (v3's agent/hangar) chose the opposite, because
// it held LLM workspace bytes that had no upstream and genuinely could not be
// recreated: it verified an exact metadata vocabulary, refused objects it could
// not prove, and the daemon turned any non-miss error into a 502. Applied to
// derivable bytes that same strictness converts a free cache miss into a broken
// build.
package durable

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
)

// Store is a flat key/value space holding one opaque blob per key.
//
// Implementations must treat a missing object as an ordinary outcome, not an
// error: Get and Has report absence through their bool, and Delete of an absent
// key succeeds. An error means the store itself failed — unreachable, denied,
// malformed — and every caller in the daemon responds to that by carrying on
// without the store.
type Store interface {
	// Has reports whether the key is present, without transferring the body.
	// It exists so a cache probe costs one metadata round-trip rather than a
	// download the caller discards.
	Has(ctx context.Context, key string) (bool, error)

	// Get opens the object. A miss is (nil, false, nil).
	//
	// The caller owns the returned ReadCloser and must close it.
	Get(ctx context.Context, key string) (io.ReadCloser, bool, error)

	// Put writes the object, replacing any previous copy of the same key.
	//
	// Keys are content-derived upstream, so a rewrite carries the same bytes
	// as the copy it replaces; implementations need not make Put atomic
	// against a concurrent Get of the same key.
	Put(ctx context.Context, key string, body io.Reader) error

	// Delete removes the object. Deleting an absent key is not an error, so a
	// reclaim pass can be re-run without tracking what it already removed.
	Delete(ctx context.Context, key string) error
}

// ErrTooLarge is returned by Put when the body exceeds the configured limit.
// It is a real error rather than a silent truncation: a half-stored artifact
// that later restores as a valid-looking short tar is worse than no artifact,
// because the build consuming it fails somewhere further away.
var ErrTooLarge = errors.New("durable: object exceeds size limit")

// keyPattern is deliberately narrow. Keys reach a filesystem path in the fs
// backend and an object name in S3, and the daemon's callers only ever produce
// resource-cache keys, so anything outside this shape is a bug upstream rather
// than a case to encode.
var keyPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$`)

// ValidateKey rejects keys that could escape the store's namespace.
//
// The fs backend joins the key onto a root directory, so "../" would write
// outside it; S3 tolerates far more but a key with a slash silently becomes a
// prefix, which turns Delete into a no-op and hides the object from a reclaim
// pass.
func ValidateKey(key string) error {
	if !keyPattern.MatchString(key) {
		return fmt.Errorf("durable: invalid key %q", key)
	}

	return nil
}

// limitedReader fails at the limit instead of truncating at it.
type limitedReader struct {
	r    io.Reader
	left int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.left <= 0 {
		return 0, ErrTooLarge
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)

	return n, err
}

// LimitReader caps how much will be read from body, reporting ErrTooLarge
// rather than a short read when the cap is hit. A limit of zero or less is
// unbounded.
func LimitReader(body io.Reader, limit int64) io.Reader {
	if limit <= 0 {
		return body
	}

	// +1 so hitting exactly the limit still reads the byte that proves the
	// body was longer.
	return &limitedReader{r: body, left: limit + 1}
}
