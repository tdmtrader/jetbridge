package publisher

import (
	"context"
	"fmt"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

type WorkItemOperation struct {
	OperationKey          string
	Input                 snapshot.SnapshotRef
	Destination           string
	Mode                  Mode
	Parameters            map[string]string
	ApprovalPolicyVersion string
	CanonicalArchivePath  string
	Authority             Authority
}

type WorkItemResult struct {
	ExternalID string
	URL        string
}

type WorkItemBackend interface {
	// Lookup recovers a provider-side idempotent operation after a process
	// crash between the external write and Store.Complete.
	Lookup(context.Context, Credential, string) (WorkItemResult, bool, error)
	Publish(context.Context, Credential, WorkItemOperation) (WorkItemResult, error)
}

// SnapshotValue is an exact, re-hashed materialization of the snapshot a
// work-item publication consumes. The provider adapter receives its canonical
// archive rather than treating the snapshot reference as decorative causality.
type SnapshotValue struct {
	CanonicalArchivePath string
	close                func() error
}

func (value SnapshotValue) Validate() error {
	if value.CanonicalArchivePath == "" {
		return fmt.Errorf("publisher: snapshot value archive is required")
	}
	return nil
}

func (value SnapshotValue) Close() error {
	if value.close == nil {
		return nil
	}
	return value.close()
}

type SnapshotValueInspector interface {
	InspectValue(context.Context, Request) (SnapshotValue, error)
}

type WorkItemService struct {
	store       Store
	credentials CredentialProvider
	values      SnapshotValueInspector
	backend     WorkItemBackend
	timeout     time.Duration
	lease       time.Duration
	actions     ActionsModeReader
}

func NewWorkItemService(
	store Store,
	credentials CredentialProvider,
	values SnapshotValueInspector,
	backend WorkItemBackend,
	timeout time.Duration,
	lease time.Duration,
	options ...ServiceOption,
) (*WorkItemService, error) {
	if nilInterface(store) || nilInterface(credentials) || nilInterface(values) || nilInterface(backend) {
		return nil, fmt.Errorf("publisher: work-item store, credentials, snapshot inspector, and backend are required")
	}
	if timeout <= 0 || timeout > time.Hour || lease <= 0 || lease > 24*time.Hour {
		return nil, fmt.Errorf("publisher: work-item timeout and lease are invalid")
	}
	return &WorkItemService{
		store: store, credentials: credentials, values: values, backend: backend,
		timeout: timeout, lease: lease, actions: buildServiceOptions(options).actions,
	}, nil
}

func (service *WorkItemService) Execute(ctx context.Context, request Request) (Publication, error) {
	if ctx == nil {
		return Publication{}, fmt.Errorf("%w: context is required", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return Publication{}, err
	}
	request = request.Clone()
	if err := request.Validate(); err != nil {
		return Publication{}, err
	}
	if request.Publisher != WorkItemPublisher {
		return Publication{}, fmt.Errorf("%w: work-item service requires %s", ErrInvalidRequest, WorkItemPublisher)
	}
	publication, execute, err := acquireForExecution(ctx, service.store, request, service.lease)
	if err != nil || !execute {
		return publication, err
	}
	if err := publication.Request.ValidatePersisted(); err != nil {
		return Publication{}, fmt.Errorf("publisher: acquired publication authority is invalid: %w", err)
	}
	// The action switch is checked AFTER the durable intent is acquired and
	// BEFORE any external interaction (including the recovery Lookup): the
	// operation row stays pending, so this exact semantic operation is retried
	// unchanged — and executed exactly once — after an admin resumes actions.
	if err := checkActionsAdmitted(service.actions); err != nil {
		return Publication{}, err
	}
	externalContext, cancel := context.WithTimeout(ctx, service.timeout)
	defer cancel()
	authorizedRequest := publication.Request.Clone()
	credential, err := service.credentials.AuthorizeDestination(externalContext, authorizedRequest)
	if err != nil {
		return Publication{}, preserveExternalError(externalContext, "resolve work-item credential", err)
	}
	if err := credential.Validate(); err != nil {
		return Publication{}, err
	}
	value, err := service.values.InspectValue(ctx, authorizedRequest)
	if err != nil {
		return Publication{}, fmt.Errorf("publisher: inspect work-item snapshot: %w", err)
	}
	defer func() { _ = value.Close() }()
	if err := value.Validate(); err != nil {
		return Publication{}, err
	}
	prior, found, err := service.backend.Lookup(externalContext, credential, publication.OperationKey)
	if err != nil {
		return Publication{}, preserveExternalError(externalContext, "reconcile work-item publication", err)
	}
	if found {
		return service.store.Complete(ctx, publication.OperationKey, publication.Attempt, Result{
			Status: StatusSucceeded, ExternalID: prior.ExternalID, URL: prior.URL,
		})
	}
	result, err := service.backend.Publish(externalContext, credential, WorkItemOperation{
		OperationKey: publication.OperationKey, Input: authorizedRequest.Input,
		Destination: authorizedRequest.Destination, Mode: authorizedRequest.Mode, Parameters: cloneParameters(authorizedRequest.Parameters),
		ApprovalPolicyVersion: authorizedRequest.ApprovalPolicyVersion,
		CanonicalArchivePath:  value.CanonicalArchivePath, Authority: authorizedRequest.Authority,
	})
	if err != nil {
		return Publication{}, preserveExternalError(externalContext, "publish work-item update", err)
	}
	return service.store.Complete(ctx, publication.OperationKey, publication.Attempt, Result{
		Status: StatusSucceeded, ExternalID: result.ExternalID, URL: result.URL,
	})
}
