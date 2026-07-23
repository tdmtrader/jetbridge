package experiments_test

import (
	"testing"

	"github.com/concourse/concourse/atc"
)

func TestExperimentRoutesAreRegisteredExactlyOnce(t *testing.T) {
	want := map[string]struct {
		method string
		path   string
	}{
		atc.CreateAgentExperiment:       {"POST", "/api/v1/agent/experiments"},
		atc.ListAgentExperiments:        {"GET", "/api/v1/agent/experiments"},
		atc.GetAgentExperiment:          {"GET", "/api/v1/agent/experiments/:experiment_id"},
		atc.UpdateAgentExperiment:       {"PUT", "/api/v1/agent/experiments/:experiment_id"},
		atc.ValidateAgentExperiment:     {"POST", "/api/v1/agent/experiments/:experiment_id/validate"},
		atc.StartAgentExperiment:        {"POST", "/api/v1/agent/experiments/:experiment_id/start"},
		atc.CancelAgentExperiment:       {"POST", "/api/v1/agent/experiments/:experiment_id/cancel"},
		atc.ListAgentExperimentCells:    {"GET", "/api/v1/agent/experiments/:experiment_id/cells"},
		atc.GetAgentExperimentCell:      {"GET", "/api/v1/agent/experiments/:experiment_id/cells/:cell_id"},
		atc.GetAgentExperimentScorecard: {"GET", "/api/v1/agent/experiments/:experiment_id/scorecard"},
	}
	seen := make(map[string]int, len(want))
	for _, route := range atc.Routes {
		expected, found := want[route.Name]
		if !found {
			continue
		}
		seen[route.Name]++
		if route.Method != expected.method || route.Path != expected.path {
			t.Errorf("route %q = %s %s, want %s %s", route.Name, route.Method, route.Path, expected.method, expected.path)
		}
	}
	for name := range want {
		if seen[name] != 1 {
			t.Errorf("route %q registered %d times, want exactly once", name, seen[name])
		}
	}
}
