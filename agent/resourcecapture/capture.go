// Package resourcecapture turns one exact, already-known Concourse resource
// version into a durable typed snapshot through a server-owned one-shot
// pipeline. The resource source remains ordinary Concourse configuration; the
// persisted production metadata contains only safe identifiers and hashes.
package resourcecapture

import (
	"bytes"
	"context"
	"crypto/md5" // #nosec G501 -- validates legacy persisted Concourse version digests.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/mitchellh/copystructure"
)

const (
	AdapterResourceVersion = "resource-version"
	templateNamePrefix     = "agent-resource-capture-"
	outputPort             = "snapshot"
	captureJob             = "capture"
)

var (
	ErrNotFound          = errors.New("resource version not found")
	ErrDisabled          = errors.New("resource version is disabled")
	ErrTypeRequired      = errors.New("snapshot type is required for this resource type")
	ErrUnavailable       = errors.New("resource capture is unavailable")
	ErrTemplateConflict  = errors.New("resource capture immutable template conflicts with existing state")
	ErrOutputUnavailable = errors.New("resource capture output is unavailable or unauthorized")
	// ErrPersistedSelectionDrift rejects a capture before it can create a
	// template when current ATC state no longer proves the exact scheduler
	// selection persisted by a source admission.
	ErrPersistedSelectionDrift = errors.New("resource capture persisted selection drifted")
)

type Request struct {
	TeamID    int              `json:"-"`
	TeamName  string           `json:"-"`
	Pipeline  atc.PipelineRef  `json:"pipeline"`
	Resource  string           `json:"resource"`
	Version   atc.Version      `json:"version"`
	Type      snapshot.TypeRef `json:"type,omitempty"`
	CreatedBy string           `json:"-"`
	Actor     string           `json:"-"`
	// RetryPipelineRunID authorizes exactly one new generation after the
	// caller observed that terminal failed generation. Missing-output retries
	// are derived internally after output authorization fails closed.
	RetryPipelineRunID int64 `json:"-"`
}

func (request Request) Validate() error {
	if request.TeamID <= 0 || strings.TrimSpace(request.TeamName) == "" {
		return fmt.Errorf("resource capture: trusted team identity is required")
	}
	if strings.TrimSpace(request.Pipeline.Name) == "" {
		return fmt.Errorf("resource capture: pipeline is required")
	}
	if strings.TrimSpace(request.Resource) == "" {
		return fmt.Errorf("resource capture: resource is required")
	}
	if len(request.Version) == 0 {
		return fmt.Errorf("resource capture: exact resource version is required")
	}
	for key, value := range request.Version {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			return fmt.Errorf("resource capture: version keys and values must not be empty")
		}
	}
	if request.Type != "" {
		if err := request.Type.Validate(); err != nil {
			return fmt.Errorf("resource capture: type: %w", err)
		}
	}
	if strings.TrimSpace(request.CreatedBy) == "" || strings.TrimSpace(request.Actor) == "" {
		return fmt.Errorf("resource capture: trusted creator and actor are required")
	}
	if request.RetryPipelineRunID < 0 {
		return fmt.Errorf("resource capture: retry pipeline run ID must not be negative")
	}
	return nil
}

type ResolveRequest struct {
	TeamID   int
	TeamName string
	Pipeline atc.PipelineRef
	Resource string
	Version  atc.Version
}

func (request ResolveRequest) Clone() ResolveRequest {
	request.Pipeline.InstanceVars = cloneInstanceVars(request.Pipeline.InstanceVars)
	request.Version = cloneVersion(request.Version)
	return request
}

// ResolvedResource is a trusted, pipeline-scoped resolution. Resource contains
// the fully rendered source used by the get step and must therefore never be
// copied into SourceMetadata.
type ResolvedResource struct {
	TeamID                  int
	TeamName                string
	PipelineID              int
	Pipeline                atc.PipelineRef
	PipelineConfigVersion   int
	ResourceID              int
	Resource                atc.ResourceConfig
	ResourceTypes           atc.ResourceTypes
	ResourceConfigVersionID int
	ResourceVersionID       int
	Version                 atc.Version
	Enabled                 bool
	CapturedAt              time.Time
}

// PersistedSelection is server-derived source-admission evidence. It is used
// only by the composition adapter after the admission store has already
// verified the selecting build; public Capture requests cannot provide it.
type PersistedSelection struct {
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
	ResourceConfigVersionID int
	ResourceVersionID       int
	VersionDigest           string
	Version                 atc.Version
	SnapshotType            snapshot.TypeRef
	CaptureOperationKey     string
}

func (selection PersistedSelection) Clone() PersistedSelection {
	selection.Pipeline.InstanceVars = cloneInstanceVars(selection.Pipeline.InstanceVars)
	selection.Version = cloneVersion(selection.Version)
	if copied, err := copystructure.Copy(selection.Resource); err == nil {
		selection.Resource = copied.(atc.ResourceConfig)
	}
	if copied, err := copystructure.Copy(selection.ResourceTypes); err == nil {
		selection.ResourceTypes = copied.(atc.ResourceTypes)
	}
	return selection
}

func (selection PersistedSelection) validate() error {
	if selection.AdmissionID <= 0 || selection.WorkflowDefinitionID <= 0 || strings.TrimSpace(selection.SourceName) == "" ||
		selection.TeamID <= 0 || strings.TrimSpace(selection.TeamName) == "" ||
		selection.SourcePipelineID <= 0 || selection.PipelineID <= 0 ||
		selection.SourcePipelineID != selection.PipelineID ||
		strings.TrimSpace(selection.Pipeline.Name) == "" ||
		selection.PipelineConfigVersion <= 0 || selection.ResourceID <= 0 ||
		strings.TrimSpace(selection.Resource.Name) == "" ||
		strings.TrimSpace(selection.Resource.Type) == "" ||
		selection.ResourceConfigVersionID <= 0 || selection.ResourceVersionID <= 0 ||
		len(selection.Version) == 0 || strings.TrimSpace(selection.CaptureOperationKey) == "" {
		return fmt.Errorf("%w: invalid durable selection identity", ErrPersistedSelectionDrift)
	}
	if err := selection.SnapshotType.Validate(); err != nil {
		return fmt.Errorf("%w: invalid declared snapshot type: %v", ErrPersistedSelectionDrift, err)
	}
	if err := validateVersionDigest(selection.VersionDigest, selection.Version); err != nil {
		return fmt.Errorf("%w: %v", ErrPersistedSelectionDrift, err)
	}
	expected, err := db.WorkflowResourceSourceCaptureOperationKey(
		selection.TeamID, selection.WorkflowDefinitionID, selection.SourcePipelineID, selection.PipelineConfigVersion,
		selection.SourceName, selection.Resource.Name, selection.VersionDigest, selection.SnapshotType,
	)
	if err != nil || !sourceCaptureOperationKeyPattern.MatchString(selection.CaptureOperationKey) || selection.CaptureOperationKey != expected {
		return fmt.Errorf("%w: capture operation key is invalid", ErrPersistedSelectionDrift)
	}
	return nil
}

func (resolved ResolvedResource) validate() error {
	if resolved.TeamID <= 0 || strings.TrimSpace(resolved.TeamName) == "" || strings.TrimSpace(resolved.Pipeline.Name) == "" {
		return fmt.Errorf("resource capture: resolver returned an invalid scope")
	}
	if strings.TrimSpace(resolved.Resource.Name) == "" || strings.TrimSpace(resolved.Resource.Type) == "" || len(resolved.Version) == 0 {
		return fmt.Errorf("resource capture: resolver returned an invalid resource version")
	}
	if resolved.ResourceConfigVersionID <= 0 {
		return fmt.Errorf("resource capture: resolver returned an invalid resource config version")
	}
	return nil
}

type Resolver interface {
	Resolve(context.Context, ResolveRequest) (ResolvedResource, bool, error)
}

type TemplateSpec struct {
	TeamID        int
	TeamName      string
	Name          string
	OperationKey  string
	FullHash      string
	CanonicalJSON json.RawMessage
	Config        atc.Config
}

func newTemplateSpec(resolved ResolvedResource, operationKey string, config atc.Config, canonical []byte) (TemplateSpec, error) {
	if len(operationKey) < 24 {
		return TemplateSpec{}, fmt.Errorf("resource capture: operation key is too short for immutable template identity")
	}
	targetHash, err := workflow.TargetConfigHash(config)
	if err != nil {
		return TemplateSpec{}, fmt.Errorf("resource capture: hash template config: %w", err)
	}
	if len(targetHash) < 12 {
		return TemplateSpec{}, fmt.Errorf("resource capture: template config hash is too short")
	}
	configDigest := sha256.Sum256(canonical)
	return TemplateSpec{
		TeamID: resolved.TeamID, TeamName: resolved.TeamName,
		Name: templateNamePrefix + operationKey[:24] + "-" + targetHash[:12], OperationKey: operationKey,
		FullHash: hex.EncodeToString(configDigest[:]), CanonicalJSON: canonical, Config: config,
	}, nil
}

func (spec TemplateSpec) Clone() TemplateSpec {
	spec.CanonicalJSON = append(json.RawMessage(nil), spec.CanonicalJSON...)
	if copied, err := copystructure.Copy(spec.Config); err == nil {
		spec.Config = copied.(atc.Config)
	}
	return spec
}

type TemplateRef struct {
	ID            int
	TeamID        int
	Name          string
	ConfigVersion int
	FullHash      string
}

type TemplateStore interface {
	SaveOrReuse(context.Context, TemplateSpec) (TemplateRef, error)
}

type ExecutionRequest struct {
	TeamID             int
	TeamName           string
	OperationKey       string
	Template           TemplateRef
	CreatedBy          string
	RetryPipelineRunID int64
}

type Execution struct {
	PipelineRunID      int64                `json:"pipeline_run_id"`
	TemplatePipelineID int                  `json:"template_pipeline_id"`
	InstancePipelineID int                  `json:"instance_pipeline_id"`
	Status             db.PipelineRunStatus `json:"status"`
}

func (execution Execution) validate() error {
	if execution.PipelineRunID <= 0 || execution.TemplatePipelineID <= 0 || execution.InstancePipelineID <= 0 {
		return fmt.Errorf("resource capture: execution store returned invalid identifiers")
	}
	switch execution.Status {
	case db.PipelineRunRunning, db.PipelineRunSucceeded, db.PipelineRunFailed, db.PipelineRunErrored, db.PipelineRunAborted:
		return nil
	default:
		return fmt.Errorf("resource capture: execution store returned invalid status %q", execution.Status)
	}
}

type ExecutionStore interface {
	StartOrGet(context.Context, ExecutionRequest) (Execution, bool, error)
}

type OutputRequest struct {
	TeamID        int
	TeamName      string
	PipelineRunID int64
	OperationKey  string
	OutputPort    string
	ExpectedType  snapshot.TypeRef
	Actor         string
}

type OutputStore interface {
	Finalize(context.Context, OutputRequest) (snapshot.Snapshot, bool, error)
}

type SourceMetadata struct {
	Adapter                 string           `json:"adapter"`
	OperationKey            string           `json:"operation_key"`
	Team                    string           `json:"team"`
	Pipeline                string           `json:"pipeline"`
	PipelineInstanceVars    atc.InstanceVars `json:"pipeline_instance_vars,omitempty"`
	PipelineConfigVersion   int              `json:"pipeline_config_version"`
	Resource                string           `json:"resource"`
	ResourceType            string           `json:"resource_type"`
	ResourceConfigVersionID int              `json:"resource_config_version_id"`
	ResourceVersionID       int              `json:"resource_version_id,omitempty"`
	ResourceConfigHash      string           `json:"resource_config_hash"`
	Version                 atc.Version      `json:"version"`
	SnapshotType            snapshot.TypeRef `json:"snapshot_type"`
}

type CaptureResult struct {
	OperationKey string             `json:"operation_key"`
	Created      bool               `json:"created"`
	Execution    Execution          `json:"execution"`
	Snapshot     *snapshot.Snapshot `json:"snapshot,omitempty"`
}

type Capturer struct {
	resolver   Resolver
	templates  TemplateStore
	executions ExecutionStore
	outputs    OutputStore
	taskImage  string
}

func NewCapturer(resolver Resolver, templates TemplateStore, executions ExecutionStore, outputs OutputStore, taskImage string) (*Capturer, error) {
	for name, dependency := range map[string]any{
		"resolver": resolver, "template store": templates, "execution store": executions, "output store": outputs,
	} {
		if isNil(dependency) {
			return nil, fmt.Errorf("resource capture: %s is required", name)
		}
	}
	return &Capturer{resolver: resolver, templates: templates, executions: executions, outputs: outputs, taskImage: strings.TrimSpace(taskImage)}, nil
}

func (capturer *Capturer) Capture(ctx context.Context, request Request) (CaptureResult, error) {
	if err := contextError(ctx); err != nil {
		return CaptureResult{}, err
	}
	if err := request.Validate(); err != nil {
		return CaptureResult{}, err
	}
	resolved, found, err := capturer.resolver.Resolve(ctx, ResolveRequest{
		TeamID: request.TeamID, TeamName: request.TeamName, Pipeline: request.Pipeline,
		Resource: request.Resource, Version: cloneVersion(request.Version),
	})
	if err != nil {
		return CaptureResult{}, fmt.Errorf("resource capture: resolve exact version: %w", err)
	}
	if !found {
		return CaptureResult{}, ErrNotFound
	}
	if err := resolved.validate(); err != nil {
		return CaptureResult{}, err
	}
	if !resolved.Enabled {
		return CaptureResult{}, ErrDisabled
	}

	snapshotType := request.Type
	if snapshotType == "" {
		if !resourceTypeDerivesFrom(resolved.Resource.Type, "git", resolved.ResourceTypes) {
			return CaptureResult{}, ErrTypeRequired
		}
		snapshotType = snapshot.TypeRef("repository/v1")
	}
	if err := snapshotType.Validate(); err != nil {
		return CaptureResult{}, fmt.Errorf("resource capture: type: %w", err)
	}
	if capturer.taskImage == "" {
		return CaptureResult{}, fmt.Errorf("%w: agent step image is not configured", ErrUnavailable)
	}
	if err := atc.ValidatePinnedOCIImage(capturer.taskImage); err != nil {
		return CaptureResult{}, fmt.Errorf("%w: agent step image must be immutable: %v", ErrUnavailable, err)
	}

	operationKey, resourceHash, err := captureIdentity(resolved, snapshotType, capturer.taskImage)
	if err != nil {
		return CaptureResult{}, err
	}
	metadata, err := json.Marshal(SourceMetadata{
		Adapter: AdapterResourceVersion, OperationKey: operationKey, Team: resolved.TeamName,
		Pipeline: resolved.Pipeline.Name, PipelineInstanceVars: cloneInstanceVars(resolved.Pipeline.InstanceVars),
		PipelineConfigVersion: resolved.PipelineConfigVersion, Resource: resolved.Resource.Name,
		ResourceType: resolved.Resource.Type, ResourceConfigVersionID: resolved.ResourceConfigVersionID,
		ResourceVersionID: resolved.ResourceVersionID, ResourceConfigHash: resourceHash,
		Version: cloneVersion(resolved.Version), SnapshotType: snapshotType,
	})
	if err != nil {
		return CaptureResult{}, fmt.Errorf("resource capture: encode source metadata: %w", err)
	}
	config, err := captureConfig(resolved, snapshotType, metadata, capturer.taskImage)
	if err != nil {
		return CaptureResult{}, err
	}
	canonical, err := config.CanonicalJSON()
	if err != nil {
		return CaptureResult{}, fmt.Errorf("resource capture: canonicalize template: %w", err)
	}
	spec, err := newTemplateSpec(resolved, operationKey, config, canonical)
	if err != nil {
		return CaptureResult{}, err
	}
	template, err := capturer.templates.SaveOrReuse(ctx, spec)
	if err != nil {
		return CaptureResult{}, fmt.Errorf("resource capture: save immutable template: %w", err)
	}
	if template.ID <= 0 || template.Name != spec.Name {
		return CaptureResult{}, fmt.Errorf("resource capture: template store returned a mismatched template")
	}
	executionRequest := ExecutionRequest{
		TeamID: resolved.TeamID, TeamName: resolved.TeamName, OperationKey: operationKey,
		Template: template, CreatedBy: request.CreatedBy,
	}
	// A stale caller can discover that another caller already won a retry and
	// completed it. Bound the hand-off loop so a corrupt store cannot cycle
	// succeeded generations forever.
	for handoffs := 0; handoffs < 4; handoffs++ {
		execution, created, err := capturer.executions.StartOrGet(ctx, executionRequest)
		if err != nil {
			return CaptureResult{}, fmt.Errorf("resource capture: start execution: %w", err)
		}
		if err := execution.validate(); err != nil {
			return CaptureResult{}, err
		}
		if execution.TemplatePipelineID != template.ID {
			return CaptureResult{}, fmt.Errorf("resource capture: execution belongs to a different template")
		}
		result := CaptureResult{OperationKey: operationKey, Created: created, Execution: execution}
		if execution.Status == db.PipelineRunFailed || execution.Status == db.PipelineRunErrored || execution.Status == db.PipelineRunAborted {
			if executionRequest.RetryPipelineRunID == 0 && request.RetryPipelineRunID == execution.PipelineRunID {
				executionRequest.RetryPipelineRunID = execution.PipelineRunID
				continue
			}
			return result, nil
		}
		if execution.Status != db.PipelineRunSucceeded {
			return result, nil
		}
		manifest, _, err := capturer.outputs.Finalize(ctx, OutputRequest{
			TeamID: resolved.TeamID, TeamName: resolved.TeamName, PipelineRunID: execution.PipelineRunID,
			OperationKey: operationKey, OutputPort: outputPort, ExpectedType: snapshotType, Actor: request.Actor,
		})
		if errors.Is(err, ErrOutputUnavailable) {
			executionRequest.RetryPipelineRunID = execution.PipelineRunID
			continue
		}
		if err != nil {
			return CaptureResult{}, fmt.Errorf("resource capture: finalize output: %w", err)
		}
		if manifest.Type != snapshotType {
			return CaptureResult{}, fmt.Errorf("resource capture: output type %q does not match %q", manifest.Type, snapshotType)
		}
		result.Snapshot = &manifest
		return result, nil
	}
	return CaptureResult{}, fmt.Errorf("resource capture: retry generations did not converge")
}

// CapturePersistedSelection seals only an exact selection that has already
// been derived from a durable standing-pipeline build. It re-resolves that
// exact version and fails closed if any ownership/configuration/version field
// has changed before a capture template or build can be created.
func (capturer *Capturer) CapturePersistedSelection(ctx context.Context, selection PersistedSelection) (CaptureResult, error) {
	if err := contextError(ctx); err != nil {
		return CaptureResult{}, err
	}
	selection = selection.Clone()
	if err := selection.validate(); err != nil {
		return CaptureResult{}, err
	}
	resolved, found, err := capturer.resolver.Resolve(ctx, ResolveRequest{
		TeamID: selection.TeamID, TeamName: selection.TeamName, Pipeline: selection.Pipeline,
		Resource: selection.Resource.Name, Version: cloneVersion(selection.Version),
	})
	if err != nil {
		return CaptureResult{}, fmt.Errorf("resource capture: resolve persisted exact version: %w", err)
	}
	if !found {
		return CaptureResult{}, fmt.Errorf("%w: exact resource version is unavailable", ErrPersistedSelectionDrift)
	}
	if err := resolved.validate(); err != nil {
		return CaptureResult{}, fmt.Errorf("%w: resolver returned invalid evidence: %v", ErrPersistedSelectionDrift, err)
	}
	if err := verifyPersistedSelection(selection, resolved); err != nil {
		return CaptureResult{}, err
	}
	if !resolved.Enabled {
		return CaptureResult{}, ErrDisabled
	}
	if capturer.taskImage == "" {
		return CaptureResult{}, fmt.Errorf("%w: agent step image is not configured", ErrUnavailable)
	}
	if err := atc.ValidatePinnedOCIImage(capturer.taskImage); err != nil {
		return CaptureResult{}, fmt.Errorf("%w: agent step image must be immutable: %v", ErrUnavailable, err)
	}
	_, resourceHash, err := captureIdentity(resolved, selection.SnapshotType, capturer.taskImage)
	if err != nil {
		return CaptureResult{}, err
	}
	metadata, err := json.Marshal(SourceMetadata{
		Adapter: AdapterResourceVersion, OperationKey: selection.CaptureOperationKey, Team: resolved.TeamName,
		Pipeline: resolved.Pipeline.Name, PipelineInstanceVars: cloneInstanceVars(resolved.Pipeline.InstanceVars),
		PipelineConfigVersion: resolved.PipelineConfigVersion, Resource: resolved.Resource.Name,
		ResourceType: resolved.Resource.Type, ResourceConfigVersionID: resolved.ResourceConfigVersionID,
		ResourceVersionID: resolved.ResourceVersionID, ResourceConfigHash: resourceHash,
		Version: cloneVersion(resolved.Version), SnapshotType: selection.SnapshotType,
	})
	if err != nil {
		return CaptureResult{}, fmt.Errorf("resource capture: encode persisted source metadata: %w", err)
	}
	config, err := captureConfig(resolved, selection.SnapshotType, metadata, capturer.taskImage)
	if err != nil {
		return CaptureResult{}, err
	}
	canonical, err := config.CanonicalJSON()
	if err != nil {
		return CaptureResult{}, fmt.Errorf("resource capture: canonicalize persisted template: %w", err)
	}
	spec, err := newTemplateSpec(resolved, selection.CaptureOperationKey, config, canonical)
	if err != nil {
		return CaptureResult{}, err
	}
	template, err := capturer.templates.SaveOrReuse(ctx, spec)
	if err != nil {
		return CaptureResult{}, fmt.Errorf("resource capture: save persisted immutable template: %w", err)
	}
	if template.ID <= 0 || template.Name != spec.Name {
		return CaptureResult{}, fmt.Errorf("resource capture: persisted selection template identity mismatch")
	}
	executionRequest := ExecutionRequest{
		TeamID: resolved.TeamID, TeamName: resolved.TeamName, OperationKey: selection.CaptureOperationKey,
		Template: template, CreatedBy: "system:workflow-resource-source",
	}
	for handoffs := 0; handoffs < 4; handoffs++ {
		execution, created, err := capturer.executions.StartOrGet(ctx, executionRequest)
		if err != nil {
			return CaptureResult{}, fmt.Errorf("resource capture: start persisted execution: %w", err)
		}
		if err := execution.validate(); err != nil || execution.TemplatePipelineID != template.ID {
			return CaptureResult{}, fmt.Errorf("%w: persisted capture execution drifted", ErrPersistedSelectionDrift)
		}
		result := CaptureResult{OperationKey: selection.CaptureOperationKey, Created: created, Execution: execution}
		if execution.Status == db.PipelineRunFailed || execution.Status == db.PipelineRunErrored || execution.Status == db.PipelineRunAborted {
			if executionRequest.RetryPipelineRunID == 0 {
				executionRequest.RetryPipelineRunID = execution.PipelineRunID
				continue
			}
			return result, nil
		}
		if execution.Status != db.PipelineRunSucceeded {
			return result, nil
		}
		manifest, _, err := capturer.outputs.Finalize(ctx, OutputRequest{
			TeamID: resolved.TeamID, TeamName: resolved.TeamName, PipelineRunID: execution.PipelineRunID,
			OperationKey: selection.CaptureOperationKey, OutputPort: outputPort,
			ExpectedType: selection.SnapshotType, Actor: "system:workflow-resource-source",
		})
		if errors.Is(err, ErrOutputUnavailable) {
			executionRequest.RetryPipelineRunID = execution.PipelineRunID
			continue
		}
		if err != nil {
			return CaptureResult{}, fmt.Errorf("resource capture: finalize persisted output: %w", err)
		}
		if manifest.Type != selection.SnapshotType {
			return CaptureResult{}, fmt.Errorf("%w: persisted output type changed", ErrPersistedSelectionDrift)
		}
		result.Snapshot = &manifest
		return result, nil
	}
	return CaptureResult{}, fmt.Errorf("resource capture: persisted retry generations did not converge")
}

func verifyPersistedSelection(selection PersistedSelection, resolved ResolvedResource) error {
	if selection.TeamID != resolved.TeamID || selection.TeamName != resolved.TeamName ||
		selection.SourcePipelineID != resolved.PipelineID || selection.PipelineID != resolved.PipelineID ||
		!reflect.DeepEqual(selection.Pipeline, resolved.Pipeline) ||
		selection.PipelineConfigVersion != resolved.PipelineConfigVersion ||
		selection.ResourceID != resolved.ResourceID ||
		selection.ResourceConfigVersionID != resolved.ResourceConfigVersionID ||
		selection.ResourceVersionID != resolved.ResourceVersionID ||
		!reflect.DeepEqual(selection.Version, resolved.Version) {
		return ErrPersistedSelectionDrift
	}
	if err := validateVersionDigest(selection.VersionDigest, resolved.Version); err != nil {
		return fmt.Errorf("%w: %v", ErrPersistedSelectionDrift, err)
	}
	for _, value := range []struct{ expected, actual any }{
		{selection.Resource, resolved.Resource}, {selection.ResourceTypes, resolved.ResourceTypes},
	} {
		expected, expectedErr := atc.CanonicalJSON(value.expected)
		actual, actualErr := atc.CanonicalJSON(value.actual)
		if expectedErr != nil || actualErr != nil || !bytes.Equal(expected, actual) {
			return ErrPersistedSelectionDrift
		}
	}
	return nil
}

func validateVersionDigest(digest string, version atc.Version) error {
	encoded, err := json.Marshal(version)
	if err != nil {
		return err
	}
	switch len(digest) {
	case md5.Size * 2:
		sum := md5.Sum(encoded) // #nosec G401 -- validates legacy persisted Concourse digests.
		if digest == hex.EncodeToString(sum[:]) {
			return nil
		}
	case sha256.Size * 2:
		sum := sha256.Sum256(encoded)
		if digest == hex.EncodeToString(sum[:]) {
			return nil
		}
	default:
		return fmt.Errorf("version digest is malformed")
	}
	return fmt.Errorf("version JSON does not match its persisted digest")
}

var sourceCaptureOperationKeyPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func captureIdentity(resolved ResolvedResource, snapshotType snapshot.TypeRef, taskImage string) (string, string, error) {
	resourceJSON, err := atc.CanonicalJSON(resolved.Resource)
	if err != nil {
		return "", "", fmt.Errorf("resource capture: canonicalize resource config: %w", err)
	}
	resourceDigest := sha256.Sum256(resourceJSON)
	resourceHash := hex.EncodeToString(resourceDigest[:])
	resourceTypesJSON, err := atc.CanonicalJSON(resolved.ResourceTypes)
	if err != nil {
		return "", "", fmt.Errorf("resource capture: canonicalize resource types: %w", err)
	}
	resourceTypesDigest := sha256.Sum256(resourceTypesJSON)
	identity := struct {
		Domain                  string
		CanonicalJSONVersion    int
		TeamID                  int
		TeamName                string
		Pipeline                atc.PipelineRef
		PipelineConfigVersion   int
		Resource                string
		ResourceConfigVersionID int
		ResourceVersionID       int
		ResourceConfigHash      string
		ResourceTypesHash       string
		Version                 atc.Version
		SnapshotType            snapshot.TypeRef
		TaskImage               string
	}{
		Domain: "jetbridge.resource-capture.v1", CanonicalJSONVersion: atc.CanonicalJSONVersion,
		TeamID: resolved.TeamID, TeamName: resolved.TeamName, Pipeline: resolved.Pipeline,
		PipelineConfigVersion: resolved.PipelineConfigVersion, Resource: resolved.Resource.Name,
		ResourceConfigVersionID: resolved.ResourceConfigVersionID, ResourceVersionID: resolved.ResourceVersionID,
		ResourceConfigHash: resourceHash, ResourceTypesHash: hex.EncodeToString(resourceTypesDigest[:]),
		Version: resolved.Version, SnapshotType: snapshotType, TaskImage: taskImage,
	}
	canonical, err := atc.CanonicalJSON(identity)
	if err != nil {
		return "", "", fmt.Errorf("resource capture: canonicalize operation identity: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), resourceHash, nil
}

func captureConfig(resolved ResolvedResource, snapshotType snapshot.TypeRef, metadata json.RawMessage, taskImage string) (atc.Config, error) {
	image, err := registryImage(taskImage)
	if err != nil {
		return atc.Config{}, fmt.Errorf("resource capture: agent step image: %w", err)
	}
	resourceCopy, err := copystructure.Copy(resolved.Resource)
	if err != nil {
		return atc.Config{}, fmt.Errorf("resource capture: clone resource: %w", err)
	}
	typesCopy, err := copystructure.Copy(resolved.ResourceTypes)
	if err != nil {
		return atc.Config{}, fmt.Errorf("resource capture: clone resource types: %w", err)
	}
	config := atc.Config{
		Template:      true,
		Resources:     atc.ResourceConfigs{resourceCopy.(atc.ResourceConfig)},
		ResourceTypes: typesCopy.(atc.ResourceTypes),
		Jobs: atc.JobConfigs{{
			Name: captureJob, Serial: true, RawMaxInFlight: 1,
			PlanSequence: []atc.Step{
				{Config: &atc.GetStep{
					Name: "source", Resource: resolved.Resource.Name,
					Version: &atc.VersionConfig{Pinned: cloneVersion(resolved.Version)},
				}},
				{Config: &atc.TaskStep{
					Name: "seal-snapshot", FunctionID: "resource-version-capture/v1", Hermetic: true,
					Config: &atc.TaskConfig{
						Platform: "linux", ImageResource: image,
						Run:    atc.TaskRunConfig{Path: "/bin/sh", Args: []string{"-ec", `cp -a source/. snapshot/`}},
						Inputs: []atc.TaskInputConfig{{Name: "source"}}, Outputs: []atc.TaskOutputConfig{{Name: outputPort}},
					},
					SnapshotOutputs: map[string]atc.SnapshotOutputConfig{outputPort: {
						Type: snapshotType, Retention: snapshot.RetentionClassBinding,
						SourceMetadata: append(json.RawMessage(nil), metadata...),
					}},
				}},
			},
		}},
	}
	if err := config.Jobs[0].PlanSequence[1].Config.(*atc.TaskStep).Config.Validate(); err != nil {
		return atc.Config{}, err
	}
	if err := config.Jobs[0].PlanSequence[1].Config.(*atc.TaskStep).SnapshotOutputs[outputPort].Validate(); err != nil {
		return atc.Config{}, err
	}
	return config, nil
}

func registryImage(reference string) (*atc.ImageResource, error) {
	reference = strings.TrimSpace(reference)
	for _, prefix := range []string{"docker://", "raw://"} {
		reference = strings.TrimPrefix(reference, prefix)
	}
	reference = strings.TrimPrefix(reference, "/")
	if reference == "" || strings.ContainsAny(reference, " \t\r\n") {
		return nil, fmt.Errorf("invalid image reference")
	}
	source := atc.Source{}
	version := atc.Version{}
	if repository, digest, found := strings.Cut(reference, "@"); found {
		if repository == "" || digest == "" || !strings.Contains(digest, ":") {
			return nil, fmt.Errorf("invalid digest image reference %q", reference)
		}
		source["repository"] = repository
		version["digest"] = digest
	} else {
		repository, tag := reference, "latest"
		lastSlash, lastColon := strings.LastIndex(reference, "/"), strings.LastIndex(reference, ":")
		if lastColon > lastSlash {
			repository, tag = reference[:lastColon], reference[lastColon+1:]
		}
		if repository == "" || tag == "" {
			return nil, fmt.Errorf("invalid tagged image reference %q", reference)
		}
		source["repository"] = repository
		source["tag"] = tag
	}
	return &atc.ImageResource{Name: "agent-step-image", Type: "registry-image", Source: source, Version: version}, nil
}

func resourceTypeDerivesFrom(resourceType, base string, resourceTypes atc.ResourceTypes) bool {
	seen := map[string]struct{}{}
	for resourceType != "" {
		if resourceType == base {
			return true
		}
		if _, duplicate := seen[resourceType]; duplicate {
			return false
		}
		seen[resourceType] = struct{}{}
		parent, found := resourceTypes.Lookup(resourceType)
		if !found {
			return false
		}
		resourceType = parent.Type
	}
	return false
}

func cloneVersion(version atc.Version) atc.Version {
	if version == nil {
		return nil
	}
	cloned := make(atc.Version, len(version))
	for key, value := range version {
		cloned[key] = value
	}
	return cloned
}

func cloneInstanceVars(instanceVars atc.InstanceVars) atc.InstanceVars {
	if instanceVars == nil {
		return nil
	}
	if cloned, err := copystructure.Copy(instanceVars); err == nil {
		return cloned.(atc.InstanceVars)
	}
	return instanceVars
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("resource capture: context is required")
	}
	return ctx.Err()
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
