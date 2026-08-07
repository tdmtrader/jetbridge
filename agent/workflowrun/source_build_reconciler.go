package workflowrun

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc/db"
)

type SourceBuildStore = db.WorkflowResourceSourceBuildStore

// SourceBuildPipelineStore exposes durable registry state. The reconciler
// consults it instead of pause timing before creating a fresh automatic claim.
type SourceBuildPipelineStore interface {
	ResourceSourcePipelineLifecycle(
		context.Context,
		int,
	) ([]db.AgentWorkflowResourceSourcePipelineLifecycle, error)
}

// SourceBuildDefinitionStore loads the immutable definition that declared the
// source names, resources, and snapshot types for a registered pipeline.
type SourceBuildDefinitionStore interface {
	Get(string, int) (*workflow.Definition, bool, error)
}

type ReadySourceAdmissionBinder interface {
	BindReadySourceAdmission(
		context.Context,
		AdmissionContext,
		ReadySourceAdmission,
		string,
	) (BindResult, error)
}

type automaticSourceLaunch struct {
	teamName string
	captures SourceBuildCaptureCoordinator
	sources  ResourceSourceAdmitter
	launcher ReadySourceAdmissionBinder
}

type SourceBuildCaptureCoordinator interface {
	CaptureReady(context.Context, int, int64) (db.ReadySourceAdmission, error)
}

type SourceBuildReconcilerOption func(*SourceBuildReconciler) error

// WithAutomaticSourceLaunch closes the server-owned hand-off from a claimed
// scheduler build through capture and into the workflow binder. The ready
// admission never crosses an HTTP request boundary.
func WithAutomaticSourceLaunch(
	trustedTeamName string,
	captures SourceBuildCaptureCoordinator,
	sources ResourceSourceAdmitter,
	launcher ReadySourceAdmissionBinder,
) SourceBuildReconcilerOption {
	return func(reconciler *SourceBuildReconciler) error {
		if strings.TrimSpace(trustedTeamName) == "" ||
			nilInterface(captures) || nilInterface(sources) || nilInterface(launcher) {
			return fmt.Errorf("workflowrun: automatic source launch dependencies are required")
		}
		reconciler.launch = &automaticSourceLaunch{
			teamName: trustedTeamName,
			captures: captures,
			sources:  sources,
			launcher: launcher,
		}
		return nil
	}
}

// SourceBuildReconciler claims ordinary successful source-pipeline builds and
// persists their already-adopted input mapping. When automatic launch is
// composed, selecting/capturing/ready claims remain retryable until the final
// workflow-run row has been allocated.
type SourceBuildReconciler struct {
	teamID      int
	pipelines   SourceBuildPipelineStore
	builds      SourceBuildStore
	admissions  db.WorkflowResourceSourceAdmissionStore
	definitions SourceBuildDefinitionStore
	launch      *automaticSourceLaunch
}

func NewSourceBuildReconciler(
	trustedTeamID int,
	pipelines SourceBuildPipelineStore,
	builds SourceBuildStore,
	admissions db.WorkflowResourceSourceAdmissionStore,
	definitions SourceBuildDefinitionStore,
	options ...SourceBuildReconcilerOption,
) (*SourceBuildReconciler, error) {
	if trustedTeamID <= 0 {
		return nil, fmt.Errorf("workflowrun: source build reconciler team ID must be positive")
	}
	for name, dependency := range map[string]any{
		"source pipeline store":   pipelines,
		"source build store":      builds,
		"source admission store":  admissions,
		"source definition store": definitions,
	} {
		if nilInterface(dependency) {
			return nil, fmt.Errorf("workflowrun: source build reconciler %s is required", name)
		}
	}
	reconciler := &SourceBuildReconciler{
		teamID: trustedTeamID, pipelines: pipelines, builds: builds,
		admissions: admissions, definitions: definitions,
	}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("workflowrun: source build reconciler option is required")
		}
		if err := option(reconciler); err != nil {
			return nil, err
		}
	}
	return reconciler, nil
}

func (reconciler *SourceBuildReconciler) Reconcile(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("workflowrun: source build reconciler context is required")
	}
	pipelines, err := reconciler.pipelines.ResourceSourcePipelineLifecycle(ctx, reconciler.teamID)
	if err != nil {
		return err
	}
	for _, lifecycle := range pipelines {
		pipeline := lifecycle.AgentWorkflowResourceSourcePipeline
		if pipeline.TeamID != reconciler.teamID {
			return fmt.Errorf("workflowrun: source pipeline belongs to another team")
		}
		switch pipeline.State {
		case db.AgentWorkflowResourceSourcePipelineActive,
			db.AgentWorkflowResourceSourcePipelineDraining,
			db.AgentWorkflowResourceSourcePipelineArchived:
		default:
			return fmt.Errorf("workflowrun: invalid source pipeline state %q", pipeline.State)
		}
		builds, err := reconciler.builds.SuccessfulUnclaimedBuilds(
			ctx, reconciler.teamID, pipeline.PipelineID,
		)
		if err != nil {
			return err
		}
		for _, build := range builds {
			if err := reconciler.reconcileBuild(ctx, pipeline, build); err != nil {
				return err
			}
		}
	}
	return nil
}

func (reconciler *SourceBuildReconciler) reconcileBuild(
	ctx context.Context,
	pipeline db.AgentWorkflowResourceSourcePipeline,
	build db.SourceBuild,
) error {
	if build.ID <= 0 || build.TeamID != reconciler.teamID ||
		build.PipelineID != pipeline.PipelineID || build.JobID <= 0 {
		return fmt.Errorf("workflowrun: source build is outside its registered pipeline authority")
	}

	var admission db.AgentWorkflowResourceSourceAdmission
	if build.AdmissionID == nil {
		if build.ManuallyTriggered {
			return fmt.Errorf("workflowrun: manually-triggered source build has no durable manual admission")
		}
		// Registry state—not delayed pipeline pausing—is authoritative for a
		// fresh automatic admission. A prior claim is still allowed to drain.
		if pipeline.State != db.AgentWorkflowResourceSourcePipelineActive {
			return nil
		}
		claim := db.BuildClaim{
			WorkflowDefinitionID: pipeline.WorkflowDefinitionID,
			SourceConfigHash:     pipeline.ConfigHash,
			IdempotencyKey: fmt.Sprintf(
				"source-build:%d:%d", pipeline.PipelineID, build.ID,
			),
		}
		claimed, _, err := reconciler.admissions.ClaimBuild(
			ctx, reconciler.teamID, pipeline.PipelineID, int64(build.ID), claim,
		)
		if err != nil {
			return err
		}
		admission = claimed
	} else {
		if *build.AdmissionID <= 0 {
			return fmt.Errorf("workflowrun: source build has invalid claimed admission")
		}
		admission = db.AgentWorkflowResourceSourceAdmission{
			ID:                   *build.AdmissionID,
			TeamID:               reconciler.teamID,
			WorkflowDefinitionID: pipeline.WorkflowDefinitionID,
			SourcePipelineID:     pipeline.PipelineID,
			SourceConfigHash:     pipeline.ConfigHash,
			Mode:                 build.AdmissionMode,
			Status:               build.AdmissionStatus,
		}
	}
	if err := validateSourceBuildAdmission(admission, pipeline); err != nil {
		return err
	}
	if build.ManuallyTriggered !=
		(admission.Mode == db.AgentWorkflowResourceSourceAdmissionManual) {
		return fmt.Errorf("workflowrun: source build admission mode drifted")
	}

	var definition *workflow.Definition
	if admission.Status == db.AgentWorkflowResourceSourceAdmissionSelecting {
		mapping, found, err := reconciler.builds.ExactInputMapping(
			ctx, reconciler.teamID, pipeline.PipelineID, build.ID,
		)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		definition, err = reconciler.loadDefinition(pipeline)
		if err != nil {
			return err
		}
		selections, err := sourceBuildSelections(
			admission,
			pipeline,
			build.ID,
			definition.Compiled.Function.ResourceSources,
			mapping,
		)
		if err != nil {
			return err
		}
		if _, err = reconciler.admissions.BindSelection(
			ctx, reconciler.teamID, admission.ID, int64(build.ID), selections,
		); err != nil {
			return err
		}
	}
	if reconciler.launch == nil {
		return nil
	}
	if _, err := reconciler.launch.captures.CaptureReady(
		ctx, reconciler.teamID, admission.ID,
	); err != nil {
		if errors.Is(err, ErrSourceCapturePending) {
			return nil
		}
		return err
	}
	// A manual request owns its caller idempotency key. Reconciliation may
	// finish its capture, but only the retrying manual binder may launch it.
	if admission.Mode == db.AgentWorkflowResourceSourceAdmissionManual {
		return nil
	}
	if definition == nil {
		var err error
		definition, err = reconciler.loadDefinition(pipeline)
		if err != nil {
			return err
		}
	}
	target, err := workflow.ResourceSourcePipelineTargetFor(
		*definition, reconciler.teamID,
	)
	if err != nil {
		return fmt.Errorf("workflowrun: derive automatic source target: %w", err)
	}
	ready, err := reconciler.launch.sources.LoadReady(
		ctx, reconciler.teamID, admission.ID, target,
	)
	if err != nil {
		return err
	}
	idempotencyKey := automaticSourceBuildIdempotencyKey(
		pipeline.PipelineID, build.ID,
	)
	// Resource-triggered admission: unattached. The trigger is a standing
	// source pipeline build, not a workflow run, so there is no run whose
	// association could be inherited and nothing explicit to carry.
	_, err = reconciler.launch.launcher.BindReadySourceAdmission(
		ctx,
		AdmissionContext{
			TeamID: reconciler.teamID, TeamName: reconciler.launch.teamName,
			CreatedBy: "workflow-resource-source-reconciler",
			Origin: Origin{
				Kind: "resource-source-build",
				Reference: fmt.Sprintf(
					"pipeline:%d:build:%d", pipeline.PipelineID, build.ID,
				),
			},
		},
		ready,
		idempotencyKey,
	)
	return err
}

func (reconciler *SourceBuildReconciler) loadDefinition(
	pipeline db.AgentWorkflowResourceSourcePipeline,
) (*workflow.Definition, error) {
	definition, found, err := reconciler.definitions.Get(
		pipeline.WorkflowName, pipeline.WorkflowVersion,
	)
	if err != nil {
		return nil, err
	}
	if !found || definition == nil ||
		definition.ID != pipeline.WorkflowDefinitionID {
		return nil, fmt.Errorf("workflowrun: registered source pipeline definition is unavailable or drifted")
	}
	if definition.Compiled.Function == nil {
		return nil, fmt.Errorf("workflowrun: registered source pipeline definition has no function")
	}
	return definition, nil
}

func automaticSourceBuildIdempotencyKey(pipelineID, buildID int) string {
	return fmt.Sprintf("source-build:%d:%d", pipelineID, buildID)
}

func validateSourceBuildAdmission(
	admission db.AgentWorkflowResourceSourceAdmission,
	pipeline db.AgentWorkflowResourceSourcePipeline,
) error {
	if admission.ID <= 0 || admission.TeamID != pipeline.TeamID ||
		admission.WorkflowDefinitionID != pipeline.WorkflowDefinitionID ||
		admission.SourcePipelineID != pipeline.PipelineID ||
		admission.SourceConfigHash != pipeline.ConfigHash {
		return fmt.Errorf("workflowrun: source build admission identity drifted")
	}
	switch admission.Status {
	case db.AgentWorkflowResourceSourceAdmissionSelecting,
		db.AgentWorkflowResourceSourceAdmissionCapturing,
		db.AgentWorkflowResourceSourceAdmissionReady:
	default:
		return fmt.Errorf("workflowrun: source build admission identity drifted")
	}
	if admission.Mode != db.AgentWorkflowResourceSourceAdmissionManual &&
		admission.Mode != db.AgentWorkflowResourceSourceAdmissionAutomatic {
		return fmt.Errorf("workflowrun: source build admission mode is invalid")
	}
	return nil
}

func sourceBuildSelections(
	admission db.AgentWorkflowResourceSourceAdmission,
	pipeline db.AgentWorkflowResourceSourcePipeline,
	buildID int,
	declared []workflow.ResourceSource,
	mapping []db.SelectedSource,
) ([]db.SelectedSource, error) {
	if admission.TeamID != pipeline.TeamID ||
		admission.WorkflowDefinitionID != pipeline.WorkflowDefinitionID ||
		admission.SourcePipelineID != pipeline.PipelineID ||
		admission.SourceConfigHash != pipeline.ConfigHash {
		return nil, fmt.Errorf("workflowrun: source build admission does not match registered pipeline")
	}
	if len(declared) == 0 || len(mapping) != len(declared) {
		return nil, fmt.Errorf("workflowrun: source build input mapping does not cover declared sources")
	}
	byName := make(map[string]db.SelectedSource, len(mapping))
	for _, selected := range mapping {
		if selected.SelectingBuildID != int64(buildID) || selected.SourceName == "" ||
			selected.ResourceName == "" || selected.ResourceID <= 0 ||
			selected.ResourceConfigVersionID <= 0 || selected.ResourceVersionID <= 0 ||
			selected.VersionDigest == "" || len(selected.Version) == 0 {
			return nil, fmt.Errorf("workflowrun: source build mapping has invalid exact input")
		}
		if _, duplicate := byName[selected.SourceName]; duplicate {
			return nil, fmt.Errorf("workflowrun: source build mapping has duplicate source %q", selected.SourceName)
		}
		selected.Version = cloneSourceBuildVersion(selected.Version)
		byName[selected.SourceName] = selected
	}

	selections := make([]db.SelectedSource, 0, len(declared))
	for _, source := range declared {
		selected, found := byName[source.Name]
		if !found || selected.ResourceName != source.Resource {
			return nil, fmt.Errorf("workflowrun: source build mapping does not match declaration %q", source.Name)
		}
		selected.SnapshotType = source.Type
		captureKey, err := db.WorkflowResourceSourceCaptureOperationKey(
			pipeline.TeamID,
			pipeline.WorkflowDefinitionID,
			pipeline.PipelineID,
			pipeline.PipelineConfigVersion,
			source.Name,
			source.Resource,
			selected.VersionDigest,
			source.Type,
		)
		if err != nil {
			return nil, fmt.Errorf("workflowrun: derive Task 12 source capture operation key: %w", err)
		}
		selected.CaptureOperationKey = captureKey
		selections = append(selections, selected)
	}
	sort.Slice(selections, func(left, right int) bool {
		return selections[left].SourceName < selections[right].SourceName
	})
	return selections, nil
}
