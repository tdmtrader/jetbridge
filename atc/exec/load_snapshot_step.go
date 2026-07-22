package exec

import (
	"context"
	"fmt"
	"strings"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/tracing"
)

// WorkflowInputBindingVerifier is the narrow authoritative workflow/build
// binding check used by load_snapshot. The DB implementation performs the
// execution linkage and exact input comparison in one query.
type WorkflowInputBindingVerifier interface {
	InputBindingMatches(context.Context, int, int, snapshot.WorkflowRunID, string, *snapshot.SnapshotRef) (bool, error)
}

type SnapshotUnavailableError struct{}

func (SnapshotUnavailableError) Error() string { return "snapshot unavailable or unauthorized" }

type SnapshotBindingMismatchError struct{}

func (SnapshotBindingMismatchError) Error() string {
	return "snapshot workflow input binding does not match"
}

type LoadSnapshotStep struct {
	planID          atc.PlanID
	plan            atc.LoadSnapshotPlan
	metadata        StepMetadata
	delegateFactory BuildStepDelegateFactory
	metadataStore   snapshot.MetadataStore
	contentStore    snapshot.ContentStore
	bindings        WorkflowInputBindingVerifier
}

func NewLoadSnapshotStep(
	planID atc.PlanID,
	plan atc.LoadSnapshotPlan,
	metadata StepMetadata,
	delegateFactory BuildStepDelegateFactory,
	metadataStore snapshot.MetadataStore,
	contentStore snapshot.ContentStore,
	bindings WorkflowInputBindingVerifier,
) Step {
	return &LoadSnapshotStep{
		planID: planID, plan: plan, metadata: metadata, delegateFactory: delegateFactory,
		metadataStore: metadataStore, contentStore: contentStore, bindings: bindings,
	}
}

func (step *LoadSnapshotStep) Run(ctx context.Context, state RunState) (bool, error) {
	delegate := step.delegateFactory.BuildStepDelegate(state)
	ctx, span := delegate.StartSpan(ctx, "load_snapshot", tracing.Attrs{
		"name": step.plan.Name,
		"type": step.plan.Type.String(),
	})
	ok, err := step.run(ctx, state, delegate)
	tracing.End(span, err)
	return ok, err
}

func (step *LoadSnapshotStep) run(ctx context.Context, state RunState, delegate BuildStepDelegate) (bool, error) {
	logger := lagerctx.FromContext(ctx).Session("load-snapshot-step", lager.Data{
		"step-name": step.plan.Name,
		"type":      step.plan.Type.String(),
		"job-id":    step.metadata.JobID,
	})
	delegate.Initializing(logger)

	if step.metadataStore == nil || step.contentStore == nil {
		return false, fmt.Errorf("load_snapshot: snapshot loading is disabled; enable agent snapshots on the web node")
	}
	if step.metadata.TeamID <= 0 || step.metadata.BuildID <= 0 {
		return false, fmt.Errorf("load_snapshot: server build identity is unavailable")
	}
	if warning, err := atc.ValidateIdentifier(step.plan.Name); err != nil || warning != nil {
		return false, fmt.Errorf("load_snapshot: invalid artifact name")
	}
	if err := step.plan.Type.Validate(); err != nil {
		return false, fmt.Errorf("load_snapshot: invalid snapshot type")
	}
	if strings.Contains(step.plan.ID, "((") || strings.Contains(step.plan.WorkflowRunID, "((") {
		return false, fmt.Errorf("load_snapshot: unresolved identifier parameter")
	}

	var workflowRunID snapshot.WorkflowRunID
	var workflowBound bool
	if step.plan.WorkflowRunID != "" {
		parsed, err := snapshot.ParseWorkflowRunID(step.plan.WorkflowRunID)
		if err != nil {
			return false, fmt.Errorf("load_snapshot: invalid workflow run identifier")
		}
		if step.bindings == nil {
			return false, fmt.Errorf("load_snapshot: workflow input verifier is unavailable")
		}
		workflowRunID = parsed
		workflowBound = true
	}

	delegate.Starting(logger)
	if step.plan.ID == "0" {
		if !step.plan.Optional {
			return false, fmt.Errorf("load_snapshot: snapshot id 0 requires optional input")
		}
		if workflowBound {
			matches, err := step.bindings.InputBindingMatches(
				ctx, step.metadata.TeamID, step.metadata.BuildID, workflowRunID, step.plan.Name, nil,
			)
			if err != nil {
				return false, fmt.Errorf("load_snapshot: workflow input binding verification failed")
			}
			if !matches {
				return false, SnapshotBindingMismatchError{}
			}
		}
		delegate.Finished(logger, true)
		return true, nil
	}

	id, err := snapshot.ParseSnapshotID(step.plan.ID)
	if err != nil {
		return false, fmt.Errorf("load_snapshot: invalid snapshot identifier")
	}
	manifest, found, err := step.metadataStore.GetAuthorized(ctx, step.metadata.TeamID, id)
	if err != nil {
		return false, fmt.Errorf("load_snapshot: metadata authorization failed")
	}
	if !found {
		return false, SnapshotUnavailableError{}
	}
	if err := manifest.Validate(); err != nil {
		return false, fmt.Errorf("load_snapshot: metadata store returned an invalid manifest")
	}
	if manifest.ID != id {
		return false, fmt.Errorf("load_snapshot: metadata store returned a mismatched identity")
	}
	if manifest.Type != step.plan.Type {
		return false, fmt.Errorf("load_snapshot: stored snapshot type does not match the declared type")
	}
	if manifest.ContentState != snapshot.ContentStateAvailable {
		return false, SnapshotUnavailableError{}
	}

	ref := snapshot.SnapshotRef{ID: manifest.ID, Type: manifest.Type, Digest: manifest.Digest}
	if err := ref.Validate(); err != nil {
		return false, fmt.Errorf("load_snapshot: metadata store returned an invalid immutable reference")
	}
	if workflowBound {
		matches, err := step.bindings.InputBindingMatches(
			ctx, step.metadata.TeamID, step.metadata.BuildID, workflowRunID, step.plan.Name, &ref,
		)
		if err != nil {
			return false, fmt.Errorf("load_snapshot: workflow input binding verification failed")
		}
		if !matches {
			return false, SnapshotBindingMismatchError{}
		}
	}

	artifact, err := runtime.NewSnapshotArtifact(manifest, step.contentStore)
	if err != nil {
		return false, fmt.Errorf("load_snapshot: stored snapshot cannot be materialized")
	}
	repository := state.ArtifactRepository()
	name := build.ArtifactName(step.plan.Name)
	if existing, present := repository.ArtifactEntryFor(name); present {
		if identicalSnapshotArtifact(existing, ref, artifact) {
			delegate.Finished(logger, true)
			return true, nil
		}
		return false, fmt.Errorf("load_snapshot: artifact name is already produced")
	}
	if err := repository.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
		name: {Artifact: artifact, FromCache: false, Snapshot: &ref},
	}); err != nil {
		return false, fmt.Errorf("load_snapshot: artifact publication failed")
	}

	delegate.Finished(logger, true)
	return true, nil
}

func identicalSnapshotArtifact(existing build.ArtifactEntry, ref snapshot.SnapshotRef, artifact runtime.Artifact) bool {
	trusted, ok := existing.Artifact.(*runtime.SnapshotArtifact)
	return ok && existing.Snapshot != nil && *existing.Snapshot == ref && trusted.SnapshotRef() == ref &&
		artifact.Handle() == trusted.Handle()
}
