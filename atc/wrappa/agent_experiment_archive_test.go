package wrappa_test

import (
	"testing"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/wrappa"
	"github.com/tedsuo/rata"
)

func TestAgentExperimentRoutesAreNotRejectedForArchivedPipelines(t *testing.T) {
	routes := []string{
		atc.CreateAgentExperiment, atc.ListAgentExperiments, atc.GetAgentExperiment,
		atc.UpdateAgentExperiment, atc.ValidateAgentExperiment, atc.StartAgentExperiment,
		atc.CancelAgentExperiment, atc.ListAgentExperimentCells, atc.GetAgentExperimentCell,
		atc.GetAgentExperimentScorecard,
	}
	// This route authorizes against the team named in the request, so the handler
	// never reaches a db factory. nil is deliberate: if that ever changes, the
	// test panics instead of quietly reading a zero value.
	wrapper := wrappa.NewRejectArchivedWrappa(pipelineserver.NewRejectArchivedHandlerFactory(nil))
	for _, route := range routes {
		delegate := &stupidHandler{}
		wrapped := wrapper.Wrap(rata.Handlers{route: delegate})
		if wrapped[route] != delegate {
			t.Errorf("route %q was incorrectly scoped to an archived pipeline", route)
		}
	}
}
