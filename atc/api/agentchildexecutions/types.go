package agentchildexecutions

import (
	"fmt"
	"strings"
)

func (scope Scope) Validate() error {
	if scope.TeamID <= 0 || scope.BuildID <= 0 || scope.WorkflowDefinitionID <= 0 || scope.WorkflowRunID <= 0 || scope.ParentAttempt <= 0 || strings.TrimSpace(scope.TeamName) == "" || strings.TrimSpace(scope.SnapshotCreatedBy) == "" || strings.TrimSpace(scope.NodePlanID) == "" || strings.TrimSpace(scope.BrokerInstance) == "" || scope.LeaseDuration <= 0 || len(scope.Inputs) == 0 && scope.WorkspaceBase == nil {
		return fmt.Errorf("complete agent child execution scope is required")
	}
	for name, ref := range scope.Inputs {
		if strings.TrimSpace(name) == "" || ref.Validate() != nil {
			return fmt.Errorf("complete immutable agent child input authority is required")
		}
	}
	if scope.WorkspaceBase != nil {
		if scope.WorkspaceBase.Validate() != nil || scope.WorkspaceBase.Type != "repository/v1" {
			return fmt.Errorf("workspace base must be an exact repository/v1 snapshot")
		}
	}
	return nil
}
