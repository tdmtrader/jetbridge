// Package publishertest provides the in-memory publication store the
// publisher tests run against. It lives outside the production package so no
// test double is compiled into the web binary.
package publishertest

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
)

// operationKeyPattern mirrors the durable store's operation-key shape check.
var operationKeyPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// terminalStatus mirrors publisher.Status.terminal, which is unexported.
func terminalStatus(status publisher.Status) bool {
	switch status {
	case publisher.StatusSucceeded, publisher.StatusFailed, publisher.StatusStaleBase, publisher.StatusRebaseRequired:
		return true
	default:
		return false
	}
}

type MemoryStore struct {
	mu           sync.Mutex
	now          func() time.Time
	nextID       snapshot.DatabaseID
	publications map[string]*memoryPublicationOperation
}

type memoryPublicationOccurrenceKey struct {
	runID   snapshot.WorkflowRunID
	buildID int64
}

type memoryPublicationOccurrence struct {
	id        snapshot.DatabaseID
	request   publisher.Request
	createdAt time.Time
	updatedAt time.Time
}

type memoryPublicationOperation struct {
	operationKey string
	status       publisher.Status
	attempt      int
	leaseUntil   time.Time
	result       publisher.Result
	createdAt    time.Time
	updatedAt    time.Time
	leaseOwner   memoryPublicationOccurrenceKey
	occurrences  map[memoryPublicationOccurrenceKey]memoryPublicationOccurrence
}

func NewMemoryStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{now: now, publications: make(map[string]*memoryPublicationOperation)}
}

func (store *MemoryStore) Acquire(
	ctx context.Context,
	request publisher.Request,
	lease time.Duration,
) (publisher.Publication, bool, error) {
	if ctx == nil {
		return publisher.Publication{}, false, fmt.Errorf("%w: context is required", publisher.ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return publisher.Publication{}, false, err
	}
	request = request.Clone()
	if err := request.ValidatePersisted(); err != nil {
		return publisher.Publication{}, false, err
	}
	key, err := request.OperationKey()
	if err != nil {
		return publisher.Publication{}, false, err
	}
	if lease <= 0 || lease > 24*time.Hour {
		return publisher.Publication{}, false, fmt.Errorf("%w: lease must be within 0-24h", publisher.ErrInvalidRequest)
	}
	now := store.now().UTC()
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return publisher.Publication{}, false, err
	}
	operation, found := store.publications[key]
	if found {
		occurrenceKey := memoryPublicationOccurrenceKey{
			runID: request.Authority.WorkflowRunID, buildID: request.Authority.BuildID,
		}
		occurrence, occurrenceFound := operation.occurrences[occurrenceKey]
		if !occurrenceFound {
			store.nextID++
			occurrence = memoryPublicationOccurrence{
				id: store.nextID, request: request.Clone(), createdAt: now, updatedAt: now,
			}
			operation.occurrences[occurrenceKey] = occurrence
		} else if occurrence.request.Authority != request.Authority {
			return publisher.Publication{}, false, publisher.ErrOperationConflict
		}
		if terminalStatus(operation.status) || now.Before(operation.leaseUntil) {
			return projectMemoryPublication(operation, occurrence), false, nil
		}
		operation.attempt++
		operation.leaseUntil = now.Add(lease)
		operation.updatedAt = now
		operation.leaseOwner = occurrenceKey
		return projectMemoryPublication(operation, occurrence), true, nil
	}
	store.nextID++
	occurrenceKey := memoryPublicationOccurrenceKey{
		runID: request.Authority.WorkflowRunID, buildID: request.Authority.BuildID,
	}
	occurrence := memoryPublicationOccurrence{
		id: store.nextID, request: request.Clone(), createdAt: now, updatedAt: now,
	}
	operation = &memoryPublicationOperation{
		operationKey: key, status: publisher.StatusPending, attempt: 1,
		leaseUntil: now.Add(lease), createdAt: now, updatedAt: now,
		leaseOwner: occurrenceKey,
		occurrences: map[memoryPublicationOccurrenceKey]memoryPublicationOccurrence{
			occurrenceKey: occurrence,
		},
	}
	store.publications[key] = operation
	return projectMemoryPublication(operation, occurrence), true, nil
}

func (store *MemoryStore) Complete(
	ctx context.Context,
	operationKey string,
	attempt int,
	result publisher.Result,
) (publisher.Publication, error) {
	if ctx == nil {
		return publisher.Publication{}, fmt.Errorf("%w: context is required", publisher.ErrInvalidResult)
	}
	if err := ctx.Err(); err != nil {
		return publisher.Publication{}, err
	}
	if !operationKeyPattern.MatchString(operationKey) || attempt <= 0 {
		return publisher.Publication{}, publisher.ErrInvalidResult
	}
	if err := result.Validate(); err != nil {
		return publisher.Publication{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	operation, found := store.publications[operationKey]
	if !found {
		return publisher.Publication{}, publisher.ErrOperationNotFound
	}
	owner, ownerFound := operation.occurrences[operation.leaseOwner]
	if !ownerFound {
		return publisher.Publication{}, publisher.ErrOperationConflict
	}
	if terminalStatus(operation.status) {
		if operation.attempt == attempt && reflect.DeepEqual(operation.result, result) {
			return projectMemoryPublication(operation, owner), nil
		}
		return publisher.Publication{}, publisher.ErrOperationConflict
	}
	if operation.attempt != attempt {
		return publisher.Publication{}, publisher.ErrOperationConflict
	}
	operation.status = result.Status
	operation.result = result
	operation.leaseUntil = time.Time{}
	operation.updatedAt = store.now().UTC()
	return projectMemoryPublication(operation, owner), nil
}

func (store *MemoryStore) Get(ctx context.Context, operationKey string) (publisher.Publication, bool, error) {
	if ctx == nil {
		return publisher.Publication{}, false, fmt.Errorf("%w: context is required", publisher.ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return publisher.Publication{}, false, err
	}
	if !operationKeyPattern.MatchString(operationKey) {
		return publisher.Publication{}, false, publisher.ErrInvalidRequest
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	operation, found := store.publications[operationKey]
	if !found {
		return publisher.Publication{}, false, nil
	}
	owner, found := operation.occurrences[operation.leaseOwner]
	if !found {
		return publisher.Publication{}, false, publisher.ErrOperationConflict
	}
	return projectMemoryPublication(operation, owner), true, nil
}

func projectMemoryPublication(
	operation *memoryPublicationOperation,
	occurrence memoryPublicationOccurrence,
) publisher.Publication {
	updatedAt := operation.updatedAt
	if occurrence.updatedAt.After(updatedAt) {
		updatedAt = occurrence.updatedAt
	}
	return publisher.Publication{
		ID: occurrence.id, OperationKey: operation.operationKey,
		Request: occurrence.request.Clone(), Status: operation.status, Attempt: operation.attempt,
		LeaseUntil: operation.leaseUntil, Result: operation.result,
		CreatedAt: occurrence.createdAt, UpdatedAt: updatedAt,
	}
}
