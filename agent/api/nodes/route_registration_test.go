package nodes_test

import (
	"testing"

	"github.com/concourse/concourse/atc"
)

func TestNodeRoutesRegistered(t *testing.T) {
	want := map[string]struct{ method, path string }{
		atc.ListAgentNodes:            {"GET", "/api/v1/agent/nodes"},
		atc.ListAgentNodeVersions:     {"GET", "/api/v1/agent/nodes/:node_name/versions"},
		atc.GetAgentNodeVersion:       {"GET", "/api/v1/agent/nodes/:node_name/versions/:version"},
		atc.CreateAgentNodeVersion:    {"POST", "/api/v1/agent/nodes/:node_name/versions"},
		atc.ReleaseAgentNodeVersion:   {"PUT", "/api/v1/agent/nodes/:node_name/versions/:version/release"},
		atc.DeprecateAgentNodeVersion: {"PUT", "/api/v1/agent/nodes/:node_name/versions/:version/deprecation"},
	}
	for name, expected := range want {
		found := false
		for _, route := range atc.Routes {
			if route.Name == name {
				found = true
				if route.Method != expected.method || route.Path != expected.path {
					t.Errorf("route %q = %s %s, want %s %s", name, route.Method, route.Path, expected.method, expected.path)
				}
			}
		}
		if !found {
			t.Errorf("route %q not registered", name)
		}
	}
}
