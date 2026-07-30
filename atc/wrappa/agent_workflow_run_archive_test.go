package wrappa_test

import (
	"testing"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/wrappa"
	"github.com/tedsuo/rata"
)

func TestAgentNodeAndWorkflowRunRoutesAreNotRejectedForArchivedPipelines(t *testing.T) {
	routes := []string{
		atc.ListAgentNodes,
		atc.ListAgentNodeVersions,
		atc.GetAgentNodeVersion,
		atc.CreateAgentNodeVersion,
		atc.ReleaseAgentNodeVersion,
		atc.DeprecateAgentNodeVersion,
		atc.CreateAgentWorkflowRun,
		atc.ListAgentWorkflowRuns,
		atc.GetAgentWorkflowRunOperationalStatusCounts,
		atc.GetAgentWorkflowRun,
		atc.CancelAgentWorkflowRun,
		atc.RetryAgentWorkflowRun,
		atc.GetAgentWorkflowRunOutputs,
	}
	factory := pipelineserver.NewRejectArchivedHandlerFactory(new(dbfakes.FakeTeamFactory))
	wrapper := wrappa.NewRejectArchivedWrappa(factory)
	for _, route := range routes {
		delegate := &stupidHandler{}
		wrapped := wrapper.Wrap(rata.Handlers{route: delegate})
		if wrapped[route] != delegate {
			t.Errorf("route %q was incorrectly scoped to an archived pipeline", route)
		}
	}
}
