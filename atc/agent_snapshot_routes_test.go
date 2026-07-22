package atc

import "testing"

func TestAgentSnapshotRoutesAreRegisteredExactlyOnce(t *testing.T) {
	want := map[string]struct {
		method string
		path   string
	}{
		CreateAgentSnapshot:   {"POST", "/api/v1/teams/:team_name/agent/snapshots"},
		ListAgentSnapshots:    {"GET", "/api/v1/teams/:team_name/agent/snapshots"},
		GetAgentSnapshot:      {"GET", "/api/v1/teams/:team_name/agent/snapshots/:snapshot_id"},
		DownloadAgentSnapshot: {"GET", "/api/v1/teams/:team_name/agent/snapshots/:snapshot_id/content"},
		PinAgentSnapshot:      {"PUT", "/api/v1/teams/:team_name/agent/snapshots/:snapshot_id/pin"},
		UnpinAgentSnapshot:    {"DELETE", "/api/v1/teams/:team_name/agent/snapshots/:snapshot_id/pin"},
	}
	seen := make(map[string]int, len(want))
	for _, route := range Routes {
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
