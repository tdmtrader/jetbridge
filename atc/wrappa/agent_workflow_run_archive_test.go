package wrappa_test

import (
	"testing"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/pipelineserver"
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
		atc.CreateAgentNodeRun,
		atc.ListAgentNodeRuns,
		atc.GetAgentNodeRun,
		atc.CancelAgentNodeRun,
		atc.ListAgentNodeConsumers,
		atc.UpgradeAgentNodeConsumers,
		atc.CreateAgentWorkflowRun,
		atc.ListAgentWorkflowRuns,
		atc.GetAgentWorkflowRunOperationalStatusCounts,
		atc.GetAgentWorkflowRun,
		atc.CancelAgentWorkflowRun,
		atc.RetryAgentWorkflowRun,
		atc.GetAgentWorkflowRunOutputs,
		atc.GetAgentWorkflowRunGraph,
	}
	// This route authorizes against the team named in the request, so the handler
	// never reaches a db factory. nil is deliberate: if that ever changes, the
	// test panics instead of quietly reading a zero value.
	factory := pipelineserver.NewRejectArchivedHandlerFactory(nil)
	wrapper := wrappa.NewRejectArchivedWrappa(factory)
	for _, route := range routes {
		delegate := &stupidHandler{}
		wrapped := wrapper.Wrap(rata.Handlers{route: delegate})
		if wrapped[route] != delegate {
			t.Errorf("route %q was incorrectly scoped to an archived pipeline", route)
		}
	}
}
