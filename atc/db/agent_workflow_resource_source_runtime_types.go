package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

// AgentWorkflowResourceSourceAdmissionMode records whether an admission came
// from an ordinary standing-pipeline build or a server-created manual admit
// build. It is durable evidence, not a caller-controlled execution option.
type AgentWorkflowResourceSourceAdmissionMode string

type AgentWorkflowResourceSourcePipelineState string

var (
	ErrAgentWorkflowResourceSourceConflict  = errors.New("db: workflow resource source conflict")
	ErrAgentWorkflowResourceSourceInactive  = errors.New("db: workflow resource source pipeline is not active")
	ErrAgentWorkflowResourceSourceNotReady  = errors.New("db: workflow resource source admission is not ready")
	ErrAgentWorkflowResourceSourceImmutable = errors.New("db: workflow resource source pipeline is server-owned and immutable")
)

const (
	AgentWorkflowResourceSourceAdmissionManual    AgentWorkflowResourceSourceAdmissionMode = "manual"
	AgentWorkflowResourceSourceAdmissionAutomatic AgentWorkflowResourceSourceAdmissionMode = "automatic"
)

// The resource-source registration is the durable pipeline identity. Lookup
// and admission paths are definition-owned, and the shared lifecycle projects
// registrations so a newly committed one can be activated by the existing
// tick. Keep this alias at the runtime seam so consumers cannot accidentally
// introduce a second registration model.
type AgentWorkflowResourceSourcePipeline = WorkflowResourceSourcePipeline

type AgentWorkflowResourceSourcePipelineLifecycle struct {
	AgentWorkflowResourceSourcePipeline
	Paused                bool
	Archived              bool
	InFlightBuilds        int
	NonterminalAdmissions int
}

func (mode AgentWorkflowResourceSourceAdmissionMode) Validate() error {
	if mode != AgentWorkflowResourceSourceAdmissionManual && mode != AgentWorkflowResourceSourceAdmissionAutomatic {
		return fmt.Errorf("db: invalid workflow resource source admission mode %q", mode)
	}
	return nil
}

type AgentWorkflowResourceSourceAdmissionStatus string

const (
	AgentWorkflowResourceSourceAdmissionSelecting AgentWorkflowResourceSourceAdmissionStatus = "selecting"
	AgentWorkflowResourceSourceAdmissionCapturing AgentWorkflowResourceSourceAdmissionStatus = "capturing"
	AgentWorkflowResourceSourceAdmissionReady     AgentWorkflowResourceSourceAdmissionStatus = "ready"
	AgentWorkflowResourceSourceAdmissionFailed    AgentWorkflowResourceSourceAdmissionStatus = "failed"
)

func (status AgentWorkflowResourceSourceAdmissionStatus) Validate() error {
	switch status {
	case AgentWorkflowResourceSourceAdmissionSelecting,
		AgentWorkflowResourceSourceAdmissionCapturing,
		AgentWorkflowResourceSourceAdmissionReady,
		AgentWorkflowResourceSourceAdmissionFailed:
		return nil
	default:
		return fmt.Errorf("db: invalid workflow resource source admission status %q", status)
	}
}

const (
	AgentWorkflowResourceSourcePipelineActive   AgentWorkflowResourceSourcePipelineState = "active"
	AgentWorkflowResourceSourcePipelineDraining AgentWorkflowResourceSourcePipelineState = "draining"
	AgentWorkflowResourceSourcePipelineArchived AgentWorkflowResourceSourcePipelineState = "archived"
)

func (state AgentWorkflowResourceSourcePipelineState) Validate() error {
	switch state {
	case AgentWorkflowResourceSourcePipelineActive,
		AgentWorkflowResourceSourcePipelineDraining,
		AgentWorkflowResourceSourcePipelineArchived:
		return nil
	default:
		return fmt.Errorf("db: invalid workflow resource source pipeline state %q", state)
	}
}

// AgentWorkflowResourceSourceAdmission is the server-owned selection/capture
// record. The selection is always derived from a persisted build input row.
type AgentWorkflowResourceSourceAdmission struct {
	ID                   int64
	TeamID               int
	WorkflowDefinitionID int
	SourcePipelineID     int
	SourceConfigHash     string
	IdempotencyKey       string
	Mode                 AgentWorkflowResourceSourceAdmissionMode
	SelectingBuildID     *int64
	Status               AgentWorkflowResourceSourceAdmissionStatus
	ErrorMessage         string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type ManualAdmissionIdentity struct {
	WorkflowDefinitionID int
	SourcePipelineID     int
	SourceConfigHash     string
	IdempotencyKey       string
}

func (identity ManualAdmissionIdentity) Validate() error {
	if identity.WorkflowDefinitionID <= 0 || identity.SourcePipelineID <= 0 ||
		!lowerHex64.MatchString(identity.SourceConfigHash) ||
		strings.TrimSpace(identity.IdempotencyKey) == "" ||
		len([]byte(identity.IdempotencyKey)) > 512 {
		return fmt.Errorf("db: invalid manual workflow resource source admission identity")
	}
	return nil
}

type BuildClaim struct {
	WorkflowDefinitionID int
	SourceConfigHash     string
	IdempotencyKey       string
}

func (claim BuildClaim) Validate() error {
	if claim.WorkflowDefinitionID <= 0 || !lowerHex64.MatchString(claim.SourceConfigHash) ||
		strings.TrimSpace(claim.IdempotencyKey) == "" || len([]byte(claim.IdempotencyKey)) > 512 {
		return fmt.Errorf("db: invalid automatic workflow resource source admission claim")
	}
	return nil
}

// SelectedSource is an exact selected build input. Version and digest are
// persisted Concourse evidence; no latest-selection operation exists here.
type SelectedSource struct {
	SourceName              string
	ResourceName            string
	SelectingBuildID        int64
	ResourceID              int
	ResourceConfigVersionID int64
	ResourceVersionID       int64
	VersionDigest           string
	Version                 atc.Version
	SnapshotType            snapshot.TypeRef
	CaptureOperationKey     string
}

func (selected SelectedSource) Validate() error {
	if strings.TrimSpace(selected.SourceName) == "" ||
		strings.TrimSpace(selected.ResourceName) == "" ||
		selected.ResourceID <= 0 || selected.ResourceConfigVersionID <= 0 ||
		selected.ResourceVersionID <= 0 ||
		selected.ResourceConfigVersionID != selected.ResourceVersionID ||
		!validResourceSourceVersionDigest(selected.VersionDigest) ||
		len(selected.Version) == 0 || selected.SnapshotType.Validate() != nil ||
		!lowerHex64.MatchString(selected.CaptureOperationKey) {
		return fmt.Errorf("db: invalid persisted workflow resource source selection")
	}
	return nil
}

type SourceBuild struct {
	ID                int
	PipelineID        int
	JobID             int
	TeamID            int
	ManuallyTriggered bool
	AdmissionID       *int64
	AdmissionMode     AgentWorkflowResourceSourceAdmissionMode
	AdmissionStatus   AgentWorkflowResourceSourceAdmissionStatus
}

// WorkflowResourceSourceBuildStore is the server-only bridge to ordinary
// Concourse scheduler builds. Its mapping method reads adopted input rows; it
// never performs candidate or latest-version selection.
type WorkflowResourceSourceBuildStore interface {
	EnsureManualBuild(context.Context, int, int64, int) (int, bool, error)
	RequestPostDispatchChecks(context.Context, int, int, int) error
	SuccessfulUnclaimedBuilds(context.Context, int, int) ([]SourceBuild, error)
	ExactInputMapping(context.Context, int, int, int) ([]SelectedSource, bool, error)
}

type AgentWorkflowResourceSourceBinding struct {
	AdmissionID             int64
	SourceName              string
	ResourceName            string
	SelectingBuildID        int64
	SourcePipelineID        int
	PipelineConfigVersion   int
	ResourceID              int
	ResourceConfigVersionID int64
	ResourceVersionID       int64
	VersionDigest           string
	Version                 atc.Version
	SnapshotType            snapshot.TypeRef
	CaptureOperationKey     string
	SnapshotID              *snapshot.SnapshotID
	CreatedAt               time.Time
	CapturedAt              *time.Time
}

type ReadySourceAdmission struct {
	Admission AgentWorkflowResourceSourceAdmission
	Bindings  []AgentWorkflowResourceSourceBinding
}

type CapturingSourceAdmission struct {
	Admission            AgentWorkflowResourceSourceAdmission
	PipelineRegistration WorkflowResourceSourcePipeline
	TeamName             string
	Pipeline             atc.PipelineRef
	Bindings             []AgentWorkflowResourceSourceBinding
	Resources            atc.ResourceConfigs
	ResourceTypes        atc.ResourceTypes
}

// WorkflowResourceSourceAdmissionStore is deliberately selection-oriented:
// implementations cannot ask Concourse to calculate a latest version.
type WorkflowResourceSourceAdmissionStore interface {
	CreateManual(context.Context, int, ManualAdmissionIdentity) (AgentWorkflowResourceSourceAdmission, bool, error)
	ClaimBuild(context.Context, int, int, int64, BuildClaim) (AgentWorkflowResourceSourceAdmission, bool, error)
	BindSelection(context.Context, int, int64, int64, []SelectedSource) (bool, error)
	BindCapture(context.Context, int, int64, string, snapshot.SnapshotID) (bool, error)
	Ready(context.Context, int, int64) (ReadySourceAdmission, bool, error)
	Capturing(context.Context, int, int64) (CapturingSourceAdmission, bool, error)
}
