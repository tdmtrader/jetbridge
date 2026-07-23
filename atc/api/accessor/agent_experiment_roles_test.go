package accessor

import (
	"testing"

	"github.com/concourse/concourse/atc"
)

func TestAgentExperimentRoutesHaveExplicitMainTeamRoles(t *testing.T) {
	want := map[string]string{
		atc.CreateAgentExperiment: MemberRole, atc.ListAgentExperiments: ViewerRole,
		atc.GetAgentExperiment: ViewerRole, atc.UpdateAgentExperiment: MemberRole,
		atc.ValidateAgentExperiment: ViewerRole, atc.StartAgentExperiment: MemberRole,
		atc.CancelAgentExperiment: MemberRole, atc.ListAgentExperimentCells: ViewerRole,
		atc.GetAgentExperimentCell: ViewerRole, atc.GetAgentExperimentScorecard: ViewerRole,
	}
	for route, role := range want {
		if got, found := DefaultRoles[route]; !found || got != role {
			t.Errorf("DefaultRoles[%q] = %q, %v; want %q, true", route, got, found, role)
		}
	}
}
