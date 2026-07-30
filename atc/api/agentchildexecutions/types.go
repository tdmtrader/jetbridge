package agentchildexecutions

import (
	"fmt"
	"strings"
)

func (scope Scope) Validate() error {
	if scope.TeamID <= 0 || scope.BuildID <= 0 || scope.WorkflowDefinitionID <= 0 || scope.WorkflowRunID <= 0 || scope.ParentAttempt <= 0 || strings.TrimSpace(scope.TeamName) == "" || strings.TrimSpace(scope.SnapshotCreatedBy) == "" || strings.TrimSpace(scope.NodePlanID) == "" || strings.TrimSpace(scope.BrokerInstance) == "" || scope.LeaseDuration <= 0 || len(scope.Inputs) == 0 {
		return fmt.Errorf("complete agent child execution scope is required")
	}
	for name, ref := range scope.Inputs {
		if strings.TrimSpace(name) == "" || ref.Validate() != nil {
			return fmt.Errorf("complete immutable agent child input authority is required")
		}
	}
	return nil
}
