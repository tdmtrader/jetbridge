// Package conformance contains provider-neutral adapter contract scenarios.
// Provider packages supply their real adapters; the suite deliberately leaves
// provider HTTP shapes in provider-owned tests.
package conformance

import (
	"context"
	"testing"

	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// MutationSubject identifies one configured real provider mutator.
type MutationSubject struct {
	Mutator    pullrequest.Mutator
	Provider   pullrequest.Provider
	Repository string
	ExternalID string
}

// RunMutationSuite proves common authority checks fail before a provider side
// effect. Exact HTTP mapping, idempotent recovery, and pagination remain in
// each provider package because those wire contracts intentionally differ.
func RunMutationSuite(t *testing.T, subject MutationSubject) {
	t.Helper()
	if subject.Mutator == nil {
		t.Fatal("conformance mutator is required")
	}
	if subject.Provider != pullrequest.ProviderGitHub {
		t.Fatalf("conformance provider %q is unsupported", subject.Provider)
	}
	if subject.Repository == "" || subject.ExternalID == "" {
		t.Fatal("conformance repository and external id are required")
	}

	const (
		sourceRef = "refs/heads/agent/upgrade"
		targetRef = "refs/heads/main"
		sourceSHA = "cccccccccccccccccccccccccccccccccccccccc"
		targetSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		validKey  = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	)
	validLocator := pullrequest.Locator{
		Provider: subject.Provider, Repository: subject.Repository,
		ExternalID: subject.ExternalID,
	}
	missingLocator := validLocator
	missingLocator.ExternalID = ""
	// A synthetic provider the core does not accept. It must never equal
	// subject.Provider, or the rejection assertions below would assert that a
	// valid locator is refused.
	wrongProvider := pullrequest.Provider("unsupported-provider")
	wrongLocator := validLocator
	wrongLocator.Provider = wrongProvider
	wrongMissing := missingLocator
	wrongMissing.Provider = wrongProvider

	t.Run("wrong provider locator is rejected", func(t *testing.T) {
		mustReject(t, func() error {
			_, err := subject.Mutator.CompareAndSwapBranch(
				context.Background(),
				branchRequest(
					wrongLocator, sourceRef, targetRef,
					sourceSHA, targetSHA, validKey,
				),
			)
			return err
		})
		mustReject(t, func() error {
			_, err := subject.Mutator.FindOrCreatePullRequest(
				context.Background(),
				createRequest(
					wrongMissing, sourceRef, targetRef,
					sourceSHA, targetSHA, validKey,
				),
			)
			return err
		})
		mustReject(t, func() error {
			_, err := subject.Mutator.PublishValidationStatus(
				context.Background(),
				statusRequest(wrongLocator, targetRef, sourceSHA, validKey),
			)
			return err
		})
		mustReject(t, func() error {
			_, err := subject.Mutator.PublishReviewResponse(
				context.Background(),
				responseRequest(
					wrongLocator, targetRef, sourceSHA, validKey, "thread-1",
				),
			)
			return err
		})
	})

	t.Run("malformed operation identity is rejected", func(t *testing.T) {
		const malformed = "sha256:not-a-digest"
		mustReject(t, func() error {
			_, err := subject.Mutator.CompareAndSwapBranch(
				context.Background(),
				branchRequest(
					validLocator, sourceRef, targetRef,
					sourceSHA, targetSHA, malformed,
				),
			)
			return err
		})
		mustReject(t, func() error {
			_, err := subject.Mutator.FindOrCreatePullRequest(
				context.Background(),
				createRequest(
					missingLocator, sourceRef, targetRef,
					sourceSHA, targetSHA, malformed,
				),
			)
			return err
		})
		mustReject(t, func() error {
			_, err := subject.Mutator.PublishValidationStatus(
				context.Background(),
				statusRequest(validLocator, targetRef, sourceSHA, malformed),
			)
			return err
		})
		mustReject(t, func() error {
			_, err := subject.Mutator.PublishReviewResponse(
				context.Background(),
				responseRequest(
					validLocator, targetRef, sourceSHA, malformed, "thread-1",
				),
			)
			return err
		})
	})

	t.Run("missing target ref is rejected", func(t *testing.T) {
		mustReject(t, func() error {
			_, err := subject.Mutator.PublishValidationStatus(
				context.Background(),
				statusRequest(validLocator, "", sourceSHA, validKey),
			)
			return err
		})
		mustReject(t, func() error {
			_, err := subject.Mutator.PublishReviewResponse(
				context.Background(),
				responseRequest(
					validLocator, "", sourceSHA, validKey, "thread-1",
				),
			)
			return err
		})
	})

	t.Run("non-head target ref is rejected", func(t *testing.T) {
		for _, malformed := range []string{
			"main",
			"refs/tags/main",
			"refs/heads/../main",
			"refs/heads/main.lock",
		} {
			mustReject(t, func() error {
				_, err := subject.Mutator.PublishValidationStatus(
					context.Background(),
					statusRequest(
						validLocator, malformed, sourceSHA, validKey,
					),
				)
				return err
			})
			mustReject(t, func() error {
				_, err := subject.Mutator.PublishReviewResponse(
					context.Background(),
					responseRequest(
						validLocator, malformed, sourceSHA, validKey,
						"thread-1",
					),
				)
				return err
			})
		}
	})

	t.Run("reply outside sealed batch is rejected", func(t *testing.T) {
		mustReject(t, func() error {
			_, err := subject.Mutator.PublishReviewResponse(
				context.Background(),
				responseRequest(
					validLocator, targetRef, sourceSHA, validKey, "thread-2",
				),
			)
			return err
		})
	})
}

func branchRequest(
	locator pullrequest.Locator,
	sourceRef string,
	targetRef string,
	sourceSHA string,
	targetSHA string,
	operationKey string,
) pullrequest.BranchMutation {
	return pullrequest.BranchMutation{
		Locator:           locator,
		Ref:               sourceRef,
		ExpectedSource:    contracts.PullRequestHeadExpectation{Exists: true, SHA: sourceSHA},
		TargetRef:         targetRef,
		ExpectedTargetSHA: targetSHA,
		NewSourceSHA:      "dddddddddddddddddddddddddddddddddddddddd",
		OperationKey:      operationKey,
	}
}

func createRequest(
	locator pullrequest.Locator,
	sourceRef string,
	targetRef string,
	sourceSHA string,
	targetSHA string,
	operationKey string,
) pullrequest.CreateRequest {
	return pullrequest.CreateRequest{
		Locator: locator, SourceRef: sourceRef, SourceSHA: sourceSHA,
		TargetRef: targetRef, TargetSHA: targetSHA,
		Title: "Upgrade widget", Body: "Validated change.",
		OperationKey: operationKey,
	}
}

func statusRequest(
	locator pullrequest.Locator,
	targetRef string,
	sourceSHA string,
	operationKey string,
) pullrequest.StatusRequest {
	return pullrequest.StatusRequest{
		Locator: locator, TargetRef: targetRef,
		SourceSHA: sourceSHA, State: "success",
		Description:  "Validation passed",
		TargetURL:    "https://ci.example/runs/91",
		OperationKey: operationKey,
	}
}

func responseRequest(
	locator pullrequest.Locator,
	targetRef string,
	sourceSHA string,
	operationKey string,
	replyThread string,
) pullrequest.ResponseRequest {
	return pullrequest.ResponseRequest{
		Locator: locator, TargetRef: targetRef,
		Batch: pullrequest.ReviewBatch{
			ID: "review-1", ReviewID: "1", CommitSHA: sourceSHA,
			Reviewer: "reviewer-1", Ready: true,
			ThreadIDs: []string{"thread-1"},
		},
		Response: contracts.PullRequestResponseBody{
			BatchID: "review-1", Summary: "Addressed review.",
			Replies: []contracts.PullRequestThreadResponse{{
				ThreadID: replyThread, Body: "Updated.",
			}},
		},
		OperationKey: operationKey,
	}
}

func mustReject(t *testing.T, operation func() error) {
	t.Helper()
	if err := operation(); err == nil {
		t.Fatal("invalid provider-neutral mutation authority was accepted")
	}
}
