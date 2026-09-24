// Package gcstest is tier 1 of the output plane's storage substrate: one
// in-memory objectstore.Client, shared by the publisher, inventory, reclaimer
// and policy roles, plus the recording wrapper the role-honesty assertions
// read.
//
// It is the fault-injection tier and only that. Timeouts, an upload whose
// response is lost, a truncated body and a cancelled context are the four
// things a server-backed fake cannot produce on demand, and they are the four
// things this exists for. Everything about the *API* -- pagination,
// metageneration, the 404/412/403 split, a body-capable get used as a stat --
// is tier 2's job, against fake-gcs-server, because a fake that answered those
// questions would be a fake asserting itself.
//
// Nothing here is linked by a production binary. It is a package rather than a
// _test.go file because four role packages need the same one, and four copies
// of a fake is four descriptions of one API.
package gcstest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/concourse/concourse/hangar/objectstore"
)

// Memory is the tier-1 client.
type Memory struct {
	mu      sync.Mutex
	objects map[string]map[string]storedObject

	// buckets is which bucket names EXIST, which is a different question from
	// which ones hold an object.
	//
	// It is here because the alternative answered a list against a bucket that
	// was never created with an empty page -- and an empty page from a wrong
	// bucket name is a sweep reporting that the plane is clean. Real GCS
	// answers that list with a bucket 404; this one now does too. Object stat
	// and delete are deliberately NOT modelled on the bucket, because real GCS
	// answers those with an ordinary object 404 and a fake that could tell them
	// apart would be a fake nobody can rely on.
	buckets map[string]bool
	// nextGeneration is monotonic across the whole store, the way a real
	// bucket's generations are: a test that assumed 1, 2, 3 per key would be
	// asserting the fake.
	nextGeneration int64
	clock          time.Time

	// The fault seams. Each is a fact about the next operation of its kind
	// rather than a script, because a scripted fake is a fake with its own
	// state machine to get wrong.
	faults Faults
}

// Faults are the injected failures tier 1 exists to produce.
type Faults struct {
	// CreateResponseLost commits the object and then reports a failure, which
	// is the ambiguous upload: the bytes are there and the caller does not
	// know it. Reconciliation by exact stat is the only way out, and this is
	// how that path is reached.
	CreateResponseLost bool

	// CreateTimeout fails a create with a deadline, without committing.
	CreateTimeout bool

	// StatTimeout, ReadTimeout and DeleteTimeout do the same for the other
	// operations.
	StatTimeout, ReadTimeout, DeleteTimeout bool

	// DeleteResponseLost deletes and then reports a failure: the inferred
	// deletion, which must never be reported as confirmed.
	DeleteResponseLost bool

	// TruncateReadAfter cuts a body short after this many bytes, so a caller
	// that trusted Size rather than the bytes it actually read is caught.
	TruncateReadAfter int

	// CorruptBodyBytes rewrites the stored body under the caller, so a
	// digest check has something to fail on.
	CorruptBodyBytes bool

	// Unauthorized fails every operation with the 403 sentinel.
	Unauthorized bool
}

type storedObject struct {
	key            string
	body           []byte
	metadata       map[string]string
	generation     int64
	metageneration int64
	created        time.Time
}

// NewMemory returns an empty store. The clock is fixed: nothing here is about
// elapsed time, and a wall clock would make a created-at assertion flaky.
func NewMemory() *Memory {
	return &Memory{
		objects:        map[string]map[string]storedObject{},
		buckets:        map[string]bool{},
		nextGeneration: 1725830823000001,
		clock:          time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// CreateBucket makes a bucket name exist without putting anything in it.
//
// A harness calls it for the same reason a deployment creates a bucket before
// it publishes: "the bucket is empty" and "the bucket is not there" are two
// different answers, and a substrate that cannot give the second cannot pin
// what a controller pointed at the wrong bucket sees.
func (memory *Memory) CreateBucket(bucket string) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	memory.buckets[bucket] = true
}

// Inject arms the faults for subsequent operations.
func (memory *Memory) Inject(faults Faults) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	memory.faults = faults
}

// Seed puts an object at a key without going through the create path, which is
// how a collision is set up: the wrong variant has to be there before the
// publisher runs, and a publisher that put it there would be the thing under
// test.
func (memory *Memory) Seed(bucket, key string, body []byte, metadata map[string]string) objectstore.Attrs {
	memory.mu.Lock()
	defer memory.mu.Unlock()

	return memory.putLocked(bucket, key, body, metadata)
}

func (memory *Memory) putLocked(bucket, key string, body []byte, metadata map[string]string) objectstore.Attrs {
	if memory.objects[bucket] == nil {
		memory.objects[bucket] = map[string]storedObject{}
	}
	memory.buckets[bucket] = true
	generation := memory.nextGeneration
	memory.nextGeneration++
	object := storedObject{
		key:            key,
		body:           append([]byte(nil), body...),
		metadata:       cloneMetadata(metadata),
		generation:     generation,
		metageneration: 1,
		created:        memory.clock,
	}
	memory.objects[bucket][key] = object

	return attrsOf(object)
}

// TouchMetadata moves an object's METAGENERATION without moving its generation.
//
// It exists because that state was unreachable in both tiers -- this fake set
// metageneration to 1 at creation and never incremented it, and fake-gcs-server
// reports 1 forever even after a full rewrite -- and it is the state a real
// bucket reaches on any metadata change: a SetStorageClass lifecycle
// transition, Autoclass, an ACL or metadata edit, a hold. None of those is a
// Delete rule, so the bucket still attests safe, and a delete conditioned on a
// stale metageneration 412s against every object in it.
//
// It is not a production seam. Nothing in this plane updates object metadata;
// what this models is somebody ELSE'S benign, spec-permitted change.
func (memory *Memory) TouchMetadata(bucket, key string) {
	memory.mu.Lock()
	defer memory.mu.Unlock()

	object, found := memory.objects[bucket][key]
	if !found {
		return
	}
	object.metageneration++
	memory.objects[bucket][key] = object
}

// Keys is what the bucket holds, sorted. "The bucket holds exactly one object"
// is an outcome read out of the store, never a count of calls.
func (memory *Memory) Keys(bucket string) []string {
	memory.mu.Lock()
	defer memory.mu.Unlock()

	keys := make([]string, 0, len(memory.objects[bucket]))
	for key := range memory.objects[bucket] {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	return keys
}

// Body is the stored bytes at a key, so a dedup assertion can name which bytes
// are there afterwards rather than only how many objects there are.
func (memory *Memory) Body(bucket, key string) ([]byte, bool) {
	memory.mu.Lock()
	defer memory.mu.Unlock()

	object, found := memory.objects[bucket][key]

	return object.body, found
}

func (memory *Memory) List(ctx context.Context, bucket string, request objectstore.ListRequest) (objectstore.Page, error) {
	if err := ctx.Err(); err != nil {
		return objectstore.Page{}, err
	}
	memory.mu.Lock()
	defer memory.mu.Unlock()

	if memory.faults.Unauthorized {
		return objectstore.Page{}, fmt.Errorf("%w: injected", objectstore.ErrUnauthorized)
	}
	if request.PageSize <= 0 {
		return objectstore.Page{}, fmt.Errorf("%w: a list page size must be positive",
			objectstore.ErrInfrastructure)
	}
	if !memory.buckets[bucket] {
		return objectstore.Page{}, fmt.Errorf("%w: %s", objectstore.ErrBucketNotFound, bucket)
	}

	keys := make([]string, 0, len(memory.objects[bucket]))
	for key := range memory.objects[bucket] {
		if strings.HasPrefix(key, request.Prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	// The (key, generation) after-key. A resumed listing drops the key it
	// resumed from ONLY when the object there is no newer than the generation
	// the cursor named: an object recreated at that key since is a new object
	// at an old name, and skipping it would lose it for a whole cycle.
	start := 0
	for start < len(keys) && request.After != "" && keys[start] <= request.After {
		if keys[start] == request.After && request.AfterGeneration != 0 &&
			memory.objects[bucket][keys[start]].generation > request.AfterGeneration {
			break
		}
		start++
	}
	end := start + request.PageSize
	if end > len(keys) {
		end = len(keys)
	}

	page := objectstore.Page{Done: end >= len(keys)}
	for _, key := range keys[start:end] {
		page.Objects = append(page.Objects, attrsOf(memory.objects[bucket][key]))
	}
	if len(page.Objects) > 0 {
		page.LastKey = page.Objects[len(page.Objects)-1].Key
	}

	return page, nil
}

func (memory *Memory) OpenExact(ctx context.Context, bucket, key string, generation int64) (io.ReadCloser, error) {
	if err := objectstore.ValidateGeneration(generation); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if memory.faults.Unauthorized {
		return nil, objectstore.ErrUnauthorized
	}
	if memory.faults.ReadTimeout {
		return nil, context.DeadlineExceeded
	}
	object, err := memory.lookupLocked(bucket, key, generation, false)
	if err != nil {
		return nil, err
	}
	body := object.body
	if memory.faults.CorruptBodyBytes {
		body = append([]byte("corrupt"), body...)
	}
	if limit := memory.faults.TruncateReadAfter; limit > 0 && limit < len(body) {
		body = body[:limit]
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func (memory *Memory) StatCurrent(ctx context.Context, bucket, key string) (objectstore.Attrs, error) {
	return memory.stat(ctx, bucket, key, 0)
}
func (memory *Memory) StatExact(ctx context.Context, bucket, key string, generation int64) (objectstore.Attrs, error) {
	if err := objectstore.ValidateGeneration(generation); err != nil {
		return objectstore.Attrs{}, err
	}
	return memory.stat(ctx, bucket, key, generation)
}
func (memory *Memory) stat(ctx context.Context, bucket, key string, generation int64) (objectstore.Attrs, error) {
	if err := ctx.Err(); err != nil {
		return objectstore.Attrs{}, err
	}
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if memory.faults.Unauthorized {
		return objectstore.Attrs{}, objectstore.ErrUnauthorized
	}
	if memory.faults.StatTimeout {
		return objectstore.Attrs{}, context.DeadlineExceeded
	}
	object, err := memory.lookupLocked(bucket, key, generation, false)
	if err != nil {
		return objectstore.Attrs{}, err
	}
	return attrsOf(object), nil
}
func (memory *Memory) DeleteExact(ctx context.Context, bucket, key string, generation int64) error {
	if err := objectstore.ValidateGeneration(generation); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if memory.faults.Unauthorized {
		return objectstore.ErrUnauthorized
	}
	if memory.faults.DeleteTimeout {
		return context.DeadlineExceeded
	}
	if _, err := memory.lookupLocked(bucket, key, generation, true); err != nil {
		return err
	}
	delete(memory.objects[bucket], key)
	if memory.faults.DeleteResponseLost {
		return fmt.Errorf("%w: injected lost delete response", objectstore.ErrInfrastructure)
	}
	return nil
}

// lookupLocked distinguishes an absent exact read from a refused stale delete.
func (memory *Memory) lookupLocked(bucket, key string, generation int64, deletion bool) (storedObject, error) {
	object, found := memory.objects[bucket][key]
	if !found {
		return storedObject{}, fmt.Errorf("%w: no object at %s/%s", objectstore.ErrNotFound, bucket, key)
	}
	if generation != 0 && object.generation != generation {
		sentinel := objectstore.ErrNotFound
		if deletion {
			sentinel = objectstore.ErrPreconditionFailed
		}
		return storedObject{}, fmt.Errorf("%w: %s/%s is at generation %d, not %d", sentinel, bucket, key, object.generation, generation)
	}

	return object, nil
}

func (memory *Memory) CreateAbsent(ctx context.Context, bucket, key string, metadata map[string]string, body io.Reader) (objectstore.Attrs, error) {
	if err := ctx.Err(); err != nil {
		return objectstore.Attrs{}, err
	}
	content, err := io.ReadAll(body)
	if err != nil {
		return objectstore.Attrs{}, err
	}
	if err := ctx.Err(); err != nil {
		return objectstore.Attrs{}, err
	}
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if memory.faults.Unauthorized {
		return objectstore.Attrs{}, objectstore.ErrUnauthorized
	}
	if memory.faults.CreateTimeout {
		return objectstore.Attrs{}, context.DeadlineExceeded
	}
	if _, found := memory.objects[bucket][key]; found {
		return objectstore.Attrs{}, objectstore.ErrPreconditionFailed
	}
	attrs := memory.putLocked(bucket, key, content, metadata)
	if memory.faults.CreateResponseLost {
		return objectstore.Attrs{}, fmt.Errorf("%w: injected lost create response", objectstore.ErrInfrastructure)
	}
	return attrs, nil
}

func attrsOf(object storedObject) objectstore.Attrs {
	return objectstore.Attrs{
		Key:            object.key,
		Generation:     object.generation,
		Metageneration: object.metageneration,
		Size:           int64(len(object.body)),
		Created:        object.created,
		Metadata:       cloneMetadata(object.metadata),
	}
}

func cloneMetadata(metadata map[string]string) map[string]string {
	if metadata == nil {
		return nil
	}
	copied := make(map[string]string, len(metadata))
	for key, value := range metadata {
		copied[key] = value
	}

	return copied
}

var (
	_ objectstore.Client       = (*Memory)(nil)
	_ objectstore.DeleteClient = (*Memory)(nil)
)
