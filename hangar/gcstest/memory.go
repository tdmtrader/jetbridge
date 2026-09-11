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
		nextGeneration: 1725830823000001,
		clock:          time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
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

func (memory *Memory) Object(bucket, key string) objectstore.Handle {
	return &memoryHandle{memory: memory, bucket: bucket, key: key}
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

type memoryHandle struct {
	memory      *Memory
	bucket, key string
	conditions  objectstore.Conditions
	generation  int64
}

func (handle *memoryHandle) If(conditions objectstore.Conditions) objectstore.Handle {
	copied := *handle
	copied.conditions = conditions

	return &copied
}

func (handle *memoryHandle) Generation(generation int64) objectstore.Handle {
	copied := *handle
	copied.generation = generation

	return &copied
}

func (handle *memoryHandle) NewWriter(ctx context.Context) objectstore.Writer {
	return &memoryWriter{ctx: ctx, handle: handle}
}

func (handle *memoryHandle) NewReader(ctx context.Context) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	handle.memory.mu.Lock()
	defer handle.memory.mu.Unlock()

	if handle.memory.faults.Unauthorized {
		return nil, fmt.Errorf("%w: injected", objectstore.ErrUnauthorized)
	}
	if handle.memory.faults.ReadTimeout {
		return nil, context.DeadlineExceeded
	}

	object, err := handle.memory.lookupLocked(handle.bucket, handle.key, handle.generation, handle.conditions)
	if err != nil {
		return nil, err
	}

	body := object.body
	if handle.memory.faults.CorruptBodyBytes {
		body = append([]byte("corrupt"), body...)
	}
	if limit := handle.memory.faults.TruncateReadAfter; limit > 0 && limit < len(body) {
		body = body[:limit]
	}

	return io.NopCloser(bytes.NewReader(body)), nil
}

func (handle *memoryHandle) Attrs(ctx context.Context) (objectstore.Attrs, error) {
	if err := ctx.Err(); err != nil {
		return objectstore.Attrs{}, err
	}
	handle.memory.mu.Lock()
	defer handle.memory.mu.Unlock()

	if handle.memory.faults.Unauthorized {
		return objectstore.Attrs{}, fmt.Errorf("%w: injected", objectstore.ErrUnauthorized)
	}
	if handle.memory.faults.StatTimeout {
		return objectstore.Attrs{}, context.DeadlineExceeded
	}

	object, err := handle.memory.lookupLocked(handle.bucket, handle.key, handle.generation, handle.conditions)
	if err != nil {
		return objectstore.Attrs{}, err
	}

	return attrsOf(object), nil
}

func (handle *memoryHandle) Delete(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	handle.memory.mu.Lock()
	defer handle.memory.mu.Unlock()

	if handle.memory.faults.Unauthorized {
		return fmt.Errorf("%w: injected", objectstore.ErrUnauthorized)
	}
	if handle.memory.faults.DeleteTimeout {
		return context.DeadlineExceeded
	}

	if _, err := handle.memory.lookupLocked(handle.bucket, handle.key, handle.generation, handle.conditions); err != nil {
		return err
	}
	delete(handle.memory.objects[handle.bucket], handle.key)

	if handle.memory.faults.DeleteResponseLost {
		return fmt.Errorf("%w: injected lost delete response", objectstore.ErrInfrastructure)
	}

	return nil
}

// lookupLocked applies the generation pin and the preconditions in the order a
// real store does: absence first, then the exact generation, then
// metageneration.
func (memory *Memory) lookupLocked(bucket, key string, generation int64, conditions objectstore.Conditions) (storedObject, error) {
	object, found := memory.objects[bucket][key]
	if !found {
		return storedObject{}, fmt.Errorf("%w: no object at %s/%s", objectstore.ErrNotFound, bucket, key)
	}
	if generation != 0 && object.generation != generation {
		return storedObject{}, fmt.Errorf("%w: %s/%s is at generation %d, not %d",
			objectstore.ErrNotFound, bucket, key, object.generation, generation)
	}
	if conditions.DoesNotExist {
		return storedObject{}, fmt.Errorf("%w: %s/%s already exists at generation %d",
			objectstore.ErrPreconditionFailed, bucket, key, object.generation)
	}
	if conditions.GenerationMatch != 0 && object.generation != conditions.GenerationMatch {
		return storedObject{}, fmt.Errorf("%w: %s/%s is at generation %d, the precondition names %d",
			objectstore.ErrPreconditionFailed, bucket, key, object.generation, conditions.GenerationMatch)
	}
	if conditions.MetagenerationMatch != 0 && object.metageneration != conditions.MetagenerationMatch {
		return storedObject{}, fmt.Errorf("%w: %s/%s is at metageneration %d, the precondition names %d",
			objectstore.ErrPreconditionFailed, bucket, key, object.metageneration, conditions.MetagenerationMatch)
	}

	return object, nil
}

type memoryWriter struct {
	ctx      context.Context
	handle   *memoryHandle
	buffer   bytes.Buffer
	metadata map[string]string
	attrs    objectstore.Attrs
	aborted  bool
}

func (writer *memoryWriter) Write(content []byte) (int, error) {
	if err := writer.ctx.Err(); err != nil {
		return 0, err
	}

	return writer.buffer.Write(content)
}

func (writer *memoryWriter) SetMetadata(metadata map[string]string) {
	writer.metadata = cloneMetadata(metadata)
}

func (writer *memoryWriter) Attrs() objectstore.Attrs { return writer.attrs }

func (writer *memoryWriter) Abort(cause error) error {
	writer.aborted = true

	return nil
}

func (writer *memoryWriter) Close() error {
	if writer.aborted {
		return nil
	}
	if err := writer.ctx.Err(); err != nil {
		return err
	}

	memory := writer.handle.memory
	memory.mu.Lock()
	defer memory.mu.Unlock()

	if memory.faults.Unauthorized {
		return fmt.Errorf("%w: injected", objectstore.ErrUnauthorized)
	}
	if memory.faults.CreateTimeout {
		return context.DeadlineExceeded
	}

	if writer.handle.conditions.DoesNotExist {
		if existing, found := memory.objects[writer.handle.bucket][writer.handle.key]; found {
			return fmt.Errorf("%w: %s/%s already exists at generation %d",
				objectstore.ErrPreconditionFailed, writer.handle.bucket, writer.handle.key,
				existing.generation)
		}
	}

	writer.attrs = memory.putLocked(writer.handle.bucket, writer.handle.key,
		writer.buffer.Bytes(), writer.metadata)

	if memory.faults.CreateResponseLost {
		// The object is committed and the caller is told it failed. This is
		// the ambiguous upload, and the only honest way out is a stat.
		return fmt.Errorf("%w: injected lost create response", objectstore.ErrInfrastructure)
	}

	return nil
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

var _ objectstore.Client = (*Memory)(nil)
