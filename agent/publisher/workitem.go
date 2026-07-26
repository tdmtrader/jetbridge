package publisher

import (
	"context"
	"fmt"
	"net/url"
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

// Validate requires a work-item result to be a usable external identity.
//
// This is the work-item analogue of the Git service's head_sha cross-check
// (git.go, the "reconciled Git result does not match" refusal): it is what a
// service may trust before recording a terminal success. It is deliberately
// weaker, because it can be. A Git result carries independently derived truth
// — the result commit of the exact snapshot being published — so a recovered
// result can be *cross-checked* against content. A work-item result carries
// only an external ID and a URL; the wire response echoes nothing
// request-derived (see gatewayResult in gateway.go), and demanding that a
// provider's external ID or URL embed the destination would couple this
// package to one provider's formatting. So the enforceable invariant is
// identity usability, applied to both a recovered result and a fresh one. A
// violation is a retryable refusal: the operation stays pending, because a
// backend that answers this way once may answer correctly on the next attempt.
//
// Result.Validate (store.go) already rejects malformed non-empty text, but it
// cannot stand in for this gate: it accepts an empty external_id outright and
// says nothing about a URL's scheme, host, or userinfo. This is also why the
// refusal must be raised here rather than left to Store.Complete — the caller
// needs a named identity failure, not an incidental result-encoding failure.
func (result WorkItemResult) Validate() error {
	if !boundedText(result.ExternalID, 4096, false) {
		return fmt.Errorf("publisher: work-item result carries no usable external identity")
	}
	if result.URL != "" {
		parsed, err := url.Parse(result.URL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return fmt.Errorf("publisher: work-item result carries no usable external identity: URL is invalid")
		}
	}
	return nil
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
	actions ActionsModeReader,
	timeout time.Duration,
	lease time.Duration,
) (*WorkItemService, error) {
	if nilInterface(store) || nilInterface(credentials) || nilInterface(values) || nilInterface(backend) {
		return nil, fmt.Errorf("publisher: work-item store, credentials, snapshot inspector, and backend are required")
	}
	if nilInterface(actions) {
		return nil, errActionsReaderRequired
	}
	if timeout <= 0 || timeout > time.Hour || lease <= 0 || lease > 24*time.Hour {
		return nil, fmt.Errorf("publisher: work-item timeout and lease are invalid")
	}
	return &WorkItemService{
		store: store, credentials: credentials, values: values, backend: backend,
		timeout: timeout, lease: lease, actions: actions,
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
		if failed, handled, completeErr := terminalGatewayFailure(
			ctx, service.store, publication.OperationKey, publication.Attempt,
			"reconcile work-item publication", err,
		); handled {
			return failed, completeErr
		}
		return Publication{}, preserveExternalError(externalContext, "reconcile work-item publication", err)
	}
	if found {
		if err := prior.Validate(); err != nil {
			return Publication{}, err
		}
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
		if failed, handled, completeErr := terminalGatewayFailure(
			ctx, service.store, publication.OperationKey, publication.Attempt,
			"publish work-item update", err,
		); handled {
			return failed, completeErr
		}
		return Publication{}, preserveExternalError(externalContext, "publish work-item update", err)
	}
	if err := result.Validate(); err != nil {
		return Publication{}, err
	}
	return service.store.Complete(ctx, publication.OperationKey, publication.Attempt, Result{
		Status: StatusSucceeded, ExternalID: result.ExternalID, URL: result.URL,
	})
}
