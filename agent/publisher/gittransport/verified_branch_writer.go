package gittransport

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/publisher/directgit"
	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot"
)

type VerifiedBranchWriterConfig struct {
	Request        publisher.BranchPublicationRequest
	Metadata       snapshot.MetadataStore
	Content        snapshot.ContentStore
	Canonicalizer  snapshot.Canonicalizer
	Runner         directgit.Runner
	RemoteURL      string
	Token          pullrequest.TokenSource
	Authentication AuthenticationMode
	Timeout        time.Duration
}

// VerifiedBranchWriter binds a durable branch action to its exact,
// team-authorized candidate snapshot. Each call reopens and verifies the outer
// value, privately re-canonicalizes its nested repository, and gives only that
// nested repository to RefLease.
type VerifiedBranchWriter struct {
	request        publisher.BranchPublicationRequest
	inspector      *publisher.SnapshotChangeInspector
	materializer   *directgit.VerifiedChangeMaterializer
	runner         directgit.Runner
	remoteURL      string
	token          pullrequest.TokenSource
	authentication AuthenticationMode
	timeout        time.Duration
}

func NewVerifiedBranchWriter(
	config VerifiedBranchWriterConfig,
) (*VerifiedBranchWriter, error) {
	action := publisher.PRAction{
		Kind:   publisher.OperationPublishPRBranch,
		Branch: &config.Request,
	}.Clone()
	if err := action.ValidatePersisted(); err != nil {
		return nil, err
	}
	request := *action.Branch
	switch request.Locator.Provider {
	case publisher.PRProviderGitHub:
		if config.Authentication != AuthenticationAskpass {
			return nil, fmt.Errorf("git transport: GitHub branch publication requires explicit askpass authentication")
		}
	default:
		return nil, fmt.Errorf("git transport: branch publication provider is invalid")
	}
	if nilInterface(config.Runner) || nilInterface(config.Token) {
		return nil, fmt.Errorf("git transport: runner and credential source are required")
	}
	if err := validateRemoteURL(config.RemoteURL); err != nil {
		return nil, err
	}
	if err := validateTimeout(config.Timeout); err != nil {
		return nil, err
	}
	inspector, err := publisher.NewSnapshotChangeInspector(
		config.Metadata,
		config.Content,
		config.Canonicalizer,
	)
	if err != nil {
		return nil, err
	}
	materializer, err := directgit.NewVerifiedChangeMaterializer(
		config.Runner,
		config.Canonicalizer.TempDir,
		config.Canonicalizer,
	)
	if err != nil {
		return nil, err
	}
	return &VerifiedBranchWriter{
		request: request, inspector: inspector, materializer: materializer,
		runner: config.Runner, remoteURL: config.RemoteURL, token: config.Token,
		authentication: config.Authentication, timeout: config.Timeout,
	}, nil
}

func (writer *VerifiedBranchWriter) CompareAndSwapBranch(
	ctx context.Context,
	mutation pullrequest.BranchMutation,
) (result pullrequest.BranchResult, returnedErr error) {
	if ctx == nil {
		return pullrequest.BranchResult{}, fmt.Errorf("git transport: context is required")
	}
	if err := ctx.Err(); err != nil {
		return pullrequest.BranchResult{}, err
	}
	if writer == nil || writer.inspector == nil || writer.materializer == nil {
		return pullrequest.BranchResult{}, fmt.Errorf("git transport: verified branch writer is required")
	}
	expected, err := mutationForRequest(writer.request)
	if err != nil {
		return pullrequest.BranchResult{}, err
	}
	if mutation != expected {
		return pullrequest.BranchResult{}, fmt.Errorf("git transport: mutation does not match the persisted branch action")
	}

	change, err := writer.inspector.InspectPRCandidate(ctx, writer.request)
	if err != nil {
		return pullrequest.BranchResult{}, err
	}
	defer func() {
		if cleanupErr := change.Close(); cleanupErr != nil {
			result = pullrequest.BranchResult{}
			returnedErr = errors.Join(
				returnedErr,
				fmt.Errorf("git transport: close verified candidate: %w", cleanupErr),
			)
		}
	}()
	if change.BaseSHA != writer.request.ExpectedTargetSHA ||
		change.ResultSHA != writer.request.NewSourceSHA {
		return pullrequest.BranchResult{}, fmt.Errorf(
			"git transport: verified candidate does not match the persisted branch action",
		)
	}

	verified, err := writer.materializer.Materialize(ctx, change)
	if err != nil {
		return pullrequest.BranchResult{}, err
	}
	defer func() {
		if cleanupErr := verified.Close(); cleanupErr != nil {
			result = pullrequest.BranchResult{}
			returnedErr = errors.Join(
				returnedErr,
				fmt.Errorf("git transport: close verified repository: %w", cleanupErr),
			)
		}
	}()
	lease, err := NewRefLeaseWithAuthentication(
		writer.runner,
		writer.remoteURL,
		verified.Repository(),
		writer.token,
		writer.authentication,
		writer.timeout,
	)
	if err != nil {
		return pullrequest.BranchResult{}, err
	}
	return lease.CompareAndSwapBranch(ctx, mutation)
}

func mutationForRequest(
	request publisher.BranchPublicationRequest,
) (pullrequest.BranchMutation, error) {
	key, err := request.OperationKey()
	if err != nil {
		return pullrequest.BranchMutation{}, err
	}
	return pullrequest.BranchMutation{
		Locator: pullrequest.Locator{
			Provider:   pullrequest.Provider(request.Locator.Provider),
			Repository: request.Locator.Repository,
			ExternalID: request.Locator.ExternalID,
		},
		Ref:               request.SourceRef,
		ExpectedSource:    request.ExpectedSource,
		TargetRef:         request.TargetRef,
		ExpectedTargetSHA: request.ExpectedTargetSHA,
		NewSourceSHA:      request.NewSourceSHA,
		OperationKey:      key,
	}, nil
}

var _ interface {
	CompareAndSwapBranch(
		context.Context,
		pullrequest.BranchMutation,
	) (pullrequest.BranchResult, error)
} = (*VerifiedBranchWriter)(nil)
