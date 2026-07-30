package agentchildexecutions

import (
	"fmt"
	"strings"
)

func (scope Scope) Validate() error {
	if scope.TeamID <= 0 || scope.WorkflowRunID <= 0 || scope.ParentAttempt <= 0 || strings.TrimSpace(scope.NodePlanID) == "" || strings.TrimSpace(scope.BrokerInstance) == "" || scope.LeaseDuration <= 0 {
		return fmt.Errorf("complete agent child execution scope is required")
	}
	return nil
}
