// Package projection derives query and presentation records from immutable,
// typed snapshots. Snapshots remain canonical; projections are disposable and
// may be rebuilt by reconciliation.
package projection

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

const (
	defaultProjectionTimeout     = time.Minute
	defaultMaxConcurrentTriggers = 4
)

// ErrCorruptSnapshot identifies content/metadata that cannot be the exact
// immutable value declared by its manifest. It is shared by every projector.
var ErrCorruptSnapshot = errors.New("projection: corrupt snapshot content")

// Handler projects one exact snapshot type and discovers canonical snapshots
// whose projection is absent. Candidates must never include inferred or
// compatible versions: registration and dispatch are exact TypeRef matches.
type Handler interface {
	SnapshotType() snapshot.TypeRef
	Project(context.Context, snapshot.SnapshotRef) error
	Candidates(context.Context, int) ([]snapshot.SnapshotRef, error)
}

type ErrorReporter func(context.Context, error)

type RegistryOption func(*Registry)

func WithTimeout(timeout time.Duration) RegistryOption {
	return func(registry *Registry) { registry.timeout = timeout }
}

func WithErrorReporter(reporter ErrorReporter) RegistryOption {
	return func(registry *Registry) { registry.report = reporter }
}

func WithMaxConcurrentTriggers(limit int) RegistryOption {
	return func(registry *Registry) { registry.maxConcurrentTriggers = limit }
}

// Registry is an immutable exact-type dispatcher. Trigger deliberately
// detaches projection from request/build cancellation after the seal commit:
// projection failure cannot roll back or invalidate canonical snapshot bytes.
type Registry struct {
	handlers              map[snapshot.TypeRef]Handler
	ordered               []Handler
	timeout               time.Duration
	report                ErrorReporter
	maxConcurrentTriggers int
	triggerSlots          chan struct{}
	triggerMu             sync.Mutex
	inflight              map[snapshot.SnapshotRef]struct{}
}

func NewRegistry(handlers []Handler, options ...RegistryOption) (*Registry, error) {
	registry := &Registry{
		handlers:              make(map[snapshot.TypeRef]Handler, len(handlers)),
		ordered:               make([]Handler, 0, len(handlers)),
		timeout:               defaultProjectionTimeout,
		report:                func(context.Context, error) {},
		maxConcurrentTriggers: defaultMaxConcurrentTriggers,
		inflight:              make(map[snapshot.SnapshotRef]struct{}),
	}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("projection: registry option is required")
		}
		option(registry)
	}
	if registry.timeout <= 0 {
		return nil, fmt.Errorf("projection: timeout must be positive")
	}
	if registry.maxConcurrentTriggers <= 0 {
		return nil, fmt.Errorf("projection: max concurrent triggers must be positive")
	}
	if registry.report == nil {
		return nil, fmt.Errorf("projection: error reporter is required")
	}
	for _, handler := range handlers {
		if handler == nil {
			return nil, fmt.Errorf("projection: handler is required")
		}
		typeRef := handler.SnapshotType()
		if err := typeRef.Validate(); err != nil {
			return nil, fmt.Errorf("projection: register handler: %w", err)
		}
		if _, exists := registry.handlers[typeRef]; exists {
			return nil, fmt.Errorf("projection: duplicate handler for %q", typeRef)
		}
		registry.handlers[typeRef] = handler
		registry.ordered = append(registry.ordered, handler)
	}
	registry.triggerSlots = make(chan struct{}, registry.maxConcurrentTriggers)
	return registry, nil
}

func (registry *Registry) Project(ctx context.Context, ref snapshot.SnapshotRef) error {
	if registry == nil {
		return fmt.Errorf("projection: registry is required")
	}
	if err := ref.Validate(); err != nil {
		return fmt.Errorf("projection: invalid snapshot reference: %w", err)
	}
	handler, found := registry.handlers[ref.Type]
	if !found {
		return fmt.Errorf("%w: no projector registered for exact type %q", snapshot.ErrUnsupportedType, ref.Type)
	}
	if err := handler.Project(ctx, ref); err != nil {
		return fmt.Errorf("projection: project snapshot %s as %s: %w", ref.ID, ref.Type, err)
	}
	return nil
}

func (registry *Registry) Handles(typeRef snapshot.TypeRef) bool {
	if registry == nil {
		return false
	}
	_, found := registry.handlers[typeRef]
	return found
}

// Trigger starts a best-effort post-commit projection. parent is accepted so
// callers can pass request context for tracing in future, but cancellation and
// values are intentionally not inherited across the durable commit boundary.
func (registry *Registry) Trigger(_ context.Context, ref snapshot.SnapshotRef) {
	if registry == nil {
		return
	}
	registry.triggerMu.Lock()
	if _, found := registry.inflight[ref]; found {
		registry.triggerMu.Unlock()
		return
	}
	select {
	case registry.triggerSlots <- struct{}{}:
		registry.inflight[ref] = struct{}{}
	default:
		registry.triggerMu.Unlock()
		return
	}
	registry.triggerMu.Unlock()
	go func() {
		defer func() {
			registry.triggerMu.Lock()
			delete(registry.inflight, ref)
			<-registry.triggerSlots
			registry.triggerMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), registry.timeout)
		defer cancel()
		if err := registry.Project(ctx, ref); err != nil {
			registry.report(ctx, err)
		}
	}()
}

// Reconcile retries every currently unprojected candidate, continuing after
// individual failures. Successful upserts remove rows from subsequent real
// store queries; failed rows remain eligible for retry.
func (registry *Registry) Reconcile(ctx context.Context, limit int) error {
	if registry == nil {
		return fmt.Errorf("projection: registry is required")
	}
	if limit <= 0 {
		return fmt.Errorf("projection: reconciliation limit must be positive")
	}
	var failures []error
	for _, handler := range registry.ordered {
		candidates, err := handler.Candidates(ctx, limit)
		if err != nil {
			failures = append(failures, fmt.Errorf("projection: list %s candidates: %w", handler.SnapshotType(), err))
			continue
		}
		for _, ref := range candidates {
			if err := ctx.Err(); err != nil {
				return errors.Join(append(failures, err)...)
			}
			if ref.Type != handler.SnapshotType() {
				failures = append(failures, fmt.Errorf("projection: candidate %s type %q does not match handler type %q", ref.ID, ref.Type, handler.SnapshotType()))
				continue
			}
			if err := registry.Project(ctx, ref); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

// ReconcileAsync performs a detached best-effort startup/periodic pass.
func (registry *Registry) ReconcileAsync(parent context.Context, limit int) {
	if registry == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), registry.timeout)
		defer cancel()
		if err := registry.Reconcile(ctx, limit); err != nil {
			registry.report(ctx, err)
		}
	}()
}

// ProjectingCreator decorates the canonical snapshot commit boundary. It does
// not participate in the transaction: only successful delegate results are
// triggered, asynchronously, after durable seal/upload commit.
type ProjectingCreator struct {
	delegate snapshot.SnapshotCreator
	registry *Registry
}

func NewProjectingCreator(delegate snapshot.SnapshotCreator, registry *Registry) (*ProjectingCreator, error) {
	if delegate == nil {
		return nil, fmt.Errorf("projection: snapshot creator is required")
	}
	if registry == nil {
		return nil, fmt.Errorf("projection: registry is required")
	}
	return &ProjectingCreator{delegate: delegate, registry: registry}, nil
}

func (creator *ProjectingCreator) Upload(ctx context.Context, request snapshot.UploadRequest) (snapshot.Snapshot, error) {
	manifest, err := creator.delegate.Upload(ctx, request)
	if err != nil {
		return snapshot.Snapshot{}, err
	}
	ref := snapshot.SnapshotRef{ID: manifest.ID, Type: manifest.Type, Digest: manifest.Digest}
	if creator.registry.Handles(ref.Type) {
		creator.registry.Trigger(ctx, ref)
	}
	return manifest, nil
}

func (creator *ProjectingCreator) Seal(ctx context.Context, request snapshot.SealRequest) (map[string]snapshot.SealedOutput, error) {
	sealed, err := creator.delegate.Seal(ctx, request)
	if err != nil {
		return nil, err
	}
	for _, output := range sealed {
		if creator.registry.Handles(output.Snapshot.Type) {
			creator.registry.Trigger(ctx, output.Snapshot)
		}
	}
	return sealed, nil
}

var _ snapshot.SnapshotCreator = (*ProjectingCreator)(nil)
