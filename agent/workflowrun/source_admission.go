package workflowrun

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

var ErrSourceCapturePending = errors.New("workflowrun: source admission capture is pending")

// SourceCaptureAdmissionStore exposes only the durable selection/capture
// boundary. Scheduler candidate calculation deliberately does not appear in
// this interface: all versions and operation keys must already be persisted.
type SourceCaptureAdmissionStore interface {
	Ready(context.Context, int, int64) (db.ReadySourceAdmission, bool, error)
	Capturing(context.Context, int, int64) (db.CapturingSourceAdmission, bool, error)
	BindCapture(context.Context, int, int64, string, snapshot.SnapshotID) (bool, error)
}

type SourceCaptureDefinitionStore interface {
	Get(string, int) (*workflow.Definition, bool, error)
}

// PersistedSourceCapture is the exact, internal input for the resourcecapture
// adapter. Its fields originate from the locked admission/pipeline/binding
// rows plus the immutable workflow definition registered for that pipeline.
// It intentionally has no latest policy or caller-provided version field.
type PersistedSourceCapture struct {
	AdmissionID             int64
	WorkflowDefinitionID    int
	SourceName              string
	TeamID                  int
	TeamName                string
	SourcePipelineID        int
	PipelineID              int
	Pipeline                atc.PipelineRef
	PipelineConfigVersion   int
	ResourceID              int
	Resource                atc.ResourceConfig
	ResourceTypes           atc.ResourceTypes
	ResourceConfigVersionID int64
	ResourceVersionID       int64
	VersionDigest           string
	Version                 atc.Version
	SnapshotType            snapshot.TypeRef
	CaptureOperationKey     string
}

type SourceCaptureResult struct {
	Snapshot *snapshot.Snapshot
}

// PersistedSourceCapturer is implemented by the trusted resource-capture
// adapter at composition time. Keeping this narrow data interface in
// workflowrun avoids a package cycle with resourcecapture's template adapter.
type PersistedSourceCapturer interface {
	CapturePersistedSource(context.Context, PersistedSourceCapture) (SourceCaptureResult, error)
}

type SourceCaptureCoordinator struct {
	teamID      int
	teamName    string
	admissions  SourceCaptureAdmissionStore
	definitions SourceCaptureDefinitionStore
	capturer    PersistedSourceCapturer
}

func NewSourceCaptureCoordinator(
	trustedTeamID int,
	trustedTeamName string,
	admissions SourceCaptureAdmissionStore,
	definitions SourceCaptureDefinitionStore,
	capturer PersistedSourceCapturer,
) (*SourceCaptureCoordinator, error) {
	if trustedTeamID <= 0 || strings.TrimSpace(trustedTeamName) == "" {
		return nil, fmt.Errorf("workflowrun: source capture coordinator requires trusted team identity")
	}
	for name, dependency := range map[string]any{
		"admission store": admissions, "definition store": definitions, "capturer": capturer,
	} {
		if nilInterface(dependency) {
			return nil, fmt.Errorf("workflowrun: source capture coordinator %s is required", name)
		}
	}
	return &SourceCaptureCoordinator{
		teamID: trustedTeamID, teamName: trustedTeamName,
		admissions: admissions, definitions: definitions, capturer: capturer,
	}, nil
}

// CaptureReady idempotently completes one durable source admission. A ready
// admission returns before the selection reader or capture adapter is called;
// a partial earlier pass skips bindings that already have their snapshot.
func (coordinator *SourceCaptureCoordinator) CaptureReady(
	ctx context.Context,
	teamID int,
	admissionID int64,
) (db.ReadySourceAdmission, error) {
	if ctx == nil || teamID != coordinator.teamID || admissionID <= 0 {
		return db.ReadySourceAdmission{}, fmt.Errorf("workflowrun: source capture requires trusted team and admission")
	}
	ready, found, err := coordinator.admissions.Ready(ctx, teamID, admissionID)
	if err != nil {
		return db.ReadySourceAdmission{}, err
	}
	if found {
		if ready.Admission.TeamID != teamID || ready.Admission.ID != admissionID ||
			ready.Admission.Status != db.AgentWorkflowResourceSourceAdmissionReady {
			return db.ReadySourceAdmission{}, fmt.Errorf("workflowrun: ready source admission identity drifted")
		}
		return ready, nil
	}

	capturing, found, err := coordinator.admissions.Capturing(ctx, teamID, admissionID)
	if err != nil {
		return db.ReadySourceAdmission{}, err
	}
	if !found {
		return db.ReadySourceAdmission{}, ErrSourceCapturePending
	}
	selections, err := coordinator.persistedSelections(ctx, capturing)
	if err != nil {
		return db.ReadySourceAdmission{}, err
	}
	if err := coordinator.captureSelections(
		ctx, selections,
		func(sourceName string, snapshotID snapshot.SnapshotID) error {
			_, err := coordinator.admissions.BindCapture(
				ctx, teamID, admissionID, sourceName, snapshotID,
			)
			return err
		},
	); err != nil {
		return db.ReadySourceAdmission{}, err
	}
	ready, found, err = coordinator.admissions.Ready(ctx, teamID, admissionID)
	if err != nil {
		return db.ReadySourceAdmission{}, err
	}
	if !found {
		return db.ReadySourceAdmission{}, ErrSourceCapturePending
	}
	if ready.Admission.TeamID != teamID || ready.Admission.ID != admissionID ||
		ready.Admission.Status != db.AgentWorkflowResourceSourceAdmissionReady {
		return db.ReadySourceAdmission{}, fmt.Errorf("workflowrun: ready source admission identity drifted")
	}
	return ready, nil
}

func (coordinator *SourceCaptureCoordinator) captureSelections(
	ctx context.Context,
	selections []persistedCaptureBinding,
	bind func(string, snapshot.SnapshotID) error,
) error {
	for _, selection := range selections {
		if selection.snapshotID != nil {
			continue
		}
		result, err := coordinator.capturer.CapturePersistedSource(
			ctx, selection.PersistedSourceCapture,
		)
		if err != nil {
			return err
		}
		if result.Snapshot == nil {
			return ErrSourceCapturePending
		}
		if result.Snapshot.ID <= 0 ||
			result.Snapshot.Type != selection.SnapshotType {
			return fmt.Errorf(
				"workflowrun: capture result does not match its declared source type",
			)
		}
		if err := bind(selection.SourceName, result.Snapshot.ID); err != nil {
			return err
		}
	}
	return nil
}

type persistedCaptureBinding struct {
	PersistedSourceCapture
	snapshotID *snapshot.SnapshotID
}

func (coordinator *SourceCaptureCoordinator) persistedSelections(
	ctx context.Context,
	capturing db.CapturingSourceAdmission,
) ([]persistedCaptureBinding, error) {
	admission := capturing.Admission
	registered := capturing.PipelineRegistration
	if admission.ID <= 0 || admission.TeamID != coordinator.teamID ||
		admission.Status != db.AgentWorkflowResourceSourceAdmissionCapturing ||
		registered.TeamID != coordinator.teamID ||
		registered.PipelineID != admission.SourcePipelineID ||
		registered.WorkflowDefinitionID != admission.WorkflowDefinitionID ||
		registered.ConfigHash != admission.SourceConfigHash ||
		registered.PipelineConfigVersion <= 0 || capturing.TeamName != coordinator.teamName ||
		strings.TrimSpace(capturing.Pipeline.Name) == "" || len(capturing.Bindings) == 0 {
		return nil, fmt.Errorf("workflowrun: capturing source admission identity drifted")
	}
	definition, found, err := coordinator.definitions.Get(
		registered.WorkflowName, registered.WorkflowVersion,
	)
	if err != nil {
		return nil, err
	}
	if !found || definition == nil || definition.ID != admission.WorkflowDefinitionID || definition.Compiled.Function == nil {
		return nil, fmt.Errorf("workflowrun: capturing source admission definition is unavailable or drifted")
	}
	function := definition.Compiled.Function
	resourceTypes, err := workflow.ResourceSourceResourceTypes(
		function.ResourceSources, function.Resources, function.ResourceTypes,
	)
	if err != nil {
		return nil, err
	}
	sources := make(map[string]workflow.ResourceSource, len(function.ResourceSources))
	for _, source := range function.ResourceSources {
		if _, duplicate := sources[source.Name]; duplicate {
			return nil, fmt.Errorf("workflowrun: capturing source declaration is duplicated")
		}
		sources[source.Name] = source
	}
	bindings := append([]db.AgentWorkflowResourceSourceBinding(nil), capturing.Bindings...)
	sort.Slice(bindings, func(left, right int) bool { return bindings[left].SourceName < bindings[right].SourceName })
	selections := make([]persistedCaptureBinding, 0, len(bindings))
	seen := map[string]struct{}{}
	for _, binding := range bindings {
		if _, duplicate := seen[binding.SourceName]; duplicate {
			return nil, fmt.Errorf("workflowrun: capturing source binding is duplicated")
		}
		seen[binding.SourceName] = struct{}{}
		source, found := sources[binding.SourceName]
		if !found || source.Resource != binding.ResourceName || source.Type != binding.SnapshotType ||
			binding.AdmissionID != admission.ID || binding.SourcePipelineID != registered.PipelineID ||
			binding.PipelineConfigVersion != registered.PipelineConfigVersion || binding.ResourceID <= 0 ||
			binding.ResourceConfigVersionID <= 0 || binding.ResourceVersionID <= 0 ||
			len(binding.Version) == 0 || strings.TrimSpace(binding.VersionDigest) == "" ||
			strings.TrimSpace(binding.CaptureOperationKey) == "" {
			return nil, fmt.Errorf("workflowrun: persisted source binding drifted")
		}
		resource, found := function.Resources.Lookup(binding.ResourceName)
		if !found {
			return nil, fmt.Errorf("workflowrun: persisted source resource is unavailable")
		}
		expectedOperation, err := db.WorkflowResourceSourceCaptureOperationKey(
			coordinator.teamID,
			admission.WorkflowDefinitionID,
			registered.PipelineID,
			registered.PipelineConfigVersion,
			binding.SourceName,
			binding.ResourceName,
			binding.VersionDigest,
			binding.SnapshotType,
		)
		if err != nil || binding.CaptureOperationKey != expectedOperation {
			return nil, fmt.Errorf("workflowrun: persisted source capture operation identity drifted")
		}
		selections = append(selections, persistedCaptureBinding{
			PersistedSourceCapture: PersistedSourceCapture{
				AdmissionID: admission.ID, WorkflowDefinitionID: admission.WorkflowDefinitionID, SourceName: binding.SourceName,
				TeamID: coordinator.teamID, TeamName: coordinator.teamName,
				SourcePipelineID: registered.PipelineID, PipelineID: registered.PipelineID,
				Pipeline: capturing.Pipeline, PipelineConfigVersion: registered.PipelineConfigVersion,
				ResourceID: binding.ResourceID, Resource: resource, ResourceTypes: resourceTypes,
				ResourceConfigVersionID: binding.ResourceConfigVersionID,
				ResourceVersionID:       binding.ResourceVersionID, VersionDigest: binding.VersionDigest,
				Version: cloneSourceBuildVersion(binding.Version), SnapshotType: binding.SnapshotType,
				CaptureOperationKey: binding.CaptureOperationKey,
			},
			snapshotID: binding.SnapshotID,
		})
	}
	if len(sources) != len(selections) {
		return nil, fmt.Errorf("workflowrun: persisted source bindings do not cover declarations")
	}
	return selections, nil
}

func cloneSourceBuildVersion(version atc.Version) atc.Version {
	cloned := make(atc.Version, len(version))
	for key, value := range version {
		cloned[key] = value
	}
	return cloned
}
