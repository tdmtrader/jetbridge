package atccmd

// ValidateK8sRuntimeForTest exports the private validateK8sRuntime method for
// external test packages. See command_test.go.
func ValidateK8sRuntimeForTest(cmd *RunCommand) error {
	return cmd.validateK8sRuntime()
}
