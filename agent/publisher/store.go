package publisher

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

var (
	ErrInvalidResult     = errors.New("publisher: invalid result")
	ErrOperationNotFound = errors.New("publisher: operation not found")
	ErrOperationConflict = errors.New("publisher: operation conflict")
)

type Status string

const (
	StatusPending        Status = "pending"
	StatusSucceeded      Status = "succeeded"
	StatusFailed         Status = "failed"
	StatusStaleBase      Status = "stale_base"
	StatusRebaseRequired Status = "rebase_required"
)

func (status Status) terminal() bool {
	switch status {
	case StatusSucceeded, StatusFailed, StatusStaleBase, StatusRebaseRequired:
		return true
	default:
		return false
	}
}

type Result struct {
	Status     Status `json:"status"`
	ExternalID string `json:"external_id,omitempty"`
	URL        string `json:"url,omitempty"`
	HeadSHA    string `json:"head_sha,omitempty"`
	BaseSHA    string `json:"base_sha,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

func (result Result) Validate() error {
	if !result.Status.terminal() {
		return fmt.Errorf("%w: status must be terminal", ErrInvalidResult)
	}
	for name, value := range map[string]string{
		"external_id": result.ExternalID, "url": result.URL, "head_sha": result.HeadSHA,
		"base_sha": result.BaseSHA,
	} {
		if value != "" && !boundedText(value, 4096, false) {
			return fmt.Errorf("%w: %s is invalid", ErrInvalidResult, name)
		}
	}
	if len(result.Detail) > 64<<10 || strings.IndexByte(result.Detail, 0) >= 0 {
		return fmt.Errorf("%w: detail is invalid", ErrInvalidResult)
	}
	if result.Status != StatusSucceeded && strings.TrimSpace(result.Detail) == "" {
		return fmt.Errorf("%w: non-success result requires detail", ErrInvalidResult)
	}
	return nil
}

type Publication struct {
	ID           snapshot.DatabaseID `json:"id,omitempty"`
	OperationKey string              `json:"operation_key"`
	Request      Request             `json:"request"`
	Status       Status              `json:"status"`
	Attempt      int                 `json:"attempt"`
	LeaseUntil   time.Time           `json:"lease_until,omitempty"`
	Result       Result              `json:"result,omitempty"`
	CreatedAt    time.Time           `json:"created_at"`
	UpdatedAt    time.Time           `json:"updated_at"`
}

func (publication Publication) Clone() Publication {
	publication.Request = publication.Request.Clone()
	return publication
}

type Store interface {
	Acquire(context.Context, Request, time.Duration) (Publication, bool, error)
	Complete(context.Context, string, int, Result) (Publication, error)
	Get(context.Context, string) (Publication, bool, error)
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
	request   Request
	createdAt time.Time
	updatedAt time.Time
}

type memoryPublicationOperation struct {
	operationKey string
	status       Status
	attempt      int
	leaseUntil   time.Time
	result       Result
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
	request Request,
	lease time.Duration,
) (Publication, bool, error) {
	if ctx == nil {
		return Publication{}, false, fmt.Errorf("%w: context is required", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return Publication{}, false, err
	}
	request = request.Clone()
	if err := request.ValidatePersisted(); err != nil {
		return Publication{}, false, err
	}
	key, err := request.OperationKey()
	if err != nil {
		return Publication{}, false, err
	}
	if lease <= 0 || lease > 24*time.Hour {
		return Publication{}, false, fmt.Errorf("%w: lease must be within 0-24h", ErrInvalidRequest)
	}
	now := store.now().UTC()
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Publication{}, false, err
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
			return Publication{}, false, ErrOperationConflict
		}
		if operation.status.terminal() || now.Before(operation.leaseUntil) {
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
		operationKey: key, status: StatusPending, attempt: 1,
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
	result Result,
) (Publication, error) {
	if ctx == nil {
		return Publication{}, fmt.Errorf("%w: context is required", ErrInvalidResult)
	}
	if err := ctx.Err(); err != nil {
		return Publication{}, err
	}
	if !operationKeyPattern.MatchString(operationKey) || attempt <= 0 {
		return Publication{}, ErrInvalidResult
	}
	if err := result.Validate(); err != nil {
		return Publication{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	operation, found := store.publications[operationKey]
	if !found {
		return Publication{}, ErrOperationNotFound
	}
	owner, ownerFound := operation.occurrences[operation.leaseOwner]
	if !ownerFound {
		return Publication{}, ErrOperationConflict
	}
	if operation.status.terminal() {
		if operation.attempt == attempt && reflect.DeepEqual(operation.result, result) {
			return projectMemoryPublication(operation, owner), nil
		}
		return Publication{}, ErrOperationConflict
	}
	if operation.attempt != attempt {
		return Publication{}, ErrOperationConflict
	}
	operation.status = result.Status
	operation.result = result
	operation.leaseUntil = time.Time{}
	operation.updatedAt = store.now().UTC()
	return projectMemoryPublication(operation, owner), nil
}

func (store *MemoryStore) Get(ctx context.Context, operationKey string) (Publication, bool, error) {
	if ctx == nil {
		return Publication{}, false, fmt.Errorf("%w: context is required", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return Publication{}, false, err
	}
	if !operationKeyPattern.MatchString(operationKey) {
		return Publication{}, false, ErrInvalidRequest
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	operation, found := store.publications[operationKey]
	if !found {
		return Publication{}, false, nil
	}
	owner, found := operation.occurrences[operation.leaseOwner]
	if !found {
		return Publication{}, false, ErrOperationConflict
	}
	return projectMemoryPublication(operation, owner), true, nil
}

func projectMemoryPublication(
	operation *memoryPublicationOperation,
	occurrence memoryPublicationOccurrence,
) Publication {
	updatedAt := operation.updatedAt
	if occurrence.updatedAt.After(updatedAt) {
		updatedAt = occurrence.updatedAt
	}
	return Publication{
		ID: occurrence.id, OperationKey: operation.operationKey,
		Request: occurrence.request.Clone(), Status: operation.status, Attempt: operation.attempt,
		LeaseUntil: operation.leaseUntil, Result: operation.result,
		CreatedAt: occurrence.createdAt, UpdatedAt: updatedAt,
	}
}

var operationKeyPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
