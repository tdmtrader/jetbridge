package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/pullrequest"
)

func TestMutationSuiteExercisesEveryFailClosedOperation(t *testing.T) {
	mutator := &rejectingMutator{}
	RunMutationSuite(t, MutationSubject{
		Mutator:    mutator,
		Provider:   pullrequest.ProviderGitHub,
		Repository: "acme/widget",
		ExternalID: "42",
	})
	if mutator.branchCalls == 0 || mutator.createCalls == 0 ||
		mutator.statusCalls == 0 || mutator.responseCalls == 0 {
		t.Fatalf("suite calls = %+v", mutator)
	}
}

type rejectingMutator struct {
	branchCalls   int
	createCalls   int
	statusCalls   int
	responseCalls int
}

func (mutator *rejectingMutator) CompareAndSwapBranch(
	context.Context,
	pullrequest.BranchMutation,
) (pullrequest.BranchResult, error) {
	mutator.branchCalls++
	return pullrequest.BranchResult{}, errors.New("rejected")
}

func (mutator *rejectingMutator) FindOrCreatePullRequest(
	context.Context,
	pullrequest.CreateRequest,
) (pullrequest.ExternalPullRequest, error) {
	mutator.createCalls++
	return pullrequest.ExternalPullRequest{}, errors.New("rejected")
}

func (mutator *rejectingMutator) PublishValidationStatus(
	context.Context,
	pullrequest.StatusRequest,
) (pullrequest.ExternalResult, error) {
	mutator.statusCalls++
	return pullrequest.ExternalResult{}, errors.New("rejected")
}

func (mutator *rejectingMutator) PublishReviewResponse(
	context.Context,
	pullrequest.ResponseRequest,
) (pullrequest.ExternalResult, error) {
	mutator.responseCalls++
	return pullrequest.ExternalResult{}, errors.New("rejected")
}
