package main

import (
	"strings"
	"testing"
)

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

// All three TLS flags is mTLS and none is plaintext; anything between used to
// serve plaintext silently and is now refused, naming what is missing.
func TestDaemonTLSMode(t *testing.T) {
	const cert, key, ca = "/tls/tls.crt", "/tls/tls.key", "/tls/ca.crt"
	cases := []struct {
		name          string
		cert, key, ca string
		wantTLS       bool
		wantMissing   []string // empty: no error expected
	}{
		{"none: plaintext", "", "", "", false, nil},
		{"all: mTLS", cert, key, ca, true, nil},
		{"cert only", cert, "", "", false, []string{"--tls-key", "--tls-ca-cert"}},
		{"key only", "", key, "", false, []string{"--tls-cert", "--tls-ca-cert"}},
		{"ca only", "", "", ca, false, []string{"--tls-cert", "--tls-key"}},
		{"cert and key, no ca", cert, key, "", false, []string{"--tls-ca-cert"}},
		{"cert and ca, no key", cert, "", ca, false, []string{"--tls-key"}},
		{"key and ca, no cert", "", key, ca, false, []string{"--tls-cert"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotTLS, err := daemonTLSMode(tc.cert, tc.key, tc.ca)
			if gotTLS != tc.wantTLS {
				t.Errorf("tls = %v, want %v", gotTLS, tc.wantTLS)
			}
			if len(tc.wantMissing) == 0 {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("partial TLS triple accepted; it must be refused")
			}
			for _, flag := range tc.wantMissing {
				if !strings.Contains(err.Error(), flag) {
					t.Errorf("error %q does not name missing %s", err, flag)
				}
			}
		})
	}
}
