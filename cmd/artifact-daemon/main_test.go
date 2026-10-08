package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
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

// The Hangar key is the output plane's and nobody else's: required exactly
// when --execution-control mounts the plane, refused when nothing on the
// daemon verifies a warrant, and the refusal names the flag that decides it.
func TestLoadHangarKeyIsRequiredWithAndOnlyWithExecutionControl(t *testing.T) {
	if key, err := loadHangarKey("", false); err != nil || key != nil {
		t.Errorf("no key and no plane = (%v, %v), want (nil, nil)", key, err)
	}
	if _, err := loadHangarKey("", true); err == nil || !strings.Contains(err.Error(), "--execution-control") {
		t.Errorf("a plane with no key was accepted, or the refusal does not name the flag: %v", err)
	}
	if _, err := loadHangarKey("/nonexistent/key", false); err == nil || !strings.Contains(err.Error(), "--execution-control") {
		t.Errorf("a key with no plane was accepted, or the refusal does not name the flag: %v", err)
	}
}

// Shutdown runs in the reverse of startup: the readiness label comes off
// before the listener closes, so the scheduler stops sending pods to a node
// that is about to stop answering, and the output plane's store client closes
// last, after the requests that were using it have drained.
func TestCleanupRemovesTheLabelThenShutsDownThenClosesThePlane(t *testing.T) {
	const labelKey = "concourse.dev/artifact-cache"
	client := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", Labels: map[string]string{labelKey: "ready"}}})
	var order []string
	client.PrependReactor("patch", "nodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		var patch struct {
			Metadata struct {
				Labels map[string]any `json:"labels"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(action.(k8stesting.PatchAction).GetPatch(), &patch); err != nil {
			t.Fatal(err)
		}
		for key := range patch.Metadata.Labels {
			order = append(order, key)
		}
		return false, nil, nil
	})
	labeler := NewNodeLabeler(lagertest.NewTestLogger("legacy-label"), client, "node", labelKey)
	if err := cleanupDaemonServices(context.Background(), labeler,
		func() error { order = append(order, "shutdown"); return nil },
		func() error { order = append(order, "close"); return nil }); err != nil {
		t.Fatal(err)
	}
	node, _ := client.CoreV1().Nodes().Get(context.Background(), "node", metav1.GetOptions{})
	wantOrder := []string{labelKey, "shutdown", "close"}
	if len(node.Labels) != 0 || !slices.Equal(order, wantOrder) {
		t.Fatalf("cleanup left labels=%v order=%v, want %v", node.Labels, order, wantOrder)
	}

	// A daemon that never got that far passes nothing, and cleanup still runs.
	if err := cleanupDaemonServices(context.Background(), nil, nil, nil); err != nil {
		t.Fatalf("cleanup with nothing to clean up: %v", err)
	}
}
