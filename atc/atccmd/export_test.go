package atccmd

import (
	"time"

	"code.cloudfoundry.org/lager/v3"

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

// ValidateCustomRolesForTest exports the private validateCustomRoles method for
// external test packages. See command_test.go.
func ValidateCustomRolesForTest(cmd *RunCommand) error {
	return cmd.validateCustomRoles()
}

// HangarOutputComponentsForTest exports the private hangarOutputComponents
// method so a spec can assert what a deployment actually registers. The
// component constructors only store their collaborators, so a nil connection is
// enough to build the list; nothing here runs a component.
func HangarOutputComponentsForTest(cmd *RunCommand, dbConn db.DbConn) []RunnableComponent {
	return cmd.hangarOutputComponents(dbConn)
}

// HangarOutputDeleteComponentsForTest exports the constructor of the web's two
// deleting passes. Building them opens the store clients, which make no call.
func HangarOutputDeleteComponentsForTest(cmd *RunCommand) ([]RunnableComponent, error) {
	return cmd.hangarOutputDeleteComponents(nil, nil)
}

// RunComponentsForTest exports the private runComponents method: the Run
// components every web node registers, with or without an output plane. As
// above, a nil connection is enough to build the list.
func RunComponentsForTest(cmd *RunCommand, dbConn db.DbConn) []RunnableComponent {
	return cmd.runComponents(dbConn)
}

// PublishCheckIntervalsForTest exports the private publishCheckIntervals
// method, which Runner calls to set the atc.Default*Interval globals.
func PublishCheckIntervalsForTest(cmd *RunCommand, logger lager.Logger) {
	cmd.publishCheckIntervals(logger)
}
