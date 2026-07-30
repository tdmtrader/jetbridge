package nodeupgrades_test

import (
	"testing"

	"github.com/concourse/concourse/atc"
)

func TestNodeConsumerUpgradeRoutesRegisteredExactlyOnce(t *testing.T) {
	required := map[string]struct {
		method string
		path   string
	}{
		atc.ListAgentNodeConsumers: {
			method: "GET",
			path:   "/api/v1/agent/nodes/:node_name/versions/:version/consumers",
		},
		atc.UpgradeAgentNodeConsumers: {
			method: "POST",
			path:   "/api/v1/agent/nodes/:node_name/versions/:version/upgrades",
		},
	}
	counts := map[string]int{}
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
			t.Errorf("route %q registered %d times, want once", name, counts[name])
		}
	}
}
