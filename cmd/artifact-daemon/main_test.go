package main

import "testing"

// --peer-discovery exists so a daemon under a namespace-scoped
// ServiceAccount can find its peers without --node-name, which would also
// try to patch the node. The client must be built for either flag and for
// neither flag not at all.
func TestDaemonClientNeeded(t *testing.T) {
	cases := []struct {
		name          string
		nodeName      string
		peerDiscovery bool
		want          bool
	}{
		{"neither flag: no client", "", false, false},
		{"node name alone: labeling needs the client", "node-a", false, true},
		{"peer discovery alone: peers need the client", "", true, true},
		{"both: still one client", "node-a", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := daemonClientNeeded(tc.nodeName, tc.peerDiscovery); got != tc.want {
				t.Errorf("daemonClientNeeded(%q, %v) = %v, want %v", tc.nodeName, tc.peerDiscovery, got, tc.want)
			}
		})
	}
}
