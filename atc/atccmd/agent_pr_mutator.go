package atccmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/publisher/directgit"
	"github.com/concourse/concourse/agent/publisher/gittransport"
	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/pullrequest/github"
	"github.com/concourse/concourse/agent/snapshot"
)

const (
	maxPRMutatorHTTPTimeout = 30 * time.Second
	maxPRMutatorGitTimeout  = 5 * time.Minute
)

// agentPRMutatorResolver is ATC's narrow composition bridge from persisted
// publication actions to provider-native mutators. It owns only deployment
// dependencies; every provider identity and write credential is resolved anew
// from the acquired action by FileCredentialProvider.
type agentPRMutatorResolver struct {
	credentials   *publisher.FileCredentialProvider
	metadata      snapshot.MetadataStore
	content       snapshot.ContentStore
	canonicalizer snapshot.Canonicalizer
	runner        directgit.Runner
	gitTimeout    time.Duration
	httpClient    *http.Client
}

func newAgentPRMutatorResolver(
	credentials *publisher.FileCredentialProvider,
	metadata snapshot.MetadataStore,
	content snapshot.ContentStore,
	canonicalizer snapshot.Canonicalizer,
	runner directgit.Runner,
	timeout time.Duration,
) (*agentPRMutatorResolver, error) {
	if credentials == nil || isNilDependency(metadata) || isNilDependency(content) || isNilDependency(runner) {
		return nil, fmt.Errorf("agent PR mutator: credentials, snapshot stores, and controlled Git runner are required")
	}
	if timeout <= 0 || timeout > time.Hour {
		return nil, fmt.Errorf("agent PR mutator: timeout is invalid")
	}
	clientTimeout := timeout
	if clientTimeout > maxPRMutatorHTTPTimeout {
		clientTimeout = maxPRMutatorHTTPTimeout
	}
	gitTimeout := timeout
	if gitTimeout > maxPRMutatorGitTimeout {
		gitTimeout = maxPRMutatorGitTimeout
	}
	client := noRedirectPRHTTPClient(clientTimeout)
	return &agentPRMutatorResolver{
		credentials: credentials, metadata: metadata, content: content,
		canonicalizer: canonicalizer, runner: runner, gitTimeout: gitTimeout,
		httpClient: client,
	}, nil
}

func noRedirectPRHTTPClient(timeout time.Duration) *http.Client {
	client := *http.DefaultClient
	client.Timeout = timeout
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &client
}

func (resolver *agentPRMutatorResolver) ResolvePRMutator(
	ctx context.Context,
	action publisher.PRAction,
) (publisher.PRMutator, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is required", publisher.ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if resolver == nil || resolver.credentials == nil || isNilDependency(resolver.metadata) ||
		isNilDependency(resolver.content) || isNilDependency(resolver.runner) || resolver.httpClient == nil {
		return nil, fmt.Errorf("agent PR mutator: resolver is unavailable")
	}
	action = action.Clone()
	if err := action.ValidatePersisted(); err != nil {
		return nil, fmt.Errorf("agent PR mutator: acquired action is invalid")
	}
	authorization, err := resolver.credentials.AuthorizePR(ctx, action)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, fmt.Errorf("agent PR mutator: authorize acquired action")
	}
	if err := authorization.Validate(); err != nil {
		return nil, fmt.Errorf("agent PR mutator: resolved authorization is invalid")
	}
	if authorization.Provider() != actionProvider(action) || authorization.AdapterKind() != adapterForProvider(actionProvider(action)) {
		return nil, fmt.Errorf("agent PR mutator: authorization does not match acquired action")
	}
	token, err := newRedactedPRToken(authorization)
	if err != nil {
		return nil, err
	}

	var delegate pullrequest.Mutator
	var authentication gittransport.AuthenticationMode
	switch authorization.Provider() {
	case publisher.PRProviderGitHub:
		authentication = gittransport.AuthenticationAskpass
		branches, err := resolver.branchWriter(action, authorization, token, authentication)
		if err != nil {
			return nil, err
		}
		delegate, err = github.NewMutator(authorization.APIBaseURL(), token, branches, resolver.httpClient)
		if err != nil {
			return nil, fmt.Errorf("agent PR mutator: GitHub adapter is unavailable")
		}
	default:
		return nil, fmt.Errorf("agent PR mutator: provider is unsupported")
	}
	return newBoundPRMutator(action, delegate, authentication)
}

func (resolver *agentPRMutatorResolver) branchWriter(
	action publisher.PRAction,
	authorization publisher.PRAuthorization,
	token pullrequest.TokenSource,
	authentication gittransport.AuthenticationMode,
) (github.BranchWriter, error) {
	if action.Kind != publisher.OperationPublishPRBranch {
		return unavailablePRBranchWriter{}, nil
	}
	writer, err := gittransport.NewVerifiedBranchWriter(gittransport.VerifiedBranchWriterConfig{
		Request: *action.Branch, Metadata: resolver.metadata, Content: resolver.content,
		Canonicalizer: resolver.canonicalizer, Runner: resolver.runner,
		RemoteURL: authorization.RepositoryURL(), Token: token, Authentication: authentication,
		Timeout: resolver.gitTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("agent PR mutator: verified branch writer is unavailable")
	}
	return writer, nil
}

func actionProvider(action publisher.PRAction) publisher.PRProvider {
	switch action.Kind {
	case publisher.OperationPublishPRBranch:
		return action.Branch.Locator.Provider
	case publisher.OperationCreatePR:
		return action.PullRequest.Locator.Provider
	case publisher.OperationPublishPRStatus:
		return action.Status.Locator.Provider
	case publisher.OperationRespondToReview:
		return action.Response.Locator.Provider
	default:
		return ""
	}
}

func adapterForProvider(provider publisher.PRProvider) publisher.AdapterKind {
	switch provider {
	case publisher.PRProviderGitHub:
		return publisher.AdapterGitHub
	default:
		return ""
	}
}

type unavailablePRBranchWriter struct{}

func (unavailablePRBranchWriter) CompareAndSwapBranch(context.Context, pullrequest.BranchMutation) (pullrequest.BranchResult, error) {
	return pullrequest.BranchResult{}, fmt.Errorf("agent PR mutator: branch mutation is not authorized by acquired action")
}

// boundPRMutator makes the provider adapter single-use for the exact durable
// action that selected it. This protects the composition seam itself in
// addition to publisher.PRService's action-to-mutation mapping.
type boundPRMutator struct {
	action         publisher.PRAction
	delegate       pullrequest.Mutator
	authentication gittransport.AuthenticationMode
}

func newBoundPRMutator(action publisher.PRAction, delegate pullrequest.Mutator, authentication gittransport.AuthenticationMode) (*boundPRMutator, error) {
	action = action.Clone()
	if err := action.ValidatePersisted(); err != nil || nilPullRequestMutator(delegate) {
		return nil, fmt.Errorf("agent PR mutator: action-bound provider adapter is invalid")
	}
	return &boundPRMutator{action: action, delegate: delegate, authentication: authentication}, nil
}

func (mutator *boundPRMutator) CompareAndSwapBranch(ctx context.Context, mutation publisher.BranchMutation) (publisher.BranchResult, error) {
	if err := requirePRMutatorContext(ctx); err != nil {
		return publisher.BranchResult{}, err
	}
	expected, err := branchMutationFor(mutator.action)
	if err != nil || !reflect.DeepEqual(mutation, expected) {
		return publisher.BranchResult{}, fmt.Errorf("agent PR mutator: branch mutation does not match acquired PR action")
	}
	result, err := mutator.delegate.CompareAndSwapBranch(ctx, pullrequest.BranchMutation{
		Locator: pullrequestLocator(mutation.Locator), Ref: mutation.Ref, TargetRef: mutation.TargetRef,
		ExpectedSource: mutation.ExpectedSource, ExpectedTargetSHA: mutation.ExpectedTargetSHA,
		NewSourceSHA: mutation.NewSourceSHA, OperationKey: mutation.OperationKey,
	})
	if err != nil {
		return publisher.BranchResult{}, safePRMutatorError(ctx, err)
	}
	return publisher.BranchResult{HeadSHA: result.HeadSHA, Applied: result.Applied}, nil
}

func (mutator *boundPRMutator) FindOrCreatePullRequest(ctx context.Context, mutation publisher.CreatePRMutation) (publisher.ExternalPullRequest, error) {
	if err := requirePRMutatorContext(ctx); err != nil {
		return publisher.ExternalPullRequest{}, err
	}
	expected, err := createMutationFor(mutator.action)
	if err != nil || !reflect.DeepEqual(mutation, expected) {
		return publisher.ExternalPullRequest{}, fmt.Errorf("agent PR mutator: create mutation does not match acquired PR action")
	}
	result, err := mutator.delegate.FindOrCreatePullRequest(ctx, pullrequest.CreateRequest{
		Locator: pullrequestLocator(mutation.Locator), SourceRef: mutation.SourceRef, SourceSHA: mutation.SourceSHA,
		TargetRef: mutation.TargetRef, TargetSHA: mutation.TargetSHA, Title: mutation.Title, Body: mutation.Body,
		OperationKey: mutation.OperationKey,
	})
	if err != nil {
		return publisher.ExternalPullRequest{}, safePRMutatorError(ctx, err)
	}
	return publisher.ExternalPullRequest{Locator: publisherLocator(result.Locator), URL: result.URL, State: result.State,
		SourceRef: result.SourceRef, SourceSHA: result.SourceSHA, TargetRef: result.TargetRef, TargetSHA: result.TargetSHA}, nil
}

func (mutator *boundPRMutator) PublishValidationStatus(ctx context.Context, mutation publisher.StatusMutation) (publisher.ExternalResult, error) {
	if err := requirePRMutatorContext(ctx); err != nil {
		return publisher.ExternalResult{}, err
	}
	expected, err := statusMutationFor(mutator.action)
	if err != nil || !reflect.DeepEqual(mutation, expected) {
		return publisher.ExternalResult{}, fmt.Errorf("agent PR mutator: status mutation does not match acquired PR action")
	}
	result, err := mutator.delegate.PublishValidationStatus(ctx, pullrequest.StatusRequest{
		Locator: pullrequestLocator(mutation.Locator), TargetRef: mutation.TargetRef, SourceSHA: mutation.SourceSHA,
		State: mutation.State, Description: mutation.Description, TargetURL: mutation.TargetURL, OperationKey: mutation.OperationKey,
	})
	if err != nil {
		return publisher.ExternalResult{}, safePRMutatorError(ctx, err)
	}
	return publisher.ExternalResult{OperationKey: result.OperationKey, ExternalID: result.ExternalID, URL: result.URL}, nil
}

func (mutator *boundPRMutator) PublishReviewResponse(ctx context.Context, mutation publisher.ResponseMutation) (publisher.ExternalResult, error) {
	if err := requirePRMutatorContext(ctx); err != nil {
		return publisher.ExternalResult{}, err
	}
	expected, err := responseMutationFor(mutator.action)
	if err != nil || !reflect.DeepEqual(mutation, expected) {
		return publisher.ExternalResult{}, fmt.Errorf("agent PR mutator: response mutation does not match acquired PR action")
	}
	result, err := mutator.delegate.PublishReviewResponse(ctx, pullrequest.ResponseRequest{
		Locator: pullrequestLocator(mutation.Locator), TargetRef: mutation.TargetRef, Batch: mutation.Batch,
		Response: mutation.Response, OperationKey: mutation.OperationKey,
	})
	if err != nil {
		return publisher.ExternalResult{}, safePRMutatorError(ctx, err)
	}
	return publisher.ExternalResult{OperationKey: result.OperationKey, ExternalID: result.ExternalID, URL: result.URL}, nil
}

func branchMutationFor(action publisher.PRAction) (publisher.BranchMutation, error) {
	if action.Kind != publisher.OperationPublishPRBranch || action.Branch == nil {
		return publisher.BranchMutation{}, fmt.Errorf("wrong action")
	}
	key, err := action.OperationKey()
	if err != nil {
		return publisher.BranchMutation{}, err
	}
	request := action.Branch
	return publisher.BranchMutation{Locator: request.Locator, Ref: request.SourceRef, TargetRef: request.TargetRef,
		ExpectedSource: request.ExpectedSource, ExpectedTargetSHA: request.ExpectedTargetSHA, NewSourceSHA: request.NewSourceSHA, OperationKey: key}, nil
}

func createMutationFor(action publisher.PRAction) (publisher.CreatePRMutation, error) {
	if action.Kind != publisher.OperationCreatePR || action.PullRequest == nil {
		return publisher.CreatePRMutation{}, fmt.Errorf("wrong action")
	}
	key, err := action.OperationKey()
	if err != nil {
		return publisher.CreatePRMutation{}, err
	}
	request := action.PullRequest
	return publisher.CreatePRMutation{Locator: request.Locator, SourceRef: request.SourceRef, SourceSHA: request.SourceSHA,
		TargetRef: request.TargetRef, TargetSHA: request.TargetSHA, Title: request.Title, Body: request.Body, OperationKey: key}, nil
}

func statusMutationFor(action publisher.PRAction) (publisher.StatusMutation, error) {
	if action.Kind != publisher.OperationPublishPRStatus || action.Status == nil {
		return publisher.StatusMutation{}, fmt.Errorf("wrong action")
	}
	key, err := action.OperationKey()
	if err != nil {
		return publisher.StatusMutation{}, err
	}
	request := action.Status
	return publisher.StatusMutation{Locator: request.Locator, TargetRef: request.TargetRef, SourceSHA: request.SourceSHA,
		State: request.State, Description: request.Description, TargetURL: request.TargetURL, OperationKey: key}, nil
}

func responseMutationFor(action publisher.PRAction) (publisher.ResponseMutation, error) {
	if action.Kind != publisher.OperationRespondToReview || action.Response == nil {
		return publisher.ResponseMutation{}, fmt.Errorf("wrong action")
	}
	key, err := action.OperationKey()
	if err != nil {
		return publisher.ResponseMutation{}, err
	}
	request := action.Response
	return publisher.ResponseMutation{Locator: request.Locator, TargetRef: request.TargetRef, Batch: request.Batch, Response: request.Response, OperationKey: key}, nil
}

func pullrequestLocator(locator publisher.PRLocator) pullrequest.Locator {
	return pullrequest.Locator{Provider: pullrequest.Provider(locator.Provider), Repository: locator.Repository, ExternalID: locator.ExternalID}
}
func publisherLocator(locator pullrequest.Locator) publisher.PRLocator {
	return publisher.PRLocator{Provider: publisher.PRProvider(locator.Provider), Repository: locator.Repository, ExternalID: locator.ExternalID}
}

func safePRMutatorError(ctx context.Context, err error) error {
	if ctx != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
	}
	switch {
	case errors.Is(err, gittransport.ErrStaleSource):
		return publisher.ErrPRSourceStale
	case errors.Is(err, gittransport.ErrStaleTarget):
		return publisher.ErrPRTargetStale
	default:
		return fmt.Errorf("agent PR mutator: provider operation failed")
	}
}

func requirePRMutatorContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", publisher.ErrInvalidRequest)
	}
	return ctx.Err()
}

func nilPullRequestMutator(value pullrequest.Mutator) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	return reflected.Kind() == reflect.Pointer && reflected.IsNil()
}

func (mutator *boundPRMutator) String() string   { return "agent PR mutator (credential redacted)" }
func (mutator *boundPRMutator) GoString() string { return mutator.String() }
func (resolver *agentPRMutatorResolver) String() string {
	return "agent PR mutator resolver (credentials redacted)"
}
func (resolver *agentPRMutatorResolver) GoString() string { return resolver.String() }

type redactedPRToken struct{ secret []byte }

func newRedactedPRToken(authorization publisher.PRAuthorization) (*redactedPRToken, error) {
	credential := authorization.WriteCredential()
	secret := credential.Secret()
	if len(secret) == 0 {
		return nil, fmt.Errorf("agent PR mutator: write credential is unavailable")
	}
	return &redactedPRToken{secret: secret}, nil
}

func (token *redactedPRToken) Token(ctx context.Context) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("agent PR mutator: context is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if token == nil || len(token.secret) == 0 {
		return "", fmt.Errorf("agent PR mutator: write credential is unavailable")
	}
	return string(token.secret), nil
}
func (*redactedPRToken) String() string         { return "agent PR credential (redacted)" }
func (token *redactedPRToken) GoString() string { return token.String() }

var _ publisher.PRMutatorResolver = (*agentPRMutatorResolver)(nil)
var _ publisher.PRMutator = (*boundPRMutator)(nil)
