package reviews_test

import (
	"testing"

	"github.com/concourse/concourse/atc"
)

func TestCanonicalReviewRoutesRegisteredExactlyOnce(t *testing.T) {
	required := map[string]struct {
		method string
		path   string
	}{
		atc.GetAgentSnapshotReview: {
			method: "GET",
			path:   "/api/v1/agent/snapshots/:snapshot_id/projections/review",
		},
		atc.ListAgentWorkflowRunReviews: {
			method: "GET",
			path:   "/api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/reviews",
		},
	}

	counts := make(map[string]int, len(required))
	for _, route := range atc.Routes {
		want, relevant := required[route.Name]
		if !relevant {
			continue
		}
		counts[route.Name]++
		if route.Method != want.method || route.Path != want.path {
			t.Errorf("route %q = %s %s, want %s %s", route.Name, route.Method, route.Path, want.method, want.path)
		}
	}
	for name := range required {
		if counts[name] != 1 {
			t.Errorf("route %q registered %d times, want exactly once", name, counts[name])
		}
	}
}
