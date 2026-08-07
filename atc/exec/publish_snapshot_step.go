package exec

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/tracing"
)

type PublishSnapshotStep struct {
	planID           atc.PlanID
	plan             atc.PublishSnapshotPlan
	metadata         StepMetadata
	delegateFactory  BuildStepDelegateFactory
	metadataStore    snapshot.MetadataStore
	contentStore     snapshot.ContentStore
	executor         publisher.Executor
	approvalVerifier publisher.MergeApprovalVerifier
}

type PublishSnapshotStepOption func(*PublishSnapshotStep)

func WithPublishSnapshotContentStore(store snapshot.ContentStore) PublishSnapshotStepOption {
	return func(step *PublishSnapshotStep) { step.contentStore = store }
}

func NewPublishSnapshotStep(
	planID atc.PlanID,
	plan atc.PublishSnapshotPlan,
	metadata StepMetadata,
	delegateFactory BuildStepDelegateFactory,
	metadataStore snapshot.MetadataStore,
	executor publisher.Executor,
	approvalVerifier publisher.MergeApprovalVerifier,
	opts ...PublishSnapshotStepOption,
) Step {
	plan.Parameters = clonePublishParameters(plan.Parameters)
	step := &PublishSnapshotStep{
		planID: planID, plan: plan, metadata: metadata, delegateFactory: delegateFactory,
		metadataStore: metadataStore, executor: executor,
		approvalVerifier: approvalVerifier,
	}
	for _, option := range opts {
		option(step)
	}
	return step
}

func (step *PublishSnapshotStep) Run(ctx context.Context, state RunState) (bool, error) {
	delegate := step.delegateFactory.BuildStepDelegate(state)
	ctx, span := delegate.StartSpan(ctx, "publish_snapshot", tracing.Attrs{
		"name": step.plan.Name, "publisher": step.plan.Publisher.String(), "mode": string(step.plan.Mode),
	})
	ok, err := step.run(ctx, state, delegate)
	tracing.End(span, err)
	return ok, err
}

func (step *PublishSnapshotStep) run(ctx context.Context, state RunState, delegate BuildStepDelegate) (bool, error) {
	logger := lagerctx.FromContext(ctx).Session("publish-snapshot-step", lager.Data{
		"step-name": step.plan.Name, "publisher": step.plan.Publisher.String(),
		"mode": string(step.plan.Mode), "job-id": step.metadata.JobID,
	})
	delegate.Initializing(logger)
	if step.metadataStore == nil {
		return false, fmt.Errorf("publish_snapshot: publication is disabled on the web node")
	}
	if step.executor == nil {
		return false, fmt.Errorf("publish_snapshot: publication is disabled on the web node")
	}
	if step.metadata.TeamID <= 0 || step.metadata.BuildID <= 0 ||
		strings.TrimSpace(step.metadata.TeamName) == "" || strings.TrimSpace(step.metadata.SnapshotCreatedBy) == "" {
		return false, fmt.Errorf("publish_snapshot: server build identity is unavailable")
	}
	if warning, err := atc.ValidateIdentifier(step.plan.Name); err != nil || warning != nil {
		return false, fmt.Errorf("publish_snapshot: invalid step name")
	}
	if warning, err := atc.ValidateIdentifier(step.plan.Input); err != nil || warning != nil {
		return false, fmt.Errorf("publish_snapshot: invalid input artifact name")
	}
	if strings.Contains(step.plan.Destination, "((") || publishParametersContainInterpolation(step.plan.Parameters) {
		return false, fmt.Errorf("publish_snapshot: unresolved publication parameter")
	}

	ref, manifest, err := step.authorizedInput(ctx, state.ArtifactRepository())
	if err != nil {
		return false, err
	}
	if step.plan.InputType == snapshot.TypeRef("repository-change/v1") {
		if step.plan.PublishValidation == nil {
			return false, fmt.Errorf("publish_snapshot: authoritative validation plan is unavailable")
		}
		if err := requireValidationRequirement(ctx, "publish_snapshot", state.ArtifactRepository(), step.metadata, step.metadataStore, step.contentStore, publishRequirement(*step.plan.PublishValidation), step.plan.Input); err != nil {
			return false, err
		}
	}
	parameters := clonePublishParameters(step.plan.Parameters)
	if step.plan.Mode == publisher.ModeMerge {
		if step.approvalVerifier == nil {
			return false, fmt.Errorf("publish_snapshot: durable merge approval verification is unavailable")
		}
		// Derive the base assertion from the exact snapshot being published —
		// the same snapshot the approval pinned by digest, so await_snapshot
		// derived the identical value. See publisher.MergeBaseParameter.
		baseSHA, err := publisher.MergeBaseFromChange(manifest)
		if err != nil {
			return false, fmt.Errorf("publish_snapshot: merge base is unavailable for the published change")
		}
		parameters, err = publisher.StampMergeBase(step.plan.Parameters, baseSHA)
		if err != nil {
			return false, fmt.Errorf("publish_snapshot: invalid publication plan")
		}
	}
	request := publisher.Request{
		Publisher: step.plan.Publisher, Input: ref, Destination: step.plan.Destination,
		Mode: step.plan.Mode, Parameters: clonePublishParameters(parameters),
		ApprovalPolicyVersion: step.plan.ApprovalPolicyVersion,
		Authority: publisher.Authority{
			TeamID: step.metadata.TeamID, TeamName: step.metadata.TeamName,
			BuildID: int64(step.metadata.BuildID), PlanID: string(step.planID),
			Actor: step.metadata.SnapshotCreatedBy,
		},
	}
	if step.plan.Mode == publisher.ModeMerge {
		runID, err := snapshot.ParseWorkflowRunID(step.plan.WorkflowRunID)
		if err != nil {
			return false, fmt.Errorf("publish_snapshot: invalid workflow run identifier")
		}
		approval, _, err := step.authorizedArtifact(ctx, state.ArtifactRepository(), step.plan.Approval, snapshot.TypeRef("human-answer/v1"))
		if err != nil {
			return false, err
		}
		evidence, err := step.approvalVerifier.Verify(ctx, publisher.MergeApprovalRequest{
			TeamID: step.metadata.TeamID, WorkflowRunID: runID, BuildID: int64(step.metadata.BuildID),
			Input: ref, Approval: approval, Publisher: step.plan.Publisher, Mode: step.plan.Mode,
			Destination: step.plan.Destination, Parameters: clonePublishParameters(parameters),
			ExpectedBaseSHA:       parameters[publisher.MergeBaseParameter],
			ApprovalPolicyVersion: step.plan.ApprovalPolicyVersion,
		})
		if err != nil {
			return false, fmt.Errorf("publish_snapshot: durable merge approval was rejected")
		}
		request.Authority.WorkflowRunID = runID
		request.ApprovedBy = evidence.ResolvedBy
		request.Approval = &evidence
	}
	if err := request.Validate(); err != nil {
		return false, fmt.Errorf("publish_snapshot: invalid publication plan")
	}
	expectedKey, err := request.OperationKey()
	if err != nil {
		return false, fmt.Errorf("publish_snapshot: invalid publication plan")
	}

	delegate.Starting(logger)
	publication, err := step.executor.Execute(ctx, request.Clone())
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return false, contextErr
		}
		return false, fmt.Errorf("publish_snapshot: publication execution failed")
	}
	if err := validatePublicationResponse(publication, expectedKey, request); err != nil {
		return false, fmt.Errorf("publish_snapshot: publication service returned an invalid response")
	}
	switch publication.Status {
	case publisher.StatusSucceeded:
		delegate.Finished(logger, true)
		return true, nil
	case publisher.StatusPending:
		return false, fmt.Errorf("publish_snapshot: publication is still pending")
	case publisher.StatusFailed, publisher.StatusStaleBase, publisher.StatusRebaseRequired:
		return false, fmt.Errorf("publish_snapshot: publication ended with status %s", publication.Status)
	default:
		return false, fmt.Errorf("publish_snapshot: publication service returned an invalid response")
	}
}

func (step *PublishSnapshotStep) authorizedInput(ctx context.Context, repository *build.Repository) (snapshot.SnapshotRef, snapshot.Snapshot, error) {
	return step.authorizedArtifact(ctx, repository, step.plan.Input, step.plan.InputType)
}

func (step *PublishSnapshotStep) authorizedArtifact(ctx context.Context, repository *build.Repository, name string, expectedType snapshot.TypeRef) (snapshot.SnapshotRef, snapshot.Snapshot, error) {
	entry, found := repository.ArtifactEntryFor(build.ArtifactName(name))
	if !found || entry.Snapshot == nil || entry.Snapshot.Type != expectedType {
		return snapshot.SnapshotRef{}, snapshot.Snapshot{}, fmt.Errorf("publish_snapshot: sealed input snapshot is unavailable")
	}
	ref := *entry.Snapshot
	if err := ref.Validate(); err != nil {
		return snapshot.SnapshotRef{}, snapshot.Snapshot{}, fmt.Errorf("publish_snapshot: sealed input snapshot is unavailable")
	}
	manifest, found, err := step.metadataStore.GetAuthorized(ctx, step.metadata.TeamID, ref.ID)
	if err != nil {
		return snapshot.SnapshotRef{}, snapshot.Snapshot{}, fmt.Errorf("publish_snapshot: snapshot authorization failed")
	}
	if !found || manifest.Validate() != nil || manifest.ID != ref.ID || manifest.Type != ref.Type ||
		manifest.Digest != ref.Digest || manifest.Type != expectedType ||
		manifest.ContentState != snapshot.ContentStateAvailable {
		return snapshot.SnapshotRef{}, snapshot.Snapshot{}, fmt.Errorf("publish_snapshot: sealed input snapshot is unavailable")
	}
	return ref, manifest, nil
}

func validatePublicationResponse(
	publication publisher.Publication,
	expectedKey string,
	expectedRequest publisher.Request,
) error {
	if publication.ID <= 0 || publication.OperationKey != expectedKey || publication.Attempt <= 0 ||
		publication.CreatedAt.IsZero() || publication.UpdatedAt.IsZero() ||
		publication.UpdatedAt.Before(publication.CreatedAt) {
		return fmt.Errorf("publication identity mismatch")
	}
	if err := publication.Request.ValidatePersisted(); err != nil {
		return fmt.Errorf("publication authority was not verified")
	}
	requestKey, err := publication.Request.OperationKey()
	if err != nil || requestKey != expectedKey {
		return fmt.Errorf("publication request mismatch")
	}
	expectedRequest = expectedRequest.Clone()
	if expectedRequest.Authority.WorkflowRunID == 0 {
		expectedRequest.Authority.WorkflowRunID = publication.Request.Authority.WorkflowRunID
	}
	if publication.Request.Publisher != expectedRequest.Publisher ||
		publication.Request.Input != expectedRequest.Input ||
		publication.Request.Destination != expectedRequest.Destination ||
		publication.Request.Mode != expectedRequest.Mode ||
		!maps.Equal(publication.Request.Parameters, expectedRequest.Parameters) ||
		publication.Request.ApprovalPolicyVersion != expectedRequest.ApprovalPolicyVersion ||
		publication.Request.Authority != expectedRequest.Authority {
		return fmt.Errorf("publication request mismatch")
	}
	switch publication.Status {
	case publisher.StatusPending:
		if publication.LeaseUntil.IsZero() {
			return fmt.Errorf("pending publication lease is unavailable")
		}
		return nil
	case publisher.StatusSucceeded, publisher.StatusFailed, publisher.StatusStaleBase, publisher.StatusRebaseRequired:
		if !publication.LeaseUntil.IsZero() {
			return fmt.Errorf("terminal publication retains a lease")
		}
		if publication.Result.Status != publication.Status {
			return fmt.Errorf("publication result status mismatch")
		}
		return publication.Result.Validate()
	default:
		return fmt.Errorf("invalid publication status")
	}
}

func clonePublishParameters(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for name, value := range source {
		cloned[name] = value
	}
	return cloned
}

func publishParametersContainInterpolation(parameters map[string]string) bool {
	for name, value := range parameters {
		if strings.Contains(name, "((") || strings.Contains(value, "((") {
			return true
		}
	}
	return false
}

var _ Step = (*PublishSnapshotStep)(nil)
