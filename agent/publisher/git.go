package publisher

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

// Credential is an opaque, destination-scoped authorization handle. Concrete
// backends may resolve it to an in-memory token or mounted secret; it is never
// part of the publication request or operation key.
type Credential struct {
	Reference string

	// bearerToken is process-local gateway state. It is deliberately
	// unexported so it cannot be authored, serialized, or audited with a
	// publication request.
	bearerToken string
}

func (credential Credential) Validate() error {
	if !boundedText(credential.Reference, 4096, false) {
		return fmt.Errorf("publisher: destination credential is unavailable")
	}
	return nil
}

type CredentialProvider interface {
	// AuthorizeDestination resolves a credential only after applying the
	// deployment's team/run/destination policy to the complete trusted
	// publication request. Returning a zero credential must fail closed.
	AuthorizeDestination(context.Context, Request) (Credential, error)
}

type RepositoryChange struct {
	BaseSHA              string
	ResultSHA            string
	MaterializedRoot     string
	CanonicalArchivePath string
	close                func() error
}

// Close releases process-local materialization owned by a ChangeInspector.
// It is safe for implementations that return no cleanup function.
func (change RepositoryChange) Close() error {
	if change.close == nil {
		return nil
	}
	return change.close()
}

func (change RepositoryChange) Validate() error {
	for name, value := range map[string]string{
		"base SHA": change.BaseSHA, "result SHA": change.ResultSHA, "materialized root": change.MaterializedRoot,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("publisher: repository change %s is required", name)
		}
	}
	return nil
}

type ChangeInspector interface {
	Inspect(context.Context, Request) (RepositoryChange, error)
}

type GitOperation struct {
	OperationKey          string
	Input                 snapshot.SnapshotRef
	Destination           string
	Mode                  Mode
	Parameters            map[string]string
	ApprovalPolicyVersion string
	ApprovedBy            string
	Approval              *ApprovalEvidence
	BaseSHA               string
	ResultSHA             string
	MaterializedRoot      string
	CanonicalArchivePath  string
	Authority             Authority
}

type GitResult struct {
	ExternalID string
	URL        string
	HeadSHA    string
}

type GitBackend interface {
	// Lookup returns the durable provider-side result for an operation key.
	// Implementations must make Publish and Lookup one idempotent operation:
	// after Publish may have reached the provider, Lookup must recover the
	// result before a lease reclaim can attempt the side effect again.
	Lookup(context.Context, Credential, string) (GitResult, bool, error)
	CurrentBase(context.Context, Credential, string, string) (string, error)
	Publish(context.Context, Credential, GitOperation) (GitResult, error)
}

type GitService struct {
	store       Store
	credentials CredentialProvider
	changes     ChangeInspector
	backend     GitBackend
	timeout     time.Duration
	lease       time.Duration
	actions     ActionsModeReader
}

func NewGitService(
	store Store,
	credentials CredentialProvider,
	changes ChangeInspector,
	backend GitBackend,
	timeout time.Duration,
	lease time.Duration,
	options ...ServiceOption,
) (*GitService, error) {
	if nilInterface(store) || nilInterface(credentials) || nilInterface(changes) || nilInterface(backend) {
		return nil, fmt.Errorf("publisher: git store, credentials, change inspector, and backend are required")
	}
	if timeout <= 0 || timeout > time.Hour || lease <= 0 || lease > 24*time.Hour {
		return nil, fmt.Errorf("publisher: git timeout and lease are invalid")
	}
	return &GitService{
		store: store, credentials: credentials, changes: changes, backend: backend,
		timeout: timeout, lease: lease, actions: buildServiceOptions(options).actions,
	}, nil
}

func (service *GitService) Execute(ctx context.Context, request Request) (Publication, error) {
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
	if request.Publisher != GitPublisher {
		return Publication{}, fmt.Errorf("%w: git service requires %s", ErrInvalidRequest, GitPublisher)
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
	authorizedRequest := publication.Request.Clone()
	externalContext, cancel := context.WithTimeout(ctx, service.timeout)
	defer cancel()
	credential, err := service.credentials.AuthorizeDestination(externalContext, authorizedRequest)
	if err != nil {
		return Publication{}, preserveExternalError(externalContext, "resolve destination credential", err)
	}
	if err := credential.Validate(); err != nil {
		return Publication{}, err
	}
	// Reopen and re-hash the exact persisted snapshot before trusting either a
	// recovered provider result or a new side effect. This keeps reconciliation
	// bound to the value the workflow actually produced.
	change, err := service.changes.Inspect(ctx, authorizedRequest)
	if err != nil {
		return Publication{}, fmt.Errorf("publisher: inspect repository change: %w", err)
	}
	defer func() { _ = change.Close() }()
	if err := change.Validate(); err != nil {
		return Publication{}, err
	}
	// The base assertion is server-derived from the same snapshot this
	// inspection just re-read (see MergeBaseParameter), so a mismatch here means
	// the two views of one value disagree. It is a corruption assertion, NOT the
	// stale-base gate: that is the CurrentBase comparison below, which is the
	// only check that can see a destination that moved after approval.
	if asserted := authorizedRequest.Parameters[MergeBaseParameter]; asserted != "" && asserted != change.BaseSHA {
		return Publication{}, fmt.Errorf("%w: %s conflicts with repository-change/v1", ErrInvalidRequest, MergeBaseParameter)
	}
	prior, found, err := service.backend.Lookup(externalContext, credential, publication.OperationKey)
	if err != nil {
		return Publication{}, preserveExternalError(externalContext, "reconcile repository publication", err)
	}
	if found {
		if prior.HeadSHA != change.ResultSHA {
			return Publication{}, fmt.Errorf("publisher: reconciled Git result does not match repository-change/v1 result_sha")
		}
		return service.store.Complete(ctx, publication.OperationKey, publication.Attempt, Result{
			Status: StatusSucceeded, ExternalID: prior.ExternalID, URL: prior.URL, HeadSHA: prior.HeadSHA,
		})
	}
	targetBranch := authorizedRequest.Parameters["target_branch"]
	currentBase, err := service.backend.CurrentBase(externalContext, credential, authorizedRequest.Destination, targetBranch)
	if err != nil {
		return Publication{}, preserveExternalError(externalContext, "read destination base", err)
	}
	if currentBase != change.BaseSHA {
		status := StatusRebaseRequired
		detail := fmt.Sprintf("destination base changed from %s to %s; rebase the repository change before publication", change.BaseSHA, currentBase)
		if authorizedRequest.Mode == ModeMerge {
			status = StatusStaleBase
			detail = fmt.Sprintf("merge approval was for base %s but destination is now %s", change.BaseSHA, currentBase)
		}
		return service.store.Complete(ctx, publication.OperationKey, publication.Attempt, Result{
			Status: status, BaseSHA: currentBase, HeadSHA: change.ResultSHA, Detail: detail,
		})
	}
	gitResult, err := service.backend.Publish(externalContext, credential, GitOperation{
		OperationKey: publication.OperationKey, Input: authorizedRequest.Input,
		Destination: authorizedRequest.Destination, Mode: authorizedRequest.Mode, Parameters: cloneParameters(authorizedRequest.Parameters),
		ApprovalPolicyVersion: authorizedRequest.ApprovalPolicyVersion,
		ApprovedBy:            authorizedRequest.ApprovedBy, Approval: cloneApproval(authorizedRequest.Approval),
		BaseSHA: change.BaseSHA, ResultSHA: change.ResultSHA,
		MaterializedRoot: change.MaterializedRoot, CanonicalArchivePath: change.CanonicalArchivePath,
		Authority: authorizedRequest.Authority,
	})
	if err != nil {
		return Publication{}, preserveExternalError(externalContext, "publish repository change", err)
	}
	if gitResult.HeadSHA != change.ResultSHA {
		return Publication{}, fmt.Errorf("publisher: Git result does not match repository-change/v1 result_sha")
	}
	return service.store.Complete(ctx, publication.OperationKey, publication.Attempt, Result{
		Status: StatusSucceeded, ExternalID: gitResult.ExternalID, URL: gitResult.URL,
		HeadSHA: gitResult.HeadSHA, BaseSHA: currentBase,
	})
}

func preserveExternalError(ctx context.Context, operation string, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return errors.Join(contextErr, fmt.Errorf("publisher: %s failed", operation))
	}
	return fmt.Errorf("publisher: %s: %w", operation, err)
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
