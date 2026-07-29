package atccmd

// ValidateK8sRuntimeForTest exports the private validateK8sRuntime method for
// external test packages. See command_test.go.
func ValidateK8sRuntimeForTest(cmd *RunCommand) error {
	return cmd.validateK8sRuntime()
}

func ValidateAgentSnapshotsForTest(cmd *RunCommand) error {
	return cmd.validateAgentSnapshots()
}

func ValidateAgentCheckpointsForTest(cmd *RunCommand) error {
	return cmd.validateAgentCheckpoints()
}

func ValidateAgentWorkflowRunsForTest(cmd *RunCommand) error {
	return cmd.validateAgentWorkflowRuns()
}

func ValidateAgentPublisherGatewayForTest(cmd *RunCommand) error {
	return cmd.validateAgentPublisherGateway()
}

func ValidateGarbageCollectionForTest(cmd *RunCommand) error {
	return cmd.validateGarbageCollection()
}
