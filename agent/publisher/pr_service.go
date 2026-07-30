package publisher

import (
	"context"
	"fmt"
	"time"

	"github.com/concourse/concourse/agent/snapshot/contracts"
)

type BranchMutation struct {
	Locator           PRLocator
	Ref               string
	TargetRef         string
	ExpectedSource    HeadExpectation
	ExpectedTargetSHA string
	NewSourceSHA      string
	OperationKey      string
}

type BranchResult struct {
	HeadSHA string
	Applied bool
}

type CreatePRMutation struct {
	Locator      PRLocator
	SourceRef    string
	SourceSHA    string
	TargetRef    string
	TargetSHA    string
	Title        string
	Body         string
	OperationKey string
}

type ExternalPullRequest struct {
	Locator   PRLocator
	URL       string
	State     contracts.PullRequestState
	SourceRef string
	SourceSHA string
	TargetRef string
	TargetSHA string
}

type StatusMutation struct {
	Locator      PRLocator
	SourceSHA    string
	State        string
	Description  string
	TargetURL    string
	OperationKey string
}

type ResponseMutation struct {
	Locator      PRLocator
	Batch        PRReviewBatch
	Response     contracts.PullRequestResponseBody
	OperationKey string
}

type ExternalResult struct {
	OperationKey string
	ExternalID   string
	URL          string
}

// PRMutator is the migration-independent provider mutation boundary. The
// later ATC composition adapter maps these types to pullrequest.Mutator after
// resolving the exact policy rule and credential; publisher cannot import the
// pullrequest package because that package currently depends on atc.
type PRMutator interface {
	CompareAndSwapBranch(context.Context, BranchMutation) (BranchResult, error)
	FindOrCreatePullRequest(context.Context, CreatePRMutation) (ExternalPullRequest, error)
	PublishValidationStatus(context.Context, StatusMutation) (ExternalResult, error)
	PublishReviewResponse(context.Context, ResponseMutation) (ExternalResult, error)
}

// PRMutatorResolver is the narrow future composition seam for exact provider
// policy and credential authorization. It receives only the acquired,
// persisted action.
type PRMutatorResolver interface {
	ResolvePRMutator(context.Context, PRAction) (PRMutator, error)
}

type PRService interface {
	PublishBranch(context.Context, BranchPublicationRequest) (Publication, error)
	FindOrCreate(context.Context, PullRequestPublicationRequest) (Publication, error)
	PublishStatus(context.Context, StatusPublicationRequest) (Publication, error)
	PublishResponse(context.Context, ResponsePublicationRequest) (Publication, error)
}

type providerPRService struct {
	store    PRStore
	resolver PRMutatorResolver
	timeout  time.Duration
	lease    time.Duration
}

func NewPRService(store PRStore, resolver PRMutatorResolver, timeout, lease time.Duration) (PRService, error) {
	if nilInterface(store) || nilInterface(resolver) {
		return nil, fmt.Errorf("publisher: PR store and mutator resolver are required")
	}
	if timeout <= 0 || timeout > time.Hour || lease <= 0 || lease > 24*time.Hour {
		return nil, fmt.Errorf("publisher: PR timeout and lease are invalid")
	}
	return &providerPRService{store: store, resolver: resolver, timeout: timeout, lease: lease}, nil
}

func (service *providerPRService) PublishBranch(ctx context.Context, request BranchPublicationRequest) (Publication, error) {
	return service.execute(ctx, branchAction(request))
}

func (service *providerPRService) FindOrCreate(ctx context.Context, request PullRequestPublicationRequest) (Publication, error) {
	return service.execute(ctx, pullRequestAction(request))
}

func (service *providerPRService) PublishStatus(ctx context.Context, request StatusPublicationRequest) (Publication, error) {
	return service.execute(ctx, statusAction(request))
}

func (service *providerPRService) PublishResponse(ctx context.Context, request ResponsePublicationRequest) (Publication, error) {
	return service.execute(ctx, responseAction(request))
}

func (service *providerPRService) execute(ctx context.Context, action PRAction) (Publication, error) {
	if ctx == nil {
		return Publication{}, fmt.Errorf("%w: context is required", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return Publication{}, err
	}
	if err := action.Validate(); err != nil {
		return Publication{}, err
	}
	publication, execute, err := acquirePRForExecution(ctx, service.store, action, service.lease)
	if err != nil || !execute {
		return publication, err
	}
	if publication.PRAction == nil || publication.OperationKind != action.Kind {
		return Publication{}, fmt.Errorf("publisher: acquired PR publication authority is invalid")
	}
	authorized := publication.PRAction.Clone()
	if err := authorized.ValidatePersisted(); err != nil {
		return Publication{}, fmt.Errorf("publisher: acquired PR publication authority is invalid: %w", err)
	}
	key, err := authorized.OperationKey()
	if err != nil || key != publication.OperationKey {
		return Publication{}, fmt.Errorf("publisher: acquired PR operation identity is invalid")
	}

	externalContext, cancel := context.WithTimeout(ctx, service.timeout)
	defer cancel()
	mutator, err := service.resolver.ResolvePRMutator(externalContext, authorized)
	if err != nil {
		return Publication{}, preserveExternalError(externalContext, "resolve PR mutator", err)
	}
	if nilInterface(mutator) {
		return Publication{}, fmt.Errorf("publisher: resolved PR mutator is unavailable")
	}

	switch authorized.Kind {
	case OperationPublishPRBranch:
		request := authorized.Branch
		result, err := mutator.CompareAndSwapBranch(externalContext, BranchMutation{
			Locator: request.Locator, Ref: request.SourceRef, TargetRef: request.TargetRef,
			ExpectedSource: request.ExpectedSource, ExpectedTargetSHA: request.ExpectedTargetSHA,
			NewSourceSHA: request.NewSourceSHA, OperationKey: publication.OperationKey,
		})
		if err != nil {
			return Publication{}, preserveExternalError(externalContext, "publish PR branch", err)
		}
		if result.HeadSHA != request.NewSourceSHA {
			return Publication{}, fmt.Errorf("publisher: PR branch result does not match requested source head")
		}
		return service.store.CompletePR(ctx, publication.OperationKey, publication.Attempt, Result{
			Status: StatusSucceeded, ExternalID: request.SourceRef,
			HeadSHA: result.HeadSHA, BaseSHA: request.ExpectedTargetSHA,
		})
	case OperationCreatePR:
		request := authorized.PullRequest
		result, err := mutator.FindOrCreatePullRequest(externalContext, CreatePRMutation{
			Locator: request.Locator, SourceRef: request.SourceRef, SourceSHA: request.SourceSHA,
			TargetRef: request.TargetRef, TargetSHA: request.TargetSHA,
			Title: request.Title, Body: request.Body, OperationKey: publication.OperationKey,
		})
		if err != nil {
			return Publication{}, preserveExternalError(externalContext, "find or create pull request", err)
		}
		if err := validateCreatedPullRequest(*request, result); err != nil {
			return Publication{}, err
		}
		return service.store.CompletePR(ctx, publication.OperationKey, publication.Attempt, Result{
			Status: StatusSucceeded, ExternalID: result.Locator.ExternalID, URL: result.URL,
			HeadSHA: result.SourceSHA, BaseSHA: result.TargetSHA,
		})
	case OperationPublishPRStatus:
		request := authorized.Status
		result, err := mutator.PublishValidationStatus(externalContext, StatusMutation{
			Locator: request.Locator, SourceSHA: request.SourceSHA, State: request.State,
			Description: request.Description, TargetURL: request.TargetURL,
			OperationKey: publication.OperationKey,
		})
		if err != nil {
			return Publication{}, preserveExternalError(externalContext, "publish PR status", err)
		}
		if err := validateExternalResult(publication.OperationKey, result); err != nil {
			return Publication{}, err
		}
		return service.store.CompletePR(ctx, publication.OperationKey, publication.Attempt, Result{
			Status: StatusSucceeded, ExternalID: result.ExternalID, URL: result.URL,
			HeadSHA: request.SourceSHA,
		})
	case OperationRespondToReview:
		request := authorized.Response
		result, err := mutator.PublishReviewResponse(externalContext, ResponseMutation{
			Locator: request.Locator, Batch: request.Batch, Response: request.Response,
			OperationKey: publication.OperationKey,
		})
		if err != nil {
			return Publication{}, preserveExternalError(externalContext, "publish review response", err)
		}
		if err := validateExternalResult(publication.OperationKey, result); err != nil {
			return Publication{}, err
		}
		return service.store.CompletePR(ctx, publication.OperationKey, publication.Attempt, Result{
			Status: StatusSucceeded, ExternalID: result.ExternalID, URL: result.URL,
			HeadSHA: request.Batch.CommitSHA,
		})
	default:
		return Publication{}, fmt.Errorf("%w: PR operation kind is invalid", ErrInvalidRequest)
	}
}

func acquirePRForExecution(ctx context.Context, store PRStore, action PRAction, lease time.Duration) (Publication, bool, error) {
	for {
		publication, execute, err := store.AcquirePR(ctx, action, lease)
		if err != nil || execute || publication.Status.terminal() {
			return publication, execute, err
		}
		if publication.Status != StatusPending || publication.LeaseUntil.IsZero() {
			return Publication{}, false, fmt.Errorf("%w: pending PR publication lease is invalid", ErrInvalidRequest)
		}
		wait := publicationAcquirePollInterval
		untilLeaseExpiry := time.Until(publication.LeaseUntil)
		if untilLeaseExpiry < wait {
			wait = untilLeaseExpiry
		}
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return Publication{}, false, ctx.Err()
		case <-timer.C:
		}
	}
}

func validateCreatedPullRequest(request PullRequestPublicationRequest, result ExternalPullRequest) error {
	if result.Locator.Provider != request.Locator.Provider ||
		result.Locator.Repository != request.Locator.Repository ||
		!boundedText(result.Locator.ExternalID, 256, false) ||
		result.SourceRef != request.SourceRef || result.SourceSHA != request.SourceSHA ||
		result.TargetRef != request.TargetRef || result.TargetSHA != request.TargetSHA ||
		!validHTTPURL(result.URL, 2048) {
		return fmt.Errorf("publisher: provider pull request does not match the exact creation request")
	}
	switch result.State {
	case contracts.PullRequestActive, contracts.PullRequestCompleted, contracts.PullRequestAbandoned:
		return nil
	default:
		return fmt.Errorf("publisher: provider pull request state is invalid")
	}
}

func validateExternalResult(operationKey string, result ExternalResult) error {
	if result.OperationKey != operationKey || !operationKeyPattern.MatchString(result.OperationKey) ||
		!boundedText(result.ExternalID, 4096, false) ||
		!validHTTPURL(result.URL, 2048) {
		return fmt.Errorf("publisher: provider PR result does not match the operation")
	}
	return nil
}

var _ PRService = (*providerPRService)(nil)
