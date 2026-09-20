package artifactwire

import "testing"

// The route table is the daemon's route table: seventeen routes, no two with
// the same pattern, and the mTLS-exempt set is exactly the six a caller with
// no certificate has to reach.
func TestRouteTable(t *testing.T) {
	routes := Routes()
	if len(routes) != 17 {
		t.Fatalf("expected 17 routes, got %d", len(routes))
	}
	seen := map[string]bool{}
	for _, r := range routes {
		if seen[r.Pattern()] {
			t.Errorf("pattern %q registered twice", r.Pattern())
		}
		seen[r.Pattern()] = true
	}

	wantExempt := map[string]bool{
		"GET /healthz":                        true,
		"POST /resolve":                       true,
		"POST /resolve-batch":                 true,
		"GET /capture-held/steps/{handle...}": true,
		"GET /metrics":                        true,
		"POST /hangar/v1/materializations":    true,
	}
	for _, r := range routes {
		if r.MTLSExempt != wantExempt[r.Pattern()] {
			t.Errorf("%s: MTLSExempt=%v, want %v", r.Pattern(), r.MTLSExempt, wantExempt[r.Pattern()])
		}
	}
	if got := len(wantExempt); got != 6 {
		t.Fatalf("the exempt set in this test has %d entries, want 6", got)
	}
}

// The prefixes a key is appended to are the paths the prefix routes register,
// so a URL built from one lands on the handler registered for the other.
func TestPrefixesMatchRoutes(t *testing.T) {
	for _, tc := range []struct {
		prefix string
		route  Route
	}{
		{ArtifactsPrefix, GetArtifact},
		{ArtifactsPrefix, DeleteArtifact},
		{StreamInPrefix, StreamIn},
		{ResourceCachesPrefix, HeadResourceCache},
	} {
		if tc.prefix != tc.route.Path {
			t.Errorf("prefix %q does not match route %s", tc.prefix, tc.route.Pattern())
		}
	}
	if CaptureHeld.Path != CaptureHeldStepsPrefix+"{handle...}" {
		t.Errorf("capture-held route %q is not the prefix plus a handle wildcard", CaptureHeld.Path)
	}
}
