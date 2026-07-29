package workflowrun

import (
	"context"
	"fmt"

	"github.com/concourse/concourse/atc/db"
)

// SourceManualAdmission creates the durable admission before it asks the
// ordinary Concourse admit job to create its manual build. The build remains
// the sole source-version selector; retries reuse both durable identities.
type SourceManualAdmission struct {
	teamID     int
	admissions db.WorkflowResourceSourceAdmissionStore
	builds     SourceBuildStore
}

func NewSourceManualAdmission(
	trustedTeamID int,
	admissions db.WorkflowResourceSourceAdmissionStore,
	builds SourceBuildStore,
) (*SourceManualAdmission, error) {
	if trustedTeamID <= 0 || nilInterface(admissions) || nilInterface(builds) {
		return nil, fmt.Errorf("workflowrun: manual source admission dependencies are required")
	}
	return &SourceManualAdmission{teamID: trustedTeamID, admissions: admissions, builds: builds}, nil
}

func (service *SourceManualAdmission) Admit(
	ctx context.Context,
	identity db.ManualAdmissionIdentity,
) (db.AgentWorkflowResourceSourceAdmission, error) {
	if ctx == nil {
		return db.AgentWorkflowResourceSourceAdmission{}, fmt.Errorf("workflowrun: manual source admission context is required")
	}
	admission, _, err := service.admissions.CreateManual(ctx, service.teamID, identity)
	if err != nil {
		return db.AgentWorkflowResourceSourceAdmission{}, err
	}
	buildID, _, err := service.builds.EnsureManualBuild(ctx, service.teamID, admission.ID, identity.SourcePipelineID)
	if err != nil {
		return db.AgentWorkflowResourceSourceAdmission{}, err
	}
	if err := service.builds.RequestPostDispatchChecks(ctx, service.teamID, identity.SourcePipelineID, buildID); err != nil {
		return db.AgentWorkflowResourceSourceAdmission{}, err
	}
	selected := int64(buildID)
	if admission.SelectingBuildID != nil && *admission.SelectingBuildID != selected {
		return db.AgentWorkflowResourceSourceAdmission{}, fmt.Errorf("workflowrun: manual source admission selecting build changed")
	}
	admission.SelectingBuildID = &selected
	return admission, nil
}

var _ resourceSourceManualAdmitter = (*SourceManualAdmission)(nil)
