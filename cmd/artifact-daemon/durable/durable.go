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
//
// # One storage interface
//
// The tier has no backend of its own. It is a thin wrapper over the same
// hangar/objectstore.Client the output plane uses, with the same two backends
// (hangar/gcs and hangar/disk), against its OWN bucket or disk namespace and
// its OWN client instance. Fail-open versus fail-closed is decided here, in
// how answers are read, and the two never share a read path: the daemon
// refuses to start with the cache namespace equal to the output one
// (ADR-0002).
//
// The wrapper is also the only node-side holder of a delete, and only over the
// cache namespace: the daemon expires its own cache objects by retention class.
package durable

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// Attributes describe a stored object without transferring it.
type Attributes struct {
	// Key is the object's key, as passed to Put.
	Key string

	// Size is the object's length in bytes.
	Size int64

	// Updated is when this object was last written, or the zero time where the
	// backend does not report one.
	//
	// It is the only thing that makes age-based reclaim possible: a bucket
	// lifecycle rule can expire by age without help, but anything JetBridge does
	// itself has to be able to ask how old an object is. Note it is last-WRITE,
	// not last-read -- object stores do not track reads, so no LRU can be built
	// on this.
	Updated time.Time

	// Version identifies one particular write: the object's generation, in
	// decimal. Objects are immutable, so a key's version only changes when the
	// object is expired and created again.
	//
	// It exists so a caller that cares which write it read (v4's snapshot
	// store will) can pin one, without this package taking a position on
	// whether that matters.
	Version string
}

// Store is a flat key/value space holding one opaque blob per key.
//
// Implementations must treat a missing object as an ordinary outcome, not an
// error: Stat and Get report absence through their bool, and Delete of an
// absent key succeeds. An error means the store itself failed -- unreachable,
// denied, malformed -- and every caller in the daemon responds to that by
// carrying on without the store.
type Store interface {
	// Stat reports the object's attributes without transferring the body. It
	// exists so a cache probe costs one metadata round-trip rather than a
	// download the caller discards.
	Stat(ctx context.Context, key string) (Attributes, bool, error)

	// Get opens the object's current generation and reports its attributes,
	// whose Version is the generation being read. A miss is (nil, {}, false,
	// nil).
	//
	// The caller owns the returned ReadCloser and must close it.
	Get(ctx context.Context, key string) (io.ReadCloser, Attributes, bool, error)

	// Put writes the object if the key is absent.
	//
	// Objects are immutable. Keys are content-derived upstream, so a second
	// write of a key carries the same bytes as the first, and finding the key
	// already present is success, not an error.
	Put(ctx context.Context, key string, body io.Reader) error

	// Delete removes the object. Deleting an absent key is not an error, so a
	// reclaim pass can be re-run without tracking what it already removed.
	Delete(ctx context.Context, key string) error

	// DeleteVersion removes exactly the version (generation) a Get or Stat
	// reported, and never a newer object at the same key. Absent, or already
	// replaced, is not an error.
	DeleteVersion(ctx context.Context, key, version string) error

	// List calls fn for every object in the store, in unspecified order,
	// stopping early if fn returns an error and returning it.
	//
	// Reclaim needs this. Storage is the only authority on what storage
	// holds: a database can be restored, rebuilt or diverge, and anything
	// reconciling a bucket against one has to be able to enumerate the bucket
	// rather than infer its contents. A store without List can only ever
	// delete objects something already remembered, which is precisely the set
	// that does not leak.
	List(ctx context.Context, fn func(Attributes) error) error
}

// ErrTooLarge is returned by Put when the body exceeds the configured limit.
// It is a real error rather than a silent truncation: a half-stored artifact
// that later restores as a valid-looking short tar is worse than no artifact,
// because the build consuming it fails somewhere further away.
var ErrTooLarge = errors.New("durable: object exceeds size limit")

// segmentPattern is deliberately narrow. A segment reaches an object name in
// the cache bucket or disk namespace, and the daemon also joins a restore's
// local alias onto its steps/ root with the same validator, so anything outside
// this shape is a bug upstream rather than a case to encode.
var segmentPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$`)

// maxKeySegments: a key is one segment, or a retention class and one segment.
const maxKeySegments = 2

// ValidateKey rejects keys that are not a cache object name.
//
// A key is either one segment, or a retention-class prefix and one segment:
//
//	rc-<sha256>
//	resource-caches/rc-<sha256>
//
// The prefix exists so an object lifecycle rule can expire whole classes of
// artifact at different ages -- a task cache in days, a review perhaps never --
// without the store, the daemon, or this function knowing what any class means.
// It stays a KEY PREFIX rather than metadata because GCS lifecycle rules match
// prefixes. Each segment is validated identically; the slash is structure, not
// content.
//
// Exactly one slash is allowed, because a lifecycle rule matches a prefix, and
// a hierarchy invites rules that overlap in ways nobody can predict from
// reading them.
//
// What this does NOT do any more is keep the cache away from exact trees by
// the depth of a name. That separation is now the namespace: the cache is its
// own bucket (GCS) or disk namespace, and the daemon refuses to start with it
// equal to the input or output one (objectstore.Namespaces).
func ValidateKey(key string) error {
	segments := strings.Split(key, "/")
	if len(segments) > maxKeySegments {
		return fmt.Errorf("durable: invalid key %q: at most one prefix segment", key)
	}

	for _, segment := range segments {
		if !segmentPattern.MatchString(segment) {
			return fmt.Errorf("durable: invalid key %q", key)
		}
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
