package atccmd

import (
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
)

// ValidateK8sRuntimeForTest exports the private validateK8sRuntime method for
// external test packages. See command_test.go.
func ValidateK8sRuntimeForTest(cmd *RunCommand) error {
	return cmd.validateK8sRuntime()
}

func NewPipelineRunReclaimerComponentForTest(lifecycle db.PipelineRunReclaimLifecycle, now func() time.Time, batchSize int) RunnableComponent {
	return newPipelineRunReclaimerComponent(lifecycle, now, batchSize)
}

// GCComponentsForTest exports the private gcComponents method so a spec can
// assert what the parsed flags actually reach. Every constructor it calls only
// stores its collaborators, so a nil connection and lock factory are enough to
// build the component list; nothing here runs a component.
func GCComponentsForTest(cmd *RunCommand, logger lager.Logger, gcConn db.DbConn, lockFactory lock.LockFactory) ([]RunnableComponent, error) {
	return cmd.gcComponents(logger, gcConn, lockFactory)
}

// ValidateCustomRolesForTest exports the private validateCustomRoles method for
// external test packages. See command_test.go.
func ValidateCustomRolesForTest(cmd *RunCommand) error {
	return cmd.validateCustomRoles()
}
