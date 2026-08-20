package atccmd

import (
	"time"

	"github.com/concourse/concourse/atc/db"
)

// ValidateK8sRuntimeForTest exports the private validateK8sRuntime method for
// external test packages. See command_test.go.
func ValidateK8sRuntimeForTest(cmd *RunCommand) error {
	return cmd.validateK8sRuntime()
}

func NewPipelineRunReclaimerComponentForTest(lifecycle db.PipelineRunReclaimLifecycle, now func() time.Time) RunnableComponent {
	return newPipelineRunReclaimerComponent(lifecycle, now)
}
