package workflowrun

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot"
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

type monitorSourceLaunch struct {
	teamName    string
	captures    BindingSourceBuildCaptureCoordinator
	snapshots   SnapshotAuthorizer
	coordinator pullrequest.MonitorCoordinator
}

type SourceBuildCaptureCoordinator interface {
	CaptureReady(context.Context, int, int64) (db.ReadySourceAdmission, error)
}

type BindingSourceBuildCaptureCoordinator interface {
	CaptureBindingReady(
		context.Context,
		int,
		int64,
		int64,
	) (db.ReadySourceAdmission, error)
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

// WithPRMonitorSourceLaunch composes the binding-owned scheduler path. Its
// explicit binding capture and coordinator interfaces prevent a monitor build
// from falling through the definition-owned source admission path.
func WithPRMonitorSourceLaunch(
	trustedTeamName string,
	captures BindingSourceBuildCaptureCoordinator,
	snapshots SnapshotAuthorizer,
	coordinator pullrequest.MonitorCoordinator,
) SourceBuildReconcilerOption {
	return func(reconciler *SourceBuildReconciler) error {
		if strings.TrimSpace(trustedTeamName) == "" ||
			nilInterface(captures) || nilInterface(snapshots) ||
			nilInterface(coordinator) {
			return fmt.Errorf(
				"workflowrun: PR monitor source launch dependencies are required",
			)
		}
		reconciler.monitor = &monitorSourceLaunch{
			teamName: trustedTeamName, captures: captures,
			snapshots: snapshots, coordinator: coordinator,
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
	monitor     *monitorSourceLaunch
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
		if pipeline.PRBindingID != nil {
			if reconciler.monitor == nil {
				return fmt.Errorf(
					"workflowrun: binding source pipeline has no monitor launch coordinator",
				)
			}
			binding, err := reconciler.monitor.coordinator.
				ReconcileTerminal(
					ctx, reconciler.teamID, *pipeline.PRBindingID,
				)
			if err != nil {
				return err
			}
			if err := validateMonitorPipelineBinding(
				pipeline, binding,
			); err != nil {
				return err
			}
			if binding.State != pullrequest.BindingActive ||
				binding.Paused || binding.OperatorTerminated {
				continue
			}
		}
		builds, err := reconciler.builds.SuccessfulUnclaimedBuilds(
			ctx, reconciler.teamID, pipeline.PipelineID,
		)
		if err != nil {
			return err
		}
		for _, build := range builds {
			if pipeline.PRBindingID != nil {
				stop, err := reconciler.reconcileMonitorBuild(
					ctx, pipeline, build,
				)
				if err != nil {
					return err
				}
				if stop {
					break
				}
				continue
			}
			if err := reconciler.reconcileBuild(ctx, pipeline, build); err != nil {
				return err
			}
		}
	}
	return nil
}

func (reconciler *SourceBuildReconciler) reconcileMonitorBuild(
	ctx context.Context,
	pipeline db.AgentWorkflowResourceSourcePipeline,
	build db.SourceBuild,
) (bool, error) {
	if pipeline.PRBindingID == nil || *pipeline.PRBindingID <= 0 ||
		build.ID <= 0 || build.TeamID != reconciler.teamID ||
		build.PipelineID != pipeline.PipelineID || build.JobID <= 0 {
		return false, fmt.Errorf(
			"workflowrun: monitor source build is outside binding authority",
		)
	}
	if build.ManuallyTriggered {
		return false, fmt.Errorf(
			"workflowrun: binding monitor source build cannot be manual",
		)
	}
	builds, ok := reconciler.builds.(db.WorkflowResourceSourceBindingBuildStore)
	if !ok || nilInterface(builds) {
		return false, fmt.Errorf(
			"workflowrun: binding source build store is unavailable",
		)
	}
	admissions, ok := reconciler.admissions.(db.WorkflowResourceSourceBindingAdmissionStore)
	if !ok || nilInterface(admissions) {
		return false, fmt.Errorf(
			"workflowrun: binding source admission store is unavailable",
		)
	}
	bindingID := *pipeline.PRBindingID
	var admission db.AgentWorkflowResourceSourceAdmission
	if build.AdmissionID == nil {
		if pipeline.State != db.AgentWorkflowResourceSourcePipelineActive {
			return true, nil
		}
		claim := db.BuildClaim{
			WorkflowDefinitionID: pipeline.WorkflowDefinitionID,
			SourceConfigHash:     pipeline.ConfigHash,
			IdempotencyKey: automaticSourceBuildIdempotencyKey(
				pipeline.PipelineID, build.ID,
			),
		}
		claimed, _, err := admissions.ClaimBindingBuild(
			ctx, reconciler.teamID, bindingID, pipeline.PipelineID,
			int64(build.ID), claim,
		)
		if err != nil {
			return false, err
		}
		admission = claimed
	} else {
		if *build.AdmissionID <= 0 {
			return false, fmt.Errorf(
				"workflowrun: monitor source build has invalid admission",
			)
		}
		admission = db.AgentWorkflowResourceSourceAdmission{
			ID: *build.AdmissionID, TeamID: reconciler.teamID,
			WorkflowDefinitionID: pipeline.WorkflowDefinitionID,
			SourcePipelineID:     pipeline.PipelineID,
			SourceConfigHash:     pipeline.ConfigHash,
			Mode:                 build.AdmissionMode, Status: build.AdmissionStatus,
		}
	}
	if err := validateSourceBuildAdmission(
		admission, pipeline,
	); err != nil {
		return false, err
	}
	if admission.Mode !=
		db.AgentWorkflowResourceSourceAdmissionAutomatic {
		return false, fmt.Errorf(
			"workflowrun: monitor source admission is not automatic",
		)
	}
	if admission.Status ==
		db.AgentWorkflowResourceSourceAdmissionSelecting {
		mapping, found, err := builds.ExactBindingInputMapping(
			ctx, reconciler.teamID, bindingID,
			pipeline.PipelineID, build.ID,
		)
		if err != nil {
			return false, err
		}
		if !found {
			return true, nil
		}
		declared, err := monitorSourceDeclarations(pipeline)
		if err != nil {
			return false, err
		}
		selections, err := sourceBuildSelections(
			admission, pipeline, build.ID, declared, mapping,
		)
		if err != nil {
			return false, err
		}
		if _, err := admissions.BindBindingSelection(
			ctx, reconciler.teamID, bindingID, admission.ID,
			int64(build.ID), selections,
		); err != nil {
			return false, err
		}
	}
	ready, err := reconciler.monitor.captures.CaptureBindingReady(
		ctx, reconciler.teamID, bindingID, admission.ID,
	)
	if err != nil {
		if errors.Is(err, ErrSourceCapturePending) {
			return true, nil
		}
		return false, err
	}
	captured, snapshotID, err := validateReadyMonitorAdmission(
		ready, admission, pipeline, build.ID,
	)
	if err != nil {
		return false, err
	}
	value, found, err := reconciler.monitor.snapshots.GetAuthorized(
		ctx, reconciler.teamID, snapshotID,
	)
	if err != nil {
		return false, err
	}
	if !found || value.ID != snapshotID ||
		value.Type != snapshot.TypeRef("pull-request/v1") ||
		value.Digest.Validate() != nil ||
		value.ContentState != snapshot.ContentStateAvailable {
		return false, fmt.Errorf(
			"workflowrun: captured PR observation is unavailable or drifted",
		)
	}
	source, err := pullrequest.NewMonitorSourceBuild(
		pullrequest.MonitorSourceBuildSpec{
			TeamID:    reconciler.teamID,
			TeamName:  reconciler.monitor.teamName,
			BindingID: bindingID, PipelineID: pipeline.PipelineID,
			BuildID: build.ID, AdmissionID: admission.ID,
			WorkflowDefinitionID: pipeline.WorkflowDefinitionID,
			WorkflowName:         pipeline.WorkflowName,
			WorkflowVersion:      pipeline.WorkflowVersion,
			Observation: snapshot.SnapshotRef{
				ID: value.ID, Type: value.Type, Digest: value.Digest,
			},
			SelectedVersion: cloneSourceBuildVersion(
				captured.Version,
			),
		},
	)
	if err != nil {
		return false, err
	}
	protected, err := source.Protected()
	if err != nil {
		return false, err
	}
	if kind := pullrequest.ActionKind(
		protected.Version.ActionKind,
	); kind == pullrequest.ActionCompleted ||
		kind == pullrequest.ActionAbandoned {
		_, err = reconciler.monitor.coordinator.
			ReconcileDirectTerminal(ctx, protected)
		if errors.Is(err, pullrequest.ErrBindingBusy) {
			return true, nil
		}
		if errors.Is(err, pullrequest.ErrStaleMonitorSourceVersion) {
			if _, failErr := admissions.FailBindingAdmission(
				ctx, reconciler.teamID, bindingID, admission.ID,
				"stale projected binding revision",
			); failErr != nil {
				return false, errors.Join(err, failErr)
			}
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return true, nil
	}
	runID, launched, err := reconciler.monitor.coordinator.
		ReserveAndLaunch(ctx, protected)
	if errors.Is(err, pullrequest.ErrStaleMonitorSourceVersion) {
		if _, failErr := admissions.FailBindingAdmission(
			ctx, reconciler.teamID, bindingID, admission.ID,
			"stale projected binding revision",
		); failErr != nil {
			return false, errors.Join(err, failErr)
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if launched && runID.Validate() != nil {
		return false, fmt.Errorf(
			"workflowrun: monitor coordinator returned invalid run",
		)
	}
	return true, nil
}

func monitorSourceDeclarations(
	pipeline db.AgentWorkflowResourceSourcePipeline,
) ([]workflow.ResourceSource, error) {
	if len(pipeline.SourceDeclarations) != 1 {
		return nil, fmt.Errorf(
			"workflowrun: monitor pipeline must declare one source",
		)
	}
	declaration := pipeline.SourceDeclarations[0]
	if declaration.SourceName != pullrequest.MonitorSourceName ||
		declaration.ResourceName != pullrequest.MonitorResourceName ||
		declaration.SnapshotType != snapshot.TypeRef("pull-request/v1") {
		return nil, fmt.Errorf(
			"workflowrun: monitor pipeline source declaration drifted",
		)
	}
	return []workflow.ResourceSource{{
		Name: declaration.SourceName, Resource: declaration.ResourceName,
		Type: declaration.SnapshotType,
	}}, nil
}

func validateReadyMonitorAdmission(
	ready db.ReadySourceAdmission,
	admission db.AgentWorkflowResourceSourceAdmission,
	pipeline db.AgentWorkflowResourceSourcePipeline,
	buildID int,
) (db.AgentWorkflowResourceSourceBinding, snapshot.SnapshotID, error) {
	if ready.Admission.ID != admission.ID ||
		ready.Admission.TeamID != admission.TeamID ||
		ready.Admission.WorkflowDefinitionID !=
			admission.WorkflowDefinitionID ||
		ready.Admission.SourcePipelineID != admission.SourcePipelineID ||
		ready.Admission.SourceConfigHash != admission.SourceConfigHash ||
		ready.Admission.Mode !=
			db.AgentWorkflowResourceSourceAdmissionAutomatic ||
		ready.Admission.Status !=
			db.AgentWorkflowResourceSourceAdmissionReady ||
		len(ready.Bindings) != 1 {
		return db.AgentWorkflowResourceSourceBinding{}, 0, fmt.Errorf(
			"workflowrun: ready monitor source admission drifted",
		)
	}
	binding := ready.Bindings[0]
	if binding.AdmissionID != admission.ID ||
		binding.SourceName != pullrequest.MonitorSourceName ||
		binding.ResourceName != pullrequest.MonitorResourceName ||
		binding.SelectingBuildID != int64(buildID) ||
		binding.SourcePipelineID != pipeline.PipelineID ||
		binding.PipelineConfigVersion !=
			pipeline.PipelineConfigVersion ||
		binding.SnapshotType != snapshot.TypeRef("pull-request/v1") ||
		binding.SnapshotID == nil ||
		binding.SnapshotID.Validate() != nil ||
		len(binding.Version) != 8 {
		return db.AgentWorkflowResourceSourceBinding{}, 0, fmt.Errorf(
			"workflowrun: captured monitor source binding drifted",
		)
	}
	return binding, *binding.SnapshotID, nil
}

func validateMonitorPipelineBinding(
	pipeline db.AgentWorkflowResourceSourcePipeline,
	binding pullrequest.Binding,
) error {
	if pipeline.PRBindingID == nil ||
		binding.ID != *pipeline.PRBindingID ||
		binding.TeamID != pipeline.TeamID ||
		binding.MonitorWorkflowDefinitionID !=
			pipeline.WorkflowDefinitionID ||
		binding.MonitorWorkflowVersion != pipeline.WorkflowVersion ||
		binding.PipelineID == nil ||
		*binding.PipelineID != pipeline.PipelineID {
		return fmt.Errorf(
			"workflowrun: monitor binding no longer owns source pipeline",
		)
	}
	if binding.State.Validate() != nil {
		return fmt.Errorf("workflowrun: monitor binding state is invalid")
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
